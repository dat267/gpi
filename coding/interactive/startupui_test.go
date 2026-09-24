package interactive

import (
	"testing"
	"time"
)

// The startup prompt runs before the app's TUI: it owns the terminal, asks, and
// hands a clean screen over. Project trust is the reason it exists — the answer
// decides whether the project's .pi settings and resources are read at all.

// startupSelectorPrompt drives ShowStartupSelector on a scripted terminal and
// returns what it answered plus the label list it was offered.
func startupSelectorPrompt(t *testing.T, keys string) (string, bool, []string) {
	t.Helper()
	SetCustomThemesDir(t.TempDir())
	SetRegisteredThemes(nil)
	SetTrueColorSupport(true)
	SetStyleColorsEnabled(true)
	InitTheme("dark", false)

	terminal := &scriptedTerminal{fakeRendererTerminal: fakeRendererTerminal{width: 80, height: 24}, started: make(chan struct{})}
	type answer struct {
		label string
		ok    bool
	}
	done := make(chan answer, 1)
	go func() {
		label, ok := ShowStartupSelector(
			StartupSelectorOptions{Terminal: terminal},
			"Trust project folder?\n/tmp/project",
			[]string{"Trust", "Trust (this session only)", "Do not trust"},
		)
		done <- answer{label: label, ok: ok}
	}()

	select {
	case <-terminal.started:
	case <-time.After(5 * time.Second):
		t.Fatal("the startup screen never started the terminal input pump")
	}
	time.Sleep(200 * time.Millisecond) // let it render
	terminal.onInput(keys)

	select {
	case got := <-done:
		return got.label, got.ok, []string{"Trust", "Trust (this session only)", "Do not trust"}
	case <-time.After(5 * time.Second):
		t.Fatal("ShowStartupSelector did not return")
		return "", false, nil
	}
}

func TestShowStartupSelectorReturnsTheChoice(t *testing.T) {
	label, ok, _ := startupSelectorPrompt(t, "\r")
	if !ok || label != "Trust" {
		t.Fatalf("answer = %q, %v, want the default option", label, ok)
	}
}

func TestShowStartupSelectorReportsCancel(t *testing.T) {
	label, ok, _ := startupSelectorPrompt(t, "\x1b")
	if ok || label != "" {
		t.Fatalf("answer = %q, %v, want a cancelled prompt", label, ok)
	}
}
