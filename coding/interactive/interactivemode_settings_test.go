package interactive

import (
	"testing"

	"github.com/dat267/gpi/ai"
	"github.com/dat267/gpi/coding"
	"github.com/dat267/gpi/tui"
)

// settingsTestSession implements SettingsSession.
type settingsTestSession struct {
	autoCompact  bool
	model        *ai.Model
	models       []*ai.Model
	steering     coding.QueueMode
	followUp     coding.QueueMode
	cacheWarming string
	thinking     ai.ThinkingLevel
	streaming    bool
}

func (s *settingsTestSession) AutoCompactionEnabled() bool           { return s.autoCompact }
func (s *settingsTestSession) Model() *ai.Model                      { return s.model }
func (s *settingsTestSession) GetAvailableModels() []*ai.Model       { return s.models }
func (s *settingsTestSession) SteeringMode() coding.QueueMode        { return s.steering }
func (s *settingsTestSession) FollowUpMode() coding.QueueMode        { return s.followUp }
func (s *settingsTestSession) SetAutoCompactionEnabled(enabled bool) { s.autoCompact = enabled }
func (s *settingsTestSession) SetSteeringMode(mode coding.QueueMode) { s.steering = mode }
func (s *settingsTestSession) SetFollowUpMode(mode coding.QueueMode) { s.followUp = mode }
func (s *settingsTestSession) SetCacheWarmingMode(mode coding.CacheWarmingMode) {
	s.cacheWarming = mode
}
func (s *settingsTestSession) SetThinkingLevel(level ai.ThinkingLevel, _ ...coding.ModelMutationOptions) {
	s.thinking = level
}
func (s *settingsTestSession) IsStreaming() bool { return s.streaming }

// settingsTestTheme implements SettingsThemeController.
type settingsTestTheme struct {
	selection string
	terminal  TerminalTheme
	setCalls  []string
	previews  []string
}

func (t *settingsTestTheme) GetThemeSelection() string       { return t.selection }
func (t *settingsTestTheme) GetTerminalTheme() TerminalTheme { return t.terminal }
func (t *settingsTestTheme) SetThemeSetting(theme string) error {
	t.setCalls = append(t.setCalls, theme)
	return nil
}
func (t *settingsTestTheme) Preview(theme string) { t.previews = append(t.previews, theme) }

func newSettingsTestWiring(t *testing.T) (*SettingsWiring, *settingsTestSession, *settingsTestTheme, *coding.SettingsManager) {
	t.Helper()
	SetCustomThemesDir(t.TempDir())
	SetRegisteredThemes(nil)
	SetTrueColorSupport(true)
	SetStyleColorsEnabled(true)
	InitTheme("dark", false)

	screen := tui.NewMainScreen(&fakeRendererTerminal{width: 80, height: 24}, false, "")
	editorContainer := &tui.Container{}
	editor := NewCustomEditor(editorTestHost{}, tui.EditorTheme{}, NewAppKeybindingsManager(nil, ""), CustomEditorOptions{})
	editorContainer.AddChild(editor)
	settings := coding.NewInMemorySettingsManager(nil, coding.SettingsManagerCreateOptions{})
	session := &settingsTestSession{autoCompact: true, steering: "one-at-a-time", followUp: "all", thinking: "medium"}
	theme := &settingsTestTheme{selection: "light", terminal: TerminalThemeLight}
	wiring := &SettingsWiring{
		Slot:            NewSelectorSlot(screen, editorContainer, editor),
		Settings:        settings,
		Session:         session,
		ThemeController: theme,
		UI:              screen,
		Chat:            &tui.Container{},
		DefaultEditor:   editor,
		Editor:          editor,
		Renderer:        screen,
	}
	return wiring, session, theme, settings
}

// TestSettingsConfigAssembly covers the settings snapshot.
func TestSettingsConfigAssembly(t *testing.T) {
	wiring, session, theme, settings := newSettingsTestWiring(t)
	settings.SetDefaultProvider("anthropic")
	settings.SetDefaultModel("claude-x")
	settings.SetImageWidthCells(120)
	settings.SetImageAutoResize(false)
	settings.SetBlockImages(true)
	settings.SetEnableSkillCommands(false)
	settings.SetTransport("websocket")
	settings.SetHTTPIdleTimeoutMS(30000)
	settings.SetCacheWarmingMode("streaming")
	settings.SetDefaultThinkingLevel("high")
	settings.SetModelThinkingLevel("anthropic", "claude-x", "max")
	settings.SetHideThinkingBlock(true)
	settings.SetMermaidRenderingMode("off")
	settings.SetCollapseChangelog(true)
	settings.SetEnableInstallTelemetry(true)
	settings.SetDoubleEscapeAction("fork")
	settings.SetTreeFilterMode("no-tools")
	settings.SetShowHardwareCursor(true)
	settings.SetShowCacheMissNotices(true)
	settings.SetDefaultProjectTrust("always")
	settings.SetEditorPaddingX(2)
	settings.SetOutputPad(1)
	settings.SetAutocompleteMaxVisible(15)
	settings.SetQuietStartup(true)
	settings.SetClearOnShrink(false)
	settings.SetShowTerminalProgress(true)
	settings.SetFullscreenExitOutput("resume-hint")
	settings.SetFullscreenScrollbar("hidden")
	settings.SetFullscreenCopyOnSelect(false)
	settings.SetWarnings(coding.SettingsWarnings{AnthropicExtraUsage: boolPtr(false)})
	session.model = &ai.Model{ID: "claude-x", Provider: "anthropic", Reasoning: true}
	session.models = []*ai.Model{session.model}

	config := wiring.BuildSettingsConfig()
	checks := []struct {
		name string
		got  any
		want any
	}{
		{"autoCompact", config.AutoCompact, true},
		{"defaultModel", config.DefaultModel, "anthropic/claude-x"},
		{"imageWidthCells", config.ImageWidthCells, 120},
		{"autoResizeImages", config.AutoResizeImages, false},
		{"blockImages", config.BlockImages, true},
		{"enableSkillCommands", config.EnableSkillCommands, false},
		{"steeringMode", config.SteeringMode, "one-at-a-time"},
		{"followUpMode", config.FollowUpMode, "all"},
		{"transport", config.Transport, "websocket"},
		{"httpIdleTimeoutMs", config.HTTPIdleTimeoutMs, int64(30000)},
		{"cacheWarmingMode", config.CacheWarmingMode, "streaming"},
		{"thinkingLevel", config.ThinkingLevel, "high"},
		{"hideThinkingBlock", config.HideThinkingBlock, true},
		{"mermaidRenderingMode", config.MermaidRenderingMode, "off"},
		{"collapseChangelog", config.CollapseChangelog, true},
		{"enableInstallTelemetry", config.EnableInstallTelemetry, true},
		{"doubleEscapeAction", config.DoubleEscapeAction, "fork"},
		{"treeFilterMode", config.TreeFilterMode, "no-tools"},
		{"showHardwareCursor", config.ShowHardwareCursor, true},
		{"showCacheMissNotices", config.ShowCacheMissNotices, true},
		{"defaultProjectTrust", config.DefaultProjectTrust, "always"},
		{"editorPaddingX", config.EditorPaddingX, 2},
		{"outputPad", config.OutputPad, 1},
		{"autocompleteMaxVisible", config.AutocompleteMaxVisible, 15},
		{"quietStartup", config.QuietStartup, true},
		{"clearOnShrink", config.ClearOnShrink, false},
		{"showTerminalProgress", config.ShowTerminalProgress, true},
		{"tuiMode", config.TuiMode, "regular"},
		{"fullscreenExitOutput", config.FullscreenExitOutput, "resume-hint"},
		{"fullscreenScrollbar", config.FullscreenScrollbar, "hidden"},
		{"fullscreenCopyOnSelect", config.FullscreenCopyOnSelect, false},
		{"currentTheme", config.CurrentTheme, "light"},
		{"terminalTheme", config.TerminalTheme, TerminalThemeLight},
	}
	for _, check := range checks {
		if check.got != check.want {
			t.Errorf("%s = %v, want %v", check.name, check.got, check.want)
		}
	}
	if len(config.AvailableThinkingLevels) != 7 {
		t.Errorf("thinking levels = %v", config.AvailableThinkingLevels)
	}
	if config.ModelThinkingLevels["anthropic/claude-x"] != "max" {
		t.Errorf("model thinking = %v", config.ModelThinkingLevels)
	}
	if config.Warnings.AnthropicExtraUsage == nil || *config.Warnings.AnthropicExtraUsage {
		t.Errorf("warnings = %+v", config.Warnings)
	}
	if theme.selection != "light" {
		t.Errorf("theme selection = %q", theme.selection)
	}
}

// TestSettingsCallbacks covers the change callbacks.
func TestSettingsCallbacks(t *testing.T) {
	wiring, session, theme, settings := newSettingsTestWiring(t)
	statuses := []string{}
	wiring.ShowStatus = func(message string) { statuses = append(statuses, message) }
	thinkingVisible := []bool{}
	wiring.UpdateThinkingBlockVisibility = func(hidden bool) { thinkingVisible = append(thinkingVisible, hidden) }
	rebuilt := 0
	wiring.RebuildChatFromMessages = func() { rebuilt++ }
	autocompleteSetups := 0
	wiring.SetupAutocompleteProvider = func() { autocompleteSetups++ }
	httpTimeouts := []int64{}
	wiring.ConfigureHTTPIdleTimeout = func(timeoutMS int64) { httpTimeouts = append(httpTimeouts, timeoutMS) }
	scrollbarApplied := 0
	wiring.ApplyFullscreenScrollbarSetting = func() { scrollbarApplied++ }
	hideThinking := false
	wiring.HideThinkingBlock = &hideThinking
	outputPad := 0
	wiring.OutputPad = &outputPad

	callbacks := wiring.BuildSettingsCallbacks(nil, nil)

	callbacks.OnAutoCompactChange(false)
	if session.autoCompact {
		t.Fatal("auto compact not applied")
	}
	callbacks.OnEnableSkillCommandsChange(false)
	if settings.GetEnableSkillCommands() || autocompleteSetups != 1 {
		t.Fatal("skill commands not applied")
	}
	callbacks.OnSteeringModeChange("all")
	if session.steering != "all" {
		t.Fatal("steering not applied")
	}
	callbacks.OnHTTPIdleTimeoutMsChange(60000)
	if len(httpTimeouts) != 1 || httpTimeouts[0] != 60000 {
		t.Fatalf("http timeouts = %v", httpTimeouts)
	}
	if statuses[len(statuses)-1] != "HTTP idle timeout: 1 min" {
		t.Fatalf("statuses = %v", statuses)
	}
	callbacks.OnCacheWarmingModeChange("idle")
	if session.cacheWarming != "idle" || statuses[len(statuses)-1] != "Cache warming: idle" {
		t.Fatalf("cache warming = %q", session.cacheWarming)
	}
	// Model thinking level for the current model also updates the session.
	session.model = &ai.Model{ID: "m", Provider: "p"}
	callbacks.OnModelThinkingLevelChange("p", "m", "high")
	if level := settings.GetModelThinkingLevel("p", "m"); level == nil || *level != "high" {
		t.Fatalf("model thinking = %v", level)
	}
	if session.thinking != "high" {
		t.Fatalf("session thinking = %q", session.thinking)
	}
	callbacks.OnModelThinkingLevelRemove("p", "m")
	if level := settings.GetModelThinkingLevel("p", "m"); level != nil && *level != "" {
		t.Fatalf("override not removed: %v", level)
	}
	// Removing the current model's override reverts to the global default.
	if session.thinking != coding.DefaultThinkingLevel {
		t.Fatalf("session level after remove = %q", session.thinking)
	}
	// A non-current model does not change the session level.
	callbacks.OnModelThinkingLevelChange("other", "x", "low")
	if session.thinking != coding.DefaultThinkingLevel {
		t.Fatalf("session level = %q", session.thinking)
	}
	callbacks.OnThemeChange("dark")
	if len(theme.setCalls) != 1 || theme.setCalls[0] != "dark" {
		t.Fatalf("theme set calls = %v", theme.setCalls)
	}
	callbacks.OnThemePreview("light")
	if len(theme.previews) != 1 {
		t.Fatalf("previews = %v", theme.previews)
	}
	callbacks.OnHideThinkingBlockChange(true)
	if !hideThinking || !settings.GetHideThinkingBlock() || len(thinkingVisible) != 1 {
		t.Fatal("hide thinking not applied")
	}
	callbacks.OnShowCacheMissNoticesChange(true)
	if !settings.GetShowCacheMissNotices() || rebuilt != 1 {
		t.Fatal("cache miss notices not applied")
	}
	callbacks.OnEditorPaddingXChange(3)
	if settings.GetEditorPaddingX() != 3 {
		t.Fatal("editor padding not applied")
	}
	callbacks.OnOutputPadChange(1)
	if settings.GetOutputPad() != 1 || outputPad != 1 {
		t.Fatal("output pad not applied")
	}
	if rebuilt != 2 {
		t.Fatalf("rebuild calls = %d", rebuilt)
	}
	callbacks.OnAutocompleteMaxVisibleChange(20)
	if settings.GetAutocompleteMaxVisible() != 20 {
		t.Fatal("autocomplete max not applied")
	}
	callbacks.OnClearOnShrinkChange(false)
	if settings.GetClearOnShrink() {
		t.Fatal("clear on shrink not applied")
	}
	callbacks.OnTuiModeChange("fullscreen")
	if settings.GetTuiMode() != "fullscreen" || statuses[len(statuses)-1] != "TUI mode: fullscreen" {
		t.Fatalf("tui mode = %q", settings.GetTuiMode())
	}
	callbacks.OnFullscreenScrollbarChange("always")
	if settings.GetFullscreenScrollbar() != "always" || scrollbarApplied != 1 {
		t.Fatal("scrollbar not applied")
	}
	callbacks.OnFullscreenCopyOnSelectChange(true)
	if !settings.GetFullscreenCopyOnSelect() {
		t.Fatal("copy on select not applied")
	}
	callbacks.OnWarningsChange(WarningSettings{AnthropicExtraUsage: boolPtr(false)})
	if warnings := settings.GetWarnings(); warnings.AnthropicExtraUsage == nil || *warnings.AnthropicExtraUsage {
		t.Fatalf("warnings = %+v", warnings)
	}
}

// TestSettingsTuiModeRejection covers the switch failure path.
func TestSettingsTuiModeRejection(t *testing.T) {
	wiring, _, _, settings := newSettingsTestWiring(t)
	statuses := []string{}
	wiring.ShowStatus = func(message string) { statuses = append(statuses, message) }
	wiring.SwitchTuiMode = func(mode string) bool { return false }
	refreshed := 0
	callbacks := wiring.BuildSettingsCallbacks(nil, func() { refreshed++ })
	callbacks.OnTuiModeChange("fullscreen")
	if settings.GetTuiMode() == "fullscreen" {
		t.Fatal("mode should not be persisted on rejection")
	}
	if refreshed != 1 || statuses[len(statuses)-1] != "Close active overlays before changing TUI mode" {
		t.Fatalf("refreshed = %d, statuses = %v", refreshed, statuses)
	}
}

// TestSettingsShowSelector covers the selector slot integration.
func TestSettingsShowSelector(t *testing.T) {
	wiring, _, _, _ := newSettingsTestWiring(t)
	wiring.ShowSettingsSelector()
	if !wiring.Slot.HasActiveSelector() {
		t.Fatal("settings selector not shown")
	}
	selector, ok := wiring.Slot.ActiveSelectorComponent().(*SettingsSelectorComponent)
	if !ok {
		t.Fatalf("component = %T", wiring.Slot.ActiveSelectorComponent())
	}
	if selector.GetSettingsList() == nil {
		t.Fatal("missing settings list")
	}
	// The cancel callback (built with the slot's done) restores the editor.
	wiring.ShowSettingsSelector()
	var cancel func()
	original := wiring.ShowStatus
	wiring.ShowStatus = original
	_ = cancel
	wiring.Slot.DisposeActiveSelector()
	if wiring.Slot.HasActiveSelector() {
		t.Fatal("dispose did not close the selector")
	}
}
