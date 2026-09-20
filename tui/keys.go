package tui

// Port of src/keys.ts: key identifier matching, parsing, and Kitty /
// modifyOtherKeys decoding.
//
// Upstream's KeyId is a TypeScript string-literal union; Go uses a plain
// string alias and the Key helper object carries the same names (divergence
// D53).

import (
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

// ---- Global Kitty protocol state ----

var kittyProtocolState struct {
	mu     sync.Mutex
	active bool
}

// SetKittyProtocolActive sets the global Kitty keyboard protocol state.
// Called by ProcessTerminal after detecting protocol support.
func SetKittyProtocolActive(active bool) {
	kittyProtocolState.mu.Lock()
	defer kittyProtocolState.mu.Unlock()
	kittyProtocolState.active = active
}

// IsKittyProtocolActive queries the global Kitty keyboard protocol state.
func IsKittyProtocolActive() bool {
	kittyProtocolState.mu.Lock()
	defer kittyProtocolState.mu.Unlock()
	return kittyProtocolState.active
}

// ---- Key identifiers ----

// KeyId is a key identifier such as "escape", "ctrl+c", or "shift+ctrl+p".
type KeyId = string

// Key mirrors upstream's Key helper object.
var Key = struct {
	// Special keys.
	Escape    string
	Esc       string
	Enter     string
	Return    string
	Tab       string
	Space     string
	Backspace string
	Delete    string
	Insert    string
	Clear     string
	Home      string
	End       string
	PageUp    string
	PageDown  string
	Up        string
	Down      string
	Left      string
	Right     string
	F1        string
	F2        string
	F3        string
	F4        string
	F5        string
	F6        string
	F7        string
	F8        string
	F9        string
	F10       string
	F11       string
	F12       string

	// Symbol keys.
	Backtick     string
	Hyphen       string
	Equals       string
	LeftBracket  string
	RightBracket string
	Backslash    string
	Semicolon    string
	Quote        string
	Comma        string
	Period       string
	Slash        string
	Exclamation  string
	At           string
	Hash         string
	Dollar       string
	Percent      string
	Caret        string
	Ampersand    string
	Asterisk     string
	LeftParen    string
	RightParen   string
	Underscore   string
	Plus         string
	Pipe         string
	Tilde        string
	LeftBrace    string
	RightBrace   string
	Colon        string
	LessThan     string
	GreaterThan  string
	Question     string

	// Modifier helpers.
	Ctrl           func(key string) string
	Shift          func(key string) string
	Alt            func(key string) string
	Super          func(key string) string
	CtrlShift      func(key string) string
	ShiftCtrl      func(key string) string
	CtrlAlt        func(key string) string
	AltCtrl        func(key string) string
	ShiftAlt       func(key string) string
	AltShift       func(key string) string
	CtrlSuper      func(key string) string
	SuperCtrl      func(key string) string
	CtrlShiftAlt   func(key string) string
	CtrlShiftSuper func(key string) string
}{
	Escape: "escape", Esc: "esc", Enter: "enter", Return: "return", Tab: "tab", Space: "space",
	Backspace: "backspace", Delete: "delete", Insert: "insert", Clear: "clear", Home: "home", End: "end",
	PageUp: "pageUp", PageDown: "pageDown", Up: "up", Down: "down", Left: "left", Right: "right",
	F1: "f1", F2: "f2", F3: "f3", F4: "f4", F5: "f5", F6: "f6", F7: "f7", F8: "f8", F9: "f9",
	F10: "f10", F11: "f11", F12: "f12",
	Backtick: "`", Hyphen: "-", Equals: "=", LeftBracket: "[", RightBracket: "]", Backslash: "\\",
	Semicolon: ";", Quote: "'", Comma: ",", Period: ".", Slash: "/", Exclamation: "!", At: "@",
	Hash: "#", Dollar: "$", Percent: "%", Caret: "^", Ampersand: "&", Asterisk: "*", LeftParen: "(",
	RightParen: ")", Underscore: "_", Plus: "+", Pipe: "|", Tilde: "~", LeftBrace: "{", RightBrace: "}",
	Colon: ":", LessThan: "<", GreaterThan: ">", Question: "?",
	Ctrl:           func(key string) string { return "ctrl+" + key },
	Shift:          func(key string) string { return "shift+" + key },
	Alt:            func(key string) string { return "alt+" + key },
	Super:          func(key string) string { return "super+" + key },
	CtrlShift:      func(key string) string { return "ctrl+shift+" + key },
	ShiftCtrl:      func(key string) string { return "shift+ctrl+" + key },
	CtrlAlt:        func(key string) string { return "ctrl+alt+" + key },
	AltCtrl:        func(key string) string { return "alt+ctrl+" + key },
	ShiftAlt:       func(key string) string { return "shift+alt+" + key },
	AltShift:       func(key string) string { return "alt+shift+" + key },
	CtrlSuper:      func(key string) string { return "ctrl+super+" + key },
	SuperCtrl:      func(key string) string { return "super+ctrl+" + key },
	CtrlShiftAlt:   func(key string) string { return "ctrl+shift+alt+" + key },
	CtrlShiftSuper: func(key string) string { return "ctrl+shift+super+" + key },
}

var symbolKeys = map[string]bool{
	"`": true, "-": true, "=": true, "[": true, "]": true, "\\": true, ";": true, "'": true,
	",": true, ".": true, "/": true, "!": true, "@": true, "#": true, "$": true, "%": true,
	"^": true, "&": true, "*": true, "(": true, ")": true, "_": true, "+": true, "|": true,
	"~": true, "{": true, "}": true, ":": true, "<": true, ">": true, "?": true,
}

const (
	modShift = 1
	modAlt   = 2
	modCtrl  = 4
	modSuper = 8
	// lockMask covers Caps Lock + Num Lock.
	lockMask = 64 + 128
)

const (
	cpEscape    = 27
	cpTab       = 9
	cpEnter     = 13
	cpSpace     = 32
	cpBackspace = 127
	// cpKpEnter is Numpad Enter (Kitty protocol).
	cpKpEnter = 57414
)

const (
	arrowUp    = -1
	arrowDown  = -2
	arrowRight = -3
	arrowLeft  = -4
)

const (
	funcDelete   = -10
	funcInsert   = -11
	funcPageUp   = -12
	funcPageDown = -13
	funcHome     = -14
	funcEnd      = -15
)

var kittyFunctionalKeyEquivalents = map[int]int{
	57399: 48, 57400: 49, 57401: 50, 57402: 51, 57403: 52, 57404: 53, 57405: 54,
	57406: 55, 57407: 56, 57408: 57, 57409: 46, 57410: 47, 57411: 42, 57412: 45,
	57413: 43, 57415: 61, 57416: 44,
	57417: arrowLeft, 57418: arrowRight, 57419: arrowUp, 57420: arrowDown,
	57421: funcPageUp, 57422: funcPageDown, 57423: funcHome, 57424: funcEnd,
	57425: funcInsert, 57426: funcDelete,
}

func normalizeKittyFunctionalCodepoint(codepoint int) int {
	if equivalent, ok := kittyFunctionalKeyEquivalents[codepoint]; ok {
		return equivalent
	}
	return codepoint
}

func normalizeShiftedLetterIdentityCodepoint(codepoint int, modifier int) int {
	effectiveModifier := modifier & ^lockMask
	if effectiveModifier&modShift != 0 && codepoint >= 65 && codepoint <= 90 {
		return codepoint + 32
	}
	return codepoint
}

var legacyKeySequences = map[string][]string{
	"up":       {"\x1b[A", "\x1bOA"},
	"down":     {"\x1b[B", "\x1bOB"},
	"right":    {"\x1b[C", "\x1bOC"},
	"left":     {"\x1b[D", "\x1bOD"},
	"home":     {"\x1b[H", "\x1bOH", "\x1b[1~", "\x1b[7~"},
	"end":      {"\x1b[F", "\x1bOF", "\x1b[4~", "\x1b[8~"},
	"insert":   {"\x1b[2~"},
	"delete":   {"\x1b[3~"},
	"pageUp":   {"\x1b[5~", "\x1b[[5~"},
	"pageDown": {"\x1b[6~", "\x1b[[6~"},
	"clear":    {"\x1b[E", "\x1bOE"},
	"f1":       {"\x1bOP", "\x1b[11~", "\x1b[[A"},
	"f2":       {"\x1bOQ", "\x1b[12~", "\x1b[[B"},
	"f3":       {"\x1bOR", "\x1b[13~", "\x1b[[C"},
	"f4":       {"\x1bOS", "\x1b[14~", "\x1b[[D"},
	"f5":       {"\x1b[15~", "\x1b[[E"},
	"f6":       {"\x1b[17~"},
	"f7":       {"\x1b[18~"},
	"f8":       {"\x1b[19~"},
	"f9":       {"\x1b[20~"},
	"f10":      {"\x1b[21~"},
	"f11":      {"\x1b[23~"},
	"f12":      {"\x1b[24~"},
}

var legacyShiftSequences = map[string][]string{
	"up":       {"\x1b[a"},
	"down":     {"\x1b[b"},
	"right":    {"\x1b[c"},
	"left":     {"\x1b[d"},
	"clear":    {"\x1b[e"},
	"insert":   {"\x1b[2$"},
	"delete":   {"\x1b[3$"},
	"pageUp":   {"\x1b[5$"},
	"pageDown": {"\x1b[6$"},
	"home":     {"\x1b[7$"},
	"end":      {"\x1b[8$"},
}

var legacyCtrlSequences = map[string][]string{
	"up":       {"\x1bOa"},
	"down":     {"\x1bOb"},
	"right":    {"\x1bOc"},
	"left":     {"\x1bOd"},
	"clear":    {"\x1bOe"},
	"insert":   {"\x1b[2^"},
	"delete":   {"\x1b[3^"},
	"pageUp":   {"\x1b[5^"},
	"pageDown": {"\x1b[6^"},
	"home":     {"\x1b[7^"},
	"end":      {"\x1b[8^"},
}

var legacySequenceKeyIDs = map[string]string{
	"\x1bOA": "up", "\x1bOB": "down", "\x1bOC": "right", "\x1bOD": "left",
	"\x1bOH": "home", "\x1bOF": "end", "\x1b[E": "clear", "\x1bOE": "clear",
	"\x1bOe": "ctrl+clear", "\x1b[e": "shift+clear",
	"\x1b[2~": "insert", "\x1b[2$": "shift+insert", "\x1b[2^": "ctrl+insert",
	"\x1b[3$": "shift+delete", "\x1b[3^": "ctrl+delete",
	"\x1b[[5~": "pageUp", "\x1b[[6~": "pageDown",
	"\x1b[a": "shift+up", "\x1b[b": "shift+down", "\x1b[c": "shift+right", "\x1b[d": "shift+left",
	"\x1bOa": "ctrl+up", "\x1bOb": "ctrl+down", "\x1bOc": "ctrl+right", "\x1bOd": "ctrl+left",
	"\x1b[5$": "shift+pageUp", "\x1b[6$": "shift+pageDown", "\x1b[7$": "shift+home", "\x1b[8$": "shift+end",
	"\x1b[5^": "ctrl+pageUp", "\x1b[6^": "ctrl+pageDown", "\x1b[7^": "ctrl+home", "\x1b[8^": "ctrl+end",
	"\x1bOP": "f1", "\x1bOQ": "f2", "\x1bOR": "f3", "\x1bOS": "f4",
	"\x1b[11~": "f1", "\x1b[12~": "f2", "\x1b[13~": "f3", "\x1b[14~": "f4",
	"\x1b[[A": "f1", "\x1b[[B": "f2", "\x1b[[C": "f3", "\x1b[[D": "f4", "\x1b[[E": "f5",
	"\x1b[15~": "f5", "\x1b[17~": "f6", "\x1b[18~": "f7", "\x1b[19~": "f8",
	"\x1b[20~": "f9", "\x1b[21~": "f10", "\x1b[23~": "f11", "\x1b[24~": "f12",
	"\x1bb": "alt+left", "\x1bf": "alt+right", "\x1bp": "alt+up", "\x1bn": "alt+down",
}

func matchesLegacySequence(data string, sequences []string) bool {
	for _, sequence := range sequences {
		if data == sequence {
			return true
		}
	}
	return false
}

func matchesLegacyModifierSequence(data string, key string, modifier int) bool {
	if modifier == modShift {
		return matchesLegacySequence(data, legacyShiftSequences[key])
	}
	if modifier == modCtrl {
		return matchesLegacySequence(data, legacyCtrlSequences[key])
	}
	return false
}

// ---- Kitty protocol parsing ----

// KeyEventType is the event type from the Kitty keyboard protocol (flag 2):
// 1 = press, 2 = repeat, 3 = release.
type KeyEventType string

const (
	KeyEventPress   KeyEventType = "press"
	KeyEventRepeat  KeyEventType = "repeat"
	KeyEventRelease KeyEventType = "release"
)

type parsedKittySequence struct {
	codepoint     int
	shiftedKey    int
	hasShiftedKey bool
	baseLayoutKey int
	hasBaseLayout bool
	modifier      int
	eventType     KeyEventType
}

type parsedModifyOtherKeysSequence struct {
	codepoint int
	modifier  int
}

// lastEventType stores the last parsed event type (upstream keeps this
// module-level state; nothing reads it, but it is ported for fidelity).
var lastEventTypeState struct {
	mu        sync.Mutex
	eventType KeyEventType
}

func setLastEventType(eventType KeyEventType) {
	lastEventTypeState.mu.Lock()
	defer lastEventTypeState.mu.Unlock()
	lastEventTypeState.eventType = eventType
}

// LastKeyEventType returns the last parsed event type.
func LastKeyEventType() KeyEventType {
	lastEventTypeState.mu.Lock()
	defer lastEventTypeState.mu.Unlock()
	if lastEventTypeState.eventType == "" {
		return KeyEventPress
	}
	return lastEventTypeState.eventType
}

// IsKeyRelease reports whether the input is a Kitty protocol key-release event.
// Release events with flag 2 contain ":3" after the modifier.
func IsKeyRelease(data string) bool {
	// Bracketed paste content must not be treated as a key release, even when
	// it contains patterns like ":3F" (e.g. bluetooth MAC addresses). The
	// terminal re-wraps pasted data with bracketed paste markers, so pasted
	// data always contains \x1b[200~.
	if strings.Contains(data, "\x1b[200~") {
		return false
	}
	return containsAny(data,
		":3u", ":3~", ":3A", ":3B", ":3C", ":3D", ":3H", ":3F")
}

// IsKeyRepeat reports whether the input is a Kitty protocol key-repeat event.
// Only meaningful when the Kitty keyboard protocol with flag 2 is active.
func IsKeyRepeat(data string) bool {
	// See IsKeyRelease for why bracketed paste content is excluded.
	if strings.Contains(data, "\x1b[200~") {
		return false
	}
	return containsAny(data,
		":2u", ":2~", ":2A", ":2B", ":2C", ":2D", ":2H", ":2F")
}

func parseEventType(eventTypeStr string, has bool) KeyEventType {
	if !has || eventTypeStr == "" {
		return KeyEventPress
	}
	switch eventTypeStr {
	case "2":
		return KeyEventRepeat
	case "3":
		return KeyEventRelease
	}
	return KeyEventPress
}

var (
	kittyCsiURegex       = regexp.MustCompile(`^\x1b\[(\d+)(?::(\d*))?(?::(\d+))?(?:;(\d+))?(?::(\d+))?u$`)
	kittyArrowRegex      = regexp.MustCompile(`^\x1b\[1;(\d+)(?::(\d+))?([ABCD])$`)
	kittyFuncRegex       = regexp.MustCompile(`^\x1b\[(\d+)(?:;(\d+))?(?::(\d+))?~$`)
	kittyHomeEndRegex    = regexp.MustCompile(`^\x1b\[1;(\d+)(?::(\d+))?([HF])$`)
	modifyOtherKeysRegex = regexp.MustCompile(`^\x1b\[27;(\d+);(\d+)~$`)
)

func atoiSafe(s string) int {
	value, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return value
}

func parseKittySequence(data string) (parsedKittySequence, bool) {
	// CSI u format with alternate keys (flag 4):
	// \x1b[<codepoint>u
	// \x1b[<codepoint>;<mod>u
	// \x1b[<codepoint>;<mod>:<event>u
	// \x1b[<codepoint>:<shifted>;<mod>u
	// \x1b[<codepoint>:<shifted>:<base>;<mod>u
	// \x1b[<codepoint>::<base>;<mod>u (no shifted key, only base)
	//
	// With flag 2 the event type follows the modifier colon; with flag 4
	// alternate keys follow the codepoint with colons.
	if match := kittyCsiURegex.FindStringSubmatch(data); match != nil {
		parsed := parsedKittySequence{codepoint: atoiSafe(match[1])}
		if match[2] != "" {
			parsed.shiftedKey = atoiSafe(match[2])
			parsed.hasShiftedKey = true
		}
		if match[3] != "" {
			parsed.baseLayoutKey = atoiSafe(match[3])
			parsed.hasBaseLayout = true
		}
		modValue := 1
		if match[4] != "" {
			modValue = atoiSafe(match[4])
		}
		parsed.eventType = parseEventType(match[5], match[5] != "")
		parsed.modifier = modValue - 1
		setLastEventType(parsed.eventType)
		return parsed, true
	}

	// Arrow keys with modifier: \x1b[1;<mod>A/B/C/D or \x1b[1;<mod>:<event>A/B/C/D.
	if match := kittyArrowRegex.FindStringSubmatch(data); match != nil {
		modValue := atoiSafe(match[1])
		parsed := parsedKittySequence{modifier: modValue - 1}
		parsed.eventType = parseEventType(match[2], match[2] != "")
		switch match[3] {
		case "A":
			parsed.codepoint = arrowUp
		case "B":
			parsed.codepoint = arrowDown
		case "C":
			parsed.codepoint = arrowRight
		case "D":
			parsed.codepoint = arrowLeft
		}
		setLastEventType(parsed.eventType)
		return parsed, true
	}

	// Functional keys: \x1b[<num>~ or \x1b[<num>;<mod>~ or
	// \x1b[<num>;<mod>:<event>~.
	if match := kittyFuncRegex.FindStringSubmatch(data); match != nil {
		keyNum := atoiSafe(match[1])
		modValue := 1
		if match[2] != "" {
			modValue = atoiSafe(match[2])
		}
		funcCodes := map[int]int{
			2: funcInsert, 3: funcDelete, 5: funcPageUp, 6: funcPageDown, 7: funcHome, 8: funcEnd,
		}
		if codepoint, ok := funcCodes[keyNum]; ok {
			parsed := parsedKittySequence{codepoint: codepoint, modifier: modValue - 1}
			parsed.eventType = parseEventType(match[3], match[3] != "")
			setLastEventType(parsed.eventType)
			return parsed, true
		}
	}

	// Home/End with modifier: \x1b[1;<mod>H/F or \x1b[1;<mod>:<event>H/F.
	if match := kittyHomeEndRegex.FindStringSubmatch(data); match != nil {
		modValue := atoiSafe(match[1])
		codepoint := funcEnd
		if match[3] == "H" {
			codepoint = funcHome
		}
		parsed := parsedKittySequence{codepoint: codepoint, modifier: modValue - 1}
		parsed.eventType = parseEventType(match[2], match[2] != "")
		setLastEventType(parsed.eventType)
		return parsed, true
	}

	return parsedKittySequence{}, false
}

func matchesKittySequence(data string, expectedCodepoint int, expectedModifier int) bool {
	parsed, ok := parseKittySequence(data)
	if !ok {
		return false
	}
	actualMod := parsed.modifier & ^lockMask
	expectedMod := expectedModifier & ^lockMask
	if actualMod != expectedMod {
		return false
	}

	normalizedCodepoint := normalizeShiftedLetterIdentityCodepoint(
		normalizeKittyFunctionalCodepoint(parsed.codepoint), parsed.modifier)
	normalizedExpectedCodepoint := normalizeShiftedLetterIdentityCodepoint(
		normalizeKittyFunctionalCodepoint(expectedCodepoint), expectedModifier)

	// Primary match: the codepoint matches directly after normalizing
	// functional keys.
	if normalizedCodepoint == normalizedExpectedCodepoint {
		return true
	}

	// Alternate match: use the base layout key for non-Latin keyboard
	// layouts, so Ctrl+С (Cyrillic) matches Ctrl+c (Latin). Only fall back
	// when the codepoint is not already a recognized Latin letter (a-z) or
	// symbol: when the codepoint is a recognized key it is authoritative
	// regardless of physical position, which prevents remapped layouts
	// (Dvorak, Colemak, xremap) from causing false matches.
	if parsed.hasBaseLayout && parsed.baseLayoutKey == expectedCodepoint {
		cp := normalizedCodepoint
		isLatinLetter := cp >= 97 && cp <= 122
		isKnownSymbol := symbolKeys[string(rune(cp))]
		if !isLatinLetter && !isKnownSymbol {
			return true
		}
	}

	return false
}

func parseModifyOtherKeysSequence(data string) (parsedModifyOtherKeysSequence, bool) {
	match := modifyOtherKeysRegex.FindStringSubmatch(data)
	if match == nil {
		return parsedModifyOtherKeysSequence{}, false
	}
	return parsedModifyOtherKeysSequence{
		modifier:  atoiSafe(match[1]) - 1,
		codepoint: atoiSafe(match[2]),
	}, true
}

// matchesModifyOtherKeys matches the xterm modifyOtherKeys format:
// CSI 27 ; modifiers ; keycode ~. Modifier values are 1-indexed.
func matchesModifyOtherKeys(data string, expectedKeycode int, expectedModifier int) bool {
	parsed, ok := parseModifyOtherKeysSequence(data)
	if !ok {
		return false
	}
	return parsed.codepoint == expectedKeycode && parsed.modifier == expectedModifier
}

func isWindowsTerminalSession() bool {
	return os.Getenv("WT_SESSION") != "" && os.Getenv("SSH_CONNECTION") == "" &&
		os.Getenv("SSH_CLIENT") == "" && os.Getenv("SSH_TTY") == ""
}

// matchesRawBackspace handles raw 0x08 (BS), which is ambiguous in legacy
// terminals: Windows Terminal uses it for Ctrl+Backspace, while some legacy
// terminals and tmux setups send it for plain Backspace. Explicit Kitty /
// CSI-u / modifyOtherKeys sequences are preferred; the Windows Terminal
// heuristic only applies to the raw BS byte.
func matchesRawBackspace(data string, expectedModifier int) bool {
	if data == "\x7f" {
		return expectedModifier == 0
	}
	if data != "\x08" {
		return false
	}
	if isWindowsTerminalSession() {
		return expectedModifier == modCtrl
	}
	return expectedModifier == 0
}

// rawCtrlChar returns the control character for a key using the universal
// formula code & 0x1f.
func rawCtrlChar(key string) (string, bool) {
	char := strings.ToLower(key)
	if len(char) == 0 {
		return "", false
	}
	code := int(char[0])
	if (code >= 97 && code <= 122) || char == "[" || char == "\\" || char == "]" || char == "_" {
		return string(rune(code & 0x1f)), true
	}
	// "-" maps to the same physical key as "_" on US keyboards.
	if char == "-" {
		return string(rune(31)), true
	}
	return "", false
}

func isDigitKey(key string) bool { return key >= "0" && key <= "9" }

func matchesPrintableModifyOtherKeys(data string, expectedKeycode int, expectedModifier int) bool {
	if expectedModifier == 0 {
		return false
	}
	parsed, ok := parseModifyOtherKeysSequence(data)
	if !ok || parsed.modifier != expectedModifier {
		return false
	}
	return normalizeShiftedLetterIdentityCodepoint(parsed.codepoint, parsed.modifier) ==
		normalizeShiftedLetterIdentityCodepoint(expectedKeycode, expectedModifier)
}

func formatKeyNameWithModifiers(keyName string, modifier int) (string, bool) {
	var mods []string
	effectiveMod := modifier & ^lockMask
	supportedModifierMask := modShift | modCtrl | modAlt | modSuper
	if effectiveMod&^supportedModifierMask != 0 {
		return "", false
	}
	if effectiveMod&modShift != 0 {
		mods = append(mods, "shift")
	}
	if effectiveMod&modCtrl != 0 {
		mods = append(mods, "ctrl")
	}
	if effectiveMod&modAlt != 0 {
		mods = append(mods, "alt")
	}
	if effectiveMod&modSuper != 0 {
		mods = append(mods, "super")
	}
	if len(mods) > 0 {
		return strings.Join(mods, "+") + "+" + keyName, true
	}
	return keyName, true
}

type parsedKeyID struct {
	key   string
	ctrl  bool
	shift bool
	alt   bool
	super bool
}

func parseKeyID(keyID string) (parsedKeyID, bool) {
	parts := strings.Split(strings.ToLower(keyID), "+")
	key := parts[len(parts)-1]
	if key == "" {
		return parsedKeyID{}, false
	}
	has := func(target string) bool {
		for _, part := range parts {
			if part == target {
				return true
			}
		}
		return false
	}
	return parsedKeyID{key: key, ctrl: has("ctrl"), shift: has("shift"), alt: has("alt"), super: has("super")}, true
}

// MatchesKey matches input data against a key identifier string.
//
// Supported identifiers: single keys ("escape", "tab", "enter", "backspace",
// "delete", "home", "end", "space"), arrows ("up", "down", "left", "right"),
// modifiers ("ctrl+c", "shift+tab", "alt+enter", "super+k"), and combined
// modifiers ("shift+ctrl+p", "ctrl+alt+x", "ctrl+super+k").
func MatchesKey(data string, keyID KeyId) bool {
	parsed, ok := parseKeyID(keyID)
	if !ok {
		return false
	}
	key := parsed.key
	modifier := 0
	if parsed.shift {
		modifier |= modShift
	}
	if parsed.alt {
		modifier |= modAlt
	}
	if parsed.ctrl {
		modifier |= modCtrl
	}
	if parsed.super {
		modifier |= modSuper
	}

	switch key {
	case "escape", "esc":
		if modifier != 0 {
			return false
		}
		return data == "\x1b" || matchesKittySequence(data, cpEscape, 0) ||
			matchesModifyOtherKeys(data, cpEscape, 0)

	case "space":
		if !IsKittyProtocolActive() {
			if modifier == modCtrl && data == "\x00" {
				return true
			}
			if modifier == modAlt && data == "\x1b " {
				return true
			}
		}
		if modifier == 0 {
			return data == " " || matchesKittySequence(data, cpSpace, 0) ||
				matchesModifyOtherKeys(data, cpSpace, 0)
		}
		return matchesKittySequence(data, cpSpace, modifier) ||
			matchesModifyOtherKeys(data, cpSpace, modifier)

	case "tab":
		if modifier == modShift {
			return data == "\x1b[Z" || matchesKittySequence(data, cpTab, modShift) ||
				matchesModifyOtherKeys(data, cpTab, modShift)
		}
		if modifier == 0 {
			return data == "\t" || matchesKittySequence(data, cpTab, 0)
		}
		return matchesKittySequence(data, cpTab, modifier) ||
			matchesModifyOtherKeys(data, cpTab, modifier)

	case "enter", "return":
		if modifier == modShift {
			if matchesKittySequence(data, cpEnter, modShift) ||
				matchesKittySequence(data, cpKpEnter, modShift) {
				return true
			}
			if matchesModifyOtherKeys(data, cpEnter, modShift) {
				return true
			}
			// With Kitty active, legacy sequences are custom terminal
			// mappings: \x1b\r is Kitty's "map shift+enter send_text all
			// \e\r", \n is Ghostty's "keybind = shift+enter=text:\n".
			if IsKittyProtocolActive() {
				return data == "\x1b\r" || data == "\n"
			}
			return false
		}
		if modifier == modAlt {
			if matchesKittySequence(data, cpEnter, modAlt) ||
				matchesKittySequence(data, cpKpEnter, modAlt) {
				return true
			}
			if matchesModifyOtherKeys(data, cpEnter, modAlt) {
				return true
			}
			// \x1b\r is alt+enter only in legacy mode.
			if !IsKittyProtocolActive() {
				return data == "\x1b\r"
			}
			return false
		}
		if modifier == 0 {
			return data == "\r" ||
				(!IsKittyProtocolActive() && data == "\n") ||
				data == "\x1bOM" || // SS3 M (numpad enter in some terminals)
				matchesKittySequence(data, cpEnter, 0) ||
				matchesKittySequence(data, cpKpEnter, 0)
		}
		return matchesKittySequence(data, cpEnter, modifier) ||
			matchesKittySequence(data, cpKpEnter, modifier) ||
			matchesModifyOtherKeys(data, cpEnter, modifier)

	case "backspace":
		if modifier == modAlt {
			if data == "\x1b\x7f" || data == "\x1b\b" {
				return true
			}
			return matchesKittySequence(data, cpBackspace, modAlt) ||
				matchesModifyOtherKeys(data, cpBackspace, modAlt)
		}
		if modifier == modCtrl {
			// Legacy raw 0x08 is ambiguous (Ctrl+Backspace on Windows
			// Terminal, plain Backspace elsewhere, overlapping Ctrl+H).
			if matchesRawBackspace(data, modCtrl) {
				return true
			}
			return matchesKittySequence(data, cpBackspace, modCtrl) ||
				matchesModifyOtherKeys(data, cpBackspace, modCtrl)
		}
		if modifier == 0 {
			return matchesRawBackspace(data, 0) ||
				matchesKittySequence(data, cpBackspace, 0) ||
				matchesModifyOtherKeys(data, cpBackspace, 0)
		}
		return matchesKittySequence(data, cpBackspace, modifier) ||
			matchesModifyOtherKeys(data, cpBackspace, modifier)

	case "insert":
		if modifier == 0 {
			return matchesLegacySequence(data, legacyKeySequences["insert"]) ||
				matchesKittySequence(data, funcInsert, 0)
		}
		if matchesLegacyModifierSequence(data, "insert", modifier) {
			return true
		}
		return matchesKittySequence(data, funcInsert, modifier)

	case "delete":
		if modifier == 0 {
			return matchesLegacySequence(data, legacyKeySequences["delete"]) ||
				matchesKittySequence(data, funcDelete, 0)
		}
		if matchesLegacyModifierSequence(data, "delete", modifier) {
			return true
		}
		return matchesKittySequence(data, funcDelete, modifier)

	case "clear":
		if modifier == 0 {
			return matchesLegacySequence(data, legacyKeySequences["clear"])
		}
		return matchesLegacyModifierSequence(data, "clear", modifier)

	case "home":
		if modifier == 0 {
			return matchesLegacySequence(data, legacyKeySequences["home"]) ||
				matchesKittySequence(data, funcHome, 0)
		}
		if matchesLegacyModifierSequence(data, "home", modifier) {
			return true
		}
		return matchesKittySequence(data, funcHome, modifier)

	case "end":
		if modifier == 0 {
			return matchesLegacySequence(data, legacyKeySequences["end"]) ||
				matchesKittySequence(data, funcEnd, 0)
		}
		if matchesLegacyModifierSequence(data, "end", modifier) {
			return true
		}
		return matchesKittySequence(data, funcEnd, modifier)

	case "pageup":
		if modifier == 0 {
			return matchesLegacySequence(data, legacyKeySequences["pageUp"]) ||
				matchesKittySequence(data, funcPageUp, 0)
		}
		if matchesLegacyModifierSequence(data, "pageUp", modifier) {
			return true
		}
		return matchesKittySequence(data, funcPageUp, modifier)

	case "pagedown":
		if modifier == 0 {
			return matchesLegacySequence(data, legacyKeySequences["pageDown"]) ||
				matchesKittySequence(data, funcPageDown, 0)
		}
		if matchesLegacyModifierSequence(data, "pageDown", modifier) {
			return true
		}
		return matchesKittySequence(data, funcPageDown, modifier)

	case "up":
		if modifier == modAlt {
			return data == "\x1bp" || matchesKittySequence(data, arrowUp, modAlt)
		}
		if modifier == 0 {
			return matchesLegacySequence(data, legacyKeySequences["up"]) ||
				matchesKittySequence(data, arrowUp, 0)
		}
		if matchesLegacyModifierSequence(data, "up", modifier) {
			return true
		}
		return matchesKittySequence(data, arrowUp, modifier)

	case "down":
		if modifier == modAlt {
			return data == "\x1bn" || matchesKittySequence(data, arrowDown, modAlt)
		}
		if modifier == 0 {
			return matchesLegacySequence(data, legacyKeySequences["down"]) ||
				matchesKittySequence(data, arrowDown, 0)
		}
		if matchesLegacyModifierSequence(data, "down", modifier) {
			return true
		}
		return matchesKittySequence(data, arrowDown, modifier)

	case "left":
		if modifier == modAlt {
			return data == "\x1b[1;3D" ||
				(!IsKittyProtocolActive() && data == "\x1bB") ||
				data == "\x1bb" ||
				matchesKittySequence(data, arrowLeft, modAlt)
		}
		if modifier == modCtrl {
			return data == "\x1b[1;5D" ||
				matchesLegacyModifierSequence(data, "left", modCtrl) ||
				matchesKittySequence(data, arrowLeft, modCtrl)
		}
		if modifier == 0 {
			return matchesLegacySequence(data, legacyKeySequences["left"]) ||
				matchesKittySequence(data, arrowLeft, 0)
		}
		if matchesLegacyModifierSequence(data, "left", modifier) {
			return true
		}
		return matchesKittySequence(data, arrowLeft, modifier)

	case "right":
		if modifier == modAlt {
			return data == "\x1b[1;3C" ||
				(!IsKittyProtocolActive() && data == "\x1bF") ||
				data == "\x1bf" ||
				matchesKittySequence(data, arrowRight, modAlt)
		}
		if modifier == modCtrl {
			return data == "\x1b[1;5C" ||
				matchesLegacyModifierSequence(data, "right", modCtrl) ||
				matchesKittySequence(data, arrowRight, modCtrl)
		}
		if modifier == 0 {
			return matchesLegacySequence(data, legacyKeySequences["right"]) ||
				matchesKittySequence(data, arrowRight, 0)
		}
		if matchesLegacyModifierSequence(data, "right", modifier) {
			return true
		}
		return matchesKittySequence(data, arrowRight, modifier)

	case "f1", "f2", "f3", "f4", "f5", "f6", "f7", "f8", "f9", "f10", "f11", "f12":
		if modifier != 0 {
			return false
		}
		return matchesLegacySequence(data, legacyKeySequences[key])
	}

	// Handle single letter/digit/symbol keys.
	if len(key) == 1 && ((key >= "a" && key <= "z") || isDigitKey(key) || symbolKeys[key]) {
		codepoint := int(key[0])
		rawCtrl, hasRawCtrl := rawCtrlChar(key)
		isLetter := key >= "a" && key <= "z"
		isDigit := isDigitKey(key)

		if modifier == modCtrl+modAlt && !IsKittyProtocolActive() && hasRawCtrl {
			// Legacy: ctrl+alt+key is ESC followed by the control character.
			// If that does not match, continue so CSI-u and modifyOtherKeys
			// sequences from tmux can still be recognized.
			if data == "\x1b"+rawCtrl {
				return true
			}
		}

		if modifier == modAlt && !IsKittyProtocolActive() && (isLetter || isDigit || symbolKeys[key]) {
			// Legacy: alt+printable key is ESC followed by the key.
			if data == "\x1b"+key {
				return true
			}
		}

		if modifier == modCtrl {
			// Legacy: ctrl+key sends the control character.
			if hasRawCtrl && data == rawCtrl {
				return true
			}
			return matchesKittySequence(data, codepoint, modCtrl) ||
				matchesPrintableModifyOtherKeys(data, codepoint, modCtrl)
		}

		if modifier == modShift+modCtrl {
			return matchesKittySequence(data, codepoint, modShift+modCtrl) ||
				matchesPrintableModifyOtherKeys(data, codepoint, modShift+modCtrl)
		}

		if modifier == modShift {
			// Legacy: shift+letter produces uppercase.
			if isLetter && data == strings.ToUpper(key) {
				return true
			}
			return matchesKittySequence(data, codepoint, modShift) ||
				matchesPrintableModifyOtherKeys(data, codepoint, modShift)
		}

		if modifier != 0 {
			return matchesKittySequence(data, codepoint, modifier) ||
				matchesPrintableModifyOtherKeys(data, codepoint, modifier)
		}

		// Check both the raw char and the Kitty sequence (needed for release
		// events).
		return data == key || matchesKittySequence(data, codepoint, 0)
	}

	return false
}

// formatParsedKey formats a parsed codepoint and modifier as a key id.
func formatParsedKey(codepoint int, modifier int, baseLayoutKey int, hasBaseLayout bool) (string, bool) {
	normalizedCodepoint := normalizeKittyFunctionalCodepoint(codepoint)
	identityCodepoint := normalizeShiftedLetterIdentityCodepoint(normalizedCodepoint, modifier)

	// Use the base layout key only when the codepoint is not a recognized
	// Latin letter (a-z), digit (0-9), or symbol. For those the codepoint is
	// authoritative regardless of physical key position, which prevents
	// remapped layouts from reporting the wrong key name.
	isLatinLetter := identityCodepoint >= 97 && identityCodepoint <= 122
	isDigit := identityCodepoint >= 48 && identityCodepoint <= 57
	isKnownSymbol := symbolKeys[string(rune(identityCodepoint))]
	effectiveCodepoint := identityCodepoint
	if !isLatinLetter && !isDigit && !isKnownSymbol && hasBaseLayout {
		effectiveCodepoint = baseLayoutKey
	}

	var keyName string
	switch {
	case effectiveCodepoint == cpEscape:
		keyName = "escape"
	case effectiveCodepoint == cpTab:
		keyName = "tab"
	case effectiveCodepoint == cpEnter || effectiveCodepoint == cpKpEnter:
		keyName = "enter"
	case effectiveCodepoint == cpSpace:
		keyName = "space"
	case effectiveCodepoint == cpBackspace:
		keyName = "backspace"
	case effectiveCodepoint == funcDelete:
		keyName = "delete"
	case effectiveCodepoint == funcInsert:
		keyName = "insert"
	case effectiveCodepoint == funcHome:
		keyName = "home"
	case effectiveCodepoint == funcEnd:
		keyName = "end"
	case effectiveCodepoint == funcPageUp:
		keyName = "pageUp"
	case effectiveCodepoint == funcPageDown:
		keyName = "pageDown"
	case effectiveCodepoint == arrowUp:
		keyName = "up"
	case effectiveCodepoint == arrowDown:
		keyName = "down"
	case effectiveCodepoint == arrowLeft:
		keyName = "left"
	case effectiveCodepoint == arrowRight:
		keyName = "right"
	case effectiveCodepoint >= 48 && effectiveCodepoint <= 57:
		keyName = string(rune(effectiveCodepoint))
	case effectiveCodepoint >= 97 && effectiveCodepoint <= 122:
		keyName = string(rune(effectiveCodepoint))
	case symbolKeys[string(rune(effectiveCodepoint))]:
		keyName = string(rune(effectiveCodepoint))
	}

	if keyName == "" {
		return "", false
	}
	return formatKeyNameWithModifiers(keyName, modifier)
}

// ParseKey parses input data and returns the key identifier if recognized.
func ParseKey(data string) (string, bool) {
	if kitty, ok := parseKittySequence(data); ok {
		return formatParsedKey(kitty.codepoint, kitty.modifier, kitty.baseLayoutKey, kitty.hasBaseLayout)
	}

	if modifyOtherKeys, ok := parseModifyOtherKeysSequence(data); ok {
		return formatParsedKey(modifyOtherKeys.codepoint, modifyOtherKeys.modifier, 0, false)
	}

	// Mode-aware legacy sequences. With Kitty active, ambiguous sequences are
	// custom terminal mappings: \x1b\r is shift+enter (Kitty), \n is
	// shift+enter (Ghostty).
	if IsKittyProtocolActive() {
		if data == "\x1b\r" || data == "\n" {
			return "shift+enter", true
		}
	}

	if legacy, ok := legacySequenceKeyIDs[data]; ok {
		return legacy, true
	}

	// Legacy sequences (used when Kitty is not active, or unambiguous).
	switch data {
	case "\x1b":
		return "escape", true
	case "\x1c":
		return "ctrl+\\", true
	case "\x1d":
		return "ctrl+]", true
	case "\x1f":
		return "ctrl+-", true
	case "\x1b\x1b":
		return "ctrl+alt+[", true
	case "\x1b\x1c":
		return "ctrl+alt+\\", true
	case "\x1b\x1d":
		return "ctrl+alt+]", true
	case "\x1b\x1f":
		return "ctrl+alt+-", true
	case "\t":
		return "tab", true
	case "\r":
		return "enter", true
	case "\x00":
		return "ctrl+space", true
	case " ":
		return "space", true
	case "\x7f":
		return "backspace", true
	case "\x08":
		if isWindowsTerminalSession() {
			return "ctrl+backspace", true
		}
		return "backspace", true
	case "\x1b[Z":
		return "shift+tab", true
	case "\x1b\x7f", "\x1b\b":
		return "alt+backspace", true
	}
	if !IsKittyProtocolActive() && data == "\n" {
		return "enter", true
	}
	// SS3 M (numpad enter) is recognized regardless of the protocol state.
	if data == "\x1bOM" {
		return "enter", true
	}
	if !IsKittyProtocolActive() && data == "\x1b\r" {
		return "alt+enter", true
	}
	if !IsKittyProtocolActive() && data == "\x1b " {
		return "alt+space", true
	}
	if !IsKittyProtocolActive() && data == "\x1bB" {
		return "alt+left", true
	}
	if !IsKittyProtocolActive() && data == "\x1bF" {
		return "alt+right", true
	}
	if !IsKittyProtocolActive() && len(data) == 2 && data[0] == '\x1b' {
		code := int(data[1])
		if code >= 1 && code <= 26 {
			return "ctrl+alt+" + string(rune(code+96)), true
		}
		// Legacy alt+letter/digit/symbol (ESC followed by the key).
		key := string(rune(code))
		if (code >= 97 && code <= 122) || (code >= 48 && code <= 57) || symbolKeys[key] {
			return "alt+" + key, true
		}
	}

	// Unambiguous ANSI arrow/navigation sequences.
	switch data {
	case "\x1b[A":
		return "up", true
	case "\x1b[B":
		return "down", true
	case "\x1b[C":
		return "right", true
	case "\x1b[D":
		return "left", true
	case "\x1b[H", "\x1bOH":
		return "home", true
	case "\x1b[F", "\x1bOF":
		return "end", true
	case "\x1b[3~":
		return "delete", true
	case "\x1b[5~":
		return "pageUp", true
	case "\x1b[6~":
		return "pageDown", true
	}

	// Raw Ctrl+letter and printable characters.
	if len(data) == 1 {
		code := int(data[0])
		if code >= 1 && code <= 26 {
			return "ctrl+" + string(rune(code+96)), true
		}
		if code >= 32 && code <= 126 {
			return data, true
		}
	}

	return "", false
}

// ---- Kitty CSI-u printable decoding ----

const kittyPrintableAllowedModifiers = modShift | lockMask

// DecodeKittyPrintable decodes a Kitty CSI-u sequence into a printable
// character, if applicable.
//
// With Kitty flag 1 (disambiguate) terminals send CSI-u sequences for all
// keys, including plain printable characters. Only plain or Shift-modified
// keys are accepted; Ctrl, Alt, and unsupported modifiers are handled by
// keybinding matching instead. The shifted keycode is preferred when Shift is
// held and reported.
func DecodeKittyPrintable(data string) (string, bool) {
	match := kittyCsiURegex.FindStringSubmatch(data)
	if match == nil {
		return "", false
	}

	// CSI-u groups: <codepoint>[:<shifted>[:<base>]];<mod>[:<event>]u
	codepoint := atoiSafe(match[1])

	shiftedKey := 0
	hasShiftedKey := false
	if match[2] != "" {
		shiftedKey = atoiSafe(match[2])
		hasShiftedKey = true
	}
	modValue := 1
	if match[4] != "" {
		modValue = atoiSafe(match[4])
	}
	modifier := modValue - 1

	if modifier&^kittyPrintableAllowedModifiers != 0 {
		return "", false
	}
	if modifier&(modAlt|modCtrl) != 0 {
		return "", false
	}

	effectiveCodepoint := codepoint
	if modifier&modShift != 0 && hasShiftedKey {
		effectiveCodepoint = shiftedKey
	}
	effectiveCodepoint = normalizeKittyFunctionalCodepoint(effectiveCodepoint)
	if effectiveCodepoint < 32 {
		return "", false
	}
	if !isValidCodepoint(effectiveCodepoint) {
		return "", false
	}
	return string(rune(effectiveCodepoint)), true
}

func decodeModifyOtherKeysPrintable(data string) (string, bool) {
	parsed, ok := parseModifyOtherKeysSequence(data)
	if !ok {
		return "", false
	}
	modifier := parsed.modifier & ^lockMask
	if modifier&^modShift != 0 {
		return "", false
	}
	if parsed.codepoint < 32 || !isValidCodepoint(parsed.codepoint) {
		return "", false
	}
	return string(rune(parsed.codepoint)), true
}

// DecodePrintableKey decodes a printable character from Kitty CSI-u or
// modifyOtherKeys input.
func DecodePrintableKey(data string) (string, bool) {
	if printable, ok := DecodeKittyPrintable(data); ok {
		return printable, true
	}
	return decodeModifyOtherKeysPrintable(data)
}

func isValidCodepoint(codepoint int) bool {
	return codepoint >= 0 && codepoint <= 0x10FFFF && !(codepoint >= 0xD800 && codepoint <= 0xDFFF)
}

func containsAny(data string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(data, needle) {
			return true
		}
	}
	return false
}
