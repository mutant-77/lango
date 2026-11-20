package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"lango/internal/applog"
	"lango/internal/instances"
	"lango/internal/pty"
)

type fakeBackend struct {
	mu      sync.Mutex
	cond    *sync.Cond
	buf     bytes.Buffer
	alive   bool
	closed  bool
	writes  []string
	resizes [][2]int
	cols    int
	rows    int
	pid     int
}

func newFakeBackend() *fakeBackend {
	fb := &fakeBackend{
		alive: true,
		pid:   1234,
	}
	fb.cond = sync.NewCond(&fb.mu)
	return fb
}

func (f *fakeBackend) Spawn(argv []string, cwd string, env []string, cols, rows int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cols = cols
	f.rows = rows
	f.alive = true
	return nil
}

func (f *fakeBackend) Push(data []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.buf.Write(data)
	f.cond.Broadcast()
}

func (f *fakeBackend) Read(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for f.buf.Len() == 0 && !f.closed {
		f.cond.Wait()
	}
	if f.buf.Len() > 0 {
		return f.buf.Read(p)
	}
	return 0, io.EOF
}

func (f *fakeBackend) Write(s string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writes = append(f.writes, s)
	return nil
}

func (f *fakeBackend) Resize(cols, rows int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cols = cols
	f.rows = rows
	f.resizes = append(f.resizes, [2]int{cols, rows})
	return nil
}

func (f *fakeBackend) Alive() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.alive
}

func (f *fakeBackend) Pid() int {
	return f.pid
}

func (f *fakeBackend) Close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	f.alive = false
	f.cond.Broadcast()
}

func (f *fakeBackend) Writes() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := make([]string, len(f.writes))
	copy(cp, f.writes)
	return cp
}

func (f *fakeBackend) Resizes() [][2]int {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := make([][2]int, len(f.resizes))
	copy(cp, f.resizes)
	return cp
}

func dialTermWS(t *testing.T, ctx context.Context, port int, instID string) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	opts := &websocket.DialOptions{
		HTTPHeader: http.Header{"Origin": []string{"http://wails.localhost"}},
	}
	return websocket.Dial(ctx, fmt.Sprintf("ws://127.0.0.1:%d/ws/term/%s", port, instID), opts)
}

func readJSON(ctx context.Context, conn *websocket.Conn) (map[string]any, error) {
	typ, payload, err := conn.Read(ctx)
	if err != nil {
		return nil, err
	}
	if typ != websocket.MessageText {
		return nil, fmt.Errorf("unexpected message type: %v", typ)
	}
	var msg map[string]any
	dec := json.NewDecoder(bytes.NewReader(payload))
	dec.UseNumber()
	if err := dec.Decode(&msg); err != nil {
		return nil, err
	}
	return msg, nil
}

func writeRawJSON(ctx context.Context, conn *websocket.Conn, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return conn.Write(ctx, websocket.MessageText, b)
}

func writeText(ctx context.Context, conn *websocket.Conn, s string) error {
	return conn.Write(ctx, websocket.MessageText, []byte(s))
}

func TestTerm_UnknownID(t *testing.T) {
	dir := t.TempDir()
	mgr := instances.NewManager(filepath.Join(dir, "lango.json"))

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port

	ws := &WS{
		Mgr:    mgr,
		Port:   port,
		Origin: "http://wails.localhost",
		Log:    applog.New("test"),
	}

	srv := &http.Server{Handler: ws.Handler()}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	_, resp, err := dialTermWS(t, ctx, port, "unknown-id")
	if err == nil {
		t.Fatal("expected dial error on unknown id")
	}
	if resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 on unknown id, got %v", resp)
	}
}

func TestTerm_FirstConnect_EmptyBuffer(t *testing.T) {
	dir := t.TempDir()
	mgr := instances.NewManager(filepath.Join(dir, "lango.json"))
	inst, err := mgr.Create(dir)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port

	fb := newFakeBackend()
	ws := &WS{
		Mgr:    mgr,
		Port:   port,
		Origin: "http://wails.localhost",
		Log:    applog.New("test"),
		NewBridge: func(i *instances.Instance) *pty.Bridge {
			return pty.NewBridge(i.Path, pty.WithBackend(fb))
		},
	}

	srv := &http.Server{Handler: ws.Handler()}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	conn, _, err := dialTermWS(t, ctx, port, inst.ID)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	// Give server a moment to ensure no spurious replay message is sent
	time.Sleep(50 * time.Millisecond)

	// Push output chunk after connect
	fb.Push([]byte("hello world\r\n"))

	msg, err := readJSON(ctx, conn)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if msg["type"] != "data" || msg["data"] != "hello world\r\n" {
		t.Fatalf("unexpected message: %v", msg)
	}
}

func TestTerm_Reconnect_And_AltScreen(t *testing.T) {
	dir := t.TempDir()
	mgr := instances.NewManager(filepath.Join(dir, "lango.json"))
	inst, err := mgr.Create(dir)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port

	fb := newFakeBackend()
	ws := &WS{
		Mgr:    mgr,
		Port:   port,
		Origin: "http://wails.localhost",
		Log:    applog.New("test"),
		NewBridge: func(i *instances.Instance) *pty.Bridge {
			return pty.NewBridge(i.Path, pty.WithBackend(fb))
		},
	}

	srv := &http.Server{Handler: ws.Handler()}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// 1. First connection
	conn1, _, err := dialTermWS(t, ctx, port, inst.ID)
	if err != nil {
		t.Fatalf("dial 1: %v", err)
	}

	fb.Push([]byte("prompt> ls\r\nfile1.txt\r\n"))
	msg1, err := readJSON(ctx, conn1)
	if err != nil {
		t.Fatalf("read 1: %v", err)
	}
	if msg1["data"] != "prompt> ls\r\nfile1.txt\r\n" {
		t.Fatalf("unexpected data: %v", msg1)
	}
	_ = conn1.Close(websocket.StatusNormalClosure, "")

	// 2. Reconnect without alt screen
	conn2, _, err := dialTermWS(t, ctx, port, inst.ID)
	if err != nil {
		t.Fatalf("dial 2: %v", err)
	}

	msg2, err := readJSON(ctx, conn2)
	if err != nil {
		t.Fatalf("read 2: %v", err)
	}
	expectedSeq := "\x1bc\x1b[?1049l\x1b[2J\x1b[3J\x1b[Hprompt> ls\r\nfile1.txt\r\n"
	if msg2["type"] != "data" || msg2["data"] != expectedSeq {
		t.Fatalf("expected exact replay sequence %q, got %q", expectedSeq, msg2["data"])
	}
	_ = conn2.Close(websocket.StatusNormalClosure, "")

	// 3. Enter alt screen
	fb.Push([]byte("\x1b[?1049h"))
	time.Sleep(50 * time.Millisecond)

	// Reconnect with alt screen active
	conn3, _, err := dialTermWS(t, ctx, port, inst.ID)
	if err != nil {
		t.Fatalf("dial 3: %v", err)
	}
	defer conn3.Close(websocket.StatusNormalClosure, "")

	msg3, err := readJSON(ctx, conn3)
	if err != nil {
		t.Fatalf("read 3: %v", err)
	}
	expectedAltSeq := "\x1bc\x1b[?1049l\x1b[2J\x1b[3J\x1b[H\x1b[?1049hprompt> ls\r\nfile1.txt\r\n\x1b[?1049h"
	if msg3["type"] != "data" || msg3["data"] != expectedAltSeq {
		t.Fatalf("expected alt replay sequence %q, got %q", expectedAltSeq, msg3["data"])
	}
}

func TestTerm_D9a_AtomicSnapshot(t *testing.T) {
	dir := t.TempDir()
	mgr := instances.NewManager(filepath.Join(dir, "lango.json"))
	inst, err := mgr.Create(dir)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port

	fb := newFakeBackend()
	ws := &WS{
		Mgr:    mgr,
		Port:   port,
		Origin: "http://wails.localhost",
		Log:    applog.New("test"),
		NewBridge: func(i *instances.Instance) *pty.Bridge {
			return pty.NewBridge(i.Path, pty.WithBackend(fb))
		},
	}

	srv := &http.Server{Handler: ws.Handler()}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	fb.Push([]byte("chunk-01;"))
	fb.Push([]byte("chunk-02;"))

	totalChunks := 30
	stopPush := make(chan struct{})
	go func() {
		for i := 3; i <= totalChunks; i++ {
			select {
			case <-stopPush:
				return
			case <-time.After(5 * time.Millisecond):
				fb.Push([]byte(fmt.Sprintf("chunk-%02d;", i)))
			}
		}
	}()
	defer close(stopPush)

	conn, _, err := dialTermWS(t, ctx, port, inst.ID)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	var accumulated string
	prefix := "\x1bc\x1b[?1049l\x1b[2J\x1b[3J\x1b[H"
	first := true

	for !strings.Contains(accumulated, fmt.Sprintf("chunk-%02d;", totalChunks)) {
		msg, err := readJSON(ctx, conn)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if msg["type"] == "data" {
			data, _ := msg["data"].(string)
			if first {
				first = false
				if strings.HasPrefix(data, prefix) {
					data = strings.TrimPrefix(data, prefix)
				}
			}
			accumulated += data
		}
	}

	parts := strings.Split(strings.TrimSuffix(accumulated, ";"), ";")
	if len(parts) < totalChunks {
		t.Fatalf("expected at least %d chunks, got %d: %s", totalChunks, len(parts), accumulated)
	}
	for i := 1; i <= totalChunks; i++ {
		expected := fmt.Sprintf("chunk-%02d", i)
		count := strings.Count(accumulated, expected+";")
		if count != 1 {
			t.Fatalf("chunk %s appeared %d times (expected 1)", expected, count)
		}
	}
}

func TestTerm_Ping(t *testing.T) {
	dir := t.TempDir()
	mgr := instances.NewManager(filepath.Join(dir, "lango.json"))
	inst, err := mgr.Create(dir)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port

	fb := newFakeBackend()
	ws := &WS{
		Mgr:          mgr,
		Port:         port,
		Origin:       "http://wails.localhost",
		Log:          applog.New("test"),
		PingInterval: 50 * time.Millisecond,
		NewBridge: func(i *instances.Instance) *pty.Bridge {
			return pty.NewBridge(i.Path, pty.WithBackend(fb))
		},
	}

	srv := &http.Server{Handler: ws.Handler()}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	conn, _, err := dialTermWS(t, ctx, port, inst.ID)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	msg, err := readJSON(ctx, conn)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if msg["type"] != "ping" {
		t.Fatalf("expected ping, got %v", msg)
	}
}

func TestTerm_Input(t *testing.T) {
	dir := t.TempDir()
	mgr := instances.NewManager(filepath.Join(dir, "lango.json"))
	inst, err := mgr.Create(dir)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port

	fb := newFakeBackend()
	ws := &WS{
		Mgr:    mgr,
		Port:   port,
		Origin: "http://wails.localhost",
		Log:    applog.New("test"),
		NewBridge: func(i *instances.Instance) *pty.Bridge {
			return pty.NewBridge(i.Path, pty.WithBackend(fb))
		},
	}

	srv := &http.Server{Handler: ws.Handler()}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	conn, _, err := dialTermWS(t, ctx, port, inst.ID)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	// 1. {"type":"data","data":"ls\r"} -> backend got "ls\r"
	if err := writeRawJSON(ctx, conn, map[string]any{"type": "data", "data": "ls\r"}); err != nil {
		t.Fatalf("write: %v", err)
	}

	// 2. {"type":"data","data":null} -> backend got "None"
	if err := writeRawJSON(ctx, conn, map[string]any{"type": "data", "data": nil}); err != nil {
		t.Fatalf("write: %v", err)
	}

	// 3. {"type":"data"} -> backend got ""
	if err := writeRawJSON(ctx, conn, map[string]any{"type": "data"}); err != nil {
		t.Fatalf("write: %v", err)
	}

	// 4. [1,2] (non-object) ignored
	if err := writeText(ctx, conn, "[1,2]"); err != nil {
		t.Fatalf("write: %v", err)
	}

	// 5. {"type":"data","data":"after"} to ensure loop continues
	if err := writeRawJSON(ctx, conn, map[string]any{"type": "data", "data": "after"}); err != nil {
		t.Fatalf("write: %v", err)
	}

	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if len(fb.Writes()) >= 4 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	writes := fb.Writes()
	expected := []string{"ls\r", "None", "", "after"}
	if len(writes) != len(expected) {
		t.Fatalf("expected %d writes, got %d: %v", len(expected), len(writes), writes)
	}
	for i, exp := range expected {
		if writes[i] != exp {
			t.Errorf("writes[%d] = %q, want %q", i, writes[i], exp)
		}
	}
}

func TestTerm_Resize(t *testing.T) {
	dir := t.TempDir()
	mgr := instances.NewManager(filepath.Join(dir, "lango.json"))
	inst, err := mgr.Create(dir)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port

	fb := newFakeBackend()
	ws := &WS{
		Mgr:    mgr,
		Port:   port,
		Origin: "http://wails.localhost",
		Log:    applog.New("test"),
		NewBridge: func(i *instances.Instance) *pty.Bridge {
			return pty.NewBridge(i.Path, pty.WithBackend(fb))
		},
	}

	srv := &http.Server{Handler: ws.Handler()}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	conn, _, err := dialTermWS(t, ctx, port, inst.ID)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	// 1. {"type":"resize","cols":"100","rows":30} -> Resize(100,30)
	if err := writeRawJSON(ctx, conn, map[string]any{"type": "resize", "cols": "100", "rows": 30}); err != nil {
		t.Fatalf("write: %v", err)
	}

	// 2. {"type":"resize","cols":12.9,"rows":5} -> Resize(12,5)
	if err := writeRawJSON(ctx, conn, map[string]any{"type": "resize", "cols": 12.9, "rows": 5}); err != nil {
		t.Fatalf("write: %v", err)
	}

	// 3. {"type":"resize"} -> Resize(80,24)
	if err := writeRawJSON(ctx, conn, map[string]any{"type": "resize"}); err != nil {
		t.Fatalf("write: %v", err)
	}

	// 4. {"type":"resize","cols":0,"rows":-3} -> Resize(1,1)
	if err := writeRawJSON(ctx, conn, map[string]any{"type": "resize", "cols": 0, "rows": -3}); err != nil {
		t.Fatalf("write: %v", err)
	}

	// 5. {"type":"resize","cols":null} -> error logged, no Resize
	if err := writeRawJSON(ctx, conn, map[string]any{"type": "resize", "cols": nil}); err != nil {
		t.Fatalf("write: %v", err)
	}

	// 6. {"type":"resize","cols":"1.5"} -> error logged, no Resize
	if err := writeRawJSON(ctx, conn, map[string]any{"type": "resize", "cols": "1.5"}); err != nil {
		t.Fatalf("write: %v", err)
	}

	// 7. Verify connection stays open and next message processed
	if err := writeRawJSON(ctx, conn, map[string]any{"type": "data", "data": "open"}); err != nil {
		t.Fatalf("write: %v", err)
	}

	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if len(fb.Writes()) >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	resizes := fb.Resizes()
	expectedResizes := [][2]int{
		{100, 30},
		{12, 5},
		{80, 24},
		{1, 1},
	}
	if len(resizes) != len(expectedResizes) {
		t.Fatalf("expected %d resizes, got %d: %v", len(expectedResizes), len(resizes), resizes)
	}
	for i, exp := range expectedResizes {
		if resizes[i] != exp {
			t.Errorf("resizes[%d] = %v, want %v", i, resizes[i], exp)
		}
	}

	writes := fb.Writes()
	if len(writes) != 1 || writes[0] != "open" {
		t.Fatalf("expected write [open], got %v", writes)
	}
}

func TestTerm_PTYAlive_And_SimultaneousConnect(t *testing.T) {
	dir := t.TempDir()
	mgr := instances.NewManager(filepath.Join(dir, "lango.json"))
	inst, err := mgr.Create(dir)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port

	var bridgeCount atomic.Int32
	var createdFb *fakeBackend
	var fbMu sync.Mutex

	ws := &WS{
		Mgr:    mgr,
		Port:   port,
		Origin: "http://wails.localhost",
		Log:    applog.New("test"),
		NewBridge: func(i *instances.Instance) *pty.Bridge {
			bridgeCount.Add(1)
			fb := newFakeBackend()
			fbMu.Lock()
			createdFb = fb
			fbMu.Unlock()
			return pty.NewBridge(i.Path, pty.WithBackend(fb))
		},
	}

	srv := &http.Server{Handler: ws.Handler()}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	var conn1, conn2 *websocket.Conn
	var err1, err2 error

	wg.Add(2)
	go func() {
		defer wg.Done()
		conn1, _, err1 = dialTermWS(t, ctx, port, inst.ID)
	}()
	go func() {
		defer wg.Done()
		conn2, _, err2 = dialTermWS(t, ctx, port, inst.ID)
	}()
	wg.Wait()

	if err1 != nil {
		t.Fatalf("conn1 dial failed: %v", err1)
	}
	if err2 != nil {
		t.Fatalf("conn2 dial failed: %v", err2)
	}

	if got := bridgeCount.Load(); got != 1 {
		t.Fatalf("expected exactly 1 bridge created, got %d", got)
	}

	fbMu.Lock()
	fb := createdFb
	fbMu.Unlock()

	// Both connections can write to the same bridge
	if err := writeRawJSON(ctx, conn1, map[string]any{"type": "data", "data": "c1\r"}); err != nil {
		t.Fatalf("conn1 write: %v", err)
	}
	if err := writeRawJSON(ctx, conn2, map[string]any{"type": "data", "data": "c2\r"}); err != nil {
		t.Fatalf("conn2 write: %v", err)
	}

	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if len(fb.Writes()) >= 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	writes := fb.Writes()
	if len(writes) != 2 {
		t.Fatalf("expected 2 writes, got %d: %v", len(writes), writes)
	}

	_ = conn1.Close(websocket.StatusNormalClosure, "")
	_ = conn2.Close(websocket.StatusNormalClosure, "")

	time.Sleep(50 * time.Millisecond)

	p, _ := inst.Term("1")
	if p == nil {
		t.Fatal("expected instance PTY to be non-nil after disconnect")
	}
	if !p.IsAlive() {
		t.Fatal("expected instance PTY to be alive after disconnect")
	}
}

func TestTerm_PyStr(t *testing.T) {
	tests := []struct {
		input    any
		expected string
	}{
		{nil, "None"},
		{"hello", "hello"},
		{true, "True"},
		{false, "False"},
		{json.Number("42"), "42"},
		{json.Number("-10"), "-10"},
		{json.Number("0"), "0"},
		{json.Number("1.0"), "1.0"},
		{json.Number("1e3"), "1000.0"},
		{json.Number("1.5"), "1.5"},
		{json.Number("-0.0"), "-0.0"},
		{json.Number("1e-05"), "1e-05"},
		{42, "42"},
		{int64(99), "99"},
		{1.0, "1.0"},
		{1.5, "1.5"},
		{[]any{1, 2}, "[1,2]"},
	}

	for _, tt := range tests {
		got := pyStr(tt.input)
		if got != tt.expected {
			t.Errorf("pyStr(%v) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

func TestTerm_ParsePyInt(t *testing.T) {
	tests := []struct {
		input     any
		expected  int
		shouldErr bool
	}{
		{true, 1, false},
		{false, 0, false},
		{json.Number("100"), 100, false},
		{json.Number("-5"), -5, false},
		{json.Number("12.9"), 12, false},
		{json.Number("-12.9"), -12, false},
		{"100", 100, false},
		{"  -42  ", -42, false},
		{"+15", 15, false},
		{10, 10, false},
		{int64(20), 20, false},
		{12.3, 12, false},
		{"1.5", 0, true},
		{"abc", 0, true},
		{"", 0, true},
		{nil, 0, true},
		{[]any{1, 2}, 0, true},
	}

	for _, tt := range tests {
		got, err := parsePyInt(tt.input)
		if tt.shouldErr {
			if err == nil {
				t.Errorf("parsePyInt(%v) expected error, got %d", tt.input, got)
			}
		} else {
			if err != nil {
				t.Errorf("parsePyInt(%v) unexpected error: %v", tt.input, err)
			}
			if got != tt.expected {
				t.Errorf("parsePyInt(%v) = %d, want %d", tt.input, got, tt.expected)
			}
		}
	}
}

func dialTermWSPath(t *testing.T, ctx context.Context, port int, path string) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	opts := &websocket.DialOptions{
		HTTPHeader: http.Header{"Origin": []string{"http://wails.localhost"}},
	}
	return websocket.Dial(ctx, fmt.Sprintf("ws://127.0.0.1:%d%s", port, path), opts)
}

func TestTerm_MultiRoutes_DistinctShells(t *testing.T) {
	dir := t.TempDir()
	mgr := instances.NewManager(filepath.Join(dir, "lango.json"))
	inst, err := mgr.Create(dir)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port

	ws := &WS{
		Mgr:    mgr,
		Port:   port,
		Origin: "http://wails.localhost",
		Log:    applog.New("test"),
		NewBridge: func(i *instances.Instance) *pty.Bridge {
			fb := newFakeBackend()
			return pty.NewBridge(i.Path, pty.WithBackend(fb))
		},
	}

	srv := &http.Server{Handler: ws.Handler()}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// 1. Connect to "1" via old route: /ws/term/{id}
	conn1, _, err := dialTermWSPath(t, ctx, port, fmt.Sprintf("/ws/term/%s", inst.ID))
	if err != nil {
		t.Fatalf("dial conn1 (old route): %v", err)
	}
	defer conn1.Close(websocket.StatusNormalClosure, "")

	// 2. Connect to "1" via new route: /ws/term/{id}/1
	conn2, _, err := dialTermWSPath(t, ctx, port, fmt.Sprintf("/ws/term/%s/1", inst.ID))
	if err != nil {
		t.Fatalf("dial conn2 (new route /1): %v", err)
	}
	defer conn2.Close(websocket.StatusNormalClosure, "")

	// 3. Connect to "2" via new route: /ws/term/{id}/2
	conn3, _, err := dialTermWSPath(t, ctx, port, fmt.Sprintf("/ws/term/%s/2", inst.ID))
	if err != nil {
		t.Fatalf("dial conn3 (new route /2): %v", err)
	}
	defer conn3.Close(websocket.StatusNormalClosure, "")

	var term1 instances.PTY
	for start := time.Now(); time.Since(start) < 2*time.Second; time.Sleep(5 * time.Millisecond) {
		if p, ok := inst.Term("1"); ok && p != nil {
			term1 = p
			break
		}
	}
	if term1 == nil {
		t.Fatal("expected term 1 to be registered")
	}

	var term2 instances.PTY
	for start := time.Now(); time.Since(start) < 2*time.Second; time.Sleep(5 * time.Millisecond) {
		if p, ok := inst.Term("2"); ok && p != nil {
			term2 = p
			break
		}
	}
	if term2 == nil {
		t.Fatal("expected term 2 to be registered")
	}

	if term1 == term2 {
		t.Fatal("expected term 1 and term 2 to be distinct shells")
	}

	terms := inst.Terms()
	if len(terms) != 2 {
		t.Fatalf("expected 2 terms in instance, got %d", len(terms))
	}
}

func TestTerm_InvalidTermID_404(t *testing.T) {
	dir := t.TempDir()
	mgr := instances.NewManager(filepath.Join(dir, "lango.json"))
	inst, err := mgr.Create(dir)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port

	ws := &WS{
		Mgr:    mgr,
		Port:   port,
		Origin: "http://wails.localhost",
		Log:    applog.New("test"),
	}

	srv := &http.Server{Handler: ws.Handler()}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	for _, invalidID := range []string{"6", "x", "0", "10", "abc"} {
		path := fmt.Sprintf("/ws/term/%s/%s", inst.ID, invalidID)
		_, resp, err := dialTermWSPath(t, ctx, port, path)
		if err == nil {
			t.Fatalf("expected dial error for term_id %q", invalidID)
		}
		if resp == nil || resp.StatusCode != http.StatusNotFound {
			t.Fatalf("expected 404 for term_id %q, got %v", invalidID, resp)
		}
		body, _ := io.ReadAll(resp.Body)
		var m map[string]any
		_ = json.Unmarshal(body, &m)
		if m["detail"] == nil {
			t.Errorf("expected json detail in 404 response for term_id %q", invalidID)
		}
	}
}

func TestTerm_Delete_SendsExitAndClosesSocket(t *testing.T) {
	dir := t.TempDir()
	mgr := instances.NewManager(filepath.Join(dir, "lango.json"))
	inst, err := mgr.Create(dir)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	restServer := &Server{
		Mgr: mgr,
		Log: applog.New("test"),
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port

	ws := &WS{
		Mgr:               mgr,
		Port:              port,
		Origin:            "http://wails.localhost",
		Log:               applog.New("test"),
		ExitCheckInterval: 25 * time.Millisecond,
		NewBridge: func(i *instances.Instance) *pty.Bridge {
			fb := newFakeBackend()
			return pty.NewBridge(i.Path, pty.WithBackend(fb))
		},
	}

	srv := &http.Server{Handler: ws.Handler()}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()

	conn, _, err := dialTermWSPath(t, ctx, port, fmt.Sprintf("/ws/term/%s/2", inst.ID))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	var term2 instances.PTY
	for start := time.Now(); time.Since(start) < 2*time.Second; time.Sleep(5 * time.Millisecond) {
		if p, ok := inst.Term("2"); ok && p != nil {
			term2 = p
			break
		}
	}
	if term2 == nil {
		t.Fatal("expected term 2 to be registered")
	}

	// Send DELETE request via REST server
	delReq := httptest.NewRequest("DELETE", fmt.Sprintf("/instances/%s/terms/2", inst.ID), nil)
	delRec := httptest.NewRecorder()
	restServer.Handler().ServeHTTP(delRec, delReq)
	if delRec.Code != 200 {
		t.Fatalf("expected 200 for DELETE term, got %d", delRec.Code)
	}

	// Read from WebSocket: must receive exit message
	msg, err := readJSON(ctx, conn)
	if err != nil {
		t.Fatalf("expected exit message from socket, got error: %v", err)
	}
	if msg["type"] != "exit" {
		t.Fatalf("expected message type 'exit', got %v", msg)
	}

	// Next read must report socket closed
	_, nextErr := readJSON(ctx, conn)
	if nextErr == nil {
		t.Fatal("expected socket to be closed after exit")
	}

	// Term 2 must be removed from instance
	if _, exists := inst.Term("2"); exists {
		t.Fatal("expected term 2 to be removed from instance")
	}
}

type spawnFailBackend struct {
	fakeBackend
}

func (f *spawnFailBackend) Spawn(argv []string, cwd string, env []string, cols, rows int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.alive = false
	return errors.New("simulated spawn failure")
}

func TestTerm_StartFailure_NoExitAndErrorMessage(t *testing.T) {
	dir := t.TempDir()
	mgr := instances.NewManager(filepath.Join(dir, "lango.json"))
	inst, err := mgr.Create(dir)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port

	sfb := &spawnFailBackend{}
	sfb.cond = sync.NewCond(&sfb.mu)

	ws := &WS{
		Mgr:               mgr,
		Port:              port,
		Origin:            "http://wails.localhost",
		Log:               applog.New("test"),
		ExitCheckInterval: 25 * time.Millisecond,
		NewBridge: func(i *instances.Instance) *pty.Bridge {
			return pty.NewBridge(i.Path, pty.WithBackend(sfb))
		},
	}

	srv := &http.Server{Handler: ws.Handler()}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	conn, _, err := dialTermWSPath(t, ctx, port, fmt.Sprintf("/ws/term/%s/1", inst.ID))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	// 1. Should receive error message as data
	msg, err := readJSON(ctx, conn)
	if err != nil {
		t.Fatalf("expected data message with error, got: %v", err)
	}
	if msg["type"] != "data" {
		t.Fatalf("expected type 'data', got %v", msg["type"])
	}
	dataStr, _ := msg["data"].(string)
	if !strings.Contains(dataStr, "simulated spawn failure") {
		t.Fatalf("expected error message in data, got %q", dataStr)
	}

	// 2. Read with short timeout: must NOT receive exit message
	shortCtx, shortCancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer shortCancel()
	_, extraErr := readJSON(shortCtx, conn)
	if extraErr == nil {
		t.Fatal("unexpected message received; expected no exit message")
	}
	if !errors.Is(extraErr, context.DeadlineExceeded) && !strings.Contains(extraErr.Error(), "context deadline exceeded") {
		t.Fatalf("expected timeout waiting for messages, got: %v", extraErr)
	}
}
