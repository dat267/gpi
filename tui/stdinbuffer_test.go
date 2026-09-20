package tui

import (
	"strings"
	"sync"
	"testing"
	"time"
)

// collectBuffer wires a StdinBuffer to a mutex-protected slice; the second
// return emits the collected sequences and the third resets the collection
// (tests call it alongside buffer.Clear()).
func collectBuffer(options StdinBufferOptions) (*StdinBuffer, func() []string, func()) {
	var mu sync.Mutex
	var sequences []string
	buffer := NewStdinBuffer(options)
	buffer.OnData = func(sequence string) {
		mu.Lock()
		defer mu.Unlock()
		sequences = append(sequences, sequence)
	}
	return buffer, func() []string {
			mu.Lock()
			defer mu.Unlock()
			return append([]string(nil), sequences...)
		}, func() {
			mu.Lock()
			defer mu.Unlock()
			sequences = nil
		}
}

func TestStdinBufferRegularCharacters(t *testing.T) {
	buffer, emitted, _ := collectBuffer(StdinBufferOptions{Timeout: 10})

	buffer.ProcessString("a")
	if got := emitted(); strings.Join(got, ",") != "a" {
		t.Fatalf("got %q", got)
	}

	buffer.ProcessString("abc")
	if got := emitted(); strings.Join(got, ",") != "a,a,b,c" {
		t.Fatalf("got %q", got)
	}

	buffer.ProcessString("hello 世界")
	if got := emitted(); strings.Join(got, ",") != "a,a,b,c,h,e,l,l,o, ,世,界" {
		t.Fatalf("got %q", got)
	}
}

func TestStdinBufferCompleteSequences(t *testing.T) {
	buffer, emitted, resetEmitted := collectBuffer(StdinBufferOptions{Timeout: 10})

	// Complete mouse SGR, arrow, function key, meta key, and SS3 sequences
	// pass through as single events.
	for _, sequence := range []string{
		"\x1b[<35;20;5m",
		"\x1b[A",
		"\x1b[15~",
		"\x1bc",
		"\x1bOP",
	} {
		buffer.ProcessString(sequence)
		if got := emitted(); strings.Join(got, "|") != sequence {
			t.Fatalf("sequence %q: got %q", sequence, got)
		}
		buffer.Clear()
		resetEmitted()
	}
}

func TestStdinBufferPartialSequences(t *testing.T) {
	buffer, emitted, resetEmitted := collectBuffer(StdinBufferOptions{Timeout: 10})

	// An incomplete mouse SGR sequence stays buffered.
	buffer.ProcessString("\x1b[<35;")
	if got := emitted(); len(got) != 0 {
		t.Fatalf("emitted early: %q", got)
	}
	if buffer.GetBuffer() != "\x1b[<35;" {
		t.Fatalf("buffer = %q", buffer.GetBuffer())
	}
	buffer.ProcessString("20;5m")
	if got := emitted(); strings.Join(got, "") != "\x1b[<35;20;5m" {
		t.Fatalf("got %q", got)
	}

	// Split across many chunks.
	buffer.Clear()
	resetEmitted()
	buffer.ProcessString("\x1b")
	buffer.ProcessString("[")
	buffer.ProcessString("<")
	buffer.ProcessString("35")
	buffer.ProcessString(";20")
	buffer.ProcessString(";5")
	buffer.ProcessString("m")
	if got := emitted(); strings.Join(got, "") != "\x1b[<35;20;5m" {
		t.Fatalf("got %q", got)
	}

	// Incomplete sequences flush after the timeout.
	buffer.Clear()
	resetEmitted()
	buffer.ProcessString("\x1b[12")
	time.Sleep(30 * time.Millisecond)
	if got := emitted(); strings.Join(got, "") != "\x1b[12" {
		t.Fatalf("got %q", got)
	}
}

func TestStdinBufferLoneEscape(t *testing.T) {
	// A lone ESC waits for the escape timeout, not the sequence timeout.
	buffer, emitted, resetEmitted := collectBuffer(StdinBufferOptions{Timeout: 200, EscapeTimeout: 10})
	buffer.ProcessString("\x1b")
	if got := emitted(); len(got) != 0 {
		t.Fatalf("emitted early: %q", got)
	}
	time.Sleep(30 * time.Millisecond)
	if got := emitted(); strings.Join(got, "") != "\x1b" {
		t.Fatalf("got %q", got)
	}

	// Explicit flush emits the buffer immediately.
	buffer.Clear()
	resetEmitted()
	buffer.ProcessString("\x1b")
	if flushed := buffer.Flush(); strings.Join(flushed, "") != "\x1b" {
		t.Fatalf("flush = %q", flushed)
	}

	// ESC + CR split across chunks within the escape timeout merges into a
	// meta sequence.
	buffer.Clear()
	resetEmitted()
	buffer.ProcessString("\x1b")
	time.Sleep(25 * time.Millisecond) // > escape timeout
	buffer.ProcessString("\r")
	if got := emitted(); strings.Join(got, "|") != "\x1b|\r" {
		t.Fatalf("got %q", got)
	}
}

func TestStdinBufferMixedContent(t *testing.T) {
	buffer, emitted, resetEmitted := collectBuffer(StdinBufferOptions{Timeout: 10})

	buffer.ProcessString("a\x1b[B")
	if got := emitted(); strings.Join(got, "|") != "a|\x1b[B" {
		t.Fatalf("got %q", got)
	}

	buffer.Clear()
	resetEmitted()
	buffer.ProcessString("\x1b[Bb")
	if got := emitted(); strings.Join(got, "|") != "\x1b[B|b" {
		t.Fatalf("got %q", got)
	}

	buffer.Clear()
	resetEmitted()
	buffer.ProcessString("ab\x1b[<0;")
	if got := emitted(); strings.Join(got, "|") != "a|b" {
		t.Fatalf("got %q", got)
	}
}

func TestStdinBufferKittyProtocol(t *testing.T) {
	buffer, emitted, resetEmitted := collectBuffer(StdinBufferOptions{Timeout: 10})

	// Press and release events pass through individually.
	buffer.ProcessString("\x1b[97u\x1b[97;1:3u")
	if got := emitted(); strings.Join(got, "|") != "\x1b[97u|\x1b[97;1:3u" {
		t.Fatalf("got %q", got)
	}

	// Batched events split.
	buffer.Clear()
	resetEmitted()
	buffer.ProcessString("\x1b[104u\x1b[104;1:3u\x1b[105u\x1b[105;1:3u")
	if got := emitted(); strings.Join(got, "|") != "\x1b[104u|\x1b[104;1:3u|\x1b[105u|\x1b[105;1:3u" {
		t.Fatalf("got %q", got)
	}

	// WezTerm regression: ESC+ESC+CSI splits into a standalone ESC and the CSI
	// sequence when followed by a new escape.
	buffer.Clear()
	resetEmitted()
	buffer.ProcessString("\x1b\x1b[97;2u")
	if got := emitted(); strings.Join(got, "|") != "\x1b|\x1b[97;2u" {
		t.Fatalf("got %q", got)
	}

	// ESC+ESC alone stays as-is (e.g. ctrl+alt+[).
	buffer.Clear()
	resetEmitted()
	buffer.ProcessString("\x1b\x1b")
	if got := emitted(); strings.Join(got, "|") != "\x1b\x1b" {
		t.Fatalf("got %q", got)
	}

	// The raw duplicate character after a matching Kitty printable sequence is
	// dropped.
	buffer.Clear()
	resetEmitted()
	buffer.ProcessString("\x1b[224uà")
	if got := emitted(); strings.Join(got, "|") != "\x1b[224u" {
		t.Fatalf("got %q", got)
	}

	// ... across chunks.
	buffer.Clear()
	resetEmitted()
	buffer.ProcessString("\x1b[64u")
	buffer.ProcessString("@")
	if got := emitted(); strings.Join(got, "|") != "\x1b[64u" {
		t.Fatalf("got %q", got)
	}

	// Non-matching characters stay.
	buffer.Clear()
	resetEmitted()
	buffer.ProcessString("\x1b[97ub")
	if got := emitted(); strings.Join(got, "|") != "\x1b[97u|b" {
		t.Fatalf("got %q", got)
	}

	// Modified Kitty sequences do not suppress the raw character.
	buffer.Clear()
	resetEmitted()
	buffer.ProcessString("\x1b[64;3u@")
	if got := emitted(); strings.Join(got, "|") != "\x1b[64;3u|@" {
		t.Fatalf("got %q", got)
	}
}

func TestStdinBufferMouseEvents(t *testing.T) {
	buffer, emitted, resetEmitted := collectBuffer(StdinBufferOptions{Timeout: 10})

	for _, sequence := range []string{
		"\x1b[<0;10;5M",
		"\x1b[<0;10;5m",
		"\x1b[<35;20;5m",
	} {
		buffer.ProcessString(sequence)
		if got := emitted(); strings.Join(got, "|") != sequence {
			t.Fatalf("got %q", got)
		}
		buffer.Clear()
		resetEmitted()
	}

	// Split mouse events reassemble.
	buffer.Clear()
	resetEmitted()
	buffer.ProcessString("\x1b[<3")
	buffer.ProcessString("5;1")
	buffer.ProcessString("5;")
	buffer.ProcessString("10m")
	if got := emitted(); strings.Join(got, "") != "\x1b[<35;15;10m" {
		t.Fatalf("got %q", got)
	}

	// Old-style mouse: ESC[M + 3 bytes.
	buffer.Clear()
	resetEmitted()
	buffer.ProcessString("\x1b[M abc")
	if got := emitted(); strings.Join(got, "|") != "\x1b[M ab|c" {
		t.Fatalf("got %q", got)
	}

	buffer.Clear()
	resetEmitted()
	buffer.ProcessString("\x1b[M")
	if buffer.GetBuffer() != "\x1b[M" {
		t.Fatalf("buffer = %q", buffer.GetBuffer())
	}
	buffer.ProcessString(" a")
	if buffer.GetBuffer() != "\x1b[M a" {
		t.Fatalf("buffer = %q", buffer.GetBuffer())
	}
	buffer.ProcessString("b")
	if got := emitted(); strings.Join(got, "|") != "\x1b[M ab" {
		t.Fatalf("got %q", got)
	}
}

func TestStdinBufferBracketedPaste(t *testing.T) {
	var mu sync.Mutex
	var pastes []string
	var sequences []string
	buffer := NewStdinBuffer(StdinBufferOptions{Timeout: 10})
	buffer.OnData = func(sequence string) {
		mu.Lock()
		defer mu.Unlock()
		sequences = append(sequences, sequence)
	}
	buffer.OnPaste = func(content string) {
		mu.Lock()
		defer mu.Unlock()
		pastes = append(pastes, content)
	}

	buffer.ProcessString("pre\x1b[200~pasted text\x1b[201~post")
	mu.Lock()
	joinedPastes, joinedSequences := strings.Join(pastes, "|"), strings.Join(sequences, "|")
	mu.Unlock()
	if joinedPastes != "pasted text" {
		t.Fatalf("pastes = %q", joinedPastes)
	}
	// Content before the paste start is parsed as sequences; the content after
	// continues as normal input.
	if !strings.HasPrefix(joinedSequences, "p|r|e") {
		t.Fatalf("sequences = %q", joinedSequences)
	}
	if !strings.HasSuffix(joinedSequences, "p|o|s|t") {
		t.Fatalf("sequences = %q", joinedSequences)
	}

	// Paste content with escape-like data is not parsed.
	mu.Lock()
	pastes, sequences = nil, nil
	mu.Unlock()
	buffer.ProcessString("\x1b[200~\x1b[Bnot parsed\x1b[201~")
	mu.Lock()
	joinedPastes, joinedSequences = strings.Join(pastes, "|"), strings.Join(sequences, "|")
	mu.Unlock()
	if joinedPastes != "\x1b[Bnot parsed" {
		t.Fatalf("pastes = %q", joinedPastes)
	}
	if joinedSequences != "" {
		t.Fatalf("sequences = %q", joinedSequences)
	}
}

func TestStdinBufferHighByteConversion(t *testing.T) {
	buffer, emitted, _ := collectBuffer(StdinBufferOptions{Timeout: 10})

	// A single byte > 127 converts to ESC + (byte - 128).
	buffer.Process([]byte{0xE9}) // é
	time.Sleep(20 * time.Millisecond)
	if got := emitted(); strings.Join(got, "|") != "\x1bi" {
		t.Fatalf("got %q", got)
	}
}

func TestParseKeyboardProtocolNegotiationSequence(t *testing.T) {
	parsed := ParseKeyboardProtocolNegotiationSequence("\x1b[?7u")
	if parsed == nil || parsed.Kind != NegotiationKittyFlags || parsed.Flags != 7 {
		t.Fatalf("parsed = %+v", parsed)
	}

	parsed = ParseKeyboardProtocolNegotiationSequence("\x1b[?0u")
	if parsed == nil || parsed.Kind != NegotiationKittyFlags || parsed.Flags != 0 {
		t.Fatalf("parsed = %+v", parsed)
	}

	parsed = ParseKeyboardProtocolNegotiationSequence("\x1b[?62;c")
	if parsed == nil || parsed.Kind != NegotiationDeviceAttributes {
		t.Fatalf("parsed = %+v", parsed)
	}

	if ParseKeyboardProtocolNegotiationSequence("hello") != nil {
		t.Fatal("plain text parsed")
	}
	if ParseKeyboardProtocolNegotiationSequence("\x1b[?1;2u extra") != nil {
		t.Fatal("trailing garbage parsed")
	}

	if !isKeyboardProtocolNegotiationSequencePrefix("\x1b[?12") {
		t.Fatal("prefix not detected")
	}
	if isKeyboardProtocolNegotiationSequencePrefix("\x1b[?12x") {
		t.Fatal("non-prefix detected")
	}
}

func TestResolveEscapeTimeoutMs(t *testing.T) {
	if got := ResolveEscapeTimeoutMs(nil); got != 10 {
		t.Fatalf("default = %d", got)
	}
	if got := ResolveEscapeTimeoutMs(func(key string) string {
		if key == "SSH_CONNECTION" {
			return "1 2 3 4"
		}
		return ""
	}); got != 100 {
		t.Fatalf("ssh = %d", got)
	}
	if got := ResolveEscapeTimeoutMs(func(key string) string {
		if key == "PI_TUI_ESC_TIMEOUT" {
			return "250"
		}
		return ""
	}); got != 250 {
		t.Fatalf("configured = %d", got)
	}
	// The configured value wins over SSH detection.
	if got := ResolveEscapeTimeoutMs(func(key string) string {
		switch key {
		case "PI_TUI_ESC_TIMEOUT":
			return "0" // invalid: falls through
		case "SSH_TTY":
			return "/dev/pts/0"
		}
		return ""
	}); got != 100 {
		t.Fatalf("invalid configured = %d", got)
	}
}

func TestNormalizeNativeShiftEnterInput(t *testing.T) {
	if got := NormalizeNativeShiftEnterInput("\r", true, true); got != nativeShiftEnterSequence {
		t.Fatalf("got %q", got)
	}
	if got := NormalizeNativeShiftEnterInput("\r", true, false); got != "\r" {
		t.Fatalf("got %q", got)
	}
	if got := NormalizeNativeShiftEnterInput("x", true, true); got != "x" {
		t.Fatalf("got %q", got)
	}
	if got := NormalizeNativeShiftEnterInput("\r", false, true); got != "\r" {
		t.Fatalf("got %q", got)
	}
}

func TestKittyProtocolGlobals(t *testing.T) {
	if IsKittyProtocolActive() {
		t.Fatal("expected inactive by default")
	}
	SetKittyProtocolActive(true)
	if !IsKittyProtocolActive() {
		t.Fatal("expected active")
	}
	SetKittyProtocolActive(false)
	if IsKittyProtocolActive() {
		t.Fatal("expected inactive after reset")
	}
}
