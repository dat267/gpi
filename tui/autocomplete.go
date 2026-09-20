package tui

import (
	"context"
	"regexp"
	"unicode"
)

func unicodePunctuation(r rune) bool { return unicode.IsPunct(r) }

// Port of the autocomplete surface from src/autocomplete.ts that the editor
// consumes. The provider implementations (combined slash-command and file
// completion) are ported separately.

// AutocompleteItem is a completion candidate.
type AutocompleteItem struct {
	Value       string `json:"value"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

// AutocompleteSuggestions is a completion result set.
type AutocompleteSuggestions struct {
	Items  []AutocompleteItem
	Prefix string
}

// SlashCommand describes a slash command for completion.
type SlashCommand struct {
	Name         string
	Description  string
	ArgumentHint string
	// GetArgumentCompletions returns argument completions, or nil.
	GetArgumentCompletions func(argumentPrefix string) ([]AutocompleteItem, bool)
}

// CompletionResult is the text state after applying a completion.
type CompletionResult struct {
	Lines      []string
	CursorLine int
	CursorCol  int
}

// AutocompleteProvider supplies completions for the editor.
type AutocompleteProvider interface {
	// TriggerCharacters are the characters that trigger this provider at token
	// boundaries.
	TriggerCharacters() []string
	// GetSuggestions returns completions for the cursor position, or nil.
	// The context carries cancellation (upstream's AbortSignal).
	GetSuggestions(ctx context.Context, lines []string, cursorLine int, cursorCol int, force bool) *AutocompleteSuggestions
	// ApplyCompletion applies the selected item.
	ApplyCompletion(lines []string, cursorLine int, cursorCol int, item AutocompleteItem, prefix string) CompletionResult
}

// FileCompletionTrigger is the optional provider hook deciding whether an
// explicit Tab completion may trigger file completion.
type FileCompletionTrigger interface {
	ShouldTriggerFileCompletion(lines []string, cursorLine int, cursorCol int) bool
}

// ---- Text classification from src/utils.ts ----

var cjkBreakRegex = regexp.MustCompile(`[\p{Han}\p{Hiragana}\p{Katakana}\p{Hangul}\p{Bopomofo}]`)

// cjkPunctuation is the explicit CJK punctuation set from utils.ts.
var editorCJKPunctuation = map[rune]bool{
	'，': true, '．': true, '：': true, '；': true, '！': true, '？': true,
	'（': true, '）': true, '［': true, '］': true, '｛': true, '｝': true,
	'“': true, '”': true, '‘': true, '’': true, '…': true, '—': true,
}

// isAutocompleteSeparator reports whether the character separates a completion
// token from the preceding text (upstream's autocompleteSeparatorRegex, which
// uses a lookahead RE2 cannot express: a punctuation character directly
// followed by a CJK character).
func isAutocompleteSeparator(character rune, next rune, hasNext bool) bool {
	if isWhitespaceRune(character) {
		return true
	}
	if editorCJKPunctuation[character] {
		return true
	}
	if unicodePunctuation(character) && hasNext && cjkBreakRegex.MatchString(string(next)) {
		return true
	}
	return false
}

// mustCompileRegex compiles a pattern that is known to be valid.
func mustCompileRegex(pattern string) *regexp.Regexp { return regexp.MustCompile(pattern) }
