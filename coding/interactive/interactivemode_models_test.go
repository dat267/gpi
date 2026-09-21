package interactive

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/dat267/pier/ai"
	"github.com/dat267/pier/coding"
	"github.com/dat267/pier/tui"
)

// modelTestSession implements ModelSession.
type modelTestSession struct {
	model      *ai.Model
	runtime    ModelSelectorRuntime
	scoped     []coding.ScopedModel
	setModels  []*ai.Model
	setPersist []bool
	setErr     error
}

func (s *modelTestSession) Model() *ai.Model                   { return s.model }
func (s *modelTestSession) ModelRuntime() ModelSelectorRuntime { return s.runtime }
func (s *modelTestSession) ScopedModels() []coding.ScopedModel { return s.scoped }
func (s *modelTestSession) SetModel(_ context.Context, model *ai.Model, options coding.ModelMutationOptions) error {
	s.model = model
	s.setModels = append(s.setModels, model)
	s.setPersist = append(s.setPersist, options.Persist)
	return s.setErr
}
func (s *modelTestSession) SetScopedModels(models []coding.ScopedModel) { s.scoped = models }

// modelTestRuntime implements ModelSelectorRuntime and ModelCatalogRuntime.
type modelTestRuntime struct {
	models  []*ai.Model
	refresh func() (ai.ModelsRefreshResult, error)
}

func (r *modelTestRuntime) GetAvailableSnapshot() []*ai.Model { return r.models }
func (r *modelTestRuntime) GetModel(providerID string, modelID string) *ai.Model {
	for _, model := range r.models {
		if model.Provider == providerID && model.ID == modelID {
			return model
		}
	}
	return nil
}
func (r *modelTestRuntime) GetError() string { return "" }
func (r *modelTestRuntime) Refresh(ctx context.Context, _ *coding.ModelsRefreshCallOptions) (ai.ModelsRefreshResult, error) {
	if r.refresh != nil {
		return r.refresh()
	}
	<-ctx.Done()
	return ai.ModelsRefreshResult{Aborted: true}, ctx.Err()
}

func newModelTestWiring(t *testing.T, runtime *modelTestRuntime) (*ModelWiring, *modelTestSession, *coding.SettingsManager) {
	t.Helper()
	SetCustomThemesDir(t.TempDir())
	SetRegisteredThemes(nil)
	SetTrueColorSupport(true)
	SetStyleColorsEnabled(true)
	InitTheme("dark", false)

	screen := tui.NewMainScreen(&fakeRendererTerminal{width: 80, height: 24}, false, "")
	screen.DisableAutoRender()
	editorContainer := &tui.Container{}
	editor := NewCustomEditor(editorTestHost{}, tui.EditorTheme{}, NewAppKeybindingsManager(nil, ""), CustomEditorOptions{})
	editorContainer.AddChild(editor)
	settings := coding.NewInMemorySettingsManager(nil, coding.SettingsManagerCreateOptions{})
	session := &modelTestSession{runtime: runtime, model: &ai.Model{ID: "a", Provider: "p"}}
	wiring := &ModelWiring{
		Slot:     NewSelectorSlot(screen, editorContainer, editor),
		Settings: settings,
		Session:  session,
		UI:       screen,
	}
	return wiring, session, settings
}

// TestModelSelectorWiring covers the single-model selector.
func TestModelSelectorWiring(t *testing.T) {
	runtime := &modelTestRuntime{models: []*ai.Model{{ID: "a", Provider: "p"}, {ID: "b", Provider: "p"}}}
	wiring, session, settings := newModelTestWiring(t, runtime)
	settings.SetDefaultProvider("p")
	settings.SetDefaultModel("b")
	statuses := []string{}
	errors := []string{}
	wiring.ShowStatus = func(message string) { statuses = append(statuses, message) }
	wiring.ShowError = func(message string) { errors = append(errors, message) }
	selected := []string{}
	wiring.OnModelSelected = func(model *ai.Model) { selected = append(selected, model.ID) }
	borderUpdates := 0
	wiring.UpdateEditorBorderColor = func() { borderUpdates++ }
	providerUpdates := 0
	wiring.UpdateAvailableProviderCount = func() { providerUpdates++ }

	wiring.ShowModelSelector(context.Background(), "b")
	if !wiring.Slot.HasActiveSelector() {
		t.Fatal("model selector not shown")
	}
	selector := wiring.Slot.ActiveSelectorComponent().(*ModelSelectorComponent)
	if selector.GetDefaultModelReference() == nil || selector.GetDefaultModelReference().ID != "b" {
		t.Fatalf("default model = %+v", selector.GetDefaultModelReference())
	}

	// Selecting (non-persist) switches the model and reports the status.
	selector.SelectModel(&ai.Model{ID: "b", Provider: "p"})
	if len(session.setModels) != 1 || session.setPersist[0] {
		t.Fatalf("set models = %v persist = %v", session.setModels, session.setPersist)
	}
	if statuses[len(statuses)-1] != "Model: b" {
		t.Fatalf("statuses = %v", statuses)
	}
	if borderUpdates != 1 || providerUpdates != 1 || len(selected) != 1 {
		t.Fatalf("border = %d, providers = %d, selected = %v", borderUpdates, providerUpdates, selected)
	}
	if wiring.Slot.HasActiveSelector() {
		t.Fatal("selector not closed after select")
	}

	// Selecting as default persists and reports the default status.
	wiring.ShowModelSelector(context.Background(), "")
	selector = wiring.Slot.ActiveSelectorComponent().(*ModelSelectorComponent)
	selector.SelectModelAsDefault(&ai.Model{ID: "a", Provider: "p"})
	if len(session.setPersist) != 2 || !session.setPersist[1] {
		t.Fatalf("persist = %v", session.setPersist)
	}
	if statuses[len(statuses)-1] != "Default model: p/a" {
		t.Fatalf("statuses = %v", statuses)
	}
	_ = errors
}

// TestModelSelectorError covers the failing set-model path.
func TestModelSelectorError(t *testing.T) {
	runtime := &modelTestRuntime{models: []*ai.Model{{ID: "a", Provider: "p"}}}
	wiring, session, _ := newModelTestWiring(t, runtime)
	session.setErr = errors.New("boom")
	errorsShown := []string{}
	wiring.ShowError = func(message string) { errorsShown = append(errorsShown, message) }
	wiring.ShowModelSelector(context.Background(), "")
	selector := wiring.Slot.ActiveSelectorComponent().(*ModelSelectorComponent)
	selector.SelectModel(&ai.Model{ID: "a", Provider: "p"})
	if len(errorsShown) != 1 || errorsShown[0] != "boom" {
		t.Fatalf("errors = %v", errorsShown)
	}
	if wiring.Slot.HasActiveSelector() {
		t.Fatal("selector not closed on error")
	}
}

// TestScopedModelsWiring covers the scoped-models selector and refresh.
func TestScopedModelsWiring(t *testing.T) {
	models := []*ai.Model{{ID: "a", Provider: "p"}, {ID: "b", Provider: "p"}}
	runtime := &modelTestRuntime{models: models, refresh: func() (ai.ModelsRefreshResult, error) {
		return ai.ModelsRefreshResult{}, nil
	}}
	wiring, session, settings := newModelTestWiring(t, runtime)
	settings.SetEnabledModels([]string{"p/a"})
	statuses := []string{}
	wiring.ShowStatus = func(message string) { statuses = append(statuses, message) }
	timers := []func(){}
	wiring.ScheduleTimer = func(ms int, fn func()) func() {
		timers = append(timers, fn)
		return func() {}
	}

	wiring.ShowModelsSelector(context.Background())
	if !wiring.Slot.HasActiveSelector() {
		t.Fatal("scoped selector not shown")
	}
	selector := wiring.Slot.ActiveSelectorComponent().(*ScopedModelsSelectorComponent)
	if enabled := selector.EnabledIDs(); len(enabled.IDs) != 1 || enabled.IDs[0] != "p/a" {
		t.Fatalf("enabled = %+v", enabled)
	}
	// The refresh goroutine completes; wait for the render request.
	waitForCondition(t, func() bool { return selector.RefreshStatus() == "Model catalogs refreshed." })

	// Persisting a subset writes the settings.
	selector.PersistEnabled(EnabledIds{IDs: []string{"p/a"}})
	if patterns := settings.GetEnabledModels(); len(patterns) != 1 || patterns[0] != "p/a" {
		t.Fatalf("patterns = %v", patterns)
	}
	if statuses[len(statuses)-1] != "Model selection saved to settings" {
		t.Fatalf("statuses = %v", statuses)
	}

	// Changing the selection scopes the session.
	selector.ChangeEnabled(EnabledIds{IDs: []string{"p/a"}})
	if len(session.scoped) != 1 || session.scoped[0].Model.ID != "a" {
		t.Fatalf("scoped = %+v", session.scoped)
	}
	// All-enabled clears the scope.
	selector.ChangeEnabled(EnabledIds{IDs: []string{"p/a", "p/b"}})
	if len(session.scoped) != 0 {
		t.Fatalf("scoped = %+v", session.scoped)
	}
	// Persisting all-enabled clears the patterns.
	selector.PersistEnabled(EnabledIds{IDs: []string{"p/a", "p/b"}})
	if patterns := settings.GetEnabledModels(); len(patterns) != 0 {
		t.Fatalf("patterns = %v", patterns)
	}
	_ = timers
}

// TestSessionSelectorWiring covers the resume selector.
func TestSessionSelectorWiring(t *testing.T) {
	SetCustomThemesDir(t.TempDir())
	SetRegisteredThemes(nil)
	SetTrueColorSupport(true)
	SetStyleColorsEnabled(true)
	InitTheme("dark", false)

	screen := tui.NewMainScreen(&fakeRendererTerminal{width: 80, height: 24}, false, "")
	screen.DisableAutoRender()
	editorContainer := &tui.Container{}
	editor := NewCustomEditor(editorTestHost{}, tui.EditorTheme{}, NewAppKeybindingsManager(nil, ""), CustomEditorOptions{})
	editorContainer.AddChild(editor)
	manager := coding.NewSessionManager("/tmp/proj", &coding.SessionManagerOptions{Persist: boolPtr(false)})
	wiring := &SessionWiring{
		Slot:        NewSelectorSlot(screen, editorContainer, editor),
		SessionInfo: manager,
		UI:          screen,
	}
	statuses := []string{}
	wiring.ShowStatus = func(message string) { statuses = append(statuses, message) }
	wiring.ClearStatusIndicator = func() {}

	resumed := []string{}
	wiring.SwitchSession = func(_ context.Context, sessionPath string, cwdOverride string) (*SessionSwitchResult, error) {
		resumed = append(resumed, sessionPath)
		return &SessionSwitchResult{}, nil
	}
	wiring.ShowSessionSelector()
	if !wiring.Slot.HasActiveSelector() {
		t.Fatal("session selector not shown")
	}
	selector := wiring.Slot.ActiveSelectorComponent().(*SessionSelectorComponent)
	selector.GetSessionList().OnSelect("session-a")
	if len(resumed) != 1 || resumed[0] != "session-a" {
		t.Fatalf("resumed = %v", resumed)
	}
	if statuses[len(statuses)-1] != "Resumed session" {
		t.Fatalf("statuses = %v", statuses)
	}

	// A cancelled switch reports nothing.
	wiring.SwitchSession = func(context.Context, string, string) (*SessionSwitchResult, error) {
		return &SessionSwitchResult{Cancelled: true}, nil
	}
	wiring.HandleResumeSession(context.Background(), "session-b")

	// A missing-cwd error can be resolved by prompting.
	wiring.SwitchSession = func(_ context.Context, _ string, cwdOverride string) (*SessionSwitchResult, error) {
		if cwdOverride == "" {
			return nil, errors.New("Missing session cwd")
		}
		return &SessionSwitchResult{}, nil
	}
	wiring.PromptForMissingCwd = func(context.Context, string) (string, bool) { return "/tmp/other", true }
	wiring.HandleResumeSession(context.Background(), "session-c")
	if statuses[len(statuses)-1] != "Resumed session in current cwd" {
		t.Fatalf("statuses = %v", statuses)
	}
	// Declining the prompt cancels.
	wiring.PromptForMissingCwd = func(context.Context, string) (string, bool) { return "", false }
	wiring.HandleResumeSession(context.Background(), "session-d")
	if statuses[len(statuses)-1] != "Resume cancelled" {
		t.Fatalf("statuses = %v", statuses)
	}
}

// waitForCondition polls until the condition holds or the timeout elapses.
func waitForCondition(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("condition not met before timeout")
}
