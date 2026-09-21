package interactive

import (
	"context"
	"errors"
	"sort"
	"strings"

	"github.com/dat267/pier/ai"
	"github.com/dat267/pier/coding"
	"github.com/dat267/pier/tui"
)

// Port of the login/logout wiring of
// src/modes/interactive/interactive-mode.ts (getLoginProviderOptions,
// getLogoutProviderOptions, findLoginProviderOptions, handleLoginCommand,
// startProviderLogin, showLoginAuthTypeSelector, showLoginProviderSelector,
// showOAuthSelector, showAuthSelect, showAuthPrompt, notifyAuthDialog,
// loginProvider, showLoginDialog, showApiKeyLoginDialog,
// completeProviderAuthentication, showAmbientAuthDialog).
//
// Divergences: the extension-selector dialog is a seam (D41) and the
// runtime/collaborators are injected (D120).

// AuthSession is the session surface the auth wiring needs.
type AuthSession interface {
	ModelRuntime() *coding.ModelRuntime
	Model() *ai.Model
	SetModel(ctx context.Context, model *ai.Model, options coding.ModelMutationOptions) error
}

// AuthWiring wires the login/logout flows.
type AuthWiring struct {
	Slot            *SelectorSlot
	EditorContainer *tui.Container
	Editor          tui.Component
	UI              tui.TUI
	Session         AuthSession
	Settings        *coding.SettingsManager

	// ShowAuthSelect shows the select prompt (extension UI seam; D41).
	ShowAuthSelect func(dialog *LoginDialogComponent, prompt ai.AuthPrompt) (string, error)
	// ShowStatus/ShowError/ShowWarning report messages.
	ShowStatus  func(message string)
	ShowError   func(message string)
	ShowWarning func(message string)
	// UpdateAvailableProviderCount refreshes the footer provider count.
	UpdateAvailableProviderCount func()
	// UpdateEditorBorderColor refreshes the editor border.
	UpdateEditorBorderColor func()
	// OnAuthenticated runs the post-login hooks (subscription warning, eggs).
	OnAuthenticated func(model *ai.Model, hasModel bool)
	// RequestRender requests a render.
	RequestRender func()
	// AuthPath is the auth.json path shown in the statuses.
	AuthPath string
	// DocsPath is the docs directory (amazon-bedrock details).
	DocsPath string
	// ScheduleTimer schedules the refresh timeout (test seam).
	ScheduleTimer func(ms int, fn func()) func()
	// OnPromptShown runs after a dialog prompt is created (test seam).
	OnPromptShown func()
}

func (w *AuthWiring) showStatus(message string) {
	if w.ShowStatus != nil {
		w.ShowStatus(message)
	}
}

func (w *AuthWiring) showError(message string) {
	if w.ShowError != nil {
		w.ShowError(message)
	}
}

func (w *AuthWiring) showWarning(message string) {
	if w.ShowWarning != nil {
		w.ShowWarning(message)
	}
}

func (w *AuthWiring) requestRender() {
	if w.RequestRender != nil {
		w.RequestRender()
	} else if w.UI != nil {
		w.UI.RequestRender(false)
	}
}

func (w *AuthWiring) restoreEditor() {
	if w.EditorContainer == nil {
		return
	}
	w.EditorContainer.Clear()
	if w.Editor != nil {
		w.EditorContainer.AddChild(w.Editor)
	}
	if w.UI != nil && w.Editor != nil {
		w.UI.SetFocus(w.Editor)
	}
	w.requestRender()
}

// GetLoginProviderOptions lists the login providers.
func (w *AuthWiring) GetLoginProviderOptions(authType string) []AuthSelectorProvider {
	var options []AuthSelectorProvider
	runtime := w.Session.ModelRuntime()
	for _, provider := range runtime.GetProviders() {
		authStatus := runtime.GetProviderAuthStatus(provider.ID)
		var status *ai.AuthCheck
		if authStatus.Configured {
			checkType := "api_key"
			if runtime.IsUsingOAuth(provider.ID) {
				checkType = "oauth"
			}
			source := authStatus.Source
			if authStatus.Label != "" {
				source = authStatus.Label
			}
			status = &ai.AuthCheck{Type: checkType, Source: source}
		}
		if (authType == "" || authType == "oauth") && provider.Auth.OAuth != nil {
			options = append(options, AuthSelectorProvider{
				ID: provider.ID, Name: provider.Name, AuthType: "oauth",
				Method: provider.Auth.OAuth, Status: status,
			})
		}
		if (authType == "" || authType == "api_key") && provider.Auth.APIKey != nil {
			options = append(options, AuthSelectorProvider{
				ID: provider.ID, Name: provider.Name, AuthType: "api_key",
				Method: provider.Auth.APIKey, Status: status,
			})
		}
	}
	sort.SliceStable(options, func(i int, j int) bool { return options[i].Name < options[j].Name })
	return options
}

// GetLogoutProviderOptions lists the stored credentials.
func (w *AuthWiring) GetLogoutProviderOptions(ctx context.Context) ([]AuthSelectorProvider, error) {
	credentials, err := w.Session.ModelRuntime().ListCredentials(ctx)
	if err != nil {
		return nil, err
	}
	runtime := w.Session.ModelRuntime()
	options := make([]AuthSelectorProvider, 0, len(credentials))
	for _, credential := range credentials {
		name := credential.ProviderID
		if provider := runtime.GetProvider(credential.ProviderID); provider != nil {
			name = provider.Name
		}
		options = append(options, AuthSelectorProvider{
			ID: credential.ProviderID, Name: name, AuthType: credential.Type,
			Status: &ai.AuthCheck{Type: credential.Type, Source: "stored credential"},
		})
	}
	sort.SliceStable(options, func(i int, j int) bool { return options[i].Name < options[j].Name })
	return options, nil
}

// FindLoginProviderOptions finds the providers matching a reference.
func (w *AuthWiring) FindLoginProviderOptions(providerRef string) []AuthSelectorProvider {
	normalized := strings.ToLower(strings.TrimSpace(providerRef))
	if normalized == "" {
		return nil
	}
	var matches []AuthSelectorProvider
	for _, provider := range w.GetLoginProviderOptions("") {
		if strings.ToLower(provider.ID) == normalized || strings.ToLower(provider.Name) == normalized {
			matches = append(matches, provider)
		}
	}
	return matches
}

// HandleLoginCommand handles `/login [provider]`.
func (w *AuthWiring) HandleLoginCommand(ctx context.Context, providerRef string) {
	if strings.TrimSpace(providerRef) == "" {
		w.ShowLoginAuthTypeSelector(ctx, nil)
		return
	}
	providerOptions := w.FindLoginProviderOptions(providerRef)
	if len(providerOptions) == 1 {
		w.StartProviderLogin(ctx, providerOptions[0])
		return
	}
	if len(providerOptions) > 1 {
		ids := map[string]bool{}
		for _, provider := range providerOptions {
			ids[provider.ID] = true
		}
		if len(ids) == 1 {
			w.ShowLoginAuthTypeSelector(ctx, providerOptions)
			return
		}
	}
	w.ShowLoginProviderSelector(ctx, "", providerRef)
}

// StartProviderLogin starts the provider's login flow.
func (w *AuthWiring) StartProviderLogin(ctx context.Context, providerOption AuthSelectorProvider) {
	switch {
	case providerOption.AuthType == "oauth":
		w.ShowLoginDialog(ctx, providerOption.ID, providerOption.Name)
	case hasInteractiveLogin(providerOption.Method):
		w.ShowApiKeyLoginDialog(ctx, providerOption.ID, providerOption.Name)
	default:
		w.ShowAmbientAuthDialog(providerOption)
	}
}

func hasInteractiveLogin(method any) bool {
	switch typed := method.(type) {
	case *ai.ApiKeyAuth:
		return typed != nil && typed.Login != nil
	case ai.ApiKeyAuth:
		return typed.Login != nil
	}
	return false
}

// ShowLoginAuthTypeSelector shows the auth-type picker.
func (w *AuthWiring) ShowLoginAuthTypeSelector(ctx context.Context, providerOptions []AuthSelectorProvider) {
	oauthLabel := "Sign in with an account"
	for _, provider := range providerOptions {
		if provider.AuthType != "oauth" {
			continue
		}
		if oauth, ok := provider.Method.(*ai.OAuthAuth); ok && oauth != nil && oauth.LoginLabel != "" {
			oauthLabel = oauth.LoginLabel
		}
	}
	apiKeyLabel := "Sign in with an API key"

	available := map[string]bool{}
	if providerOptions != nil {
		for _, provider := range providerOptions {
			available[provider.AuthType] = true
		}
	} else {
		available["oauth"] = true
		available["api_key"] = true
	}
	var options []string
	if available["oauth"] {
		options = append(options, oauthLabel)
	}
	if available["api_key"] {
		options = append(options, apiKeyLabel)
	}
	if len(options) == 0 {
		w.showStatus("No login methods available.")
		return
	}
	if providerOptions != nil && len(options) == 1 {
		w.StartProviderLogin(ctx, providerOptions[0])
		return
	}

	title := "Select authentication method:"
	if len(providerOptions) > 0 {
		title = "Select authentication method for " + providerOptions[0].Name + ":"
	}
	w.Slot.Show(func(done func()) CreatedSelector {
		selector := NewExtensionSelectorComponent(title, options,
			func(option string) {
				done()
				authType := "api_key"
				if option == oauthLabel {
					authType = "oauth"
				}
				if providerOptions != nil {
					for _, provider := range providerOptions {
						if provider.AuthType == authType {
							w.StartProviderLogin(ctx, provider)
							return
						}
					}
					return
				}
				w.ShowLoginProviderSelector(ctx, authType, "")
			},
			func() {
				done()
				w.requestRender()
			})
		return CreatedSelector{Component: selector, Focus: selector}
	})
}

// ShowLoginProviderSelector shows the login provider list.
func (w *AuthWiring) ShowLoginProviderSelector(ctx context.Context, authType string, initialSearchInput string) {
	providerOptions := w.GetLoginProviderOptions(authType)
	if len(providerOptions) == 0 {
		message := "No login providers available."
		switch authType {
		case "oauth":
			message = "No subscription providers available."
		case "api_key":
			message = "No API key providers available."
		}
		w.showStatus(message)
		return
	}
	w.Slot.Show(func(done func()) CreatedSelector {
		selector := NewOAuthSelectorComponent("login", providerOptions,
			func(providerID string, selectedAuthType string) {
				done()
				for _, provider := range providerOptions {
					if provider.ID == providerID && provider.AuthType == selectedAuthType {
						w.StartProviderLogin(ctx, provider)
						return
					}
				}
			},
			func() {
				done()
				if authType != "" {
					w.ShowLoginAuthTypeSelector(ctx, nil)
				} else {
					w.requestRender()
				}
			},
			initialSearchInput)
		return CreatedSelector{Component: selector, Focus: selector}
	})
}

// ShowOAuthSelector shows the login/logout selector.
func (w *AuthWiring) ShowOAuthSelector(ctx context.Context, mode string) {
	if mode == "login" {
		w.ShowLoginAuthTypeSelector(ctx, nil)
		return
	}
	providerOptions, err := w.GetLogoutProviderOptions(ctx)
	if err != nil {
		w.showError("Could not read stored credentials: " + err.Error())
		return
	}
	if len(providerOptions) == 0 {
		w.showStatus("No stored credentials to remove. /logout only removes credentials saved by /login; environment variables and models.json config are unchanged.")
		return
	}
	w.Slot.Show(func(done func()) CreatedSelector {
		selector := NewOAuthSelectorComponent(mode, providerOptions,
			func(providerID string, _ string) {
				done()
				var providerOption *AuthSelectorProvider
				for index := range providerOptions {
					if providerOptions[index].ID == providerID {
						providerOption = &providerOptions[index]
						break
					}
				}
				if providerOption == nil {
					return
				}
				if err := w.Session.ModelRuntime().Logout(providerOption.ID, ctx); err != nil {
					message := err.Error()
					var syncErr *coding.CredentialSynchronizationError
					if errors.As(err, &syncErr) {
						w.showError("Credentials removed for " + providerOption.Name +
							", but local model state could not be synchronized: " + message)
					} else {
						w.showError("Logout failed: " + message)
					}
					return
				}
				if w.UpdateAvailableProviderCount != nil {
					w.UpdateAvailableProviderCount()
				}
				if providerOption.AuthType == "oauth" {
					w.showStatus("Logged out of " + providerOption.Name)
				} else {
					w.showStatus("Removed stored API key for " + providerOption.Name +
						". Environment variables and models.json config are unchanged.")
				}
			},
			func() {
				done()
				w.requestRender()
			},
			"")
		return CreatedSelector{Component: selector, Focus: selector}
	})
}

// ShowAuthPrompt maps an auth prompt onto the dialog.
func (w *AuthWiring) ShowAuthPrompt(dialog *LoginDialogComponent, prompt ai.AuthPrompt) (string, error) {
	var response <-chan LoginInputResult
	switch prompt.Type {
	case ai.AuthPromptSelect:
		if w.ShowAuthSelect != nil {
			return w.ShowAuthSelect(dialog, prompt)
		}
		return "", errors.New("Login cancelled")
	case ai.AuthPromptManualCode:
		response = dialog.ShowManualInput(prompt.Message)
	default:
		response = dialog.ShowPrompt(prompt.Message, prompt.Placeholder)
	}
	if w.OnPromptShown != nil {
		w.OnPromptShown()
	}
	select {
	case result := <-response:
		if result.Err != nil {
			return "", errors.New("Login cancelled")
		}
		return result.Value, nil
	case <-dialog.Aborted():
		return "", errors.New("Login cancelled")
	}
}

// NotifyAuthDialog maps an auth event onto the dialog.
func (w *AuthWiring) NotifyAuthDialog(dialog *LoginDialogComponent, event ai.AuthEvent) {
	switch event.Type {
	case ai.AuthEventAuthURL:
		dialog.ShowAuth(event.URL, event.Instructions)
	case ai.AuthEventDeviceCode:
		dialog.ShowDeviceCode(event.VerificationURI, event.UserCode)
		dialog.ShowWaiting("Waiting for authentication...")
	case ai.AuthEventInfo:
		dialog.ShowInfo(event.Message, event.Links, false)
	default:
		dialog.ShowProgress(event.Message)
	}
}

// LoginProvider runs the runtime login for a dialog.
func (w *AuthWiring) LoginProvider(ctx context.Context, dialog *LoginDialogComponent, providerID string, method string) error {
	interaction := &ai.AuthInteraction{
		Ctx:    ctx,
		Prompt: func(prompt ai.AuthPrompt) (string, error) { return w.ShowAuthPrompt(dialog, prompt) },
		Notify: func(event ai.AuthEvent) { w.NotifyAuthDialog(dialog, event) },
	}
	_, err := w.Session.ModelRuntime().Login(providerID, method, interaction)
	return err
}

// ShowLoginDialog runs an OAuth login.
func (w *AuthWiring) ShowLoginDialog(ctx context.Context, providerID string, providerName string) {
	previousModel := w.Session.Model()
	dialog := NewLoginDialogComponent(w.UI, providerID, func(bool, string) {}, providerName, "")
	w.showDialog(dialog)
	err := w.LoginProvider(ctx, dialog, providerID, "oauth")
	w.restoreEditor()
	if err != nil {
		w.reportLoginError(err, providerName, "Logged in to", "Failed to login to")
		return
	}
	w.CompleteProviderAuthentication(ctx, providerID, providerName, "oauth", previousModel)
}

// ShowApiKeyLoginDialog runs an API-key login.
func (w *AuthWiring) ShowApiKeyLoginDialog(ctx context.Context, providerID string, providerName string) {
	previousModel := w.Session.Model()
	dialog := NewLoginDialogComponent(w.UI, providerID, func(bool, string) {}, providerName, "")
	if providerID == "amazon-bedrock" {
		theme := ActiveTheme()
		docsPath := "providers.md"
		if w.DocsPath != "" {
			docsPath = w.DocsPath + "/providers.md"
		}
		dialog.ShowDetails([]string{
			theme.Fg("text", "You can also use an AWS profile, IAM keys, or role-based credentials."),
			theme.Fg("muted", "See:"),
			theme.Fg("accent", "  "+docsPath),
		})
	}
	w.showDialog(dialog)
	err := w.LoginProvider(ctx, dialog, providerID, "api_key")
	w.restoreEditor()
	if err != nil {
		w.reportLoginError(err, providerName, "Saved API key for", "Failed to save API key for")
		return
	}
	w.CompleteProviderAuthentication(ctx, providerID, providerName, "api_key", previousModel)
}

func (w *AuthWiring) reportLoginError(err error, providerName string, syncPrefix string, failurePrefix string) {
	var syncErr *coding.CredentialSynchronizationError
	if errors.As(err, &syncErr) {
		w.showError(syncPrefix + " " + providerName + ", but local model state could not be synchronized: " + err.Error())
		return
	}
	if err.Error() != "Login cancelled" {
		w.showError(failurePrefix + " " + providerName + ": " + err.Error())
	}
}

func (w *AuthWiring) showDialog(dialog *LoginDialogComponent) {
	if w.EditorContainer == nil {
		return
	}
	w.EditorContainer.Clear()
	w.EditorContainer.AddChild(dialog)
	if w.UI != nil {
		w.UI.SetFocus(dialog)
	}
	w.requestRender()
}

// ShowAmbientAuthDialog shows the ambient-auth info dialog.
func (w *AuthWiring) ShowAmbientAuthDialog(providerOption AuthSelectorProvider) {
	dialog := NewLoginDialogComponent(w.UI, providerOption.ID,
		func(bool, string) { w.restoreEditor() }, providerOption.Name, providerOption.Name+" setup")
	methodName := "Authentication"
	switch typed := providerOption.Method.(type) {
	case *ai.ApiKeyAuth:
		if typed != nil && typed.Name != "" {
			methodName = typed.Name
		}
	}
	dialog.ShowInfo(methodName+" is configured outside "+coding.AppName+".", nil, true)
	w.showDialog(dialog)
}

// CompleteProviderAuthentication finishes a login with model selection.
func (w *AuthWiring) CompleteProviderAuthentication(ctx context.Context, providerID string, providerName string, authType string, previousModel *ai.Model) {
	actionLabel := "Saved API key for " + providerName
	if authType == "oauth" {
		actionLabel = "Logged in to " + providerName
	}
	session := w.Session
	runtime := session.ModelRuntime()

	deferSelection := IsUnknownModel(previousModel) && HasDefaultModelProvider(providerID) &&
		!hasDefaultModelInSnapshot(runtime.GetAvailableSnapshot(), providerID)

	finish := func() {
		var selectedModel *ai.Model
		selectionError := ""
		if IsUnknownModel(previousModel) {
			providerModels := modelsForProvider(runtime.GetAvailableSnapshot(), providerID)
			switch {
			case providerID == "llama.cpp":
				selectionError = LlamaCppPostLoginGuidance(actionLabel, len(providerModels))
			case !HasDefaultModelProvider(providerID):
				selectionError = actionLabel + ", but no default model is configured for provider \"" + providerID +
					"\". Use /model to select a model."
			case len(providerModels) == 0:
				selectionError = actionLabel + ", but no models are available for that provider. Use /model to select a model."
			default:
				defaultModelID := coding.DefaultModelPerProvider[providerID]
				for _, model := range providerModels {
					if model.ID == defaultModelID {
						selectedModel = model
						break
					}
				}
				if selectedModel == nil && providerID == "radius" {
					selectedModel = providerModels[0]
				}
				if selectedModel == nil {
					selectionError = actionLabel + ", but its default model \"" + defaultModelID +
						"\" is not available. Use /model to select a model."
				} else if err := session.SetModel(ctx, selectedModel, coding.ModelMutationOptions{Persist: true}); err != nil {
					selectedModel = nil
					selectionError = actionLabel + ", but selecting its default model failed: " + err.Error() +
						". Use /model to select a model."
				}
			}
		}

		if w.UpdateAvailableProviderCount != nil {
			w.UpdateAvailableProviderCount()
		}
		if w.UpdateEditorBorderColor != nil {
			w.UpdateEditorBorderColor()
		}
		if selectedModel != nil {
			w.showStatus(actionLabel + ". Selected " + selectedModel.ID + ". Credentials saved to " + w.AuthPath)
			if w.OnAuthenticated != nil {
				w.OnAuthenticated(selectedModel, true)
			}
			return
		}
		w.showStatus(actionLabel + ". Credentials saved to " + w.AuthPath)
		if selectionError != "" {
			w.showError(selectionError)
		} else if w.OnAuthenticated != nil {
			w.OnAuthenticated(nil, false)
		}
	}

	if deferSelection {
		w.showStatus(actionLabel + ". Credentials saved to " + w.AuthPath + ". Refreshing model catalog…")
	} else {
		finish()
	}

	refreshCtx, cancel := context.WithCancel(ctx)
	var cancelTimer func()
	if w.ScheduleTimer != nil {
		cancelTimer = w.ScheduleTimer(15000, cancel)
	} else {
		cancelTimer = func() {}
	}
	go func() {
		defer cancel()
		result, err := runtime.Refresh(refreshCtx, &coding.ModelsRefreshCallOptions{Providers: []string{providerID}})
		cancelTimer()
		if err != nil {
			w.showWarning(actionLabel + ", but its model catalog could not be refreshed: " + err.Error())
			return
		}
		if result.Aborted {
			w.showWarning(actionLabel + ", but its model catalog refresh timed out; using cached models.")
		} else if len(result.Errors) > 0 {
			w.showWarning(actionLabel + ", but its model catalog could not be refreshed; using cached models.")
		}
		if deferSelection && w.Session == session && w.Session.Model() == previousModel {
			finish()
		}
		if w.UpdateAvailableProviderCount != nil {
			w.UpdateAvailableProviderCount()
		}
		w.requestRender()
	}()
}

func hasDefaultModelInSnapshot(models []*ai.Model, providerID string) bool {
	defaultModelID, ok := coding.DefaultModelPerProvider[providerID]
	if !ok {
		return false
	}
	for _, model := range models {
		if model.Provider == providerID && model.ID == defaultModelID {
			return true
		}
	}
	return false
}

func modelsForProvider(models []*ai.Model, providerID string) []*ai.Model {
	var result []*ai.Model
	for _, model := range models {
		if model.Provider == providerID {
			result = append(result, model)
		}
	}
	return result
}
