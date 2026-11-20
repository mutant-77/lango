package pty_test

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"lango/internal/pty"
)

type errorBackend struct {
	fakeBackend
	writeErr error
	cols     int
	rows     int
}

func (e *errorBackend) Write(s string) error {
	return e.writeErr
}

func (e *errorBackend) Resize(cols, rows int) error {
	e.cols = cols
	e.rows = rows
	return nil
}

func TestWriteErrorBroadcast(t *testing.T) {
	eb := &errorBackend{
		fakeBackend: *newFakeBackend(true),
		writeErr:    errors.New("disk failure"),
	}
	bridge := pty.NewBridge("C:\\tmp", pty.WithBackend(eb))
	_ = bridge.Start()

	_, _, sub := bridge.SnapshotAndSubscribe()
	defer sub.Close()

	_ = bridge.Write("bad command")

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	msg, ok := sub.Next(ctx)
	if !ok {
		t.Fatal("expected broadcast message on write error")
	}

	data, _ := msg["data"].(string)
	if !strings.Contains(data, "[Lango] PTY write error: disk failure") {
		t.Fatalf("unexpected write error message: %q", data)
	}
}

func TestResizeMax1AndLastResizeUpdated(t *testing.T) {
	clk := &fakeClock{currTime: 100.0}
	eb := &errorBackend{
		fakeBackend: *newFakeBackend(true),
	}
	bridge := pty.NewBridge("C:\\tmp", pty.WithBackend(eb), pty.WithClock(clk))
	_ = bridge.Start()

	// Resize with 0 or negative values -> clamped to max(1)
	_ = bridge.Resize(-10, 0)
	if eb.cols != 1 || eb.rows != 1 {
		t.Fatalf("expected cols=1 rows=1, got cols=%d rows=%d", eb.cols, eb.rows)
	}

	// Verify lastResize updated even when size unchanged
	clk.currTime = 250.0
	_ = bridge.Resize(1, 1)

	// In echo window (250.1 - 250.0 = 0.1 < 0.3)
	clk.currTime = 250.1
	bridge.SetAgent("test")
	if bridge.State() != "waiting" {
		t.Fatalf("expected waiting during echo window after resize")
	}
}

func TestSubContextCancellation(t *testing.T) {
	bridge := pty.NewBridge("C:\\tmp", pty.WithBackend(newFakeBackend(true)))
	_ = bridge.Start()

	_, _, sub := bridge.SnapshotAndSubscribe()
	defer sub.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	msg, ok := sub.Next(ctx)
	if ok || msg != nil {
		t.Fatalf("expected context cancellation, got ok=%v, msg=%v", ok, msg)
	}
}

func TestOnFirstSubscribeHook(t *testing.T) {
	bridge := pty.NewBridge("C:\\tmp", pty.WithBackend(newFakeBackend(true)))
	_ = bridge.Start()

	var hookCount atomic.Int32
	bridge.SetOnFirstSubscribe(func() {
		hookCount.Add(1)
	})

	_, _, sub1 := bridge.SnapshotAndSubscribe()
	defer sub1.Close()

	_, _, sub2 := bridge.SnapshotAndSubscribe()
	defer sub2.Close()

	time.Sleep(50 * time.Millisecond)
	if hookCount.Load() != 1 {
		t.Fatalf("expected hook called exactly once, got %d", hookCount.Load())
	}
}

func TestSetAgentAndAgent(t *testing.T) {
	bridge := pty.NewBridge("C:\\tmp", pty.WithBackend(newFakeBackend(true)))
	bridge.SetAgent("pi")
	if bridge.Agent() != "pi" {
		t.Fatalf("expected agent 'pi', got %q", bridge.Agent())
	}
	bridge.SetAgent("")
	if bridge.Agent() != "" {
		t.Fatalf("expected empty agent, got %q", bridge.Agent())
	}
}
