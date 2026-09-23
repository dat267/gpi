package interactive

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dat267/pier/ai"
	"github.com/dat267/pier/tui"
)

// findLoginDialog returns the login dialog mounted in the editor container, or
// nil when none is mounted.
func findLoginDialog(wiring *AuthWiring) *LoginDialogComponent {
	if wiring.EditorContainer == nil {
		return nil
	}
	for _, child := range wiring.EditorContainer.Children {
		if dialog, ok := child.(*LoginDialogComponent); ok {
			return dialog
		}
	}
	return nil
}

// TestApiKeyLoginRunsOffTheUILoop pins down the login deadlock: the flow waits
// on the dialog for its answer, so a flow running on the UI loop can never be
// answered — the loop is the thing that would have to deliver the keystroke.
//
// In production one goroutine (the loop) dispatches /login and then goes on
// rendering and routing input, so the dispatch has to return while the flow
// waits. This test models that: the dispatching call gets a watchdog, and the
// test goroutine then plays the loop — draining posted mutations, answering the
// prompt the way a key handler would, and draining again — until the flow's
// continuation has run.
func TestApiKeyLoginRunsOffTheUILoop(t *testing.T) {
	wiring, session := newAuthTestWiring(t)
	statuses := &messageRecorder{}
	wiring.ShowStatus = statuses.add
	wiring.ShowError = (&messageRecorder{}).add
	wiring.ShowWarning = (&messageRecorder{}).add
	wiring.ScheduleTimer = func(int, func()) func() { return func() {} }
	wiring.AuthPath = filepath.Join(t.TempDir(), "auth.json")
	screen := wiring.UI.(*tui.MainScreen)

	// A known previous model keeps the post-login path synchronous: the
	// selection logic and its catalog refresh are skipped, so the status is the
	// last thing the flow does.
	session.model = &ai.Model{Provider: "openai", ID: "gpt-4", API: ai.APIAnthropicMessages}

	dispatched := make(chan struct{})
	go func() {
		defer close(dispatched)
		wiring.ShowApiKeyLoginDialog(context.Background(), "openai", "OpenAI")
	}()
	select {
	case <-dispatched:
	case <-time.After(5 * time.Second):
		t.Fatal("ShowApiKeyLoginDialog never returned: the login flow is running on the UI loop")
	}

	// From here on this goroutine is the loop. The dialog is mounted before the
	// flow starts, so wait for the posted prompt itself rather than the dialog.
	var dialog *LoginDialogComponent
	waitForCondition(t, func() bool {
		screen.RenderNow(true)
		found := findLoginDialog(wiring)
		if found == nil || !strings.Contains(strings.Join(found.ContentLines(80), "\n"), "Enter OpenAI API key") {
			return false
		}
		dialog = found
		return true
	})

	dialog.input.SetValue("sk-test-key")
	dialog.HandleInput("\r")

	waitForCondition(t, func() bool {
		screen.RenderNow(true)
		return statuses.len() > 0
	})
	if !strings.Contains(statuses.last(), "Saved API key for OpenAI") {
		t.Fatalf("statuses = %v", statuses.messages)
	}
	if findLoginDialog(wiring) != nil {
		t.Fatal("dialog still mounted: the continuation did not restore the editor")
	}
}
