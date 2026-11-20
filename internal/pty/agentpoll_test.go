//go:build windows

package pty

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type pollMockBackend struct {
	mu     sync.Mutex
	alive  bool
	pid    int
	closed chan struct{}
}

func newPollMockBackend(pid int) *pollMockBackend {
	return &pollMockBackend{
		alive:  true,
		pid:    pid,
		closed: make(chan struct{}),
	}
}

func (m *pollMockBackend) Spawn(argv []string, cwd string, env []string, cols, rows int) error {
	return nil
}

func (m *pollMockBackend) Read(p []byte) (int, error) {
	<-m.closed
	return 0, errors.New("closed")
}

func (m *pollMockBackend) Write(s string) error        { return nil }
func (m *pollMockBackend) Resize(cols, rows int) error { return nil }

func (m *pollMockBackend) Alive() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.alive
}

func (m *pollMockBackend) Pid() int {
	return m.pid
}

func (m *pollMockBackend) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.alive {
		m.alive = false
		close(m.closed)
	}
}

func getRepoConptyDir(t *testing.T) string {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd failed: %v", err)
	}
	repoDir := filepath.Clean(filepath.Join(wd, "..", ".."))
	conptyDir := filepath.Join(repoDir, "third_party", "conpty")
	if _, err := os.Stat(filepath.Join(conptyDir, "conpty.dll")); err != nil {
		t.Skipf("conpty.dll not found in %s (run third_party\\conpty\\fetch.ps1): %v", conptyDir, err)
	}
	return conptyDir
}

func TestAgentPoll_Grandchildren(t *testing.T) {
	snap := func() (map[int]int, error) {
		return map[int]int{
			1000: 1,
			1010: 1000,
			1020: 1010,
		}, nil
	}
	cmd := func(pid int) (string, error) {
		switch pid {
		case 1010:
			return "cmd.exe /c run.bat", nil
		case 1020:
			return `C:\tools\claude.exe --prompt hello`, nil
		default:
			return "", nil
		}
	}

	t.Cleanup(setTestProviders(snap, cmd, 10*time.Millisecond))

	backend := newPollMockBackend(1000)
	bridge := NewBridge("C:\\tmp", WithBackend(backend))

	var (
		agentMu     sync.Mutex
		agentCalls  []string
		receivedCh  = make(chan string, 1)
	)

	bridge.EnableAgentPolling(func(agent string) {
		agentMu.Lock()
		agentCalls = append(agentCalls, agent)
		agentMu.Unlock()
		select {
		case receivedCh <- agent:
		default:
		}
	})

	if err := bridge.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer bridge.Terminate()

	_, _, sub := bridge.SnapshotAndSubscribe()
	defer sub.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	var gotAgentMsg bool
	for {
		msg, ok := sub.Next(ctx)
		if !ok {
			break
		}
		if msg["type"] == "agent" {
			if msg["agent"] == "claude" {
				gotAgentMsg = true
			}
			break
		}
	}

	if !gotAgentMsg {
		t.Fatal("expected subscriber to receive agent=claude")
	}

	select {
	case ag := <-receivedCh:
		if ag != "claude" {
			t.Fatalf("expected onAgent 'claude', got %q", ag)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("timed out waiting for onAgent call")
	}

	if bridge.Agent() != "claude" {
		t.Fatalf("expected bridge.Agent() to be 'claude', got %q", bridge.Agent())
	}
}

func TestAgentPoll_ErrorDiscardsPass(t *testing.T) {
	snap := func() (map[int]int, error) {
		return map[int]int{
			1000: 1,
			1010: 1000,
		}, nil
	}
	cmd := func(pid int) (string, error) {
		return "", errors.New("access denied")
	}

	t.Cleanup(setTestProviders(snap, cmd, 10*time.Millisecond))

	backend := newPollMockBackend(1000)
	bridge := NewBridge("C:\\tmp", WithBackend(backend))
	bridge.SetAgent("claude")

	var onAgentCalls atomic.Int32
	bridge.EnableAgentPolling(func(agent string) {
		onAgentCalls.Add(1)
	})

	if err := bridge.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer bridge.Terminate()

	_, _, sub := bridge.SnapshotAndSubscribe()
	defer sub.Close()

	time.Sleep(60 * time.Millisecond)

	if bridge.Agent() != "claude" {
		t.Fatalf("expected agent to remain 'claude', got %q", bridge.Agent())
	}
	if onAgentCalls.Load() != 0 {
		t.Fatalf("expected 0 onAgent calls, got %d", onAgentCalls.Load())
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	for {
		msg, ok := sub.Next(ctx)
		if !ok {
			break
		}
		if msg["type"] == "agent" {
			t.Fatalf("unexpected agent broadcast: %v", msg)
		}
	}
}

func TestAgentPoll_ShellMissingStopsLoop(t *testing.T) {
	var snapCount atomic.Int32
	snap := func() (map[int]int, error) {
		snapCount.Add(1)
		return map[int]int{
			2000: 1,
		}, nil
	}
	cmd := func(pid int) (string, error) {
		return "", nil
	}

	t.Cleanup(setTestProviders(snap, cmd, 10*time.Millisecond))

	backend := newPollMockBackend(1000)
	bridge := NewBridge("C:\\tmp", WithBackend(backend))

	bridge.EnableAgentPolling(func(agent string) {})

	if err := bridge.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer bridge.Terminate()

	_, _, sub := bridge.SnapshotAndSubscribe()
	defer sub.Close()

	time.Sleep(100 * time.Millisecond)

	count := snapCount.Load()
	if count != 1 {
		t.Fatalf("expected polling loop to stop after 1 snapshot, got %d", count)
	}
}

func TestAgentPoll_ChangeBroadcastsOnce(t *testing.T) {
	snap := func() (map[int]int, error) {
		return map[int]int{
			1000: 1,
			1010: 1000,
		}, nil
	}
	cmd := func(pid int) (string, error) {
		return "gemini.exe", nil
	}

	t.Cleanup(setTestProviders(snap, cmd, 10*time.Millisecond))

	backend := newPollMockBackend(1000)
	bridge := NewBridge("C:\\tmp", WithBackend(backend))

	var (
		agentMu    sync.Mutex
		agentCalls []string
	)

	bridge.EnableAgentPolling(func(agent string) {
		agentMu.Lock()
		agentCalls = append(agentCalls, agent)
		agentMu.Unlock()
	})

	if err := bridge.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer bridge.Terminate()

	_, _, sub := bridge.SnapshotAndSubscribe()
	defer sub.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	var broadcastCount int
	for {
		msg, ok := sub.Next(ctx)
		if !ok {
			break
		}
		if msg["type"] == "agent" {
			if msg["agent"] == "gemini" {
				broadcastCount++
			}
			break
		}
	}

	if broadcastCount != 1 {
		t.Fatalf("expected 1 broadcast for gemini, got %d", broadcastCount)
	}

	time.Sleep(60 * time.Millisecond)

	drainCtx, drainCancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer drainCancel()
	for {
		msg, ok := sub.Next(drainCtx)
		if !ok {
			break
		}
		if msg["type"] == "agent" {
			t.Fatalf("unexpected duplicate broadcast: %v", msg)
		}
	}

	agentMu.Lock()
	calls := append([]string(nil), agentCalls...)
	agentMu.Unlock()

	if len(calls) != 1 || calls[0] != "gemini" {
		t.Fatalf("expected onAgent called once with 'gemini', got %v", calls)
	}
}

func TestAgentPoll_SameAgentNoDuplicate(t *testing.T) {
	snap := func() (map[int]int, error) {
		return map[int]int{
			1000: 1,
			1010: 1000,
		}, nil
	}
	cmd := func(pid int) (string, error) {
		return "gemini.exe", nil
	}

	t.Cleanup(setTestProviders(snap, cmd, 10*time.Millisecond))

	backend := newPollMockBackend(1000)
	bridge := NewBridge("C:\\tmp", WithBackend(backend))
	bridge.SetAgent("gemini")

	var onAgentCalls atomic.Int32
	bridge.EnableAgentPolling(func(agent string) {
		onAgentCalls.Add(1)
	})

	if err := bridge.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer bridge.Terminate()

	_, _, sub := bridge.SnapshotAndSubscribe()
	defer sub.Close()

	time.Sleep(60 * time.Millisecond)

	if onAgentCalls.Load() != 0 {
		t.Fatalf("expected 0 onAgent calls, got %d", onAgentCalls.Load())
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	for {
		msg, ok := sub.Next(ctx)
		if !ok {
			break
		}
		if msg["type"] == "agent" {
			t.Fatalf("unexpected agent broadcast: %v", msg)
		}
	}
}

func TestAgentPoll_Integration(t *testing.T) {
	conptyDir := getRepoConptyDir(t)
	tempDir := t.TempDir()

	sysRoot := os.Getenv("SystemRoot")
	if sysRoot == "" {
		sysRoot = "C:\\Windows"
	}
	cmdSrc := filepath.Join(sysRoot, "System32", "cmd.exe")
	claudeExe := filepath.Join(tempDir, "claude.exe")

	cmdData, err := os.ReadFile(cmdSrc)
	if err != nil {
		t.Fatalf("failed to read %s: %v", cmdSrc, err)
	}
	if err := os.WriteFile(claudeExe, cmdData, 0755); err != nil {
		t.Fatalf("failed to write %s: %v", claudeExe, err)
	}

	bridge := NewBridge(tempDir, WithDllDir(conptyDir))

	var (
		agentMu     sync.Mutex
		agentEvents []string
	)

	bridge.EnableAgentPolling(func(agent string) {
		agentMu.Lock()
		agentEvents = append(agentEvents, agent)
		agentMu.Unlock()
	})

	if err := bridge.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer bridge.Terminate()

	replay, _, sub := bridge.SnapshotAndSubscribe()
	defer sub.Close()

	var promptSeen bool
	if strings.Contains(replay, "PS ") || strings.Contains(replay, ">") {
		promptSeen = true
	}
	var output strings.Builder
	output.WriteString(replay)

	promptDeadline := time.Now().Add(15 * time.Second)
	for !promptSeen && time.Now().Before(promptDeadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		msg, ok := sub.Next(ctx)
		cancel()
		if ok && msg["type"] == "data" {
			txt, _ := msg["data"].(string)
			output.WriteString(txt)
			if strings.Contains(output.String(), "PS ") || strings.Contains(output.String(), ">") {
				promptSeen = true
				break
			}
		}
	}

	if !promptSeen {
		t.Fatalf("shell prompt not seen within 15 s: %q", output.String())
	}

	time.Sleep(200 * time.Millisecond)

	shell := ResolveShell()
	isCmd := strings.EqualFold(filepath.Base(shell[0]), "cmd.exe")
	var cmdLine string
	if isCmd {
		cmdLine = fmt.Sprintf("%q /c ping -n 6 127.0.0.1 > NUL\r", claudeExe)
	} else {
		cmdLine = fmt.Sprintf("& %q /c ping -n 6 127.0.0.1 > NUL\r", claudeExe)
	}

	if err := bridge.Write(cmdLine); err != nil {
		t.Fatalf("Write failed: %v", err)
	}

	var gotClaude bool
	deadlineClaude := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadlineClaude) {
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		msg, ok := sub.Next(ctx)
		cancel()
		if ok && msg["type"] == "agent" {
			if ag, ok := msg["agent"].(string); ok && ag == "claude" {
				gotClaude = true
				break
			}
		}
	}
	if !gotClaude {
		t.Fatal("subscriber did not receive {\"type\":\"agent\",\"agent\":\"claude\"} within 3 s")
	}

	var gotNil bool
	deadlineNil := time.Now().Add(12 * time.Second)
	for time.Now().Before(deadlineNil) {
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		msg, ok := sub.Next(ctx)
		cancel()
		if ok && msg["type"] == "agent" {
			if msg["agent"] == nil {
				gotNil = true
				break
			}
		}
	}
	if !gotNil {
		t.Fatal("subscriber did not receive {\"type\":\"agent\",\"agent\":nil} within 3 s of agent exit")
	}

	agentMu.Lock()
	events := append([]string(nil), agentEvents...)
	agentMu.Unlock()

	if len(events) != 2 || events[0] != "claude" || events[1] != "" {
		t.Fatalf("expected onAgent [claude, \"\"], got %v", events)
	}

	bridge.Terminate()
}
