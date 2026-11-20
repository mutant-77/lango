//go:build windows

package pty_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unsafe"

	"lango/internal/pty"

	"golang.org/x/sys/windows"
)

type captureLogger struct {
	mu       sync.Mutex
	warnings []string
}

func (l *captureLogger) Warn(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.warnings = append(l.warnings, fmt.Sprintf(format, args...))
}
func (l *captureLogger) Info(format string, args ...any)  {}
func (l *captureLogger) Error(format string, args ...any) {}
func (l *captureLogger) Debug(format string, args ...any) {}

func (l *captureLogger) hasWarn(substr string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, w := range l.warnings {
		if strings.Contains(w, substr) {
			return true
		}
	}
	return false
}

func getRepoConptyDir(t *testing.T) string {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd failed: %v", err)
	}
	// wd is <repo>/internal/pty -> repo is ../..
	repoDir := filepath.Clean(filepath.Join(wd, "..", ".."))
	conptyDir := filepath.Join(repoDir, "third_party", "conpty")
	if _, err := os.Stat(filepath.Join(conptyDir, "conpty.dll")); err != nil {
		t.Skipf("conpty.dll not found in %s (run third_party\\conpty\\fetch.ps1): %v", conptyDir, err)
	}
	return conptyDir
}

func findOpenConsoleChild(t *testing.T, parentPID int) bool {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		t.Fatalf("CreateToolhelp32Snapshot failed: %v", err)
	}
	defer windows.CloseHandle(snap)

	var pe windows.ProcessEntry32
	pe.Size = uint32(unsafe.Sizeof(pe))
	for err = windows.Process32First(snap, &pe); err == nil; err = windows.Process32Next(snap, &pe) {
		exe := windows.UTF16ToString(pe.ExeFile[:])
		if strings.EqualFold(exe, "OpenConsole.exe") && int(pe.ParentProcessID) == parentPID {
			return true
		}
	}
	return false
}

func isPidRunning(pid int) bool {
	if pid <= 0 {
		return false
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)
	event, err := windows.WaitForSingleObject(h, 0)
	if err != nil {
		return false
	}
	return event == uint32(windows.WAIT_TIMEOUT)
}

func TestConptyDLLExportsExist(t *testing.T) {
	conptyDir := getRepoConptyDir(t)
	dll := windows.NewLazyDLL(filepath.Join(conptyDir, "conpty.dll"))
	if err := dll.Load(); err != nil {
		t.Fatalf("failed to load conpty.dll: %v", err)
	}
	procs := []string{
		"ConptyCreatePseudoConsole",
		"ConptyResizePseudoConsole",
		"ConptyClosePseudoConsole",
		"ConptyShowHidePseudoConsole",
	}
	for _, name := range procs {
		p := dll.NewProc(name)
		if err := p.Find(); err != nil {
			t.Fatalf("export %s not found in conpty.dll: %v", name, err)
		}
	}
}

func TestConptyEchoHola(t *testing.T) {
	conptyDir := getRepoConptyDir(t)
	backend := pty.NewConptyBackend(conptyDir, nil)
	env := pty.BuildEnv()
	cwd, _ := os.Getwd()

	err := backend.Spawn([]string{"cmd.exe", "/c", "echo hola & timeout /t 1 >nul"}, cwd, env, 80, 24)
	if err != nil {
		t.Fatalf("Spawn failed: %v", err)
	}
	defer backend.Close()

	deadline := time.Now().Add(5 * time.Second)
	buf := make([]byte, 1024)
	var output strings.Builder
	for time.Now().Before(deadline) {
		n, err := backend.Read(buf)
		t.Logf("Read n=%d, err=%v, data=%q", n, err, string(buf[:n]))
		if n > 0 {
			output.Write(buf[:n])
			if strings.Contains(output.String(), "hola") {
				break
			}
		}
		if err != nil {
			break
		}
	}

	if !strings.Contains(output.String(), "hola") {
		t.Fatalf("expected 'hola' in output within 5s, got: %q", output.String())
	}
}

func TestConptyOpenConsoleProcessAndExit(t *testing.T) {
	conptyDir := getRepoConptyDir(t)
	cwd, _ := os.Getwd()
	bridge := pty.NewBridge(cwd, pty.WithDllDir(conptyDir))

	err := bridge.Start()
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer bridge.Terminate()

	// 1. While session is alive, verify OpenConsole.exe exists with parent = test process
	time.Sleep(300 * time.Millisecond)
	if !bridge.IsAlive() {
		t.Fatal("expected session to be alive")
	}

	hasOpenConsole := findOpenConsoleChild(t, os.Getpid())
	if !hasOpenConsole {
		t.Fatal("expected OpenConsole.exe child process whose parent is the test process")
	}

	// 2. Esperar al prompt; luego "exit\r" debe terminar en < 2 s
	promptDeadline := time.Now().Add(15 * time.Second)
	for {
		replay, _, sub := bridge.SnapshotAndSubscribe()
		sub.Close()
		if strings.Contains(replay, "PS ") || strings.Contains(replay, ">") {
			break
		}
		if time.Now().After(promptDeadline) {
			t.Fatalf("shell prompt not seen in 15 s: %q", replay)
		}
		time.Sleep(100 * time.Millisecond)
	}
	_ = bridge.Write("exit\r")

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !bridge.IsAlive() {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	if bridge.IsAlive() {
		t.Fatal("expected IsAlive to be false within 2s after exit")
	}
}

func TestConptyKernel32Fallback(t *testing.T) {
	tempDir := t.TempDir()
	logger := &captureLogger{}
	backend := pty.NewConptyBackend(tempDir, logger)
	env := pty.BuildEnv()
	cwd, _ := os.Getwd()

	err := backend.Spawn([]string{"cmd.exe", "/c", "echo THIS_IS_A_VERY_UNIQUE_STRING_12345"}, cwd, env, 80, 24)
	if err != nil {
		t.Fatalf("Spawn with fallback failed: %v", err)
	}
	defer backend.Close()

	if !logger.hasWarn("conpty.dll no disponible, usando kernel32:") {
		t.Fatalf("expected WARN log for fallback, got: %v", logger.warnings)
	}

	deadline := time.Now().Add(5 * time.Second)
	buf := make([]byte, 1024)
	var output strings.Builder
	for time.Now().Before(deadline) {
		n, err := backend.Read(buf)
		t.Logf("Read n=%d, err=%v, data=%q", n, err, string(buf[:n]))
		if n > 0 {
			output.Write(buf[:n])
			if strings.Contains(output.String(), "THIS_IS_A_VERY_UNIQUE_STRING_12345") {
				break
			}
		}
		if err != nil {
			break
		}
	}

	if !strings.Contains(output.String(), "THIS_IS_A_VERY_UNIQUE_STRING_12345") {
		t.Fatalf("expected 'THIS_IS_A_VERY_UNIQUE_STRING_12345' from fallback kernel32, got: %q", output.String())
	}
}

func TestConptyTerminate(t *testing.T) {
	conptyDir := getRepoConptyDir(t)
	cwd, _ := os.Getwd()
	bridge := pty.NewBridge(cwd, pty.WithDllDir(conptyDir))

	err := bridge.Start()
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	time.Sleep(300 * time.Millisecond)
	if !bridge.IsAlive() {
		t.Fatal("expected bridge alive")
	}

	pid := bridge.Pid()
	if pid <= 0 {
		t.Fatalf("expected valid pid, got %d", pid)
	}

	bridge.Terminate()

	// Within 3 s, IsAlive is false and shell PID no longer exists
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if !bridge.IsAlive() && !isPidRunning(pid) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	if bridge.IsAlive() {
		t.Fatal("expected IsAlive to be false after Terminate")
	}
	if isPidRunning(pid) {
		t.Fatalf("expected shell PID %d to no longer exist after Terminate", pid)
	}
}

// Ensure unused exec import isn't flagged
var _ = exec.LookPath
var _ = context.Background
