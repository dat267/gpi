package interactive

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/dat267/gpi/ai"
	"github.com/dat267/gpi/coding"
	"github.com/dat267/gpi/tui"
)

// TestAppComposition covers the interactive-mode object graph: construction,
// mounting, focus and the submit/key wiring.
func TestAppComposition(t *testing.T) {
	app, cleanup := newTestApp(t)
	defer cleanup()

	app.Init(context.Background())
	if len(app.UI.GetMountedRoots()) == 0 {
		t.Fatal("no mounted roots")
	}
	if app.UI.GetFocusedComponent() != app.DefaultEditor {
		t.Fatal("editor not focused")
	}
	if !app.Lifecycle.IsInitialized() {
		t.Fatal("lifecycle not marked initialized")
	}

	// A slash command routes through the submit handler into the chat.
	app.Submit.HandleSubmit(context.Background(), "/session")
	rendered := renderAppChat(app)
	if !strings.Contains(rendered, "Session") {
		t.Fatalf("chat = %q", rendered)
	}

	// The key wiring installs the editor escape/action handlers.
	app.Key.SetupKeyHandlers(func() int64 { return time.Now().UnixMilli() })
	if app.DefaultEditor.OnEscape == nil {
		t.Fatal("escape handler not installed")
	}
	if len(app.DefaultEditor.ActionHandlers) == 0 {
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

	waitForConditionWithin(t, func() bool { return app.Lifecycle.IsInitialized() }, 6*time.Second)
	app.Startup.QueueUserInput("hello from the smoke test")

	// The loop forwards the input to the session; with no model the prompt
	// errors, but the user message is recorded before the model call.
	waitForConditionWithin(t, func() bool {
		var sawUser, sawAssistant bool
		for _, message := range app.Session.Messages() {
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
	// with a background render.
	if screen, ok := app.UI.(*tui.MainScreen); ok {
		screen.DisableAutoRender()
	}

	cleanup := func() {
		app.Lifecycle.UnregisterSignalHandlers()
		tui.SetKeybindings(previous)
	}
	return app, cleanup
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

func renderAppChat(app *App) string {
	lines := app.Chat.Render(80)
	return coding.StripAnsi(strings.Join(lines, "\n"))
}
