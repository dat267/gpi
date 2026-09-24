package interactive

import (
	"errors"
	"strings"
	"testing"

	"github.com/dat267/pier/tui"
)

func newReloadTestWiring(t *testing.T) (*CommandWiring, *commandTestSession) {
	t.Helper()
	wiring, session, _ := newCommandTestWiring(t)
	wiring.Editor = tui.NewEditor(nil, tui.EditorTheme{}, tui.EditorOptions{})
	wiring.EditorContainer = &tui.Container{}
	wiring.EditorContainer.AddChild(wiring.Editor)
	return wiring, session
}

// TestHandleReloadCommand covers the upstream reload flow: guards while
// streaming/compacting, the reload-box editor swap, the detached reload,
// settings reapplication, and the editor restore on success and failure.
func TestHandleReloadCommand(t *testing.T) {
	statuses := []string{}
	warnings := []string{}
	errorsShown := []string{}
	applied := 0

	wiring, session := newReloadTestWiring(t)
	wiring.ShowStatus = func(message string) { statuses = append(statuses, message) }
	wiring.ShowWarning = func(message string) { warnings = append(warnings, message) }
	wiring.ShowError = func(message string) { errorsShown = append(errorsShown, message) }
	wiring.ApplyReloadedSettings = func() { applied++ }

	// Streaming: warn, nothing else happens.
	session.streaming = true
	wiring.HandleReloadCommand()
	if len(warnings) != 1 || !strings.Contains(warnings[0], "Wait for the current response to finish") {
		t.Fatalf("warnings = %v", warnings)
	}
	if wiring.EditorContainer.Children[0] != tui.Component(wiring.Editor) {
		t.Fatal("editor was swapped while streaming")
	}
	session.streaming = false

	// Compacting: warn.
	session.compacting = true
	wiring.HandleReloadCommand()
	if len(warnings) != 2 || !strings.Contains(warnings[1], "compaction") {
		t.Fatalf("warnings = %v", warnings)
	}
	session.compacting = false

	// Idle: the reload box swaps in, the work runs detached, the settings are
	// reapplied, and the editor is restored after the status message.
	reloaded := false
	modelsJSONError := ""
	savedTrust := false
	var reloadErr error
	wiring.ReloadNow = func() (string, bool, error) {
		reloaded = true
		return modelsJSONError, savedTrust, reloadErr
	}
	detached := 0
	wiring.RunDetached = func(fn func()) { detached++; fn() }
	wiring.HandleReloadCommand()

	if !reloaded || detached != 1 {
		t.Fatalf("reloaded = %v, detached = %d", reloaded, detached)
	}
	if applied != 1 {
		t.Fatalf("ApplyReloadedSettings calls = %d", applied)
	}
	if len(statuses) != 1 || !strings.Contains(statuses[0], "Reloaded "+reloadedItems) {
		t.Fatalf("statuses = %v", statuses)
	}
	if len(errorsShown) != 0 {
		t.Fatalf("errors = %v", errorsShown)
	}
	if wiring.EditorContainer.Children[0] != tui.Component(wiring.Editor) || wiring.EditorContainer.Children[0] == nil {
		t.Fatal("editor not restored")
	}

	// The trust-save suffix (upstream's savedImplicitProjectTrust wording).
	savedTrust = true
	wiring.HandleReloadCommand()
	if !strings.Contains(statuses[len(statuses)-1], "; saved project trust") {
		t.Fatalf("statuses = %v", statuses)
	}

	// A models.json error is reported separately from the failure path.
	savedTrust = false
	modelsJSONError = "bad models.json"
	wiring.HandleReloadCommand()
	if !strings.Contains(errorsShown[len(errorsShown)-1], "models.json error: bad models.json") {
		t.Fatalf("errors = %v", errorsShown)
	}
	if len(statuses) != 3 {
		t.Fatalf("reload continued after models.json error: %v", statuses)
	}

	// A reload failure restores the editor and reports the error; the
	// settings are not reapplied.
	modelsJSONError = ""
	reloadErr = errors.New("boom")
	wiring.HandleReloadCommand()
	if !strings.Contains(errorsShown[len(errorsShown)-1], "Reload failed: boom") {
		t.Fatalf("errors = %v", errorsShown)
	}
	if applied != 3 {
		t.Fatalf("ApplyReloadedSettings ran on failure: %d", applied)
	}
	if wiring.EditorContainer.Children[0] != tui.Component(wiring.Editor) {
		t.Fatal("editor not restored after failure")
	}
}

// TestReloadNoticeListsWhatIsReloaded pins the /reload notice to the work the
// port actually does. The notice came from upstream, extensions first — but
// extension mechanics are out of scope (D41), so the mode swapped the editor
// for a box announcing a reload that never happened. Everything else in the
// list is real: the session's reload re-reads settings, queue modes, context
// files, skills and the system/append prompt files, and the mode then re-reads
// keybindings, trust and the theme.
func TestReloadNoticeListsWhatIsReloaded(t *testing.T) {
	if strings.Contains(strings.ToLower(reloadedItems), "extension") {
		t.Fatalf("the /reload notice claims extensions: %q", reloadedItems)
	}
	for _, item := range []string{"keybindings", "skills", "prompts", "themes", "context files"} {
		if !strings.Contains(reloadedItems, item) {
			t.Errorf("the /reload notice no longer mentions %q: %q", item, reloadedItems)
		}
	}
}
