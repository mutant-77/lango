//go:build windows

package pty

import (
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"lango/internal/applog"
	"lango/internal/paths"

	"golang.org/x/sys/windows"
)

// Logger defines logging methods for the pty package.
type Logger interface {
	Warn(format string, args ...any)
	Info(format string, args ...any)
	Error(format string, args ...any)
	Debug(format string, args ...any)
}

// Backend defines the low-level PTY backend interface.
type Backend interface {
	Spawn(argv []string, cwd string, env []string, cols, rows int) error
	Read(p []byte) (int, error)
	Write(s string) error
	Resize(cols, rows int) error
	Alive() bool
	Pid() int
	Close()
}

type conptyAPI struct {
	create, resize, close, showHide *windows.LazyProc
}

type conptyBackend struct {
	dllDir string
	logger Logger

	mu          sync.Mutex
	api         *conptyAPI // nil = kernel32
	hPC         windows.Handle
	hInputWrite windows.Handle
	hProcess    windows.Handle
	attrList    *windows.ProcThreadAttributeListContainer

	hOutputRead windows.Handle // solo lo usa (y cierra) el lector
	pid         int
	alive       atomic.Bool
	closeOnce   sync.Once
}

// NewConptyBackend creates a Backend that loads conpty.dll from dllDir (default: exe dir).
func NewConptyBackend(dllDir string, logger Logger) Backend {
	if dllDir == "" {
		dllDir = paths.ExeDir()
	}
	if logger == nil {
		logger = applog.New("lango.pty")
	}
	return &conptyBackend{dllDir: dllDir, logger: logger}
}

func loadConptyDLL(dir string) (*conptyAPI, error) {
	dll := windows.NewLazyDLL(filepath.Join(dir, "conpty.dll"))
	if err := dll.Load(); err != nil {
		return nil, err
	}
	api := &conptyAPI{
		create:   dll.NewProc("ConptyCreatePseudoConsole"),
		resize:   dll.NewProc("ConptyResizePseudoConsole"),
		close:    dll.NewProc("ConptyClosePseudoConsole"),
		showHide: dll.NewProc("ConptyShowHidePseudoConsole"),
	}
	for _, p := range []*windows.LazyProc{api.create, api.resize, api.close, api.showHide} {
		if err := p.Find(); err != nil {
			return nil, err
		}
	}
	return api, nil
}

func coordArg(c windows.Coord) uintptr {
	return uintptr(*(*uint32)(unsafe.Pointer(&c)))
}

func buildCmdline(argv []string) string {
	var sb strings.Builder
	sb.WriteString(`"` + strings.Trim(argv[0], `"`) + `"`)
	for _, arg := range argv[1:] {
		sb.WriteString(" " + windows.EscapeArg(arg))
	}
	return sb.String()
}

func (b *conptyBackend) Spawn(argv []string, cwd string, env []string, cols, rows int) error {
	if len(argv) == 0 {
		return errors.New("argv cannot be empty")
	}
	api, loadErr := loadConptyDLL(b.dllDir)
	if loadErr != nil {
		b.logger.Warn("conpty.dll no disponible, usando kernel32: %v", loadErr)
	}

	var inRead, inWrite, outRead, outWrite windows.Handle
	if err := windows.CreatePipe(&inRead, &inWrite, nil, 128*1024); err != nil {
		return fmt.Errorf("CreatePipe in: %w", err)
	}
	if err := windows.CreatePipe(&outRead, &outWrite, nil, 128*1024); err != nil {
		windows.CloseHandle(inRead)
		windows.CloseHandle(inWrite)
		return fmt.Errorf("CreatePipe out: %w", err)
	}
	closePipes := func() {
		windows.CloseHandle(inRead)
		windows.CloseHandle(inWrite)
		windows.CloseHandle(outRead)
		windows.CloseHandle(outWrite)
	}

	size := windows.Coord{X: int16(cols), Y: int16(rows)}
	var hpc windows.Handle
	if api != nil {
		r1, _, _ := api.create.Call(coordArg(size), uintptr(inRead), uintptr(outWrite), 0, uintptr(unsafe.Pointer(&hpc)))
		if int32(r1) < 0 {
			closePipes()
			return fmt.Errorf("ConptyCreatePseudoConsole: 0x%08x", uint32(r1))
		}
		api.showHide.Call(uintptr(hpc), 0)
	} else if err := windows.CreatePseudoConsole(size, inRead, outWrite, 0, &hpc); err != nil {
		closePipes()
		return fmt.Errorf("CreatePseudoConsole: %w", err)
	}
	// El pseudoconsole ya duplicó sus extremos.
	windows.CloseHandle(inRead)
	windows.CloseHandle(outWrite)

	b.mu.Lock()
	b.api, b.hPC, b.hInputWrite = api, hpc, inWrite
	b.mu.Unlock()
	b.hOutputRead = outRead

	// La lista de atributos vive hasta Close.
	attrList, err := windows.NewProcThreadAttributeList(1)
	if err != nil {
		b.Close()
		return fmt.Errorf("NewProcThreadAttributeList: %w", err)
	}
	b.mu.Lock()
	b.attrList = attrList
	b.mu.Unlock()
	// lpValue es el propio HPCON, no un puntero a él.
	hpcValue := *(*unsafe.Pointer)(unsafe.Pointer(&hpc))
	if err := attrList.Update(windows.PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE, hpcValue, unsafe.Sizeof(hpc)); err != nil {
		b.Close()
		return fmt.Errorf("UpdateProcThreadAttribute: %w", err)
	}

	si := windows.StartupInfoEx{ProcThreadAttributeList: attrList.List()}
	si.Cb = uint32(unsafe.Sizeof(si))
	// Sin esto el hijo hereda la E/S estándar del padre si está redirigida.
	si.Flags = windows.STARTF_USESTDHANDLES

	cmdline, err := windows.UTF16PtrFromString(buildCmdline(argv))
	if err != nil {
		b.Close()
		return err
	}
	var cwdPtr *uint16
	if cwd != "" {
		if cwdPtr, err = windows.UTF16PtrFromString(cwd); err != nil {
			b.Close()
			return err
		}
	}
	block := EnvBlock(env)
	var envPtr *uint16
	if len(block) > 0 {
		envPtr = &block[0]
	}

	var pi windows.ProcessInformation
	err = windows.CreateProcess(nil, cmdline, nil, nil, false,
		windows.EXTENDED_STARTUPINFO_PRESENT|windows.CREATE_UNICODE_ENVIRONMENT,
		envPtr, cwdPtr, &si.StartupInfo, &pi)
	runtime.KeepAlive(block)
	if err != nil {
		b.Close()
		return fmt.Errorf("CreateProcess: %w", err)
	}
	windows.CloseHandle(pi.Thread)

	b.mu.Lock()
	b.hProcess = pi.Process
	b.mu.Unlock()
	b.pid = int(pi.ProcessId)
	b.alive.Store(true)

	go func(h windows.Handle) {
		windows.WaitForSingleObject(h, windows.INFINITE)
		b.alive.Store(false)
		time.Sleep(100 * time.Millisecond) // deja vaciar la salida pendiente
		b.Close()
	}(pi.Process)
	return nil
}

func (b *conptyBackend) Read(p []byte) (int, error) {
	h := b.hOutputRead
	if h == 0 {
		return 0, io.EOF
	}
	var n uint32
	err := windows.ReadFile(h, p, &n, nil)
	if err != nil || n == 0 {
		if err == nil || err == windows.ERROR_BROKEN_PIPE || err == windows.ERROR_HANDLE_EOF || err == windows.ERROR_INVALID_HANDLE {
			windows.CloseHandle(h)
			b.hOutputRead = 0
			return int(n), io.EOF
		}
		return int(n), err
	}
	return int(n), nil
}

func (b *conptyBackend) Write(s string) error {
	b.mu.Lock()
	h := b.hInputWrite
	b.mu.Unlock()
	if h == 0 {
		return errors.New("closed pipe")
	}
	data := []byte(s)
	for len(data) > 0 {
		var n uint32
		if err := windows.WriteFile(h, data, &n, nil); err != nil {
			return err
		}
		data = data[n:]
	}
	return nil
}

func (b *conptyBackend) Resize(cols, rows int) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.hPC == 0 {
		return errors.New("pseudoconsole not initialized")
	}
	size := windows.Coord{X: int16(cols), Y: int16(rows)}
	if b.api != nil {
		r1, _, _ := b.api.resize.Call(uintptr(b.hPC), coordArg(size))
		if int32(r1) < 0 {
			return fmt.Errorf("ConptyResizePseudoConsole: 0x%08x", uint32(r1))
		}
		return nil
	}
	return windows.ResizePseudoConsole(b.hPC, size)
}

func (b *conptyBackend) Alive() bool {
	if !b.alive.Load() {
		return false
	}
	b.mu.Lock()
	h := b.hProcess
	b.mu.Unlock()
	if h == 0 {
		return false
	}
	ev, err := windows.WaitForSingleObject(h, 0)
	if err != nil || ev != uint32(windows.WAIT_TIMEOUT) {
		b.alive.Store(false)
		return false
	}
	return true
}

func (b *conptyBackend) Pid() int { return b.pid }

// Close closes the pseudoconsole and handles once (a double ClosePseudoConsole is a use-after-free).
func (b *conptyBackend) Close() {
	b.closeOnce.Do(func() {
		b.alive.Store(false)
		b.mu.Lock()
		defer b.mu.Unlock()
		if b.hPC != 0 {
			if b.api != nil {
				b.api.close.Call(uintptr(b.hPC))
			} else {
				windows.ClosePseudoConsole(b.hPC)
			}
			b.hPC = 0
		}
		if b.hInputWrite != 0 {
			windows.CloseHandle(b.hInputWrite)
			b.hInputWrite = 0
		}
		if b.hProcess != 0 {
			windows.CloseHandle(b.hProcess)
			b.hProcess = 0
		}
		if b.attrList != nil {
			b.attrList.Delete()
			b.attrList = nil
		}
	})
}
