package pty

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"lango/internal/instances"
	"lango/internal/pyutf8"

	"golang.org/x/sys/windows"
)

// Ensure Bridge satisfies instances.PTY at compile time.
var _ instances.PTY = (*Bridge)(nil)

// Message represents a generic message map.
type Message = map[string]any

// Sub represents a subscriber to PTY terminal output and events.
type Sub struct {
	mu      sync.Mutex
	queue   []Message
	notify  chan struct{}
	closed  bool
	onClose func(*Sub)
}

func newSub(onClose func(*Sub)) *Sub {
	return &Sub{
		notify:  make(chan struct{}, 1),
		onClose: onClose,
	}
}

func (s *Sub) enqueue(msg Message) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.queue = append(s.queue, msg)
	s.mu.Unlock()

	select {
	case s.notify <- struct{}{}:
	default:
	}
}

// Next waits for the next message. Returns (msg, true), or (nil, false) if closed or ctx is done.
func (s *Sub) Next(ctx context.Context) (Message, bool) {
	for {
		s.mu.Lock()
		if len(s.queue) > 0 {
			msg := s.queue[0]
			s.queue = s.queue[1:]
			s.mu.Unlock()
			return msg, true
		}
		if s.closed {
			s.mu.Unlock()
			return nil, false
		}
		s.mu.Unlock()

		select {
		case <-ctx.Done():
			return nil, false
		case <-s.notify:
		}
	}
}

// Close closes the subscriber.
func (s *Sub) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	s.mu.Unlock()

	select {
	case s.notify <- struct{}{}:
	default:
	}

	if s.onClose != nil {
		s.onClose(s)
	}
}

// BackendFactory creates a Backend instance.
type BackendFactory func(dllDir string, logger Logger) (Backend, error)

// Option configures a Bridge instance.
type Option func(*Bridge)

// WithBackend sets a pre-spawned or mock backend.
func WithBackend(b Backend) Option {
	return func(br *Bridge) {
		br.backend = b
	}
}

// WithBackendFactory sets a custom backend factory.
func WithBackendFactory(f BackendFactory) Option {
	return func(br *Bridge) {
		br.factory = f
	}
}

// WithClock sets a custom clock.
func WithClock(c Clock) Option {
	return func(br *Bridge) {
		br.clock = c
	}
}

// WithDllDir sets the directory for conpty.dll.
func WithDllDir(dir string) Option {
	return func(br *Bridge) {
		br.dllDir = dir
	}
}

// WithLogger sets the logger.
func WithLogger(l Logger) Option {
	return func(br *Bridge) {
		br.logger = l
	}
}

// WithScrollbackLimit sets a custom scrollback buffer limit.
func WithScrollbackLimit(limit int) Option {
	return func(br *Bridge) {
		br.scrollbackLimit = limit
	}
}

// Bridge bridges a terminal session to subscribers and manages process state.
type Bridge struct {
	cwd             string
	cols, rows      int
	backend         Backend
	factory         BackendFactory
	clock           Clock
	dllDir          string
	logger          Logger
	scrollbackLimit int

	mu                 sync.Mutex
	buf                []byte
	alive              bool
	agent              string
	used               bool
	inAltScreen        bool
	lastOutput         float64
	lastActivity       float64
	lastInput          float64
	lastResize         float64
	subscribers        []*Sub
	onFirstSubscribe   func()
	firstSubscribeOnce sync.Once
	terminated         bool
	stopPoll           chan struct{}
}

// NewBridge creates a new Bridge.
func NewBridge(cwd string, opts ...Option) *Bridge {
	c := NewRealClock()
	now := c.Now()
	b := &Bridge{
		cwd:             cwd,
		cols:            120,
		rows:            30,
		clock:           c,
		scrollbackLimit: 10 * 1024 * 1024,
		lastActivity:    now,
		stopPoll:        make(chan struct{}),
	}
	for _, opt := range opts {
		opt(b)
	}
	return b
}

func splitIncompleteUTF8(b []byte) ([]byte, []byte) {
	n := len(b)
	if n == 0 {
		return b, nil
	}
	b0 := b[n-1]
	if b0 >= 0xC2 && b0 <= 0xDF {
		return b[:n-1], b[n-1:]
	}
	if b0 >= 0xE0 && b0 <= 0xEF {
		return b[:n-1], b[n-1:]
	}
	if b0 >= 0xF0 && b0 <= 0xF4 {
		return b[:n-1], b[n-1:]
	}

	if n >= 2 {
		b0 = b[n-2]
		b1 := b[n-1]
		if b0 >= 0xE0 && b0 <= 0xEF {
			b1Min, b1Max := byte(0x80), byte(0xBF)
			if b0 == 0xE0 {
				b1Min = 0xA0
			} else if b0 == 0xED {
				b1Max = 0x9F
			}
			if b1 >= b1Min && b1 <= b1Max {
				return b[:n-2], b[n-2:]
			}
		}
		if b0 >= 0xF0 && b0 <= 0xF4 {
			b1Min, b1Max := byte(0x80), byte(0xBF)
			if b0 == 0xF0 {
				b1Min = 0x90
			} else if b0 == 0xF4 {
				b1Max = 0x8F
			}
			if b1 >= b1Min && b1 <= b1Max {
				return b[:n-2], b[n-2:]
			}
		}
	}

	if n >= 3 {
		b0 = b[n-3]
		b1 := b[n-2]
		b2 := b[n-1]
		if b0 >= 0xF0 && b0 <= 0xF4 {
			b1Min, b1Max := byte(0x80), byte(0xBF)
			if b0 == 0xF0 {
				b1Min = 0x90
			} else if b0 == 0xF4 {
				b1Max = 0x8F
			}
			if b1 >= b1Min && b1 <= b1Max && b2 >= 0x80 && b2 <= 0xBF {
				return b[:n-3], b[n-3:]
			}
		}
	}

	return b, nil
}

// Start launches the PTY process and starts the background reader.
func (b *Bridge) Start() error {
	if b.backend == nil {
		if b.factory != nil {
			backend, err := b.factory(b.dllDir, b.logger)
			if err != nil {
				errMsg := fmt.Sprintf("\r\n\x1b[31m[Lango] Error starting terminal: %v\x1b[0m\r\n", err)
				b.mu.Lock()
				b.buf = append(b.buf, []byte(errMsg)...)
				b.alive = false
				b.mu.Unlock()
				return err
			}
			b.backend = backend
		} else {
			b.backend = NewConptyBackend(b.dllDir, b.logger)
		}
	}

	env := BuildEnv()
	shell := ResolveShell()
	cols, rows := 120, 30
	if b.cols > 0 {
		cols = b.cols
	}
	if b.rows > 0 {
		rows = b.rows
	}

	if err := b.backend.Spawn(shell, b.cwd, env, cols, rows); err != nil {
		errMsg := fmt.Sprintf("\r\n\x1b[31m[Lango] Error starting terminal: %v\x1b[0m\r\n", err)
		b.mu.Lock()
		b.buf = append(b.buf, []byte(errMsg)...)
		b.alive = false
		b.mu.Unlock()
		return err
	}

	b.mu.Lock()
	b.alive = true
	b.mu.Unlock()

	go b.readLoop()
	return nil
}

func (b *Bridge) readLoop() {
	defer func() {
		b.mu.Lock()
		b.alive = false
		select {
		case <-b.stopPoll:
		default:
			close(b.stopPoll)
		}
		b.mu.Unlock()
	}()

	var carry []byte
	raw := make([]byte, 4096)

	for {
		n, err := b.backend.Read(raw)
		if n > 0 {
			chunk := append(carry, raw[:n]...)
			validChunk, nextCarry := splitIncompleteUTF8(chunk)
			carry = nextCarry

			if len(validChunk) > 0 {
				now := b.clock.Now()
				text := pyutf8.DecodeReplace(validChunk)
				textBytes := []byte(text)

				b.mu.Lock()
				b.noteOutputLocked(now)

				// Alt screen tracking
				hc := bytes.Count(validChunk, []byte("\x1b[?1049h"))
				lc := bytes.Count(validChunk, []byte("\x1b[?1049l"))
				if hc > lc {
					b.inAltScreen = true
				} else if lc > hc {
					b.inAltScreen = false
				}

				b.buf = append(b.buf, textBytes...)
				if len(b.buf) > b.scrollbackLimit {
					trimTo := len(b.buf) - b.scrollbackLimit
					nl := bytes.LastIndexByte(b.buf[:trimTo], '\n')
					if nl >= 0 {
						trimTo = nl + 1
					} else {
						for trimTo < len(b.buf) && (b.buf[trimTo]&0xC0) == 0x80 {
							trimTo++
						}
					}
					b.buf = b.buf[trimTo:]
				}

				msg := Message{"type": "data", "data": text}
				for _, sub := range b.subscribers {
					sub.enqueue(msg)
				}
				b.mu.Unlock()
			}
		}

		if err != nil {
			if len(carry) > 0 {
				now := b.clock.Now()
				text := pyutf8.DecodeReplace(carry)
				textBytes := []byte(text)

				b.mu.Lock()
				b.noteOutputLocked(now)
				b.buf = append(b.buf, textBytes...)
				msg := Message{"type": "data", "data": text}
				for _, sub := range b.subscribers {
					sub.enqueue(msg)
				}
				b.mu.Unlock()
				carry = nil
			}
			break
		}
	}
}

func (b *Bridge) noteInputLocked(s string, now float64) {
	b.lastInput = now
	b.lastActivity = now
	if strings.ContainsRune(s, '\r') || strings.ContainsRune(s, '\n') {
		b.used = true
	}
}

func (b *Bridge) noteOutputLocked(now float64) {
	b.lastActivity = now
	maxIn := b.lastInput
	if b.lastResize > maxIn {
		maxIn = b.lastResize
	}
	if now-maxIn >= EchoWindow {
		b.lastOutput = now
	}
}

func (b *Bridge) removeSubscriber(s *Sub) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for i, sub := range b.subscribers {
		if sub == s {
			b.subscribers = append(b.subscribers[:i], b.subscribers[i+1:]...)
			break
		}
	}
}

// SetOnFirstSubscribe registers a callback triggered on the first subscriber.
func (b *Bridge) SetOnFirstSubscribe(fn func()) {
	b.mu.Lock()
	b.onFirstSubscribe = fn
	b.mu.Unlock()
}

// SnapshotAndSubscribe atomically takes a snapshot of the buffer and adds a subscriber.
func (b *Bridge) SnapshotAndSubscribe() (string, bool, *Sub) {
	b.mu.Lock()

	var firstHook func()
	b.firstSubscribeOnce.Do(func() {
		firstHook = b.onFirstSubscribe
	})

	replay := pyutf8.DecodeReplace(b.buf)
	inAlt := b.inAltScreen

	sub := newSub(b.removeSubscriber)
	b.subscribers = append(b.subscribers, sub)
	b.mu.Unlock()

	if firstHook != nil {
		go firstHook()
	}

	return replay, inAlt, sub
}

// Broadcast sends a message to all active subscribers.
func (b *Bridge) Broadcast(msg Message) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, sub := range b.subscribers {
		sub.enqueue(msg)
	}
}

// SetAgent sets the active agent name.
func (b *Bridge) SetAgent(a string) {
	b.mu.Lock()
	b.agent = a
	b.mu.Unlock()
}

// Agent returns the active agent name.
func (b *Bridge) Agent() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.agent
}

// Write writes data to the PTY.
func (b *Bridge) Write(s string) error {
	now := b.clock.Now()
	b.mu.Lock()
	b.noteInputLocked(s, now)
	b.mu.Unlock()

	if b.backend == nil {
		return errors.New("backend not initialized")
	}
	err := b.backend.Write(s)
	if err != nil {
		errMsg := fmt.Sprintf("\r\n\x1b[31m[Lango] PTY write error: %v\x1b[0m\r\n", err)
		b.Broadcast(Message{"type": "data", "data": errMsg})
	}
	return err
}

// Resize resizes the PTY dimensions.
func (b *Bridge) Resize(cols, rows int) error {
	now := b.clock.Now()
	b.mu.Lock()
	b.lastResize = now
	if cols < 1 {
		cols = 1
	}
	if rows < 1 {
		rows = 1
	}
	b.cols = cols
	b.rows = rows
	backend := b.backend
	b.mu.Unlock()

	if backend == nil {
		return nil
	}
	return backend.Resize(cols, rows)
}

// State returns the current session state.
func (b *Bridge) State() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.clock.Now()
	alive := b.alive && b.backend != nil && b.backend.Alive()
	return ComputeState(alive, b.agent, b.used, b.lastOutput, now)
}

// IdleS returns the idle seconds since the last activity.
func (b *Bridge) IdleS() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return int(b.clock.Now() - b.lastActivity)
}

// IsAlive returns whether the PTY is alive.
func (b *Bridge) IsAlive() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.alive && b.backend != nil && b.backend.Alive()
}

// Pid returns the process ID of the spawned shell.
func (b *Bridge) Pid() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.backend == nil {
		return 0
	}
	return b.backend.Pid()
}

// Terminate terminates the PTY session and process tree.
func (b *Bridge) Terminate() {
	b.mu.Lock()
	if b.terminated {
		b.mu.Unlock()
		return
	}
	b.terminated = true
	b.alive = false
	select {
	case <-b.stopPoll:
	default:
		close(b.stopPoll)
	}
	pid := 0
	if b.backend != nil {
		pid = b.backend.Pid()
	}
	b.mu.Unlock()

	if pid > 0 {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "taskkill", "/F", "/T", "/PID", strconv.Itoa(pid))
		cmd.SysProcAttr = &windows.SysProcAttr{
			CreationFlags: windows.CREATE_NO_WINDOW,
		}
		_ = cmd.Run()
	}

	if b.backend != nil {
		b.backend.Close()
	}
}
