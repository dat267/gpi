package interactive

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/dat267/gpi/ai"
	"github.com/dat267/gpi/coding"
	"github.com/dat267/gpi/tui"
)

// BenchmarkScrollLongTranscript measures per-frame render cost on a long
// transcript (fullscreen viewport).
func BenchmarkScrollLongTranscript(b *testing.B) {
	app, cleanup := newTestAppB(b)
	defer cleanup()

	screen, _ := app.UI.(*tui.AltScreen)
	screen.Start()
	screen.DisableAutoRender()

	for i := 0; i < benchmarkMessageCount; i++ {
		app.Events.HandleEvent(&coding.SessionEvent{
			Type: coding.SessionMessageStart,
			Agent: agentEvent("message_start", &ai.UserMessage{
				Content: ai.StringOrBlocks{Text: fmt.Sprintf("user message %d with some reasonably long text that wraps across lines in the terminal viewport", i)},
			}),
		})
		assistant := &ai.AssistantMessage{
			API: ai.APIAnthropicMessages, Provider: "test", Model: "m",
			Content: ai.ContentList{ai.TextContent{Text: fmt.Sprintf("# assistant reply %d\n\n```go\nfunc f%d() int { return %d }\n```\n\nsome paragraph with **bold** and `code` spans that wraps quite a bit as well to fill lines\n", i, i, i)}},
		}
		app.Events.HandleEvent(&coding.SessionEvent{
			Type: coding.SessionMessageStart, Agent: agentEvent("message_start", assistant),
		})
		app.Events.HandleEvent(&coding.SessionEvent{
			Type: coding.SessionMessageEnd, Agent: agentEvent("message_end", assistant),
		})
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		app.UI.RenderNow(true)
	}
}

func newTestAppB(tb testing.TB) (*App, func()) {
	SetCustomThemesDir(tb.TempDir())
	SetRegisteredThemes(nil)
	SetTrueColorSupport(true)
	SetStyleColorsEnabled(true)
	dark := "dark"
	InitTheme(dark, false)

	appKeybindings := NewAppKeybindingsManager(nil, "")
	previous := tui.GetKeybindings()
	tui.SetKeybindings(appKeybindings.KeybindingsManager)

	dir := tb.TempDir()
	refresh := false
	credentials := ai.NewInMemoryCredentialStore()
	if _, err := credentials.Modify("anthropic", func(*ai.Credential) (*ai.Credential, error) {
		return &ai.Credential{Type: ai.CredentialAPIKey, APIKey: &ai.ApiKeyCredential{Key: "test-key"}}, nil
	}, context.Background()); err != nil {
		tb.Fatalf("seed credential: %v", err)
	}
	runtime, err := coding.CreateModelRuntime(coding.CreateModelRuntimeOptions{
		Credentials: credentials, RefreshOnCreate: &refresh,
	})
	if err != nil {
		tb.Fatalf("create runtime: %v", err)
	}
	anthropicModels := runtime.GetModels("anthropic")
	if len(anthropicModels) == 0 {
		tb.Fatal("no anthropic models")
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
		tb.Fatalf("create session: %v", err)
	}

	app := NewApp(AppOptions{
		Cwd: dir, AgentDir: dir,
		Terminal: &fakeRendererTerminal{width: 100, height: 30},
		TuiMode:  "fullscreen", Version: "1.0.0", AppName: "pi", QuietStartup: true,
		Settings: settings, Session: created.Session, Runtime: runtime, SessionMgr: sessions,
		Keybindings: appKeybindings, InitialThemeSetting: &dark,
		Exit:           func(int) {},
		RegisterSignal: func(sig os.Signal, handler func()) func() { return func() {} },
	})
	app.Init(context.Background())
	return app, func() {
		app.Lifecycle.UnregisterSignalHandlers()
		tui.SetKeybindings(previous)
	}
}

var benchmarkMessageCount = 500
