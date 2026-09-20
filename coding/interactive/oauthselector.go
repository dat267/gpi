package interactive

import (
	"regexp"
	"strings"

	"github.com/dat267/gpi/ai"
	"github.com/dat267/gpi/coding"
	"github.com/dat267/gpi/tui"
)

// Port of src/modes/interactive/components/{session-selector-search,
// oauth-selector,login-dialog}.ts.

// SortMode is a session sort mode.
type SortMode string

const (
	SortThreaded  SortMode = "threaded"
	SortRecent    SortMode = "recent"
	SortRelevance SortMode = "relevance"
)

// NameFilter filters sessions by whether they have a name.
type NameFilter string

const (
	NameFilterAll   NameFilter = "all"
	NameFilterNamed NameFilter = "named"
)

// SearchToken is a parsed query token. The JSON field names match upstream.
type SearchToken struct {
	Kind  string `json:"kind"` // "fuzzy" | "phrase"
	Value string `json:"value"`
}

// ParsedSearchQuery is a parsed session search query.
type ParsedSearchQuery struct {
	Mode   string // "tokens" | "regex"
	Tokens []SearchToken
	Regex  *regexp.Regexp
	// Pattern is the raw regex source (without the case-insensitive flag,
	// matching JS RegExp.source).
	Pattern string
	Error   string
}

// MatchResult is a session match outcome.
type MatchResult struct {
	Matches bool
	Score   float64
}

func normalizeWhitespaceLower(text string) string {
	return strings.TrimSpace(regexp.MustCompile(`\s+`).ReplaceAllString(strings.ToLower(text), " "))
}

func getSessionSearchText(session coding.SessionInfo) string {
	return session.ID + " " + session.Name + " " + session.AllMessagesText + " " + session.Cwd
}

// HasSessionName reports whether the session has a non-empty name.
func HasSessionName(session coding.SessionInfo) bool {
	return strings.TrimSpace(session.Name) != ""
}

func matchesNameFilter(session coding.SessionInfo, filter NameFilter) bool {
	if filter == NameFilterAll {
		return true
	}
	return HasSessionName(session)
}

// ParseSearchQuery parses a session search query.
func ParseSearchQuery(query string) ParsedSearchQuery {
	trimmed := strings.TrimSpace(query)
	if trimmed == "" {
		return ParsedSearchQuery{Mode: "tokens"}
	}

	if strings.HasPrefix(trimmed, "re:") {
		pattern := strings.TrimSpace(trimmed[3:])
		if pattern == "" {
			return ParsedSearchQuery{Mode: "regex", Error: "Empty regex"}
		}
		compiled, err := regexp.Compile("(?i)" + pattern)
		if err != nil {
			return ParsedSearchQuery{Mode: "regex", Error: err.Error()}
		}
		return ParsedSearchQuery{Mode: "regex", Regex: compiled, Pattern: pattern}
	}

	var tokens []SearchToken
	var builder strings.Builder
	inQuote := false
	hadUnclosedQuote := false

	flush := func(kind string) {
		value := strings.TrimSpace(builder.String())
		builder.Reset()
		if value == "" {
			return
		}
		tokens = append(tokens, SearchToken{Kind: kind, Value: value})
	}

	for _, character := range trimmed {
		switch {
		case character == '"':
			if inQuote {
				flush("phrase")
				inQuote = false
			} else {
				flush("fuzzy")
				inQuote = true
			}
		case !inQuote && isSpaceRune(character):
			flush("fuzzy")
		default:
			builder.WriteRune(character)
		}
	}
	if inQuote {
		hadUnclosedQuote = true
	}

	if hadUnclosedQuote {
		var fallback []SearchToken
		for _, token := range strings.Fields(trimmed) {
			value := strings.TrimSpace(token)
			if value != "" {
				fallback = append(fallback, SearchToken{Kind: "fuzzy", Value: value})
			}
		}
		return ParsedSearchQuery{Mode: "tokens", Tokens: fallback}
	}

	flush("fuzzy")
	return ParsedSearchQuery{Mode: "tokens", Tokens: tokens}
}

// MatchSession matches a session against a parsed query.
func MatchSession(session coding.SessionInfo, parsed ParsedSearchQuery) MatchResult {
	text := getSessionSearchText(session)

	if parsed.Mode == "regex" {
		if parsed.Regex == nil {
			return MatchResult{}
		}
		index := parsed.Regex.FindStringIndex(text)
		if index == nil {
			return MatchResult{}
		}
		return MatchResult{Matches: true, Score: float64(index[0]) * 0.1}
	}

	if len(parsed.Tokens) == 0 {
		return MatchResult{Matches: true}
	}

	totalScore := 0.0
	normalizedText := ""
	hasNormalized := false

	for _, token := range parsed.Tokens {
		if token.Kind == "phrase" {
			if !hasNormalized {
				normalizedText = normalizeWhitespaceLower(text)
				hasNormalized = true
			}
			phrase := normalizeWhitespaceLower(token.Value)
			if phrase == "" {
				continue
			}
			index := strings.Index(normalizedText, phrase)
			if index < 0 {
				return MatchResult{}
			}
			totalScore += float64(index) * 0.1
			continue
		}
		match := tui.MatchFuzzy(token.Value, text)
		if !match.Matches {
			return MatchResult{}
		}
		totalScore += match.Score
	}
	return MatchResult{Matches: true, Score: totalScore}
}

// FilterAndSortSessions filters and sorts sessions for the selector.
func FilterAndSortSessions(sessions []coding.SessionInfo, query string, sortMode SortMode, nameFilter NameFilter) []coding.SessionInfo {
	filtered := sessions
	if nameFilter != NameFilterAll {
		filtered = nil
		for _, session := range sessions {
			if matchesNameFilter(session, nameFilter) {
				filtered = append(filtered, session)
			}
		}
	}
	if strings.TrimSpace(query) == "" {
		return filtered
	}

	parsed := ParseSearchQuery(query)
	if parsed.Error != "" {
		return nil
	}

	if sortMode == SortRecent {
		var result []coding.SessionInfo
		for _, session := range filtered {
			if MatchSession(session, parsed).Matches {
				result = append(result, session)
			}
		}
		return result
	}

	type scoredSession struct {
		session coding.SessionInfo
		score   float64
	}
	var scored []scoredSession
	for _, session := range filtered {
		match := MatchSession(session, parsed)
		if match.Matches {
			scored = append(scored, scoredSession{session: session, score: match.Score})
		}
	}
	for i := 1; i < len(scored); i++ {
		for j := i; j > 0; j-- {
			less := scored[j].score < scored[j-1].score
			if scored[j].score == scored[j-1].score {
				less = scored[j].session.Modified.After(scored[j-1].session.Modified)
			}
			if !less {
				break
			}
			scored[j], scored[j-1] = scored[j-1], scored[j]
		}
	}
	result := make([]coding.SessionInfo, 0, len(scored))
	for _, entry := range scored {
		result = append(result, entry.session)
	}
	return result
}

func isSpaceRune(r rune) bool { return tui.IsWhitespaceChar(string(r)) }

// ---- OAuth selector ----

// AuthSelectorProvider is a provider entry for the auth selector.
type AuthSelectorProvider struct {
	ID       string
	Name     string
	AuthType string // "oauth" | "api_key"
	Method   any
	Status   *ai.AuthCheck
}

// FormatAuthSelectorProviderType formats the auth type label.
func FormatAuthSelectorProviderType(authType string) string {
	if authType == "oauth" {
		return "subscription"
	}
	return "API key"
}

// OAuthSelectorComponent renders the auth provider selector.
type OAuthSelectorComponent struct {
	*tui.Container

	searchInput        *tui.Input
	focused            bool
	listContainer      *tui.Container
	allProviders       []AuthSelectorProvider
	filteredProviders  []AuthSelectorProvider
	selectedIndex      int
	mode               string
	onSelect           func(providerID string, authType string)
	onCancel           func()
	showAuthTypeLabels bool
}

// NewOAuthSelectorComponent creates the selector.
func NewOAuthSelectorComponent(mode string, providers []AuthSelectorProvider, onSelect func(string, string), onCancel func(), initialSearchInput string) *OAuthSelectorComponent {
	theme := ActiveTheme()
	component := &OAuthSelectorComponent{
		Container:         &tui.Container{},
		mode:              mode,
		allProviders:      providers,
		filteredProviders: providers,
		onSelect:          onSelect,
		onCancel:          onCancel,
	}
	authTypes := map[string]bool{}
	for _, provider := range providers {
		authTypes[provider.AuthType] = true
	}
	component.showAuthTypeLabels = len(authTypes) > 1

	component.AddChild(NewDynamicBorder(nil))
	component.AddChild(tui.NewSpacer(1))
	title := "Select provider to configure:"
	if mode == "logout" {
		title = "Select provider to logout:"
	}
	component.AddChild(tui.NewTruncatedText(theme.Fg("accent", theme.Bold(title)), 1, 0))

	component.AddChild(tui.NewSpacer(1))
	component.searchInput = tui.NewInput(tui.InputOptions{})
	if initialSearchInput != "" {
		component.searchInput.SetValue(initialSearchInput)
	}
	component.searchInput.OnSubmit = func(string) {
		if component.selectedIndex >= 0 && component.selectedIndex < len(component.filteredProviders) {
			selected := component.filteredProviders[component.selectedIndex]
			if component.onSelect != nil {
				component.onSelect(selected.ID, selected.AuthType)
			}
		}
	}
	component.AddChild(component.searchInput)
	component.AddChild(tui.NewSpacer(1))

	component.listContainer = &tui.Container{}
	component.AddChild(component.listContainer)
	component.AddChild(tui.NewSpacer(1))
	component.AddChild(NewDynamicBorder(nil))

	component.FilterProviders(initialSearchInput)
	return component
}

// FilterProviders filters the provider list.
func (c *OAuthSelectorComponent) FilterProviders(query string) {
	if query != "" {
		c.filteredProviders = tui.FuzzyFilter(c.allProviders, query, func(provider AuthSelectorProvider) string {
			methodName := ""
			if named, ok := provider.Method.(interface{ GetName() string }); ok {
				methodName = named.GetName()
			}
			return provider.Name + " " + provider.ID + " " + provider.AuthType + " " + methodName
		})
	} else {
		c.filteredProviders = c.allProviders
	}
	c.selectedIndex = maxIntLocal(0, minIntLocal(c.selectedIndex, maxIntLocal(0, len(c.filteredProviders)-1)))
	c.updateList()
}

func (c *OAuthSelectorComponent) updateList() {
	theme := ActiveTheme()
	c.listContainer.Clear()

	const maxVisible = 8
	startIndex := maxIntLocal(0, minIntLocal(c.selectedIndex-maxVisible/2, len(c.filteredProviders)-maxVisible))
	endIndex := minIntLocal(startIndex+maxVisible, len(c.filteredProviders))
	for i := startIndex; i < endIndex; i++ {
		provider := c.filteredProviders[i]
		isSelected := i == c.selectedIndex
		statusIndicator := c.formatStatusIndicator(provider)
		authTypeLabel := ""
		if c.showAuthTypeLabels {
			authTypeLabel = theme.Fg("muted", " ["+FormatAuthSelectorProviderType(provider.AuthType)+"]")
		}
		line := "  " + theme.Fg("text", provider.Name) + authTypeLabel + statusIndicator
		if isSelected {
			line = theme.Fg("accent", "→ ") + theme.Fg("accent", provider.Name) + authTypeLabel + statusIndicator
		}
		c.listContainer.AddChild(tui.NewTruncatedText(line, 1, 0))
	}

	if startIndex > 0 || endIndex < len(c.filteredProviders) {
		c.listContainer.AddChild(tui.NewTruncatedText(theme.Fg("muted",
			"  ("+itoa(c.selectedIndex+1)+"/"+itoa(len(c.filteredProviders))+")"), 1, 0))
	}

	if len(c.filteredProviders) == 0 {
		message := "No matching providers"
		if len(c.allProviders) == 0 {
			if c.mode == "login" {
				message = "No providers available"
			} else {
				message = "No providers logged in. Use /login first."
			}
		}
		c.listContainer.AddChild(tui.NewTruncatedText(theme.Fg("muted", "  "+message), 1, 0))
	}
}

var envVarRegex = regexp.MustCompile(`^[A-Z][A-Z0-9_]*(?:, [A-Z][A-Z0-9_]*)*$`)

func (c *OAuthSelectorComponent) formatStatusIndicator(provider AuthSelectorProvider) string {
	theme := ActiveTheme()
	if provider.Status == nil {
		return theme.Fg("muted", " • unconfigured")
	}
	if provider.Status.Type != provider.AuthType {
		label := "API key configured"
		if provider.Status.Type == "oauth" {
			label = "subscription configured"
		}
		return theme.Fg("muted", " • ") + theme.Fg("warning", label)
	}
	if provider.Status.Source == "" || provider.Status.Source == "OAuth" || provider.Status.Source == "stored credential" {
		return theme.Fg("success", " ✓ configured")
	}
	source := provider.Status.Source
	if envVarRegex.MatchString(source) {
		source = "env: " + source
	}
	return theme.Fg("success", " ✓ "+source)
}

// HandleInput processes input.
func (c *OAuthSelectorComponent) HandleInput(data string) {
	kb := tui.GetKeybindings()
	switch {
	case kb.Matches(data, "tui.select.up"):
		if len(c.filteredProviders) == 0 {
			return
		}
		c.selectedIndex = maxIntLocal(0, c.selectedIndex-1)
		c.updateList()
	case kb.Matches(data, "tui.select.down"):
		if len(c.filteredProviders) == 0 {
			return
		}
		c.selectedIndex = minIntLocal(len(c.filteredProviders)-1, c.selectedIndex+1)
		c.updateList()
	case kb.Matches(data, "tui.select.confirm"):
		if c.selectedIndex >= 0 && c.selectedIndex < len(c.filteredProviders) {
			selected := c.filteredProviders[c.selectedIndex]
			if c.onSelect != nil {
				c.onSelect(selected.ID, selected.AuthType)
			}
		}
	case kb.Matches(data, "tui.select.cancel"):
		if c.onCancel != nil {
			c.onCancel()
		}
	default:
		c.searchInput.HandleInput(data)
		c.FilterProviders(c.searchInput.Value())
	}
}

// SetFocused implements Focusable.
func (c *OAuthSelectorComponent) SetFocused(focused bool) {
	c.focused = focused
	c.searchInput.SetFocused(focused)
}

// IsFocused implements Focusable.
func (c *OAuthSelectorComponent) IsFocused() bool { return c.focused }

// SelectedProvider returns the highlighted provider.
func (c *OAuthSelectorComponent) SelectedProvider() (AuthSelectorProvider, bool) {
	if c.selectedIndex < 0 || c.selectedIndex >= len(c.filteredProviders) {
		return AuthSelectorProvider{}, false
	}
	return c.filteredProviders[c.selectedIndex], true
}

var (
	_ tui.Component = (*OAuthSelectorComponent)(nil)
	_ tui.Focusable = (*OAuthSelectorComponent)(nil)
)
