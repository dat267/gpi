package interactive

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dat267/pier/ai"
	"github.com/dat267/pier/coding"
	"github.com/dat267/pier/tui"
)

// commandTestSession implements CommandSession.
type commandTestSession struct {
	stats       *coding.SessionStats
	lastText    string
	name        string
	exported    []string
	exportErr   error
	compacted   []string
	cacheStatus *coding.CacheWarmingStatus
	runtime     *coding.ModelRuntime
}

func (s *commandTestSession) GetSessionStats() *coding.SessionStats { return s.stats }
func (s *commandTestSession) GetLastAssistantText() string          { return s.lastText }
func (s *commandTestSession) SetSessionName(name string)            { s.name = name }
func (s *commandTestSession) ExportToJsonl(outputPath string) (string, error) {
	if s.exportErr != nil {
		return "", s.exportErr
	}
	s.exported = append(s.exported, outputPath)
	return outputPath, nil
}
func (s *commandTestSession) CompactSession(_ context.Context, instructions string) error {
	s.compacted = append(s.compacted, instructions)
	return nil
}
func (s *commandTestSession) GetCacheWarmingStatus() *coding.CacheWarmingStatus { return s.cacheStatus }
func (s *commandTestSession) ModelRuntime() *coding.ModelRuntime                { return s.runtime }

func newCommandTestWiring(t *testing.T) (*CommandWiring, *commandTestSession, *coding.SettingsManager) {
	t.Helper()
	SetCustomThemesDir(t.TempDir())
	SetRegisteredThemes(nil)
	SetTrueColorSupport(true)
	SetStyleColorsEnabled(true)
	InitTheme("dark", false)

	appKeybindings := NewAppKeybindingsManager(nil, "")
	previous := tui.GetKeybindings()
	tui.SetKeybindings(appKeybindings.KeybindingsManager)
	t.Cleanup(func() { tui.SetKeybindings(previous) })

	runtime, err := coding.CreateModelRuntime(coding.CreateModelRuntimeOptions{
		Credentials: ai.NewInMemoryCredentialStore(), DisableModelsJSON: true,
	})
	if err != nil {
		t.Fatalf("create runtime: %v", err)
	}
	settings := coding.NewInMemorySettingsManager(nil, coding.SettingsManagerCreateOptions{})
	session := &commandTestSession{
		stats:   &coding.SessionStats{SessionID: "sess-1", TotalMessages: 3, UserMessages: 1, AssistantMessages: 1, ToolCalls: 1, ToolResults: 1},
		runtime: runtime,
	}
	manager := coding.NewSessionManager("/tmp/proj", &coding.SessionManagerOptions{Persist: boolPtr(false)})
	wiring := &CommandWiring{
		Chat:        &tui.Container{},
		Settings:    settings,
		Session:     session,
		SessionInfo: manager,
		AppName:     "pi",
		Platform:    "linux",
	}
	return wiring, session, settings
}

// TestCommandPathArgument covers the export/import path parsing.
func TestCommandPathArgument(t *testing.T) {
	cases := []struct {
		text    string
		command string
		want    string
		ok      bool
	}{
		{"/export", "/export", "", false},
		{"/export ", "/export", "", false},
		{"/export out.html", "/export", "out.html", true},
		{"/export   out.html  extra", "/export", "out.html", true},
		{`/export "my file.html"`, "/export", "my file.html", true},
		{`/export 'a.jsonl'`, "/export", "a.jsonl", true},
		{`/export "unclosed`, "/export", "", false},
		{"/import in.jsonl", "/import", "in.jsonl", true},
		{"/exportx", "/export", "", false},
	}
	for _, testCase := range cases {
		got, ok := GetPathCommandArgument(testCase.text, testCase.command)
		if ok != testCase.ok || got != testCase.want {
			t.Errorf("%q -> %q,%v want %q,%v", testCase.text, got, ok, testCase.want, testCase.ok)
		}
	}
}

// TestCommandExportImport covers the export/import handlers.
func TestCommandExportImport(t *testing.T) {
	wiring, session, _ := newCommandTestWiring(t)
	statuses := []string{}
	errorsShown := []string{}
	wiring.ShowStatus = func(message string) { statuses = append(statuses, message) }
	wiring.ShowError = func(message string) { errorsShown = append(errorsShown, message) }

	// JSONL export.
	wiring.HandleExportCommand(context.Background(), "/export out.jsonl")
	if len(session.exported) != 1 || statuses[len(statuses)-1] != "Session exported to: out.jsonl" {
		t.Fatalf("exported = %v statuses = %v", session.exported, statuses)
	}
	// HTML export.
	wiring.ExportToHTML = func(outputPath string) (string, error) { return "out.html", nil }
	wiring.HandleExportCommand(context.Background(), "/export out.html")
	if statuses[len(statuses)-1] != "Session exported to: out.html" {
		t.Fatalf("statuses = %v", statuses)
	}
	// A failing export reports the error.
	wiring.ExportToHTML = func(string) (string, error) { return "", errors.New("nope") }
	wiring.HandleExportCommand(context.Background(), "/export bad.html")
	if !strings.Contains(errorsShown[len(errorsShown)-1], "Failed to export session: nope") {
		t.Fatalf("errors = %v", errorsShown)
	}

	// Import without a path reports the usage error.
	wiring.HandleImportCommand(context.Background(), "/import")
	if errorsShown[len(errorsShown)-1] != "Usage: /import <path.jsonl>" {
		t.Fatalf("errors = %v", errorsShown)
	}
	// A declined confirm cancels.
	wiring.ShowExtensionConfirm = func(context.Context, string, string) (bool, error) { return false, nil }
	wiring.HandleImportCommand(context.Background(), "/import in.jsonl")
	if statuses[len(statuses)-1] != "Import cancelled" {
		t.Fatalf("statuses = %v", statuses)
	}
	// A successful import.
	imported := []string{}
	wiring.ShowExtensionConfirm = func(context.Context, string, string) (bool, error) { return true, nil }
	wiring.ImportFromJSONL = func(_ context.Context, path string, _ string) (bool, error) {
		imported = append(imported, path)
		return false, nil
	}
	wiring.HandleImportCommand(context.Background(), "/import in.jsonl")
	if len(imported) != 1 || statuses[len(statuses)-1] != "Session imported from: in.jsonl" {
		t.Fatalf("imported = %v statuses = %v", imported, statuses)
	}
	// A missing-cwd error prompts and retries.
	wiring.ImportFromJSONL = func(_ context.Context, _ string, cwdOverride string) (bool, error) {
		if cwdOverride == "" {
			return false, errors.New("Missing session cwd")
		}
		return false, nil
	}
	wiring.PromptForMissingCwd = func(context.Context, string) (string, bool) { return "/tmp/other", true }
	wiring.HandleImportCommand(context.Background(), "/import in.jsonl")
	if statuses[len(statuses)-1] != "Session imported from: in.jsonl" {
		t.Fatalf("statuses = %v", statuses)
	}
}

// TestCommandCopy covers the copy handler.
func TestCommandCopy(t *testing.T) {
	wiring, session, _ := newCommandTestWiring(t)
	statuses := []string{}
	errorsShown := []string{}
	wiring.ShowStatus = func(message string) { statuses = append(statuses, message) }
	wiring.ShowError = func(message string) { errorsShown = append(errorsShown, message) }

	// No assistant text.
	wiring.HandleCopyCommand(false, false)
	if errorsShown[len(errorsShown)-1] != "No agent messages to copy yet." {
		t.Fatalf("errors = %v", errorsShown)
	}
	// A successful copy.
	session.lastText = "hello"
	copied := []string{}
	wiring.CopyToClipboard = func(text string) (bool, string) { copied = append(copied, text); return true, "" }
	wiring.HandleCopyCommand(false, false)
	if len(copied) != 1 || statuses[len(statuses)-1] != "Copied last agent message to clipboard" {
		t.Fatalf("copied = %v statuses = %v", copied, statuses)
	}
	// A failing copy reports the message.
	wiring.CopyToClipboard = func(string) (bool, string) { return false, "clipboard blocked" }
	wiring.HandleCopyCommand(false, false)
	if errorsShown[len(errorsShown)-1] != "clipboard blocked" {
		t.Fatalf("errors = %v", errorsShown)
	}
}

// TestCommandName covers the session-name command.
func TestCommandName(t *testing.T) {
	wiring, session, _ := newCommandTestWiring(t)
	warnings := []string{}
	wiring.ShowWarning = func(message string) { warnings = append(warnings, message) }

	// No name and no current name: usage warning.
	wiring.HandleNameCommand("/name")
	if warnings[len(warnings)-1] != "Usage: /name <name>" {
		t.Fatalf("warnings = %v", warnings)
	}
	// Setting a name renders the confirmation.
	wiring.HandleNameCommand("/name  my session ")
	if session.name != "my session" {
		t.Fatalf("name = %q", session.name)
	}
	lines := strings.Join(wiring.Chat.Render(80), "\n")
	if !strings.Contains(lines, "Session name set:") {
		t.Fatalf("chat = %q", lines)
	}
}

// TestCommandSessionInfo covers the session info panel.
func TestCommandSessionInfo(t *testing.T) {
	wiring, session, settings := newCommandTestWiring(t)
	settings.SetCacheWarmingMode("streaming")
	session.stats.SessionFile = "/tmp/sess.jsonl"
	session.stats.Tokens.Input = 100
	session.stats.Tokens.CacheRead = 300
	session.stats.Tokens.CacheWrite = 50
	session.stats.Tokens.Output = 40
	session.stats.Tokens.Total = 490
	session.stats.Cost = 0.5
	session.cacheStatus = &coding.CacheWarmingStatus{
		State: "scheduled",
		Decision: &coding.CacheWarmingDecision{
			EconomicsAvailable: true, WarmCost: 0.001, MissCost: 0.01,
		},
	}
	wiring.SessionInfo.AppendSessionInfo("my session")

	wiring.HandleSessionCommand(1000)
	lines := coding.StripAnsi(strings.Join(wiring.Chat.Render(120), "\n"))
	for _, expected := range []string{"Session Info", "Name: my session", "/tmp/sess.jsonl", "sess-1",
		"Total: 3", "User: 1", "Tools: 1 calls, 1 results", "Input: 450", "Cached: 300 (66.7%)",
		"Uncached: 150 (50 written to cache)", "Output: 40", "Cache Warming", "Mode: streaming",
		"Cache miss penalty: $0.010", "Refresh cost: $0.001", "Cost", "$0.500"} {
		if !strings.Contains(lines, expected) {
			t.Errorf("missing %q in:\n%s", expected, lines)
		}
	}
}

// TestCommandChangelog covers the changelog command.
func TestCommandChangelog(t *testing.T) {
	wiring, _, _ := newCommandTestWiring(t)
	dir := t.TempDir()
	coding.SetPackageDir(dir)
	defer coding.SetPackageDir("")
	if err := os.WriteFile(filepath.Join(dir, "CHANGELOG.md"), []byte("## [1.0.0] - 2026-01-01\n\n- First\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	wiring.HandleChangelogCommand()
	lines := coding.StripAnsi(strings.Join(wiring.Chat.Render(100), "\n"))
	if !strings.Contains(lines, "What's New") || !strings.Contains(lines, "First") {
		t.Fatalf("changelog = %q", lines)
	}

	// A missing changelog reports the fallback.
	empty, _, _ := newCommandTestWiring(t)
	coding.SetPackageDir(filepath.Join(dir, "missing"))
	empty.HandleChangelogCommand()
	lines = coding.StripAnsi(strings.Join(empty.Chat.Render(100), "\n"))
	if !strings.Contains(lines, "No changelog entries found.") {
		t.Fatalf("empty changelog = %q", lines)
	}
}

// TestCommandHotkeys covers the hotkeys table.
func TestCommandHotkeys(t *testing.T) {
	wiring, _, _ := newCommandTestWiring(t)
	wiring.HandleHotkeysCommand()
	lines := coding.StripAnsi(strings.Join(wiring.Chat.Render(120), "\n"))
	for _, expected := range []string{"Keyboard Shortcuts", "Navigation", "Editing", "Other",
		"Move cursor / browse history", "Send message", "Slash commands"} {
		if !strings.Contains(lines, expected) {
			t.Errorf("missing %q in:\n%s", expected, lines)
		}
	}
}

// TestCommandClearDebugEggs covers the remaining handlers.
func TestCommandClearDebugEggs(t *testing.T) {
	wiring, _, _ := newCommandTestWiring(t)
	errorsShown := []string{}
	wiring.ShowError = func(message string) { errorsShown = append(errorsShown, message) }
	wiring.ClearStatusIndicator = func() {}

	// New session.
	newSessions := 0
	wiring.NewSession = func(context.Context) (bool, error) { newSessions++; return false, nil }
	wiring.HandleClearCommand(context.Background())
	if newSessions != 1 {
		t.Fatalf("new sessions = %d", newSessions)
	}
	lines := coding.StripAnsi(strings.Join(wiring.Chat.Render(80), "\n"))
	if !strings.Contains(lines, "✓ New session started") {
		t.Fatalf("chat = %q", lines)
	}
	// A failing new session reports the error.
	wiring.NewSession = func(context.Context) (bool, error) { return false, errors.New("boom") }
	wiring.HandleClearCommand(context.Background())
	if !strings.Contains(errorsShown[len(errorsShown)-1], "Failed to create session: boom") {
		t.Fatalf("errors = %v", errorsShown)
	}

	// Debug log.
	commandsScreen := tui.NewMainScreen(&fakeRendererTerminal{width: 40, height: 10}, false, "")
	commandsScreen.DisableAutoRender()
	wiring.UI = commandsScreen
	written := ""
	wiring.WriteDebugLog = func(content string) error { written = content; return nil }
	wiring.HandleDebugCommand("2026-01-01T00:00:00Z")
	if !strings.Contains(written, "Debug output at 2026-01-01T00:00:00Z") ||
		!strings.Contains(written, "Terminal: 40x10") ||
		!strings.Contains(written, "=== Agent messages (JSONL) ===") {
		t.Fatalf("debug log = %q", written)
	}

	// Easter eggs.
	before := len(wiring.Chat.Children)
	wiring.HandleArminSaysHi(nil, 1)
	wiring.HandleDementedDelves()
	wiring.HandleDaxnuts(nil)
	if len(wiring.Chat.Children) != before+6 {
		t.Fatalf("children = %d", len(wiring.Chat.Children))
	}
	// Daxnuts only for the OpenCode Kimi K2.5 model.
	egg, _, _ := newCommandTestWiring(t)
	egg.CheckDaxnutsEasterEgg("opencode", "kimi-k2.5", nil)
	if len(egg.Chat.Children) != 2 {
		t.Fatalf("children = %d", len(egg.Chat.Children))
	}
	egg2, _, _ := newCommandTestWiring(t)
	egg2.CheckDaxnutsEasterEgg("openai", "gpt", nil)
	if len(egg2.Chat.Children) != 0 {
		t.Fatal("unexpected easter egg")
	}

	// Compact.
	session := wiring.Session.(*commandTestSession)
	wiring.HandleCompactCommand(context.Background(), "focus")
	if len(session.compacted) != 1 || session.compacted[0] != "focus" {
		t.Fatalf("compacted = %v", session.compacted)
	}
}

// TestCommandStop covers the stop teardown.
func TestCommandStop(t *testing.T) {
	wiring, _, _ := newCommandTestWiring(t)
	calls := []string{}
	wiring.ClearStatusIndicator = func() { calls = append(calls, "status") }
	wiring.Stop("transcript",
		func() { calls = append(calls, "selector") },
		func() { calls = append(calls, "listeners") },
		func() { calls = append(calls, "footer") },
		func() { calls = append(calls, "footerData") },
		func() { calls = append(calls, "unsubscribe") },
		func(output string) { calls = append(calls, "stopTui:"+output) },
		func() { calls = append(calls, "signals") })
	want := "selector,status,listeners,footer,footerData,unsubscribe,stopTui:transcript,signals"
	if strings.Join(calls, ",") != want {
		t.Fatalf("calls = %v", calls)
	}
}
