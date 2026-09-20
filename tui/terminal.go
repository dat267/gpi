package tui

import (
	"os"
	"regexp"
	"strconv"
	"sync"
	"time"

	"golang.org/x/term"
)

// compileRegex caches the small set of patterns used during input handling.
var regexCache sync.Map

func compileRegex(pattern string) *regexp.Regexp {
	if cached, ok := regexCache.Load(pattern); ok {
		return cached.(*regexp.Regexp)
	}
	compiled := regexp.MustCompile(pattern)
	regexCache.Store(pattern, compiled)
	return compiled
}

// Port of src/terminal.ts: the Terminal interface implementation over
// os.Stdin/os.Stdout, the Kitty keyboard protocol negotiation with the
// modifyOtherKeys fallback, and the input helpers.
//
// Out of scope: the native platform helpers (enableWindowsVTInput,
// isNativeModifierPressed) — divergence D52; native Shift+Enter detection is
// disabled, the sequence normalization hook stays. Stdin cannot be paused in
// Go, so Stop discards buffered input instead of pausing it (divergence D50).

const (
	terminalProgressKeepaliveMS               = 1000
	terminalProgressActiveSequence            = "\x1b]9;4;3\x07"
	terminalProgressClearSequence             = "\x1b]9;4;0\x07"
	nativeShiftEnterSequence                  = "\x1b[13;2u"
	desiredKittyKeyboardProtocolFlag          = 7
	keyboardProtocolResponseFragmentTimeoutMS = 150
)

// kittyKeyboardProtocolQuery is built from desiredKittyKeyboardProtocolFlag.
var kittyKeyboardProtocolQuery = "\x1b[>" + strconv.Itoa(desiredKittyKeyboardProtocolFlag) + "u\x1b[?u\x1b[c"

const (
	defaultEscapeTimeoutMSValue = 10
	defaultSSHEscapeTimeoutMS   = 100
)

// KeyboardProtocolNegotiationSequenceKind discriminates the negotiation union.
type KeyboardProtocolNegotiationSequenceKind string

const (
	NegotiationKittyFlags       KeyboardProtocolNegotiationSequenceKind = "kitty-flags"
	NegotiationDeviceAttributes KeyboardProtocolNegotiationSequenceKind = "device-attributes"
)

// KeyboardProtocolNegotiationSequence is a parsed terminal response to the
// keyboard protocol query.
type KeyboardProtocolNegotiationSequence struct {
	Kind  KeyboardProtocolNegotiationSequenceKind
	Flags int
}

// ParseKeyboardProtocolNegotiationSequence parses a negotiation response.
func ParseKeyboardProtocolNegotiationSequence(sequence string) *KeyboardProtocolNegotiationSequence {
	if flags, ok := matchRegexPrefix(sequence, `^\x1b\[\?(\d+)u$`); ok {
		return &KeyboardProtocolNegotiationSequence{Kind: NegotiationKittyFlags, Flags: flags}
	}
	if matchesRegex(sequence, `^\x1b\[\?[\d;]*c$`) {
		return &KeyboardProtocolNegotiationSequence{Kind: NegotiationDeviceAttributes}
	}
	return nil
}

func isKeyboardProtocolNegotiationSequencePrefix(sequence string) bool {
	return sequence == "\x1b[" || matchesRegex(sequence, `^\x1b\[\?[\d;]*$`)
}

// IsAppleTerminalSession reports whether the terminal is Apple Terminal on
// darwin.
func IsAppleTerminalSession() bool {
	return isDarwin() && os.Getenv("TERM_PROGRAM") == "Apple_Terminal"
}

// RefreshTerminalDimensions sends SIGWINCH to this process so the terminal
// dimensions refresh. Best-effort: restricted environments may return EACCES,
// in which case the refresh is skipped.
func RefreshTerminalDimensions() {
	if isWindows() {
		return
	}
	// syscall.Kill on our own pid; a permission error is ignored (best-effort).
	killSelfSIGWINCH()
}

// NormalizeNativeShiftEnterInput maps a bare CR to the native Shift+Enter
// sequence when the native modifier detection is available. Upstream consults
// isNativeModifierPressed; the Go port takes the shift state from the
// caller (divergence D52).
func NormalizeNativeShiftEnterInput(data string, shouldDetectNativeShiftEnter bool, isShiftPressed bool) string {
	if shouldDetectNativeShiftEnter && data == "\r" && isShiftPressed {
		return nativeShiftEnterSequence
	}
	return data
}

// ResolveEscapeTimeoutMs resolves how long to wait for the rest of an escape
// sequence before dispatching a lone ESC as the Escape key. Legacy Alt+key
// input is ESC plus another byte, so high-latency transports need a longer
// reassembly window.
func ResolveEscapeTimeoutMs(env func(string) string) int {
	if env != nil {
		if configured, err := strconv.Atoi(env("PI_TUI_ESC_TIMEOUT")); err == nil && configured > 0 {
			return configured
		}
		if env("SSH_CONNECTION") != "" || env("SSH_TTY") != "" {
			return defaultSSHEscapeTimeoutMS
		}
	}
	return defaultEscapeTimeoutMSValue
}

// ProcessTerminal is the Terminal implementation over os.Stdin/os.Stdout.
type ProcessTerminal struct {
	mu sync.Mutex

	stdin  *os.File
	stdout *os.File

	wasRaw                            *term.State
	inputHandler                      func(data string)
	resizeHandler                     func()
	kittyProtocolActive               bool
	modifyOtherKeysActive             bool
	keyboardProtocolPushed            bool
	keyboardProtocolNegotiationBuffer string
	keyboardProtocolBufferFlushTimer  *time.Timer
	stdinBuffer                       *StdinBuffer
	progressInterval                  *time.Ticker
	progressDone                      chan struct{}
	writeLogPath                      string
	closed                            bool
}

// NewProcessTerminal creates a terminal over the given files (os.Stdin and
// os.Stdout in production).
func NewProcessTerminal(stdin *os.File, stdout *os.File) *ProcessTerminal {
	return &ProcessTerminal{stdin: stdin, stdout: stdout, writeLogPath: resolveWriteLogPath()}
}

func resolveWriteLogPath() string {
	env := os.Getenv("PI_TUI_WRITE_LOG")
	if env == "" {
		return ""
	}
	if info, err := os.Stat(env); err == nil && info.IsDir() {
		now := time.Now()
		ts := now.Format("2006-01-02_15-04-05")
		return env + "/tui-" + ts + "-" + strconv.Itoa(os.Getpid()) + ".log"
	}
	return env
}

// KittyProtocolActive reports whether the Kitty keyboard protocol is active.
func (t *ProcessTerminal) KittyProtocolActive() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.kittyProtocolActive
}

// ModifyOtherKeysActive reports whether the modifyOtherKeys fallback is active.
func (t *ProcessTerminal) ModifyOtherKeysActive() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.modifyOtherKeysActive
}

// Start enables raw mode, bracketed paste, and the keyboard protocol query.
func (t *ProcessTerminal) Start(onInput func(data string), onResize func()) {
	t.mu.Lock()
	t.inputHandler = onInput
	t.resizeHandler = onResize

	// Save previous state and enable raw mode.
	if state, err := term.MakeRaw(int(t.stdin.Fd())); err == nil {
		t.wasRaw = state
	}

	// Enable bracketed paste mode: the terminal wraps pastes in
	// \x1b[200~ ... \x1b[201~.
	t.writeLocked("\x1b[?2004h")

	// Set up the resize handler immediately.
	if t.resizeHandler != nil {
		startResizeWatcher(t.resizeHandler)
	}

	// Refresh terminal dimensions: they may be stale after suspend/resume
	// (SIGWINCH is lost while the process is stopped).
	RefreshTerminalDimensions()
	t.mu.Unlock()

	// Query Kitty keyboard protocol; fall back to modifyOtherKeys when the DA
	// sentinel confirms no Kitty response.
	t.queryAndEnableKittyProtocol()
}

// setupStdinBuffer reads stdin and forwards complete sequences.
func (t *ProcessTerminal) setupStdinBufferLocked() {
	t.stdinBuffer = NewStdinBuffer(StdinBufferOptions{EscapeTimeout: ResolveEscapeTimeoutMs(os.Getenv)})
	t.stdinBuffer.OnData = func(sequence string) {
		negotiationSequence := t.readKeyboardProtocolNegotiationSequence(sequence)
		if negotiationSequence.kind == "pending" {
			t.scheduleKeyboardProtocolNegotiationBufferFlush()
			return // Wait briefly for the rest of a split Kitty response.
		}
		if t.handleKeyboardProtocolNegotiationSequence(negotiationSequence) {
			return
		}
		t.forwardInputSequence(sequence)
	}
	// Re-wrap paste content with bracketed paste markers for the editor.
	t.stdinBuffer.OnPaste = func(content string) {
		t.mu.Lock()
		handler := t.inputHandler
		t.mu.Unlock()
		if handler != nil {
			handler("\x1b[200~" + content + "\x1b[201~")
		}
	}

	// Pipe stdin data through the buffer.
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := t.stdin.Read(buf)
			if n > 0 {
				t.mu.Lock()
				buffer := t.stdinBuffer
				closed := t.closed
				t.mu.Unlock()
				if closed {
					return
				}
				if buffer != nil {
					data := make([]byte, n)
					copy(data, buf[:n])
					buffer.Process(data)
				}
			}
			if err != nil {
				return
			}
		}
	}()
}

// queryAndEnableKittyProtocol queries the terminal for Kitty keyboard
// protocol support and enables it when available.
//
// Kitty's progressive enhancement detection requires requesting the desired
// flags before querying them. The trailing DA query is a sentinel supported
// by terminals that do not know the Kitty keyboard protocol; receiving DA
// before a Kitty response enables the modifyOtherKeys fallback without a
// startup timeout. Requested flags: 1 = disambiguate escape codes, 2 = report
// event types, 4 = report alternate keys.
func (t *ProcessTerminal) queryAndEnableKittyProtocol() {
	t.mu.Lock()
	t.setupStdinBufferLocked()
	t.keyboardProtocolPushed = true
	t.clearKeyboardProtocolNegotiationBufferLocked()
	t.writeLocked(kittyKeyboardProtocolQuery)
	t.mu.Unlock()
}

func (t *ProcessTerminal) handleKeyboardProtocolNegotiationSequence(negotiationSequence negotiationResult) bool {
	if negotiationSequence.kind == "none" || negotiationSequence.kind == "pending" {
		return false
	}
	parsed := negotiationSequence.parsed
	t.clearKeyboardProtocolNegotiationBufferLocked()
	if parsed.Kind == NegotiationKittyFlags {
		if parsed.Flags != 0 {
			t.disableModifyOtherKeysLocked()
			if !t.kittyProtocolActive {
				t.kittyProtocolActive = true
				SetKittyProtocolActive(true)
			}
		} else {
			t.enableModifyOtherKeysLocked()
		}
		return true
	}

	if !t.kittyProtocolActive {
		t.enableModifyOtherKeysLocked()
	}
	return true
}

type negotiationResult struct {
	kind   string // "sequence" | "pending" | "none"
	parsed *KeyboardProtocolNegotiationSequence
}

var negotiationPending = negotiationResult{kind: "pending"}

func (t *ProcessTerminal) readKeyboardProtocolNegotiationSequence(sequence string) negotiationResult {
	if t.keyboardProtocolNegotiationBuffer != "" {
		bufferedSequence := t.keyboardProtocolNegotiationBuffer + sequence
		if parsed := ParseKeyboardProtocolNegotiationSequence(bufferedSequence); parsed != nil {
			t.clearKeyboardProtocolNegotiationBufferLocked()
			return negotiationResult{kind: "sequence", parsed: parsed}
		}
		if isKeyboardProtocolNegotiationSequencePrefix(bufferedSequence) {
			t.setKeyboardProtocolNegotiationBufferLocked(bufferedSequence)
			return negotiationPending
		}
		t.flushKeyboardProtocolNegotiationBufferAsInputLocked()
	}

	if parsed := ParseKeyboardProtocolNegotiationSequence(sequence); parsed != nil {
		return negotiationResult{kind: "sequence", parsed: parsed}
	}
	if isKeyboardProtocolNegotiationSequencePrefix(sequence) {
		t.setKeyboardProtocolNegotiationBufferLocked(sequence)
		return negotiationPending
	}
	return negotiationResult{kind: "none"}
}

func (t *ProcessTerminal) setKeyboardProtocolNegotiationBufferLocked(sequence string) {
	t.clearKeyboardProtocolNegotiationBufferFlushTimerLocked()
	t.keyboardProtocolNegotiationBuffer = sequence
}

func (t *ProcessTerminal) clearKeyboardProtocolNegotiationBufferLocked() {
	t.clearKeyboardProtocolNegotiationBufferFlushTimerLocked()
	t.keyboardProtocolNegotiationBuffer = ""
}

func (t *ProcessTerminal) flushKeyboardProtocolNegotiationBufferAsInputLocked() {
	if t.keyboardProtocolNegotiationBuffer == "" {
		return
	}
	sequence := t.keyboardProtocolNegotiationBuffer
	t.clearKeyboardProtocolNegotiationBufferLocked()
	t.forwardInputSequenceLocked(sequence)
}

func (t *ProcessTerminal) scheduleKeyboardProtocolNegotiationBufferFlush() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.keyboardProtocolNegotiationBuffer == "" || t.keyboardProtocolBufferFlushTimer != nil {
		return
	}
	t.keyboardProtocolBufferFlushTimer = time.AfterFunc(keyboardProtocolResponseFragmentTimeoutMS*time.Millisecond, func() {
		t.mu.Lock()
		t.keyboardProtocolBufferFlushTimer = nil
		t.flushKeyboardProtocolNegotiationBufferAsInputLocked()
		t.mu.Unlock()
	})
}

func (t *ProcessTerminal) clearKeyboardProtocolNegotiationBufferFlushTimerLocked() {
	if t.keyboardProtocolBufferFlushTimer == nil {
		return
	}
	t.keyboardProtocolBufferFlushTimer.Stop()
	t.keyboardProtocolBufferFlushTimer = nil
}

func (t *ProcessTerminal) forwardInputSequence(sequence string) {
	t.mu.Lock()
	t.forwardInputSequenceLocked(sequence)
	t.mu.Unlock()
}

func (t *ProcessTerminal) forwardInputSequenceLocked(sequence string) {
	if t.inputHandler == nil {
		return
	}
	shouldDetectNativeShiftEnter := sequence == "\r" && (IsAppleTerminalSession() || isWindows())
	input := NormalizeNativeShiftEnterInput(sequence, shouldDetectNativeShiftEnter,
		shouldDetectNativeShiftEnter && nativeShiftPressed())
	t.inputHandler(input)
}

func (t *ProcessTerminal) enableModifyOtherKeysLocked() {
	if t.kittyProtocolActive || t.modifyOtherKeysActive {
		return
	}
	t.writeLocked("\x1b[>4;2m")
	t.modifyOtherKeysActive = true
}

func (t *ProcessTerminal) disableModifyOtherKeysLocked() {
	if !t.modifyOtherKeysActive {
		return
	}
	t.writeLocked("\x1b[>4;0m")
	t.modifyOtherKeysActive = false
}

// DrainInput drains stdin before exiting to prevent Kitty key release events
// from leaking to the parent shell over slow SSH connections.
func (t *ProcessTerminal) DrainInput(maxMs int, idleMs int) error {
	if maxMs == 0 {
		maxMs = 1000
	}
	if idleMs == 0 {
		idleMs = 50
	}
	t.mu.Lock()
	shouldDisableKittyProtocol := t.keyboardProtocolPushed || t.kittyProtocolActive
	t.clearKeyboardProtocolNegotiationBufferLocked()
	if shouldDisableKittyProtocol {
		// Disable the Kitty keyboard protocol first so any late key releases
		// do not generate new Kitty escape sequences.
		t.writeLocked("\x1b[<u")
		t.keyboardProtocolPushed = false
		t.kittyProtocolActive = false
		SetKittyProtocolActive(false)
	}
	t.disableModifyOtherKeysLocked()
	t.mu.Unlock()

	previousHandler := t.swapInputHandler(nil)
	defer t.swapInputHandlerRestore(previousHandler)

	var lastData time.Time
	var lastDataMu sync.Mutex
	setLastData := func() {
		lastDataMu.Lock()
		lastData = time.Now()
		lastDataMu.Unlock()
	}
	// Track late input arriving on the reading goroutine via the buffer.
	t.mu.Lock()
	if t.stdinBuffer != nil {
		previousOnData := t.stdinBuffer.OnData
		t.stdinBuffer.OnData = func(sequence string) {
			setLastData()
			if previousOnData != nil {
				previousOnData(sequence)
			}
		}
		t.mu.Unlock()
		defer func() {
			t.mu.Lock()
			if t.stdinBuffer != nil {
				t.stdinBuffer.OnData = previousOnData
			}
			t.mu.Unlock()
		}()
	} else {
		t.mu.Unlock()
	}

	lastData = time.Now()
	endTime := time.Now().Add(time.Duration(maxMs) * time.Millisecond)
	for {
		now := time.Now()
		if !endTime.After(now) {
			break
		}
		lastDataMu.Lock()
		idle := now.Sub(lastData)
		lastDataMu.Unlock()
		if idle >= time.Duration(idleMs)*time.Millisecond {
			break
		}
		time.Sleep(minDuration(time.Duration(idleMs)*time.Millisecond, endTime.Sub(now)))
	}
	return nil
}

func (t *ProcessTerminal) swapInputHandler(handler func(string)) func(string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	previous := t.inputHandler
	t.inputHandler = handler
	return previous
}

func (t *ProcessTerminal) swapInputHandlerRestore(previous func(string)) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.inputHandler = previous
}

// Stop restores the terminal state.
func (t *ProcessTerminal) Stop() {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.clearProgressIntervalLocked() {
		t.writeLocked(terminalProgressClearSequence)
	}

	// Disable bracketed paste mode.
	t.writeLocked("\x1b[?2004l")

	shouldDisableKittyProtocol := t.keyboardProtocolPushed || t.kittyProtocolActive
	t.clearKeyboardProtocolNegotiationBufferLocked()

	// Disable the Kitty keyboard protocol if not already done by DrainInput.
	if shouldDisableKittyProtocol {
		t.writeLocked("\x1b[<u")
		t.keyboardProtocolPushed = false
		t.kittyProtocolActive = false
		SetKittyProtocolActive(false)
	}
	t.disableModifyOtherKeysLocked()

	t.closed = true
	if t.stdinBuffer != nil {
		t.stdinBuffer.Destroy()
		t.stdinBuffer = nil
	}
	t.inputHandler = nil
	t.resizeHandler = nil
	stopResizeWatcher()

	// Restore raw mode state.
	if t.wasRaw != nil {
		_ = term.Restore(int(t.stdin.Fd()), t.wasRaw)
		t.wasRaw = nil
	}
}

func (t *ProcessTerminal) writeLocked(data string) {
	_, _ = t.stdout.WriteString(data)
	if t.writeLogPath != "" {
		f, err := os.OpenFile(t.writeLogPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err == nil {
			_, _ = f.WriteString(data)
			_ = f.Close()
		}
	}
}

// Write writes output to the terminal.
func (t *ProcessTerminal) Write(data string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.writeLocked(data)
}

// Columns returns the terminal width.
func (t *ProcessTerminal) Columns() int {
	if width, _, err := term.GetSize(int(t.stdout.Fd())); err == nil && width != 0 {
		return width
	}
	if columns, err := strconv.Atoi(os.Getenv("COLUMNS")); err == nil && columns != 0 {
		return columns
	}
	return 80
}

// Rows returns the terminal height.
func (t *ProcessTerminal) Rows() int {
	if _, height, err := term.GetSize(int(t.stdout.Fd())); err == nil && height != 0 {
		return height
	}
	if lines, err := strconv.Atoi(os.Getenv("LINES")); err == nil && lines != 0 {
		return lines
	}
	return 24
}

// MoveBy moves the cursor up (negative) or down (positive) by N lines.
func (t *ProcessTerminal) MoveBy(lines int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if lines > 0 {
		t.writeLocked("\x1b[" + strconv.Itoa(lines) + "B")
	} else if lines < 0 {
		t.writeLocked("\x1b[" + strconv.Itoa(-lines) + "A")
	}
}

// HideCursor hides the cursor.
func (t *ProcessTerminal) HideCursor() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.writeLocked("\x1b[?25l")
}

// ShowCursor shows the cursor.
func (t *ProcessTerminal) ShowCursor() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.writeLocked("\x1b[?25h")
}

// ClearLine clears the current line.
func (t *ProcessTerminal) ClearLine() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.writeLocked("\x1b[K")
}

// ClearFromCursor clears from the cursor to the end of the screen.
func (t *ProcessTerminal) ClearFromCursor() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.writeLocked("\x1b[J")
}

// ClearScreen clears the entire screen and moves to (0,0).
func (t *ProcessTerminal) ClearScreen() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.writeLocked("\x1b[2J\x1b[H")
}

// SetTitle sets the terminal window title (OSC 0;title BEL).
func (t *ProcessTerminal) SetTitle(title string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.writeLocked("\x1b]0;" + title + "\x07")
}

// SetProgress drives the OSC 9;4 progress indicator; active shows an
// indeterminate progress with a keepalive.
func (t *ProcessTerminal) SetProgress(active bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if active {
		t.writeLocked(terminalProgressActiveSequence)
		if t.progressInterval == nil {
			t.progressInterval = time.NewTicker(terminalProgressKeepaliveMS * time.Millisecond)
			t.progressDone = make(chan struct{})
			go func(ticker *time.Ticker, done chan struct{}) {
				for {
					select {
					case <-done:
						return
					case <-ticker.C:
						t.mu.Lock()
						t.writeLocked(terminalProgressActiveSequence)
						t.mu.Unlock()
					}
				}
			}(t.progressInterval, t.progressDone)
		}
	} else {
		if t.clearProgressIntervalLocked() {
			t.writeLocked(terminalProgressClearSequence)
		}
	}
}

func (t *ProcessTerminal) clearProgressIntervalLocked() bool {
	if t.progressInterval == nil {
		return false
	}
	close(t.progressDone)
	t.progressInterval.Stop()
	t.progressInterval = nil
	t.progressDone = nil
	return true
}

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}

// ---- regex helpers ----

func matchesRegex(s string, pattern string) bool {
	return compileRegex(pattern).MatchString(s)
}

func matchRegexPrefix(s string, pattern string) (int, bool) {
	matches := compileRegex(pattern).FindStringSubmatch(s)
	if matches == nil {
		return 0, false
	}
	value, err := strconv.Atoi(matches[1])
	if err != nil {
		return 0, false
	}
	return value, true
}
