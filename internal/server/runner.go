package server

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"

	"lango/internal/winutil"
)

// Runner abstracts process execution and system launching for testing.
type Runner interface {
	ShellExecute(file, dir string) error
	StartRaw(cmdline string) error
	Start(argv []string, cwd string, flags uint32) error
}

// RealRunner is the production implementation of Runner.
type RealRunner struct{}

func (r *RealRunner) ShellExecute(file, dir string) error {
	return winutil.ShellExecute(file, dir)
}

func (r *RealRunner) StartRaw(cmdline string) error {
	trimmed := strings.TrimSpace(cmdline)
	var bin string
	if strings.HasPrefix(trimmed, `"`) {
		if end := strings.Index(trimmed[1:], `"`); end != -1 {
			bin = trimmed[1 : 1+end]
		} else {
			bin = trimmed[1:]
		}
	} else {
		if end := strings.IndexByte(trimmed, ' '); end != -1 {
			bin = trimmed[:end]
		} else {
			bin = trimmed
		}
	}

	cmd := exec.Command(bin)
	cmd.SysProcAttr = &windows.SysProcAttr{
		CmdLine: cmdline,
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() {
		_ = cmd.Wait()
	}()
	return nil
}

func (r *RealRunner) Start(argv []string, cwd string, flags uint32) error {
	if len(argv) == 0 {
		return fmt.Errorf("empty argv")
	}

	bin := argv[0]
	resolved, err := exec.LookPath(bin)
	if err != nil {
		if localAppData := os.Getenv("LOCALAPPDATA"); localAppData != "" {
			candidate := filepath.Join(localAppData, "Microsoft", "WindowsApps", bin)
			if _, lerr := os.Lstat(candidate); lerr == nil {
				resolved = candidate
				err = nil
			}
		}
	}
	if err != nil {
		return fmt.Errorf("%w: %s", exec.ErrNotFound, bin)
	}

	cmd := exec.Command(resolved, argv[1:]...)
	cmd.Dir = cwd
	cmd.SysProcAttr = &windows.SysProcAttr{
		CreationFlags: flags,
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() {
		_ = cmd.Wait()
	}()
	return nil
}
