package interactive

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dat267/pier/coding"
	"github.com/dat267/pier/tui"
)

// Port of src/modes/interactive/components/session-selector.ts: the resume
// session selector with the tree view, search, delete and rename.
//
// Divergences: AbortController becomes a context (D100); the status-message
// timer is injectable (D101); the session-file deleter is injectable (D102);
// loader results are applied under a state mutex with a settle helper because
// the Go port has no single-threaded event loop (D103).

// SessionScope selects the loaded session set.
type SessionScope = string

// Session scopes.
const (
	SessionScopeCurrent SessionScope = "current"
	SessionScopeAll     SessionScope = "all"
)

// SessionListProgress reports partial session loads.
type SessionListProgress func(loaded int, total int, partialSessions []coding.SessionInfo)

// SessionsLoader loads sessions (upstream SessionsLoader).
type SessionsLoader func(onProgress SessionListProgress, ctx context.Context) ([]coding.SessionInfo, error)

// SessionDeleteResult is the outcome of deleting a session file.
type SessionDeleteResult struct {
	OK     bool
	Method string // "trash" | "unlink"
	Error  string
}

// SessionFileDeleter deletes a session file (D102).
type SessionFileDeleter func(sessionPath string) SessionDeleteResult

var sessionFileDeleter SessionFileDeleter = DeleteSessionFile

// SetSessionFileDeleter overrides the session file deleter (test seam, D102).
func SetSessionFileDeleter(deleter SessionFileDeleter) { sessionFileDeleter = deleter }

// DeleteSessionFile deletes a session file, trying the `trash` CLI first and
// falling back to a permanent unlink.
func DeleteSessionFile(sessionPath string) SessionDeleteResult {
	trashArgs := []string{sessionPath}
	if strings.HasPrefix(sessionPath, "-") {
		trashArgs = []string{"--", sessionPath}
	}
	trashErrorHint := func(err error, stderr string) string {
		var parts []string
		if err != nil {
			parts = append(parts, err.Error())
		}
		if trimmed := strings.TrimSpace(stderr); trimmed != "" {
			first := strings.Split(trimmed, "\n")[0]
			parts = append(parts, first)
		}
		if len(parts) == 0 {
			return ""
		}
		hint := "trash: " + strings.Join(parts, " · ")
		if len(hint) > 200 {
			hint = hint[:200]
		}
		return hint
	}

	command := exec.Command("trash", trashArgs...)
	output, err := command.CombinedOutput()
	_, statErr := os.Stat(sessionPath)
	if err == nil || os.IsNotExist(statErr) {
		return SessionDeleteResult{OK: true, Method: "trash"}
	}
	stderr := string(output)

	if removeErr := os.Remove(sessionPath); removeErr != nil {
		message := removeErr.Error()
		if hint := trashErrorHint(err, stderr); hint != "" {
			message = message + " (" + hint + ")"
		}
		return SessionDeleteResult{OK: false, Method: "unlink", Error: message}
	}
	return SessionDeleteResult{OK: true, Method: "unlink"}
}

func shortenSessionPath(path string) string {
	homeDir, _ := os.UserHomeDir()
	if path == "" {
		return path
	}
	if homeDir != "" && strings.HasPrefix(path, homeDir) {
		return "~" + path[len(homeDir):]
	}
	return path
}

func formatSessionDate(date time.Time, now time.Time) string {
	diffMs := now.Sub(date).Milliseconds()
	diffMins := diffMs / 60000
	diffHours := diffMs / 3600000
	diffDays := diffMs / 86400000

	switch {
	case diffMins < 1:
		return "now"
	case diffMins < 60:
		return itoa(int(diffMins)) + "m"
	case diffHours < 24:
		return itoa(int(diffHours)) + "h"
	case diffDays < 7:
		return itoa(int(diffDays)) + "d"
	case diffDays < 30:
		return itoa(int(diffDays/7)) + "w"
	case diffDays < 365:
		return itoa(int(diffDays/30)) + "mo"
	default:
		return itoa(int(diffDays/365)) + "y"
	}
}

func canonicalizeOptionalPath(path string) string {
	if path == "" {
		return path
	}
	return coding.CanonicalizePath(path)
}

type sessionStatusMessage struct {
	Type    string // "info" | "error"
	Message string
}

// SessionSelectorHeader renders the selector title, scope and hints.
type SessionSelectorHeader struct {
	scope      SessionScope
	sortMode   SortMode
	nameFilter NameFilter

	requestRender func()

	loading              bool
	loadProgress         *[2]int
	showPath             bool
	confirmingDeletePath *string
	statusMessage        *sessionStatusMessage
	showRenameHint       bool

	clearStatus func()
	schedule    func(ms int, fn func())
}

// NewSessionSelectorHeader creates the header.
func NewSessionSelectorHeader(scope SessionScope, sortMode SortMode, nameFilter NameFilter, requestRender func()) *SessionSelectorHeader {
	return &SessionSelectorHeader{
		scope: scope, sortMode: sortMode, nameFilter: nameFilter, requestRender: requestRender,
		schedule: func(ms int, fn func()) { time.AfterFunc(time.Duration(ms)*time.Millisecond, fn) },
	}
}

// SetScope updates the scope.
func (h *SessionSelectorHeader) SetScope(scope SessionScope) { h.scope = scope }

// SetSortMode updates the sort mode.
func (h *SessionSelectorHeader) SetSortMode(sortMode SortMode) { h.sortMode = sortMode }

// SetNameFilter updates the name filter.
func (h *SessionSelectorHeader) SetNameFilter(nameFilter NameFilter) { h.nameFilter = nameFilter }

// SetLoading updates the loading state and clears the progress.
func (h *SessionSelectorHeader) SetLoading(loading bool) {
	h.loading = loading
	h.loadProgress = nil
}

// SetProgress updates the load progress.
func (h *SessionSelectorHeader) SetProgress(loaded int, total int) {
	h.loadProgress = &[2]int{loaded, total}
}

// SetShowPath toggles the path display.
func (h *SessionSelectorHeader) SetShowPath(showPath bool) { h.showPath = showPath }

// SetShowRenameHint toggles the rename hint.
func (h *SessionSelectorHeader) SetShowRenameHint(show bool) { h.showRenameHint = show }

// SetConfirmingDeletePath sets the delete-confirmation path.
func (h *SessionSelectorHeader) SetConfirmingDeletePath(path *string) {
	h.confirmingDeletePath = path
}

func (h *SessionSelectorHeader) clearStatusTimeout() {
	if h.clearStatus != nil {
		h.clearStatus()
		h.clearStatus = nil
	}
}

// SetStatusMessage sets (or clears) the status message.
func (h *SessionSelectorHeader) SetStatusMessage(message *sessionStatusMessage, autoHideMs int) {
	h.clearStatusTimeout()
	h.statusMessage = message
	if message == nil || autoHideMs == 0 {
		return
	}
	h.clearStatus = scheduleOnce(h.schedule, autoHideMs, func() {
		h.statusMessage = nil
		h.clearStatus = nil
		if h.requestRender != nil {
			h.requestRender()
		}
	})
}

// scheduleOnce wraps a scheduler and returns a cancel function. The timer may
// fire on another goroutine, so the cancel flag is atomic (stage 4: no lock).
func scheduleOnce(schedule func(ms int, fn func()), ms int, fn func()) func() {
	var canceled atomic.Bool
	schedule(ms, func() {
		if canceled.Load() {
			return
		}
		fn()
	})
	return func() { canceled.Store(true) }
}

// Invalidate is a no-op.
func (h *SessionSelectorHeader) Invalidate() {}

// Render renders the header.
func (h *SessionSelectorHeader) Render(width int) []string {
	theme := ActiveTheme()
	titleText := "Resume Session (Current Folder)"
	if h.scope == SessionScopeAll {
		titleText = "Resume Session (All)"
	}
	leftText := theme.Bold(titleText)

	sortLabel := "Fuzzy"
	switch h.sortMode {
	case SortThreaded:
		sortLabel = "Threaded"
	case SortRecent:
		sortLabel = "Recent"
	}
	sortText := theme.Fg("muted", "Sort: ") + theme.Fg("accent", sortLabel)

	nameLabel := "All"
	if h.nameFilter == NameFilterNamed {
		nameLabel = "Named"
	}
	nameText := theme.Fg("muted", "Name: ") + theme.Fg("accent", nameLabel)

	var scopeText string
	switch {
	case h.loading:
		progressText := "..."
		if h.loadProgress != nil {
			progressText = itoa(h.loadProgress[0]) + "/" + itoa(h.loadProgress[1])
		}
		scopeText = theme.Fg("muted", "○ Current Folder | ") + theme.Fg("accent", "Loading "+progressText)
	case h.scope == SessionScopeCurrent:
		scopeText = theme.Fg("accent", "◉ Current Folder") + theme.Fg("muted", " | ○ All")
	default:
		scopeText = theme.Fg("muted", "○ Current Folder | ") + theme.Fg("accent", "◉ All")
	}

	rightText := tui.TruncateToWidth(scopeText+"  "+nameText+"  "+sortText, width, "", false)
	availableLeft := max(0, width-tui.VisibleWidth(rightText)-1)
	left := tui.TruncateToWidth(leftText, availableLeft, "", false)
	spacing := max(0, width-tui.VisibleWidth(left)-tui.VisibleWidth(rightText))

	var hintLine1, hintLine2 string
	switch {
	case h.confirmingDeletePath != nil:
		confirmHint := "Delete session? " + KeyHint("tui.select.confirm", "confirm") + " · " + KeyHint("tui.select.cancel", "cancel")
		hintLine1 = theme.Fg("error", tui.TruncateToWidth(confirmHint, width, "…", false))
	case h.statusMessage != nil:
		color := "accent"
		if h.statusMessage.Type == "error" {
			color = "error"
		}
		hintLine1 = theme.Fg(color, tui.TruncateToWidth(h.statusMessage.Message, width, "…", false))
	default:
		pathState := "(off)"
		if h.showPath {
			pathState = "(on)"
		}
		sep := theme.Fg("muted", " · ")
		hint1 := KeyHint("tui.input.tab", "scope") + sep + theme.Fg("muted", `re:<pattern> regex · "phrase" exact`)
		hint2Parts := []string{
			KeyHint("app.session.toggleSort", "sort"),
			KeyHint("app.session.toggleNamedFilter", "named"),
			KeyHint("app.session.delete", "delete"),
			KeyHint("app.session.togglePath", "path "+pathState),
		}
		if h.showRenameHint {
			hint2Parts = append(hint2Parts, KeyHint("app.session.rename", "rename"))
		}
		hintLine1 = tui.TruncateToWidth(hint1, width, "…", false)
		hintLine2 = tui.TruncateToWidth(strings.Join(hint2Parts, sep), width, "…", false)
	}

	return []string{left + strings.Repeat(" ", spacing) + rightText, hintLine1, hintLine2}
}

// sessionTreeNode is a session tree node.
type sessionTreeNode struct {
	Session        coding.SessionInfo
	Children       []*sessionTreeNode
	LatestActivity int64
}

// flatSessionNode is a flattened tree node with the tree metadata.
type flatSessionNode struct {
	Session           coding.SessionInfo
	Depth             int
	IsLast            bool
	AncestorContinues []bool
}

// buildSessionTree builds a tree from the sessions' parent paths.
func buildSessionTree(sessions []coding.SessionInfo) []*sessionTreeNode {
	byPath := map[string]*sessionTreeNode{}
	for _, session := range sessions {
		sessionPath := canonicalizeOptionalPath(session.Path)
		byPath[sessionPath] = &sessionTreeNode{Session: session, LatestActivity: session.Modified.UnixMilli()}
	}

	var roots []*sessionTreeNode
	for _, session := range sessions {
		sessionPath := canonicalizeOptionalPath(session.Path)
		node := byPath[sessionPath]
		parentPath := canonicalizeOptionalPath(session.ParentSessionPath)
		if parentPath != "" {
			if parent, ok := byPath[parentPath]; ok {
				parent.Children = append(parent.Children, node)
				continue
			}
		}
		roots = append(roots, node)
	}

	var updateLatestActivity func(node *sessionTreeNode) int64
	updateLatestActivity = func(node *sessionTreeNode) int64 {
		latest := node.Session.Modified.UnixMilli()
		for _, child := range node.Children {
			if childLatest := updateLatestActivity(child); childLatest > latest {
				latest = childLatest
			}
		}
		node.LatestActivity = latest
		return latest
	}
	for _, root := range roots {
		updateLatestActivity(root)
	}

	var sortNodes func(nodes []*sessionTreeNode)
	sortNodes = func(nodes []*sessionTreeNode) {
		sortNodesByActivity(nodes)
		for _, node := range nodes {
			sortNodes(node.Children)
		}
	}
	sortNodes(roots)
	return roots
}

func sortNodesByActivity(nodes []*sessionTreeNode) {
	for i := 1; i < len(nodes); i++ {
		for j := i; j > 0 && nodes[j-1].LatestActivity < nodes[j].LatestActivity; j-- {
			nodes[j-1], nodes[j] = nodes[j], nodes[j-1]
		}
	}
}

// flattenSessionTree flattens the tree with tree-drawing metadata.
func flattenSessionTree(roots []*sessionTreeNode) []flatSessionNode {
	var result []flatSessionNode
	var walk func(node *sessionTreeNode, depth int, ancestorContinues []bool, isLast bool)
	walk = func(node *sessionTreeNode, depth int, ancestorContinues []bool, isLast bool) {
		result = append(result, flatSessionNode{
			Session: node.Session, Depth: depth, IsLast: isLast, AncestorContinues: ancestorContinues,
		})
		for index, child := range node.Children {
			childIsLast := index == len(node.Children)-1
			continues := depth > 0 && !isLast
			nextAncestors := append(append([]bool{}, ancestorContinues...), continues)
			walk(child, depth+1, nextAncestors, childIsLast)
		}
	}
	for index, root := range roots {
		walk(root, 0, nil, index == len(roots)-1)
	}
	return result
}

// SessionList is the focusable session list.
type SessionList struct {
	allSessions                 []coding.SessionInfo
	filtered                    []flatSessionNode
	selectedIndex               int
	selectionTouched            bool
	searchInput                 *tui.Input
	showCwd                     bool
	sortMode                    SortMode
	nameFilter                  NameFilter
	keybindings                 *tui.KeybindingsManager
	showPath                    bool
	confirmingDeletePath        *string
	currentSessionCanonicalPath string

	OnSelect                   func(sessionPath string)
	OnCancel                   func()
	OnExit                     func()
	OnToggleScope              func()
	OnToggleSort               func()
	OnToggleNameFilter         func()
	OnTogglePath               func(showPath bool)
	OnDeleteConfirmationChange func(path *string)
	OnDeleteSession            func(sessionPath string)
	OnRenameSession            func(sessionPath string)
	OnError                    func(message string)

	maxVisible int

	now func() time.Time

	focused bool
}

// NewSessionList creates the session list.
func NewSessionList(sessions []coding.SessionInfo, showCwd bool, sortMode SortMode, nameFilter NameFilter, keybindings *tui.KeybindingsManager, currentSessionFilePath string) *SessionList {
	list := &SessionList{
		allSessions:                 sessions,
		searchInput:                 tui.NewInput(tui.InputOptions{}),
		showCwd:                     showCwd,
		sortMode:                    sortMode,
		nameFilter:                  nameFilter,
		keybindings:                 keybindings,
		currentSessionCanonicalPath: canonicalizeOptionalPath(currentSessionFilePath),
		maxVisible:                  10,
		now:                         time.Now,
	}
	list.searchInput.OnSubmit = func(string) {
		if list.selectedIndex >= 0 && list.selectedIndex < len(list.filtered) {
			if list.OnSelect != nil {
				list.OnSelect(list.filtered[list.selectedIndex].Session.Path)
			}
		}
	}
	list.FilterSessions("")
	return list
}

// GetSelectedSessionPath returns the selected session path.
func (l *SessionList) GetSelectedSessionPath() string {
	if l.selectedIndex < 0 || l.selectedIndex >= len(l.filtered) {
		return ""
	}
	return l.filtered[l.selectedIndex].Session.Path
}

// SetFocused focuses the list and its search input.
func (l *SessionList) SetFocused(focused bool) {
	l.focused = focused
	l.searchInput.SetFocused(focused)
}

// Focused reports the focus state.
func (l *SessionList) Focused() bool { return l.focused }

// SetSortMode updates the sort mode.
func (l *SessionList) SetSortMode(sortMode SortMode) {
	l.sortMode = sortMode
	l.FilterSessions(l.searchInput.Value())
}

// SetNameFilter updates the name filter.
func (l *SessionList) SetNameFilter(nameFilter NameFilter) {
	l.nameFilter = nameFilter
	l.FilterSessions(l.searchInput.Value())
}

// SetSessions replaces the sessions and restores the selection.
func (l *SessionList) SetSessions(sessions []coding.SessionInfo, showCwd bool) {
	selectedPath := ""
	if l.selectionTouched {
		selectedPath = l.GetSelectedSessionPath()
	}
	l.allSessions = sessions
	l.showCwd = showCwd
	l.FilterSessions(l.searchInput.Value())
	if !l.selectionTouched {
		l.selectedIndex = 0
	} else if selectedPath != "" {
		for index, node := range l.filtered {
			if node.Session.Path == selectedPath {
				l.selectedIndex = index
				break
			}
		}
	}
}

// FilterSessions applies the search and sort to the sessions.
func (l *SessionList) FilterSessions(query string) {
	trimmed := strings.TrimSpace(query)
	nameFiltered := l.allSessions
	if l.nameFilter != NameFilterAll {
		nameFiltered = nil
		for _, session := range l.allSessions {
			if HasSessionName(session) {
				nameFiltered = append(nameFiltered, session)
			}
		}
	}

	if l.sortMode == SortThreaded && trimmed == "" {
		roots := buildSessionTree(nameFiltered)
		l.filtered = flattenSessionTree(roots)
	} else {
		filtered := FilterAndSortSessions(nameFiltered, query, l.sortMode, NameFilterAll)
		l.filtered = make([]flatSessionNode, 0, len(filtered))
		for _, session := range filtered {
			l.filtered = append(l.filtered, flatSessionNode{Session: session, Depth: 0, IsLast: true})
		}
	}
	if l.selectedIndex > len(l.filtered)-1 {
		l.selectedIndex = max(0, len(l.filtered)-1)
	}
}

// SelectedIndex returns the selection index.
func (l *SessionList) SelectedIndex() int { return l.selectedIndex }

// SearchInput exposes the search input.
func (l *SessionList) SearchInput() *tui.Input { return l.searchInput }

func (l *SessionList) setConfirmingDeletePath(path *string) {
	l.confirmingDeletePath = path
	if l.OnDeleteConfirmationChange != nil {
		l.OnDeleteConfirmationChange(path)
	}
}

func (l *SessionList) startDeleteConfirmationForSelectedSession() {
	if l.selectedIndex < 0 || l.selectedIndex >= len(l.filtered) {
		return
	}
	selected := l.filtered[l.selectedIndex]
	if l.isCurrentSessionPath(selected.Session.Path) {
		if l.OnError != nil {
			l.OnError("Cannot delete the currently active session")
		}
		return
	}
	path := selected.Session.Path
	l.setConfirmingDeletePath(&path)
}

func (l *SessionList) isCurrentSessionPath(path string) bool {
	if l.currentSessionCanonicalPath == "" {
		return false
	}
	return canonicalizeOptionalPath(path) == l.currentSessionCanonicalPath
}

// Invalidate is a no-op.
func (l *SessionList) Invalidate() {}

// Render renders the list.
func (l *SessionList) Render(width int) []string {
	theme := ActiveTheme()
	var lines []string
	lines = append(lines, l.searchInput.Render(width)...)
	lines = append(lines, "")

	if len(l.filtered) == 0 {
		var emptyMessage string
		switch {
		case l.nameFilter == NameFilterNamed:
			toggleKey := KeyText("app.session.toggleNamedFilter")
			if l.showCwd {
				emptyMessage = "  No named sessions found. Press " + toggleKey + " to show all."
			} else {
				emptyMessage = "  No named sessions in current folder. Press " + toggleKey + " to show all, or Tab to view all."
			}
		case l.showCwd:
			emptyMessage = "  No sessions found"
		default:
			emptyMessage = "  No sessions in current folder. Press Tab to view all."
		}
		lines = append(lines, theme.Fg("muted", tui.TruncateToWidth(emptyMessage, width, "…", false)))
		return lines
	}

	startIndex := max(0, min(l.selectedIndex-l.maxVisible/2, len(l.filtered)-l.maxVisible))
	endIndex := min(startIndex+l.maxVisible, len(l.filtered))

	now := l.now()
	for i := startIndex; i < endIndex; i++ {
		node := l.filtered[i]
		session := node.Session
		isSelected := i == l.selectedIndex
		isConfirmingDelete := l.confirmingDeletePath != nil && session.Path == *l.confirmingDeletePath
		isCurrent := l.isCurrentSessionPath(session.Path)

		prefix := l.buildTreePrefix(node)

		hasName := session.Name != ""
		displayText := session.Name
		if !hasName {
			displayText = session.FirstMessage
		}
		normalizedMessage := strings.TrimSpace(stripControlCharacters(displayText))

		age := formatSessionDate(session.Modified, now)
		msgCount := itoa(session.MessageCount)
		rightPart := msgCount + " " + age
		if l.showCwd && session.Cwd != "" {
			rightPart = shortenSessionPath(session.Cwd) + " " + rightPart
		}
		if l.showPath {
			rightPart = shortenSessionPath(session.Path) + " " + rightPart
		}

		cursor := "  "
		if isSelected {
			cursor = theme.Fg("accent", "› ")
		}

		prefixWidth := tui.VisibleWidth(prefix)
		rightWidth := tui.VisibleWidth(rightPart) + 2
		availableForMsg := width - 2 - prefixWidth - rightWidth

		truncatedMsg := tui.TruncateToWidth(normalizedMessage, max(10, availableForMsg), "…", false)

		messageColor := ""
		switch {
		case isConfirmingDelete:
			messageColor = "error"
		case isCurrent:
			messageColor = "accent"
		case hasName:
			messageColor = "warning"
		}
		styledMsg := truncatedMsg
		if messageColor != "" {
			styledMsg = theme.Fg(messageColor, truncatedMsg)
		}
		if isSelected {
			styledMsg = theme.Bold(styledMsg)
		}

		leftPart := cursor + theme.Fg("dim", prefix) + styledMsg
		leftWidth := tui.VisibleWidth(leftPart)
		spacing := max(1, width-leftWidth-tui.VisibleWidth(rightPart))
		rightColor := "dim"
		if isConfirmingDelete {
			rightColor = "error"
		}
		styledRight := theme.Fg(rightColor, rightPart)

		line := leftPart + strings.Repeat(" ", spacing) + styledRight
		if isSelected {
			line = theme.Bg("selectedBg", line)
		}
		lines = append(lines, tui.TruncateToWidth(line, width, "", false))
	}

	if startIndex > 0 || endIndex < len(l.filtered) {
		scrollText := "  (" + itoa(l.selectedIndex+1) + "/" + itoa(len(l.filtered)) + ")"
		lines = append(lines, theme.Fg("muted", tui.TruncateToWidth(scrollText, width, "", false)))
	}
	return lines
}

// stripControlCharacters replaces control characters with spaces.
func stripControlCharacters(text string) string {
	var builder strings.Builder
	for _, r := range text {
		if r < 0x20 || r == 0x7f {
			builder.WriteRune(' ')
			continue
		}
		builder.WriteRune(r)
	}
	return builder.String()
}

func (l *SessionList) buildTreePrefix(node flatSessionNode) string {
	if node.Depth == 0 {
		return ""
	}
	var builder strings.Builder
	for _, continues := range node.AncestorContinues {
		if continues {
			builder.WriteString("│  ")
		} else {
			builder.WriteString("   ")
		}
	}
	if node.IsLast {
		builder.WriteString("└─ ")
	} else {
		builder.WriteString("├─ ")
	}
	return builder.String()
}

// HandleInput processes the list input.
func (l *SessionList) HandleInput(keyData string) {
	println("LIST-INPUT", len(keyData), len(l.filtered))
	kb := tui.GetKeybindings()

	if l.confirmingDeletePath != nil {
		switch {
		case kb.Matches(keyData, "tui.select.confirm"):
			pathToDelete := *l.confirmingDeletePath
			l.setConfirmingDeletePath(nil)
			if l.OnDeleteSession != nil {
				l.OnDeleteSession(pathToDelete)
			}
		case kb.Matches(keyData, "tui.select.cancel"):
			l.setConfirmingDeletePath(nil)
		}
		return
	}

	switch {
	case kb.Matches(keyData, "tui.input.tab"):
		if l.OnToggleScope != nil {
			l.OnToggleScope()
		}
		return
	case kb.Matches(keyData, "app.session.toggleSort"):
		if l.OnToggleSort != nil {
			l.OnToggleSort()
		}
		return
	case l.keybindings != nil && l.keybindings.Matches(keyData, "app.session.toggleNamedFilter"):
		if l.OnToggleNameFilter != nil {
			l.OnToggleNameFilter()
		}
		return
	case kb.Matches(keyData, "app.session.togglePath"):
		l.showPath = !l.showPath
		if l.OnTogglePath != nil {
			l.OnTogglePath(l.showPath)
		}
		return
	case kb.Matches(keyData, "app.session.delete"):
		l.startDeleteConfirmationForSelectedSession()
		return
	case kb.Matches(keyData, "app.session.rename"):
		if l.selectedIndex >= 0 && l.selectedIndex < len(l.filtered) && l.OnRenameSession != nil {
			l.OnRenameSession(l.filtered[l.selectedIndex].Session.Path)
		}
		return
	case kb.Matches(keyData, "app.session.deleteNoninvasive"):
		if len(l.searchInput.Value()) > 0 {
			l.searchInput.HandleInput(keyData)
			l.FilterSessions(l.searchInput.Value())
			return
		}
		l.startDeleteConfirmationForSelectedSession()
		return
	}

	l.selectionTouched = true
	switch {
	case kb.Matches(keyData, "tui.select.up"):
		l.selectedIndex = max(0, l.selectedIndex-1)
	case kb.Matches(keyData, "tui.select.down"):
		l.selectedIndex = min(len(l.filtered)-1, l.selectedIndex+1)
	case kb.Matches(keyData, "tui.select.pageUp"):
		l.selectedIndex = max(0, l.selectedIndex-l.maxVisible)
	case kb.Matches(keyData, "tui.select.pageDown"):
		l.selectedIndex = min(len(l.filtered)-1, l.selectedIndex+l.maxVisible)
	case kb.Matches(keyData, "tui.select.confirm"):
		if l.selectedIndex >= 0 && l.selectedIndex < len(l.filtered) && l.OnSelect != nil {
			l.OnSelect(l.filtered[l.selectedIndex].Session.Path)
		}
	case kb.Matches(keyData, "tui.select.cancel"):
		if l.OnCancel != nil {
			l.OnCancel()
		}
	default:
		l.searchInput.HandleInput(keyData)
		l.FilterSessions(l.searchInput.Value())
	}
}

// SessionSelectorOptions configure the selector.
type SessionSelectorOptions struct {
	// Post marshals background loader results onto the UI loop. It must be set
	// at construction: the initial load starts inside the constructor.
	Post                   func(fn func())
	RenameSession          func(sessionPath string, currentName string) error
	ShowRenameHint         bool
	Keybindings            *tui.KeybindingsManager
	CurrentSessionFilePath string
	// Now overrides the clock (test seam).
	Now func() time.Time
	// ScheduleTimer overrides the status-message timer (test seam, D101).
	ScheduleTimer func(ms int, fn func())
	// DeleteSession overrides the session file deleter (test seam, D102).
	DeleteSession func(sessionPath string) SessionDeleteResult
}

// SessionSelectorComponent is the resume session selector.
type SessionSelectorComponent struct {
	*tui.Container

	sessionList *SessionList
	header      *SessionSelectorHeader
	keybindings *tui.KeybindingsManager
	scope       SessionScope
	sortMode    SortMode
	nameFilter  NameFilter

	currentSessions []coding.SessionInfo
	allSessions     []coding.SessionInfo

	currentSessionsLoader SessionsLoader
	allSessionsLoader     SessionsLoader
	requestRender         func()

	renameSession func(sessionPath string, currentName string) error
	canRename     bool

	currentLoad context.CancelFunc
	allLoad     context.CancelFunc
	loads       sync.WaitGroup

	// Post marshals background loader results onto the UI loop (stage 4).
	// Nil applies inline (tests without a loop).
	Post func(fn func())

	mode             string // "list" | "rename"
	renameInput      *tui.Input
	renameTargetPath string

	onSelect func(sessionPath string)
	onCancel func()
	onExit   func()

	now           func() time.Time
	scheduleTimer func(ms int, fn func())
	deleteSession func(sessionPath string) SessionDeleteResult

	focused bool
}

// NewSessionSelectorComponent creates the selector.
func NewSessionSelectorComponent(currentSessionsLoader SessionsLoader, allSessionsLoader SessionsLoader, onSelect func(string), onCancel func(), onExit func(), requestRender func(), options SessionSelectorOptions) *SessionSelectorComponent {
	keybindings := options.Keybindings
	if keybindings == nil {
		keybindings = tui.GetKeybindings()
	}
	component := &SessionSelectorComponent{
		Container:             &tui.Container{},
		Post:                  options.Post,
		keybindings:           keybindings,
		currentSessionsLoader: currentSessionsLoader,
		allSessionsLoader:     allSessionsLoader,
		requestRender:         requestRender,
		scope:                 SessionScopeCurrent,
		sortMode:              SortThreaded,
		nameFilter:            NameFilterAll,
		mode:                  "list",
		renameInput:           tui.NewInput(tui.InputOptions{}),
		onSelect:              onSelect,
		onCancel:              onCancel,
		onExit:                onExit,
		renameSession:         options.RenameSession,
		now:                   time.Now,
		scheduleTimer:         func(ms int, fn func()) { time.AfterFunc(time.Duration(ms)*time.Millisecond, fn) },
		deleteSession:         sessionFileDeleter,
	}
	if options.Now != nil {
		component.now = options.Now
	}
	if options.ScheduleTimer != nil {
		component.scheduleTimer = options.ScheduleTimer
	}
	if options.DeleteSession != nil {
		component.deleteSession = options.DeleteSession
	}
	component.canRename = options.RenameSession != nil

	component.header = NewSessionSelectorHeader(component.scope, component.sortMode, component.nameFilter, requestRender)
	component.header.schedule = component.scheduleTimer
	component.header.SetShowRenameHint(options.ShowRenameHint || component.canRename)

	component.sessionList = NewSessionList(nil, false, component.sortMode, component.nameFilter, keybindings, options.CurrentSessionFilePath)
	component.sessionList.now = component.now
	component.buildBaseLayout(component.sessionList, true)

	component.renameInput.OnSubmit = func(value string) { component.confirmRename(value) }

	component.sessionList.OnSelect = func(sessionPath string) {
		component.header.SetStatusMessage(nil, 0)
		component.cancelLoads()
		if component.onSelect != nil {
			component.onSelect(sessionPath)
		}
	}
	component.sessionList.OnCancel = func() {
		component.header.SetStatusMessage(nil, 0)
		component.cancelLoads()
		if component.onCancel != nil {
			component.onCancel()
		}
	}
	component.sessionList.OnExit = func() {
		component.header.SetStatusMessage(nil, 0)
		component.cancelLoads()
		if component.onExit != nil {
			component.onExit()
		}
	}
	component.sessionList.OnToggleScope = component.toggleScope
	component.sessionList.OnToggleSort = component.toggleSortMode
	component.sessionList.OnToggleNameFilter = component.toggleNameFilter
	component.sessionList.OnRenameSession = func(sessionPath string) {
		if component.renameSession == nil {
			return
		}
		loading := component.currentLoad != nil
		if component.scope == SessionScopeAll {
			loading = component.allLoad != nil
		}
		sessions := component.currentSessions
		if component.scope == SessionScopeAll {
			sessions = component.allSessions
		}
		if loading {
			return
		}
		currentName := ""
		for _, session := range sessions {
			if session.Path == sessionPath {
				currentName = session.Name
				break
			}
		}
		component.enterRenameMode(sessionPath, currentName)
	}
	component.sessionList.OnTogglePath = func(showPath bool) {
		component.header.SetShowPath(showPath)
		component.requestRenderNow()
	}
	component.sessionList.OnDeleteConfirmationChange = func(path *string) {
		component.header.SetConfirmingDeletePath(path)
		component.requestRenderNow()
	}
	component.sessionList.OnError = func(message string) {
		component.header.SetStatusMessage(&sessionStatusMessage{Type: "error", Message: message}, 3000)
		component.requestRenderNow()
	}
	component.sessionList.OnDeleteSession = func(sessionPath string) {
		component.deleteSessionAndRefresh(sessionPath)
	}

	component.loadScope(SessionScopeCurrent)
	return component
}

// Render drains queued load results (under the renderer's lock, serialized
// with input handling) before rendering.
func (c *SessionSelectorComponent) Render(width int) []string {
	return c.Container.Render(width)
}

// requestRenderNow schedules a render.
func (c *SessionSelectorComponent) requestRenderNow() {
	if c.requestRender != nil {
		c.requestRender()
	}
}

// WaitForPendingLoads blocks until the in-flight loaders have finished (test
// seam, D103). Loader results apply inline when no Post sink is installed.
func (c *SessionSelectorComponent) WaitForPendingLoads() {
	c.loads.Wait()
}

func (c *SessionSelectorComponent) buildBaseLayout(content tui.Component, showHeader bool) {
	c.Container.Clear()
	c.AddChild(tui.NewSpacer(1))
	c.AddChild(NewDynamicBorder(func(text string) string { return ActiveTheme().Fg("accent", text) }))
	c.AddChild(tui.NewSpacer(1))
	if showHeader {
		c.AddChild(c.header)
		c.AddChild(tui.NewSpacer(1))
	}
	c.AddChild(content)
	c.AddChild(tui.NewSpacer(1))
	c.AddChild(NewDynamicBorder(func(text string) string { return ActiveTheme().Fg("accent", text) }))
}

// HandleInput processes the selector input.
func (c *SessionSelectorComponent) HandleInput(data string) {
	if c.mode == "rename" {
		if tui.GetKeybindings().Matches(data, "tui.select.cancel") {
			c.exitRenameMode()
			return
		}
		c.renameInput.HandleInput(data)
		return
	}
	c.sessionList.HandleInput(data)
}

// SetFocused focuses the selector.
func (c *SessionSelectorComponent) SetFocused(focused bool) {
	c.focused = focused
	c.sessionList.SetFocused(focused)
	c.renameInput.SetFocused(focused)
	if focused && c.mode == "rename" {
		c.renameInput.SetFocused(true)
	}
}

// Focused reports the focus state.
func (c *SessionSelectorComponent) Focused() bool { return c.focused }

func (c *SessionSelectorComponent) cancelLoads() {
	if c.currentLoad != nil {
		c.currentLoad()
		c.currentLoad = nil
		c.currentSessions = nil
	}
	if c.allLoad != nil {
		c.allLoad()
		c.allLoad = nil
		c.allSessions = nil
	}
}

func (c *SessionSelectorComponent) enterRenameMode(sessionPath string, currentName string) {
	c.mode = "rename"
	c.renameTargetPath = sessionPath
	c.renameInput.SetValue(currentName)
	c.renameInput.SetFocused(true)

	theme := ActiveTheme()
	panel := &tui.Container{}
	panel.AddChild(tui.NewText(theme.Bold("Rename Session"), 1, 0, nil))
	panel.AddChild(tui.NewSpacer(1))
	panel.AddChild(c.renameInput)
	panel.AddChild(tui.NewSpacer(1))
	panel.AddChild(tui.NewText(theme.Fg("muted",
		KeyText("tui.select.confirm")+" to save · "+KeyText("tui.select.cancel")+" to cancel"), 1, 0, nil))

	c.buildBaseLayout(panel, false)
	c.requestRenderNow()
}

func (c *SessionSelectorComponent) exitRenameMode() {
	c.mode = "list"
	c.renameTargetPath = ""
	c.buildBaseLayout(c.sessionList, true)
	c.requestRenderNow()
}

func (c *SessionSelectorComponent) confirmRename(value string) {
	next := strings.TrimSpace(value)
	if next == "" {
		return
	}
	target := c.renameTargetPath
	if target == "" {
		c.exitRenameMode()
		return
	}
	if c.renameSession == nil {
		c.exitRenameMode()
		return
	}
	if err := c.renameSession(target, next); err != nil {
		c.exitRenameMode()
		return
	}
	c.refreshSessionsAfterMutation()
	c.exitRenameMode()
}

func (c *SessionSelectorComponent) deleteSessionAndRefresh(sessionPath string) {
	result := c.deleteSession(sessionPath)
	if result.OK {
		c.currentSessions = filterOutSession(c.currentSessions, sessionPath)
		c.allSessions = filterOutSession(c.allSessions, sessionPath)
		sessions := c.currentSessions
		if c.scope == SessionScopeAll {
			sessions = c.allSessions
		}

		c.sessionList.SetSessions(sessions, c.scope == SessionScopeAll)
		message := "Session deleted"
		if result.Method == "trash" {
			message = "Session moved to trash"
		}
		c.header.SetStatusMessage(&sessionStatusMessage{Type: "info", Message: message}, 2000)
		c.refreshSessionsAfterMutation()
	} else {
		errorMessage := result.Error
		if errorMessage == "" {
			errorMessage = "Unknown error"
		}
		c.header.SetStatusMessage(&sessionStatusMessage{Type: "error", Message: "Failed to delete: " + errorMessage}, 3000)
	}
	c.requestRenderNow()
}

func filterOutSession(sessions []coding.SessionInfo, sessionPath string) []coding.SessionInfo {
	if sessions == nil {
		return nil
	}
	filtered := make([]coding.SessionInfo, 0, len(sessions))
	for _, session := range sessions {
		if session.Path != sessionPath {
			filtered = append(filtered, session)
		}
	}
	return filtered
}

func (c *SessionSelectorComponent) loadScope(scope SessionScope) {
	alreadyLoading := c.currentLoad != nil
	if scope == SessionScopeAll {
		alreadyLoading = c.allLoad != nil
	}
	if alreadyLoading {
		return
	}
	showCwd := scope == SessionScopeAll
	ctx, cancel := context.WithCancel(context.Background())
	if scope == SessionScopeCurrent {
		c.currentLoad = cancel
	} else {
		c.allLoad = cancel
	}

	c.header.SetScope(scope)
	c.header.SetLoading(true)
	c.requestRenderNow()

	c.loads.Add(1)
	go func() {
		defer c.loads.Done()
		// The worker is a pure producer: results are handed to the loop, which
		// owns the selector state (stage 4). Cancellation is observed through
		// the load context instead of shared state.
		onProgress := func(loaded int, total int, partialSessions []coding.SessionInfo) {
			if ctx.Err() != nil {
				return
			}
			if partialSessions != nil {
				sessions := append([]coding.SessionInfo{}, partialSessions...)
				c.postApply(func() {
					if scope == SessionScopeCurrent {
						c.currentSessions = sessions
					} else {
						c.allSessions = sessions
					}
					if scope == c.scope {
						c.sessionList.SetSessions(sessions, showCwd)
					}
				})
			}
			c.postApply(func() {
				if scope != c.scope {
					return
				}
				c.header.SetProgress(loaded, total)
				c.requestRenderNow()
			})
		}

		loader := c.currentSessionsLoader
		if scope == SessionScopeAll {
			loader = c.allSessionsLoader
		}
		sessions, err := loader(onProgress, ctx)
		if ctx.Err() != nil {
			return
		}

		message := ""
		if err != nil {
			message = err.Error()
		}
		c.postApply(func() {
			if scope == SessionScopeCurrent {
				c.currentSessions = sessions
				c.currentLoad = nil
			} else {
				c.allSessions = sessions
				c.allLoad = nil
			}
			if scope != c.scope {
				return
			}
			if message != "" {
				c.header.SetLoading(false)
				c.header.SetStatusMessage(&sessionStatusMessage{Type: "error", Message: "Failed to load sessions: " + message}, 4000)
				c.sessionList.SetSessions(nil, showCwd)
			} else {
				c.header.SetLoading(false)
				c.sessionList.SetSessions(sessions, showCwd)
			}
			c.requestRenderNow()
		})
	}()
}

// postApply runs fn on the UI loop when a Post sink is installed, else inline
// (tests without a loop).
func (c *SessionSelectorComponent) postApply(fn func()) {
	if c.Post != nil {
		c.Post(fn)
		return
	}
	fn()
}

func (c *SessionSelectorComponent) toggleSortMode() {
	switch c.sortMode {
	case SortThreaded:
		c.sortMode = SortRecent
	case SortRecent:
		c.sortMode = SortRelevance
	default:
		c.sortMode = SortThreaded
	}
	c.header.SetSortMode(c.sortMode)
	c.sessionList.SetSortMode(c.sortMode)
	c.requestRenderNow()
}

func (c *SessionSelectorComponent) toggleNameFilter() {
	if c.nameFilter == NameFilterAll {
		c.nameFilter = NameFilterNamed
	} else {
		c.nameFilter = NameFilterAll
	}
	c.header.SetNameFilter(c.nameFilter)
	c.sessionList.SetNameFilter(c.nameFilter)
	c.requestRenderNow()
}

func (c *SessionSelectorComponent) refreshSessionsAfterMutation() {
	c.cancelLoads()
	c.currentSessions = nil
	c.allSessions = nil
	scope := c.scope
	c.loadScope(scope)
}

func (c *SessionSelectorComponent) toggleScope() {
	if c.scope == SessionScopeCurrent {
		c.scope = SessionScopeAll
	} else {
		c.scope = SessionScopeCurrent
	}
	sessions := c.currentSessions
	loading := c.currentLoad != nil
	if c.scope == SessionScopeAll {
		sessions = c.allSessions
		loading = c.allLoad != nil
	}

	c.header.SetScope(c.scope)
	c.header.SetLoading(loading)
	c.sessionList.SetSessions(sessions, c.scope == SessionScopeAll)
	c.requestRenderNow()
	if sessions == nil && !loading {
		c.loadScope(c.scope)
	}
}

// GetSessionList returns the session list.
func (c *SessionSelectorComponent) GetSessionList() *SessionList { return c.sessionList }

// GetHeader returns the header.
func (c *SessionSelectorComponent) GetHeader() *SessionSelectorHeader { return c.header }

// Mode returns the current mode (test helper).
func (c *SessionSelectorComponent) Mode() string { return c.mode }
