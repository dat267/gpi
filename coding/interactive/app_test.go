package interactive

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dat267/pier/ai"
	"github.com/dat267/pier/coding"
	"github.com/dat267/pier/tui"
)

// TestAppComposition covers the interactive-mode object graph: construction,
// mounting, focus and the submit/key wiring.
func TestAppComposition(t *testing.T) {
	app, cleanup := newTestApp(t)
	defer cleanup()

	app.Init(context.Background())
	if len(app.ui.GetMountedRoots()) == 0 {
		t.Fatal("no mounted roots")
	}
	if app.ui.GetFocusedComponent() != app.defaultEditor {
		t.Fatal("editor not focused")
	}
	if !app.lifecycle.IsInitialized() {
		t.Fatal("lifecycle not marked initialized")
	}

	// A slash command routes through the submit handler into the chat. The
	// session panel is built off the loop and posted back to it (D159), so the
	// test plays the loop until it lands.
	app.submit.HandleSubmit(context.Background(), "/session")
	waitForConditionWithin(t, func() bool {
		app.ui.RenderNow(true)
		return strings.Contains(renderAppChat(app), "Session")
	}, 5*time.Second)

	// The key wiring installs the editor escape/action handlers.
	app.key.SetupKeyHandlers(func() int64 { return time.Now().UnixMilli() })
	if app.defaultEditor.OnEscape == nil {
		t.Fatal("escape handler not installed")
	}
	if len(app.defaultEditor.ActionHandlers) == 0 {
		t.Fatal("action handlers not installed")
	}
}

// TestAppEndToEndLoop drives the app's main input loop end to end: an input
// queued from the editor reaches the session and the transcript.
func TestAppEndToEndLoop(t *testing.T) {
	app, cleanup := newTestApp(t)
	defer cleanup()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		app.Run(ctx)
	}()

	waitForConditionWithin(t, func() bool { return app.lifecycle.IsInitialized() }, 6*time.Second)
	app.startup.QueueUserInput("hello from the smoke test")

	// The loop forwards the input to the session; with no model the prompt
	// errors, but the user message is recorded before the model call.
	waitForConditionWithin(t, func() bool {
		var sawUser, sawAssistant bool
		for _, message := range app.session.Messages() {
			switch typed := message.(type) {
			case *ai.UserMessage:
				if ai.ContentText(typed.Content, "") == "hello from the smoke test" {
					sawUser = true
				}
			case *ai.AssistantMessage:
				if ai.ContentText(ai.StringOrBlocks{Blocks: typed.Content}, "") == "ack" {
					sawAssistant = true
				}
			}
		}
		return sawUser && sawAssistant
	}, 6*time.Second)

	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("run loop did not exit after cancellation")
	}
}

func newTestApp(t *testing.T) (*App, func()) {
	t.Helper()
	SetCustomThemesDir(t.TempDir())
	SetRegisteredThemes(nil)
	SetTrueColorSupport(true)
	SetStyleColorsEnabled(true)
	dark := "dark"
	InitTheme(dark, false)

	appKeybindings := NewAppKeybindingsManager(nil, "")
	previous := tui.GetKeybindings()
	tui.SetKeybindings(appKeybindings.KeybindingsManager)

	dir := t.TempDir()
	refresh := false
	credentials := ai.NewInMemoryCredentialStore()
	if _, err := credentials.Modify("anthropic", func(*ai.Credential) (*ai.Credential, error) {
		return &ai.Credential{Type: ai.CredentialAPIKey, APIKey: &ai.ApiKeyCredential{Key: "test-key"}}, nil
	}, context.Background()); err != nil {
		t.Fatalf("seed credential: %v", err)
	}
	runtime, err := coding.CreateModelRuntime(coding.CreateModelRuntimeOptions{
		Credentials: credentials, RefreshOnCreate: &refresh,
	})
	if err != nil {
		t.Fatalf("create runtime: %v", err)
	}
	anthropicModels := runtime.GetModels("anthropic")
	if len(anthropicModels) == 0 {
		t.Fatal("no anthropic models in the catalog")
	}
	settings := coding.NewInMemorySettingsManager(nil, coding.SettingsManagerCreateOptions{})
	persist := false
	sessions := coding.NewSessionManager(dir, &coding.SessionManagerOptions{Persist: &persist})
	fauxModel := anthropicModels[0]
	streamFn := func(model *ai.Model, context ai.TranscriptContext, options *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
		stream := ai.NewAssistantMessageEventStream()
		go func() {
			message := &ai.AssistantMessage{
				API: ai.APIAnthropicMessages, Provider: model.Provider, Model: model.ID,
				Content: ai.ContentList{ai.TextContent{Text: "ack"}}, StopReason: ai.StopStop,
			}
			stream.Push(ai.AssistantMessageEvent{Type: ai.EventDone, Reason: ai.StopStop, Message: message})
		}()
		return stream
	}
	created, err := coding.CreateAgentSession(context.Background(), &coding.CreateAgentSessionOptions{
		Cwd: dir, AgentDir: dir, Model: fauxModel, StreamFn: streamFn,
		SessionManager: sessions, ModelRuntime: runtime, SettingsManager: settings,
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	app := NewApp(AppOptions{
		Cwd:                 dir,
		AgentDir:            dir,
		Terminal:            &fakeRendererTerminal{width: 80, height: 24},
		TuiMode:             "regular",
		Version:             "1.0.0",
		AppName:             "pi",
		QuietStartup:        true,
		Settings:            settings,
		Session:             created.Session,
		Runtime:             runtime,
		SessionMgr:          sessions,
		Keybindings:         appKeybindings,
		InitialThemeSetting: &dark,
		Exit:                func(int) {},
		RegisterSignal:      func(os.Signal, func()) func() { return func() {} },
	})

	// Disable the render timer so the test's direct component access cannot race
	// with a background render. app.UI is a TuiReference (D105), so resolve the
	// concrete renderer instead of type-asserting the reference itself.
	disableAutoRenderForTest(app)

	cleanup := func() {
		app.lifecycle.UnregisterSignalHandlers()
		tui.SetKeybindings(previous)
	}
	return app, cleanup
}

// disableAutoRenderForTest turns off the render timer on the app's current
// renderer (TuiReference-aware: the D105 forwarding reference hides the
// concrete MainScreen from a plain type assertion).
func disableAutoRenderForTest(app *App) {
	if app == nil {
		return
	}
	if screen, ok := app.currentRenderer().(*tui.MainScreen); ok {
		screen.DisableAutoRender()
	}
}

// waitForConditionWithin polls until the condition holds or the deadline
// passes (a longer deadline than the shared helper for the end-to-end loop,
// which runs under heavy suite load).
func waitForConditionWithin(t *testing.T, condition func() bool, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met before timeout")
}

// renderAppChat renders the chat without racing the app's loop: when the loop
// is running it executes the render on the loop goroutine (the containers are
// loop-owned since D146 removed the container lock); otherwise nothing else
// paints and the test renders directly.
func renderAppChat(app *App) string {
	done := make(chan []string, 1)
	app.ui.Post(func() {
		lines := app.chat.Render(80)
		select {
		case done <- lines:
		default:
		}
	})
	select {
	case lines := <-done:
		return coding.StripAnsi(strings.Join(lines, "\n"))
	case <-time.After(200 * time.Millisecond):
		// No loop consumer: render from the test goroutine.
		lines := app.chat.Render(80)
		return coding.StripAnsi(strings.Join(lines, "\n"))
	}
}

// TestAppWiringCompleteness guards the composition root's failure mode: a
// wiring field that NewApp silently never assigns. /debug (WriteDebugLog) and
// right-click paste both shipped unwired this way. Each wiring now owns its
// constructor, so the required hooks are asserted here after NewApp.
func TestAppWiringCompleteness(t *testing.T) {
	app, cleanup := newTestApp(t)
	defer cleanup()

	required := []struct {
		name string
		set  bool
	}{
		{"Commands.WriteDebugLog", app.commands.WriteDebugLog != nil},
		{"Commands.CopyToClipboard", app.commands.CopyToClipboard != nil},
		{"Commands.ReloadNow", app.commands.ReloadNow != nil},
		{"Commands.ApplyReloadedSettings", app.commands.ApplyReloadedSettings != nil},
		{"Commands.ExportToHTML", app.commands.ExportToHTML != nil},
		{"Commands.MarkdownTheme", app.commands.MarkdownTheme != nil},
		{"Commands.RunDetached", app.commands.RunDetached != nil},
		{"Autocomplete.Skills", app.autocomplete.Skills != nil},
		{"Autocomplete.LoginProviders", app.autocomplete.LoginProviders != nil},
		{"Runner.ShowLoadedResources", app.runner.ShowLoadedResources != nil},
		{"Runner.OnSignal", app.runner.OnSignal != nil},
		{"Runner.Prompt", app.runner.Prompt != nil},
		{"Runner.RefreshModelCatalogs", app.runner.RefreshModelCatalogs != nil},
		{"Runner.CheckVersion", app.runner.CheckVersion != nil},
		{"Runner.TakeCrash", app.runner.TakeCrash != nil},
		{"Runner.ShowError", app.runner.ShowError != nil},
		{"Runner.RequestRender", app.runner.RequestRender != nil},
		{"Runner.SetupKeyHandlers", app.runner.SetupKeyHandlers != nil},
		{"Runner.SetupSubmitHandler", app.runner.SetupSubmitHandler != nil},
		{"Key.OnToolsExpand", app.key.OnToolsExpand != nil},
		{"Key.OnFollowUp", app.key.OnFollowUp != nil},
		{"Key.OnDequeue", app.key.OnDequeue != nil},
		{"Key.OnPasteImage", app.key.OnPasteImage != nil},
		{"Key.OnModelSelect", app.key.OnModelSelect != nil},
		{"Key.OnSessionTree", app.key.OnSessionTree != nil},
		{"Key.OnExit", app.key.OnExit != nil},
		{"Submit.Handlers.HandleDebugCommand", app.submit.Handlers.HandleDebugCommand != nil},
		{"Submit.Handlers.HandleReloadCommand", app.submit.Handlers.HandleReloadCommand != nil},
		{"Submit.Handlers.HandleCompactCommand", app.submit.Handlers.HandleCompactCommand != nil},
		{"Submit.Handlers.HandleExportCommand", app.submit.Handlers.HandleExportCommand != nil},
		{"Submit.Handlers.HandleHotkeysCommand", app.submit.Handlers.HandleHotkeysCommand != nil},
		{"Submit.Handlers.ShowTrustSelector", app.submit.Handlers.ShowTrustSelector != nil},
		{"Submit.Handlers.Shutdown", app.submit.Handlers.Shutdown != nil},
		{"Trust.Stop", app.trust.Stop != nil},
		{"Startup.RenderInitialMessages", app.startup.RenderInitialMessages != nil},
		{"Transcript.RenderProjectTrustWarning", app.transcript.RenderProjectTrustWarning != nil},
		{"Startup.ShowError", app.startup.ShowError != nil},
		{"Startup.ShowStatus", app.startup.ShowStatus != nil},
		{"Startup.RequestRender", app.startup.RequestRender != nil},
		{"Selectors.ShowError", app.selectors.ShowError != nil},
		{"Selectors.RebuildChat", app.selectors.RebuildChat != nil},
		{"Selectors.SetNavigatedEditorText", app.selectors.SetNavigatedEditorText != nil},
		{"Selectors.FlushCompactionQueue", app.selectors.FlushCompactionQueue != nil},
		{"SettingsW.RequestRender", app.settingsW.RequestRender != nil},
		{"Models.ShowError", app.models.ShowError != nil},
		{"Sessions.Shutdown", app.sessions.Shutdown != nil},
		{"Auth.ShowError", app.auth.ShowError != nil},
	}
	for _, check := range required {
		if !check.set {
			t.Errorf("%s is not wired", check.name)
		}
	}
}

// The navigated point's message text lands in the editor only while the editor
// is empty, upstream's `result.editorText && !this.editor.getText().trim()`:
// re-editing a draft must not be overwritten by the tree navigation.
func TestNavigatedEditorTextKeepsDrafts(t *testing.T) {
	app, cleanup := newTestApp(t)
	defer cleanup()

	app.selectors.SetNavigatedEditorText("from the tree")
	if got := app.defaultEditor.GetText(); got != "from the tree" {
		t.Errorf("editor text = %q", got)
	}

	app.defaultEditor.SetText("my draft")
	app.selectors.SetNavigatedEditorText("another point")
	if got := app.defaultEditor.GetText(); got != "my draft" {
		t.Errorf("a draft was overwritten: %q", got)
	}

	// Whitespace-only counts as empty, as upstream's trim() does.
	app.defaultEditor.SetText("   ")
	app.selectors.SetNavigatedEditorText("third point")
	if got := app.defaultEditor.GetText(); got != "third point" {
		t.Errorf("whitespace-only editor = %q", got)
	}
}

// Upstream draws the untrusted-project warning from renderInitialMessages
// (interactive-mode.ts:4062) — the function the tree navigation calls to rebuild
// the transcript, so the warning comes back with it. The port's
// RenderProjectTrustWarningIfNeeded was only ever called by tests, so the
// warning never appeared on screen at all.
func TestRenderInitialMessagesWarnsAboutUntrustedProject(t *testing.T) {
	app, cleanup := newTestApp(t)
	defer cleanup()

	cwd := app.sessionMgr.GetCwd()
	configDir := filepath.Join(cwd, coding.ConfigDirName, "skills")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// SettingsManagerCreateOptions.ProjectTrusted defaults to true.
	app.settings.SetProjectTrusted(false)
	if app.settings.IsProjectTrusted() {
		t.Fatal("the test project should be untrusted")
	}
	if !coding.HasTrustRequiringProjectResources(cwd) {
		t.Fatalf("expected %s to require trust", configDir)
	}

	app.chat.Clear()
	app.transcript.RenderInitialMessages()
	rendered := strings.Join(renderChat(t, app.chat), "\n")
	if !strings.Contains(rendered, "This project is not trusted") {
		t.Errorf("the trust warning is missing from the transcript:\n%s", rendered)
	}
}
