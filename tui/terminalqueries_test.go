package tui

import (
	"strings"
	"testing"
	"time"
)

// These tests drive the terminal-query protocol directly, with a fake writer and
// no renderer: the protocol is its own module, so the pending-query pairing, the
// listener fan-out and the `CSI ? 2031` deferral are observable headlessly.

// TestTerminalQueriesRoutesRepliesToListeners covers the reply path: an OSC 11
// reply resolves the oldest pending query and notifies the background listeners,
// a scheme report notifies the scheme listeners, both are consumed out of the
// input stream, and plain input is left alone.
func TestTerminalQueriesRoutesRepliesToListeners(t *testing.T) {
	q := &terminalQueries{}

	colors := make(chan RgbColor, 1)
	unsubscribe := q.OnBackgroundChange(func(color RgbColor) { colors <- color })

	if !q.ConsumeInput("\x1b]11;#0a0b0c\x07") {
		t.Fatal("OSC 11 reply was not consumed")
	}
	select {
	case color := <-colors:
		if color.R != 10 || color.G != 11 || color.B != 12 {
			t.Fatalf("color = %+v", color)
		}
	default:
		t.Fatal("background listener was not notified")
	}

	// A reply with no pending query is still consumed and delivered: a proactive
	// probe's reply must never reach the editor as typed input.
	if !q.ConsumeInput("\x1b]11;#ffffff\x07") {
		t.Fatal("a proactive probe's reply was not consumed")
	}
	select {
	case color := <-colors:
		if color.R != 255 || color.G != 255 || color.B != 255 {
			t.Fatalf("proactive probe color = %+v", color)
		}
	default:
		t.Fatal("a proactive probe's reply did not notify the listener")
	}

	unsubscribe()
	q.ConsumeInput("\x1b]11;#000000\x07")
	select {
	case color := <-colors:
		t.Fatalf("listener ran after unsubscribe: %+v", color)
	default:
	}

	schemes := make(chan TerminalColorScheme, 1)
	unsubscribeScheme := q.OnColorSchemeChange(func(scheme TerminalColorScheme) { schemes <- scheme })
	if !q.ConsumeInput("\x1b[?997;2n") {
		t.Fatal("color-scheme report was not consumed")
	}
	if scheme := <-schemes; scheme != TerminalColorSchemeLight {
		t.Fatalf("scheme = %q", scheme)
	}
	unsubscribeScheme()

	if q.ConsumeInput("a") {
		t.Fatal("plain input was consumed as a query reply")
	}
}

// TestTerminalQueriesBackgroundRoundTrip covers the blocking query: the reply is
// matched to the in-flight query, and a query with no reply times out.
func TestTerminalQueriesBackgroundRoundTrip(t *testing.T) {
	terminal := &fakeTerminal{width: 80, height: 24}
	q := &terminalQueries{}

	results := make(chan RgbColor, 1)
	go func() {
		color, ok := q.QueryBackground(terminal, 2000)
		if ok {
			results <- color
		}
	}()
	waitForWrite(t, terminal, "\x1b]11;?")
	if !q.ConsumeInput("\x1b]11;#010203\x07") {
		t.Fatal("reply to the in-flight query was not consumed")
	}
	select {
	case color := <-results:
		if color.R != 1 || color.G != 2 || color.B != 3 {
			t.Fatalf("color = %+v", color)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("in-flight query was not resolved by its reply")
	}

	if _, ok := q.QueryBackground(terminal, 1); ok {
		t.Fatal("a query with no reply should time out")
	}
}

// TestTerminalQueriesResolveTheOldestPendingQueryFirst pins the pairing order:
// replies resolve pending queries in submission order.
func TestTerminalQueriesResolveTheOldestPendingQueryFirst(t *testing.T) {
	terminal := &fakeTerminal{width: 80, height: 24}
	q := &terminalQueries{}

	first := make(chan RgbColor, 1)
	second := make(chan RgbColor, 1)
	go func() {
		if color, ok := q.QueryBackground(terminal, 2000); ok {
			first <- color
		}
	}()
	waitForWrite(t, terminal, "\x1b]11;?")
	go func() {
		if color, ok := q.QueryBackground(terminal, 2000); ok {
			second <- color
		}
	}()
	for i := 0; i < 20000 && terminal.writeCount() < 2; i++ {
		time.Sleep(50 * time.Microsecond)
	}

	q.ConsumeInput("\x1b]11;#0a0000\x07")
	q.ConsumeInput("\x1b]11;#0b0000\x07")
	if color := <-first; color.R != 10 {
		t.Fatalf("first resolved query got %+v, want R=10", color)
	}
	if color := <-second; color.R != 11 {
		t.Fatalf("second resolved query got %+v, want R=11", color)
	}
}

// TestTerminalQueriesColorSchemeRoundTrip covers the DSR round-trip.
func TestTerminalQueriesColorSchemeRoundTrip(t *testing.T) {
	terminal := &fakeTerminal{width: 80, height: 24}
	q := &terminalQueries{}

	results := make(chan TerminalColorScheme, 1)
	go func() {
		if scheme, ok := q.QueryColorScheme(terminal, 2000); ok {
			results <- scheme
		}
	}()
	waitForWrite(t, terminal, "\x1b[?996n")
	q.ConsumeInput("\x1b[?997;1n")
	select {
	case scheme := <-results:
		if scheme != TerminalColorSchemeDark {
			t.Fatalf("scheme = %q", scheme)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("color-scheme query was not resolved by its report")
	}

	if _, ok := q.QueryColorScheme(terminal, 1); ok {
		t.Fatal("a color-scheme query with no report should time out")
	}
}

// TestTerminalQueriesNotifyDefersUntilStart covers the `CSI ? 2031` toggle: before
// the terminal is started it only flips the flag (a mode sequence written then is
// echoed into the input stream), Start replays it, a live toggle writes at once,
// and Stop disables an enabled notification.
func TestTerminalQueriesNotifyDefersUntilStart(t *testing.T) {
	terminal := &fakeTerminal{width: 80, height: 24}
	q := &terminalQueries{}

	q.SetNotify(terminal, true)
	if terminal.writeCount() != 0 {
		t.Fatalf("wrote before start: %q", terminal.joinedWrites())
	}

	q.NotifyOnStart(terminal)
	if !strings.Contains(terminal.joinedWrites(), "\x1b[?2031h") {
		t.Fatalf("start did not replay the notification: %q", terminal.joinedWrites())
	}

	q.SetNotify(terminal, false)
	if !strings.Contains(terminal.joinedWrites(), "\x1b[?2031l") {
		t.Fatalf("live disable wrote nothing: %q", terminal.joinedWrites())
	}

	q.SetNotify(terminal, true)
	q.NotifyOnStop(terminal)
	joined := terminal.joinedWrites()
	if got := strings.Count(joined, "\x1b[?2031h"); got != 2 {
		t.Fatalf("2031h written %d times, want 2: %q", got, joined)
	}
	if got := strings.Count(joined, "\x1b[?2031l"); got != 2 {
		t.Fatalf("2031l written %d times, want 2: %q", got, joined)
	}

	// After Stop the toggle only flips the flag again.
	writes := terminal.writeCount()
	q.SetNotify(terminal, true)
	if terminal.writeCount() != writes {
		t.Fatalf("wrote after stop: %q", terminal.joinedWrites())
	}
}

// TestTerminalQueriesRequestBackgroundIsFireAndForget covers the proactive probe:
// it writes the query and returns without registering a pending query.
func TestTerminalQueriesRequestBackgroundIsFireAndForget(t *testing.T) {
	terminal := &fakeTerminal{width: 80, height: 24}
	q := &terminalQueries{}

	q.RequestBackground(terminal)
	if !strings.Contains(terminal.joinedWrites(), "\x1b]11;?\x07") {
		t.Fatalf("probe wrote %q", terminal.joinedWrites())
	}
	if len(q.pendingOSC11Queries) != 0 {
		t.Fatalf("probe registered %d pending queries", len(q.pendingOSC11Queries))
	}

	// A stopped terminal is not probed.
	q.NotifyOnStop(terminal)
	writes := terminal.writeCount()
	q.RequestBackground(terminal)
	if terminal.writeCount() != writes {
		t.Fatalf("probed a stopped terminal: %q", terminal.joinedWrites())
	}
}
