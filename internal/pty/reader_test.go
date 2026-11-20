package pty_test

import (
	"context"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"lango/internal/pty"
)

type chunkBackend struct {
	mu     sync.Mutex
	chunks [][]byte
	alive  bool
	notify chan struct{}
}

func newChunkBackend(chunks [][]byte) *chunkBackend {
	return &chunkBackend{
		chunks: chunks,
		alive:  true,
		notify: make(chan struct{}, len(chunks)+10),
	}
}

func (c *chunkBackend) pushChunk(chunk []byte) {
	c.mu.Lock()
	c.chunks = append(c.chunks, chunk)
	c.mu.Unlock()
	select {
	case c.notify <- struct{}{}:
	default:
	}
}

func (c *chunkBackend) Spawn(argv []string, cwd string, env []string, cols, rows int) error {
	return nil
}

func (c *chunkBackend) Read(p []byte) (int, error) {
	for {
		c.mu.Lock()
		if len(c.chunks) > 0 {
			chunk := c.chunks[0]
			c.chunks = c.chunks[1:]
			c.mu.Unlock()
			n := copy(p, chunk)
			return n, nil
		}
		alive := c.alive
		c.mu.Unlock()

		if !alive {
			return 0, io.EOF
		}

		select {
		case <-c.notify:
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func (c *chunkBackend) Write(s string) error { return nil }
func (c *chunkBackend) Resize(cols, rows int) error { return nil }
func (c *chunkBackend) Alive() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.alive
}
func (c *chunkBackend) Pid() int { return 1234 }
func (c *chunkBackend) Close() {
	c.mu.Lock()
	c.alive = false
	c.mu.Unlock()
	select {
	case c.notify <- struct{}{}:
	default:
	}
}

func TestReaderUTF8Carry(t *testing.T) {
	// 'ñ' is 0xC3 0xB1
	// '🎉' is 0xF0 0x9F 0x8E 0x89
	chunks := [][]byte{
		[]byte("hello "),
		{0xC3},                   // First half of 'ñ'
		{0xB1, ' ', 0xF0, 0x9F}, // Second half of 'ñ', space, first 2 bytes of '🎉'
		{0x8E, 0x89, '!'},       // Last 2 bytes of '🎉', '!'
	}

	fb := newChunkBackend(chunks)
	bridge := pty.NewBridge("C:\\tmp", pty.WithBackend(fb))
	_ = bridge.Start()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, _, sub := bridge.SnapshotAndSubscribe()
	defer sub.Close()

	var received strings.Builder
	for {
		msg, ok := sub.Next(ctx)
		if !ok {
			break
		}
		if data, ok := msg["data"].(string); ok {
			received.WriteString(data)
			if strings.Contains(received.String(), "!") {
				break
			}
		}
	}

	expected := "hello ñ 🎉!"
	if received.String() != expected {
		t.Fatalf("expected %q, got %q", expected, received.String())
	}
}

func TestReaderInvalidUTF8ByteMidChunk(t *testing.T) {
	chunks := [][]byte{
		{'a', 0xFF, 'b'},
	}
	fb := newChunkBackend(chunks)
	bridge := pty.NewBridge("C:\\tmp", pty.WithBackend(fb))
	_ = bridge.Start()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, _, sub := bridge.SnapshotAndSubscribe()
	defer sub.Close()

	msg, ok := sub.Next(ctx)
	if !ok {
		t.Fatal("expected message")
	}
	data := msg["data"].(string)
	expected := "a\uFFFDb"
	if data != expected {
		t.Fatalf("expected %q, got %q", expected, data)
	}
}

func TestReaderAltScreen(t *testing.T) {
	chunks := [][]byte{
		[]byte("normal\x1b[?1049h"), // enter alt screen
		[]byte("in alt"),
		[]byte("\x1b[?1049l"),       // exit alt screen
		[]byte("tie: \x1b[?1049h\x1b[?1049l"), // tie -> unchanged
	}

	fb := newChunkBackend(chunks)
	bridge := pty.NewBridge("C:\\tmp", pty.WithBackend(fb))
	_ = bridge.Start()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, _, sub := bridge.SnapshotAndSubscribe()
	defer sub.Close()

	// Wait for 4 messages
	for i := 0; i < 4; i++ {
		_, ok := sub.Next(ctx)
		if !ok {
			t.Fatalf("failed at message %d", i)
		}
	}

	_, inAlt, sub2 := bridge.SnapshotAndSubscribe()
	defer sub2.Close()
	if inAlt {
		t.Fatal("expected inAlt to be false after exit and tie")
	}
}

func waitReplay(b *pty.Bridge) string {
	deadline := time.Now().Add(2 * time.Second)
	for {
		replay, _, sub := b.SnapshotAndSubscribe()
		sub.Close()
		if replay != "" || time.Now().After(deadline) {
			return replay
		}
		time.Sleep(10 * time.Millisecond)
	}
}
func TestReaderTrimWithAndWithoutNewline(t *testing.T) {
	limit := 50

	// 1. With newline
	chunk1 := []byte("line 1\nline 2\nline 3 long text exceeding limit\n")
	fb1 := newChunkBackend([][]byte{chunk1})
	bridge1 := pty.NewBridge("C:\\tmp", pty.WithBackend(fb1), pty.WithScrollbackLimit(limit))
	_ = bridge1.Start()

	replay1 := waitReplay(bridge1)

	if len(replay1) > limit {
		t.Fatalf("replay1 length %d exceeds limit %d", len(replay1), limit)
	}
	if !strings.HasPrefix(replay1, "line") && !strings.Contains(replay1, "\n") {
		t.Fatalf("unexpected replay1: %q", replay1)
	}

	// 2. Without newline
	chunk2 := []byte("123456789012345678901234567890123456789012345678901234567890")
	fb2 := newChunkBackend([][]byte{chunk2})
	bridge2 := pty.NewBridge("C:\\tmp", pty.WithBackend(fb2), pty.WithScrollbackLimit(limit))
	_ = bridge2.Start()

	replay2 := waitReplay(bridge2)

	if len(replay2) > limit {
		t.Fatalf("replay2 length %d exceeds limit %d", len(replay2), limit)
	}
}

func TestSnapshotAndSubscribeD9aAtomicity(t *testing.T) {
	fb := newChunkBackend(nil)
	bridge := pty.NewBridge("C:\\tmp", pty.WithBackend(fb))
	_ = bridge.Start()

	const totalChunks = 50
	var wg sync.WaitGroup

	// Writer goroutine pushing chunks
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < totalChunks; i++ {
			fb.pushChunk([]byte("c"))
			time.Sleep(1 * time.Millisecond)
		}
		fb.Close()
	}()

	// Wait slightly so some chunks are already written
	time.Sleep(10 * time.Millisecond)

	replay, _, sub := bridge.SnapshotAndSubscribe()
	defer sub.Close()

	var fromSub strings.Builder
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	for {
		msg, ok := sub.Next(ctx)
		if !ok {
			break
		}
		if data, ok := msg["data"].(string); ok {
			fromSub.WriteString(data)
		}
		if len(replay)+fromSub.Len() == totalChunks {
			break
		}
	}

	wg.Wait()

	total := len(replay) + fromSub.Len()
	if total != totalChunks {
		t.Fatalf("expected exact count %d, got %d (replay: %d, sub: %d)",
			totalChunks, total, len(replay), fromSub.Len())
	}
}
