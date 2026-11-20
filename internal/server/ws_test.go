package server

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"golang.org/x/sys/windows"

	"lango/internal/applog"
	"lango/internal/instances"
	"lango/internal/watcher"
)

func TestMain(m *testing.M) {
	if portStr := os.Getenv("TEST_OCCUPY_PORT"); portStr != "" {
		ln, err := net.Listen("tcp", "127.0.0.1:"+portStr)
		if err != nil {
			fmt.Fprintf(os.Stderr, "child listen error: %v\n", err)
			os.Exit(2)
		}
		defer ln.Close()
		fmt.Println("READY")
		_ = os.Stdout.Sync()
		time.Sleep(30 * time.Second)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestPort_Deterministic(t *testing.T) {
	if p := Port("Lango"); p != 23968 {
		t.Fatalf("Port(Lango) = %d, expected 23968", p)
	}
	if p := Port("Lango"); p != 23968 {
		t.Fatalf("Port(Lango) = %d, expected 23968", p)
	}
}

func TestWS_GuardAndPaths(t *testing.T) {
	dir := t.TempDir()
	mgr := instances.NewManager(filepath.Join(dir, "lango.json"))
	inst, err := mgr.Create(dir)
	if err != nil {
		t.Fatalf("failed to create instance: %v", err)
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

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// 1. Wrong Host -> 403, no upgrade
	{
		req, _ := http.NewRequest("GET", fmt.Sprintf("http://127.0.0.1:%d/ws/files/%s", port, inst.ID), nil)
		req.Host = fmt.Sprintf("127.0.0.1:%d", port+1)
		req.Header.Set("Origin", "http://wails.localhost")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("wrong host request failed: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("expected 403 on wrong host, got %d", resp.StatusCode)
		}
		body, _ := io.ReadAll(resp.Body)
		if !strings.Contains(string(body), `"forbidden"`) {
			t.Fatalf("expected forbidden JSON, got %s", string(body))
		}

		// Also verify via websocket.Dial
		opts := &websocket.DialOptions{
			Host:       fmt.Sprintf("127.0.0.1:%d", port+1),
			HTTPHeader: http.Header{"Origin": []string{"http://wails.localhost"}},
		}
		_, dialResp, dialErr := websocket.Dial(ctx, fmt.Sprintf("ws://127.0.0.1:%d/ws/files/%s", port, inst.ID), opts)
		if dialErr == nil {
			t.Fatal("expected dial error on wrong host")
		}
		if dialResp == nil || dialResp.StatusCode != http.StatusForbidden {
			t.Fatalf("expected 403 dialResp on wrong host, got %v", dialResp)
		}
	}

	// 2. Wrong Origin -> 403
	{
		req, _ := http.NewRequest("GET", fmt.Sprintf("http://127.0.0.1:%d/ws/files/%s", port, inst.ID), nil)
		req.Header.Set("Origin", "http://evil.com")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("wrong origin request failed: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("expected 403 on wrong origin, got %d", resp.StatusCode)
		}

		opts := &websocket.DialOptions{
			HTTPHeader: http.Header{"Origin": []string{"http://evil.com"}},
		}
		_, dialResp, dialErr := websocket.Dial(ctx, fmt.Sprintf("ws://127.0.0.1:%d/ws/files/%s", port, inst.ID), opts)
		if dialErr == nil {
			t.Fatal("expected dial error on wrong origin")
		}
		if dialResp == nil || dialResp.StatusCode != http.StatusForbidden {
			t.Fatalf("expected 403 on wrong origin, got %v", dialResp)
		}
	}

	// 3. Missing Origin -> 403
	{
		req, _ := http.NewRequest("GET", fmt.Sprintf("http://127.0.0.1:%d/ws/files/%s", port, inst.ID), nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("missing origin request failed: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("expected 403 on missing origin, got %d", resp.StatusCode)
		}

		opts := &websocket.DialOptions{}
		_, dialResp, dialErr := websocket.Dial(ctx, fmt.Sprintf("ws://127.0.0.1:%d/ws/files/%s", port, inst.ID), opts)
		if dialErr == nil {
			t.Fatal("expected dial error on missing origin")
		}
		if dialResp == nil || dialResp.StatusCode != http.StatusForbidden {
			t.Fatalf("expected 403 on missing origin, got %v", dialResp)
		}
	}

	// 4. Unknown id -> 403 no upgrade
	{
		req, _ := http.NewRequest("GET", fmt.Sprintf("http://127.0.0.1:%d/ws/files/unknown-id", port), nil)
		req.Header.Set("Origin", "http://wails.localhost")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("unknown id request failed: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("expected 403 on unknown id, got %d", resp.StatusCode)
		}

		opts := &websocket.DialOptions{
			HTTPHeader: http.Header{"Origin": []string{"http://wails.localhost"}},
		}
		_, dialResp, dialErr := websocket.Dial(ctx, fmt.Sprintf("ws://127.0.0.1:%d/ws/files/unknown-id", port), opts)
		if dialErr == nil {
			t.Fatal("expected dial error on unknown id")
		}
		if dialResp == nil || dialResp.StatusCode != http.StatusForbidden {
			t.Fatalf("expected 403 without upgrade on unknown id, got %v", dialResp)
		}
	}

	// 5. Other path (e.g. /tree) -> 404
	{
		req, _ := http.NewRequest("GET", fmt.Sprintf("http://127.0.0.1:%d/tree", port), nil)
		req.Header.Set("Origin", "http://wails.localhost")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("other path request failed: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("expected 404 on /tree, got %d", resp.StatusCode)
		}
	}
}

func TestWS_Files(t *testing.T) {
	dir := t.TempDir()
	mgr := instances.NewManager(filepath.Join(dir, "lango.json"))
	inst, err := mgr.Create(dir)
	if err != nil {
		t.Fatalf("create instance: %v", err)
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

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	opts := &websocket.DialOptions{
		HTTPHeader: http.Header{"Origin": []string{"http://wails.localhost"}},
	}
	conn, _, err := websocket.Dial(ctx, fmt.Sprintf("ws://127.0.0.1:%d/ws/files/%s", port, inst.ID), opts)
	if err != nil {
		t.Fatalf("websocket dial failed: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	// Allow Windows filesystem observer to attach
	time.Sleep(200 * time.Millisecond)

	// Create a file in instance dir
	filePath := filepath.Join(dir, "x.txt")
	if err := os.WriteFile(filePath, []byte("hello"), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	foundCreated := false
	for i := 0; i < 10; i++ {
		typ, data, err := conn.Read(ctx)
		if err != nil {
			t.Fatalf("read message: %v", err)
		}
		if typ != websocket.MessageText {
			t.Fatalf("expected text message, got %v", typ)
		}
		var ev map[string]any
		if err := json.Unmarshal(data, &ev); err != nil {
			t.Fatalf("unmarshal json: %v", err)
		}
		if ev["kind"] == "created" && ev["path"] == "x.txt" {
			foundCreated = true
			break
		}
	}

	if !foundCreated {
		t.Fatalf("did not receive created event for x.txt")
	}
}

func TestWS_RootGone(t *testing.T) {
	baseDir := t.TempDir()
	dir := filepath.Join(baseDir, "watched_sub")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	mgr := instances.NewManager(filepath.Join(baseDir, "lango.json"))
	inst, err := mgr.Create(dir)
	if err != nil {
		t.Fatalf("create instance: %v", err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port

	ws := &WS{
		Mgr:       mgr,
		Port:      port,
		Origin:    "http://wails.localhost",
		RootCheck: 100 * time.Millisecond,
		Log:       applog.New("test"),
	}

	srv := &http.Server{Handler: ws.Handler()}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	opts := &websocket.DialOptions{
		HTTPHeader: http.Header{"Origin": []string{"http://wails.localhost"}},
	}
	conn, _, err := websocket.Dial(ctx, fmt.Sprintf("ws://127.0.0.1:%d/ws/files/%s", port, inst.ID), opts)
	if err != nil {
		t.Fatalf("websocket dial failed: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	// Delete the instance directory
	if err := os.RemoveAll(dir); err != nil {
		t.Fatalf("remove dir: %v", err)
	}

	// Client must receive {"kind":"root-gone","path":<inst.Path>}
	var gotRootGone bool
	for {
		typ, data, readErr := conn.Read(ctx)
		if readErr != nil {
			break
		}
		if typ != websocket.MessageText {
			t.Fatalf("expected text message, got %v", typ)
		}
		var ev map[string]any
		if err := json.Unmarshal(data, &ev); err != nil {
			t.Fatalf("unmarshal json: %v", err)
		}
		if ev["kind"] == "root-gone" && ev["path"] == inst.Path {
			gotRootGone = true
			break
		}
	}
	if !gotRootGone {
		t.Fatalf("did not receive root-gone for %s", inst.Path)
	}

	// Verify server closes connection normally
	_, _, err = conn.Read(ctx)
	if err == nil {
		t.Fatal("expected server to close connection after root-gone")
	}
}

func TestWS_D9b_WatcherStoppedOnClose(t *testing.T) {
	dir := t.TempDir()
	mgr := instances.NewManager(filepath.Join(dir, "lango.json"))
	inst, err := mgr.Create(dir)
	if err != nil {
		t.Fatalf("create instance: %v", err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port

	var startedWatcher *watcher.Watcher
	watcherStarted := make(chan struct{})

	ws := &WS{
		Mgr:    mgr,
		Port:   port,
		Origin: "http://wails.localhost",
		Log:    applog.New("test"),
		OnWatcherStarted: func(w *watcher.Watcher) {
			startedWatcher = w
			close(watcherStarted)
		},
	}

	srv := &http.Server{Handler: ws.Handler()}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	opts := &websocket.DialOptions{
		HTTPHeader: http.Header{"Origin": []string{"http://wails.localhost"}},
	}
	conn, _, err := websocket.Dial(ctx, fmt.Sprintf("ws://127.0.0.1:%d/ws/files/%s", port, inst.ID), opts)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}

	select {
	case <-watcherStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("watcher did not start")
	}

	// Close client connection and measure time until watcher stops
	start := time.Now()
	_ = conn.Close(websocket.StatusNormalClosure, "client closing")

	select {
	case <-startedWatcher.Done():
		elapsed := time.Since(start)
		if elapsed > 100*time.Millisecond {
			t.Fatalf("watcher stopped in %v, expected <= 100ms", elapsed)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("watcher did not stop after client closed")
	}
}

type fakeKiller struct{}

func (f *fakeKiller) FindListener(port int) (int, error) {
	return 0, nil
}

func (f *fakeKiller) Kill(pid int) error {
	return nil
}

func TestListen_Fallback(t *testing.T) {
	// Find a free port P where P+1 is also free
	var port int
	var ln1 net.Listener
	for p := 32000; p < 42000; p++ {
		l1, err1 := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p))
		if err1 != nil {
			continue
		}
		l2, err2 := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p+1))
		if err2 != nil {
			l1.Close()
			continue
		}
		_ = l2.Close()
		ln1 = l1
		port = p
		break
	}
	if ln1 == nil {
		t.Fatal("could not find test port pair")
	}
	defer ln1.Close()

	ln2, boundPort, err := Listen(port, &fakeKiller{})
	if err != nil {
		t.Fatalf("Listen failed: %v", err)
	}
	defer ln2.Close()

	if boundPort != port+1 {
		t.Fatalf("expected boundPort %d, got %d", port+1, boundPort)
	}
}

func TestListen_KillChild(t *testing.T) {
	tmpLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to find free port: %v", err)
	}
	port := tmpLn.Addr().(*net.TCPAddr).Port
	_ = tmpLn.Close()

	cmd := exec.Command(os.Args[0], "-test.run=TestMain")
	cmd.Env = append(os.Environ(), fmt.Sprintf("TEST_OCCUPY_PORT=%d", port))
	cmd.SysProcAttr = &windows.SysProcAttr{
		CreationFlags: windows.CREATE_NO_WINDOW,
		HideWindow:    true,
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start child: %v", err)
	}
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	})

	reader := bufio.NewReader(stdout)
	line, err := reader.ReadString('\n')
	if err != nil || !strings.Contains(line, "READY") {
		t.Fatalf("failed to read READY from child: %v, line: %q", err, line)
	}

	ln, boundPort, err := Listen(port, &RealPortKiller{})
	if err != nil {
		t.Fatalf("Listen failed: %v", err)
	}
	defer ln.Close()

	if boundPort != port {
		t.Fatalf("expected boundPort %d, got %d", port, boundPort)
	}

	waitErr := cmd.Wait()
	if cmd.ProcessState == nil || !cmd.ProcessState.Exited() {
		t.Fatalf("child process did not exit: %v", waitErr)
	}
}
