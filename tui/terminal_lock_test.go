package tui

import (
	"os"
	"strings"
	"testing"
	"time"
)

// TestTerminalInputHandlerNotHeldUnderLock guards D138 (and its D147 form):
// the terminal must not hold a lock while delivering input to the handler,
// because the handler (the renderer) may write back to the terminal. The
// legacy delivery path runs the handler with no terminal lock held; the raw
// path never touches a lock on delivery at all.
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
	// The handler writes to the terminal, which takes writeMu.
	terminal.setInputHandler(func(string) { terminal.Write("ok") })
	terminal.setupLegacyStdinBuffer()

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

// TestRawTerminalFeedInput covers the D147 raw-input contract: raw chunks
// reassemble into sequences on the consumer goroutine, the flush deadline is
// reported and honored, and negotiation responses are filtered out.
func TestRawTerminalFeedInput(t *testing.T) {
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
	terminal.EnableRawInput()
	terminal.Start(func(string) {}, func() {})
	defer terminal.Stop()

	var dispatched []string
	var rawInterface RawInputTerminal = terminal
	_ = rawInterface
	dispatched = terminal.FeedInput([]byte("a"))
	if strings.Join(dispatched, "") != "a" {
		t.Fatalf("got %q", dispatched)
	}

	// An incomplete sequence buffers and reports a deadline; the consumer
	// flushes it once the deadline passes.
	dispatched = terminal.FeedInput([]byte("\x1b[12"))
	if len(dispatched) != 0 {
		t.Fatalf("emitted early: %q", dispatched)
	}
	deadline, ok := terminal.NextInputFlushDeadline()
	if !ok {
		t.Fatal("no flush deadline reported")
	}
	if got := terminal.FlushPendingInput(); len(got) != 0 {
		t.Fatalf("flushed before the deadline: %q", got)
	}
	_ = deadline
	time.Sleep(60 * time.Millisecond) // > the 50ms sequence timeout
	if got := terminal.FlushPendingInput(); strings.Join(got, "") != "\x1b[12" {
		t.Fatalf("flush = %q", got)
	}

	// A Kitty negotiation response is consumed by the protocol layer and
	// never dispatched to the input handler.
	dispatched = terminal.FeedInput([]byte("\x1b[?1u"))
	for _, sequence := range dispatched {
		if sequence == "\x1b[?1u" {
			t.Fatalf("negotiation response leaked: %q", dispatched)
		}
	}
	_ = inW
}
