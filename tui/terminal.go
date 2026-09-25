package tui

import (
	"os"
	"regexp"
	"strconv"
	"sync"
	"sync/atomic"
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

// inputHandlerFunc wraps the input handler for the atomic pointer.
type inputHandlerFunc func(data string)

// RawInputTerminal is implemented by terminals that support the D147
// raw-input mode: the consumer (the UI loop) feeds raw stdin chunks through
// FeedInput and drives force-flushes via the deadline accessors, instead of
// the terminal reassembling sequences on its own reader goroutine.
type RawInputTerminal interface {
	Terminal
	// EnableRawInput switches the terminal to raw-input mode (before Start).
	EnableRawInput()
	// FeedInput feeds raw stdin bytes and returns complete, negotiation-
	// filtered sequences ready to dispatch.
	FeedInput(raw []byte) []string
	// NextInputFlushDeadline reports when an incomplete sequence or a
	// buffered keyboard-protocol response must be force-flushed.
	NextInputFlushDeadline() (time.Time, bool)
	// FlushPendingInput flushes expired deadlines and returns the sequences.
	FlushPendingInput() []string
}

// ProcessTerminal is the Terminal implementation over os.Stdin/os.Stdout.
//
// D147: the interactive consumer (the UI loop) owns input — it feeds raw
// bytes through FeedInput and drives flushing via NextInputFlushDeadline /
// FlushPendingInput, so the stdin buffer and the keyboard-protocol
// negotiation state are loop-owned and lock-free. The protocol flags are
// atomics. The one retained lock is writeMu: it serializes terminal writes
// and raw-mode transitions between the progress keepalive goroutine, the
// loop, and the shutdown path — pure I/O serialization, no UI state.
type ProcessTerminal struct {
	stdin  *os.File
	stdout *os.File

	writeMu                           sync.Mutex
	wasRaw                            *term.State
	inputHandler                      atomic.Pointer[inputHandlerFunc]
	lastInputAt                       atomic.Int64
	kittyProtocolActive               atomic.Bool
	modifyOtherKeysActive             atomic.Bool
	keyboardProtocolPushed            atomic.Bool
	closed                            atomic.Bool
	useRawInput                       bool
	keyboardProtocolNegotiationBuffer string
	negotiationDeadline               time.Time
	stdinBuffer                       *StdinBuffer
	progressInterval                  *time.Ticker
	progressDone                      chan struct{}
	writeLogPath                      string

	// cols/rows cache the terminal size. Terminal size is queried with a
	// console API (GetConsoleScreenBufferInfo on Windows), and that call can
	// block for the whole duration of a mouse selection — so the render path
	// must never make it. The cache is filled at Start and refreshed by the
	// resize watcher (a poller on Windows, which has no SIGWINCH); Columns/
	// Rows only touch the cache. sizeFn is a test seam for the query.
	cols   atomic.Int64
	rows   atomic.Int64
	sizeFn func() (int, int)
}

// NewProcessTerminal creates a terminal over the given files (os.Stdin and
// os.Stdout in production).
func NewProcessTerminal(stdin *os.File, stdout *os.File) *ProcessTerminal {
	if stdin == nil {
		stdin = os.Stdin
	}
	if stdout == nil {
		stdout = os.Stdout
	}
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
	return t.kittyProtocolActive.Load()
}

// ModifyOtherKeysActive reports whether the modifyOtherKeys fallback is active.
func (t *ProcessTerminal) ModifyOtherKeysActive() bool {
	return t.modifyOtherKeysActive.Load()
}

// EnableRawInput switches the terminal to raw-input mode: Start spawns a
// reader that forwards RAW stdin chunks to the input handler (the consumer —
// the UI loop — feeds them through FeedInput), instead of reassembling
// sequences on a reader goroutine. Call before Start.
func (t *ProcessTerminal) EnableRawInput() {
	t.useRawInput = true
}

// Start enables raw mode, bracketed paste, and the keyboard protocol query.
func (t *ProcessTerminal) Start(onInput func(data string), onResize func()) {
	t.setInputHandler(onInput)

	// Save previous state and enable raw mode.
	t.writeMu.Lock()
	if state, err := term.MakeRaw(int(t.stdin.Fd())); err == nil {
		t.wasRaw = state
	}
	// Enable bracketed paste mode: the terminal wraps pastes in
	// \x1b[200~ ... \x1b[201~.
	t.writeLocked("\x1b[?2004h")
	t.writeMu.Unlock()

	// Set up the resize handler immediately and prime the size cache.
	// Populating the cache before the first render keeps the console size
	// query off the render path: on Windows a size query can block while a
	// mouse selection is active, and the render path must never block.
	t.refreshSize()
	if onResize != nil {
		startResizeWatcher(func() {
			// Only a real change repaints; the poller (Windows) fires often.
			if t.refreshSize() {
				onResize()
			}
		})
	}

	if t.useRawInput {
		// The reader forwards raw chunks to the input handler (the UI loop);
		// the loop reassembles them through FeedInput on the loop goroutine.
		t.stdinBuffer = NewStdinBuffer(StdinBufferOptions{EscapeTimeout: ResolveEscapeTimeoutMs(os.Getenv)})
		go t.readRawStdin()
	} else {
		// Legacy (library) mode: reassemble sequences on the reader goroutine.
		t.setupLegacyStdinBuffer()
	}

	// Query Kitty keyboard protocol; fall back to modifyOtherKeys when the DA
	// sentinel confirms no Kitty response.
	t.queryAndEnableKittyProtocol()
}

// readRawStdin reads stdin and forwards raw chunks to the input handler
// until the terminal closes. The handler decides where the bytes go (the UI
// loop posts them to its input channel); muting is done by swapping the
// handler to nil (DrainInput).
func (t *ProcessTerminal) readRawStdin() {
	buf := make([]byte, 4096)
	for {
		n, err := t.stdin.Read(buf)
		if t.closed.Load() {
			return
		}
		if n > 0 {
			t.lastInputAt.Store(time.Now().UnixNano())
			if handler := t.loadInputHandler(); handler != nil {
				data := make([]byte, n)
				copy(data, buf[:n])
				handler(string(data))
			}
		}
		if err != nil {
			return
		}
	}
}

// setupLegacyStdinBuffer reads stdin and forwards complete sequences (the
// pre-D147 path, kept for library consumers whose Terminal is not driven by
// a UI loop).
func (t *ProcessTerminal) setupLegacyStdinBuffer() {
	t.stdinBuffer = NewStdinBuffer(StdinBufferOptions{EscapeTimeout: ResolveEscapeTimeoutMs(os.Getenv)})
	t.stdinBuffer.OnData = func(sequence string) {
		t.lastInputAt.Store(time.Now().UnixNano())
		negotiationSequence, pendingInput := t.readKeyboardProtocolNegotiationSequence(sequence)
		if negotiationSequence.kind == "pending" {
			t.negotiationDeadline = time.Now().Add(keyboardProtocolResponseFragmentTimeoutMS * time.Millisecond)
			return // Wait briefly for the rest of a split Kitty response.
		}
		handled := t.handleKeyboardProtocolNegotiationSequence(negotiationSequence)
		handler := t.loadInputHandler()
		if handled {
			return
		}
		deliverInput(handler, pendingInput)
		deliverInput(handler, sequence)
	}
	// Re-wrap paste content with bracketed paste markers for the editor.
	t.stdinBuffer.OnPaste = func(content string) {
		if handler := t.loadInputHandler(); handler != nil {
			handler("\x1b[200~" + content + "\x1b[201~")
		}
	}

	// Pipe stdin data through the buffer.
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := t.stdin.Read(buf)
			if t.closed.Load() {
				return
			}
			if n > 0 {
				if buffer := t.stdinBuffer; buffer != nil {
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

// FeedInput feeds raw stdin bytes through the input buffer on the consumer
// goroutine and returns the complete, negotiation-filtered sequences ready
// to dispatch. Split keyboard-protocol responses are buffered internally
// until FlushPendingInput expires them.
func (t *ProcessTerminal) FeedInput(raw []byte) []string {
	if t.closed.Load() {
		return nil
	}
	var sequences []string
	buffer := t.stdinBuffer
	if buffer == nil {
		return nil
	}
	buffer.OnData = func(sequence string) {
		sequences = t.filterInputSequence(sequences, sequence)
	}
	buffer.OnPaste = func(content string) {
		sequences = append(sequences, "\x1b[200~"+content+"\x1b[201~")
	}
	buffer.Process(raw)
	return sequences
}

// NextInputFlushDeadline reports when an incomplete sequence or a buffered
// keyboard-protocol response must be force-flushed, and whether one is
// pending. The consumer arms its timer on it.
func (t *ProcessTerminal) NextInputFlushDeadline() (time.Time, bool) {
	deadline, ok := t.stdinBuffer.PendingTimeout()
	if !ok && !t.negotiationDeadline.IsZero() {
		deadline, ok = t.negotiationDeadline, true
	}
	if !ok {
		return time.Time{}, false
	}
	if !t.negotiationDeadline.IsZero() && t.negotiationDeadline.Before(deadline) {
		deadline = t.negotiationDeadline
	}
	return deadline, true
}

// FlushPendingInput flushes expired input deadlines (a lone ESC, an
// incomplete sequence, a split keyboard-protocol response) and returns the
// sequences ready to dispatch.
func (t *ProcessTerminal) FlushPendingInput() []string {
	now := time.Now()
	var sequences []string
	if !t.negotiationDeadline.IsZero() && !now.Before(t.negotiationDeadline) {
		t.negotiationDeadline = time.Time{}
		if buffered := t.takeKeyboardProtocolNegotiationBuffer(); buffered != "" {
			sequences = append(sequences, buffered)
		}
	}
	for _, sequence := range t.stdinBuffer.FlushExpired(now) {
		sequences = t.filterInputSequence(sequences, sequence)
	}
	return sequences
}

// filterInputSequence runs the keyboard-protocol negotiation filter for one
// complete sequence (loop-owned; no locking).
func (t *ProcessTerminal) filterInputSequence(sequences []string, sequence string) []string {
	negotiationSequence, pendingInput := t.readKeyboardProtocolNegotiationSequence(sequence)
	if negotiationSequence.kind == "pending" {
		// Wait briefly for the rest of a split Kitty response.
		t.negotiationDeadline = time.Now().Add(keyboardProtocolResponseFragmentTimeoutMS * time.Millisecond)
		return sequences
	}
	handled := t.handleKeyboardProtocolNegotiationSequence(negotiationSequence)
	if pendingInput != "" {
		sequences = append(sequences, pendingInput)
	}
	if !handled && sequence != "" {
		sequences = append(sequences, sequence)
	}
	return sequences
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
	t.writeMu.Lock()
	t.keyboardProtocolPushed.Store(true)
	t.clearKeyboardProtocolNegotiationBuffer()
	t.writeLocked(kittyKeyboardProtocolQuery)
	t.writeMu.Unlock()
}

func (t *ProcessTerminal) handleKeyboardProtocolNegotiationSequence(negotiationSequence negotiationResult) bool {
	if negotiationSequence.kind == "none" || negotiationSequence.kind == "pending" {
		return false
	}
	parsed := negotiationSequence.parsed
	t.clearKeyboardProtocolNegotiationBuffer()
	if parsed.Kind == NegotiationKittyFlags {
		if parsed.Flags != 0 {
			t.disableModifyOtherKeys()
			if !t.kittyProtocolActive.Load() {
				t.kittyProtocolActive.Store(true)
				SetKittyProtocolActive(true)
			}
		} else {
			t.enableModifyOtherKeys()
		}
		return true
	}

	if !t.kittyProtocolActive.Load() {
		t.enableModifyOtherKeys()
	}
	return true
}

type negotiationResult struct {
	kind   string // "sequence" | "pending" | "none"
	parsed *KeyboardProtocolNegotiationSequence
}

var negotiationPending = negotiationResult{kind: "pending"}

func (t *ProcessTerminal) readKeyboardProtocolNegotiationSequence(sequence string) (negotiationResult, string) {
	if t.keyboardProtocolNegotiationBuffer != "" {
		bufferedSequence := t.keyboardProtocolNegotiationBuffer + sequence
		if parsed := ParseKeyboardProtocolNegotiationSequence(bufferedSequence); parsed != nil {
			t.clearKeyboardProtocolNegotiationBuffer()
			return negotiationResult{kind: "sequence", parsed: parsed}, ""
		}
		if isKeyboardProtocolNegotiationSequencePrefix(bufferedSequence) {
			t.keyboardProtocolNegotiationBuffer = bufferedSequence
			return negotiationPending, ""
		}
		pending := t.takeKeyboardProtocolNegotiationBuffer()
		if parsed := ParseKeyboardProtocolNegotiationSequence(sequence); parsed != nil {
			return negotiationResult{kind: "sequence", parsed: parsed}, pending
		}
		if isKeyboardProtocolNegotiationSequencePrefix(sequence) {
			t.keyboardProtocolNegotiationBuffer = sequence
			return negotiationPending, pending
		}
		return negotiationResult{kind: "none"}, pending
	}

	if parsed := ParseKeyboardProtocolNegotiationSequence(sequence); parsed != nil {
		return negotiationResult{kind: "sequence", parsed: parsed}, ""
	}
	if isKeyboardProtocolNegotiationSequencePrefix(sequence) {
		t.keyboardProtocolNegotiationBuffer = sequence
		return negotiationPending, ""
	}
	return negotiationResult{kind: "none"}, ""
}

func (t *ProcessTerminal) clearKeyboardProtocolNegotiationBuffer() {
	t.keyboardProtocolNegotiationBuffer = ""
	t.negotiationDeadline = time.Time{}
}

func (t *ProcessTerminal) takeKeyboardProtocolNegotiationBuffer() string {
	if t.keyboardProtocolNegotiationBuffer == "" {
		return ""
	}
	sequence := t.keyboardProtocolNegotiationBuffer
	t.clearKeyboardProtocolNegotiationBuffer()
	return sequence
}

// deliverInput normalizes a sequence and hands it to the input handler without
// holding the terminal lock (D138).
func deliverInput(handler func(string), sequence string) {
	if handler == nil || sequence == "" {
		return
	}
	shouldDetectNativeShiftEnter := sequence == "\r" && (IsAppleTerminalSession() || isWindows())
	input := NormalizeNativeShiftEnterInput(sequence, shouldDetectNativeShiftEnter,
		shouldDetectNativeShiftEnter && nativeShiftPressed())
	handler(input)
}

func (t *ProcessTerminal) enableModifyOtherKeys() {
	if t.kittyProtocolActive.Load() || t.modifyOtherKeysActive.Load() {
		return
	}
	t.writeMu.Lock()
	t.writeLocked("\x1b[>4;2m")
	t.writeMu.Unlock()
	t.modifyOtherKeysActive.Store(true)
}

func (t *ProcessTerminal) disableModifyOtherKeys() {
	if !t.modifyOtherKeysActive.Load() {
		return
	}
	t.writeMu.Lock()
	t.writeLocked("\x1b[>4;0m")
	t.writeMu.Unlock()
	t.modifyOtherKeysActive.Store(false)
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
	shouldDisableKittyProtocol := t.keyboardProtocolPushed.Load() || t.kittyProtocolActive.Load()
	if shouldDisableKittyProtocol {
		// Disable the Kitty keyboard protocol first so any late key releases
		// do not generate new Kitty escape sequences.
		t.writeMu.Lock()
		t.writeLocked("\x1b[<u")
		t.writeMu.Unlock()
		t.keyboardProtocolPushed.Store(false)
		t.kittyProtocolActive.Store(false)
		SetKittyProtocolActive(false)
	}
	t.disableModifyOtherKeys()

	previousHandler := t.swapInputHandler(nil)
	defer t.swapInputHandlerRestore(previousHandler)

	// Late input is tracked through the reader-stamped atomic (the reader
	// goroutine owns the buffer; DrainInput must not touch it).
	t.lastInputAt.Store(time.Now().UnixNano())
	endTime := time.Now().Add(time.Duration(maxMs) * time.Millisecond)
	for {
		now := time.Now()
		if !endTime.After(now) {
			break
		}
		idle := time.Duration(now.UnixNano() - t.lastInputAt.Load())
		if idle >= time.Duration(idleMs)*time.Millisecond {
			break
		}
		time.Sleep(min(time.Duration(idleMs)*time.Millisecond, endTime.Sub(now)))
	}
	return nil
}

func (t *ProcessTerminal) setInputHandler(handler func(string)) {
	if handler == nil {
		t.inputHandler.Store(nil)
		return
	}
	wrapped := inputHandlerFunc(handler)
	t.inputHandler.Store(&wrapped)
}

func (t *ProcessTerminal) loadInputHandler() func(string) {
	if wrapped := t.inputHandler.Load(); wrapped != nil {
		return *wrapped
	}
	return nil
}

func (t *ProcessTerminal) swapInputHandler(handler func(string)) func(string) {
	previous := t.loadInputHandler()
	t.setInputHandler(handler)
	return previous
}

func (t *ProcessTerminal) swapInputHandlerRestore(previous func(string)) {
	t.setInputHandler(previous)
}

// Stop restores the terminal state.
func (t *ProcessTerminal) Stop() {
	t.writeMu.Lock()
	defer t.writeMu.Unlock()

	if t.clearProgressIntervalLocked() {
		t.writeLocked(terminalProgressClearSequence)
	}

	// Disable bracketed paste mode.
	t.writeLocked("\x1b[?2004l")

	shouldDisableKittyProtocol := t.keyboardProtocolPushed.Load() || t.kittyProtocolActive.Load()

	// Disable the Kitty keyboard protocol if not already done by DrainInput.
	if shouldDisableKittyProtocol {
		t.writeLocked("\x1b[<u")
		t.keyboardProtocolPushed.Store(false)
		t.kittyProtocolActive.Store(false)
		SetKittyProtocolActive(false)
	}
	t.disableModifyOtherKeysLocked()

	t.closed.Store(true)
	t.setInputHandler(nil)
	stopResizeWatcher()

	// Restore raw mode state. The stdin buffer and the negotiation state are
	// owned by the input consumer (the loop) and are left alone here: the
	// closed flag stops all further input processing.
	if t.wasRaw != nil {
		_ = term.Restore(int(t.stdin.Fd()), t.wasRaw)
		t.wasRaw = nil
	}
}

func (t *ProcessTerminal) disableModifyOtherKeysLocked() {
	if !t.modifyOtherKeysActive.Load() {
		return
	}
	t.writeLocked("\x1b[>4;0m")
	t.modifyOtherKeysActive.Store(false)
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
	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	t.writeLocked(data)
}

// Columns returns the terminal width from the cache (see the cols/rows
// comment); it queries once if the cache has not been primed (Start does).
func (t *ProcessTerminal) Columns() int {
	if t.cols.Load() == 0 {
		t.refreshSize()
	}
	return int(t.cols.Load())
}

// Rows returns the terminal height from the cache.
func (t *ProcessTerminal) Rows() int {
	if t.rows.Load() == 0 {
		t.refreshSize()
	}
	return int(t.rows.Load())
}

// refreshSize queries the terminal size, stores it, and reports whether it
// changed. It is called at Start and by the resize watcher, never from the
// render path.
func (t *ProcessTerminal) refreshSize() bool {
	width, height := t.querySize()
	changed := int64(width) != t.cols.Load() || int64(height) != t.rows.Load()
	t.cols.Store(int64(width))
	t.rows.Store(int64(height))
	return changed
}

// querySize reads the terminal size, falling back to COLUMNS/LINES and then
// 80x24 (upstream's resolveTerminalSize defaults).
func (t *ProcessTerminal) querySize() (int, int) {
	width, height := 0, 0
	if t.sizeFn != nil {
		width, height = t.sizeFn()
	} else if w, h, err := term.GetSize(int(t.stdout.Fd())); err == nil {
		width, height = w, h
	}
	if width == 0 {
		if columns, err := strconv.Atoi(os.Getenv("COLUMNS")); err == nil && columns != 0 {
			width = columns
		}
	}
	if height == 0 {
		if lines, err := strconv.Atoi(os.Getenv("LINES")); err == nil && lines != 0 {
			height = lines
		}
	}
	if width == 0 {
		width = 80
	}
	if height == 0 {
		height = 24
	}
	return width, height
}

// MoveBy moves the cursor up (negative) or down (positive) by N lines.
func (t *ProcessTerminal) MoveBy(lines int) {
	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	if lines > 0 {
		t.writeLocked("\x1b[" + strconv.Itoa(lines) + "B")
	} else if lines < 0 {
		t.writeLocked("\x1b[" + strconv.Itoa(-lines) + "A")
	}
}

// HideCursor hides the cursor.
func (t *ProcessTerminal) HideCursor() {
	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	t.writeLocked("\x1b[?25l")
}

// ShowCursor shows the cursor.
func (t *ProcessTerminal) ShowCursor() {
	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	t.writeLocked("\x1b[?25h")
}

// ClearLine clears the current line.
func (t *ProcessTerminal) ClearLine() {
	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	t.writeLocked("\x1b[K")
}

// ClearFromCursor clears from the cursor to the end of the screen.
func (t *ProcessTerminal) ClearFromCursor() {
	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	t.writeLocked("\x1b[J")
}

// ClearScreen clears the entire screen and moves to (0,0).
func (t *ProcessTerminal) ClearScreen() {
	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	t.writeLocked("\x1b[2J\x1b[H")
}

// SetTitle sets the terminal window title (OSC 0;title BEL).
func (t *ProcessTerminal) SetTitle(title string) {
	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	t.writeLocked("\x1b]0;" + title + "\x07")
}

// SetProgress drives the OSC 9;4 progress indicator; active shows an
// indeterminate progress with a keepalive.
func (t *ProcessTerminal) SetProgress(active bool) {
	t.writeMu.Lock()
	defer t.writeMu.Unlock()
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
						t.writeMu.Lock()
						t.writeLocked(terminalProgressActiveSequence)
						t.writeMu.Unlock()
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
