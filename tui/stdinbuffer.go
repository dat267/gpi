package tui

import (
	"regexp"
	"sync"
	"time"
	"unicode/utf8"
)

// Port of src/stdin-buffer.ts: buffers input and emits complete sequences,
// handling partial escape sequences that arrive across multiple chunks.
//
// Upstream works on UTF-16 code units; Go iterates runes, so astral-plane
// characters are emitted as a single sequence instead of surrogate halves
// (divergence D49).

const (
	stdinEscape              = "\x1b"
	defaultSequenceTimeoutMS = 50
	defaultEscapeTimeoutMS   = 10
	bracketedPasteStart      = "\x1b[200~"
	bracketedPasteEnd        = "\x1b[201~"
)

type seqStatus string

const (
	seqComplete   seqStatus = "complete"
	seqIncomplete seqStatus = "incomplete"
	seqNotEscape  seqStatus = "not-escape"
)

// isCompleteSequence reports whether data is a complete escape sequence or
// needs more input.
func isCompleteSequence(data string) seqStatus {
	if !startsWith(data, stdinEscape) {
		return seqNotEscape
	}
	if len(data) == 1 {
		return seqIncomplete
	}

	afterEsc := data[1:]

	switch {
	case startsWith(afterEsc, "["):
		// Old-style mouse sequence: ESC[M + 3 bytes = 6 total.
		if startsWith(afterEsc, "[M") {
			if len(data) >= 6 {
				return seqComplete
			}
			return seqIncomplete
		}
		return isCompleteCsiSequence(data)
	case startsWith(afterEsc, "]"):
		return isCompleteOscSequence(data)
	case startsWith(afterEsc, "P"):
		return isCompleteDcsSequence(data)
	case startsWith(afterEsc, "_"):
		return isCompleteApcSequence(data)
	case startsWith(afterEsc, "O"):
		// ESC O followed by a single character.
		if len(afterEsc) >= 2 {
			return seqComplete
		}
		return seqIncomplete
	case len(afterEsc) == 1:
		// Meta key sequence: ESC followed by a single character.
		return seqComplete
	}
	// Unknown escape sequence: treat as complete.
	return seqComplete
}

func isCompleteCsiSequence(data string) seqStatus {
	if !startsWith(data, stdinEscape+"[") {
		return seqComplete
	}
	if len(data) < 3 {
		return seqIncomplete
	}

	payload := data[2:]
	// CSI sequences end with a byte in the range 0x40-0x7E (@-~).
	lastChar := payload[len(payload)-1]
	if lastChar >= 0x40 && lastChar <= 0x7e {
		// Special handling for SGR mouse sequences: ESC[<B;X;YMm.
		if startsWith(payload, "<") {
			if sgrMouseRegex.MatchString(payload) {
				return seqComplete
			}
			if lastChar == 'M' || lastChar == 'm' {
				parts := splitOn(payload[1:len(payload)-1], ";")
				if len(parts) == 3 && allDigits(parts) {
					return seqComplete
				}
			}
			return seqIncomplete
		}
		return seqComplete
	}
	return seqIncomplete
}

func isCompleteOscSequence(data string) seqStatus {
	if !startsWith(data, stdinEscape+"]") {
		return seqComplete
	}
	if endsWith(data, stdinEscape+"\\") || endsWith(data, "\x07") {
		return seqComplete
	}
	return seqIncomplete
}

func isCompleteDcsSequence(data string) seqStatus {
	if !startsWith(data, stdinEscape+"P") {
		return seqComplete
	}
	if endsWith(data, stdinEscape+"\\") {
		return seqComplete
	}
	return seqIncomplete
}

func isCompleteApcSequence(data string) seqStatus {
	if !startsWith(data, stdinEscape+"_") {
		return seqComplete
	}
	if endsWith(data, stdinEscape+"\\") {
		return seqComplete
	}
	return seqIncomplete
}

var (
	sgrMouseRegex       = regexp.MustCompile(`^<\d+;\d+;\d+[Mm]$`)
	kittyPrintableRegex = regexp.MustCompile(`^\x1b\[(\d+)(?::\d*)?(?::\d+)?u$`)
)

// extractCompleteSequences splits the accumulated buffer into complete
// sequences plus the trailing remainder.
func extractCompleteSequences(buffer string) (sequences []string, remainder string) {
	pos := 0

	for pos < len(buffer) {
		remaining := buffer[pos:]

		if startsWith(remaining, stdinEscape) {
			// Find the end of this escape sequence.
			seqEnd := 1
			for seqEnd <= len(remaining) {
				candidate := remaining[:seqEnd]
				status := isCompleteSequence(candidate)

				if status == seqComplete {
					// WezTerm with enable_kitty_keyboard sends the Escape key
					// press as a raw '\x1b' byte and the release as a full
					// Kitty CSI-u sequence, concatenated as
					// '\x1b\x1b[27;...u'. '\x1b\x1b' would normally parse as a
					// complete meta-key sequence, leaving '[27;...u' to be
					// typed as plain text. If the character immediately
					// following would begin a new escape sequence, emit only
					// the first ESC and restart from the second.
					if candidate == "\x1b\x1b" {
						nextChar := byte(0)
						if seqEnd < len(remaining) {
							nextChar = remaining[seqEnd]
						}
						if nextChar == '[' || nextChar == ']' || nextChar == 'O' || nextChar == 'P' || nextChar == '_' {
							sequences = append(sequences, stdinEscape)
							pos++
							break
						}
					}
					sequences = append(sequences, candidate)
					pos += seqEnd
					break
				} else if status == seqIncomplete {
					seqEnd++
				} else {
					// Should not happen when starting with ESC.
					sequences = append(sequences, candidate)
					pos += seqEnd
					break
				}
			}

			if seqEnd > len(remaining) {
				return sequences, remaining
			}
		} else {
			// Not an escape sequence: take a single rune.
			_, size := decodeRune(remaining)
			sequences = append(sequences, remaining[:size])
			pos += size
		}
	}

	return sequences, ""
}

// parseUnmodifiedKittyPrintableCodepoint extracts the codepoint from an
// unmodified Kitty printable-key sequence.
func parseUnmodifiedKittyPrintableCodepoint(sequence string) (int, bool) {
	match := kittyPrintableRegex.FindStringSubmatch(sequence)
	if match == nil {
		return 0, false
	}
	codepoint := parseInt(match[1])
	if codepoint >= 32 {
		return codepoint, true
	}
	return 0, false
}

// StdinBuffer buffers stdin input and emits complete sequences. Upstream
// extends EventEmitter; Go takes callbacks (divergence D51).
type StdinBuffer struct {
	OnData  func(sequence string)
	OnPaste func(content string)

	mu                             sync.Mutex
	buffer                         string
	timeout                        *time.Timer
	timeoutMS                      int
	escapeTimeoutMS                int
	pasteMode                      bool
	pasteBuffer                    string
	pendingKittyPrintableCodepoint int
	hasPendingCodepoint            bool
	// emissions collects the sequences produced while the lock is held so the
	// callbacks run outside it (D139): the handler can trigger a shutdown that
	// calls Destroy, which re-locks mu.
	emissions []stdinEmission
}

// stdinEmission is one buffered callback invocation.
type stdinEmission struct {
	sequence string
	paste    bool
}

// StdinBufferOptions configures a StdinBuffer.
type StdinBufferOptions struct {
	// Timeout is the maximum time to wait for an incomplete sequence such as
	// CSI or mouse (default 50ms).
	Timeout int
	// EscapeTimeout is the maximum time to wait after a lone ESC before
	// treating it as Escape (default 10ms). Increase for high-latency Alt+key
	// input (SSH).
	EscapeTimeout int
}

// NewStdinBuffer creates a buffer with the given options.
func NewStdinBuffer(options StdinBufferOptions) *StdinBuffer {
	timeoutMS := defaultSequenceTimeoutMS
	if options.Timeout != 0 {
		timeoutMS = options.Timeout
	}
	escapeTimeoutMS := defaultEscapeTimeoutMS
	if options.EscapeTimeout != 0 {
		escapeTimeoutMS = options.EscapeTimeout
	}
	return &StdinBuffer{timeoutMS: timeoutMS, escapeTimeoutMS: escapeTimeoutMS}
}

// Process feeds input data through the buffer.
func (b *StdinBuffer) Process(data []byte) {
	b.mu.Lock()
	b.processLocked(string(data))
	emissions := b.drainEmissionsLocked()
	b.mu.Unlock()
	b.deliver(emissions)
}

// ProcessString feeds string input through the buffer.
func (b *StdinBuffer) ProcessString(data string) {
	b.mu.Lock()
	b.processLocked(data)
	emissions := b.drainEmissionsLocked()
	b.mu.Unlock()
	b.deliver(emissions)
}

// drainEmissionsLocked returns and clears the pending emissions.
func (b *StdinBuffer) drainEmissionsLocked() []stdinEmission {
	if len(b.emissions) == 0 {
		return nil
	}
	emissions := b.emissions
	b.emissions = nil
	return emissions
}

// deliver runs the buffered callbacks outside the lock (D139).
func (b *StdinBuffer) deliver(emissions []stdinEmission) {
	for _, emission := range emissions {
		if emission.paste {
			if b.OnPaste != nil {
				b.OnPaste(emission.sequence)
			}
			continue
		}
		if b.OnData != nil {
			b.OnData(emission.sequence)
		}
	}
}

func (b *StdinBuffer) processLocked(str string) {
	if b.timeout != nil {
		b.timeout.Stop()
		b.timeout = nil
	}

	// High-byte conversion (for compatibility with parseKeypress): a single
	// byte > 127 becomes ESC + (byte - 128).
	if len(str) == 1 && str[0] > 127 {
		str = stdinEscape + string(rune(str[0]-128))
	}

	if str == "" && b.buffer == "" {
		b.emitDataSequenceLocked("")
		return
	}

	b.buffer += str

	if b.pasteMode {
		b.pasteBuffer += b.buffer
		b.buffer = ""

		if endIndex := indexOf(b.pasteBuffer, bracketedPasteEnd); endIndex != -1 {
			pastedContent := b.pasteBuffer[:endIndex]
			remaining := b.pasteBuffer[endIndex+len(bracketedPasteEnd):]

			b.pasteMode = false
			b.pasteBuffer = ""
			b.pendingKittyPrintableCodepoint = 0
			b.hasPendingCodepoint = false

			b.emitPasteLocked(pastedContent)

			if remaining != "" {
				b.processLocked(remaining)
			}
		}
		return
	}

	if startIndex := indexOf(b.buffer, bracketedPasteStart); startIndex != -1 {
		if startIndex > 0 {
			beforePaste := b.buffer[:startIndex]
			sequences, _ := extractCompleteSequences(beforePaste)
			for _, sequence := range sequences {
				b.emitDataSequenceLocked(sequence)
			}
		}

		b.pendingKittyPrintableCodepoint = 0
		b.hasPendingCodepoint = false
		b.buffer = b.buffer[startIndex+len(bracketedPasteStart):]
		b.pasteMode = true
		b.pasteBuffer = b.buffer
		b.buffer = ""

		if endIndex := indexOf(b.pasteBuffer, bracketedPasteEnd); endIndex != -1 {
			pastedContent := b.pasteBuffer[:endIndex]
			remaining := b.pasteBuffer[endIndex+len(bracketedPasteEnd):]

			b.pasteMode = false
			b.pasteBuffer = ""
			b.pendingKittyPrintableCodepoint = 0
			b.hasPendingCodepoint = false

			b.emitPasteLocked(pastedContent)

			if remaining != "" {
				b.processLocked(remaining)
			}
		}
		return
	}

	sequences, remainder := extractCompleteSequences(b.buffer)
	b.buffer = remainder

	for _, sequence := range sequences {
		b.emitDataSequenceLocked(sequence)
	}

	if b.buffer != "" {
		timeoutMS := b.timeoutMS
		if b.buffer == stdinEscape {
			timeoutMS = b.escapeTimeoutMS
		}
		b.timeout = time.AfterFunc(time.Duration(timeoutMS)*time.Millisecond, func() {
			b.mu.Lock()
			b.timeout = nil
			flushed := b.flushLocked()
			emissions := b.drainEmissionsLocked()
			b.mu.Unlock()
			for _, sequence := range flushed {
				if b.OnData != nil {
					b.OnData(sequence)
				}
			}
			b.deliver(emissions)
		})
	}
}

// emitDataSequenceLocked deduplicates the raw character duplicate that
// terminals echo after an unmodified Kitty printable sequence. Callers hold
// the lock.
func (b *StdinBuffer) emitDataSequenceLocked(sequence string) {
	if runeCount(sequence) == 1 {
		codepoint, _ := decodeRune(sequence)
		if b.hasPendingCodepoint && int(codepoint) == b.pendingKittyPrintableCodepoint {
			b.pendingKittyPrintableCodepoint = 0
			b.hasPendingCodepoint = false
			return
		}
	}

	if codepoint, ok := parseUnmodifiedKittyPrintableCodepoint(sequence); ok {
		b.pendingKittyPrintableCodepoint = codepoint
		b.hasPendingCodepoint = true
	} else {
		b.pendingKittyPrintableCodepoint = 0
		b.hasPendingCodepoint = false
	}
	b.emit(sequence)
}

func (b *StdinBuffer) emit(sequence string) {
	b.emissions = append(b.emissions, stdinEmission{sequence: sequence})
}

func (b *StdinBuffer) emitPasteLocked(content string) {
	b.emissions = append(b.emissions, stdinEmission{sequence: content, paste: true})
}

// Flush returns the buffered incomplete sequence, if any.
func (b *StdinBuffer) Flush() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.flushLocked()
}

func (b *StdinBuffer) flushLocked() []string {
	if b.timeout != nil {
		b.timeout.Stop()
		b.timeout = nil
	}
	if b.buffer == "" {
		return nil
	}
	sequences := []string{b.buffer}
	b.buffer = ""
	b.pendingKittyPrintableCodepoint = 0
	b.hasPendingCodepoint = false
	return sequences
}

// Clear drops all buffered state.
func (b *StdinBuffer) Clear() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.timeout != nil {
		b.timeout.Stop()
		b.timeout = nil
	}
	b.buffer = ""
	b.pasteMode = false
	b.pasteBuffer = ""
	b.pendingKittyPrintableCodepoint = 0
	b.hasPendingCodepoint = false
}

// GetBuffer returns the pending buffer contents.
func (b *StdinBuffer) GetBuffer() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer
}

// Destroy clears the buffer (upstream's destroy).
func (b *StdinBuffer) Destroy() { b.Clear() }

// ---- small helpers shared by the terminal port ----

func startsWith(s, prefix string) bool { return len(s) >= len(prefix) && s[:len(prefix)] == prefix }

func endsWith(s, suffix string) bool {
	return len(s) >= len(suffix) && s[len(s)-len(suffix):] == suffix
}

func splitOn(s, sep string) []string {
	if s == "" {
		return nil
	}
	var out []string
	for {
		index := indexOf(s, sep)
		if index == -1 {
			out = append(out, s)
			return out
		}
		out = append(out, s[:index])
		s = s[index+len(sep):]
	}
}

func allDigits(parts []string) bool {
	for _, part := range parts {
		if part == "" {
			return false
		}
		for i := 0; i < len(part); i++ {
			if part[i] < '0' || part[i] > '9' {
				return false
			}
		}
	}
	return true
}

func parseInt(s string) int {
	value := 0
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0
		}
		value = value*10 + int(s[i]-'0')
	}
	return value
}

// decodeRune returns the first rune and its byte size.
func decodeRune(s string) (rune, int) {
	return utf8.DecodeRuneInString(s)
}

// runeCount counts the runes of a UTF-8 string. Upstream checks
// sequence.length === 1 (one UTF-16 code unit); runes are the Go analog
// (divergence D49).
func runeCount(s string) int {
	return utf8.RuneCountInString(s)
}
