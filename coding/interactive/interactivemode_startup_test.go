package interactive

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dat267/pier/agent"
	"github.com/dat267/pier/ai"
	"github.com/dat267/pier/coding"
	"github.com/dat267/pier/tui"
)

// startupTestSession implements StartupSession.
type startupTestSession struct {
	messages []ai.Message
	scoped   []coding.ScopedModel
	runtime  *coding.ModelRuntime
	model    *ai.Model
}

func (s *startupTestSession) Messages() []ai.Message                    { return s.messages }
func (s *startupTestSession) ScopedModels() []coding.ScopedModel        { return s.scoped }
func (s *startupTestSession) ModelRuntime() *coding.ModelRuntime        { return s.runtime }
func (s *startupTestSession) Model() *ai.Model                          { return s.model }
func (s *startupTestSession) GetToolDefinition(string) *agent.AgentTool { return nil }

func newStartupTestWiring(t *testing.T) (*StartupWiring, *coding.SettingsManager) {
	t.Helper()
	SetCustomThemesDir(t.TempDir())
	SetRegisteredThemes(nil)
	SetTrueColorSupport(true)
	SetStyleColorsEnabled(true)
	InitTheme("dark", false)

	runtime, err := coding.CreateModelRuntime(coding.CreateModelRuntimeOptions{
		Credentials: ai.NewInMemoryCredentialStore(), DisableModelsJSON: true,
	})
	if err != nil {
		t.Fatalf("create runtime: %v", err)
	}
	settings := coding.NewInMemorySettingsManager(nil, coding.SettingsManagerCreateOptions{})
	session := &startupTestSession{runtime: runtime}
	wiring := &StartupWiring{
		Session:         session,
		Settings:        settings,
		Version:         "1.2.3",
		Chat:            &tui.Container{},
		PendingMessages: &tui.Container{},
		LoadedResources: &tui.Container{},
	}
	return wiring, settings
}

// TestStartupUserInput covers the submission channel: buffered delivery,
// closed-queue shutdown and context cancellation.
func TestStartupUserInput(t *testing.T) {
	wiring, _ := newStartupTestWiring(t)

	// A queued input is returned immediately.
	wiring.QueueUserInput("first")
	value, ok := wiring.GetUserInput(context.Background())
	if !ok || value != "first" {
		t.Fatalf("value = %q, ok = %v", value, ok)
	}

	// A later input is buffered and received by the next read.
	wiring.QueueUserInput("second")
	select {
	case value := <-wiring.Inputs():
		if value != "second" {
			t.Fatalf("value = %q", value)
		}
	case <-time.After(time.Second):
		t.Fatal("input not delivered")
	}

	// A cancelled context returns ok=false.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, ok := wiring.GetUserInput(ctx); ok {
		t.Fatal("cancelled context returned an input")
	}
}

// TestStartupRebuildAndReset covers the chat rebuild helpers.
func TestStartupRebuildAndReset(t *testing.T) {
	wiring, _ := newStartupTestWiring(t)
	manager := coding.NewSessionManager("/tmp/proj", &coding.SessionManagerOptions{Persist: boolPtr(false)})
	manager.AppendMessage(ai.Message(&ai.UserMessage{Content: ai.StringOrBlocks{Text: "hello"}}))
	wiring.SessionInfo = manager
	wiring.Transcript = NewTranscriptRenderer(wiring.Chat, nil, wiring.Settings, nil, manager)

	wiring.RebuildChatFromMessages()
	if len(wiring.Chat.Children) == 0 {
		t.Fatal("chat not rebuilt")
	}

	// A streaming component and pending tool are cleared by the reset.
	wiring.Transcript.StreamingComponent = &AssistantMessageComponent{}
	wiring.Transcript.pendingTools["t"] = nil
	reset := 0
	wiring.RenderInitialMessages = func() { reset++ }
	wiring.RenderCurrentSessionState()
	if reset != 1 {
		t.Fatalf("reset = %d", reset)
	}
	if wiring.Transcript.StreamingComponent != nil || len(wiring.Transcript.pendingTools) != 0 {
		t.Fatal("streaming state not cleared")
	}
	if len(wiring.LoadedResources.Children) != 0 || len(wiring.PendingMessages.Children) != 0 {
		t.Fatal("containers not cleared")
	}
}

// TestStartupTmuxCheck covers the tmux keyboard warnings.
func TestStartupTmuxCheck(t *testing.T) {
	wiring, _ := newStartupTestWiring(t)

	if warning := wiring.CheckTmuxKeyboardSetup(false); warning != "" {
		t.Fatalf("warning without tmux = %q", warning)
	}
	// A failed query is silent.
	wiring.TmuxShow = func(string) (string, bool) { return "", false }
	if warning := wiring.CheckTmuxKeyboardSetup(true); warning != "" {
		t.Fatalf("warning on query failure = %q", warning)
	}
	// extended-keys off.
	wiring.TmuxShow = func(option string) (string, bool) {
		if option == "extended-keys" {
			return "off", true
		}
		return "", true
	}
	if warning := wiring.CheckTmuxKeyboardSetup(true); !strings.Contains(warning, "extended-keys is off") {
		t.Fatalf("warning = %q", warning)
	}
	// xterm format.
	wiring.TmuxShow = func(option string) (string, bool) {
		if option == "extended-keys" {
			return "on", true
		}
		return "xterm", true
	}
	if warning := wiring.CheckTmuxKeyboardSetup(true); !strings.Contains(warning, "extended-keys-format is xterm") {
		t.Fatalf("warning = %q", warning)
	}
	// csi-u is fine.
	wiring.TmuxShow = func(option string) (string, bool) {
		if option == "extended-keys" {
			return "always", true
		}
		return "csi-u", true
	}
	if warning := wiring.CheckTmuxKeyboardSetup(true); warning != "" {
		t.Fatalf("warning = %q", warning)
	}
}

// TestStartupChangelog covers the changelog display logic.
func TestStartupChangelog(t *testing.T) {
	wiring, settings := newStartupTestWiring(t)
	dir := t.TempDir()
	coding.SetPackageDir(dir)
	defer coding.SetPackageDir("")
	changelog := "## [1.2.3] - 2026-01-01\n\n- New thing [x](docs/a.md)\n\n## [1.2.2] - 2025-12-01\n\n- Old thing\n"
	if err := os.WriteFile(filepath.Join(dir, "CHANGELOG.md"), []byte(changelog), 0o644); err != nil {
		t.Fatalf("write changelog: %v", err)
	}

	// A resumed session never shows the changelog.
	wiring.Session.(*startupTestSession).messages = []ai.Message{&ai.UserMessage{}}
	if got := wiring.GetChangelogForDisplay(); got != "" {
		t.Fatalf("resumed changelog = %q", got)
	}
	wiring.Session.(*startupTestSession).messages = nil

	// A fresh install records the version and reports telemetry.
	reported := &messageRecorder{}
	wiring.ReportInstall = func(version string) error { reported.add(version); return nil }
	if got := wiring.GetChangelogForDisplay(); got != "" {
		t.Fatalf("fresh changelog = %q", got)
	}
	if version := settings.GetLastChangelogVersion(); version == nil || *version != "1.2.3" {
		t.Fatalf("version = %v", version)
	}

	// With an older last version the new entries render with normalized links.
	settings.SetLastChangelogVersion("1.2.2")
	wiring.ReportInstall = nil
	got := wiring.GetChangelogForDisplay()
	if !strings.Contains(got, "New thing") || strings.Contains(got, "Old thing") {
		t.Fatalf("changelog = %q", got)
	}
	if !strings.Contains(got, "https://github.com/earendil-works/pi/blob/v1.2.3/packages/coding-agent/docs/a.md") {
		t.Fatalf("links not normalized: %q", got)
	}

	// With the current version there is nothing new.
	settings.SetLastChangelogVersion("1.2.3")
	if got := wiring.GetChangelogForDisplay(); got != "" {
		t.Fatalf("up-to-date changelog = %q", got)
	}
}

// TestStartupMarkdownTheme covers the code-block indent setting.
func TestStartupMarkdownTheme(t *testing.T) {
	wiring, _ := newStartupTestWiring(t)
	base := tui.MarkdownTheme{CodeBlockIndent: "default"}
	// The settings default (two spaces) overrides the base theme.
	if got := wiring.GetMarkdownThemeWithSettings(base); got.CodeBlockIndent != "  " {
		t.Fatalf("indent = %q", got.CodeBlockIndent)
	}
	indent := "    "
	wiring.Settings = coding.NewInMemorySettingsManager(&coding.Settings{
		Markdown: &coding.SettingsMarkdown{CodeBlockIndent: &indent},
	}, coding.SettingsManagerCreateOptions{})
	if got := wiring.GetMarkdownThemeWithSettings(base); got.CodeBlockIndent != "    " {
		t.Fatalf("indent = %q", got.CodeBlockIndent)
	}
}

// TestStartupProviderCount covers the footer provider count.
func TestStartupProviderCount(t *testing.T) {
	wiring, _ := newStartupTestWiring(t)
	provider := coding.NewFooterDataProvider("/tmp/proj", coding.FooterDataProviderOptions{DisableWatch: true})
	defer provider.Dispose()
	wiring.FooterData = provider

	wiring.UpdateAvailableProviderCount()
	if got := provider.GetAvailableProviderCount(); got != 0 {
		t.Fatalf("count = %d", got)
	}

	// Scoped models win over the snapshot.
	session := wiring.Session.(*startupTestSession)
	session.scoped = []coding.ScopedModel{
		{Model: &ai.Model{Provider: "p", ID: "a"}},
		{Model: &ai.Model{Provider: "p", ID: "b"}},
		{Model: &ai.Model{Provider: "q", ID: "c"}},
	}
	wiring.UpdateAvailableProviderCount()
	if got := provider.GetAvailableProviderCount(); got != 2 {
		t.Fatalf("count = %d", got)
	}
}

// TestStartupAnthropicWarning covers the subscription-auth warning.
func TestStartupAnthropicWarning(t *testing.T) {
	wiring, settings := newStartupTestWiring(t)
	warnings := []string{}
	wiring.ShowWarning = func(message string) { warnings = append(warnings, message) }

	// The setting can disable the warning.
	settings.SetWarnings(coding.SettingsWarnings{AnthropicExtraUsage: boolPtr(false)})
	wiring.MaybeWarnAboutAnthropicSubscriptionAuth(context.Background(), &ai.Model{Provider: "anthropic"})
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v", warnings)
	}
	settings.SetWarnings(coding.SettingsWarnings{AnthropicExtraUsage: boolPtr(true)})

	// A non-anthropic model is ignored.
	wiring.MaybeWarnAboutAnthropicSubscriptionAuth(context.Background(), &ai.Model{Provider: "openai"})
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v", warnings)
	}

	// An anthropic model without a subscription key is ignored.
	wiring.Session.(*startupTestSession).model = &ai.Model{Provider: "anthropic", ID: "m"}
	wiring.MaybeWarnAboutAnthropicSubscriptionAuth(context.Background(), nil)
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v", warnings)
	}
}
