package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"lango/internal/applog"
	"lango/internal/instances"
	"lango/internal/pty"
	"lango/internal/watcher"
)

type eventQueue struct {
	mu     sync.Mutex
	items  []map[string]any
	notify chan struct{}
	closed bool
}

func newEventQueue() *eventQueue {
	return &eventQueue{
		notify: make(chan struct{}, 1),
	}
}

func (q *eventQueue) Push(ev map[string]any) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return
	}
	q.items = append(q.items, ev)
	select {
	case q.notify <- struct{}{}:
	default:
	}
}

func (q *eventQueue) Close() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.closed = true
	select {
	case q.notify <- struct{}{}:
	default:
	}
}

func (q *eventQueue) Pop(ctx context.Context) (map[string]any, bool) {
	for {
		q.mu.Lock()
		if len(q.items) > 0 {
			item := q.items[0]
			q.items = q.items[1:]
			q.mu.Unlock()
			return item, true
		}
		if q.closed {
			q.mu.Unlock()
			return nil, false
		}
		q.mu.Unlock()

		select {
		case <-ctx.Done():
			return nil, false
		case <-q.notify:
		}
	}
}

// WS manages WebSocket endpoints for files and terminals over TCP.
type WS struct {
	Mgr       *instances.Manager
	Log       *applog.Logger
	Port      int
	Origin    string
	RootCheck time.Duration

	// Test hook: notified when a watcher starts.
	OnWatcherStarted func(w *watcher.Watcher)

	// NewBridge instantiates a PTY bridge for an instance. Defaults to pty.NewBridge.
	NewBridge func(inst *instances.Instance) *pty.Bridge
	// PingInterval defines terminal WS ping interval. Defaults to 20 s.
	PingInterval time.Duration
	// ExitCheckInterval defines shell exit check ticker interval. Defaults to 1 s.
	ExitCheckInterval time.Duration

	termMu sync.Mutex
}

// Handler returns the HTTP handler protected by Host and Origin guards.
func (s *WS) Handler() http.Handler {
	mux := http.NewServeMux()

	// GET /ws/files/{id}
	mux.HandleFunc("GET /ws/files/{id}", s.handleFiles)

	// GET /ws/term/{id}
	mux.HandleFunc("GET /ws/term/{id}", s.handleTerm)
	mux.HandleFunc("GET /ws/term/{id}/{term_id}", s.handleTerm)

	return Guard(mux, s.Port, s.Origin, s.Log)
}

func (s *WS) handleFiles(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var inst *instances.Instance
	if s.Mgr != nil {
		inst = s.Mgr.Get(id)
	}
	if inst == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"detail":"forbidden"}`))
		return
	}

	originHost := s.Origin
	if strings.Contains(originHost, "://") {
		if u, err := url.Parse(originHost); err == nil && u.Host != "" {
			originHost = u.Host
		}
	}

	opts := &websocket.AcceptOptions{
		OriginPatterns: []string{originHost},
	}
	conn, err := websocket.Accept(w, r, opts)
	if err != nil {
		return
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	q := newEventQueue()
	wt, err := watcher.Start(inst.Path, func(ev map[string]any) {
		q.Push(ev)
	})
	if err != nil {
		if s.Log != nil {
			s.Log.Warn("watcher.Start failed for %s: %v", inst.Path, err)
		}
		_ = conn.Close(websocket.StatusInternalError, "watcher failed")
		return
	}

	if s.OnWatcherStarted != nil {
		s.OnWatcherStarted(wt)
	}

	inst.AddWatcher(wt)
	defer func() {
		inst.RemoveWatcher(wt)
		wt.Stop()
	}()

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	go func() {
		for {
			_, _, readErr := conn.Read(ctx)
			if readErr != nil {
				wt.Stop()
				q.Close()
				cancel()
				return
			}
		}
	}()

	rootCheckInterval := s.RootCheck
	if rootCheckInterval <= 0 {
		rootCheckInterval = 5 * time.Second
	}

	go func() {
		ticker := time.NewTicker(rootCheckInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				fi, statErr := os.Stat(inst.Path)
				if statErr != nil || !fi.IsDir() {
					q.Push(map[string]any{
						"kind": "root-gone",
						"path": inst.Path,
					})
					return
				}
			}
		}
	}()

	for {
		ev, ok := q.Pop(ctx)
		if !ok {
			break
		}

		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		if encErr := enc.Encode(ev); encErr != nil {
			if s.Log != nil {
				s.Log.Warn("encode error in /ws/files: %v", encErr)
			}
			continue
		}

		msg := bytes.TrimRight(buf.Bytes(), "\r\n")
		if writeErr := conn.Write(ctx, websocket.MessageText, msg); writeErr != nil {
			break
		}

		if kind, _ := ev["kind"].(string); kind == "root-gone" {
			_ = conn.Close(websocket.StatusNormalClosure, "root gone")
			break
		}
	}
}
