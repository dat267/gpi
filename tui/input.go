package tui

import (
	"strings"
)

// Port of src/components/input.ts: a single-line text input with horizontal
// scrolling, a fake cursor, bracketed-paste handling, the kill ring, and
// undo. Upstream indexes by UTF-16 code units; Go uses byte offsets into the
// UTF-8 string (divergence D61).

type inputState struct {
	value  string
	cursor int
}

// InputOptions configure an input.
type InputOptions struct {
	Prompt           string
	Placeholder      string
	PlaceholderStyle func(text string) string
}

// Input is a single-line text input component.
type Input struct {
	value               string
	cursor              int
	prompt              string
	placeholder         string
	placeholderStyle    func(text string) string
	renderedStartColumn int

	OnSubmit func(value string)
	OnEscape func()

	focused bool

	pasteBuffer string
	isInPaste   bool

	killRing   KillRing
	lastAction string // "" | "kill" | "yank" | "type-word"

	undoStack UndoStack[inputState]
}

// NewInput creates an input component.
func NewInput(options InputOptions) *Input {
	input := &Input{
		prompt:           options.Prompt,
		placeholder:      options.Placeholder,
		placeholderStyle: options.PlaceholderStyle,
	}
	if input.prompt == "" {
		input.prompt = "> "
	}
	if input.placeholderStyle == nil {
		input.placeholderStyle = func(text string) string { return text }
	}
	return input
}

// Value returns the current value.
func (in *Input) Value() string { return in.value }

// SetValue replaces the value, clamping the cursor.
func (in *Input) SetValue(value string) {
	in.value = value
	if in.cursor > len(value) {
		in.cursor = len(value)
	}
}

// SetFocused implements Focusable.
func (in *Input) SetFocused(focused bool) { in.focused = focused }

// IsFocused implements Focusable.
func (in *Input) IsFocused() bool { return in.focused }

// Invalidate drops cached state (none).
func (in *Input) Invalidate() {}

// HandleInput processes a key/input chunk.
func (in *Input) HandleInput(data string) {
	// Bracketed paste: \x1b[200~ starts, \x1b[201~ ends.
	if strings.Contains(data, "\x1b[200~") {
		in.isInPaste = true
		in.pasteBuffer = ""
		data = strings.Replace(data, "\x1b[200~", "", 1)
	}

	if in.isInPaste {
		in.pasteBuffer += data

		if endIndex := strings.Index(in.pasteBuffer, "\x1b[201~"); endIndex != -1 {
			pasteContent := in.pasteBuffer[:endIndex]
			in.handlePaste(pasteContent)
			in.isInPaste = false

			remaining := in.pasteBuffer[endIndex+6:] // 6 = len("\x1b[201~")
			in.pasteBuffer = ""
			if remaining != "" {
				in.HandleInput(remaining)
			}
		}
		return
	}

	kb := GetKeybindings()

	if kb.Matches(data, "tui.select.cancel") {
		if in.OnEscape != nil {
			in.OnEscape()
		}
		return
	}

	if kb.Matches(data, "tui.editor.undo") {
		in.undo()
		return
	}

	if kb.Matches(data, "tui.input.submit") || data == "\n" {
		if in.OnSubmit != nil {
			in.OnSubmit(in.value)
		}
		return
	}

	if kb.Matches(data, "tui.editor.deleteCharBackward") {
		in.handleBackspace()
		return
	}
	if kb.Matches(data, "tui.editor.deleteCharForward") {
		in.handleForwardDelete()
		return
	}
	if kb.Matches(data, "tui.editor.deleteWordBackward") {
		in.deleteWordBackwards()
		return
	}
	if kb.Matches(data, "tui.editor.deleteWordForward") {
		in.deleteWordForward()
		return
	}
	if kb.Matches(data, "tui.editor.deleteToLineStart") {
		in.deleteToLineStart()
		return
	}
	if kb.Matches(data, "tui.editor.deleteToLineEnd") {
		in.deleteToLineEnd()
		return
	}

	if kb.Matches(data, "tui.editor.yank") {
		in.yank()
		return
	}
	if kb.Matches(data, "tui.editor.yankPop") {
		in.yankPop()
		return
	}

	if kb.Matches(data, "tui.editor.cursorLeft") {
		in.lastAction = ""
		if in.cursor > 0 {
			in.cursor = lastGraphemeStart(in.value, in.cursor)
		}
		return
	}
	if kb.Matches(data, "tui.editor.cursorRight") {
		in.lastAction = ""
		if in.cursor < len(in.value) {
			in.cursor = firstGraphemeEnd(in.value, in.cursor)
		}
		return
	}
	if kb.Matches(data, "tui.editor.cursorLineStart") {
		in.lastAction = ""
		in.cursor = 0
		return
	}
	if kb.Matches(data, "tui.editor.cursorLineEnd") {
		in.lastAction = ""
		in.cursor = len(in.value)
		return
	}
	if kb.Matches(data, "tui.editor.cursorWordLeft") {
		in.moveWordBackwards()
		return
	}
	if kb.Matches(data, "tui.editor.cursorWordRight") {
		in.moveWordForwards()
		return
	}

	// Kitty CSI-u printable character (flag 1 sends CSI-u for all keys,
	// including plain printable characters). Decode before the control-char
	// check since the sequence contains ESC.
	if printable, ok := DecodeKittyPrintable(data); ok {
		in.insertCharacter(printable)
		return
	}

	// Regular character input: accept printable characters including Unicode,
	// but reject control characters (C0 0x00-0x1F, DEL 0x7F, C1 0x80-0x9F).
	for _, r := range data {
		if r < 32 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			return
		}
	}
	in.insertCharacter(data)
}

// HandleMouse places the cursor at the clicked column.
func (in *Input) HandleMouse(event TuiMouseEvent) *TuiMouseDispatchResult {
	if event.Type != MousePress || event.Button != MouseButtonLeft || event.Y != 0 {
		return nil
	}
	visibleColumn := max(0, event.X-2)
	targetColumn := in.renderedStartColumn + visibleColumn

	currentColumn := 0
	in.cursor = len(in.value)
	offset := 0
	for _, grapheme := range segmentGraphemes(in.value) {
		nextColumn := currentColumn + VisibleWidth(grapheme)
		if targetColumn < nextColumn {
			in.cursor = offset
			break
		}
		currentColumn = nextColumn
		offset += len(grapheme)
	}
	in.lastAction = ""
	return &TuiMouseDispatchResult{TuiMouseEventResult: TuiMouseEventResult{Handled: true, Focus: true}}
}

func (in *Input) insertCharacter(char string) {
	// Undo coalescing: consecutive word characters coalesce into one unit.
	if IsWhitespaceChar(char) || in.lastAction != "type-word" {
		in.pushUndo()
	}
	in.lastAction = "type-word"

	in.value = in.value[:in.cursor] + char + in.value[in.cursor:]
	in.cursor += len(char)
}

func (in *Input) handleBackspace() {
	in.lastAction = ""
	if in.cursor > 0 {
		in.pushUndo()
		start := lastGraphemeStart(in.value, in.cursor)
		in.value = in.value[:start] + in.value[in.cursor:]
		in.cursor = start
	}
}

func (in *Input) handleForwardDelete() {
	in.lastAction = ""
	if in.cursor < len(in.value) {
		in.pushUndo()
		end := firstGraphemeEnd(in.value, in.cursor)
		in.value = in.value[:in.cursor] + in.value[end:]
	}
}

func (in *Input) deleteToLineStart() {
	if in.cursor == 0 {
		return
	}
	in.pushUndo()
	deletedText := in.value[:in.cursor]
	in.killRing.Push(deletedText, KillRingPushOptions{Prepend: true, Accumulate: in.lastAction == "kill"})
	in.lastAction = "kill"
	in.value = in.value[in.cursor:]
	in.cursor = 0
}

func (in *Input) deleteToLineEnd() {
	if in.cursor >= len(in.value) {
		return
	}
	in.pushUndo()
	deletedText := in.value[in.cursor:]
	in.killRing.Push(deletedText, KillRingPushOptions{Prepend: false, Accumulate: in.lastAction == "kill"})
	in.lastAction = "kill"
	in.value = in.value[:in.cursor]
}

func (in *Input) deleteWordBackwards() {
	if in.cursor == 0 {
		return
	}
	// Save lastAction before cursor movement (moveWordBackwards resets it).
	wasKill := in.lastAction == "kill"
	in.pushUndo()

	oldCursor := in.cursor
	in.moveWordBackwards()
	deleteFrom := in.cursor
	in.cursor = oldCursor

	deletedText := in.value[deleteFrom:in.cursor]
	in.killRing.Push(deletedText, KillRingPushOptions{Prepend: true, Accumulate: wasKill})
	in.lastAction = "kill"

	in.value = in.value[:deleteFrom] + in.value[in.cursor:]
	in.cursor = deleteFrom
}

func (in *Input) deleteWordForward() {
	if in.cursor >= len(in.value) {
		return
	}
	wasKill := in.lastAction == "kill"
	in.pushUndo()

	oldCursor := in.cursor
	in.moveWordForwards()
	deleteTo := in.cursor
	in.cursor = oldCursor

	deletedText := in.value[in.cursor:deleteTo]
	in.killRing.Push(deletedText, KillRingPushOptions{Prepend: false, Accumulate: wasKill})
	in.lastAction = "kill"

	in.value = in.value[:in.cursor] + in.value[deleteTo:]
}

func (in *Input) yank() {
	text, ok := in.killRing.Peek()
	if !ok {
		return
	}
	in.pushUndo()
	in.value = in.value[:in.cursor] + text + in.value[in.cursor:]
	in.cursor += len(text)
	in.lastAction = "yank"
}

func (in *Input) yankPop() {
	if in.lastAction != "yank" || in.killRing.Len() <= 1 {
		return
	}
	in.pushUndo()

	prevText, _ := in.killRing.Peek()
	in.value = in.value[:in.cursor-len(prevText)] + in.value[in.cursor:]
	in.cursor -= len(prevText)

	in.killRing.Rotate()
	text, _ := in.killRing.Peek()
	in.value = in.value[:in.cursor] + text + in.value[in.cursor:]
	in.cursor += len(text)
	in.lastAction = "yank"
}

func (in *Input) pushUndo() {
	in.undoStack.Push(inputState{value: in.value, cursor: in.cursor})
}

func (in *Input) undo() {
	snapshot, ok := in.undoStack.Pop()
	if !ok {
		return
	}
	in.value = snapshot.value
	in.cursor = snapshot.cursor
	in.lastAction = ""
}

func (in *Input) moveWordBackwards() {
	if in.cursor == 0 {
		return
	}
	in.lastAction = ""
	in.cursor = FindWordBackward(in.value, in.cursor)
}

func (in *Input) moveWordForwards() {
	if in.cursor >= len(in.value) {
		return
	}
	in.lastAction = ""
	in.cursor = FindWordForward(in.value, in.cursor)
}

func (in *Input) handlePaste(pastedText string) {
	in.lastAction = ""
	in.pushUndo()

	// Remove newlines/carriage returns; tabs become four spaces.
	cleanText := strings.ReplaceAll(pastedText, "\r\n", "")
	cleanText = strings.ReplaceAll(cleanText, "\r", "")
	cleanText = strings.ReplaceAll(cleanText, "\n", "")
	cleanText = strings.ReplaceAll(cleanText, "\t", "    ")

	in.value = in.value[:in.cursor] + cleanText + in.value[in.cursor:]
	in.cursor += len(cleanText)
}

// Render renders the input line with the fake cursor and horizontal scrolling.
func (in *Input) Render(width int) []string {
	availableWidth := width - VisibleWidth(in.prompt)

	if availableWidth <= 0 {
		return []string{TruncateToWidth(in.prompt, width, "", false)}
	}

	if len(in.value) == 0 && in.placeholder != "" {
		placeholder := TruncateToWidth(in.placeholder, availableWidth, "", false)
		graphemes := segmentGraphemes(placeholder)
		atCursor := " "
		if len(graphemes) > 0 {
			atCursor = graphemes[0]
		}
		afterCursor := placeholder[len(atCursor):]
		marker := ""
		if in.focused {
			marker = CursorMarker
		}
		cursorChar := "\x1b[7m" + in.placeholderStyle(atCursor) + "\x1b[27m"
		textWithCursor := marker + cursorChar + in.placeholderStyle(afterCursor)
		padding := repeatSpaces(max(0, availableWidth-VisibleWidth(textWithCursor)))
		return []string{in.prompt + textWithCursor + padding}
	}

	visibleText := ""
	cursorDisplay := in.cursor
	in.renderedStartColumn = 0
	totalWidth := VisibleWidth(in.value)

	if totalWidth < availableWidth {
		// Everything fits (leave room for the cursor at the end).
		visibleText = in.value
	} else {
		// Horizontal scrolling: reserve a column for the end cursor.
		scrollWidth := availableWidth
		if in.cursor == len(in.value) {
			scrollWidth = availableWidth - 1
		}
		cursorCol := VisibleWidth(in.value[:in.cursor])

		if scrollWidth > 0 {
			halfWidth := scrollWidth / 2
			startCol := 0
			switch {
			case cursorCol < halfWidth:
				startCol = 0
			case cursorCol > totalWidth-halfWidth:
				startCol = max(0, totalWidth-scrollWidth)
			default:
				startCol = max(0, cursorCol-halfWidth)
			}

			in.renderedStartColumn = startCol
			visibleText = SliceByColumn(in.value, startCol, scrollWidth, true)
			beforeCursor := SliceByColumn(in.value, startCol, max(0, cursorCol-startCol), true)
			cursorDisplay = len(beforeCursor)
		} else {
			visibleText = ""
			cursorDisplay = 0
		}
	}

	// Insert the cursor character at the cursor position.
	graphemes := segmentGraphemes(visibleText[cursorDisplay:])
	atCursor := " "
	if len(graphemes) > 0 {
		atCursor = graphemes[0]
	}

	beforeCursor := visibleText[:cursorDisplay]
	afterCursor := sliceFrom(visibleText, cursorDisplay+len(atCursor))

	marker := ""
	if in.focused {
		marker = CursorMarker
	}

	cursorChar := "\x1b[7m" + atCursor + "\x1b[27m"
	textWithCursor := beforeCursor + marker + cursorChar + afterCursor

	padding := repeatSpaces(max(0, availableWidth-VisibleWidth(textWithCursor)))
	return []string{in.prompt + textWithCursor + padding}
}

// sliceFrom returns text[offset:] or "" when the offset is out of range
// (JS String.slice clamps; divergence D61).
func sliceFrom(text string, offset int) string {
	if offset < 0 || offset >= len(text) {
		return ""
	}
	return text[offset:]
}

// lastGraphemeStart returns the byte offset of the last grapheme ending at
// cursor.
func lastGraphemeStart(value string, cursor int) int {
	start := 0
	offset := 0
	for _, grapheme := range segmentGraphemes(value) {
		offset += len(grapheme)
		if offset >= cursor {
			return start
		}
		start = offset
	}
	if cursor > 0 {
		return max(0, cursor-1)
	}
	return 0
}

// firstGraphemeEnd returns the byte offset after the grapheme starting at
// cursor.
func firstGraphemeEnd(value string, cursor int) int {
	offset := 0
	for _, grapheme := range segmentGraphemes(value) {
		next := offset + len(grapheme)
		if next > cursor {
			return next
		}
		offset = next
	}
	if cursor < len(value) {
		return cursor + 1
	}
	return len(value)
}

var _ Component = (*Input)(nil)
var _ Focusable = (*Input)(nil)
var _ MouseHandler = (*Input)(nil)
