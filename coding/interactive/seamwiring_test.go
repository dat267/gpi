package interactive

import (
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/dat267/pier/ai"
	"github.com/dat267/pier/coding"
)

// ctrl+g hands the prompt to $EDITOR and takes the result back: the editor owns
// the terminal while it runs, so the TUI is stopped and restarted around it
// (upstream handleOpenExternalEditor). The runner is stubbed — the real one would
// launch the developer's editor on a temp file (see stubExternalEditorRunner).
func TestExternalEditorWiring(t *testing.T) {
	app, cleanup := newTestApp(t)
	defer cleanup()

	// The handler resolves the command from the environment, so the stub can
	// assert which one reached the editor; VISUAL wins over EDITOR.
	t.Setenv("VISUAL", "visual-editor")
	t.Setenv("EDITOR", "env-editor")
	app.defaultEditor.SetText("the original prompt")

	commandLine := ""
	handedOver := ""
	stubExternalEditorRunner(t, func(command *exec.Cmd) error {
		commandLine = strings.Join(command.Args, " ")
		path := command.Args[len(command.Args)-1]
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		handedOver = string(data)
		return os.WriteFile(path, []byte("edited in the editor"), 0o644)
	})

	newKeyWiring(app).OnExternalEditor()

	if got := app.defaultEditor.GetText(); got != "edited in the editor" {
		t.Errorf("editor text = %q, want the edited content", got)
	}
	if handedOver != "the original prompt" {
		t.Errorf("editor received %q, want the prompt text", handedOver)
	}
	if !strings.Contains(commandLine, "visual-editor") {
		t.Errorf("editor command = %q, want the $VISUAL command", commandLine)
	}
}

// The login refresh is given a ceiling so a stalled provider cannot hang the
// flow; the returned function cancels it when the refresh finishes first.
func TestAuthScheduleTimerWiring(t *testing.T) {
	app, cleanup := newTestApp(t)
	defer cleanup()

	wiring := newAuthWiring(app)
	if wiring.ScheduleTimer == nil {
		t.Fatal("the auth wiring arms no timer, so the refresh has no ceiling")
	}

	fired := make(chan struct{})
	cancel := wiring.ScheduleTimer(10, func() { close(fired) })
	select {
	case <-fired:
	case <-time.After(5 * time.Second):
		t.Fatal("the timer never fired")
	}
	cancel()

	cancelled := make(chan struct{})
	stop := wiring.ScheduleTimer(200, func() { close(cancelled) })
	stop()
	select {
	case <-cancelled:
		t.Error("a cancelled timer still fired")
	case <-time.After(400 * time.Millisecond):
	}
}

// A tree label edit is recorded on the session, which is what makes it survive a
// reload and show up in the tree. This covers the handler; that the selector is
// given it (TreeSelectorOptions.OnLabelChange) is the one-line hookup beside it.
func TestTreeLabelChangeWiring(t *testing.T) {
	app, cleanup := newTestApp(t)
	defer cleanup()

	app.sessionMgr.AppendMessage(&ai.UserMessage{Content: ai.StringOrBlocks{Text: "a message to label"}})
	entries := app.sessionMgr.GetEntries()
	if len(entries) == 0 {
		t.Fatal("no entries")
	}
	entryID := entries[len(entries)-1].ID

	label := "the label"
	newSelectorWiring(app).handleTreeLabelChange(entryID, &label)

	if got := app.sessionMgr.GetLabel(entryID); got != label {
		t.Errorf("label = %q, want %q", got, label)
	}

	// Removing it is recorded too.
	newSelectorWiring(app).handleTreeLabelChange(entryID, nil)
	if got := app.sessionMgr.GetLabel(entryID); got != "" {
		t.Errorf("label after removal = %q, want it cleared", got)
	}
}

// The scrollbar row's side effect is wired. This pins the wiring rather than the
// effect: the closure calls the same routine applyReloadedSettings uses, and the
// scroll view exposes no getter to assert the scrollbar from here.
func TestFullscreenScrollbarWiring(t *testing.T) {
	app, cleanup := newTestApp(t)
	defer cleanup()

	wiring := newSettingsWiring(app)
	if wiring.ApplyFullscreenScrollbarSetting == nil {
		t.Fatal("the settings wiring cannot apply the scrollbar setting")
	}
	wiring.ApplyFullscreenScrollbarSetting()
}

// selectorIn returns the dialog currently shown.
func selectorIn(t *testing.T, app *App) *ExtensionSelectorComponent {
	t.Helper()
	component := app.slot.ActiveSelectorComponent()
	selector, ok := component.(*ExtensionSelectorComponent)
	if !ok {
		t.Fatalf("dialog = %T, want the extension selector", component)
	}
	return selector
}

// The native dialogs the confirm seams use (upstream reaches the same steps
// through its extension UI).
func TestAppConfirmDialogs(t *testing.T) {
	app, cleanup := newTestApp(t)
	defer cleanup()

	t.Run("confirm yes", func(t *testing.T) {
		var answer bool
		answered := false
		app.askConfirm("Title", "Message", func(confirmed bool) { answer, answered = confirmed, true })
		selectorIn(t, app).HandleInput("\r")
		if !answered || !answer {
			t.Errorf("answered=%v answer=%v", answered, answer)
		}
		if app.slot.HasActiveSelector() {
			t.Error("the dialog stayed up after an answer")
		}
	})

	t.Run("confirm no", func(t *testing.T) {
		var answer bool
		app.askConfirm("Title", "Message", func(confirmed bool) { answer = confirmed })
		selector := selectorIn(t, app)
		selector.HandleInput("\x1b[B") // move to No
		selector.HandleInput("\r")
		if answer {
			t.Error("No was reported as yes")
		}
	})

	t.Run("dismissal means no", func(t *testing.T) {
		answered := false
		answer := true
		app.askConfirm("Title", "Message", func(confirmed bool) { answer, answered = confirmed, true })
		selectorIn(t, app).HandleInput("\x1b")
		if !answered || answer {
			t.Errorf("answered=%v answer=%v, want a reported no", answered, answer)
		}
	})

	t.Run("missing cwd continue", func(t *testing.T) {
		issue := coding.SessionCwdIssue{SessionCwd: "/gone", FallbackCwd: "/fallback"}
		var cwd string
		var ok bool
		app.askMissingSessionCwd(issue, func(selected string, selectedOK bool) { cwd, ok = selected, selectedOK })
		selectorIn(t, app).HandleInput("\r")
		if !ok || cwd != "/fallback" {
			t.Errorf("cwd=%q ok=%v, want the fallback", cwd, ok)
		}
	})

	t.Run("missing cwd cancel", func(t *testing.T) {
		issue := coding.SessionCwdIssue{SessionCwd: "/gone", FallbackCwd: "/fallback"}
		var ok bool
		app.askMissingSessionCwd(issue, func(_ string, selectedOK bool) { ok = selectedOK })
		selector := selectorIn(t, app)
		selector.HandleInput("\x1b[B") // move to Cancel
		selector.HandleInput("\r")
		if ok {
			t.Error("Cancel reported a cwd")
		}
	})
}

// Turning clear-on-shrink off drops the status container's idle line, unless an
// indicator is active (upstream clears it only when nothing is showing).
func TestClearStatusContainerIfIdleWiring(t *testing.T) {
	app, cleanup := newTestApp(t)
	defer cleanup()

	idle := &IdleStatus{}
	app.uiState.StatusContainer.AddChild(idle)
	callbacks := newSettingsWiring(app).BuildSettingsCallbacks(func() {}, func() {})
	callbacks.OnClearOnShrinkChange(false)
	if got := len(app.uiState.StatusContainer.Children); got != 0 {
		t.Errorf("status container still holds %d children", got)
	}

	// With an indicator up, the container is left alone.
	app.uiState.StatusContainer.AddChild(idle)
	indicator := NewWorkingStatusIndicator(app.uiState.UI, "Working...", nil, nil)
	app.uiState.ActiveStatusIndicator = indicator
	app.uiState.ClearStatusContainerIfIdle()
	if got := len(app.uiState.StatusContainer.Children); got != 1 {
		t.Errorf("status container lost its children while an indicator was active (%d)", got)
	}
	app.uiState.ActiveStatusIndicator = nil
	indicator.Dispose()
}
