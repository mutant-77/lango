//go:build windows

package watcher

import (
	"sync"

	"golang.org/x/sys/windows"
)

type Watcher struct {
	root          string
	handler       *Handler
	handle        windows.Handle
	stopEvent     windows.Handle
	ovEvent       windows.Handle
	done          chan struct{}
	goroutineDone chan struct{}
	stopOnce      sync.Once
	doneOnce      sync.Once
	mu            sync.Mutex
	closed        bool
}

func Start(root string, cb func(map[string]any)) (*Watcher, error) {
	h, err := openDirHandle(root)
	if err != nil {
		return nil, err
	}

	stopEvent, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		_ = windows.CloseHandle(h)
		return nil, err
	}

	ovEvent, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		_ = windows.CloseHandle(stopEvent)
		_ = windows.CloseHandle(h)
		return nil, err
	}

	w := &Watcher{
		root:          root,
		handler:       NewHandler(root, cb),
		handle:        h,
		stopEvent:     stopEvent,
		ovEvent:       ovEvent,
		done:          make(chan struct{}),
		goroutineDone: make(chan struct{}),
	}

	go w.readLoop()

	return w, nil
}

func (w *Watcher) Done() <-chan struct{} {
	return w.done
}

func (w *Watcher) Stop() {
	w.stopOnce.Do(func() {
		w.mu.Lock()
		if !w.closed {
			_ = windows.SetEvent(w.stopEvent)
			_ = windows.CancelIoEx(w.handle, nil)
		}
		w.mu.Unlock()
	})
	<-w.goroutineDone
}

func (w *Watcher) closeHandles() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.closed {
		w.closed = true
		_ = windows.CloseHandle(w.handle)
		_ = windows.CloseHandle(w.stopEvent)
		_ = windows.CloseHandle(w.ovEvent)
	}
}
