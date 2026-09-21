package interactive

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/dat267/pier/ai"
	"github.com/dat267/pier/coding"
	"github.com/dat267/pier/tui"
)

// authTestSession implements AuthSession.
type authTestSession struct {
	runtime   *coding.ModelRuntime
	model     *ai.Model
	setModels []*ai.Model
	setErr    error
}

func (s *authTestSession) ModelRuntime() *coding.ModelRuntime { return s.runtime }
func (s *authTestSession) Model() *ai.Model                   { return s.model }
func (s *authTestSession) SetModel(_ context.Context, model *ai.Model, _ coding.ModelMutationOptions) error {
	if s.setErr != nil {
		return s.setErr
	}
	s.model = model
	s.setModels = append(s.setModels, model)
	return nil
}

func newAuthTestRuntime(t *testing.T) *coding.ModelRuntime {
	t.Helper()
	runtime, err := coding.CreateModelRuntime(coding.CreateModelRuntimeOptions{
		Credentials:       ai.NewInMemoryCredentialStore(),
		DisableModelsJSON: true,
	})
	if err != nil {
		t.Fatalf("create runtime: %v", err)
	}
	return runtime
}

func newAuthTestWiring(t *testing.T) (*AuthWiring, *authTestSession) {
	t.Helper()
	SetCustomThemesDir(t.TempDir())
	SetRegisteredThemes(nil)
	SetTrueColorSupport(true)
	SetStyleColorsEnabled(true)
	InitTheme("dark", false)

	runtime := newAuthTestRuntime(t)
	session := &authTestSession{runtime: runtime, model: &ai.Model{Provider: "unknown", ID: "unknown", API: "unknown"}}
	screen := tui.NewMainScreen(&fakeRendererTerminal{width: 80, height: 24}, false, "")
	screen.DisableAutoRender()
	editorContainer := &tui.Container{}
	editor := NewCustomEditor(editorTestHost{}, tui.EditorTheme{}, NewAppKeybindingsManager(nil, ""), CustomEditorOptions{})
	editorContainer.AddChild(editor)
	wiring := &AuthWiring{
		Slot:            NewSelectorSlot(screen, editorContainer, editor),
		EditorContainer: editorContainer,
		Editor:          editor,
		UI:              screen,
		Session:         session,
		AuthPath:        "/tmp/auth.json",
	}
	return wiring, session
}

// TestAuthLoginProviderOptions covers the provider listing.
func TestAuthLoginProviderOptions(t *testing.T) {
	wiring, _ := newAuthTestWiring(t)

	all := wiring.GetLoginProviderOptions("")
	if len(all) == 0 {
		t.Fatal("no providers")
	}
	// Sorted by name.
	for i := 1; i < len(all); i++ {
		if all[i-1].Name > all[i].Name {
			t.Fatalf("not sorted: %q > %q", all[i-1].Name, all[i].Name)
		}
	}
	// Filtering by auth type.
	for _, provider := range wiring.GetLoginProviderOptions("oauth") {
		if provider.AuthType != "oauth" {
			t.Fatalf("oauth filter returned %q", provider.AuthType)
		}
	}
	for _, provider := range wiring.GetLoginProviderOptions("api_key") {
		if provider.AuthType != "api_key" {
			t.Fatalf("api_key filter returned %q", provider.AuthType)
		}
	}
	// Every entry has a method and a non-empty id.
	for _, provider := range all {
		if provider.ID == "" || provider.Method == nil {
			t.Fatalf("bad provider %+v", provider)
		}
	}

	// The reference lookup matches ids and names case-insensitively.
	first := all[0]
	if matches := wiring.FindLoginProviderOptions(strings.ToUpper(first.ID)); len(matches) == 0 {
		t.Fatalf("no match for %q", first.ID)
	}
	if matches := wiring.FindLoginProviderOptions("  "); len(matches) != 0 {
		t.Fatalf("blank ref matched: %+v", matches)
	}
	if matches := wiring.FindLoginProviderOptions("nope-nope"); len(matches) != 0 {
		t.Fatalf("unknown ref matched: %+v", matches)
	}
}

// TestAuthLogoutProviderOptions covers the credential listing.
func TestAuthLogoutProviderOptions(t *testing.T) {
	wiring, _ := newAuthTestWiring(t)
	store := ai.NewInMemoryCredentialStore()
	runtime, err := coding.CreateModelRuntime(coding.CreateModelRuntimeOptions{
		Credentials: store, DisableModelsJSON: true,
	})
	if err != nil {
		t.Fatalf("create runtime: %v", err)
	}
	wiring.Session.(*authTestSession).runtime = runtime

	// No credentials: empty list.
	options, err := wiring.GetLogoutProviderOptions(context.Background())
	if err != nil || len(options) != 0 {
		t.Fatalf("options = %+v, err = %v", options, err)
	}
}

// TestAuthLoginCommandRouting covers the command routing.
func TestAuthLoginCommandRouting(t *testing.T) {
	wiring, _ := newAuthTestWiring(t)
	statuses := []string{}
	wiring.ShowStatus = func(message string) { statuses = append(statuses, message) }
	authSelects := 0
	wiring.ShowAuthSelect = func(*LoginDialogComponent, ai.AuthPrompt) (string, error) {
		authSelects++
		return "option", nil
	}

	// An unknown provider reference opens the provider selector (no matches ->
	// empty status is only shown when there are no providers at all, so the
	// selector opens).
	wiring.HandleLoginCommand(context.Background(), "definitely-not-a-provider")
	// A blank reference opens the auth-type selector.
	wiring.HandleLoginCommand(context.Background(), "")
	_ = authSelects
}

// TestAuthShowAuthPrompt covers the prompt mapping.
func TestAuthShowAuthPrompt(t *testing.T) {
	wiring, _ := newAuthTestWiring(t)

	// Select prompts delegate to the extension selector seam.
	wiring.ShowAuthSelect = func(_ *LoginDialogComponent, prompt ai.AuthPrompt) (string, error) {
		if prompt.Type != ai.AuthPromptSelect {
			t.Fatalf("prompt type = %q", prompt.Type)
		}
		return "chosen", nil
	}
	dialog := NewLoginDialogComponent(nil, "p", nil, "", "")
	value, err := wiring.ShowAuthPrompt(dialog, ai.AuthPrompt{Type: ai.AuthPromptSelect})
	if err != nil || value != "chosen" {
		t.Fatalf("value = %q, err = %v", value, err)
	}

	// Without the seam the select prompt cancels.
	wiring.ShowAuthSelect = nil
	if _, err := wiring.ShowAuthPrompt(dialog, ai.AuthPrompt{Type: ai.AuthPromptSelect}); err == nil {
		t.Fatal("expected cancellation")
	}

	// A manual-code prompt waits on the dialog; the seam signals when the
	// prompt is ready so the test can submit a value.
	wiring.ShowAuthSelect = nil
	promptReady := make(chan struct{}, 1)
	wiring.OnPromptShown = func() { promptReady <- struct{}{} }
	done := make(chan struct{})
	var gotValue string
	var gotErr error
	go func() {
		defer close(done)
		gotValue, gotErr = wiring.ShowAuthPrompt(dialog, ai.AuthPrompt{Type: ai.AuthPromptManualCode, Message: "code"})
	}()
	<-promptReady
	dialog.input.SetValue("typed")
	dialog.HandleInput("\r")
	<-done
	if gotErr != nil || gotValue != "typed" {
		t.Fatalf("value = %q, err = %v", gotValue, gotErr)
	}

	// An abort closes the prompt with the cancellation error.
	done = make(chan struct{})
	go func() {
		defer close(done)
		gotValue, gotErr = wiring.ShowAuthPrompt(dialog, ai.AuthPrompt{Type: ai.AuthPromptText, Message: "value"})
	}()
	<-promptReady
	dialog.cancel()
	<-done
	if gotErr == nil {
		t.Fatalf("expected cancellation, got %q", gotValue)
	}
}

// TestAuthNotifyDialog covers the event mapping.
func TestAuthNotifyDialog(t *testing.T) {
	wiring, _ := newAuthTestWiring(t)
	dialog := NewLoginDialogComponent(nil, "p", nil, "", "")
	wiring.NotifyAuthDialog(dialog, ai.AuthEvent{Type: ai.AuthEventAuthURL, URL: "https://example.com", Instructions: "open"})
	wiring.NotifyAuthDialog(dialog, ai.AuthEvent{Type: ai.AuthEventDeviceCode, VerificationURI: "https://x", UserCode: "ABC"})
	wiring.NotifyAuthDialog(dialog, ai.AuthEvent{Type: ai.AuthEventInfo, Message: "info"})
	wiring.NotifyAuthDialog(dialog, ai.AuthEvent{Type: ai.AuthEventProgress, Message: "working"})
	lines := strings.Join(dialog.Render(60), "\n")
	if !strings.Contains(lines, "ABC") || !strings.Contains(lines, "working") {
		t.Fatalf("dialog render = %q", lines)
	}
}

// messageRecorder records messages from background goroutines.
type messageRecorder struct {
	mu       sync.Mutex
	messages []string
}

func (r *messageRecorder) add(message string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.messages = append(r.messages, message)
}

func (r *messageRecorder) len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.messages)
}

func (r *messageRecorder) last() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.messages) == 0 {
		return ""
	}
	return r.messages[len(r.messages)-1]
}

// TestAuthCompleteAuthentication covers the post-login model selection.
func TestAuthCompleteAuthentication(t *testing.T) {
	wiring, session := newAuthTestWiring(t)
	statuses := &messageRecorder{}
	errorsShown := &messageRecorder{}
	warnings := &messageRecorder{}
	wiring.ShowStatus = statuses.add
	wiring.ShowError = errorsShown.add
	wiring.ShowWarning = warnings.add
	wiring.ScheduleTimer = func(ms int, fn func()) func() { return func() {} }
	authenticated := []string{}
	wiring.OnAuthenticated = func(model *ai.Model, hasModel bool) {
		if hasModel {
			authenticated = append(authenticated, model.ID)
		} else {
			authenticated = append(authenticated, "<none>")
		}
	}

	// A known previous model skips the selection logic.
	session.model = &ai.Model{ID: "m", Provider: "anthropic"}
	wiring.CompleteProviderAuthentication(context.Background(), "anthropic", "Anthropic", "oauth", session.model)
	if statuses.len() != 1 || !strings.Contains(statuses.messages[0], "Logged in to Anthropic. Credentials saved to /tmp/auth.json") {
		t.Fatalf("statuses = %v", statuses.messages)
	}
	if len(authenticated) != 1 || authenticated[0] != "<none>" {
		t.Fatalf("authenticated = %v", authenticated)
	}

	// An unknown previous model with a provider default defers the selection
	// until the catalog refresh completes. Without a catalog the runtime
	// reports that no models are available.
	session.model = &ai.Model{Provider: "unknown", ID: "unknown", API: "unknown"}
	before := errorsShown.len()
	wiring.CompleteProviderAuthentication(context.Background(), "anthropic", "Anthropic", "api_key", session.model)
	waitForCondition(t, func() bool {
		wiring.UI.(*tui.MainScreen).RenderNow(true)
		return errorsShown.len() > before
	})
	if !strings.Contains(errorsShown.last(), "no models are available for that provider") {
		t.Fatalf("errors = %v", errorsShown.messages)
	}
	if !strings.Contains(statuses.last(), "Saved API key for Anthropic. Credentials saved to /tmp/auth.json") {
		t.Fatalf("statuses = %v", statuses.messages)
	}

	// A provider without a default reports the guidance error.
	session.model = &ai.Model{Provider: "unknown", ID: "unknown", API: "unknown"}
	wiring.CompleteProviderAuthentication(context.Background(), "llama.cpp", "llama.cpp", "api_key", session.model)
	if errorsShown.len() == 0 || !strings.Contains(errorsShown.last(), "No llama.cpp models are loaded") {
		t.Fatalf("errors = %v", errorsShown.messages)
	}

	// A provider without a configured default reports the guidance error
	// synchronously (no deferral).
	session.model = &ai.Model{Provider: "unknown", ID: "unknown", API: "unknown"}
	wiring.CompleteProviderAuthentication(context.Background(), "no-default-provider", "NoDefault", "api_key", session.model)
	if !strings.Contains(errorsShown.last(), `no default model is configured for provider "no-default-provider"`) {
		t.Fatalf("errors = %v", errorsShown.messages)
	}
	_ = warnings
}
