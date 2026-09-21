package interactive

import (
	"strconv"
	"strings"

	"github.com/dat267/pier/ai"
	"github.com/dat267/pier/coding"
	"github.com/dat267/pier/tui"
)

// Port of src/modes/interactive/components/settings-selector.ts: the main
// /settings selector with the theme, warnings and per-model thinking submenus.

// MermaidRenderingMode controls Mermaid rendering.
type MermaidRenderingMode = string

// WarningSettings are the individual warning toggles.
type WarningSettings struct {
	AnthropicExtraUsage *bool `json:"anthropicExtraUsage,omitempty"`
}

var modelPickerLayout = tui.SelectListLayoutOptions{
	MinPrimaryColumnWidth: 12, HasMin: true,
	MaxPrimaryColumnWidth: 46, HasMax: true,
}

var thinkingDescriptions = map[string]string{
	"off":     "No reasoning",
	"minimal": "Very brief reasoning (~1k tokens)",
	"low":     "Light reasoning (~2k tokens)",
	"medium":  "Moderate reasoning (~8k tokens)",
	"high":    "Deep reasoning (~16k tokens)",
	"xhigh":   "Extra-high reasoning (~32k tokens)",
	"max":     "Maximum reasoning",
}

var defaultProjectTrustLabels = []struct{ Value, Label string }{
	{"ask", "Ask"},
	{"always", "Always trust"},
	{"never", "Never trust"},
}

func defaultProjectTrustLabel(value string) string {
	for _, entry := range defaultProjectTrustLabels {
		if entry.Value == value {
			return entry.Label
		}
	}
	return value
}

func defaultProjectTrustByLabel(label string) (string, bool) {
	for _, entry := range defaultProjectTrustLabels {
		if entry.Label == label {
			return entry.Value, true
		}
	}
	return "", false
}

// SettingsConfig is the settings snapshot shown by the selector.
type SettingsConfig struct {
	AutoCompact             bool
	DefaultModel            string
	CurrentModel            *ai.Model
	AvailableDefaultModels  []*ai.Model
	ShowImages              bool
	ImageWidthCells         int
	AutoResizeImages        bool
	BlockImages             bool
	EnableSkillCommands     bool
	SteeringMode            string
	FollowUpMode            string
	Transport               ai.Transport
	HTTPIdleTimeoutMs       int64
	CacheWarmingMode        string
	ThinkingLevel           string
	AvailableThinkingLevels []string
	ModelThinkingLevels     map[string]string
	CurrentTheme            string
	TerminalTheme           TerminalTheme
	AvailableThemes         []string
	HideThinkingBlock       bool
	MermaidRenderingMode    MermaidRenderingMode
	ShowCacheMissNotices    bool
	CollapseChangelog       bool
	EnableInstallTelemetry  bool
	DoubleEscapeAction      string
	TreeFilterMode          string
	ShowHardwareCursor      bool
	EditorPaddingX          int
	OutputPad               int
	AutocompleteMaxVisible  int
	QuietStartup            bool
	DefaultProjectTrust     string
	ClearOnShrink           bool
	ShowTerminalProgress    bool
	TuiMode                 string
	FullscreenExitOutput    string
	FullscreenScrollbar     string
	FullscreenCopyOnSelect  bool
	Warnings                WarningSettings
}

// SettingsCallbacks receive the settings changes.
type SettingsCallbacks struct {
	OnAutoCompactChange            func(bool)
	OnShowImagesChange             func(bool)
	OnImageWidthCellsChange        func(int)
	OnAutoResizeImagesChange       func(bool)
	OnBlockImagesChange            func(bool)
	OnEnableSkillCommandsChange    func(bool)
	OnSteeringModeChange           func(string)
	OnFollowUpModeChange           func(string)
	OnTransportChange              func(ai.Transport)
	OnHTTPIdleTimeoutMsChange      func(int64)
	OnCacheWarmingModeChange       func(string)
	OnModelThinkingLevelChange     func(provider string, modelID string, level string)
	OnModelThinkingLevelRemove     func(provider string, modelID string)
	OnThemeChange                  func(string)
	OnThemePreview                 func(string)
	OnHideThinkingBlockChange      func(bool)
	OnMermaidRenderingModeChange   func(MermaidRenderingMode)
	OnShowCacheMissNoticesChange   func(bool)
	OnCollapseChangelogChange      func(bool)
	OnEnableInstallTelemetryChange func(bool)
	OnDoubleEscapeActionChange     func(string)
	OnTreeFilterModeChange         func(string)
	OnShowHardwareCursorChange     func(bool)
	OnEditorPaddingXChange         func(int)
	OnOutputPadChange              func(int)
	OnAutocompleteMaxVisibleChange func(int)
	OnQuietStartupChange           func(bool)
	OnDefaultProjectTrustChange    func(string)
	OnClearOnShrinkChange          func(bool)
	OnShowTerminalProgressChange   func(bool)
	OnTuiModeChange                func(string)
	OnFullscreenExitOutputChange   func(string)
	OnFullscreenScrollbarChange    func(string)
	OnFullscreenCopyOnSelectChange func(bool)
	OnWarningsChange               func(WarningSettings)
	OnCancel                       func()
}

// warningSettingsSubmenu edits the warning toggles.
type warningSettingsSubmenu struct {
	*tui.Container

	settingsList *tui.SettingsList
	state        WarningSettings
}

func newWarningSettingsSubmenu(warnings WarningSettings, onChange func(WarningSettings), onCancel func()) *warningSettingsSubmenu {
	submenu := &warningSettingsSubmenu{Container: &tui.Container{}, state: warnings}
	enabled := warnings.AnthropicExtraUsage == nil || *warnings.AnthropicExtraUsage
	items := []tui.SettingItem{{
		ID:           "anthropic-extra-usage",
		Label:        "Anthropic extra usage",
		Description:  "Warn when Anthropic subscription auth may use paid extra usage",
		CurrentValue: boolStr(enabled),
		Values:       []string{"true", "false"},
	}}
	submenu.settingsList = tui.NewSettingsList(items, minIntLocal(len(items), 10), GetSettingsListTheme(),
		func(id string, newValue string) {
			if id != "anthropic-extra-usage" {
				return
			}
			value := newValue == "true"
			submenu.state = WarningSettings{AnthropicExtraUsage: &value}
			onChange(submenu.state)
		}, onCancel, tui.SettingsListOptions{})
	submenu.AddChild(submenu.settingsList)
	return submenu
}

func (s *warningSettingsSubmenu) HandleInput(data string) { s.settingsList.HandleInput(data) }

const clearOverrideValue = "__clear__"

func boolStr(value bool) string {
	if value {
		return "true"
	}
	return "false"
}

func modelSettingKey(model *ai.Model) string { return model.Provider + "/" + model.ID }

func modelDisplayLabel(model *ai.Model) string { return model.ID + " [" + model.Provider + "]" }

func modelThinkingOverridesSummary(overrides map[string]string) string {
	count := 0
	for _, level := range overrides {
		if level != "" {
			count++
		}
	}
	if count == 0 {
		return "none"
	}
	return itoa(count) + " configured"
}

func modelItemLabel(model *ai.Model) string {
	return model.ID + " " + ActiveTheme().Fg("muted", "["+model.Provider+"]")
}

func themeItems(availableThemes []string, currentTheme string) []tui.SelectItem {
	items := make([]tui.SelectItem, 0, len(availableThemes))
	for _, name := range availableThemes {
		prefix := "  "
		if name == currentTheme {
			prefix = "✓ "
		}
		items = append(items, tui.SelectItem{Value: name, Label: prefix + name})
	}
	return items
}

const automaticThemeValue = "/"

func singleModeThemeItems(availableThemes []string, currentTheme string) []tui.SelectItem {
	items := []tui.SelectItem{{
		Value:       automaticThemeValue,
		Label:       "  Automatic",
		Description: "Use separate themes for light and dark terminal appearance",
	}}
	return append(items, themeItems(availableThemes, currentTheme)...)
}

func preferredTheme(availableThemes []string, preferred string, hasPreferred bool, fallback string) string {
	if hasPreferred && contains(availableThemes, preferred) {
		return preferred
	}
	if contains(availableThemes, fallback) {
		return fallback
	}
	if len(availableThemes) > 0 {
		return availableThemes[0]
	}
	return fallback
}

func defaultAutomaticThemes(currentThemeSetting string, availableThemes []string) (lightTheme string, darkTheme string) {
	if light, dark, ok := ParseAutoThemeSetting(&currentThemeSetting); ok {
		return light, dark
	}
	fixed := currentThemeSetting
	hasFixed := !strings.Contains(currentThemeSetting, "/")
	themeName := preferredTheme(availableThemes, fixed, hasFixed, "dark")
	return themeName, themeName
}

func contains(values []string, value string) bool {
	for _, entry := range values {
		if entry == value {
			return true
		}
	}
	return false
}

// ThemeSubmenu selects a fixed theme or the automatic light/dark pair.
type ThemeSubmenu struct {
	*tui.Container

	inputComponent tui.Component

	callbacks            *SettingsCallbacks
	availableThemes      []string
	terminalTheme        TerminalTheme
	onDone               func(tui.SubmenuResult)
	originalThemeSetting string
	mode                 string // "single" | "automatic"
	singleTheme          string
	lightTheme           string
	darkTheme            string
}

// NewThemeSubmenu creates the theme submenu.
func NewThemeSubmenu(currentThemeSetting string, terminalTheme TerminalTheme, availableThemes []string, callbacks *SettingsCallbacks, onDone func(tui.SubmenuResult)) *ThemeSubmenu {
	submenu := &ThemeSubmenu{
		Container:            &tui.Container{},
		callbacks:            callbacks,
		availableThemes:      availableThemes,
		terminalTheme:        terminalTheme,
		onDone:               onDone,
		originalThemeSetting: currentThemeSetting,
	}
	_, _, autoTheme := ParseAutoThemeSetting(&currentThemeSetting)
	automaticLight, automaticDark := defaultAutomaticThemes(currentThemeSetting, availableThemes)
	fixedTheme := currentThemeSetting
	hasFixedTheme := !autoTheme && !strings.Contains(currentThemeSetting, "/")
	submenu.mode = "single"
	if autoTheme {
		submenu.mode = "automatic"
	}
	submenu.lightTheme = automaticLight
	submenu.darkTheme = automaticDark
	preferred := fixedTheme
	hasPreferred := hasFixedTheme
	if !hasPreferred && autoTheme {
		preferred = submenu.activeAutomaticTheme()
		hasPreferred = true
	}
	submenu.singleTheme = preferredTheme(availableThemes, preferred, hasPreferred, "dark")

	if submenu.mode == "automatic" {
		submenu.showAutomaticMenu()
	} else {
		submenu.showSingleMenu()
	}
	return submenu
}

// HandleInput processes input.
func (c *ThemeSubmenu) HandleInput(data string) {
	if c.inputComponent != nil {
		type inputHandler interface{ HandleInput(string) }
		if handler, ok := c.inputComponent.(inputHandler); ok {
			handler.HandleInput(data)
		}
	}
}

func (c *ThemeSubmenu) setContent(renderComponent tui.Component, inputComponent tui.Component) {
	c.Container.Clear()
	c.AddChild(renderComponent)
	c.inputComponent = inputComponent
}

func (c *ThemeSubmenu) showSingleMenu() {
	c.mode = "single"
	menu := NewSelectSubmenu(
		"Theme",
		"Select a theme, or choose Automatic to follow terminal appearance.",
		singleModeThemeItems(c.availableThemes, c.singleTheme),
		c.singleTheme,
		func(value string) {
			if value == automaticThemeValue {
				c.mode = "automatic"
				c.themePreview(c.themeSetting())
				c.showAutomaticMenu()
				return
			}
			c.singleTheme = value
			c.apply(value)
		},
		func() { c.cancel() },
		func(value string) {
			if value == automaticThemeValue {
				c.themePreview(c.automaticThemeSetting())
				return
			}
			c.themePreview(value)
		},
		SelectSubmenuOptions{},
	)
	c.setContent(menu, menu)
}

func (c *ThemeSubmenu) showAutomaticMenu() {
	c.mode = "automatic"
	theme := ActiveTheme()
	content := &tui.Container{}
	content.AddChild(tui.NewText(theme.Bold(theme.Fg("accent", "Automatic Theme")), 0, 0, nil))
	content.AddChild(tui.NewSpacer(1))
	content.AddChild(tui.NewText(theme.Fg("muted", "Choose themes for terminal light and dark appearance."), 0, 0, nil))
	content.AddChild(tui.NewText(theme.Fg("muted", "Light/dark detection requires terminal support."), 0, 0, nil))
	content.AddChild(tui.NewSpacer(1))

	items := []tui.SettingItem{
		{
			ID:           "light-theme",
			Label:        "Light theme",
			Description:  "Theme to use in automatic mode when the terminal is light",
			CurrentValue: c.lightTheme,
			Submenu: func(currentValue string, done func(tui.SubmenuResult)) tui.Component {
				return c.createThemeSelect(
					"Light Theme",
					"Select the theme to use for light terminal appearance",
					currentValue,
					done,
					func(value string) {
						c.lightTheme = value
						c.themePreview(c.themeSetting())
						done(tui.SubmenuResult{SelectedValue: value, HasSelectedValue: true})
					},
				)
			},
		},
		{
			ID:           "dark-theme",
			Label:        "Dark theme",
			Description:  "Theme to use in automatic mode when the terminal is dark",
			CurrentValue: c.darkTheme,
			Submenu: func(currentValue string, done func(tui.SubmenuResult)) tui.Component {
				return c.createThemeSelect(
					"Dark Theme",
					"Select the theme to use for dark terminal appearance",
					currentValue,
					done,
					func(value string) {
						c.darkTheme = value
						c.themePreview(c.themeSetting())
						done(tui.SubmenuResult{SelectedValue: value, HasSelectedValue: true})
					},
				)
			},
		},
		{
			ID:           "apply",
			Label:        "Apply",
			Description:  "Save and go back",
			CurrentValue: "save and go back",
			Values:       []string{"save and go back"},
		},
		{
			ID:           "single-mode",
			Label:        "Change mode",
			Description:  "Switch to one theme for light and dark",
			CurrentValue: "switch to single theme",
			Values:       []string{"switch to single theme"},
		},
	}

	settingsList := tui.NewSettingsList(items, minIntLocal(len(items), 10), GetSettingsListTheme(),
		func(id string, newValue string) {
			switch id {
			case "single-mode":
				c.mode = "single"
				c.singleTheme = c.activeAutomaticTheme()
				c.themePreview(c.singleTheme)
				c.showSingleMenu()
			case "apply":
				c.apply(c.automaticThemeSetting())
			}
		},
		func() { c.cancel() },
		tui.SettingsListOptions{})
	content.AddChild(settingsList)
	c.setContent(content, settingsList)
}

func (c *ThemeSubmenu) createThemeSelect(title string, description string, currentValue string, done func(tui.SubmenuResult), onSelect func(string)) *SelectSubmenu {
	return NewSelectSubmenu(
		title,
		description,
		themeItems(c.availableThemes, currentValue),
		currentValue,
		onSelect,
		func() {
			c.themePreview(c.themeSetting())
			done(tui.SubmenuResult{})
		},
		func(value string) { c.themePreview(value) },
		SelectSubmenuOptions{},
	)
}

func (c *ThemeSubmenu) themePreview(value string) {
	if c.callbacks != nil && c.callbacks.OnThemePreview != nil {
		c.callbacks.OnThemePreview(value)
	}
}

func (c *ThemeSubmenu) themeSetting() string {
	if c.mode == "automatic" {
		return c.automaticThemeSetting()
	}
	return c.singleTheme
}

func (c *ThemeSubmenu) activeAutomaticTheme() string {
	if c.terminalTheme == TerminalThemeLight {
		return c.lightTheme
	}
	return c.darkTheme
}

func (c *ThemeSubmenu) automaticThemeSetting() string {
	return c.lightTheme + "/" + c.darkTheme
}

func (c *ThemeSubmenu) apply(themeSetting string) {
	c.onDone(tui.SubmenuResult{SelectedValue: themeSetting, HasSelectedValue: true})
}

func (c *ThemeSubmenu) cancel() {
	c.themePreview(c.originalThemeSetting)
	c.onDone(tui.SubmenuResult{})
}

// Mode returns the submenu mode (test helper).
func (c *ThemeSubmenu) Mode() string { return c.mode }

// SettingsSelectorComponent is the main settings selector.
type SettingsSelectorComponent struct {
	*tui.Container

	settingsList *tui.SettingsList
}

// NewSettingsSelectorComponent creates the selector.
func NewSettingsSelectorComponent(config SettingsConfig, callbacks SettingsCallbacks) *SettingsSelectorComponent {
	followUpKey := KeyDisplayText("app.message.followUp")
	cycleThinkingKey := KeyDisplayText("app.thinking.cycle")
	currentWarnings := config.Warnings
	currentModelThinkingLevels := map[string]string{}
	for key, value := range config.ModelThinkingLevels {
		currentModelThinkingLevels[key] = value
	}
	defaultModelByValue := map[string]*ai.Model{}
	for _, model := range config.AvailableDefaultModels {
		defaultModelByValue[modelSettingKey(model)] = model
	}
	currentDefaultModelKey := ""
	if _, ok := defaultModelByValue[config.DefaultModel]; ok {
		currentDefaultModelKey = config.DefaultModel
	}
	currentModelKey := ""
	if config.CurrentModel != nil {
		currentModelKey = modelSettingKey(config.CurrentModel)
	}

	idleTimeoutLabels := make([]string, 0, len(coding.HTTPIdleTimeoutChoices))
	for _, choice := range coding.HTTPIdleTimeoutChoices {
		idleTimeoutLabels = append(idleTimeoutLabels, choice.Label)
	}
	cacheWarmingValues := append([]string{}, coding.CacheWarmingModes...)

	items := []tui.SettingItem{
		{
			ID:           "autocompact",
			Label:        "Auto-compact",
			Description:  "Automatically compact context when it gets too large",
			CurrentValue: boolStr(config.AutoCompact),
			Values:       []string{"true", "false"},
		},
		{
			ID:           "steering-mode",
			Label:        "Steering mode",
			Description:  "Enter while streaming queues steering messages. 'one-at-a-time': deliver one, wait for response. 'all': deliver all at once.",
			CurrentValue: config.SteeringMode,
			Values:       []string{"one-at-a-time", "all"},
		},
		{
			ID:           "follow-up-mode",
			Label:        "Follow-up mode",
			Description:  followUpKey + " queues follow-up messages until agent stops. 'one-at-a-time': deliver one, wait for response. 'all': deliver all at once.",
			CurrentValue: config.FollowUpMode,
			Values:       []string{"one-at-a-time", "all"},
		},
		{
			ID:           "transport",
			Label:        "Transport",
			Description:  "Preferred transport for providers that support multiple transports",
			CurrentValue: config.Transport,
			Values:       []string{"sse", "websocket", "websocket-cached", "auto"},
		},
		{
			ID:           "http-idle-timeout",
			Label:        "HTTP idle timeout",
			Description:  "Maximum idle gap while waiting for HTTP headers or body chunks. Disable for local models that pause longer than five minutes.",
			CurrentValue: coding.FormatHTTPIdleTimeoutMS(config.HTTPIdleTimeoutMs),
			Values:       idleTimeoutLabels,
		},
		{
			ID:           "cache-warming-mode",
			Label:        "Cache warming",
			Description:  "off; streaming while the agent runs; idle also between runs while continuation stays profitable",
			CurrentValue: config.CacheWarmingMode,
			Values:       cacheWarmingValues,
		},
		{
			ID:           "hide-thinking",
			Label:        "Hide thinking",
			Description:  "Hide thinking blocks in assistant responses",
			CurrentValue: boolStr(config.HideThinkingBlock),
			Values:       []string{"true", "false"},
		},
		{
			ID:           "mermaid-rendering",
			Label:        "Mermaid diagrams",
			Description:  "Render Mermaid code blocks as Unicode diagrams",
			CurrentValue: config.MermaidRenderingMode,
			Values:       []string{"off", "final", "streaming"},
		},
		{
			ID:           "cache-miss-notices",
			Label:        "Cache miss notices",
			Description:  "Show transcript notices for cache costs and provider recovery diagnostics",
			CurrentValue: boolStr(config.ShowCacheMissNotices),
			Values:       []string{"true", "false"},
		},
		{
			ID:           "collapse-changelog",
			Label:        "Collapse changelog",
			Description:  "Show condensed changelog after updates",
			CurrentValue: boolStr(config.CollapseChangelog),
			Values:       []string{"true", "false"},
		},
		{
			ID:           "quiet-startup",
			Label:        "Quiet startup",
			Description:  "Disable verbose printing at startup",
			CurrentValue: boolStr(config.QuietStartup),
			Values:       []string{"true", "false"},
		},
		{
			ID:           "install-telemetry",
			Label:        "Install telemetry",
			Description:  "Send an anonymous version/update ping after changelog-detected updates",
			CurrentValue: boolStr(config.EnableInstallTelemetry),
			Values:       []string{"true", "false"},
		},
		{
			ID:           "default-project-trust",
			Label:        "Default project trust",
			Description:  "Fallback behavior when no extension or saved trust decision decides project trust",
			CurrentValue: defaultProjectTrustLabel(config.DefaultProjectTrust),
			Values:       trustLabels(),
		},
		{
			ID:           "double-escape-action",
			Label:        "Double-escape action",
			Description:  "Action when pressing Escape twice with empty editor",
			CurrentValue: config.DoubleEscapeAction,
			Values:       []string{"tree", "fork", "none"},
		},
		{
			ID:           "tree-filter-mode",
			Label:        "Tree filter mode",
			Description:  "Default filter when opening /tree",
			CurrentValue: config.TreeFilterMode,
			Values:       []string{"default", "no-tools", "user-only", "labeled-only", "all"},
		},
		{
			ID:           "warnings",
			Label:        "Warnings",
			Description:  "Enable or disable individual warnings",
			CurrentValue: "configure",
			Submenu: func(currentValue string, done func(tui.SubmenuResult)) tui.Component {
				return newWarningSettingsSubmenu(currentWarnings, func(warnings WarningSettings) {
					currentWarnings = warnings
					if callbacks.OnWarningsChange != nil {
						callbacks.OnWarningsChange(warnings)
					}
				}, func() { done(tui.SubmenuResult{}) })
			},
		},
		{
			ID:           "model-thinking",
			Label:        "Default thinking level per model",
			Description:  "Override the default thinking level for specific models. " + cycleThinkingKey + " cycles in-session.",
			CurrentValue: modelThinkingOverridesSummary(currentModelThinkingLevels),
			Submenu: func(currentValue string, done func(tui.SubmenuResult)) tui.Component {
				steps := []SteppedSubmenuStep{
					{
						Key:         "model",
						Title:       func(map[string]string) string { return "Per-Model Thinking Level" },
						Description: func(map[string]string) string { return "Select a model to configure" },
						Options: func(map[string]string) []tui.SelectItem {
							sorted := append([]*ai.Model{}, config.AvailableDefaultModels...)
							sortModelsForPicker(sorted, currentModelKey, currentDefaultModelKey)
							items := make([]tui.SelectItem, 0, len(sorted))
							for _, model := range sorted {
								key := modelSettingKey(model)
								item := tui.SelectItem{Value: key, Label: modelItemLabel(model)}
								if level, ok := currentModelThinkingLevels[key]; ok && level != "" {
									item.Description = level
								}
								items = append(items, item)
							}
							if len(items) == 0 {
								items = append(items, tui.SelectItem{
									Value:       "__none__",
									Label:       "No models available",
									Description: "Log in to a provider or configure an API key first",
								})
							}
							return items
						},
						Preselect: func(map[string]string) (string, bool) {
							if currentModelKey != "" {
								return currentModelKey, true
							}
							if currentDefaultModelKey != "" {
								return currentDefaultModelKey, true
							}
							return "", false
						},
						Searchable: true,
						Layout:     &modelPickerLayout,
						HasLayout:  true,
					},
					{
						Key: "level",
						Title: func(ctx map[string]string) string {
							if model, ok := defaultModelByValue[ctx["model"]]; ok {
								return "Thinking Level for " + modelDisplayLabel(model)
							}
							return "Thinking Level for " + ctx["model"]
						},
						Description: func(map[string]string) string { return "Select default thinking level for this model" },
						Options: func(ctx map[string]string) []tui.SelectItem {
							model, ok := defaultModelByValue[ctx["model"]]
							if !ok {
								return nil
							}
							var levels []string
							if model.Reasoning {
								levels = ai.GetSupportedThinkingLevels(model)
							} else {
								levels = []string{"off"}
							}
							activeLevel := currentModelThinkingLevels[ctx["model"]]
							items := make([]tui.SelectItem, 0, len(levels)+1)
							for _, level := range levels {
								prefix := "  "
								if level == activeLevel {
									prefix = "✓ "
								}
								items = append(items, tui.SelectItem{
									Value:       level,
									Label:       prefix + level,
									Description: thinkingDescriptions[level],
								})
							}
							if activeLevel != "" {
								items = append(items, tui.SelectItem{
									Value:       clearOverrideValue,
									Label:       "  (clear override)",
									Description: "Revert to global default (" + config.ThinkingLevel + ")",
								})
							}
							return items
						},
						Preselect: func(ctx map[string]string) (string, bool) {
							level, ok := currentModelThinkingLevels[ctx["model"]]
							return level, ok
						},
					},
				}

				return NewSteppedSubmenu(steps, func(selections map[string]string) {
					model, ok := defaultModelByValue[selections["model"]]
					if !ok {
						return
					}
					if selections["level"] == clearOverrideValue {
						if callbacks.OnModelThinkingLevelRemove != nil {
							callbacks.OnModelThinkingLevelRemove(model.Provider, model.ID)
						}
						delete(currentModelThinkingLevels, selections["model"])
						return
					}
					if callbacks.OnModelThinkingLevelChange != nil {
						callbacks.OnModelThinkingLevelChange(model.Provider, model.ID, selections["level"])
					}
					currentModelThinkingLevels[selections["model"]] = selections["level"]
				}, func() {
					done(tui.SubmenuResult{SelectedValue: modelThinkingOverridesSummary(currentModelThinkingLevels), HasSelectedValue: true})
				}, SteppedSubmenuOptions{Loop: true})
			},
		},
		{
			ID:           "tui-mode",
			Label:        "TUI mode",
			Description:  "Interface layout; fullscreen mode is experimental",
			CurrentValue: config.TuiMode,
			Values:       []string{"regular", "fullscreen"},
		},
		{
			ID:           "fullscreen-exit-output",
			Label:        "Fullscreen exit output",
			Description:  "Print the transcript or only a session resume hint when exiting fullscreen mode",
			CurrentValue: config.FullscreenExitOutput,
			Values:       []string{"transcript", "resume-hint"},
		},
		{
			ID:           "fullscreen-scrollbar",
			Label:        "Fullscreen scrollbar",
			Description:  "Scrollbar behavior in fullscreen mode; has no effect in regular mode",
			CurrentValue: config.FullscreenScrollbar,
			Values:       []string{"auto", "always", "hidden"},
		},
		{
			ID:           "fullscreen-copy-on-select",
			Label:        "Fullscreen copy on select",
			Description:  "Automatically copy selected text in fullscreen mode; disable to copy selections with Ctrl+X",
			CurrentValue: boolStr(config.FullscreenCopyOnSelect),
			Values:       []string{"true", "false"},
		},
		{
			ID:           "theme",
			Label:        "Theme",
			Description:  "Color theme for the interface",
			CurrentValue: config.CurrentTheme,
			Submenu: func(currentValue string, done func(tui.SubmenuResult)) tui.Component {
				return NewThemeSubmenu(currentValue, config.TerminalTheme, config.AvailableThemes, &callbacks, done)
			},
		},
	}

	// Image toggles depend on terminal capabilities; images are inert in the
	// Go port, so they are omitted (D26).
	insertAfter := func(id string, item tui.SettingItem) {
		for index, existing := range items {
			if existing.ID == id {
				items = append(items[:index+1], append([]tui.SettingItem{item}, items[index+1:]...)...)
				return
			}
		}
	}

	insertAfter("autocompact", tui.SettingItem{
		ID:           "auto-resize-images",
		Label:        "Auto-resize images",
		Description:  "Resize large images to 2000x2000 max for better model compatibility",
		CurrentValue: boolStr(config.AutoResizeImages),
		Values:       []string{"true", "false"},
	})
	insertAfter("auto-resize-images", tui.SettingItem{
		ID:           "block-images",
		Label:        "Block images",
		Description:  "Prevent images from being sent to LLM providers",
		CurrentValue: boolStr(config.BlockImages),
		Values:       []string{"true", "false"},
	})
	insertAfter("block-images", tui.SettingItem{
		ID:           "skill-commands",
		Label:        "Skill commands",
		Description:  "Register skills as /skill:name commands",
		CurrentValue: boolStr(config.EnableSkillCommands),
		Values:       []string{"true", "false"},
	})
	insertAfter("skill-commands", tui.SettingItem{
		ID:           "show-hardware-cursor",
		Label:        "Show hardware cursor",
		Description:  "Show the terminal cursor while still positioning it for IME support",
		CurrentValue: boolStr(config.ShowHardwareCursor),
		Values:       []string{"true", "false"},
	})
	insertAfter("show-hardware-cursor", tui.SettingItem{
		ID:           "editor-padding",
		Label:        "Editor padding",
		Description:  "Horizontal padding for input editor (0-3)",
		CurrentValue: itoa(config.EditorPaddingX),
		Values:       []string{"0", "1", "2", "3"},
	})
	insertAfter("editor-padding", tui.SettingItem{
		ID:           "output-padding",
		Label:        "Output padding",
		Description:  "Horizontal padding for user messages, assistant messages, and thinking",
		CurrentValue: itoa(config.OutputPad),
		Values:       []string{"0", "1"},
	})
	insertAfter("output-padding", tui.SettingItem{
		ID:           "autocomplete-max-visible",
		Label:        "Autocomplete max items",
		Description:  "Max visible items in autocomplete dropdown (3-20)",
		CurrentValue: itoa(config.AutocompleteMaxVisible),
		Values:       []string{"3", "5", "7", "10", "15", "20"},
	})
	insertAfter("autocomplete-max-visible", tui.SettingItem{
		ID:           "clear-on-shrink",
		Label:        "Clear on shrink",
		Description:  "Clear empty rows when content shrinks (may cause flicker)",
		CurrentValue: boolStr(config.ClearOnShrink),
		Values:       []string{"true", "false"},
	})
	insertAfter("clear-on-shrink", tui.SettingItem{
		ID:           "terminal-progress",
		Label:        "Terminal progress",
		Description:  "Show OSC 9;4 progress indicators in the terminal tab bar",
		CurrentValue: boolStr(config.ShowTerminalProgress),
		Values:       []string{"true", "false"},
	})

	component := &SettingsSelectorComponent{Container: &tui.Container{}}
	component.AddChild(NewDynamicBorder(nil))
	component.settingsList = tui.NewSettingsList(items, 10, GetSettingsListTheme(),
		func(id string, newValue string) { applySettingChange(id, newValue, callbacks, defaultModelByValue) },
		callbacks.OnCancel, tui.SettingsListOptions{EnableSearch: true})
	component.AddChild(component.settingsList)
	component.AddChild(NewDynamicBorder(nil))
	return component
}

// GetSettingsList returns the underlying list.
func (c *SettingsSelectorComponent) GetSettingsList() *tui.SettingsList { return c.settingsList }

func trustLabels() []string {
	labels := make([]string, 0, len(defaultProjectTrustLabels))
	for _, entry := range defaultProjectTrustLabels {
		labels = append(labels, entry.Label)
	}
	return labels
}

func sortModelsForPicker(models []*ai.Model, currentModelKey string, currentDefaultModelKey string) {
	rank := func(model *ai.Model) int {
		key := modelSettingKey(model)
		switch key {
		case currentModelKey:
			return 0
		case currentDefaultModelKey:
			return 1
		}
		return 2
	}
	for i := 1; i < len(models); i++ {
		for j := i; j > 0; j-- {
			left, right := models[j-1], models[j]
			leftRank, rightRank := rank(left), rank(right)
			if leftRank < rightRank || (leftRank == rightRank && left.Provider <= right.Provider) {
				break
			}
			models[j-1], models[j] = models[j], models[j-1]
		}
	}
}

func applySettingChange(id string, newValue string, callbacks SettingsCallbacks, defaultModelByValue map[string]*ai.Model) {
	boolValue := newValue == "true"
	call := func(fn func(bool)) {
		if fn != nil {
			fn(boolValue)
		}
	}
	intValue, _ := strconv.Atoi(newValue)
	switch id {
	case "autocompact":
		call(callbacks.OnAutoCompactChange)
	case "show-images":
		call(callbacks.OnShowImagesChange)
	case "image-width-cells":
		if callbacks.OnImageWidthCellsChange != nil {
			callbacks.OnImageWidthCellsChange(intValue)
		}
	case "auto-resize-images":
		call(callbacks.OnAutoResizeImagesChange)
	case "block-images":
		call(callbacks.OnBlockImagesChange)
	case "skill-commands":
		call(callbacks.OnEnableSkillCommandsChange)
	case "steering-mode":
		if callbacks.OnSteeringModeChange != nil {
			callbacks.OnSteeringModeChange(newValue)
		}
	case "follow-up-mode":
		if callbacks.OnFollowUpModeChange != nil {
			callbacks.OnFollowUpModeChange(newValue)
		}
	case "transport":
		if callbacks.OnTransportChange != nil {
			callbacks.OnTransportChange(newValue)
		}
	case "http-idle-timeout":
		if callbacks.OnHTTPIdleTimeoutMsChange != nil {
			for _, choice := range coding.HTTPIdleTimeoutChoices {
				if choice.Label == newValue {
					callbacks.OnHTTPIdleTimeoutMsChange(choice.TimeoutMS)
					break
				}
			}
		}
	case "cache-warming-mode":
		if callbacks.OnCacheWarmingModeChange != nil {
			callbacks.OnCacheWarmingModeChange(newValue)
		}
	case "hide-thinking":
		call(callbacks.OnHideThinkingBlockChange)
	case "mermaid-rendering":
		if callbacks.OnMermaidRenderingModeChange != nil {
			callbacks.OnMermaidRenderingModeChange(newValue)
		}
	case "cache-miss-notices":
		call(callbacks.OnShowCacheMissNoticesChange)
	case "collapse-changelog":
		call(callbacks.OnCollapseChangelogChange)
	case "quiet-startup":
		call(callbacks.OnQuietStartupChange)
	case "install-telemetry":
		call(callbacks.OnEnableInstallTelemetryChange)
	case "default-project-trust":
		if value, ok := defaultProjectTrustByLabel(newValue); ok && callbacks.OnDefaultProjectTrustChange != nil {
			callbacks.OnDefaultProjectTrustChange(value)
		}
	case "double-escape-action":
		if callbacks.OnDoubleEscapeActionChange != nil {
			callbacks.OnDoubleEscapeActionChange(newValue)
		}
	case "tree-filter-mode":
		if callbacks.OnTreeFilterModeChange != nil {
			callbacks.OnTreeFilterModeChange(newValue)
		}
	case "show-hardware-cursor":
		call(callbacks.OnShowHardwareCursorChange)
	case "editor-padding":
		if callbacks.OnEditorPaddingXChange != nil {
			callbacks.OnEditorPaddingXChange(intValue)
		}
	case "output-padding":
		if callbacks.OnOutputPadChange != nil {
			outputPad := 1
			if newValue == "0" {
				outputPad = 0
			}
			callbacks.OnOutputPadChange(outputPad)
		}
	case "autocomplete-max-visible":
		if callbacks.OnAutocompleteMaxVisibleChange != nil {
			callbacks.OnAutocompleteMaxVisibleChange(intValue)
		}
	case "clear-on-shrink":
		call(callbacks.OnClearOnShrinkChange)
	case "terminal-progress":
		call(callbacks.OnShowTerminalProgressChange)
	case "tui-mode":
		if callbacks.OnTuiModeChange != nil {
			callbacks.OnTuiModeChange(newValue)
		}
	case "fullscreen-exit-output":
		if callbacks.OnFullscreenExitOutputChange != nil {
			callbacks.OnFullscreenExitOutputChange(newValue)
		}
	case "fullscreen-scrollbar":
		if callbacks.OnFullscreenScrollbarChange != nil {
			callbacks.OnFullscreenScrollbarChange(newValue)
		}
	case "fullscreen-copy-on-select":
		call(callbacks.OnFullscreenCopyOnSelectChange)
	case "theme":
		if callbacks.OnThemeChange != nil {
			callbacks.OnThemeChange(newValue)
		}
	}
	_ = defaultModelByValue
}
