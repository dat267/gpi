package interactive

import (
	"context"
	"strings"

	"github.com/dat267/pier/ai"
	"github.com/dat267/pier/coding"
	"github.com/dat267/pier/tui"
)

// Port of the model/session selector wiring of
// src/modes/interactive/interactive-mode.ts (showModelSelector,
// showModelsSelector, showSessionSelector, handleResumeSession).
//
// Divergences: the runtime/session-switch collaborators are injected function
// values (D118); the refresh timeout is driven by a scheduler seam.

// ModelSession is the session surface the model selectors need.
type ModelSession interface {
	Model() *ai.Model
	ModelRuntime() ModelSelectorRuntime
	ScopedModels() []coding.ScopedModel
	SetModel(ctx context.Context, model *ai.Model, options coding.ModelMutationOptions) error
	SetScopedModels(models []coding.ScopedModel)
}

// ModelWiring builds and shows the model selectors.
type ModelWiring struct {
	Slot     *SelectorSlot
	Settings *coding.SettingsManager
	Session  ModelSession
	UI       tui.TUI

	// UpdateAvailableProviderCount refreshes the footer provider count.
	UpdateAvailableProviderCount func()
	// UpdateEditorBorderColor refreshes the editor border.
	UpdateEditorBorderColor func()
	// ShowStatus reports a status line.
	ShowStatus func(message string)
	// ShowError reports an error.
	ShowError func(message string)
	// OnModelSelected runs the post-selection hooks (subscription warning,
	// easter eggs).
	OnModelSelected func(model *ai.Model)
	// RequestRender requests a render.
	RequestRender func()
	// ScheduleTimer schedules the refresh timeout (test seam).
	ScheduleTimer func(ms int, fn func()) func()
}

func (w *ModelWiring) showStatus(message string) {
	if w.ShowStatus != nil {
		w.ShowStatus(message)
	}
}

func (w *ModelWiring) showError(message string) {
	if w.ShowError != nil {
		w.ShowError(message)
	}
}

func (w *ModelWiring) requestRender() {
	if w.RequestRender != nil {
		w.RequestRender()
	} else if w.UI != nil {
		w.UI.RequestRender(false)
	}
}

// ShowModelSelector opens the single-model selector.
func (w *ModelWiring) ShowModelSelector(ctx context.Context, initialSearchInput string) {
	w.Slot.Show(func(done func()) CreatedSelector {
		selectModel := func(model *ai.Model, persist bool) {
			if err := w.Session.SetModel(ctx, model, coding.ModelMutationOptions{Persist: persist}); err != nil {
				done()
				w.showError(err.Error())
				return
			}
			if w.UpdateAvailableProviderCount != nil {
				w.UpdateAvailableProviderCount()
			}
			if w.UpdateEditorBorderColor != nil {
				w.UpdateEditorBorderColor()
			}
			done()
			if persist {
				w.showStatus("Default model: " + model.Provider + "/" + model.ID)
			} else {
				w.showStatus("Model: " + model.ID)
			}
			if w.OnModelSelected != nil {
				w.OnModelSelected(model)
			}
		}
		var defaultModel *DefaultModelReference
		if w.Settings != nil {
			provider := w.Settings.GetDefaultProvider()
			modelID := w.Settings.GetDefaultModel()
			if provider != nil && modelID != nil {
				defaultModel = &DefaultModelReference{Provider: *provider, ID: *modelID}
			}
		}
		runtime := w.Session.ModelRuntime()
		scopedItems := make([]ScopedModelItem, 0, len(w.Session.ScopedModels()))
		for _, scoped := range w.Session.ScopedModels() {
			scopedItems = append(scopedItems, ScopedModelItem{Model: scoped.Model, ThinkingLevel: string(scoped.ThinkingLevel)})
		}
		selector := NewModelSelectorComponent(w.UI, w.UI.Post, w.Session.Model(), runtime, scopedItems,
			func(model *ai.Model) { selectModel(model, false) },
			func() {
				done()
				w.requestRender()
			},
			initialSearchInput,
			func(model *ai.Model) { selectModel(model, true) },
			defaultModel)
		return CreatedSelector{
			Component: selector, Focus: selector,
			Dispose: func() { selector.Dispose() },
		}
	})
}

// EnabledIdsFromPatterns resolves the configured model patterns.
func EnabledIdsFromPatterns(patterns []string, models []*ai.Model) *EnabledIds {
	if len(patterns) == 0 {
		return nil
	}
	resolved := coding.ResolveModelScopeFromModels(patterns, models)
	ids := make([]string, 0, len(resolved.ScopedModels))
	for _, scoped := range resolved.ScopedModels {
		ids = append(ids, scoped.Model.Provider+"/"+scoped.Model.ID)
	}
	for _, diagnostic := range resolved.Diagnostics {
		if diagnostic.Code == "no-match" && !containsID(ids, diagnostic.Pattern) {
			ids = append(ids, diagnostic.Pattern)
		}
	}
	return &EnabledIds{IDs: ids}
}

func sortStringsAscending(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j-1] > values[j]; j-- {
			values[j-1], values[j] = values[j], values[j-1]
		}
	}
}

func containsID(ids []string, id string) bool {
	for _, candidate := range ids {
		if candidate == id {
			return true
		}
	}
	return false
}

// ShowModelsSelector opens the scoped-models selector with a catalog refresh.
func (w *ModelWiring) ShowModelsSelector(ctx context.Context) {
	availableModels := w.Session.ModelRuntime().GetAvailableSnapshot()
	availableModelIDs := map[string]bool{}
	for _, model := range availableModels {
		availableModelIDs[model.Provider+"/"+model.ID] = true
	}
	var configuredPatterns []string
	if w.Settings != nil {
		configuredPatterns = w.Settings.GetEnabledModels()
	}
	sessionScopedModels := w.Session.ScopedModels()

	configuredEnabledIDs := func(models []*ai.Model) *EnabledIds {
		return EnabledIdsFromPatterns(configuredPatterns, models)
	}

	var currentEnabledIds *EnabledIds
	if len(sessionScopedModels) > 0 {
		ids := make([]string, 0, len(sessionScopedModels))
		for _, scoped := range sessionScopedModels {
			ids = append(ids, scoped.Model.Provider+"/"+scoped.Model.ID)
		}
		currentEnabledIds = &EnabledIds{IDs: ids}
	} else {
		currentEnabledIds = configuredEnabledIDs(availableModels)
	}
	selectionChanged := false

	updateSessionModels := func(enabledIds *EnabledIds) {
		if enabledIds == nil {
			currentEnabledIds = nil
		} else {
			copied := EnabledIds{All: enabledIds.All, IDs: append([]string{}, enabledIds.IDs...)}
			currentEnabledIds = &copied
		}
		hasEnabledAvailable := false
		allAvailableEnabled := enabledIds != nil
		if enabledIds != nil {
			for _, id := range enabledIds.IDs {
				if availableModelIDs[id] {
					hasEnabledAvailable = true
				}
			}
			for id := range availableModelIDs {
				if !containsID(enabledIds.IDs, id) {
					allAvailableEnabled = false
				}
			}
		}
		if enabledIds != nil && hasEnabledAvailable && !allAvailableEnabled {
			resolved := coding.ResolveModelScopeFromModels(enabledIds.IDs, availableModels)
			scoped := make([]coding.ScopedModel, 0, len(resolved.ScopedModels))
			for _, item := range resolved.ScopedModels {
				scoped = append(scoped, coding.ScopedModel{Model: item.Model, ThinkingLevel: item.ThinkingLevel})
			}
			w.Session.SetScopedModels(scoped)
		} else {
			w.Session.SetScopedModels(nil)
		}
		if w.UpdateAvailableProviderCount != nil {
			w.UpdateAvailableProviderCount()
		}
		w.requestRender()
	}

	w.Slot.Show(func(done func()) CreatedSelector {
		disposed := false
		timedOut := false
		refreshCtx, cancel := context.WithCancel(ctx)
		var cancelTimer func()
		if w.ScheduleTimer != nil {
			cancelTimer = w.ScheduleTimer(15000, func() {
				timedOut = true
				cancel()
			})
		}

		refreshStatus := "Refreshing model catalogs…"
		selector := NewScopedModelsSelectorComponent(ModelsConfig{
			AllModels: availableModels, EnabledModelIDs: currentEnabledIds, RefreshStatus: refreshStatus,
		}, ModelsCallbacks{
			OnChange: func(enabledIds EnabledIds) {
				selectionChanged = true
				copied := enabledIds
				updateSessionModels(&copied)
			},
			OnPersist: func(enabledIds EnabledIds) {
				allEnabled := len(enabledIds.IDs) == len(availableModels)
				if allEnabled {
					for _, id := range enabledIds.IDs {
						if !availableModelIDs[id] {
							allEnabled = false
							break
						}
					}
				}
				if allEnabled {
					w.Settings.SetEnabledModels(nil)
				} else {
					w.Settings.SetEnabledModels(append([]string{}, enabledIds.IDs...))
				}
				w.showStatus("Model selection saved to settings")
			},
			OnCancel: func() {
				done()
				w.requestRender()
			},
		})

		go func() {
			result, err := RefreshModelCatalogs(refreshCtx, w.Session.ModelRuntime())
			if disposed {
				return
			}
			snapshot := w.Session.ModelRuntime().GetAvailableSnapshot()
			// Mutations of the selector and the captured state are marshaled
			// onto the UI side: serialized with renders and input (D136 class).
			w.UI.Post(func() {
				if disposed {
					return
				}
				availableModels = snapshot
				availableModelIDs = map[string]bool{}
				for _, model := range snapshot {
					availableModelIDs[model.Provider+"/"+model.ID] = true
				}
				if !selectionChanged && len(sessionScopedModels) == 0 {
					currentEnabledIds = configuredEnabledIDs(availableModels)
					selector.UpdateModels(availableModels, currentEnabledIds)
				} else {
					selector.UpdateModels(availableModels, nil)
				}
				if currentEnabledIds != nil {
					updateSessionModels(currentEnabledIds)
				}
				switch {
				case err != nil:
					message := "Could not refresh model catalogs: " + err.Error()
					if timedOut {
						message = "Model refresh timed out; showing cached models."
					}
					selector.SetRefreshStatus(message, "warning")
				case result.Aborted && timedOut:
					selector.SetRefreshStatus("Model refresh timed out; showing cached models.", "warning")
				case len(result.Errors) > 0:
					providers := make([]string, 0, len(result.Errors))
					for provider := range result.Errors {
						providers = append(providers, provider)
					}
					sortStringsAscending(providers)
					selector.SetRefreshStatus("Could not refresh "+strings.Join(providers, ", ")+"; showing cached models.", "warning")
				default:
					selector.SetRefreshStatus("Model catalogs refreshed.", "success")
				}
				w.requestRender()
			})
		}()

		return CreatedSelector{
			Component: selector, Focus: selector,
			Dispose: func() {
				disposed = true
				if cancelTimer != nil {
					cancelTimer()
				}
				cancel()
			},
		}
	})
}

// SessionWiring builds and shows the session selector.
type SessionWiring struct {
	Slot        *SelectorSlot
	Settings    *coding.SettingsManager
	SessionInfo *coding.SessionManager
	UI          tui.TUI
	Keybindings *tui.KeybindingsManager

	// SwitchSession resumes a session (runtimeHost.switchSession).
	SwitchSession func(ctx context.Context, sessionPath string, cwdOverride string) (*SessionSwitchResult, error)
	// PromptForMissingCwd resolves a missing-cwd error.
	PromptForMissingCwd func(ctx context.Context, message string) (string, bool)
	// ShowStatus reports a status line.
	ShowStatus func(message string)
	// ShowError reports an error.
	ShowError func(message string)
	// Shutdown stops the mode.
	Shutdown func()
	// RequestRender requests a render.
	RequestRender func()
	// ClearStatusIndicator clears the active indicator.
	ClearStatusIndicator func()
}

// SessionSwitchResult is the runtime session-switch outcome.
type SessionSwitchResult struct {
	Cancelled bool
}

func (w *SessionWiring) showStatus(message string) {
	if w.ShowStatus != nil {
		w.ShowStatus(message)
	}
}

func (w *SessionWiring) showError(message string) {
	if w.ShowError != nil {
		w.ShowError(message)
	}
}

func (w *SessionWiring) requestRender() {
	if w.RequestRender != nil {
		w.RequestRender()
	} else if w.UI != nil {
		w.UI.RequestRender(false)
	}
}

// ShowSessionSelector opens the resume-session selector.
func (w *SessionWiring) ShowSessionSelector() {
	currentLoader := func(_ SessionListProgress, _ context.Context) ([]coding.SessionInfo, error) {
		return coding.ListSessions(w.SessionInfo.GetCwd(), w.SessionInfo.GetSessionDir()), nil
	}
	allLoader := func(_ SessionListProgress, _ context.Context) ([]coding.SessionInfo, error) {
		if w.SessionInfo.UsesDefaultSessionDir() {
			return coding.ListAllSessions(""), nil
		}
		return coding.ListAllSessions(w.SessionInfo.GetSessionDir()), nil
	}
	w.Slot.Show(func(done func()) CreatedSelector {
		selector := NewSessionSelectorComponent(currentLoader, allLoader,
			func(sessionPath string) {
				done()
				w.HandleResumeSession(context.Background(), sessionPath)
			},
			func() {
				done()
				w.requestRender()
			},
			func() {
				if w.Shutdown != nil {
					w.Shutdown()
				}
			},
			func() { w.requestRender() },
			SessionSelectorOptions{
				Post: w.Slot.UI.Post,
				RenameSession: func(sessionPath string, currentName string) error {
					next := strings.TrimSpace(currentName)
					if next == "" {
						return nil
					}
					manager, err := coding.OpenSession(sessionPath, "", "")
					if err != nil {
						return err
					}
					manager.AppendSessionInfo(next)
					return nil
				},
				ShowRenameHint:         true,
				Keybindings:            w.Keybindings,
				CurrentSessionFilePath: w.SessionInfo.GetSessionFile(),
			})
		return CreatedSelector{Component: selector, Focus: selector}
	})
}

// HandleResumeSession switches to another session.
func (w *SessionWiring) HandleResumeSession(ctx context.Context, sessionPath string) SessionSwitchResult {
	if w.ClearStatusIndicator != nil {
		w.ClearStatusIndicator()
	}
	if w.SwitchSession == nil {
		return SessionSwitchResult{}
	}
	result, err := w.SwitchSession(ctx, sessionPath, "")
	if err == nil {
		if result != nil && result.Cancelled {
			return *result
		}
		w.showStatus("Resumed session")
		if result == nil {
			return SessionSwitchResult{}
		}
		return *result
	}

	// A missing cwd can be resolved by prompting for one.
	if w.PromptForMissingCwd != nil && strings.Contains(err.Error(), "cwd") {
		selectedCwd, ok := w.PromptForMissingCwd(ctx, err.Error())
		if !ok {
			w.showStatus("Resume cancelled")
			return SessionSwitchResult{Cancelled: true}
		}
		result, err = w.SwitchSession(ctx, sessionPath, selectedCwd)
		if err == nil {
			if result != nil && result.Cancelled {
				return *result
			}
			w.showStatus("Resumed session in current cwd")
			if result == nil {
				return SessionSwitchResult{}
			}
			return *result
		}
	}
	w.showError("Failed to resume session: " + err.Error())
	return SessionSwitchResult{}
}
