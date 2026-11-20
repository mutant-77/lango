package pty_test

import (
	"strings"
	"testing"

	"lango/internal/pty"
)

func TestResolveShellReturnsList(t *testing.T) {
	shell := pty.ResolveShell()
	if len(shell) < 1 {
		t.Fatalf("expected at least 1 element, got %d", len(shell))
	}
}

func TestResolveShellWindows(t *testing.T) {
	shell := pty.ResolveShell()
	name := strings.ToLower(shell[0])
	if !strings.Contains(name, "pwsh.exe") &&
		!strings.Contains(name, "powershell.exe") &&
		!strings.Contains(name, "cmd.exe") {
		t.Fatalf("unexpected shell: %s", shell[0])
	}
}

type dummyBackend struct{}

func (d *dummyBackend) Spawn(argv []string, cwd string, env []string, cols, rows int) error {
	return nil
}
func (d *dummyBackend) Read(p []byte) (int, error) { return 0, nil }
func (d *dummyBackend) Write(s string) error       { return nil }
func (d *dummyBackend) Resize(cols, rows int) error { return nil }
func (d *dummyBackend) Alive() bool                { return false }
func (d *dummyBackend) Pid() int                   { return 0 }
func (d *dummyBackend) Close()                     {}

func TestBackendInterface(t *testing.T) {
	var _ pty.Backend = (*dummyBackend)(nil)
}

func TestPidReturnsZeroBeforeSpawn(t *testing.T) {
	backend := pty.NewConptyBackend("", nil)
	if backend.Pid() != 0 {
		t.Fatalf("expected pid 0 before spawn, got %d", backend.Pid())
	}
}
