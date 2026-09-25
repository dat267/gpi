package tui

import (
	"strings"
	"testing"
)

// The low-bandwidth render path exists for SSH/serial links, where every byte
// written crosses the network. It trims the trailing padding the styled lines
// carry and skips clears the clear-screen already performed, without changing
// the visible result. Default off: the upstream goldens assert the padded bytes.

func TestAltScreenLowBandwidthTrimsTrailingSpaces(t *testing.T) {
	SetLowBandwidth(true)
	defer SetLowBandwidth(false)

	terminal := &recordingTerminal{width: 80, height: 6}
	screen := NewAltScreen(terminal, false, t.TempDir(), AltScreenOptions{})
	screen.DisableAutoRender()
	component := &scriptedComponent{lines: []string{"hello", "world"}}
	screen.AddChild(component)

	screen.Start()
	screen.RenderNow(false) // full paint
	terminal.resetWrites()
	component.lines[1] = "changed"
	screen.RenderNow(false)
	out := terminal.takeWrites()

	if strings.Contains(out, strings.Repeat(" ", 40)) {
		t.Fatalf("low-bandwidth frame still pads a line to the width: %q", out)
	}
	if !strings.Contains(out, "changed") {
		t.Fatalf("low-bandwidth frame missing the new content: %q", out)
	}
}

func TestAltScreenLowBandwidthIsSmallerThanDefault(t *testing.T) {
	measure := func(low bool) int {
		SetLowBandwidth(low)
		terminal := &recordingTerminal{width: 100, height: 10}
		screen := NewAltScreen(terminal, false, t.TempDir(), AltScreenOptions{})
		screen.DisableAutoRender()
		component := &scriptedComponent{lines: []string{"a", "b", "c", "d"}}
		screen.AddChild(component)
		screen.Start()
		screen.RenderNow(false)
		terminal.resetWrites()
		component.lines[3] = "a changed line"
		screen.RenderNow(false)
		return len(terminal.takeWrites())
	}
	defaultBytes := measure(false)
	lowBytes := measure(true)
	SetLowBandwidth(false)
	if lowBytes >= defaultBytes {
		t.Fatalf("low-bandwidth frame is not smaller: low=%d default=%d", lowBytes, defaultBytes)
	}
}

func TestMainScreenLowBandwidthTrimsTrailingSpaces(t *testing.T) {
	SetLowBandwidth(true)
	defer SetLowBandwidth(false)

	terminal := &recordingTerminal{width: 80, height: 6}
	screen := NewMainScreen(terminal, false, t.TempDir())
	screen.DisableAutoRender()
	component := &scriptedComponent{lines: []string{"hello", "world"}}
	screen.AddChild(component)

	screen.Start()
	screen.RenderNow(false)
	terminal.resetWrites()
	component.lines[1] = "changed"
	screen.RenderNow(false)
	out := terminal.takeWrites()

	if strings.Contains(out, strings.Repeat(" ", 40)) {
		t.Fatalf("low-bandwidth main-screen frame still pads a line: %q", out)
	}
	if !strings.Contains(out, "changed") {
		t.Fatalf("low-bandwidth main-screen frame missing new content: %q", out)
	}
}
