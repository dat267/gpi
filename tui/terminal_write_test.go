package tui

import (
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// Console writes run on a dedicated goroutine, so a paused terminal (Windows
// stops draining the pty while a mouse drag-selection is active) can never
// block the caller — the UI loop. Order is preserved; Stop flushes.

func TestProcessTerminalWriteDoesNotBlockOnAPausedConsole(t *testing.T) {
	terminal := NewProcessTerminal(nil, nil)
	release := make(chan struct{})
	terminal.writeFn = func(string) { <-release }

	done := make(chan struct{})
	go func() {
		terminal.Write("frame")
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Write blocked on a paused console")
	}

	close(release)
	terminal.flushWrites()
}

func TestProcessTerminalWritesInOrder(t *testing.T) {
	terminal := NewProcessTerminal(nil, nil)
	var mu sync.Mutex
	var got strings.Builder
	terminal.writeFn = func(data string) {
		mu.Lock()
		got.WriteString(data)
		mu.Unlock()
	}

	var want strings.Builder
	for i := 0; i < 500; i++ {
		chunk := strconv.Itoa(i) + ","
		want.WriteString(chunk)
		terminal.Write(chunk)
	}
	terminal.flushWrites()

	mu.Lock()
	defer mu.Unlock()
	if got.String() != want.String() {
		t.Fatalf("writes out of order or lost:\n got %q\nwant %q", got.String(), want.String())
	}
}

func TestProcessTerminalFlushWaitsForAPausedWriter(t *testing.T) {
	terminal := NewProcessTerminal(nil, nil)
	release := make(chan struct{})
	started := make(chan struct{})
	var once sync.Once
	terminal.writeFn = func(string) {
		once.Do(func() { close(started) })
		<-release
	}

	terminal.Write("x")
	<-started

	flushed := make(chan struct{})
	go func() {
		terminal.flushWrites()
		close(flushed)
	}()
	select {
	case <-flushed:
		t.Fatal("flushWrites returned while the writer was still paused")
	case <-time.After(100 * time.Millisecond):
	}

	close(release)
	select {
	case <-flushed:
	case <-time.After(2 * time.Second):
		t.Fatal("flushWrites did not return after the writer resumed")
	}
}

// A frame is a whole renderer paint. While a terminal is paused, a newer frame
// must replace an earlier queued one instead of piling up behind it — that
// backlog is what the terminal has to ingest all at once on release.

func TestProcessTerminalCoalescesQueuedFrames(t *testing.T) {
	terminal := NewProcessTerminal(nil, nil)
	release := make(chan struct{})
	started := make(chan struct{})
	var once sync.Once
	var mu sync.Mutex
	var got strings.Builder
	terminal.writeFn = func(data string) {
		once.Do(func() { close(started) })
		<-release
		mu.Lock()
		got.WriteString(data)
		mu.Unlock()
	}

	terminal.Write("setup") // durable; the writer takes it and blocks
	<-started

	terminal.BeginFrame()
	terminal.Write("frame-A")
	terminal.EndFrame()
	terminal.BeginFrame()
	terminal.Write("frame-B")
	terminal.EndFrame()

	close(release)
	terminal.flushWrites()

	mu.Lock()
	defer mu.Unlock()
	if got.String() != "setupframe-B" {
		t.Fatalf("got %q, want %q (frame-A must be dropped)", got.String(), "setupframe-B")
	}
}

func TestProcessTerminalFrameCoalescingKeepsDurableWrites(t *testing.T) {
	terminal := NewProcessTerminal(nil, nil)
	release := make(chan struct{})
	started := make(chan struct{})
	var once sync.Once
	var mu sync.Mutex
	var got strings.Builder
	terminal.writeFn = func(data string) {
		once.Do(func() { close(started) })
		<-release
		mu.Lock()
		got.WriteString(data)
		mu.Unlock()
	}

	terminal.Write("setup")
	<-started

	terminal.BeginFrame()
	terminal.Write("frame-A")
	terminal.EndFrame()
	terminal.Write("mode") // durable write between two frames
	terminal.BeginFrame()
	terminal.Write("frame-B")
	terminal.EndFrame()

	close(release)
	terminal.flushWrites()

	mu.Lock()
	defer mu.Unlock()
	// frame-A is dropped, the durable write survives, in order.
	if got.String() != "setupmodeframe-B" {
		t.Fatalf("got %q, want %q", got.String(), "setupmodeframe-B")
	}
}
