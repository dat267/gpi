package tui

import (
	"context"
	"strings"
	"unicode/utf8"
)

// Port of src/components/editor.ts: the multi-line prompt editor with
// word-wrapped layout, cursor navigation across visual lines, paste markers,
// history, the kill ring, undo, and autocomplete integration.
//
// Divergences: upstream's async autocomplete (AbortController + debounce
// timers) becomes a synchronous provider call under the editor lock (D71);
// the state snapshot on undo copies the lines slice explicitly instead of
// structuredClone (D59).

// EditorTheme styles the editor.
type EditorTheme struct {
	BorderColor func(str string) string
	SelectList  SelectListTheme
}

// EditorOptions configure the editor.
type EditorOptions struct {
	PaddingX               int
	AutocompleteMaxVisible int
}

// EditorHost is the TUI surface the editor needs (upstream takes the whole
// TUI: divergence D62).
type EditorHost interface {
	Rows() int
	RequestRender(force bool)
}

const (
	attachmentAutocompleteDebounceMS = 20
)

var (
	defaultAutocompleteTriggerCharacters = []string{"@", "#"}
	slashCommandSelectListLayout         = SelectListLayoutOptions{
		MinPrimaryColumnWidth: 12, HasMin: true,
		MaxPrimaryColumnWidth: 32, HasMax: true,
	}
)

type editorState struct {
	lines      []string
	cursorLine int
	cursorCol  int
}

type editorSnapshot struct {
	state        editorState
	pastes       map[int]string
	pasteCounter int
}

type layoutLine struct {
	text      string
	hasCursor bool
	cursorPos int
}

// Editor is the multi-line input component.
type Editor struct {
	host     EditorHost
	theme    EditorTheme
	paddingX int
	state    editorState
	focused  bool

	lastWidth                  int
	renderedVisibleLineCount   int
	renderedAutocompleteHeight int
	scrollOffset               int

	BorderColor func(str string) string

	autocompleteProvider   AutocompleteProvider
	autocompleteTriggers   []string
	autocompleteList       *SelectList
	autocompleteState      string // "" | "regular" | "force"
	autocompletePrefix     string
	autocompleteMaxVisible int

	pastes       map[int]string
	pasteCounter int

	pasteBuffer string
	isInPaste   bool

	history      []string
	historyIndex int
	historyDraft *editorState

	killRing   KillRing
	lastAction string // "" | "kill" | "yank" | "type-word"

	jumpMode string // "" | "forward" | "backward"

	preferredVisualCol    int
	hasPreferredVisualCol bool
	snappedFromCursorCol  int
	hasSnappedFromCursor  bool

	undoStack UndoStack[editorSnapshot]

	OnSubmit func(text string)
	OnChange func(text string)
	// pendingSubmit defers OnSubmit until after the editor lock is released
	// (D136): the submit handler calls back into the editor (SetText,
	// AddToHistory), which would deadlock a non-reentrant mutex.
	pendingSubmit *string
	// TopBorder overrides the top border rendering (see renderTopBorder).
	TopBorder     func(width int, hiddenLineCount int) string
	DisableSubmit bool
}

// NewEditor creates an editor.
func NewEditor(host EditorHost, theme EditorTheme, options EditorOptions) *Editor {
	paddingX := options.PaddingX
	if paddingX < 0 {
		paddingX = 0
	}
	maxVisible := options.AutocompleteMaxVisible
	if maxVisible < 3 {
		maxVisible = 5
	}
	if maxVisible > 20 {
		maxVisible = 20
	}
	if theme.BorderColor == nil {
		theme.BorderColor = identityStyle
	}
	editor := &Editor{
		host:                     host,
		theme:                    theme,
		paddingX:                 paddingX,
		state:                    editorState{lines: []string{""}},
		lastWidth:                80,
		renderedVisibleLineCount: 1,
		BorderColor:              theme.BorderColor,
		pastes:                   map[int]string{},
		historyIndex:             -1,
		autocompleteTriggers:     append([]string(nil), defaultAutocompleteTriggerCharacters...),
		autocompleteMaxVisible:   maxVisible,
		preferredVisualCol:       -1,
		snappedFromCursorCol:     -1,
	}
	return editor
}

// SetFocused implements Focusable.
func (e *Editor) SetFocused(focused bool) { e.focused = focused }

// IsFocused implements Focusable.
func (e *Editor) IsFocused() bool { return e.focused }

// Invalidate drops cached state (none).
func (e *Editor) Invalidate() {}

// PaddingX returns the horizontal padding.
func (e *Editor) PaddingX() int {
	return e.paddingX
}

// SetPaddingX updates the horizontal padding.
func (e *Editor) SetPaddingX(padding int) {
	newPadding := padding
	if newPadding < 0 {
		newPadding = 0
	}
	changed := e.paddingX != newPadding
	e.paddingX = newPadding
	if changed {
		e.requestRender()
	}
}

// AutocompleteMaxVisible returns the visible suggestion count.
func (e *Editor) AutocompleteMaxVisible() int {
	return e.autocompleteMaxVisible
}

// SetAutocompleteMaxVisible updates the visible suggestion count.
func (e *Editor) SetAutocompleteMaxVisible(maxVisible int) {
	clamped := maxVisible
	if clamped < 3 {
		clamped = 3
	}
	if clamped > 20 {
		clamped = 20
	}
	changed := e.autocompleteMaxVisible != clamped
	e.autocompleteMaxVisible = clamped
	if changed {
		e.requestRender()
	}
}

// SetAutocompleteProvider installs the completion provider.
func (e *Editor) SetAutocompleteProvider(provider AutocompleteProvider) {
	e.cancelAutocompleteLocked()
	e.autocompleteProvider = provider
	if provider != nil {
		e.setAutocompleteTriggerCharactersLocked(provider.TriggerCharacters())
	}
}

func (e *Editor) requestRender() {
	if e.host != nil {
		e.host.RequestRender(false)
	}
}

// AddToHistory records a submitted prompt for up/down navigation.
func (e *Editor) AddToHistory(text string) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return
	}
	if len(e.history) > 0 && e.history[0] == trimmed {
		return
	}
	e.history = append([]string{trimmed}, e.history...)
	if len(e.history) > 100 {
		e.history = e.history[:100]
	}
}

func (e *Editor) isEditorEmpty() bool {
	return len(e.state.lines) == 1 && e.state.lines[0] == ""
}

func (e *Editor) isOnFirstVisualLine() bool {
	visualLines := e.buildVisualLineMap(e.lastWidth)
	return e.findCurrentVisualLine(visualLines) == 0
}

func (e *Editor) isOnLastVisualLine() bool {
	visualLines := e.buildVisualLineMap(e.lastWidth)
	return e.findCurrentVisualLine(visualLines) == len(visualLines)-1
}

func (e *Editor) navigateHistory(direction int) {
	e.lastAction = ""
	if len(e.history) == 0 {
		return
	}
	newIndex := e.historyIndex - direction
	if newIndex < -1 || newIndex >= len(e.history) {
		return
	}
	if e.historyIndex == -1 && newIndex >= 0 {
		e.pushUndoSnapshot()
		draft := e.state
		draft.lines = append([]string(nil), e.state.lines...)
		e.historyDraft = &draft
	}
	e.historyIndex = newIndex

	if e.historyIndex == -1 {
		draft := e.historyDraft
		e.historyDraft = nil
		if draft != nil {
			e.state = *draft
			e.preferredVisualCol = -1
			e.hasPreferredVisualCol = false
			e.snappedFromCursorCol = -1
			e.hasSnappedFromCursor = false
			e.scrollOffset = 0
			if e.OnChange != nil {
				e.OnChange(e.getTextLocked())
			}
		} else {
			e.setTextInternal("", "end")
		}
		return
	}
	placement := "end"
	if direction == -1 {
		placement = "start"
	}
	if e.historyIndex < len(e.history) {
		e.setTextInternal(e.history[e.historyIndex], placement)
	}
}

func (e *Editor) exitHistoryBrowsing() {
	e.historyIndex = -1
	e.historyDraft = nil
}

func (e *Editor) setTextInternal(text string, placement string) {
	lines := strings.Split(text, "\n")
	e.state.lines = lines
	if placement == "start" {
		e.state.cursorLine = 0
	} else {
		e.state.cursorLine = len(e.state.lines) - 1
	}
	if placement == "start" {
		e.setCursorCol(0)
	} else {
		e.setCursorCol(len(e.state.lines[e.state.cursorLine]))
	}
	e.scrollOffset = 0
	if e.OnChange != nil {
		e.OnChange(e.getTextLocked())
	}
}

// TopBorder overrides the top border rendering when set (upstream's protected
// renderTopBorder override, D109). Implementations that want the default
// rendering call DefaultRenderTopBorder.
func (e *Editor) renderTopBorder(width int, hiddenLineCount int) string {
	if e.TopBorder != nil {
		return e.TopBorder(width, hiddenLineCount)
	}
	return e.DefaultRenderTopBorder(width, hiddenLineCount)
}

// DefaultRenderTopBorder is the built-in top border rendering.
func (e *Editor) DefaultRenderTopBorder(width int, hiddenLineCount int) string {
	border := strings.Repeat("─", maxInt(0, width))
	if hiddenLineCount > 0 {
		border = createScrollBorder("↑", hiddenLineCount, width)
	}
	return e.BorderColor(border)
}

func (e *Editor) renderBottomBorder(width int, hiddenLineCount int) string {
	border := strings.Repeat("─", maxInt(0, width))
	if hiddenLineCount > 0 {
		border = createScrollBorder("↓", hiddenLineCount, width)
	}
	return e.BorderColor(border)
}

// Render renders the editor.
func (e *Editor) Render(width int) []string {

	maxPadding := maxInt(0, (width-1)/2)
	paddingX := minInt(e.paddingX, maxPadding)
	contentWidth := maxInt(1, width-paddingX*2)

	layoutWidth := contentWidth
	if paddingX == 0 {
		layoutWidth = maxInt(1, contentWidth-1)
	}

	e.lastWidth = layoutWidth
	layoutLines := e.layoutTextLocked(layoutWidth)

	terminalRows := 25
	if e.host != nil {
		terminalRows = e.host.Rows()
	}
	maxVisibleLines := maxInt(5, terminalRows*3/10)

	cursorLineIndex := 0
	for index, line := range layoutLines {
		if line.hasCursor {
			cursorLineIndex = index
			break
		}
	}

	if cursorLineIndex < e.scrollOffset {
		e.scrollOffset = cursorLineIndex
	} else if cursorLineIndex >= e.scrollOffset+maxVisibleLines {
		e.scrollOffset = cursorLineIndex - maxVisibleLines + 1
	}
	maxScrollOffset := maxInt(0, len(layoutLines)-maxVisibleLines)
	e.scrollOffset = maxInt(0, minInt(e.scrollOffset, maxScrollOffset))

	end := minInt(e.scrollOffset+maxVisibleLines, len(layoutLines))
	visibleLines := layoutLines[e.scrollOffset:end]
	e.renderedVisibleLineCount = len(visibleLines)

	var result []string
	leftPadding := repeatSpaces(paddingX)
	rightPadding := leftPadding

	result = append(result, e.renderTopBorder(width, e.scrollOffset))

	emitCursorMarker := e.focused

	for _, line := range visibleLines {
		displayText := line.text
		lineVisibleWidth := VisibleWidth(line.text)
		cursorInPadding := false

		if line.hasCursor && line.cursorPos >= 0 {
			before := displayText[:line.cursorPos]
			after := displayText[line.cursorPos:]
			marker := ""
			if emitCursorMarker {
				marker = CursorMarker
			}
			if len(after) > 0 {
				segments := e.segmentLocked(after, "grapheme")
				firstGrapheme := ""
				if len(segments) > 0 {
					firstGrapheme = segments[0].Segment
				}
				restAfter := after[len(firstGrapheme):]
				displayText = before + marker + "\x1b[7m" + firstGrapheme + "\x1b[0m" + restAfter
			} else {
				displayText = before + marker + "\x1b[7m \x1b[0m"
				lineVisibleWidth++
				if lineVisibleWidth > contentWidth && paddingX > 0 {
					cursorInPadding = true
				}
			}
		}

		padding := repeatSpaces(maxInt(0, contentWidth-lineVisibleWidth))
		lineRightPadding := rightPadding
		if cursorInPadding {
			lineRightPadding = rightPadding[minInt(1, len(rightPadding)):]
		}
		result = append(result, leftPadding+displayText+padding+lineRightPadding)
	}

	linesBelow := len(layoutLines) - (e.scrollOffset + len(visibleLines))
	result = append(result, e.renderBottomBorder(width, linesBelow))

	e.renderedAutocompleteHeight = 0
	if e.autocompleteState != "" && e.autocompleteList != nil {
		autocompleteResult := e.autocompleteList.Render(contentWidth)
		e.renderedAutocompleteHeight = len(autocompleteResult)
		for _, line := range autocompleteResult {
			linePadding := repeatSpaces(maxInt(0, contentWidth-VisibleWidth(line)))
			result = append(result, leftPadding+line+linePadding+rightPadding)
		}
	}

	return result
}

// HandleMouse handles clicks in the editor and its autocomplete list.
func (e *Editor) HandleMouse(event TuiMouseEvent) *TuiMouseDispatchResult {

	autocompleteStartRow := e.renderedVisibleLineCount + 2
	if e.autocompleteState != "" && e.autocompleteList != nil &&
		event.Y >= autocompleteStartRow && event.Y < autocompleteStartRow+e.renderedAutocompleteHeight {
		maxPadding := maxInt(0, (event.Width-1)/2)
		paddingX := minInt(e.paddingX, maxPadding)
		contentWidth := maxInt(1, event.Width-paddingX*2)
		childEvent := event
		childEvent.X = event.X - paddingX
		childEvent.Y = event.Y - autocompleteStartRow
		childEvent.Width = contentWidth
		childEvent.Height = e.renderedAutocompleteHeight
		// Upstream calls the list's handleMouse directly (editor.ts): the nested
		// dispatch must not stamp target/focus onto the list — the editor's
		// returned focus:true must resolve to the editor (its caller's
		// dispatchMouseEvent stamps focusTarget with the component it was
		// invoked with).
		result := e.autocompleteList.HandleMouse(childEvent)
		if result != nil {
			result.Focus = true
			return result
		}
		return nil
	}

	if event.Type != MouseClick || event.Button != MouseButtonLeft {
		return nil
	}
	if event.Y <= 0 || event.Y > e.renderedVisibleLineCount {
		return &TuiMouseDispatchResult{TuiMouseEventResult: TuiMouseEventResult{Handled: true, Focus: true}}
	}

	visualLines := e.buildVisualLineMap(e.lastWidth)
	visualLineIndex := e.scrollOffset + event.Y - 1
	if visualLineIndex < 0 || visualLineIndex >= len(visualLines) {
		return &TuiMouseDispatchResult{TuiMouseEventResult: TuiMouseEventResult{Handled: true, Focus: true}}
	}
	visualLine := visualLines[visualLineIndex]
	logicalLine := ""
	if visualLine.logicalLine < len(e.state.lines) {
		logicalLine = e.state.lines[visualLine.logicalLine]
	}
	chunkEnd := visualLine.startCol + visualLine.length
	if chunkEnd > len(logicalLine) {
		chunkEnd = len(logicalLine)
	}
	chunk := logicalLine[visualLine.startCol:chunkEnd]

	maxPadding := maxInt(0, (event.Width-1)/2)
	paddingX := minInt(e.paddingX, maxPadding)
	targetColumn := maxInt(0, event.X-paddingX)

	visibleColumn := 0
	targetIndex := len(chunk)
	lastGraphemeIndex := 0
	for _, segment := range e.segmentLocked(chunk, "grapheme") {
		nextColumn := visibleColumn + VisibleWidth(segment.Segment)
		lastGraphemeIndex = segment.Index
		if targetColumn < nextColumn {
			targetIndex = segment.Index
			break
		}
		visibleColumn = nextColumn
	}
	isLastSegment := visualLineIndex == len(visualLines)-1 ||
		visualLines[visualLineIndex+1].logicalLine != visualLine.logicalLine
	if !isLastSegment && targetIndex == len(chunk) && len(chunk) > 0 {
		targetIndex = lastGraphemeIndex
	}

	e.state.cursorLine = visualLine.logicalLine
	e.setCursorCol(visualLine.startCol + targetIndex)
	e.lastAction = ""
	e.exitHistoryBrowsing()
	if e.autocompleteState != "" {
		e.updateAutocompleteLocked()
	}
	return &TuiMouseDispatchResult{TuiMouseEventResult: TuiMouseEventResult{Handled: true, Focus: true}}
}

// HandleInput processes a key/input chunk.
func (e *Editor) HandleInput(data string) {
	e.handleInputLocked(data)
	pending := e.pendingSubmit
	e.pendingSubmit = nil
	// Invoke the submit handler outside the lock (D136).
	if pending != nil && e.OnSubmit != nil {
		e.OnSubmit(*pending)
	}
}

func (e *Editor) handleInputLocked(data string) {
	kb := GetKeybindings()

	if e.jumpMode != "" {
		if kb.Matches(data, "tui.editor.jumpForward") || kb.Matches(data, "tui.editor.jumpBackward") {
			e.jumpMode = ""
			return
		}
		printable, ok := DecodePrintableKey(data)
		if !ok && len(data) > 0 && data[0] >= 32 {
			printable, ok = data, true
		}
		if ok {
			direction := e.jumpMode
			e.jumpMode = ""
			e.jumpToChar(printable, direction)
			return
		}
		e.jumpMode = ""
	}

	if strings.Contains(data, "\x1b[200~") {
		e.isInPaste = true
		e.pasteBuffer = ""
		data = strings.Replace(data, "\x1b[200~", "", 1)
	}

	if e.isInPaste {
		e.pasteBuffer += data
		if endIndex := strings.Index(e.pasteBuffer, "\x1b[201~"); endIndex != -1 {
			pasteContent := e.pasteBuffer[:endIndex]
			if len(pasteContent) > 0 {
				e.handlePasteLocked(pasteContent)
			}
			e.isInPaste = false
			remaining := e.pasteBuffer[endIndex+6:]
			e.pasteBuffer = ""
			if len(remaining) > 0 {
				e.handleInputLocked(remaining)
			}
			return
		}
		return
	}

	// Ctrl+C is handled by the parent.
	if kb.Matches(data, "tui.input.copy") {
		return
	}
	if kb.Matches(data, "tui.editor.undo") {
		e.undoLocked()
		return
	}

	// Autocomplete mode.
	if e.autocompleteState != "" && e.autocompleteList != nil {
		if kb.Matches(data, "tui.select.cancel") {
			e.cancelAutocompleteLocked()
			return
		}
		if kb.Matches(data, "tui.select.up") || kb.Matches(data, "tui.select.down") {
			e.autocompleteList.HandleInput(data)
			return
		}
		if kb.Matches(data, "tui.input.tab") || kb.Matches(data, "tui.select.confirm") {
			selected, ok := e.autocompleteList.GetSelectedItem()
			if ok && e.autocompleteProvider != nil {
				e.pushUndoSnapshot()
				e.lastAction = ""
				result := e.autocompleteProvider.ApplyCompletion(
					e.state.lines, e.state.cursorLine, e.state.cursorCol, autocompleteItemFromSelect(selected), e.autocompletePrefix)
				e.state.lines = result.Lines
				e.state.cursorLine = result.CursorLine
				e.setCursorCol(result.CursorCol)
				if strings.HasPrefix(e.autocompletePrefix, "/") && kb.Matches(data, "tui.select.confirm") {
					e.cancelAutocompleteLocked()
					// Fall through to submit.
				} else {
					e.cancelAutocompleteLocked()
					if e.OnChange != nil {
						e.OnChange(e.getTextLocked())
					}
					return
				}
			} else if kb.Matches(data, "tui.select.confirm") {
				// No selection: fall through to submit.
			} else {
				return
			}
		}
	}

	if kb.Matches(data, "tui.input.tab") && e.autocompleteState == "" {
		e.handleTabCompletionLocked()
		return
	}

	switch {
	case kb.Matches(data, "tui.editor.deleteToLineEnd"):
		e.deleteToEndOfLineLocked()
		return
	case kb.Matches(data, "tui.editor.deleteToLineStart"):
		e.deleteToStartOfLineLocked()
		return
	case kb.Matches(data, "tui.editor.deleteWordBackward"):
		e.deleteWordBackwardsLocked()
		return
	case kb.Matches(data, "tui.editor.deleteWordForward"):
		e.deleteWordForwardLocked()
		return
	case kb.Matches(data, "tui.editor.deleteCharBackward") || MatchesKey(data, "shift+backspace"):
		e.handleBackspaceLocked()
		return
	case kb.Matches(data, "tui.editor.deleteCharForward") || MatchesKey(data, "shift+delete"):
		e.handleForwardDeleteLocked()
		return
	case kb.Matches(data, "tui.editor.yank"):
		e.yankLocked()
		return
	case kb.Matches(data, "tui.editor.yankPop"):
		e.yankPopLocked()
		return
	case kb.Matches(data, "tui.editor.historyPrevious"):
		e.cancelAutocompleteLocked()
		e.navigateHistory(-1)
		return
	case kb.Matches(data, "tui.editor.historyNext"):
		e.cancelAutocompleteLocked()
		e.navigateHistory(1)
		return
	case kb.Matches(data, "tui.editor.cursorLineStart"):
		e.moveToLineStart()
		return
	case kb.Matches(data, "tui.editor.cursorLineEnd"):
		e.moveToLineEnd()
		return
	case kb.Matches(data, "tui.editor.cursorWordLeft"):
		e.moveWordBackwardsLocked()
		return
	case kb.Matches(data, "tui.editor.cursorWordRight"):
		e.moveWordForwardsLocked()
		return
	}

	if kb.Matches(data, "tui.input.newLine") ||
		(len(data) > 1 && data[0] == 10) ||
		data == "\x1b\r" ||
		data == "\x1b[13;2~" ||
		(len(data) > 1 && strings.Contains(data, "\x1b") && strings.Contains(data, "\r")) ||
		data == "\n" {
		if e.shouldSubmitOnBackslashEnter(data, kb) {
			e.handleBackspaceLocked()
			e.submitValueLocked()
			return
		}
		e.addNewLineLocked()
		return
	}

	if kb.Matches(data, "tui.input.submit") {
		if e.DisableSubmit {
			return
		}
		currentLine := e.currentLine()
		if e.state.cursorCol > 0 && e.state.cursorCol <= len(currentLine) && currentLine[e.state.cursorCol-1] == '\\' {
			e.handleBackspaceLocked()
			e.addNewLineLocked()
			return
		}
		e.submitValueLocked()
		return
	}

	switch {
	case kb.Matches(data, "tui.editor.cursorUp"):
		if e.isOnFirstVisualLine() && (e.isEditorEmpty() || e.historyIndex > -1 || e.state.cursorCol == 0) {
			e.navigateHistory(-1)
		} else if e.isOnFirstVisualLine() {
			e.moveToLineStart()
		} else {
			e.moveCursorLocked(-1, 0)
		}
		return
	case kb.Matches(data, "tui.editor.cursorDown"):
		if e.historyIndex > -1 && e.isOnLastVisualLine() {
			e.navigateHistory(1)
		} else if e.isOnLastVisualLine() {
			e.moveToLineEnd()
		} else {
			e.moveCursorLocked(1, 0)
		}
		return
	case kb.Matches(data, "tui.editor.cursorRight"):
		e.moveCursorLocked(0, 1)
		return
	case kb.Matches(data, "tui.editor.cursorLeft"):
		e.moveCursorLocked(0, -1)
		return
	case kb.Matches(data, "tui.editor.pageUp"):
		e.pageScrollLocked(-1)
		return
	case kb.Matches(data, "tui.editor.pageDown"):
		e.pageScrollLocked(1)
		return
	case kb.Matches(data, "tui.editor.jumpForward"):
		e.jumpMode = "forward"
		return
	case kb.Matches(data, "tui.editor.jumpBackward"):
		e.jumpMode = "backward"
		return
	}

	if MatchesKey(data, "shift+space") {
		e.insertCharacterLocked(" ", false)
		return
	}

	if printable, ok := DecodePrintableKey(data); ok {
		e.insertCharacterLocked(printable, false)
		return
	}

	if len(data) > 0 {
		r, _ := utf8.DecodeRuneInString(data)
		if r >= 32 {
			e.insertCharacterLocked(data, false)
		}
	}
}

func (e *Editor) currentLine() string {
	if e.state.cursorLine < 0 || e.state.cursorLine >= len(e.state.lines) {
		return ""
	}
	return e.state.lines[e.state.cursorLine]
}

func (e *Editor) layoutTextLocked(contentWidth int) []layoutLine {
	var layoutLines []layoutLine

	if len(e.state.lines) == 0 || (len(e.state.lines) == 1 && e.state.lines[0] == "") {
		layoutLines = append(layoutLines, layoutLine{text: "", hasCursor: true, cursorPos: 0})
		return layoutLines
	}

	for i := 0; i < len(e.state.lines); i++ {
		line := e.state.lines[i]
		isCurrentLine := i == e.state.cursorLine
		if VisibleWidth(line) <= contentWidth {
			if isCurrentLine {
				layoutLines = append(layoutLines, layoutLine{text: line, hasCursor: true, cursorPos: e.state.cursorCol})
			} else {
				layoutLines = append(layoutLines, layoutLine{text: line})
			}
			continue
		}

		chunks := WordWrapLine(line, contentWidth, e.segmentLocked(line, "grapheme"))
		for chunkIndex, chunk := range chunks {
			isLastChunk := chunkIndex == len(chunks)-1
			hasCursorInChunk := false
			adjustedCursorPos := 0
			if isCurrentLine {
				if isLastChunk {
					hasCursorInChunk = e.state.cursorCol >= chunk.StartIndex
					adjustedCursorPos = e.state.cursorCol - chunk.StartIndex
				} else {
					hasCursorInChunk = e.state.cursorCol >= chunk.StartIndex && e.state.cursorCol < chunk.EndIndex
					if hasCursorInChunk {
						adjustedCursorPos = e.state.cursorCol - chunk.StartIndex
						if adjustedCursorPos > len(chunk.Text) {
							adjustedCursorPos = len(chunk.Text)
						}
					}
				}
			}
			if hasCursorInChunk {
				layoutLines = append(layoutLines, layoutLine{text: chunk.Text, hasCursor: true, cursorPos: adjustedCursorPos})
			} else {
				layoutLines = append(layoutLines, layoutLine{text: chunk.Text})
			}
		}
	}
	return layoutLines
}

func (e *Editor) getTextLocked() string { return strings.Join(e.state.lines, "\n") }

// GetText returns the editor text (with paste markers).
func (e *Editor) GetText() string {
	return e.getTextLocked()
}

func (e *Editor) expandPasteMarkersLocked(text string) string {
	result := text
	for pasteID, pasteContent := range e.pastes {
		markerRegex := mustCompileRegex(`\[paste #` + itoa(pasteID) + `( (\+\d+ lines|\d+ chars))?\]`)
		result = markerRegex.ReplaceAllString(result, pasteContent)
	}
	return result
}

// GetExpandedText returns the text with paste markers expanded.
func (e *Editor) GetExpandedText() string {
	return e.expandPasteMarkersLocked(e.getTextLocked())
}

// GetLines returns a copy of the editor lines.
func (e *Editor) GetLines() []string {
	return append([]string(nil), e.state.lines...)
}

// Cursor returns the cursor line and column.
func (e *Editor) Cursor() (line int, col int) {
	return e.state.cursorLine, e.state.cursorCol
}

// SetText replaces the editor text.
func (e *Editor) SetText(text string) {
	e.cancelAutocompleteLocked()
	e.lastAction = ""
	e.exitHistoryBrowsing()
	normalized := e.normalizeText(text)
	if e.getTextLocked() != normalized {
		e.pushUndoSnapshot()
	}
	e.pastes = map[int]string{}
	e.pasteCounter = 0
	e.setTextInternal(normalized, "end")
}

// InsertTextAtCursor inserts text atomically at the cursor.
func (e *Editor) InsertTextAtCursor(text string) {
	if text == "" {
		return
	}
	e.cancelAutocompleteLocked()
	e.pushUndoSnapshot()
	e.lastAction = ""
	e.exitHistoryBrowsing()
	e.insertTextAtCursorInternal(text)
}

func (e *Editor) normalizeText(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	return strings.ReplaceAll(text, "\t", "    ")
}

func (e *Editor) insertTextAtCursorInternal(text string) {
	if text == "" {
		return
	}
	normalized := e.normalizeText(text)
	insertedLines := strings.Split(normalized, "\n")

	currentLine := e.currentLine()
	cursorCol := minInt(e.state.cursorCol, len(currentLine))
	beforeCursor := currentLine[:cursorCol]
	afterCursor := currentLine[cursorCol:]

	if len(insertedLines) == 1 {
		e.state.lines[e.state.cursorLine] = beforeCursor + normalized + afterCursor
		e.setCursorCol(cursorCol + len(normalized))
	} else {
		var lines []string
		lines = append(lines, e.state.lines[:e.state.cursorLine]...)
		lines = append(lines, beforeCursor+insertedLines[0])
		lines = append(lines, insertedLines[1:len(insertedLines)-1]...)
		lines = append(lines, insertedLines[len(insertedLines)-1]+afterCursor)
		lines = append(lines, e.state.lines[e.state.cursorLine+1:]...)
		e.state.lines = lines
		e.state.cursorLine += len(insertedLines) - 1
		e.setCursorCol(len(insertedLines[len(insertedLines)-1]))
	}

	if e.OnChange != nil {
		e.OnChange(e.getTextLocked())
	}
}

func (e *Editor) insertCharacterLocked(char string, skipUndoCoalescing bool) {
	e.exitHistoryBrowsing()

	if !skipUndoCoalescing {
		if IsWhitespaceChar(char) || e.lastAction != "type-word" {
			e.pushUndoSnapshot()
		}
		e.lastAction = "type-word"
	}

	line := e.currentLine()
	cursorCol := minInt(e.state.cursorCol, len(line))
	e.state.lines[e.state.cursorLine] = line[:cursorCol] + char + line[cursorCol:]
	e.setCursorCol(cursorCol + len(char))

	if e.OnChange != nil {
		e.OnChange(e.getTextLocked())
	}

	if e.autocompleteState == "" {
		if char == "/" && e.isAtStartOfMessage() {
			e.tryTriggerAutocompleteLocked(false)
		} else if containsString(e.autocompleteTriggers, char) {
			textBeforeCursor := e.textBeforeCursor()
			if editorTriggerMatch(textBeforeCursor, e.autocompleteTriggers, false) {
				e.tryTriggerAutocompleteLocked(false)
			}
		} else if isAutocompleteWordChar(char) {
			textBeforeCursor := e.textBeforeCursor()
			if e.isInSlashCommandContext(textBeforeCursor) {
				e.tryTriggerAutocompleteLocked(false)
			} else if editorTriggerMatch(textBeforeCursor, e.autocompleteTriggers, false) {
				e.tryTriggerAutocompleteLocked(false)
			}
		}
	} else {
		e.updateAutocompleteLocked()
	}
}

func isAutocompleteWordChar(char string) bool {
	for _, r := range char {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') ||
			r == '.' || r == '-' || r == '_' {
			return true
		}
		return cjkBreakRegex.MatchString(char)
	}
	return false
}

func containsString(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

func (e *Editor) textBeforeCursor() string {
	line := e.currentLine()
	return line[:minInt(e.state.cursorCol, len(line))]
}

func (e *Editor) handlePasteLocked(pastedText string) {
	e.cancelAutocompleteLocked()
	e.exitHistoryBrowsing()
	e.lastAction = ""
	e.pushUndoSnapshot()

	// Terminals may re-encode control bytes inside pastes as CSI-u sequences.
	decodedText := csiCtrlPasteRegex.ReplaceAllStringFunc(pastedText, func(match string) string {
		parts := csiCtrlPasteRegex.FindStringSubmatch(match)
		cp := atoiSafe(parts[1])
		if cp >= 97 && cp <= 122 {
			return string(rune(cp - 96))
		}
		if cp >= 65 && cp <= 90 {
			return string(rune(cp - 64))
		}
		return match
	})

	cleanText := e.normalizeText(decodedText)
	var builder strings.Builder
	for _, r := range cleanText {
		if r == '\n' || r >= 32 {
			builder.WriteRune(r)
		}
	}
	filteredText := builder.String()

	if len(filteredText) > 0 && (filteredText[0] == '/' || filteredText[0] == '~' || filteredText[0] == '.') {
		charBeforeCursor := ""
		if e.state.cursorCol > 0 {
			line := e.currentLine()
			if e.state.cursorCol-1 < len(line) {
				charBeforeCursor = string(line[e.state.cursorCol-1])
			}
		}
		if charBeforeCursor != "" && isWordCharString(charBeforeCursor) {
			filteredText = " " + filteredText
		}
	}

	pastedLines := strings.Split(filteredText, "\n")
	totalChars := utf8.RuneCountInString(filteredText)
	if len(pastedLines) > 10 || totalChars > 1000 {
		e.pasteCounter++
		pasteID := e.pasteCounter
		e.pastes[pasteID] = filteredText
		marker := ""
		if len(pastedLines) > 10 {
			marker = "[paste #" + itoa(pasteID) + " +" + itoa(len(pastedLines)) + " lines]"
		} else {
			marker = "[paste #" + itoa(pasteID) + " " + itoa(totalChars) + " chars]"
		}
		e.insertTextAtCursorInternal(marker)
		return
	}

	e.insertTextAtCursorInternal(filteredText)
}

var csiCtrlPasteRegex = mustCompileRegex(`\x1b\[(\d+);5u`)

func isWordCharString(value string) bool {
	for _, r := range value {
		return r == '_' || (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
	}
	return false
}

func (e *Editor) addNewLineLocked() {
	e.cancelAutocompleteLocked()
	e.exitHistoryBrowsing()
	e.lastAction = ""
	e.pushUndoSnapshot()

	currentLine := e.currentLine()
	cursorCol := minInt(e.state.cursorCol, len(currentLine))
	before := currentLine[:cursorCol]
	after := currentLine[cursorCol:]

	e.state.lines[e.state.cursorLine] = before
	e.state.lines = append(e.state.lines, "")
	copy(e.state.lines[e.state.cursorLine+2:], e.state.lines[e.state.cursorLine+1:])
	e.state.lines[e.state.cursorLine+1] = after

	e.state.cursorLine++
	e.setCursorCol(0)

	if e.OnChange != nil {
		e.OnChange(e.getTextLocked())
	}
}

func (e *Editor) shouldSubmitOnBackslashEnter(data string, kb *KeybindingsManager) bool {
	if e.DisableSubmit {
		return false
	}
	if !MatchesKey(data, "enter") {
		return false
	}
	submitKeys := kb.GetKeys("tui.input.submit")
	hasShiftEnter := containsString(submitKeys, "shift+enter") || containsString(submitKeys, "shift+return")
	if !hasShiftEnter {
		return false
	}
	currentLine := e.currentLine()
	return e.state.cursorCol > 0 && e.state.cursorCol-1 < len(currentLine) && currentLine[e.state.cursorCol-1] == '\\'
}

func (e *Editor) submitValueLocked() {
	e.cancelAutocompleteLocked()
	result := strings.TrimSpace(e.expandPasteMarkersLocked(e.getTextLocked()))

	e.state = editorState{lines: []string{""}}
	e.pastes = map[int]string{}
	e.pasteCounter = 0
	e.exitHistoryBrowsing()
	e.scrollOffset = 0
	e.undoStack.Clear()
	e.lastAction = ""

	if e.OnChange != nil {
		e.OnChange("")
	}
	if e.OnSubmit != nil {
		value := result
		e.pendingSubmit = &value
	}
}

func (e *Editor) handleBackspaceLocked() {
	e.exitHistoryBrowsing()
	e.lastAction = ""

	if e.state.cursorCol > 0 {
		e.pushUndoSnapshot()

		line := e.currentLine()
		cursorCol := minInt(e.state.cursorCol, len(line))
		beforeCursor := line[:cursorCol]
		segments := e.segmentLocked(beforeCursor, "grapheme")
		graphemeLength := 1
		lastSegment := ""
		if len(segments) > 0 {
			lastSegment = segments[len(segments)-1].Segment
			graphemeLength = len(lastSegment)
		}

		if match := pasteMarkerSingleRegex.FindStringSubmatch(lastSegment); match != nil {
			targetID := atoiSafe(match[1])
			delete(e.pastes, targetID)
			e.pasteCounter--

			var higherIDs []int
			for id := range e.pastes {
				if id > targetID {
					higherIDs = append(higherIDs, id)
				}
			}
			sortInts(higherIDs)
			for _, id := range higherIDs {
				e.pastes[id-1] = e.pastes[id]
				delete(e.pastes, id)
			}

			for index, lineValue := range e.state.lines {
				e.state.lines[index] = pasteMarkerRegex.ReplaceAllStringFunc(lineValue, func(fullMatch string) string {
					groups := pasteMarkerRegex.FindStringSubmatch(fullMatch)
					x := atoiSafe(groups[1])
					if x <= targetID {
						return fullMatch
					}
					return "[paste #" + itoa(x-1) + groups[2] + "]"
				})
			}
		}

		line = e.currentLine()
		before := line[:minInt(e.state.cursorCol-graphemeLength, len(line))]
		after := line[minInt(e.state.cursorCol, len(line)):]
		e.state.lines[e.state.cursorLine] = before + after
		e.setCursorCol(e.state.cursorCol - graphemeLength)
	} else if e.state.cursorLine > 0 {
		e.pushUndoSnapshot()
		currentLine := e.currentLine()
		previousLine := e.state.lines[e.state.cursorLine-1]
		e.state.lines[e.state.cursorLine-1] = previousLine + currentLine
		e.state.lines = append(e.state.lines[:e.state.cursorLine], e.state.lines[e.state.cursorLine+1:]...)
		e.state.cursorLine--
		e.setCursorCol(len(previousLine))
	}

	if e.OnChange != nil {
		e.OnChange(e.getTextLocked())
	}

	if e.autocompleteState != "" {
		e.updateAutocompleteLocked()
	} else {
		textBeforeCursor := e.textBeforeCursor()
		if e.isInSlashCommandContext(textBeforeCursor) {
			e.tryTriggerAutocompleteLocked(false)
		} else if editorTriggerMatch(textBeforeCursor, e.autocompleteTriggers, false) {
			e.tryTriggerAutocompleteLocked(false)
		}
	}
}

func sortInts(values []int) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}

func (e *Editor) setCursorCol(col int) {
	e.state.cursorCol = col
	e.preferredVisualCol = -1
	e.hasPreferredVisualCol = false
	e.snappedFromCursorCol = -1
	e.hasSnappedFromCursor = false
}

type editorVisualLine struct {
	logicalLine int
	startCol    int
	length      int
}

func (e *Editor) moveToVisualLine(visualLines []editorVisualLine, currentVisualLine int, targetVisualLine int) {
	if currentVisualLine < 0 || currentVisualLine >= len(visualLines) ||
		targetVisualLine < 0 || targetVisualLine >= len(visualLines) {
		return
	}
	currentVL := visualLines[currentVisualLine]
	targetVL := visualLines[targetVisualLine]

	currentVisualCol := 0
	if e.hasSnappedFromCursor {
		vlIndex := e.findVisualLineAt(visualLines, currentVL.logicalLine, e.snappedFromCursorCol)
		currentVisualCol = e.snappedFromCursorCol - visualLines[vlIndex].startCol
	} else {
		currentVisualCol = e.state.cursorCol - currentVL.startCol
	}

	isLastSourceSegment := currentVisualLine == len(visualLines)-1 ||
		visualLines[currentVisualLine+1].logicalLine != currentVL.logicalLine
	sourceMaxVisualCol := maxInt(0, currentVL.length-1)
	if isLastSourceSegment {
		sourceMaxVisualCol = currentVL.length
	}

	isLastTargetSegment := targetVisualLine == len(visualLines)-1 ||
		visualLines[targetVisualLine+1].logicalLine != targetVL.logicalLine
	targetMaxVisualCol := maxInt(0, targetVL.length-1)
	if isLastTargetSegment {
		targetMaxVisualCol = targetVL.length
	}

	moveToVisualCol := e.computeVerticalMoveColumn(currentVisualCol, sourceMaxVisualCol, targetMaxVisualCol)

	e.state.cursorLine = targetVL.logicalLine
	targetCol := targetVL.startCol + moveToVisualCol
	logicalLine := ""
	if targetVL.logicalLine < len(e.state.lines) {
		logicalLine = e.state.lines[targetVL.logicalLine]
	}
	e.state.cursorCol = minInt(targetCol, len(logicalLine))

	// Snap the cursor to atomic segment boundaries (e.g. paste markers).
	for _, segment := range e.segmentLocked(logicalLine, "grapheme") {
		if segment.Index > e.state.cursorCol {
			break
		}
		if len(segment.Segment) <= 1 {
			continue
		}
		if e.state.cursorCol < segment.Index+len(segment.Segment) {
			isContinuation := segment.Index < targetVL.startCol
			isMovingDown := targetVisualLine > currentVisualLine
			if isContinuation && isMovingDown {
				segEnd := segment.Index + len(segment.Segment)
				next := targetVisualLine + 1
				for next < len(visualLines) && visualLines[next].logicalLine == targetVL.logicalLine &&
					visualLines[next].startCol < segEnd {
					next++
				}
				if next < len(visualLines) {
					e.moveToVisualLine(visualLines, currentVisualLine, next)
					return
				}
			}
			e.snappedFromCursorCol = e.state.cursorCol
			e.hasSnappedFromCursor = true
			e.state.cursorCol = segment.Index
			return
		}
	}
	e.snappedFromCursorCol = -1
	e.hasSnappedFromCursor = false
}

func (e *Editor) computeVerticalMoveColumn(currentVisualCol int, sourceMaxVisualCol int, targetMaxVisualCol int) int {
	hasPreferred := e.hasPreferredVisualCol
	cursorInMiddle := currentVisualCol < sourceMaxVisualCol
	targetTooShort := targetMaxVisualCol < currentVisualCol

	if !hasPreferred || cursorInMiddle {
		if targetTooShort {
			e.preferredVisualCol = currentVisualCol
			e.hasPreferredVisualCol = true
			return targetMaxVisualCol
		}
		e.preferredVisualCol = -1
		e.hasPreferredVisualCol = false
		return currentVisualCol
	}

	targetCantFitPreferred := targetMaxVisualCol < e.preferredVisualCol
	if targetTooShort || targetCantFitPreferred {
		return targetMaxVisualCol
	}
	result := e.preferredVisualCol
	e.preferredVisualCol = -1
	e.hasPreferredVisualCol = false
	return result
}

func (e *Editor) moveToLineStart() {
	e.lastAction = ""
	e.setCursorCol(0)
}

func (e *Editor) moveToLineEnd() {
	e.lastAction = ""
	e.setCursorCol(len(e.currentLine()))
}

func (e *Editor) deleteToStartOfLineLocked() {
	e.exitHistoryBrowsing()
	currentLine := e.currentLine()

	if e.state.cursorCol > 0 {
		e.pushUndoSnapshot()
		deletedText := currentLine[:minInt(e.state.cursorCol, len(currentLine))]
		e.killRing.Push(deletedText, KillRingPushOptions{Prepend: true, Accumulate: e.lastAction == "kill"})
		e.lastAction = "kill"
		e.state.lines[e.state.cursorLine] = currentLine[minInt(e.state.cursorCol, len(currentLine)):]
		e.setCursorCol(0)
	} else if e.state.cursorLine > 0 {
		e.pushUndoSnapshot()
		e.killRing.Push("\n", KillRingPushOptions{Prepend: true, Accumulate: e.lastAction == "kill"})
		e.lastAction = "kill"
		previousLine := e.state.lines[e.state.cursorLine-1]
		e.state.lines[e.state.cursorLine-1] = previousLine + currentLine
		e.state.lines = append(e.state.lines[:e.state.cursorLine], e.state.lines[e.state.cursorLine+1:]...)
		e.state.cursorLine--
		e.setCursorCol(len(previousLine))
	}

	if e.OnChange != nil {
		e.OnChange(e.getTextLocked())
	}
}

func (e *Editor) deleteToEndOfLineLocked() {
	e.exitHistoryBrowsing()
	currentLine := e.currentLine()

	if e.state.cursorCol < len(currentLine) {
		e.pushUndoSnapshot()
		deletedText := currentLine[e.state.cursorCol:]
		e.killRing.Push(deletedText, KillRingPushOptions{Prepend: false, Accumulate: e.lastAction == "kill"})
		e.lastAction = "kill"
		e.state.lines[e.state.cursorLine] = currentLine[:e.state.cursorCol]
	} else if e.state.cursorLine < len(e.state.lines)-1 {
		e.pushUndoSnapshot()
		e.killRing.Push("\n", KillRingPushOptions{Prepend: false, Accumulate: e.lastAction == "kill"})
		e.lastAction = "kill"
		nextLine := e.state.lines[e.state.cursorLine+1]
		e.state.lines[e.state.cursorLine] = currentLine + nextLine
		e.state.lines = append(e.state.lines[:e.state.cursorLine+1], e.state.lines[e.state.cursorLine+2:]...)
	}

	if e.OnChange != nil {
		e.OnChange(e.getTextLocked())
	}
}

func (e *Editor) deleteWordBackwardsLocked() {
	e.exitHistoryBrowsing()
	currentLine := e.currentLine()

	if e.state.cursorCol == 0 {
		if e.state.cursorLine > 0 {
			e.pushUndoSnapshot()
			e.killRing.Push("\n", KillRingPushOptions{Prepend: true, Accumulate: e.lastAction == "kill"})
			e.lastAction = "kill"
			previousLine := e.state.lines[e.state.cursorLine-1]
			e.state.lines[e.state.cursorLine-1] = previousLine + currentLine
			e.state.lines = append(e.state.lines[:e.state.cursorLine], e.state.lines[e.state.cursorLine+1:]...)
			e.state.cursorLine--
			e.setCursorCol(len(previousLine))
		}
	} else {
		e.pushUndoSnapshot()
		wasKill := e.lastAction == "kill"
		oldCursorCol := e.state.cursorCol
		e.moveWordBackwardsLocked()
		deleteFrom := e.state.cursorCol
		e.setCursorCol(oldCursorCol)

		deletedText := currentLine[deleteFrom:minInt(e.state.cursorCol, len(currentLine))]
		e.killRing.Push(deletedText, KillRingPushOptions{Prepend: true, Accumulate: wasKill})
		e.lastAction = "kill"
		e.state.lines[e.state.cursorLine] = currentLine[:deleteFrom] + currentLine[minInt(e.state.cursorCol, len(currentLine)):]
		e.setCursorCol(deleteFrom)
	}

	if e.OnChange != nil {
		e.OnChange(e.getTextLocked())
	}
}

func (e *Editor) deleteWordForwardLocked() {
	e.exitHistoryBrowsing()
	currentLine := e.currentLine()

	if e.state.cursorCol >= len(currentLine) {
		if e.state.cursorLine < len(e.state.lines)-1 {
			e.pushUndoSnapshot()
			e.killRing.Push("\n", KillRingPushOptions{Prepend: false, Accumulate: e.lastAction == "kill"})
			e.lastAction = "kill"
			nextLine := e.state.lines[e.state.cursorLine+1]
			e.state.lines[e.state.cursorLine] = currentLine + nextLine
			e.state.lines = append(e.state.lines[:e.state.cursorLine+1], e.state.lines[e.state.cursorLine+2:]...)
		}
	} else {
		e.pushUndoSnapshot()
		wasKill := e.lastAction == "kill"
		oldCursorCol := e.state.cursorCol
		e.moveWordForwardsLocked()
		deleteTo := e.state.cursorCol
		e.setCursorCol(oldCursorCol)

		deletedText := currentLine[e.state.cursorCol:deleteTo]
		e.killRing.Push(deletedText, KillRingPushOptions{Prepend: false, Accumulate: wasKill})
		e.lastAction = "kill"
		e.state.lines[e.state.cursorLine] = currentLine[:e.state.cursorCol] + currentLine[deleteTo:]
	}

	if e.OnChange != nil {
		e.OnChange(e.getTextLocked())
	}
}

func (e *Editor) handleForwardDeleteLocked() {
	e.exitHistoryBrowsing()
	e.lastAction = ""
	currentLine := e.currentLine()

	if e.state.cursorCol < len(currentLine) {
		e.pushUndoSnapshot()
		afterCursor := currentLine[e.state.cursorCol:]
		segments := e.segmentLocked(afterCursor, "grapheme")
		graphemeLength := 1
		if len(segments) > 0 {
			graphemeLength = len(segments[0].Segment)
		}
		before := currentLine[:e.state.cursorCol]
		after := currentLine[minInt(e.state.cursorCol+graphemeLength, len(currentLine)):]
		e.state.lines[e.state.cursorLine] = before + after
	} else if e.state.cursorLine < len(e.state.lines)-1 {
		e.pushUndoSnapshot()
		nextLine := e.state.lines[e.state.cursorLine+1]
		e.state.lines[e.state.cursorLine] = currentLine + nextLine
		e.state.lines = append(e.state.lines[:e.state.cursorLine+1], e.state.lines[e.state.cursorLine+2:]...)
	}

	if e.OnChange != nil {
		e.OnChange(e.getTextLocked())
	}

	if e.autocompleteState != "" {
		e.updateAutocompleteLocked()
	} else {
		textBeforeCursor := e.textBeforeCursor()
		if e.isInSlashCommandContext(textBeforeCursor) {
			e.tryTriggerAutocompleteLocked(false)
		} else if editorTriggerMatch(textBeforeCursor, e.autocompleteTriggers, false) {
			e.tryTriggerAutocompleteLocked(false)
		}
	}
}

func (e *Editor) buildVisualLineMap(width int) []editorVisualLine {
	var visualLines []editorVisualLine
	for i := 0; i < len(e.state.lines); i++ {
		line := e.state.lines[i]
		if len(line) == 0 {
			visualLines = append(visualLines, editorVisualLine{logicalLine: i, startCol: 0, length: 0})
			continue
		}
		if VisibleWidth(line) <= width {
			visualLines = append(visualLines, editorVisualLine{logicalLine: i, startCol: 0, length: len(line)})
			continue
		}
		for _, chunk := range WordWrapLine(line, width, e.segmentLocked(line, "grapheme")) {
			visualLines = append(visualLines, editorVisualLine{
				logicalLine: i,
				startCol:    chunk.StartIndex,
				length:      chunk.EndIndex - chunk.StartIndex,
			})
		}
	}
	return visualLines
}

func (e *Editor) findVisualLineAt(visualLines []editorVisualLine, line int, col int) int {
	for i := 0; i < len(visualLines); i++ {
		vl := visualLines[i]
		if vl.logicalLine != line {
			continue
		}
		offset := col - vl.startCol
		isLastSegmentOfLine := i == len(visualLines)-1 || visualLines[i+1].logicalLine != vl.logicalLine
		if offset >= 0 && (offset < vl.length || (isLastSegmentOfLine && offset == vl.length)) {
			return i
		}
	}
	return len(visualLines) - 1
}

func (e *Editor) findCurrentVisualLine(visualLines []editorVisualLine) int {
	return e.findVisualLineAt(visualLines, e.state.cursorLine, e.state.cursorCol)
}

func (e *Editor) moveCursorLocked(deltaLine int, deltaCol int) {
	e.lastAction = ""
	visualLines := e.buildVisualLineMap(e.lastWidth)
	currentVisualLine := e.findCurrentVisualLine(visualLines)

	if deltaLine != 0 {
		targetVisualLine := currentVisualLine + deltaLine
		if targetVisualLine >= 0 && targetVisualLine < len(visualLines) {
			e.moveToVisualLine(visualLines, currentVisualLine, targetVisualLine)
		}
	}

	if deltaCol != 0 {
		currentLine := e.currentLine()
		if deltaCol > 0 {
			if e.state.cursorCol < len(currentLine) {
				segments := e.segmentLocked(currentLine[e.state.cursorCol:], "grapheme")
				advance := 1
				if len(segments) > 0 {
					advance = len(segments[0].Segment)
				}
				e.setCursorCol(e.state.cursorCol + advance)
			} else if e.state.cursorLine < len(e.state.lines)-1 {
				e.state.cursorLine++
				e.setCursorCol(0)
			} else if currentVisualLine >= 0 && currentVisualLine < len(visualLines) {
				e.preferredVisualCol = e.state.cursorCol - visualLines[currentVisualLine].startCol
				e.hasPreferredVisualCol = true
			}
		} else {
			if e.state.cursorCol > 0 {
				beforeCursor := currentLine[:minInt(e.state.cursorCol, len(currentLine))]
				segments := e.segmentLocked(beforeCursor, "grapheme")
				back := 1
				if len(segments) > 0 {
					back = len(segments[len(segments)-1].Segment)
				}
				e.setCursorCol(e.state.cursorCol - back)
			} else if e.state.cursorLine > 0 {
				e.state.cursorLine--
				e.setCursorCol(len(e.state.lines[e.state.cursorLine]))
			}
		}
	}

	if e.autocompleteState != "" {
		e.updateAutocompleteLocked()
	}
}

func (e *Editor) pageScrollLocked(direction int) {
	e.lastAction = ""
	terminalRows := 25
	if e.host != nil {
		terminalRows = e.host.Rows()
	}
	pageSize := maxInt(5, terminalRows*3/10)

	visualLines := e.buildVisualLineMap(e.lastWidth)
	currentVisualLine := e.findCurrentVisualLine(visualLines)
	targetVisualLine := maxInt(0, minInt(len(visualLines)-1, currentVisualLine+direction*pageSize))
	e.moveToVisualLine(visualLines, currentVisualLine, targetVisualLine)
}

func (e *Editor) moveWordBackwardsLocked() {
	e.lastAction = ""
	currentLine := e.currentLine()
	if e.state.cursorCol == 0 {
		if e.state.cursorLine > 0 {
			e.state.cursorLine--
			e.setCursorCol(len(e.state.lines[e.state.cursorLine]))
		}
		return
	}
	e.setCursorCol(findWordBackwardWithSegments(currentLine, e.state.cursorCol, e))
}

func (e *Editor) moveWordForwardsLocked() {
	e.lastAction = ""
	currentLine := e.currentLine()
	if e.state.cursorCol >= len(currentLine) {
		if e.state.cursorLine < len(e.state.lines)-1 {
			e.state.cursorLine++
			e.setCursorCol(0)
		}
		return
	}
	e.setCursorCol(findWordForwardWithSegments(currentLine, e.state.cursorCol, e))
}

// findWordBackwardWithSegments applies findWordBackward with the editor's
// paste-marker aware word segmentation and atomic markers.
func findWordBackwardWithSegments(text string, cursor int, e *Editor) int {
	return findWordWithSegmentation(text, cursor, e, true)
}

func findWordForwardWithSegments(text string, cursor int, e *Editor) int {
	return findWordWithSegmentation(text, cursor, e, false)
}

func findWordWithSegmentation(text string, cursor int, e *Editor, backward bool) int {
	// The editor's segmentation merges paste markers into atomic segments; the
	// generic word navigation already treats single-paste-marker segments as
	// atomic, so the segmenter output is passed through.
	segments := e.segmentLocked(text, "word")
	if backward {
		return findWordBackwardSegments(text, cursor, segments)
	}
	return findWordForwardSegments(text, cursor, segments)
}

func findWordBackwardSegments(text string, cursor int, segments []editorSegment) int {
	if cursor <= 0 {
		return 0
	}
	filtered := make([]editorSegment, 0, len(segments))
	for _, segment := range segments {
		if segment.Index >= cursor {
			break
		}
		filtered = append(filtered, segment)
	}
	segments = filtered
	newCursor := cursor

	for len(segments) > 0 {
		last := segments[len(segments)-1]
		if isPasteMarker(last.Segment) || !IsWhitespaceChar(last.Segment) {
			break
		}
		newCursor -= len(last.Segment)
		segments = segments[:len(segments)-1]
	}
	if len(segments) == 0 {
		return newCursor
	}

	last := segments[len(segments)-1]
	if isPasteMarker(last.Segment) {
		return newCursor - len(last.Segment)
	}
	if isWordLikeSegment(last.Segment) {
		runes := []rune(last.Segment)
		lastPunctuation := -1
		for index, r := range runes {
			if punctuationChars[r] {
				lastPunctuation = index
			}
		}
		if lastPunctuation < 0 {
			return newCursor - len(last.Segment)
		}
		trailing := 0
		for _, r := range runes[lastPunctuation+1:] {
			trailing += len(string(r))
		}
		return newCursor - trailing
	}

	for len(segments) > 0 {
		last := segments[len(segments)-1]
		if isPasteMarker(last.Segment) || isWordLikeSegment(last.Segment) || IsWhitespaceChar(last.Segment) {
			break
		}
		newCursor -= len(last.Segment)
		segments = segments[:len(segments)-1]
	}
	return newCursor
}

func findWordForwardSegments(text string, cursor int, segments []editorSegment) int {
	if cursor >= len(text) {
		return len(text)
	}
	newCursor := cursor
	index := 0
	for index < len(segments) && segments[index].Index < cursor {
		index++
	}
	for index < len(segments) && IsWhitespaceChar(segments[index].Segment) && !isPasteMarker(segments[index].Segment) {
		newCursor += len(segments[index].Segment)
		index++
	}
	if index >= len(segments) {
		return newCursor
	}
	next := segments[index]
	if isPasteMarker(next.Segment) {
		return newCursor + len(next.Segment)
	}
	if isWordLikeSegment(next.Segment) {
		for _, r := range next.Segment {
			if punctuationChars[r] {
				return newCursor + utf8.RuneLen(r) - utf8.RuneLen(r) // stop before the punctuation
			}
		}
		stopped := false
		for offset, r := range next.Segment {
			if punctuationChars[r] {
				newCursor += offset
				stopped = true
				break
			}
		}
		if !stopped {
			newCursor += len(next.Segment)
		}
		return newCursor
	}
	for index < len(segments) {
		current := segments[index]
		if isPasteMarker(current.Segment) || isWordLikeSegment(current.Segment) || IsWhitespaceChar(current.Segment) {
			break
		}
		newCursor += len(current.Segment)
		index++
	}
	return newCursor
}

func isWordLikeSegment(segment string) bool {
	for _, r := range segment {
		if !(isLetterRune(r) || isDigitRune(r) || isMarkRune(r) || r == '_') {
			return false
		}
	}
	return len(segment) > 0
}

func (e *Editor) yankLocked() {
	if e.killRing.Len() == 0 {
		return
	}
	e.pushUndoSnapshot()
	text, _ := e.killRing.Peek()
	e.insertYankedTextLocked(text)
	e.lastAction = "yank"
}

func (e *Editor) yankPopLocked() {
	if e.lastAction != "yank" || e.killRing.Len() <= 1 {
		return
	}
	e.pushUndoSnapshot()
	e.deleteYankedTextLocked()
	e.killRing.Rotate()
	text, _ := e.killRing.Peek()
	e.insertYankedTextLocked(text)
	e.lastAction = "yank"
}

func (e *Editor) insertYankedTextLocked(text string) {
	e.exitHistoryBrowsing()
	lines := strings.Split(text, "\n")

	if len(lines) == 1 {
		currentLine := e.currentLine()
		cursorCol := minInt(e.state.cursorCol, len(currentLine))
		e.state.lines[e.state.cursorLine] = currentLine[:cursorCol] + text + currentLine[cursorCol:]
		e.setCursorCol(cursorCol + len(text))
	} else {
		currentLine := e.currentLine()
		cursorCol := minInt(e.state.cursorCol, len(currentLine))
		before := currentLine[:cursorCol]
		after := currentLine[cursorCol:]

		e.state.lines[e.state.cursorLine] = before + lines[0]
		for i := 1; i < len(lines)-1; i++ {
			insertAt := e.state.cursorLine + i
			e.state.lines = append(e.state.lines, "")
			copy(e.state.lines[insertAt+1:], e.state.lines[insertAt:])
			e.state.lines[insertAt] = lines[i]
		}
		lastLineIndex := e.state.cursorLine + len(lines) - 1
		e.state.lines = append(e.state.lines, "")
		copy(e.state.lines[lastLineIndex+1:], e.state.lines[lastLineIndex:])
		e.state.lines[lastLineIndex] = lines[len(lines)-1] + after

		e.state.cursorLine = lastLineIndex
		e.setCursorCol(len(lines[len(lines)-1]))
	}

	if e.OnChange != nil {
		e.OnChange(e.getTextLocked())
	}
}

func (e *Editor) deleteYankedTextLocked() {
	yankedText, ok := e.killRing.Peek()
	if !ok {
		return
	}
	yankLines := strings.Split(yankedText, "\n")

	if len(yankLines) == 1 {
		currentLine := e.currentLine()
		deleteLen := len(yankedText)
		before := currentLine[:maxInt(0, minInt(e.state.cursorCol-deleteLen, len(currentLine)))]
		after := currentLine[minInt(e.state.cursorCol, len(currentLine)):]
		e.state.lines[e.state.cursorLine] = before + after
		e.setCursorCol(e.state.cursorCol - deleteLen)
	} else {
		startLine := e.state.cursorLine - (len(yankLines) - 1)
		startLineText := ""
		if startLine >= 0 && startLine < len(e.state.lines) {
			startLineText = e.state.lines[startLine]
		}
		startCol := len(startLineText) - len(yankLines[0])

		afterCursor := ""
		if e.state.cursorLine < len(e.state.lines) {
			currentLine := e.state.lines[e.state.cursorLine]
			afterCursor = currentLine[minInt(e.state.cursorCol, len(currentLine)):]
		}
		beforeYank := startLineText[:maxInt(0, minInt(startCol, len(startLineText)))]

		newLines := append([]string(nil), e.state.lines[:startLine]...)
		newLines = append(newLines, beforeYank+afterCursor)
		if e.state.cursorLine+1 <= len(e.state.lines) {
			newLines = append(newLines, e.state.lines[e.state.cursorLine+1:]...)
		}
		e.state.lines = newLines

		e.state.cursorLine = startLine
		e.setCursorCol(startCol)
	}

	if e.OnChange != nil {
		e.OnChange(e.getTextLocked())
	}
}

func (e *Editor) pushUndoSnapshot() {
	snapshot := editorSnapshot{pasteCounter: e.pasteCounter}
	snapshot.state = e.state
	snapshot.state.lines = append([]string(nil), e.state.lines...)
	snapshot.pastes = map[int]string{}
	for id, content := range e.pastes {
		snapshot.pastes[id] = content
	}
	e.undoStack.Push(snapshot)
}

func (e *Editor) undoLocked() {
	e.exitHistoryBrowsing()
	snapshot, ok := e.undoStack.Pop()
	if !ok {
		return
	}
	e.state = snapshot.state
	e.state.lines = append([]string(nil), snapshot.state.lines...)
	e.pastes = snapshot.pastes
	e.pasteCounter = snapshot.pasteCounter
	e.lastAction = ""
	e.preferredVisualCol = -1
	e.hasPreferredVisualCol = false
	if e.OnChange != nil {
		e.OnChange(e.getTextLocked())
	}
}

func (e *Editor) jumpToChar(char string, direction string) {
	e.lastAction = ""
	isForward := direction == "forward"
	lines := e.state.lines

	if isForward {
		for lineIndex := e.state.cursorLine; lineIndex < len(lines); lineIndex++ {
			line := lines[lineIndex]
			searchFrom := 0
			if lineIndex == e.state.cursorLine {
				searchFrom = e.state.cursorCol + 1
			}
			offset, ok := indexOfRuneFrom(line, char, searchFrom, true)
			if ok {
				e.state.cursorLine = lineIndex
				e.setCursorCol(offset)
				return
			}
		}
		return
	}
	for lineIndex := e.state.cursorLine; lineIndex >= 0; lineIndex-- {
		line := lines[lineIndex]
		if lineIndex == e.state.cursorLine {
			searchFrom := e.state.cursorCol - 1
			if searchFrom >= 0 && searchFrom < len(line) {
				offset, ok := lastIndexOfRuneBefore(line, char, searchFrom+1)
				if ok {
					e.state.cursorLine = lineIndex
					e.setCursorCol(offset)
					return
				}
			}
			continue
		}
		offset, ok := lastIndexOfRuneBefore(line, char, len(line))
		if ok {
			e.state.cursorLine = lineIndex
			e.setCursorCol(offset)
			return
		}
	}
}

func indexOfRuneFrom(text string, target string, from int, _ bool) (int, bool) {
	if from > len(text) {
		return 0, false
	}
	index := strings.Index(text[from:], target)
	if index < 0 {
		return 0, false
	}
	return from + index, true
}

func lastIndexOfRuneBefore(text string, target string, before int) (int, bool) {
	if before > len(text) {
		before = len(text)
	}
	index := strings.LastIndex(text[:before], target)
	if index < 0 {
		return 0, false
	}
	return index, true
}

// ---- Autocomplete integration ----

func (e *Editor) isSlashMenuAllowed() bool { return e.state.cursorLine == 0 }

func (e *Editor) isAtStartOfMessage() bool {
	if !e.isSlashMenuAllowed() {
		return false
	}
	beforeCursor := e.textBeforeCursor()
	trimmed := strings.TrimSpace(beforeCursor)
	return trimmed == "" || trimmed == "/"
}

func (e *Editor) isInSlashCommandContext(textBeforeCursor string) bool {
	return e.isSlashMenuAllowed() && strings.HasPrefix(strings.TrimLeft(textBeforeCursor, " \t\n\r\v\f\u00a0"), "/")
}

func (e *Editor) getBestAutocompleteMatchIndex(items []AutocompleteItem, prefix string) int {
	if prefix == "" {
		return -1
	}
	firstPrefixIndex := -1
	for i, item := range items {
		if item.Value == prefix {
			return i
		}
		if firstPrefixIndex == -1 && strings.HasPrefix(item.Value, prefix) {
			firstPrefixIndex = i
		}
	}
	return firstPrefixIndex
}

func (e *Editor) createAutocompleteListLocked(prefix string, items []AutocompleteItem) *SelectList {
	var layout SelectListLayoutOptions
	if strings.HasPrefix(prefix, "/") {
		layout = slashCommandSelectListLayout
	}
	selectItems := make([]SelectItem, 0, len(items))
	for _, item := range items {
		selectItems = append(selectItems, SelectItem{Value: item.Value, Label: item.Label, Description: item.Description})
	}
	list := NewSelectList(selectItems, e.autocompleteMaxVisible, e.theme.SelectList, layout)
	list.OnSelect = func(selected SelectItem) {
		if e.autocompleteProvider == nil {
			return
		}
		e.pushUndoSnapshot()
		e.lastAction = ""
		result := e.autocompleteProvider.ApplyCompletion(
			e.state.lines, e.state.cursorLine, e.state.cursorCol,
			autocompleteItemFromSelect(selected),
			e.autocompletePrefix)
		e.state.lines = result.Lines
		e.state.cursorLine = result.CursorLine
		e.setCursorCol(result.CursorCol)
		e.cancelAutocompleteLocked()
		if e.OnChange != nil {
			e.OnChange(e.getTextLocked())
		}
	}
	return list
}

func autocompleteItemFromSelect(item SelectItem) AutocompleteItem {
	return AutocompleteItem{Value: item.Value, Label: item.Label, Description: item.Description}
}

func (e *Editor) tryTriggerAutocompleteLocked(explicitTab bool) {
	e.requestAutocompleteLocked(false, explicitTab)
}

func (e *Editor) handleTabCompletionLocked() {
	if e.autocompleteProvider == nil {
		return
	}
	beforeCursor := e.textBeforeCursor()
	if e.isInSlashCommandContext(beforeCursor) && !strings.Contains(strings.TrimLeft(beforeCursor, " \t"), " ") {
		e.requestAutocompleteLocked(false, true)
		return
	}
	e.requestAutocompleteLocked(true, true)
}

func (e *Editor) requestAutocompleteLocked(force bool, explicitTab bool) {
	if e.autocompleteProvider == nil {
		return
	}
	if force {
		if trigger, ok := e.autocompleteProvider.(FileCompletionTrigger); ok {
			if !trigger.ShouldTriggerFileCompletion(e.state.lines, e.state.cursorLine, e.state.cursorCol) {
				return
			}
		}
	}

	// The Go port requests synchronously under the editor lock (D71): upstream
	// debounces, aborts in-flight requests, and applies the result
	// asynchronously.
	suggestions := e.autocompleteProvider.GetSuggestions(
		context.Background(), e.state.lines, e.state.cursorLine, e.state.cursorCol, force)
	if suggestions == nil || len(suggestions.Items) == 0 {
		e.cancelAutocompleteLocked()
		e.requestRender()
		return
	}

	if force && explicitTab && len(suggestions.Items) == 1 {
		item := suggestions.Items[0]
		e.pushUndoSnapshot()
		e.lastAction = ""
		result := e.autocompleteProvider.ApplyCompletion(
			e.state.lines, e.state.cursorLine, e.state.cursorCol, item, suggestions.Prefix)
		e.state.lines = result.Lines
		e.state.cursorLine = result.CursorLine
		e.setCursorCol(result.CursorCol)
		if e.OnChange != nil {
			e.OnChange(e.getTextLocked())
		}
		e.requestRender()
		return
	}

	state := "regular"
	if force {
		state = "force"
	}
	e.applyAutocompleteSuggestionsLocked(suggestions, state)
	e.requestRender()
}

func (e *Editor) applyAutocompleteSuggestionsLocked(suggestions *AutocompleteSuggestions, state string) {
	e.autocompletePrefix = suggestions.Prefix
	e.autocompleteList = e.createAutocompleteListLocked(suggestions.Prefix, suggestions.Items)
	if bestMatchIndex := e.getBestAutocompleteMatchIndex(suggestions.Items, suggestions.Prefix); bestMatchIndex >= 0 {
		e.autocompleteList.SetSelectedIndex(bestMatchIndex)
	}
	e.autocompleteState = state
}

func (e *Editor) clearAutocompleteUILocked() {
	e.autocompleteState = ""
	e.autocompleteList = nil
	e.autocompletePrefix = ""
}

func (e *Editor) cancelAutocompleteLocked() {
	e.clearAutocompleteUILocked()
}

// IsShowingAutocomplete reports whether the suggestion list is visible.
func (e *Editor) IsShowingAutocomplete() bool {
	return e.autocompleteState != ""
}

func (e *Editor) updateAutocompleteLocked() {
	if e.autocompleteState == "" || e.autocompleteProvider == nil {
		return
	}
	e.requestAutocompleteLocked(e.autocompleteState == "force", false)
}

func (e *Editor) setAutocompleteTriggerCharactersLocked(triggerCharacters []string) {
	next := append([]string(nil), defaultAutocompleteTriggerCharacters...)
	for _, character := range triggerCharacters {
		if utf8.RuneCountInString(character) != 1 || character == "/" || IsWhitespaceChar(character) ||
			containsString(next, character) {
			continue
		}
		next = append(next, character)
	}
	e.autocompleteTriggers = next
}

// segmentLocked segments text with paste-marker awareness.
func (e *Editor) segmentLocked(text string, mode string) []editorSegment {
	validIDs := make(map[int]bool, len(e.pastes))
	for id := range e.pastes {
		validIDs[id] = true
	}
	return segmentWithMarkers(text, mode, validIDs)
}

var _ Component = (*Editor)(nil)
var _ Focusable = (*Editor)(nil)
var _ MouseHandler = (*Editor)(nil)
