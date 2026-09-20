package tui

import (
	"strings"
	"testing"
	"time"
)

// TestTerminalColorsParsing covers the OSC 11 / color-scheme parsers.
func TestTerminalColorsParsing(t *testing.T) {
	// OSC 11 responses.
	if !IsOsc11BackgroundColorResponse("\x1b]11;#ffffff\x07") {
		t.Fatal("BEL-terminated response not detected")
	}
	if !IsOsc11BackgroundColorResponse("\x1b]11;rgb:00/00/00\x1b\\") {
		t.Fatal("ST-terminated response not detected")
	}
	if IsOsc11BackgroundColorResponse("#ffffff") {
		t.Fatal("plain hex detected as a response")
	}

	color, ok := ParseOsc11BackgroundColor("\x1b]11;#102030\x07")
	if !ok || color.R != 0x10 || color.G != 0x20 || color.B != 0x30 {
		t.Fatalf("hex color = %+v ok=%v", color, ok)
	}
	color, ok = ParseOsc11BackgroundColor("\x1b]11;rgb:ff/80/00\x07")
	if !ok || color.R != 255 || color.G != 128 || color.B != 0 {
		t.Fatalf("rgb color = %+v ok=%v", color, ok)
	}
	color, ok = ParseOsc11BackgroundColor("\x1b]11;rgba:ffff/0000/0000\x07")
	if !ok || color.R != 255 || color.G != 0 || color.B != 0 {
		t.Fatalf("12-bit color = %+v ok=%v", color, ok)
	}
	if _, ok := ParseOsc11BackgroundColor("\x1b]11;not-a-color\x07"); ok {
		t.Fatal("invalid color parsed")
	}
	if _, ok := ParseOsc11BackgroundColor("nope"); ok {
		t.Fatal("non-response parsed")
	}

	// Color-scheme reports.
	if scheme, ok := ParseTerminalColorSchemeReport("\x1b[?997;1n"); !ok || scheme != TerminalColorSchemeDark {
		t.Fatalf("dark scheme = %q ok=%v", scheme, ok)
	}
	if scheme, ok := ParseTerminalColorSchemeReport("\x1b[?997;2n"); !ok || scheme != TerminalColorSchemeLight {
		t.Fatalf("light scheme = %q ok=%v", scheme, ok)
	}
	if _, ok := ParseTerminalColorSchemeReport("\x1b[?997;3n"); ok {
		t.Fatal("invalid scheme parsed")
	}
}

// TestRendererTerminalQueries covers the renderer query round-trips.
func TestRendererTerminalQueries(t *testing.T) {
	terminal := &fakeTerminal{width: 80, height: 24}
	renderer := NewRenderer(terminal)

	// OSC 11: the reply is consumed by HandleTerminalInput.
	results := make(chan struct {
		color RgbColor
		ok    bool
	}, 1)
	go func() {
		color, ok := renderer.QueryTerminalBackgroundColor(2000)
		results <- struct {
			color RgbColor
			ok    bool
		}{color, ok}
	}()
	waitForWrite(t, terminal, "\x1b]11;?")
	renderer.HandleTerminalInput("\x1b]11;#0a0b0c\x07")
	result := <-results
	if !result.ok || result.color.R != 10 || result.color.G != 11 || result.color.B != 12 {
		t.Fatalf("background = %+v ok=%v", result.color, result.ok)
	}

	// A timeout returns ok=false.
	if _, ok := renderer.QueryTerminalBackgroundColor(1); ok {
		t.Fatal("query without a reply should time out")
	}

	// Color scheme: a listener is notified by the report consumer.
	schemes := make(chan TerminalColorScheme, 1)
	unsubscribe := renderer.OnTerminalColorSchemeChange(func(scheme TerminalColorScheme) { schemes <- scheme })
	renderer.HandleTerminalInput("\x1b[?997;2n")
	if scheme := <-schemes; scheme != TerminalColorSchemeLight {
		t.Fatalf("scheme = %q", scheme)
	}
	unsubscribe()
	renderer.HandleTerminalInput("\x1b[?997;1n")
	select {
	case scheme := <-schemes:
		t.Fatalf("unexpected scheme after unsubscribe: %q", scheme)
	default:
	}

	// The color-scheme query round-trips.
	schemeResults := make(chan TerminalColorScheme, 1)
	go func() {
		scheme, ok := renderer.QueryTerminalColorScheme(2000)
		if ok {
			schemeResults <- scheme
		}
	}()
	waitForWrite(t, terminal, "\x1b[?996n")
	renderer.HandleTerminalInput("\x1b[?997;1n")
	if scheme := <-schemeResults; scheme != TerminalColorSchemeDark {
		t.Fatalf("queried scheme = %q", scheme)
	}

	// Notifications toggle writes the mode sequences and Stop disables them.
	renderer.SetTerminalColorSchemeNotifications(true)
	renderer.SetTerminalColorSchemeNotifications(false)
	renderer.SetTerminalColorSchemeNotifications(true)
	renderer.Stop(TuiStopOptions{})
	joined := terminal.joinedWrites()
	if !strings.Contains(joined, "\x1b[?2031h") || !strings.Contains(joined, "\x1b[?2031l") {
		t.Fatalf("writes = %q", joined)
	}
}

// waitForWrite waits until the terminal has seen a write containing the
// substring.
func waitForWrite(t *testing.T, terminal *fakeTerminal, substring string) {
	t.Helper()
	for i := 0; i < 20000; i++ {
		if terminal.hasWrite(substring) {
			return
		}
		timeSleepMicro()
	}
	t.Fatalf("terminal writes = %d, missing %q", terminal.writeCount(), substring)
}

func timeSleepMicro() {
	time.Sleep(50 * time.Microsecond)
}
