package pty_test

import (
	"testing"

	"lango/internal/pty"
)

type fakeClock struct {
	currTime float64
}

func (c *fakeClock) Now() float64 {
	return c.currTime
}

type fakeBackend struct {
	alive   bool
	written []string
	cols    int
	rows    int
	pid     int
}

func newFakeBackend(alive bool) *fakeBackend {
	return &fakeBackend{
		alive: alive,
		pid:   1234,
	}
}

func (f *fakeBackend) Spawn(argv []string, cwd string, env []string, cols, rows int) error {
	f.cols = cols
	f.rows = rows
	return nil
}

func (f *fakeBackend) Read(p []byte) (int, error) {
	return 0, nil
}

func (f *fakeBackend) Write(s string) error {
	f.written = append(f.written, s)
	return nil
}

func (f *fakeBackend) Resize(cols, rows int) error {
	f.cols = cols
	f.rows = rows
	return nil
}

func (f *fakeBackend) Alive() bool {
	return f.alive
}

func (f *fakeBackend) Pid() int {
	return f.pid
}

func (f *fakeBackend) Close() {
	f.alive = false
}

func TestComputeStateTable(t *testing.T) {
	tests := []struct {
		alive      bool
		agent      string
		used       bool
		lastOutput float64
		now        float64
		expected   string
	}{
		{false, "claude", true, 10.0, 10.5, "fresh"},
		{true, "claude", true, 10.0, 11.99, "busy"},
		{true, "claude", true, 10.0, 12.0, "waiting"},
		{true, "claude", false, 10.0, 15.0, "waiting"},
		{true, "", true, 10.0, 11.0, "used"},
		{true, "", false, 10.0, 11.0, "fresh"},
	}

	for _, tt := range tests {
		got := pty.ComputeState(tt.alive, tt.agent, tt.used, tt.lastOutput, tt.now)
		if got != tt.expected {
			t.Errorf("ComputeState(%v, %q, %v, %v, %v) = %q, want %q",
				tt.alive, tt.agent, tt.used, tt.lastOutput, tt.now, got, tt.expected)
		}
	}
}

func TestNoteInputTracksUsed(t *testing.T) {
	clk := &fakeClock{currTime: 10.0}
	fb := newFakeBackend(true)
	bridge := pty.NewBridge("C:\\tmp", pty.WithBackend(fb), pty.WithClock(clk))
	_ = bridge.Start()

	if bridge.State() != "fresh" {
		t.Fatalf("expected fresh, got %q", bridge.State())
	}

	// Write without newline
	clk.currTime = 10.0
	_ = bridge.Write("ls")
	if bridge.State() != "fresh" {
		t.Fatalf("expected fresh after 'ls', got %q", bridge.State())
	}

	// Write with newline
	clk.currTime = 11.0
	_ = bridge.Write("ls\r")
	if bridge.State() != "used" {
		t.Fatalf("expected used after 'ls\\r', got %q", bridge.State())
	}
}

func TestNoteOutputEchoWindowAfterInput(t *testing.T) {
	clk := &fakeClock{currTime: 100.0}
	fb := newFakeBackend(true)
	bridge := pty.NewBridge("C:\\tmp", pty.WithBackend(fb), pty.WithClock(clk))
	_ = bridge.Start()

	// Initial input at t0 = 100.0
	bridge.SetAgent("claude")
	_ = bridge.Write("ls")
	// State should be waiting (no output yet)
	if bridge.State() != "waiting" {
		t.Fatalf("expected waiting, got %q", bridge.State())
	}

	// Output at t0 + 0.1 (< EchoWindow = 0.3): should not mark busy
	clk.currTime = 100.1
	bridge.Broadcast(pty.Message{"type": "data", "data": "output"})
	// Directly check if State is waiting
	if bridge.State() != "waiting" {
		t.Fatalf("expected waiting during echo window, got %q", bridge.State())
	}
}

func TestNoteOutputEchoWindowAfterResize(t *testing.T) {
	clk := &fakeClock{currTime: 200.0}
	fb := newFakeBackend(true)
	bridge := pty.NewBridge("C:\\tmp", pty.WithBackend(fb), pty.WithClock(clk))
	_ = bridge.Start()
	bridge.SetAgent("claude")

	_ = bridge.Resize(80, 24)
	if bridge.State() != "waiting" {
		t.Fatalf("expected waiting, got %q", bridge.State())
	}
}

func TestPtyBridgeStateAndIdleS(t *testing.T) {
	clk := &fakeClock{currTime: 100.0}
	fb := newFakeBackend(true)
	bridge := pty.NewBridge("C:\\tmp", pty.WithBackend(fb), pty.WithClock(clk))
	_ = bridge.Start()

	// Initially fresh
	if bridge.State() != "fresh" {
		t.Fatalf("expected fresh, got %q", bridge.State())
	}

	// Used after enter
	_ = bridge.Write("pwd\r")
	if bridge.State() != "used" {
		t.Fatalf("expected used, got %q", bridge.State())
	}

	// Waiting with agent but no recent output
	bridge.SetAgent("pi")
	if bridge.State() != "waiting" {
		t.Fatalf("expected waiting, got %q", bridge.State())
	}

	// Output 0.5 s after input (outside EchoWindow) and within BusyWindow (2.0s)
	// We trigger reader output by writing to a pipe or directly simulating note_output
	clk.currTime = 100.5
	// Check ComputeState directly to verify math
	if pty.ComputeState(bridge.IsAlive(), bridge.Agent(), true, 100.5, 100.5) != "busy" {
		t.Fatalf("expected busy")
	}

	// 3.0 s later without output -> waiting
	clk.currTime = 103.5
	if pty.ComputeState(bridge.IsAlive(), bridge.Agent(), true, 100.5, 103.5) != "waiting" {
		t.Fatalf("expected waiting")
	}

	// Fresh if not alive
	fb.alive = false
	if bridge.State() != "fresh" {
		t.Fatalf("expected fresh when not alive, got %q", bridge.State())
	}

	// idle_s: 103.5 - 100.0 = 3
	if bridge.IdleS() != 3 {
		t.Fatalf("expected idle_s 3, got %d", bridge.IdleS())
	}
}
