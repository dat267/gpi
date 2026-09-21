package interactive

import (
	"testing"
	"time"

	"context"

	"github.com/dat267/pier/coding"
)

// scriptedTerminal records the renderer's input pump so tests can deliver
// keystrokes after the picker settles its loads.
type scriptedTerminal struct {
	fakeRendererTerminal
	onInput func(string)
}

func (s *scriptedTerminal) Start(onInput func(string), onResize func()) {
	s.onInput = onInput
}

func TestSelectSessionSelects(t *testing.T) {
	SetCustomThemesDir(t.TempDir())
	SetRegisteredThemes(nil)
	SetTrueColorSupport(true)
	SetStyleColorsEnabled(true)
	InitTheme("dark", false)

	sessions := []coding.SessionInfo{
		{Path: "/s/a.jsonl", FirstMessage: "a", Modified: time.Now()},
		{Path: "/s/b.jsonl", FirstMessage: "b", Modified: time.Now().Add(-time.Hour)},
	}
	loader := func(SessionListProgress, context.Context) ([]coding.SessionInfo, error) {
		return sessions, nil
	}

	terminal := &scriptedTerminal{fakeRendererTerminal: fakeRendererTerminal{width: 80, height: 24}}
	type result struct {
		path string
	}
	done := make(chan result, 1)
	go func() {
		path := SelectSession(SelectSessionOptions{
			CurrentLoader: loader,
			AllLoader:     loader,
			Terminal:      terminal,
		})
		done <- result{path: path}
	}()

	time.Sleep(200 * time.Millisecond)
	if terminal.onInput == nil {
		t.Fatal("picker never started the terminal input pump")
	}
	terminal.onInput("\r")

	select {
	case got := <-done:
		if got.path != "/s/a.jsonl" {
			t.Fatalf("selected %q, want /s/a.jsonl", got.path)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("SelectSession did not return after selection")
	}
}

func TestSelectSessionCancel(t *testing.T) {
	SetCustomThemesDir(t.TempDir())
	SetRegisteredThemes(nil)
	SetTrueColorSupport(true)
	SetStyleColorsEnabled(true)
	InitTheme("dark", false)

	loader := func(SessionListProgress, context.Context) ([]coding.SessionInfo, error) {
		return nil, nil
	}

	terminal := &scriptedTerminal{fakeRendererTerminal: fakeRendererTerminal{width: 80, height: 24}}
	done := make(chan string, 1)
	go func() {
		done <- SelectSession(SelectSessionOptions{
			CurrentLoader: loader,
			AllLoader:     loader,
			Terminal:      terminal,
		})
	}()

	time.Sleep(200 * time.Millisecond)
	terminal.onInput("\x1b")

	select {
	case got := <-done:
		if got != "" {
			t.Fatalf("cancelled picker returned %q, want empty", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("SelectSession did not return after cancel")
	}
}
