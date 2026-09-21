package tui

import (
	"os"
	"strconv"
	"strings"
	"time"
)

// Port of src/tui-main-screen.ts: the differential renderer that writes into
// the terminal's main screen and scrollback.
//
// Divergences: the surrogate-pair-aware 1 MiB chunking becomes rune-aware
// (Go strings are UTF-8: D78); the over-wide-line path writes the crash log and
// reports through OnRenderError instead of throwing (D79); the debug render
// dumps gated on PI_TUI_DEBUG are omitted.

const maxRenderWriteChars = 1024 * 1024

// BoundedTerminalWriter streams terminal output in bounded chunks.
type BoundedTerminalWriter struct {
	buffer       strings.Builder
	writtenChars int
	write        func(data string)
}

// NewBoundedTerminalWriter creates a writer over the given sink.
func NewBoundedTerminalWriter(write func(data string)) *BoundedTerminalWriter {
	return &BoundedTerminalWriter{write: write}
}

// Append appends terminal data, flushing full chunks as needed.
func (w *BoundedTerminalWriter) Append(value string) {
	offset := 0
	for offset < len(value) {
		capacity := maxRenderWriteChars - w.buffer.Len()
		if capacity == 0 {
			w.Flush()
			continue
		}

		end := len(value)
		if offset+capacity < end {
			end = offset + capacity
			// Do not split a rune.
			for end > offset && !utf8StartByte(value[end]) {
				end--
			}
		}
		if end == offset {
			w.Flush()
			continue
		}

		w.buffer.WriteString(value[offset:end])
		offset = end
		if w.buffer.Len() == maxRenderWriteChars {
			w.Flush()
		}
	}
}

// Flush writes the current chunk.
func (w *BoundedTerminalWriter) Flush() {
	if w.buffer.Len() == 0 {
		return
	}
	data := w.buffer.String()
	w.write(data)
	w.writtenChars += len(data)
	w.buffer.Reset()
}

// Len returns the number of characters written plus buffered.
func (w *BoundedTerminalWriter) Len() int { return w.writtenChars + w.buffer.Len() }

// utf8StartByte reports whether b starts a UTF-8 rune.
func utf8StartByte(b byte) bool { return b&0xC0 != 0x80 }

type kittyImageHeader struct {
	IDs  []int
	Rows int
}

func parseKittyImageHeader(line string) (kittyImageHeader, bool) {
	sequenceStart := indexOf(line, kittyPrefix)
	if sequenceStart == -1 {
		return kittyImageHeader{}, false
	}
	paramsStart := sequenceStart + len(kittyPrefix)
	paramsEnd := strings.IndexByte(line[paramsStart:], ';')
	if paramsEnd == -1 {
		return kittyImageHeader{}, false
	}
	paramsEnd += paramsStart

	header := kittyImageHeader{Rows: 1}
	for _, param := range strings.Split(line[paramsStart:paramsEnd], ",") {
		key, value, ok := strings.Cut(param, "=")
		if !ok {
			continue
		}
		numberValue, err := strconv.Atoi(value)
		if err != nil || numberValue <= 0 || numberValue > 0xffffffff {
			continue
		}
		switch key {
		case "i":
			header.IDs = append(header.IDs, numberValue)
		case "r":
			header.Rows = numberValue
		}
	}
	return header, true
}

func extractKittyImageIDs(line string) []int {
	header, ok := parseKittyImageHeader(line)
	if !ok {
		return nil
	}
	return header.IDs
}

func extractKittyImageRows(line string) int {
	header, ok := parseKittyImageHeader(line)
	if !ok {
		return 1
	}
	return header.Rows
}

// DeleteKittyImage builds the escape that deletes a placed image.
func DeleteKittyImage(imageID int) string {
	return "\x1b_Ga=d,d=I,i=" + strconv.Itoa(imageID) + ",q=2\x1b\\"
}

func isTermuxSession() bool { return os.Getenv("TERMUX_VERSION") != "" }

// MainScreenRenderState is the captured differential-render state.
type MainScreenRenderState struct {
	PreviousLines       []string
	PreviousWidth       int
	PreviousHeight      int
	CursorRow           int
	HardwareCursorRow   int
	MaxLinesRendered    int
	PreviousViewportTop int
}

// MainScreen renders into the terminal's main screen and scrollback.
type MainScreen struct {
	*Renderer

	previousLines         []string
	previousKittyImageIDs map[int]bool
	previousWidth         int
	previousHeight        int
	cursorRow             int
	hardwareCursorRow     int
	maxLinesRendered      int
	previousViewportTop   int

	// OnRenderError is called when a rendered line exceeds the terminal width;
	// upstream throws after writing the crash log (D79).
	OnRenderError func(err error)
}

// NewMainScreen creates the main-screen renderer.
func NewMainScreen(terminal Terminal, showHardwareCursor bool, logDirectory string) *MainScreen {
	screen := &MainScreen{
		Renderer:              NewRenderer(terminal),
		previousKittyImageIDs: map[int]bool{},
	}
	screen.ShowHardwareCursor = showHardwareCursor
	screen.LogDirectory = logDirectory
	screen.DoRender = screen.doRender
	screen.OnResetRenderState = screen.resetRenderState
	screen.OnBeforeTerminalStop = screen.beforeTerminalStop
	return screen
}

// CaptureRenderState snapshots the render state.
func (s *MainScreen) CaptureRenderState() MainScreenRenderState {
	return MainScreenRenderState{
		PreviousLines:       append([]string(nil), s.previousLines...),
		PreviousWidth:       s.previousWidth,
		PreviousHeight:      s.previousHeight,
		CursorRow:           s.cursorRow,
		HardwareCursorRow:   s.hardwareCursorRow,
		MaxLinesRendered:    s.maxLinesRendered,
		PreviousViewportTop: s.previousViewportTop,
	}
}

// RestoreRenderState restores a snapshot, clearing image lines (their
// placements are gone).
func (s *MainScreen) RestoreRenderState(state MainScreenRenderState) {
	s.previousLines = make([]string, 0, len(state.PreviousLines))
	for _, line := range state.PreviousLines {
		if IsImageLine(line) {
			s.previousLines = append(s.previousLines, "")
			continue
		}
		s.previousLines = append(s.previousLines, line)
	}
	s.previousKittyImageIDs = map[int]bool{}
	s.previousWidth = state.PreviousWidth
	s.previousHeight = state.PreviousHeight
	s.cursorRow = state.CursorRow
	s.hardwareCursorRow = state.HardwareCursorRow
	s.maxLinesRendered = state.MaxLinesRendered
	s.previousViewportTop = state.PreviousViewportTop
}

func (s *MainScreen) resetRenderState() {
	s.previousLines = nil
	s.previousWidth = -1
	s.previousHeight = -1
	s.cursorRow = 0
	s.hardwareCursorRow = 0
	s.maxLinesRendered = 0
	s.previousViewportTop = 0
}

func (s *MainScreen) beforeTerminalStop(options TuiStopOptions) {
	// The transcript-replay reads render-mutated state; serialize against an
	// in-flight timer render, which writes the same fields under s.mu.
	s.mu.Lock()
	defer s.mu.Unlock()
	if options.PreserveScreen || len(s.previousLines) == 0 {
		return
	}
	s.Terminal.Write(" ")
	targetRow := len(s.previousLines)
	lineDiff := targetRow - s.hardwareCursorRow
	if lineDiff > 0 {
		s.Terminal.Write("\x1b[" + strconv.Itoa(lineDiff) + "B")
	} else if lineDiff < 0 {
		s.Terminal.Write("\x1b[" + strconv.Itoa(-lineDiff) + "A")
	}
	s.Terminal.Write("\r\n")
}

func (s *MainScreen) collectKittyImageIDs(lines []string) map[int]bool {
	ids := map[int]bool{}
	for _, line := range lines {
		for _, id := range extractKittyImageIDs(line) {
			ids[id] = true
		}
	}
	return ids
}

func (s *MainScreen) deleteKittyImages(ids map[int]bool) string {
	var builder strings.Builder
	ordered := make([]int, 0, len(ids))
	for id := range ids {
		ordered = append(ordered, id)
	}
	sortInts(ordered)
	for _, id := range ordered {
		builder.WriteString(DeleteKittyImage(id))
	}
	return builder.String()
}

func (s *MainScreen) getKittyImageReservedRows(lines []string, index int, maxIndex int) int {
	if index < 0 || index >= len(lines) {
		return 1
	}
	rows := extractKittyImageRows(lines[index])
	if rows <= 1 {
		return 1
	}
	limit := maxInt(1, minInt(rows, minInt(maxIndex-index+1, len(lines)-index)))
	reservedRows := 1
	for reservedRows < limit {
		next := ""
		if index+reservedRows < len(lines) {
			next = lines[index+reservedRows]
		}
		if IsImageLine(next) || VisibleWidth(next) > 0 {
			break
		}
		reservedRows++
	}
	return reservedRows
}

func (s *MainScreen) expandChangedRangeForKittyImages(firstChanged int, lastChanged int, newLines []string) (int, int) {
	expandedFirst := firstChanged
	expandedLast := lastChanged
	expandForLines := func(lines []string) {
		for i := range lines {
			if len(extractKittyImageIDs(lines[i])) == 0 {
				continue
			}
			blockEnd := i + s.getKittyImageReservedRows(lines, i, len(lines)-1) - 1
			if i >= firstChanged || (i <= lastChanged && blockEnd >= firstChanged) {
				expandedFirst = minInt(expandedFirst, i)
				expandedLast = maxInt(expandedLast, blockEnd)
			}
		}
	}
	expandForLines(s.previousLines)
	expandForLines(newLines)
	return expandedFirst, expandedLast
}

func (s *MainScreen) deleteChangedKittyImages(firstChanged int, lastChanged int) string {
	if firstChanged < 0 || lastChanged < firstChanged {
		return ""
	}
	ids := map[int]bool{}
	maxLine := minInt(lastChanged, len(s.previousLines)-1)
	for i := firstChanged; i <= maxLine; i++ {
		for _, id := range extractKittyImageIDs(s.previousLines[i]) {
			ids[id] = true
		}
	}
	return s.deleteKittyImages(ids)
}

func (s *MainScreen) doRender() {
	s.mu.Lock()
	stopped := s.stopped
	s.mu.Unlock()
	if stopped {
		return
	}
	width := s.Terminal.Columns()
	height := s.Terminal.Rows()
	widthChanged := s.previousWidth != 0 && s.previousWidth != width
	heightChanged := s.previousHeight != 0 && s.previousHeight != height
	previousBufferLength := height
	if s.previousHeight > 0 {
		previousBufferLength = s.previousViewportTop + s.previousHeight
	}
	prevViewportTop := s.previousViewportTop
	if heightChanged {
		prevViewportTop = maxInt(0, previousBufferLength-height)
	}
	viewportTop := prevViewportTop
	hardwareCursorRow := s.hardwareCursorRow
	computeLineDiff := func(targetRow int) int {
		currentScreenRow := hardwareCursorRow - prevViewportTop
		targetScreenRow := targetRow - viewportTop
		return targetScreenRow - currentScreenRow
	}

	newLines := s.renderLocked(width)

	if s.hasOverlayEntriesLocked() {
		newLines = s.CompositeOverlays(newLines, width, height)
	}

	cursorRow, cursorCol, hasCursor := s.ExtractCursorPosition(newLines, height)
	cursorPos := struct {
		Row, Col int
		Has      bool
	}{cursorRow, cursorCol, hasCursor}

	newLines = s.ApplyLineResets(newLines)

	fullRender := func(clear bool) {
		s.fullRedrawCount++
		output := NewBoundedTerminalWriter(func(data string) { s.Terminal.Write(data) })
		output.Append("\x1b[?2026h") // Begin synchronized output
		if clear {
			output.Append(s.deleteKittyImages(s.previousKittyImageIDs))
			output.Append("\x1b[2J\x1b[H\x1b[3J") // Clear screen, home, clear scrollback
		}
		for i := 0; i < len(newLines); i++ {
			if i > 0 {
				output.Append("\r\n")
			}
			line := newLines[i]
			isImage := IsImageLine(line)
			imageReservedRows := 1
			if isImage {
				imageReservedRows = s.getKittyImageReservedRows(newLines, i, len(newLines)-1)
			}
			if imageReservedRows > 1 && imageReservedRows <= height {
				for row := 1; row < imageReservedRows; row++ {
					output.Append("\r\n")
				}
				output.Append("\x1b[" + strconv.Itoa(imageReservedRows-1) + "A")
				output.Append(line)
				output.Append("\x1b[" + strconv.Itoa(imageReservedRows-1) + "B")
				i += imageReservedRows - 1
				continue
			}
			output.Append(line)
		}
		output.Append("\x1b[?2026l") // End synchronized output
		output.Flush()
		s.cursorRow = maxInt(0, len(newLines)-1)
		s.hardwareCursorRow = s.cursorRow
		if clear {
			s.maxLinesRendered = len(newLines)
		} else {
			s.maxLinesRendered = maxInt(s.maxLinesRendered, len(newLines))
		}
		bufferLength := maxInt(height, len(newLines))
		// Commit the render state under s.mu: the stop path reads it
		// concurrently with in-flight timer renders.
		s.mu.Lock()
		s.previousViewportTop = maxInt(0, bufferLength-height)
		s.positionHardwareCursor(cursorPos.Row, cursorPos.Col, cursorPos.Has, len(newLines))
		s.previousLines = newLines
		s.previousKittyImageIDs = s.collectKittyImageIDs(newLines)
		s.previousWidth = width
		s.previousHeight = height
		s.mu.Unlock()
	}

	// First render: output everything without clearing (assumes a clean screen).
	if len(s.previousLines) == 0 && !widthChanged && !heightChanged {
		fullRender(false)
		return
	}
	if widthChanged {
		fullRender(true)
		return
	}
	// Height changes need a full re-render to keep the viewport aligned, but
	// Termux changes height when the software keyboard toggles.
	if heightChanged && !isTermuxSession() {
		fullRender(true)
		return
	}
	if s.ClearOnShrink && len(newLines) < s.maxLinesRendered && !s.hasOverlayEntriesLocked() {
		fullRender(true)
		return
	}

	firstChanged := -1
	lastChanged := -1
	maxLines := maxInt(len(newLines), len(s.previousLines))
	for i := 0; i < maxLines; i++ {
		oldLine := ""
		if i < len(s.previousLines) {
			oldLine = s.previousLines[i]
		}
		newLine := ""
		if i < len(newLines) {
			newLine = newLines[i]
		}
		if oldLine != newLine {
			if firstChanged == -1 {
				firstChanged = i
			}
			lastChanged = i
		}
	}
	appendedLines := len(newLines) > len(s.previousLines)
	if appendedLines {
		if firstChanged == -1 {
			firstChanged = len(s.previousLines)
		}
		lastChanged = len(newLines) - 1
	}
	if firstChanged != -1 {
		firstChanged, lastChanged = s.expandChangedRangeForKittyImages(firstChanged, lastChanged, newLines)
	}
	appendStart := appendedLines && firstChanged == len(s.previousLines) && firstChanged > 0

	if firstChanged == -1 {
		s.positionHardwareCursor(cursorPos.Row, cursorPos.Col, cursorPos.Has, len(newLines))
		s.previousViewportTop = prevViewportTop
		s.previousHeight = height
		return
	}

	// All changes are in deleted lines (nothing to render, just clear).
	if firstChanged >= len(newLines) {
		if len(s.previousLines) > len(newLines) {
			output := NewBoundedTerminalWriter(func(data string) { s.Terminal.Write(data) })
			output.Append("\x1b[?2026h")
			output.Append(s.deleteChangedKittyImages(firstChanged, lastChanged))
			targetRow := maxInt(0, len(newLines)-1)
			if targetRow < prevViewportTop {
				fullRender(true)
				return
			}
			lineDiff := computeLineDiff(targetRow)
			if lineDiff > 0 {
				output.Append("\x1b[" + strconv.Itoa(lineDiff) + "B")
			} else if lineDiff < 0 {
				output.Append("\x1b[" + strconv.Itoa(-lineDiff) + "A")
			}
			output.Append("\r")
			extraLines := len(s.previousLines) - len(newLines)
			if extraLines > height {
				fullRender(true)
				return
			}
			clearStartOffset := 1
			if len(newLines) == 0 {
				clearStartOffset = 0
			}
			if extraLines > 0 && clearStartOffset > 0 {
				output.Append("\x1b[" + strconv.Itoa(clearStartOffset) + "B")
			}
			for i := 0; i < extraLines; i++ {
				output.Append("\r\x1b[2K")
				if i < extraLines-1 {
					output.Append("\x1b[1B")
				}
			}
			moveBack := maxInt(0, extraLines-1+clearStartOffset)
			if moveBack > 0 {
				output.Append("\x1b[" + strconv.Itoa(moveBack) + "A")
			}
			output.Append("\x1b[?2026l")
			output.Flush()
			s.cursorRow = targetRow
			s.hardwareCursorRow = targetRow
		}
		s.mu.Lock()
		s.positionHardwareCursor(cursorPos.Row, cursorPos.Col, cursorPos.Has, len(newLines))
		s.previousLines = newLines
		s.previousKittyImageIDs = s.collectKittyImageIDs(newLines)
		s.previousWidth = width
		s.previousHeight = height
		s.previousViewportTop = prevViewportTop
		s.mu.Unlock()
		return
	}

	// Differential rendering can only touch what was visible.
	if firstChanged < prevViewportTop {
		fullRender(true)
		return
	}

	output := NewBoundedTerminalWriter(func(data string) { s.Terminal.Write(data) })
	output.Append("\x1b[?2026h")
	output.Append(s.deleteChangedKittyImages(firstChanged, lastChanged))
	prevViewportBottom := prevViewportTop + height - 1
	moveTargetRow := firstChanged
	if appendStart {
		moveTargetRow = firstChanged - 1
	}
	if moveTargetRow > prevViewportBottom {
		currentScreenRow := maxInt(0, minInt(height-1, hardwareCursorRow-prevViewportTop))
		moveToBottom := height - 1 - currentScreenRow
		if moveToBottom > 0 {
			output.Append("\x1b[" + strconv.Itoa(moveToBottom) + "B")
		}
		scroll := moveTargetRow - prevViewportBottom
		output.Append(strings.Repeat("\r\n", scroll))
		prevViewportTop += scroll
		viewportTop += scroll
		hardwareCursorRow = moveTargetRow
	}

	lineDiff := computeLineDiff(moveTargetRow)
	if lineDiff > 0 {
		output.Append("\x1b[" + strconv.Itoa(lineDiff) + "B")
	} else if lineDiff < 0 {
		output.Append("\x1b[" + strconv.Itoa(-lineDiff) + "A")
	}
	if appendStart {
		output.Append("\r\n")
	} else {
		output.Append("\r")
	}

	renderEnd := minInt(lastChanged, len(newLines)-1)
	for i := firstChanged; i <= renderEnd; i++ {
		if i > firstChanged {
			output.Append("\r\n")
		}
		line := newLines[i]
		isImage := IsImageLine(line)
		imageReservedRows := 1
		if isImage {
			imageReservedRows = s.getKittyImageReservedRows(newLines, i, renderEnd)
		}
		if imageReservedRows > 1 {
			imageStartScreenRow := i - viewportTop
			if imageStartScreenRow < 0 || imageStartScreenRow+imageReservedRows > height {
				fullRender(true)
				return
			}
			output.Append("\x1b[2K")
			for row := 1; row < imageReservedRows; row++ {
				output.Append("\r\n\x1b[2K")
			}
			output.Append("\x1b[" + strconv.Itoa(imageReservedRows-1) + "A")
			output.Append(line)
			output.Append("\x1b[" + strconv.Itoa(imageReservedRows-1) + "B")
			i += imageReservedRows - 1
			continue
		}

		output.Append("\x1b[2K")
		if !isImage && VisibleWidth(line) > width {
			if err := s.reportOverwideLine(newLines, i, width); err != nil {
				return
			}
		}
		output.Append(line)
	}

	finalCursorRow := renderEnd
	if len(s.previousLines) > len(newLines) {
		if renderEnd < len(newLines)-1 {
			moveDown := len(newLines) - 1 - renderEnd
			output.Append("\x1b[" + strconv.Itoa(moveDown) + "B")
			finalCursorRow = len(newLines) - 1
		}
		extraLines := len(s.previousLines) - len(newLines)
		for i := len(newLines); i < len(s.previousLines); i++ {
			output.Append("\r\n\x1b[2K")
		}
		output.Append("\x1b[" + strconv.Itoa(extraLines) + "A")
	}

	output.Append("\x1b[?2026l")
	output.Flush()

	s.mu.Lock()
	s.cursorRow = maxInt(0, len(newLines)-1)
	s.hardwareCursorRow = finalCursorRow
	s.maxLinesRendered = maxInt(s.maxLinesRendered, len(newLines))
	s.previousViewportTop = maxInt(prevViewportTop, finalCursorRow-height+1)

	s.positionHardwareCursor(cursorPos.Row, cursorPos.Col, cursorPos.Has, len(newLines))

	s.previousLines = newLines
	s.previousKittyImageIDs = s.collectKittyImageIDs(newLines)
	s.previousWidth = width
	s.previousHeight = height
	s.mu.Unlock()
}

// reportOverwideLine writes the crash log and reports the error (upstream
// throws; D79).
func (s *MainScreen) reportOverwideLine(lines []string, index int, width int) error {
	crashLogPath := s.LogDirectory
	if crashLogPath == "" {
		crashLogPath = os.TempDir()
	}
	crashLogPath = strings.TrimRight(crashLogPath, "/") + "/pi-tui-crash.log"

	var builder strings.Builder
	builder.WriteString("Crash at " + time.Now().UTC().Format(time.RFC3339Nano) + "\n")
	builder.WriteString("Terminal width: " + strconv.Itoa(width) + "\n")
	builder.WriteString("Line " + strconv.Itoa(index) + " visible width: " + strconv.Itoa(VisibleWidth(lines[index])) + "\n")
	builder.WriteString("\n=== All rendered lines ===\n")
	for i, line := range lines {
		builder.WriteString("[" + strconv.Itoa(i) + "] (w=" + strconv.Itoa(VisibleWidth(line)) + ") " + line + "\n")
	}
	builder.WriteString("\n")
	_ = os.MkdirAll(strings.TrimSuffix(crashLogPath, "/pi-tui-crash.log"), 0o755)
	_ = os.WriteFile(crashLogPath, []byte(builder.String()), 0o644)

	s.Stop(TuiStopOptions{})
	err := &overwideLineError{index: index, width: VisibleWidth(lines[index]), terminal: width, logPath: crashLogPath}
	if s.OnRenderError != nil {
		s.OnRenderError(err)
	}
	return err
}

// overwideLineError reports a rendered line wider than the terminal.
type overwideLineError struct {
	index    int
	width    int
	terminal int
	logPath  string
}

func (e *overwideLineError) Error() string {
	return "Rendered line " + strconv.Itoa(e.index) + " exceeds terminal width (" +
		strconv.Itoa(e.width) + " > " + strconv.Itoa(e.terminal) + ").\n\n" +
		"This is likely caused by a custom TUI component not truncating its output.\n" +
		"Use VisibleWidth() to measure and TruncateToWidth() to truncate lines.\n\n" +
		"Debug log written to: " + e.logPath
}

func (s *MainScreen) positionHardwareCursor(row int, col int, hasCursor bool, totalLines int) {
	if !hasCursor || totalLines <= 0 {
		s.Terminal.HideCursor()
		return
	}
	targetRow := maxInt(0, minInt(row, totalLines-1))
	targetCol := maxInt(0, col)

	rowDelta := targetRow - s.hardwareCursorRow
	var builder strings.Builder
	if rowDelta > 0 {
		builder.WriteString("\x1b[" + strconv.Itoa(rowDelta) + "B")
	} else if rowDelta < 0 {
		builder.WriteString("\x1b[" + strconv.Itoa(-rowDelta) + "A")
	}
	builder.WriteString("\x1b[" + strconv.Itoa(targetCol+1) + "G")
	if builder.Len() > 0 {
		s.Terminal.Write(builder.String())
	}

	s.hardwareCursorRow = targetRow
	if s.ShowHardwareCursor {
		s.Terminal.ShowCursor()
	} else {
		s.Terminal.HideCursor()
	}
}

// renderLocked renders the component tree under the renderer lock.
func (s *MainScreen) renderLocked(width int) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Container.Render(width)
}

func (s *MainScreen) hasOverlayEntriesLocked() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.overlayStack) > 0
}

var _ Component = (*MainScreen)(nil)
