package interactive

import (
	"github.com/dat267/pier/ai"
	"github.com/dat267/pier/coding"
	"github.com/dat267/pier/tui"
)

// Port of showSettingsSelector from
// src/modes/interactive/interactive-mode.ts: the settings config assembly and
// its change callbacks.
//
// Divergences: the collaborators (session, theme controller, renderer, chat,
// selector refresh) are injected as small interfaces/function values (D117).

// SettingsSession is the session surface the settings selector needs.
type SettingsSession interface {
	AutoCompactionEnabled() bool
	Model() *ai.Model
	GetAvailableModels() []*ai.Model
	SteeringMode() coding.QueueMode
	FollowUpMode() coding.QueueMode
	SetAutoCompactionEnabled(enabled bool)
	SetSteeringMode(mode coding.QueueMode)
	SetFollowUpMode(mode coding.QueueMode)
	SetCacheWarmingMode(mode coding.CacheWarmingMode)
	SetThinkingLevel(level ai.ThinkingLevel, options ...coding.ModelMutationOptions)
	IsStreaming() bool
}

// SettingsThemeController is the theme-controller surface.
type SettingsThemeController interface {
	GetThemeSelection() string
	GetTerminalTheme() TerminalTheme
	SetThemeSetting(theme string) error
	Preview(theme string)
}

// SettingsWiring builds and shows the settings selector.
type SettingsWiring struct {
	Slot            *SelectorSlot
	Settings        *coding.SettingsManager
	Session         SettingsSession
	ThemeController SettingsThemeController
	UI              tui.TUI

	// Chat is the transcript container (for the image/thinking updates).
	Chat *tui.Container
	// StreamingComponent is the in-progress assistant component.
	StreamingComponent *AssistantMessageComponent
	// DefaultEditor is the built-in editor (padding/autocomplete updates).
	DefaultEditor *CustomEditor
	// Editor is the active editor (may differ from DefaultEditor).
	Editor tui.Component
	// Renderer is the active renderer (fullscreen copy-on-select).
	Renderer tui.TUI

	// UpdateThinkingBlockVisibility re-renders the assistant messages.
	UpdateThinkingBlockVisibility func(hidden bool)
	// RebuildChatFromMessages re-renders the transcript.
	RebuildChatFromMessages func()
	// UpdateEditorBorderColor refreshes the editor border.
	UpdateEditorBorderColor func()
	// SetupAutocompleteProvider rebuilds the autocomplete provider.
	SetupAutocompleteProvider func()
	// SwitchTuiMode switches the renderer mode.
	SwitchTuiMode func(mode string) bool
	// ApplyFullscreenScrollbarSetting applies the scrollbar setting.
	ApplyFullscreenScrollbarSetting func()
	// ConfigureHTTPIdleTimeout applies the HTTP idle timeout.
	ConfigureHTTPIdleTimeout func(timeoutMS int64)
	// ShowStatus reports a status line.
	ShowStatus func(message string)
	// RequestRender requests a render.
	RequestRender func()
	// SetHideThinkingBlock tracks the hidden-thinking state.
	HideThinkingBlock *bool
	// OutputPad tracks the output padding.
	OutputPad *int
	// UpdateStatusContainer clears the status container when idle.
	ClearStatusContainerIfIdle func()
}

func (w *SettingsWiring) showStatus(message string) {
	if w.ShowStatus != nil {
		w.ShowStatus(message)
	}
}

func (w *SettingsWiring) requestRender() {
	if w.RequestRender != nil {
		w.RequestRender()
	} else if w.UI != nil {
		w.UI.RequestRender(false)
	}
}

// BuildSettingsConfig assembles the settings snapshot.
func (w *SettingsWiring) BuildSettingsConfig() SettingsConfig {
	settings := w.Settings
	session := w.Session
	defaultModel := "not set"
	if provider := settings.GetDefaultProvider(); provider != nil {
		if modelID := settings.GetDefaultModel(); modelID != nil {
			defaultModel = *provider + "/" + *modelID
		}
	}
	httpIdleTimeout, _ := settings.GetHTTPIdleTimeoutMS()
	config := SettingsConfig{
		AutoCompact:            session.AutoCompactionEnabled(),
		DefaultModel:           defaultModel,
		CurrentModel:           session.Model(),
		AvailableDefaultModels: session.GetAvailableModels(),
		ShowImages:             settings.GetShowImages(),
		ImageWidthCells:        settings.GetImageWidthCells(),
		AutoResizeImages:       settings.GetImageAutoResize(),
		BlockImages:            settings.GetBlockImages(),
		EnableSkillCommands:    settings.GetEnableSkillCommands(),
		SteeringMode:           string(session.SteeringMode()),
		FollowUpMode:           string(session.FollowUpMode()),
		Transport:              settings.GetTransport(),
		HTTPIdleTimeoutMs:      httpIdleTimeout,
		CacheWarmingMode:       settings.GetCacheWarmingMode(),
		ThinkingLevel:          coding.DefaultThinkingLevel,
		AvailableThinkingLevels: []string{
			"off", "minimal", "low", "medium", "high", "xhigh", "max",
		},
		ModelThinkingLevels:    settings.GetAllModelThinkingLevels(),
		CurrentTheme:           "dark",
		TerminalTheme:          TerminalThemeDark,
		AvailableThemes:        AvailableThemes(),
		HideThinkingBlock:      settings.GetHideThinkingBlock(),
		MermaidRenderingMode:   settings.GetMermaidRenderingMode(),
		CollapseChangelog:      settings.GetCollapseChangelog(),
		EnableInstallTelemetry: settings.GetEnableInstallTelemetry(),
		DoubleEscapeAction:     settings.GetDoubleEscapeAction(),
		TreeFilterMode:         settings.GetTreeFilterMode(),
		ShowHardwareCursor:     settings.GetShowHardwareCursor(),
		ShowCacheMissNotices:   settings.GetShowCacheMissNotices(),
		DefaultProjectTrust:    settings.GetDefaultProjectTrust(),
		EditorPaddingX:         settings.GetEditorPaddingX(),
		OutputPad:              settings.GetOutputPad(),
		AutocompleteMaxVisible: settings.GetAutocompleteMaxVisible(),
		QuietStartup:           settings.GetQuietStartup(),
		ClearOnShrink:          settings.GetClearOnShrink(),
		ShowTerminalProgress:   settings.GetShowTerminalProgress(),
		TuiMode:                "regular",
		FullscreenExitOutput:   settings.GetFullscreenExitOutput(),
		FullscreenScrollbar:    settings.GetFullscreenScrollbar(),
		FullscreenCopyOnSelect: settings.GetFullscreenCopyOnSelect(),
		Warnings:               warningSettingsFromCoding(settings.GetWarnings()),
	}
	if value := settings.GetDefaultThinkingLevel(); value != nil && *value != "" {
		config.ThinkingLevel = *value
	}
	if w.ThemeController != nil {
		if selection := w.ThemeController.GetThemeSelection(); selection != "" {
			config.CurrentTheme = selection
		}
		config.TerminalTheme = w.ThemeController.GetTerminalTheme()
	}
	if w.UI != nil {
		config.TuiMode = TuiMode(w.UI)
	}
	return config
}

// TuiMode reports the renderer mode ("" for the regular renderer).
func TuiMode(ui tui.TUI) string {
	if _, ok := tuiConcrete(ui).(*tui.AltScreen); ok {
		return "fullscreen"
	}
	return "regular"
}

// tuiConcrete unwraps the UI forwarding reference so type assertions see the
// active renderer (upstream reads this.renderer where the port holds app.UI).
func tuiConcrete(ui tui.TUI) tui.TUI {
	if reference, ok := ui.(*tui.TuiReference); ok {
		return reference.Current()
	}
	return ui
}

func warningSettingsFromCoding(warnings coding.SettingsWarnings) WarningSettings {
	return WarningSettings{AnthropicExtraUsage: warnings.AnthropicExtraUsage}
}

// BuildSettingsCallbacks assembles the change callbacks.
func (w *SettingsWiring) BuildSettingsCallbacks(done func(), refresh func()) SettingsCallbacks {
	settings := w.Settings
	session := w.Session
	callbacks := SettingsCallbacks{
		OnAutoCompactChange: func(enabled bool) {
			session.SetAutoCompactionEnabled(enabled)
		},
		OnShowImagesChange: func(enabled bool) {
			settings.SetShowImages(enabled)
			for _, child := range chatChildren(w.Chat) {
				if tool, ok := child.(*ToolExecutionComponent); ok {
					tool.SetShowImages(enabled)
				}
			}
		},
		OnImageWidthCellsChange: func(width int) {
			settings.SetImageWidthCells(width)
			for _, child := range chatChildren(w.Chat) {
				if tool, ok := child.(*ToolExecutionComponent); ok {
					tool.SetImageWidthCells(width)
				}
			}
		},
		OnAutoResizeImagesChange: func(enabled bool) { settings.SetImageAutoResize(enabled) },
		OnBlockImagesChange:      func(blocked bool) { settings.SetBlockImages(blocked) },
		OnEnableSkillCommandsChange: func(enabled bool) {
			settings.SetEnableSkillCommands(enabled)
			if w.SetupAutocompleteProvider != nil {
				w.SetupAutocompleteProvider()
			}
		},
		OnSteeringModeChange: func(mode string) { session.SetSteeringMode(mode) },
		OnFollowUpModeChange: func(mode string) { session.SetFollowUpMode(mode) },
		OnTransportChange: func(transport ai.Transport) {
			settings.SetTransport(transport)
		},
		OnHTTPIdleTimeoutMsChange: func(timeoutMS int64) {
			settings.SetHTTPIdleTimeoutMS(timeoutMS)
			if w.ConfigureHTTPIdleTimeout != nil {
				w.ConfigureHTTPIdleTimeout(timeoutMS)
			}
			w.showStatus("HTTP idle timeout: " + coding.FormatHTTPIdleTimeoutMS(timeoutMS))
		},
		OnCacheWarmingModeChange: func(mode string) {
			session.SetCacheWarmingMode(mode)
			w.showStatus("Cache warming: " + mode)
		},
		OnModelThinkingLevelChange: func(provider string, modelID string, level string) {
			settings.SetModelThinkingLevel(provider, modelID, level)
			if current := session.Model(); current != nil && current.Provider == provider && current.ID == modelID {
				session.SetThinkingLevel(level)
				if w.UpdateEditorBorderColor != nil {
					w.UpdateEditorBorderColor()
				}
			}
		},
		OnModelThinkingLevelRemove: func(provider string, modelID string) {
			settings.RemoveModelThinkingLevel(provider, modelID)
			if current := session.Model(); current != nil && current.Provider == provider && current.ID == modelID {
				level := string(coding.DefaultThinkingLevel)
				if value := settings.GetDefaultThinkingLevel(); value != nil && *value != "" {
					level = *value
				}
				session.SetThinkingLevel(level)
				if w.UpdateEditorBorderColor != nil {
					w.UpdateEditorBorderColor()
				}
			}
		},
		OnThemeChange: func(themeSetting string) {
			settings.SetTheme(themeSetting)
			if w.ThemeController != nil {
				_ = w.ThemeController.SetThemeSetting(themeSetting)
			}
		},
		OnThemePreview: func(themeName string) {
			if w.ThemeController != nil {
				w.ThemeController.Preview(themeName)
			}
		},
		OnHideThinkingBlockChange: func(hidden bool) {
			if w.HideThinkingBlock != nil {
				*w.HideThinkingBlock = hidden
			}
			settings.SetHideThinkingBlock(hidden)
			if w.UpdateThinkingBlockVisibility != nil {
				w.UpdateThinkingBlockVisibility(hidden)
			}
		},
		OnMermaidRenderingModeChange: func(mode string) {
			settings.SetMermaidRenderingMode(mode)
			if w.Chat != nil {
				w.Chat.Invalidate()
			}
			w.requestRender()
		},
		OnShowCacheMissNoticesChange: func(shown bool) {
			settings.SetShowCacheMissNotices(shown)
			if w.RebuildChatFromMessages != nil {
				w.RebuildChatFromMessages()
			}
		},
		OnCollapseChangelogChange:      func(collapsed bool) { settings.SetCollapseChangelog(collapsed) },
		OnEnableInstallTelemetryChange: func(enabled bool) { settings.SetEnableInstallTelemetry(enabled) },
		OnQuietStartupChange:           func(enabled bool) { settings.SetQuietStartup(enabled) },
		OnDefaultProjectTrustChange:    func(trust string) { settings.SetDefaultProjectTrust(trust) },
		OnDoubleEscapeActionChange:     func(action string) { settings.SetDoubleEscapeAction(action) },
		OnTreeFilterModeChange:         func(mode string) { settings.SetTreeFilterMode(mode) },
		OnShowHardwareCursorChange: func(enabled bool) {
			settings.SetShowHardwareCursor(enabled)
			if w.UI != nil {
				w.UI.SetShowHardwareCursor(enabled)
			}
		},
		OnEditorPaddingXChange: func(padding int) {
			settings.SetEditorPaddingX(padding)
			if w.DefaultEditor != nil {
				w.DefaultEditor.SetPaddingX(padding)
			}
			if w.Editor != nil && w.Editor != tui.Component(w.DefaultEditor) {
				if editor, ok := w.Editor.(interface{ SetPaddingX(int) }); ok {
					editor.SetPaddingX(padding)
				}
			}
		},
		OnOutputPadChange: func(padding int) {
			settings.SetOutputPad(padding)
			if w.OutputPad != nil {
				*w.OutputPad = padding
			}
			streaming := w.StreamingComponent != nil || session.IsStreaming()
			if streaming {
				for _, child := range chatChildren(w.Chat) {
					switch component := child.(type) {
					case *AssistantMessageComponent:
						component.SetOutputPad(padding)
					case *CustomMessageComponent:
						component.SetOutputPad(padding)
					case *UserMessageComponent:
						component.SetOutputPad(padding)
					}
				}
				if w.StreamingComponent != nil {
					w.StreamingComponent.SetOutputPad(padding)
				}
				w.requestRender()
				return
			}
			if w.RebuildChatFromMessages != nil {
				w.RebuildChatFromMessages()
			}
		},
		OnAutocompleteMaxVisibleChange: func(maxVisible int) {
			settings.SetAutocompleteMaxVisible(maxVisible)
			if w.DefaultEditor != nil {
				w.DefaultEditor.SetAutocompleteMaxVisible(maxVisible)
			}
			if w.Editor != nil && w.Editor != tui.Component(w.DefaultEditor) {
				if editor, ok := w.Editor.(interface{ SetAutocompleteMaxVisible(int) }); ok {
					editor.SetAutocompleteMaxVisible(maxVisible)
				}
			}
		},
		OnClearOnShrinkChange: func(enabled bool) {
			settings.SetClearOnShrink(enabled)
			if w.UI != nil {
				w.UI.SetClearOnShrink(enabled)
			}
			if !enabled {
				if w.ClearStatusContainerIfIdle != nil {
					w.ClearStatusContainerIfIdle()
				}
			}
		},
		OnShowTerminalProgressChange: func(enabled bool) { settings.SetShowTerminalProgress(enabled) },
		OnTuiModeChange: func(mode string) {
			if w.SwitchTuiMode != nil && !w.SwitchTuiMode(mode) {
				if refresh != nil {
					refresh()
				}
				w.showStatus("Close active overlays before changing TUI mode")
				return
			}
			settings.SetTuiMode(mode)
			if w.ClearStatusContainerIfIdle != nil {
				w.ClearStatusContainerIfIdle()
			}
			w.showStatus("TUI mode: " + mode)
		},
		OnFullscreenExitOutputChange: func(output string) { settings.SetFullscreenExitOutput(output) },
		OnFullscreenScrollbarChange: func(mode string) {
			settings.SetFullscreenScrollbar(mode)
			if w.ApplyFullscreenScrollbarSetting != nil {
				w.ApplyFullscreenScrollbarSetting()
			}
		},
		OnFullscreenCopyOnSelectChange: func(enabled bool) {
			settings.SetFullscreenCopyOnSelect(enabled)
			if renderer, ok := tuiConcrete(w.Renderer).(*tui.AltScreen); ok {
				renderer.SetCopyOnSelect(enabled)
			}
		},
		OnWarningsChange: func(warnings WarningSettings) {
			settings.SetWarnings(coding.SettingsWarnings{AnthropicExtraUsage: warnings.AnthropicExtraUsage})
		},
		OnCancel: func() {
			if done != nil {
				done()
			}
			if refresh != nil {
				refresh()
			}
			w.requestRender()
		},
	}
	return callbacks
}

// ShowSettingsSelector opens the settings selector.
func (w *SettingsWiring) ShowSettingsSelector() {
	w.Slot.Show(func(done func()) CreatedSelector {
		var selector *SettingsSelectorComponent
		callbacks := w.BuildSettingsCallbacks(done, func() {
			// The TUI-mode rejection resets the row to the renderer's mode.
			if selector == nil {
				return
			}
			mode := "regular"
			if w.UI != nil {
				mode = TuiMode(w.UI)
			}
			selector.GetSettingsList().UpdateValue("tui-mode", mode)
		})
		selector = NewSettingsSelectorComponent(w.BuildSettingsConfig(), callbacks)
		return CreatedSelector{Component: selector, Focus: selector.GetSettingsList()}
	})
}

func chatChildren(chat *tui.Container) []tui.Component {
	if chat == nil {
		return nil
	}
	return chat.Children
}
