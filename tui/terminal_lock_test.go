package tui

import (
	"os"
	"testing"
	"time"
)

// TestTerminalInputHandlerNotHeldUnderLock guards D138: the terminal must not
// hold its mutex while delivering input to the handler, because the handler
// (the renderer) may write back to the terminal, and the render path holds the
// screen lock while writing. Holding t.mu across the handler inverted the lock
// order and deadlocked scrolling.
func TestTerminalInputHandlerNotHeldUnderLock(t *testing.T) {
	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer inR.Close()
	defer inW.Close()
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer outR.Close()
	defer outW.Close()

	terminal := NewProcessTerminal(inR, outW)
	// The handler writes to the terminal, which locks t.mu.
	terminal.inputHandler = func(string) { terminal.Write("ok") }
	terminal.setupStdinBufferLocked()

	done := make(chan struct{})
	go func() {
		defer close(done)
		// Drive the real stdin data path.
		terminal.stdinBuffer.OnData("a")
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("terminal delivered input while holding its mutex (deadlock)")
	}
}
