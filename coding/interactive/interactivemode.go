package interactive

import (
	"os"
	"sort"
	"strings"

	"github.com/dat267/gpi/ai"
	"github.com/dat267/gpi/coding"
	"github.com/dat267/gpi/tui"
)

// Port of the helper layer of src/modes/interactive/interactive-mode.ts (the
// pure functions and small components above the InteractiveMode class).

// Expandable is implemented by components with an expanded/collapsed state.
type Expandable interface {
	SetExpanded(expanded bool)
}

// WorkingStatusEditor is the editor surface used for the embedded status.
type WorkingStatusEditor interface {
	tui.Component
	EmbedWorkingStatusValue() bool
	SetWorkingStatusIndicator(indicator *StatusIndicator)
}

// IsWorkingStatusEditor reports whether a component is a working-status editor.
func IsWorkingStatusEditor(component tui.Component) (WorkingStatusEditor, bool) {
	editor, ok := component.(WorkingStatusEditor)
	if !ok {
		return nil, false
	}
	return editor, editor.EmbedWorkingStatusValue()
}

// IsExpandable reports whether a component supports expansion.
func IsExpandable(value any) (Expandable, bool) {
	expandable, ok := value.(Expandable)
	return expandable, ok
}

// ExpandableText is a Text whose content depends on the expanded state.
type ExpandableText struct {
	*tui.Text

	getCollapsedText func() string
	getExpandedText  func() string
}

// NewExpandableText creates the component.
func NewExpandableText(getCollapsedText func() string, getExpandedText func() string, expanded bool, paddingX int, paddingY int) *ExpandableText {
	text := getCollapsedText()
	if expanded {
		text = getExpandedText()
	}
	return &ExpandableText{
		Text:             tui.NewText(text, paddingX, paddingY, nil),
		getCollapsedText: getCollapsedText,
		getExpandedText:  getExpandedText,
	}
}

// SetExpanded switches the text.
func (e *ExpandableText) SetExpanded(expanded bool) {
	if expanded {
		e.Text.SetText(e.getExpandedText())
		return
	}
	e.Text.SetText(e.getCollapsedText())
}

// CompactionQueuedMessage is a queued compaction/steering message.
type CompactionQueuedMessage struct {
	Text string
	Mode string // "steer" | "followUp"
}

// CompactionCostNotice is a compaction/branch-summary cost notice.
type CompactionCostNotice struct {
	Type  string // "compaction_cost"
	Kind  string // "compaction" | "branch_summary"
	Usage ai.Usage
}

// Dead terminal error codes.
var deadTerminalErrorCodes = map[string]bool{"EIO": true, "EPIPE": true, "ENOTCONN": true}

// IsDeadTerminalError reports whether an error is a dead-terminal error.
func IsDeadTerminalError(code string) bool { return deadTerminalErrorCodes[code] }

// AnthropicSubscriptionAuthWarning is shown for Anthropic subscription keys.
const AnthropicSubscriptionAuthWarning = "Anthropic subscription auth is active. Third-party harness usage draws from extra usage and is billed per token, not your Claude plan limits. Manage extra usage at https://claude.ai/settings/usage. Disable this warning in /settings."

// IsAnthropicSubscriptionAuthKey reports whether an API key is a Claude
// subscription OAuth token.
func IsAnthropicSubscriptionAuthKey(apiKey string) bool {
	return strings.HasPrefix(apiKey, "sk-ant-oat")
}

// IsUnknownModel reports whether a model is the unknown placeholder.
func IsUnknownModel(model *ai.Model) bool {
	return model != nil && model.Provider == "unknown" && model.ID == "unknown" && model.API == "unknown"
}

// QuoteIfNeeded shells-quotes a value when it contains unsafe characters.
func QuoteIfNeeded(value string) string {
	if len(value) > 0 && !strings.ContainsFunc(value, func(r rune) bool {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return false
		}
		return !strings.ContainsRune("_-./~:@", r)
	}) {
		return value
	}
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

// FormatResumeCommand builds the `pi --session …` resume command, or "" when
// the session cannot be resumed from the shell.
func FormatResumeCommand(manager *coding.SessionManager, stdoutIsTTY bool) string {
	if !stdoutIsTTY {
		return ""
	}
	if !manager.IsPersisted() {
		return ""
	}
	sessionFile := manager.GetSessionFile()
	if sessionFile == "" {
		return ""
	}
	if _, err := os.Stat(sessionFile); err != nil {
		return ""
	}
	args := []string{coding.AppName}
	if !manager.UsesDefaultSessionDir() {
		args = append(args, "--session-dir", QuoteIfNeeded(manager.GetSessionDir()))
	}
	args = append(args, "--session", manager.GetSessionID())
	return strings.Join(args, " ")
}

// HasDefaultModelProvider reports whether a provider has a default model.
func HasDefaultModelProvider(providerID string) bool {
	_, ok := coding.DefaultModelPerProvider[providerID]
	return ok
}

// LlamaCppPostLoginGuidance formats the post-login guidance for llama.cpp.
func LlamaCppPostLoginGuidance(actionLabel string, loadedModelCount int) string {
	if loadedModelCount == 0 {
		return actionLabel + ". No llama.cpp models are loaded. Use /llama to load a model, then /model to select it."
	}
	return actionLabel + ". Use /model to select a loaded llama.cpp model, or /llama to manage models."
}

// LoginProviderCompletionOption is a deduplicated provider completion entry.
type LoginProviderCompletionOption struct {
	ID        string
	Name      string
	AuthTypes []string
}

// AuthTypeOrder is the ordering of the auth-type labels.
var AuthTypeOrder = map[string]int{"oauth": 0, "api_key": 1}

// CreateFuzzyAutocompleteItems filters items and maps them to autocomplete
// entries, returning nil when nothing matches.
func CreateFuzzyAutocompleteItems[T any](items []T, prefix string, getSearchText func(T) string, toAutocompleteItem func(T) tui.AutocompleteItem) []tui.AutocompleteItem {
	filtered := tui.FuzzyFilter(items, prefix, getSearchText)
	if len(filtered) == 0 {
		return nil
	}
	result := make([]tui.AutocompleteItem, 0, len(filtered))
	for _, item := range filtered {
		result = append(result, toAutocompleteItem(item))
	}
	return result
}

// GetLoginProviderCompletionOptions deduplicates the provider options and
// merges their auth types.
func GetLoginProviderCompletionOptions(providerOptions []AuthSelectorProvider) []LoginProviderCompletionOption {
	byID := map[string]*LoginProviderCompletionOption{}
	var order []string
	for _, provider := range providerOptions {
		existing, ok := byID[provider.ID]
		if ok {
			if !configContains(existing.AuthTypes, provider.AuthType) {
				existing.AuthTypes = append(existing.AuthTypes, provider.AuthType)
				sort.SliceStable(existing.AuthTypes, func(i int, j int) bool {
					return AuthTypeOrder[existing.AuthTypes[i]] < AuthTypeOrder[existing.AuthTypes[j]]
				})
			}
			continue
		}
		byID[provider.ID] = &LoginProviderCompletionOption{
			ID: provider.ID, Name: provider.Name, AuthTypes: []string{provider.AuthType},
		}
		order = append(order, provider.ID)
	}
	result := make([]LoginProviderCompletionOption, 0, len(order))
	for _, id := range order {
		result = append(result, *byID[id])
	}
	sort.SliceStable(result, func(i int, j int) bool { return result[i].Name < result[j].Name })
	return result
}

// GetLoginProviderSearchText builds the fuzzy search text for a provider.
func GetLoginProviderSearchText(provider LoginProviderCompletionOption) string {
	parts := make([]string, 0, len(provider.AuthTypes))
	for _, authType := range provider.AuthTypes {
		parts = append(parts, authType+" "+FormatAuthSelectorProviderType(authType))
	}
	return provider.ID + " " + provider.Name + " " + strings.Join(parts, " ")
}

// FormatLoginProviderCompletionDescription formats the completion description.
func FormatLoginProviderCompletionDescription(provider LoginProviderCompletionOption) string {
	authTypes := make([]string, 0, len(provider.AuthTypes))
	for _, authType := range provider.AuthTypes {
		authTypes = append(authTypes, FormatAuthSelectorProviderType(authType))
	}
	joined := strings.Join(authTypes, "/")
	if provider.Name == provider.ID {
		return joined
	}
	return provider.Name + " · " + joined
}
