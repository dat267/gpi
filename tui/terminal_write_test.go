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
