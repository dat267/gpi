package tui

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// Port of the provider implementation in src/autocomplete.ts
// (CombinedAutocompleteProvider): slash-command completion, argument
// completion, path completion for the working directory, and fuzzy file
// search through `fd`.
//
// Divergences: the provider list is a single CommandEntry slice instead of a
// SlashCommand/AutocompleteItem union (D73); comparisons use byte ordering
// where upstream uses localeCompare (D72).

// CommandEntry is a slash command or a plain completion entry.
type CommandEntry struct {
	// Name is the slash command name (without the slash) or the item value.
	Name string
	// Label overrides the display label (defaults to Name).
	Label string
	// Description is the display description.
	Description string
	// ArgumentHint is shown before the description.
	ArgumentHint string
	// GetArgumentCompletions supplies argument completions, if any.
	GetArgumentCompletions func(argumentPrefix string) ([]AutocompleteItem, bool)
}

var pathDelimiters = map[rune]bool{' ': true, '\t': true, '"': true, '\'': true, '=': true}

func toDisplayPath(value string) string { return strings.ReplaceAll(value, "\\", "/") }

func escapeAutocompleteRegex(value string) string {
	var builder strings.Builder
	for _, r := range value {
		if strings.ContainsRune(`.*+?^${}()|[]\`, r) {
			builder.WriteByte('\\')
		}
		builder.WriteRune(r)
	}
	return builder.String()
}

// buildFdPathQuery converts a path query into an fd pattern.
func buildFdPathQuery(query string) string {
	normalized := toDisplayPath(query)
	if !strings.Contains(normalized, "/") {
		return normalized
	}

	hasTrailingSeparator := strings.HasSuffix(normalized, "/")
	trimmed := strings.Trim(normalized, "/")
	if trimmed == "" {
		return normalized
	}

	separatorPattern := "[\\\\/]"
	var segments []string
	for _, segment := range strings.Split(trimmed, "/") {
		if segment == "" {
			continue
		}
		segments = append(segments, escapeAutocompleteRegex(segment))
	}
	if len(segments) == 0 {
		return normalized
	}

	pattern := strings.Join(segments, separatorPattern)
	if hasTrailingSeparator {
		pattern += separatorPattern
	}
	return pattern
}

// findLastDelimiter returns the byte index of the last path delimiter.
func findLastDelimiter(text string) int {
	lastDelimiter := -1
	runes := []rune(text)
	byteIndex := 0
	for index, character := range runes {
		next := rune(0)
		hasNext := index+1 < len(runes)
		if hasNext {
			next = runes[index+1]
		}
		if pathDelimiters[character] || isAutocompleteSeparator(character, next, hasNext) {
			lastDelimiter = byteIndex
		}
		byteIndex += len(string(character))
	}
	return lastDelimiter
}

// findUnclosedQuoteStart returns the index of an unclosed quote, if any.
func findUnclosedQuoteStart(text string) (int, bool) {
	inQuotes := false
	quoteStart := -1
	byteIndex := 0
	for _, character := range text {
		if character == '"' {
			inQuotes = !inQuotes
			if inQuotes {
				quoteStart = byteIndex
			}
		}
		byteIndex += len(string(character))
	}
	if inQuotes {
		return quoteStart, true
	}
	return -1, false
}

func isTokenStart(text string, index int) bool {
	if index <= 0 {
		return true
	}
	runes := []rune(text)
	// The character before the index.
	var before rune
	byteIndex := 0
	for _, character := range runes {
		if byteIndex >= index {
			break
		}
		before = character
		byteIndex += len(string(character))
	}
	if pathDelimiters[before] {
		return true
	}
	return tokenStartMatches(text[:index])
}

// tokenStartMatches mirrors the tokenStartRegex (`boundary$`) applied to the
// text before the cursor.
func tokenStartMatches(text string) bool {
	if text == "" {
		return true
	}
	runes := []rune(text)
	last := runes[len(runes)-1]
	// A trailing separator (whitespace or CJK punctuation) is a boundary.
	if isAutocompleteSeparator(last, 0, false) {
		return true
	}
	// A punctuation character followed by a CJK character forms a boundary at
	// the punctuation: check whether the text ends with punctuation + CJK.
	if len(runes) >= 2 {
		previous := runes[len(runes)-2]
		if isAutocompleteSeparator(previous, last, true) {
			return true
		}
	}
	return false
}

func extractQuotedPrefix(text string) (string, bool) {
	quoteStart, ok := findUnclosedQuoteStart(text)
	if !ok {
		return "", false
	}
	if quoteStart > 0 {
		// The byte index of the character before the quote.
		before := text[:quoteStart]
		beforeRunes := []rune(before)
		if beforeRunes[len(beforeRunes)-1] == '@' {
			atIndex := quoteStart - 1
			if !isTokenStart(text, atIndex) {
				return "", false
			}
			return text[atIndex:], true
		}
	}
	if !isTokenStart(text, quoteStart) {
		return "", false
	}
	return text[quoteStart:], true
}

type parsedPathPrefix struct {
	rawPrefix      string
	isAtPrefix     bool
	isQuotedPrefix bool
}

func parsePathPrefix(prefix string) parsedPathPrefix {
	if strings.HasPrefix(prefix, `@"`) {
		return parsedPathPrefix{rawPrefix: prefix[2:], isAtPrefix: true, isQuotedPrefix: true}
	}
	if strings.HasPrefix(prefix, `"`) {
		return parsedPathPrefix{rawPrefix: prefix[1:], isQuotedPrefix: true}
	}
	if strings.HasPrefix(prefix, "@") {
		return parsedPathPrefix{rawPrefix: prefix[1:], isAtPrefix: true}
	}
	return parsedPathPrefix{rawPrefix: prefix}
}

func buildCompletionValue(path string, isDirectory bool, isAtPrefix bool, isQuotedPrefix bool) string {
	needsQuotes := isQuotedPrefix || autocompleteSeparatorCharIn(path)
	prefix := ""
	if isAtPrefix {
		prefix = "@"
	}
	if !needsQuotes {
		return prefix + path
	}
	return prefix + `"` + path + `"`
}

// autocompleteSeparatorCharIn reports whether the path contains a separator
// character (whitespace or CJK punctuation).
func autocompleteSeparatorCharIn(text string) bool {
	runes := []rune(text)
	for index, character := range runes {
		next := rune(0)
		hasNext := index+1 < len(runes)
		if hasNext {
			next = runes[index+1]
		}
		if isAutocompleteSeparator(character, next, hasNext) {
			return true
		}
	}
	return false
}

// walkDirectoryWithFd walks a directory tree with fd.
func walkDirectoryWithFd(ctx context.Context, baseDir string, fdPath string, query string, maxResults int, maxDepth int, hasMaxDepth bool) []fileEntry {
	args := []string{
		"--base-directory", baseDir,
		"--max-results", itoa(maxResults),
		"--type", "f",
		"--type", "d",
		"--follow",
		"--hidden",
		"--exclude", ".git",
		"--exclude", ".git/*",
		"--exclude", ".git/**",
	}
	if hasMaxDepth {
		args = append(args, "--max-depth", itoa(maxDepth))
	}
	if strings.Contains(toDisplayPath(query), "/") {
		args = append(args, "--full-path")
	}
	if query != "" {
		args = append(args, buildFdPathQuery(query))
	}

	command := exec.CommandContext(ctx, fdPath, args...)
	var stdout strings.Builder
	command.Stdout = &stdout
	if err := command.Run(); err != nil {
		return nil
	}
	if ctx.Err() != nil {
		return nil
	}

	var results []fileEntry
	for _, line := range strings.Split(strings.TrimSpace(stdout.String()), "\n") {
		if line == "" {
			continue
		}
		displayLine := toDisplayPath(line)
		hasTrailingSeparator := strings.HasSuffix(displayLine, "/")
		normalizedPath := displayLine
		if hasTrailingSeparator {
			normalizedPath = displayLine[:len(displayLine)-1]
		}
		if normalizedPath == ".git" || strings.HasPrefix(normalizedPath, ".git/") ||
			strings.Contains(normalizedPath, "/.git/") {
			continue
		}
		results = append(results, fileEntry{path: displayLine, isDirectory: hasTrailingSeparator})
	}
	return results
}

type fileEntry struct {
	path        string
	isDirectory bool
}

// CombinedAutocompleteProvider completes slash commands, command arguments,
// and file paths.
type CombinedAutocompleteProvider struct {
	commands []CommandEntry
	basePath string
	fdPath   string
}

// NewCombinedAutocompleteProvider creates the provider. basePath is the
// working directory for relative completions; fdPath is the `fd` binary (or
// "" to disable fuzzy search).
func NewCombinedAutocompleteProvider(commands []CommandEntry, basePath string, fdPath string) *CombinedAutocompleteProvider {
	return &CombinedAutocompleteProvider{commands: commands, basePath: basePath, fdPath: fdPath}
}

// TriggerCharacters returns no extra trigger characters (the editor's
// defaults cover @ and #).
func (p *CombinedAutocompleteProvider) TriggerCharacters() []string { return nil }

// GetSuggestions returns completions for the cursor position.
func (p *CombinedAutocompleteProvider) GetSuggestions(ctx context.Context, lines []string, cursorLine int, cursorCol int, force bool) *AutocompleteSuggestions {
	currentLine := ""
	if cursorLine >= 0 && cursorLine < len(lines) {
		currentLine = lines[cursorLine]
	}
	if cursorCol > len(currentLine) {
		cursorCol = len(currentLine)
	}
	if cursorCol < 0 {
		cursorCol = 0
	}
	textBeforeCursor := currentLine[:cursorCol]

	if atPrefix, ok := p.extractAtPrefix(textBeforeCursor); ok {
		parsed := parsePathPrefix(atPrefix)
		suggestions := p.getFuzzyFileSuggestions(ctx, parsed.rawPrefix, parsed.isQuotedPrefix)
		if len(suggestions) == 0 {
			return nil
		}
		return &AutocompleteSuggestions{Items: suggestions, Prefix: atPrefix}
	}

	if !force && strings.HasPrefix(textBeforeCursor, "/") {
		spaceIndex := strings.Index(textBeforeCursor, " ")
		if spaceIndex == -1 {
			prefix := textBeforeCursor[1:]

			type commandItem struct {
				name        string
				label       string
				description string
			}
			commandItems := make([]commandItem, 0, len(p.commands))
			for _, cmd := range p.commands {
				description := cmd.Description
				if cmd.ArgumentHint != "" {
					if description != "" {
						description = cmd.ArgumentHint + " — " + description
					} else {
						description = cmd.ArgumentHint
					}
				}
				label := cmd.Label
				if label == "" {
					label = cmd.Name
				}
				commandItems = append(commandItems, commandItem{name: cmd.Name, label: label, description: description})
			}

			filtered := FuzzyFilter(commandItems, prefix, func(item commandItem) string {
				if !strings.HasPrefix(prefix, "skill:") && strings.HasPrefix(item.name, "skill:") {
					return item.name[len("skill:"):]
				}
				return item.name
			})
			items := make([]AutocompleteItem, 0, len(filtered))
			for _, item := range filtered {
				items = append(items, AutocompleteItem{Value: item.name, Label: item.label, Description: item.description})
			}
			if len(items) == 0 {
				return nil
			}
			return &AutocompleteSuggestions{Items: items, Prefix: textBeforeCursor}
		}

		commandName := textBeforeCursor[1:spaceIndex]
		argumentText := textBeforeCursor[spaceIndex+1:]
		for _, cmd := range p.commands {
			if cmd.Name != commandName {
				continue
			}
			if cmd.GetArgumentCompletions == nil {
				return nil
			}
			argumentSuggestions, ok := cmd.GetArgumentCompletions(argumentText)
			if !ok || len(argumentSuggestions) == 0 {
				return nil
			}
			return &AutocompleteSuggestions{Items: argumentSuggestions, Prefix: argumentText}
		}
		return nil
	}

	pathMatch, ok := p.extractPathPrefix(textBeforeCursor, force)
	if !ok {
		return nil
	}
	suggestions := p.getFileSuggestions(pathMatch)
	if len(suggestions) == 0 {
		return nil
	}
	return &AutocompleteSuggestions{Items: suggestions, Prefix: pathMatch}
}

// ApplyCompletion applies the selected item.
func (p *CombinedAutocompleteProvider) ApplyCompletion(lines []string, cursorLine int, cursorCol int, item AutocompleteItem, prefix string) CompletionResult {
	currentLine := ""
	if cursorLine >= 0 && cursorLine < len(lines) {
		currentLine = lines[cursorLine]
	}
	prefixStart := cursorCol - len(prefix)
	if prefixStart < 0 {
		prefixStart = 0
	}
	if prefixStart > len(currentLine) {
		prefixStart = len(currentLine)
	}
	if cursorCol > len(currentLine) {
		cursorCol = len(currentLine)
	}
	beforePrefix := currentLine[:prefixStart]
	afterCursor := currentLine[cursorCol:]

	isQuotedPrefix := strings.HasPrefix(prefix, `"`) || strings.HasPrefix(prefix, `@"`)
	hasLeadingQuoteAfterCursor := strings.HasPrefix(afterCursor, `"`)
	hasTrailingQuoteInItem := strings.HasSuffix(item.Value, `"`)
	adjustedAfterCursor := afterCursor
	if isQuotedPrefix && hasTrailingQuoteInItem && hasLeadingQuoteAfterCursor {
		adjustedAfterCursor = afterCursor[1:]
	}

	isSlashCommand := strings.HasPrefix(prefix, "/") && strings.TrimSpace(beforePrefix) == "" &&
		!strings.Contains(prefix[1:], "/")
	if isSlashCommand {
		newLines := append([]string(nil), lines...)
		newLines[cursorLine] = beforePrefix + "/" + item.Value + " " + adjustedAfterCursor
		return CompletionResult{
			Lines:      newLines,
			CursorLine: cursorLine,
			CursorCol:  len(beforePrefix) + len(item.Value) + 2,
		}
	}

	if strings.HasPrefix(prefix, "@") {
		suffix := " "
		if strings.HasSuffix(item.Label, "/") {
			suffix = ""
		}
		newLines := append([]string(nil), lines...)
		newLines[cursorLine] = beforePrefix + item.Value + suffix + adjustedAfterCursor
		cursorOffset := len(item.Value)
		if strings.HasSuffix(item.Label, "/") && hasTrailingQuoteInItem {
			cursorOffset = len(item.Value) - 1
		}
		return CompletionResult{
			Lines:      newLines,
			CursorLine: cursorLine,
			CursorCol:  len(beforePrefix) + cursorOffset + len(suffix),
		}
	}

	cursorOffset := len(item.Value)
	if strings.HasSuffix(item.Label, "/") && hasTrailingQuoteInItem {
		cursorOffset = len(item.Value) - 1
	}

	textBeforeCursor := currentLine[:cursorCol]
	if strings.Contains(textBeforeCursor, "/") && strings.Contains(textBeforeCursor, " ") {
		newLines := append([]string(nil), lines...)
		newLines[cursorLine] = beforePrefix + item.Value + adjustedAfterCursor
		return CompletionResult{Lines: newLines, CursorLine: cursorLine, CursorCol: len(beforePrefix) + cursorOffset}
	}

	newLines := append([]string(nil), lines...)
	newLines[cursorLine] = beforePrefix + item.Value + adjustedAfterCursor
	return CompletionResult{Lines: newLines, CursorLine: cursorLine, CursorCol: len(beforePrefix) + cursorOffset}
}

func (p *CombinedAutocompleteProvider) extractAtPrefix(text string) (string, bool) {
	if quotedPrefix, ok := extractQuotedPrefix(text); ok && strings.HasPrefix(quotedPrefix, `@"`) {
		return quotedPrefix, true
	}
	lastDelimiterIndex := findLastDelimiter(text)
	tokenStart := 0
	if lastDelimiterIndex != -1 {
		tokenStart = lastDelimiterIndex + 1
	}
	if tokenStart < len(text) && text[tokenStart] == '@' {
		return text[tokenStart:], true
	}
	return "", false
}

func (p *CombinedAutocompleteProvider) extractPathPrefix(text string, forceExtract bool) (string, bool) {
	if quotedPrefix, ok := extractQuotedPrefix(text); ok {
		return quotedPrefix, true
	}
	lastDelimiterIndex := findLastDelimiter(text)
	pathPrefix := text
	if lastDelimiterIndex != -1 {
		pathPrefix = text[lastDelimiterIndex+1:]
	}
	if forceExtract {
		return pathPrefix, true
	}
	if strings.Contains(pathPrefix, "/") || strings.HasPrefix(pathPrefix, ".") || strings.HasPrefix(pathPrefix, "~/") {
		return pathPrefix, true
	}
	if pathPrefix == "" && text != "" && tokenStartMatches(text) {
		return pathPrefix, true
	}
	return "", false
}

func (p *CombinedAutocompleteProvider) expandHomePath(path string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = ""
	}
	if strings.HasPrefix(path, "~/") {
		expanded := filepath.Join(home, path[2:])
		if strings.HasSuffix(path, "/") && !strings.HasSuffix(expanded, "/") {
			return expanded + "/"
		}
		return expanded
	}
	if path == "~" {
		return home
	}
	return path
}

type scopedFuzzyQuery struct {
	baseDir     string
	query       string
	displayBase string
}

func (p *CombinedAutocompleteProvider) resolveScopedFuzzyQuery(rawQuery string) (scopedFuzzyQuery, bool) {
	normalizedQuery := toDisplayPath(rawQuery)
	slashIndex := strings.LastIndex(normalizedQuery, "/")
	if slashIndex == -1 {
		return scopedFuzzyQuery{}, false
	}
	displayBase := normalizedQuery[:slashIndex+1]
	query := normalizedQuery[slashIndex+1:]

	var baseDir string
	switch {
	case strings.HasPrefix(displayBase, "~/"):
		baseDir = p.expandHomePath(displayBase)
	case strings.HasPrefix(displayBase, "/"):
		baseDir = displayBase
	default:
		baseDir = filepath.Join(p.basePath, displayBase)
	}

	info, err := os.Stat(baseDir)
	if err != nil || !info.IsDir() {
		return scopedFuzzyQuery{}, false
	}
	return scopedFuzzyQuery{baseDir: baseDir, query: query, displayBase: displayBase}, true
}

func (p *CombinedAutocompleteProvider) scopedPathForDisplay(displayBase string, relativePath string) string {
	normalizedRelativePath := toDisplayPath(relativePath)
	if displayBase == "/" {
		return "/" + normalizedRelativePath
	}
	return toDisplayPath(displayBase) + normalizedRelativePath
}

// getFileSuggestions lists entries matching a path prefix.
func (p *CombinedAutocompleteProvider) getFileSuggestions(prefix string) []AutocompleteItem {
	parsed := parsePathPrefix(prefix)
	rawPrefix := parsed.rawPrefix
	expandedPrefix := rawPrefix
	if strings.HasPrefix(expandedPrefix, "~") {
		expandedPrefix = p.expandHomePath(expandedPrefix)
	}

	isRootPrefix := rawPrefix == "" || rawPrefix == "./" || rawPrefix == "../" || rawPrefix == "~" ||
		rawPrefix == "~/" || rawPrefix == "/" || (parsed.isAtPrefix && rawPrefix == "")

	// The last segment, for the directory/prefix split and for the "." and ".."
	// tails below (computed from the raw prefix: see the default branch). A
	// trailing "." or ".." names a directory, so it belongs to the search path;
	// ".." is not a name prefix, so it also clears the filter and has to stay in
	// the suggestion (dropping it would point at the wrong directory). A lone "."
	// stays the filter, the way a shell completes `~/.` with dotfiles only.
	rawFile := filepath.Base(rawPrefix)
	directoryTail := rawFile == "." || rawFile == ".."
	parentTail := rawFile == ".."

	var searchDir string
	var searchPrefix string
	switch {
	case isRootPrefix:
		if strings.HasPrefix(rawPrefix, "~") || strings.HasPrefix(expandedPrefix, "/") {
			searchDir = expandedPrefix
		} else {
			searchDir = filepath.Join(p.basePath, expandedPrefix)
		}
		searchPrefix = ""
	case strings.HasSuffix(rawPrefix, "/"):
		if strings.HasPrefix(rawPrefix, "~") || strings.HasPrefix(expandedPrefix, "/") {
			searchDir = expandedPrefix
		} else {
			searchDir = filepath.Join(p.basePath, expandedPrefix)
		}
		searchPrefix = ""
	default:
		// Split the raw prefix, then expand the directory: expanding first eats a
		// trailing "." (filepath.Join cleans the path), which leaves Dir/Base
		// looking at the *parent* of the directory the user meant — `~/.` listed
		// /home and offered `~/dat/` (the home directory's own name). Upstream has
		// the same bug (path.join cleans too), so this is a deliberate divergence
		// (D157).
		dir := filepath.Dir(rawPrefix)
		// A trailing "." or ".." names a directory rather than a prefix to filter
		// by, so it becomes part of the search path and the filter starts empty.
		if strings.HasPrefix(dir, "~") {
			dir = p.expandHomePath(dir)
		}
		if directoryTail {
			dir = filepath.Join(dir, rawFile)
		}
		if strings.HasPrefix(dir, "~") || strings.HasPrefix(dir, "/") {
			searchDir = dir
		} else {
			searchDir = filepath.Join(p.basePath, dir)
		}
		searchPrefix = rawFile
		if parentTail {
			searchPrefix = ""
		}
	}

	entries, err := os.ReadDir(searchDir)
	if err != nil {
		return nil
	}

	var suggestions []AutocompleteItem
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(strings.ToLower(name), strings.ToLower(searchPrefix)) {
			continue
		}

		isDirectory := entry.IsDir()
		if !isDirectory && entry.Type()&os.ModeSymlink != 0 {
			if info, err := os.Stat(filepath.Join(searchDir, name)); err == nil {
				isDirectory = info.IsDir()
			}
		}

		displayPrefix := rawPrefix
		var relativePath string
		switch {
		case parentTail:
			relativePath = displayPrefix + "/" + name
		case strings.HasSuffix(displayPrefix, "/"):
			relativePath = displayPrefix + name
		case strings.Contains(displayPrefix, "/") || strings.Contains(displayPrefix, "\\"):
			switch {
			case strings.HasPrefix(displayPrefix, "~/"):
				homeRelativeDir := displayPrefix[2:]
				dir := filepath.Dir(homeRelativeDir)
				if dir == "." {
					relativePath = "~/" + name
				} else {
					relativePath = "~/" + filepath.Join(dir, name)
				}
			case strings.HasPrefix(displayPrefix, "/"):
				dir := filepath.Dir(displayPrefix)
				if dir == "/" {
					relativePath = "/" + name
				} else {
					relativePath = dir + "/" + name
				}
			default:
				relativePath = filepath.Join(filepath.Dir(displayPrefix), name)
				if strings.HasPrefix(displayPrefix, "./") && !strings.HasPrefix(relativePath, "./") {
					relativePath = "./" + relativePath
				}
			}
		default:
			if strings.HasPrefix(displayPrefix, "~") {
				relativePath = "~/" + name
			} else {
				relativePath = name
			}
		}

		relativePath = toDisplayPath(relativePath)
		pathValue := relativePath
		if isDirectory {
			pathValue += "/"
		}
		label := name
		if isDirectory {
			label += "/"
		}
		suggestions = append(suggestions, AutocompleteItem{
			Value: buildCompletionValue(pathValue, isDirectory, parsed.isAtPrefix, parsed.isQuotedPrefix),
			Label: label,
		})
	}

	sort.SliceStable(suggestions, func(a, b int) bool {
		aIsDir := strings.HasSuffix(suggestions[a].Label, "/")
		bIsDir := strings.HasSuffix(suggestions[b].Label, "/")
		if aIsDir && !bIsDir {
			return true
		}
		if !aIsDir && bIsDir {
			return false
		}
		return localeCompareLike(suggestions[a].Label, suggestions[b].Label) < 0
	})

	return suggestions
}

// scoreEntry scores a path against a query (higher is better).
func (p *CombinedAutocompleteProvider) scoreEntry(filePath string, query string, isDirectory bool) int {
	fileName := filepath.Base(filePath)
	lowerFileName := strings.ToLower(fileName)
	lowerQuery := strings.ToLower(query)

	score := 0
	switch {
	case lowerFileName == lowerQuery:
		score = 100
	case strings.HasPrefix(lowerFileName, lowerQuery):
		score = 80
	case strings.Contains(lowerFileName, lowerQuery):
		score = 50
	case strings.Contains(strings.ToLower(filePath), lowerQuery):
		score = 30
	}
	if isDirectory && score > 0 {
		score += 10
	}
	return score
}

func (p *CombinedAutocompleteProvider) getBaseDirSuggestions(ctx context.Context, baseDir string, query string) []fileEntry {
	if p.fdPath == "" || ctx.Err() != nil {
		return nil
	}
	return walkDirectoryWithFd(ctx, baseDir, p.fdPath, query, 100, 1, true)
}

// getFuzzyFileSuggestions searches files with fd.
func (p *CombinedAutocompleteProvider) getFuzzyFileSuggestions(ctx context.Context, query string, isQuotedPrefix bool) []AutocompleteItem {
	if p.fdPath == "" || ctx.Err() != nil {
		return nil
	}

	scopedQuery, hasScopedQuery := p.resolveScopedFuzzyQuery(query)
	fdBaseDir := p.basePath
	fdQuery := query
	if hasScopedQuery {
		fdBaseDir = scopedQuery.baseDir
		fdQuery = scopedQuery.query
	}

	baseDirEntries := p.getBaseDirSuggestions(ctx, fdBaseDir, fdQuery)
	recursiveEntries := walkDirectoryWithFd(ctx, fdBaseDir, p.fdPath, fdQuery, 100, 0, false)
	seenPaths := map[string]bool{}
	for _, entry := range baseDirEntries {
		seenPaths[entry.path] = true
	}
	entries := append([]fileEntry(nil), baseDirEntries...)
	for _, entry := range recursiveEntries {
		if seenPaths[entry.path] {
			continue
		}
		seenPaths[entry.path] = true
		entries = append(entries, entry)
	}
	if ctx.Err() != nil {
		return nil
	}

	type scoredEntry struct {
		path        string
		isDirectory bool
		score       int
	}
	var scored []scoredEntry
	for _, entry := range entries {
		score := 1
		if fdQuery != "" {
			score = p.scoreEntry(entry.path, fdQuery, entry.isDirectory)
		}
		if score > 0 {
			scored = append(scored, scoredEntry{path: entry.path, isDirectory: entry.isDirectory, score: score})
		}
	}

	sort.SliceStable(scored, func(a, b int) bool {
		if scored[a].score != scored[b].score {
			return scored[a].score > scored[b].score
		}
		aDepth := len(splitNonEmpty(toDisplayPath(scored[a].path), "/"))
		bDepth := len(splitNonEmpty(toDisplayPath(scored[b].path), "/"))
		if aDepth != bDepth {
			return aDepth < bDepth
		}
		if len(scored[a].path) != len(scored[b].path) {
			return len(scored[a].path) < len(scored[b].path)
		}
		return localeCompareLike(scored[a].path, scored[b].path) < 0
	})

	if len(scored) > 20 {
		scored = scored[:20]
	}

	var suggestions []AutocompleteItem
	for _, entry := range scored {
		pathWithoutSlash := entry.path
		if entry.isDirectory {
			pathWithoutSlash = entry.path[:len(entry.path)-1]
		}
		displayPath := pathWithoutSlash
		if hasScopedQuery {
			displayPath = p.scopedPathForDisplay(scopedQuery.displayBase, pathWithoutSlash)
		}
		entryName := filepath.Base(pathWithoutSlash)
		completionPath := displayPath
		if entry.isDirectory {
			completionPath += "/"
		}
		label := entryName
		if entry.isDirectory {
			label += "/"
		}
		suggestions = append(suggestions, AutocompleteItem{
			Value:       buildCompletionValue(completionPath, entry.isDirectory, true, isQuotedPrefix),
			Label:       label,
			Description: displayPath,
		})
	}
	return suggestions
}

// localeCompareLike approximates JavaScript's localeCompare (ICU collation):
// the primary comparison is case-insensitive with the original bytes as the
// tie-break (divergence D72: full ICU collation is not reproduced).
func localeCompareLike(a string, b string) int {
	lowerA := strings.ToLower(a)
	lowerB := strings.ToLower(b)
	if lowerA != lowerB {
		if lowerA < lowerB {
			return -1
		}
		return 1
	}
	if a == b {
		return 0
	}
	if a < b {
		return -1
	}
	return 1
}

func splitNonEmpty(value string, separator string) []string {
	var result []string
	for _, part := range strings.Split(value, separator) {
		if part != "" {
			result = append(result, part)
		}
	}
	return result
}

// ShouldTriggerFileCompletion reports whether Tab may trigger file completion.
func (p *CombinedAutocompleteProvider) ShouldTriggerFileCompletion(lines []string, cursorLine int, cursorCol int) bool {
	currentLine := ""
	if cursorLine >= 0 && cursorLine < len(lines) {
		currentLine = lines[cursorLine]
	}
	if cursorCol > len(currentLine) {
		cursorCol = len(currentLine)
	}
	if cursorCol < 0 {
		cursorCol = 0
	}
	textBeforeCursor := currentLine[:cursorCol]
	trimmed := strings.TrimSpace(textBeforeCursor)
	if strings.HasPrefix(trimmed, "/") && !strings.Contains(trimmed, " ") {
		return false
	}
	return true
}

var _ AutocompleteProvider = (*CombinedAutocompleteProvider)(nil)
var _ FileCompletionTrigger = (*CombinedAutocompleteProvider)(nil)
