// Package tui is the Go port of @earendil-works/pi-tui: the terminal UI
// library with differential rendering behind the interactive coding agent.
//
// This file ports src/utils.ts: terminal-cell width calculation over grapheme
// clusters, and terminal-sequence handling (CSI/OSC/APC).
package tui

import (
	"strings"
	"sync"
	"unicode"

	"golang.org/x/text/width"
)

// Grapheme segmentation: Go has no Intl.Segmenter, so the port implements the
// practical UAX #29 subset the width math needs (base + combining marks, ZWJ
// sequences, variation selectors, skin tones, regional indicators).

// segmentGraphemes splits text into grapheme clusters.
func segmentGraphemes(text string) []string {
	if text == "" {
		return nil
	}
	runes := []rune(text)
	var segments []string
	var cluster []rune
	flush := func() {
		if len(cluster) > 0 {
			segments = append(segments, string(cluster))
			cluster = cluster[:0]
		}
	}
	for index, r := range runes {
		if index == 0 {
			cluster = append(cluster, r)
			continue
		}
		if extendsCluster(cluster[len(cluster)-1], r) {
			cluster = append(cluster, r)
			continue
		}
		flush()
		cluster = append(cluster, r)
	}
	flush()
	return segments
}

// extendsCluster reports whether r continues the grapheme cluster that ends
// with last.
func extendsCluster(last, r rune) bool {
	// Combining marks extend (Mn/Me and the spacing exceptions).
	if isCombiningMark(r) {
		return true
	}
	// Variation selectors extend.
	if r == 0xFE0E || r == 0xFE0F {
		return true
	}
	// A ZWJ joins the next character into the cluster.
	if r == 0x200D {
		return true
	}
	// Skin tone modifiers attach to emoji.
	if isSkinToneModifier(r) {
		return true
	}
	// Regional indicator pairs form flag emoji.
	if isRegionalIndicator(last) && isRegionalIndicator(r) {
		return true
	}
	// A virama joins the consonant cluster (indic scripts).
	if isVirama(last) {
		return true
	}
	// The character after a ZWJ joins the cluster (emoji sequences).
	if last == 0x200D {
		return true
	}
	return false
}

// isVirama covers the indic virama/sign-visarga separatives.
func isVirama(r rune) bool {
	switch r {
	case 0x094D, 0x09CD, 0x0A4D, 0x0ACD, 0x0B4D, 0x0BCD, 0x0C4D, 0x0CCD,
		0x0D4D, 0x0DCA, 0x0E4C, 0x0E4A, 0x0F84, 0x1039, 0x103A, 0x1714, 0x17D1:
		return true
	}
	return false
}

func isCombiningMark(r rune) bool {
	// Mn/Me are combining; Mc spacing marks occupy cells but still extend the
	// cluster for segmentation purposes.
	return isInTable(r, combiningMarkRanges)
}

func isSkinToneModifier(r rune) bool {
	return r >= 0x1F3FB && r <= 0x1F3FF
}

func isRegionalIndicator(r rune) bool {
	return r >= 0x1F1E6 && r <= 0x1F1FF
}

// combiningMarkRanges covers Mn, Me, and the Mc exceptions the width tables
// treat as combining (indic and Hebrew/Arabic marks).
var combiningMarkRanges = [][2]rune{
	{0x0300, 0x036F}, {0x0483, 0x0489}, {0x0591, 0x05BD}, {0x05BF, 0x05BF},
	{0x05C1, 0x05C2}, {0x05C4, 0x05C5}, {0x05C7, 0x05C7}, {0x0610, 0x061A},
	{0x064B, 0x065F}, {0x0670, 0x0670}, {0x06D6, 0x06DC}, {0x06DF, 0x06E4},
	{0x06E7, 0x06E8}, {0x06EA, 0x06ED}, {0x0711, 0x0711}, {0x0730, 0x074A},
	{0x07A6, 0x07B0}, {0x07EB, 0x07F3}, {0x0816, 0x0819}, {0x081B, 0x0823},
	{0x0825, 0x0827}, {0x0829, 0x082D}, {0x0859, 0x085B}, {0x08D3, 0x08E1},
	{0x08E3, 0x0903}, {0x093A, 0x093C}, {0x093E, 0x094F}, {0x0951, 0x0957},
	{0x0962, 0x0963}, {0x0981, 0x0983}, {0x09BC, 0x09BC}, {0x09BE, 0x09C4},
	{0x09C7, 0x09C8}, {0x09CB, 0x09CD}, {0x09D7, 0x09D7}, {0x09E2, 0x09E3},
	{0x0A01, 0x0A03}, {0x0A3C, 0x0A3C}, {0x0A3E, 0x0A42}, {0x0A47, 0x0A48},
	{0x0A4B, 0x0A4D}, {0x0A51, 0x0A51}, {0x0A70, 0x0A71}, {0x0A75, 0x0A75},
	{0x0A81, 0x0A83}, {0x0ABC, 0x0ABC}, {0x0ABE, 0x0AC5}, {0x0AC7, 0x0AC9},
	{0x0ACB, 0x0ACD}, {0x0AE2, 0x0AE3}, {0x0B01, 0x0B03}, {0x0B3C, 0x0B3C},
	{0x0B3E, 0x0B44}, {0x0B47, 0x0B48}, {0x0B4B, 0x0B4D}, {0x0B56, 0x0B57},
	{0x0B62, 0x0B63}, {0x0B82, 0x0B82}, {0x0BBE, 0x0BC2}, {0x0BC6, 0x0BC8},
	{0x0BCA, 0x0BCD}, {0x0BD7, 0x0BD7}, {0x0C00, 0x0C04}, {0x0C3E, 0x0C44},
	{0x0C46, 0x0C48}, {0x0C4A, 0x0C4D}, {0x0C55, 0x0C56}, {0x0C62, 0x0C63},
	{0x0C81, 0x0C83}, {0x0CBC, 0x0CBC}, {0x0CBE, 0x0CC4}, {0x0CC6, 0x0CC8},
	{0x0CCA, 0x0CCD}, {0x0CD5, 0x0CD6}, {0x0CE2, 0x0CE3}, {0x0D00, 0x0D03},
	{0x0D3B, 0x0D3C}, {0x0D3E, 0x0D44}, {0x0D46, 0x0D48}, {0x0D4A, 0x0D4D},
	{0x0D57, 0x0D57}, {0x0D62, 0x0D63}, {0x0D82, 0x0D83}, {0x0DCA, 0x0DCA},
	{0x0DCF, 0x0DD4}, {0x0DD6, 0x0DD6}, {0x0DD8, 0x0DDF}, {0x0DF2, 0x0DF3},
	{0x0E31, 0x0E31}, {0x0E34, 0x0E3A}, {0x0E47, 0x0E4E}, {0x0EB1, 0x0EB1},
	{0x0EB4, 0x0EBC}, {0x0EC8, 0x0ECD}, {0x0F18, 0x0F19}, {0x0F35, 0x0F35},
	{0x0F37, 0x0F37}, {0x0F39, 0x0F39}, {0x0F3E, 0x0F3F}, {0x0F71, 0x0F84},
	{0x0F86, 0x0F87}, {0x0F8D, 0x0F97}, {0x0F99, 0x0FBC}, {0x0FC6, 0x0FC6},
	{0x102B, 0x103E}, {0x1056, 0x1059}, {0x105E, 0x1060}, {0x1062, 0x1064},
	{0x1067, 0x106D}, {0x1071, 0x1074}, {0x1082, 0x108D}, {0x108F, 0x108F},
	{0x109A, 0x109D}, {0x135D, 0x135F}, {0x1712, 0x1714}, {0x1732, 0x1734},
	{0x1752, 0x1753}, {0x1772, 0x1773}, {0x17B4, 0x17D3}, {0x180B, 0x180D},
	{0x1885, 0x1886}, {0x18A9, 0x18A9}, {0x1920, 0x192B}, {0x1930, 0x193B},
	{0x1A17, 0x1A1B}, {0x1A55, 0x1A5E}, {0x1A60, 0x1A7C}, {0x1A7F, 0x1A7F},
	{0x1AB0, 0x1ACE}, {0x1B00, 0x1B04}, {0x1B34, 0x1B44}, {0x1B6B, 0x1B73},
	{0x1B80, 0x1B82}, {0x1BA1, 0x1BAD}, {0x1BE6, 0x1BF3}, {0x1C24, 0x1C37},
	{0x1CD0, 0x1CD2}, {0x1CD4, 0x1CE8}, {0x1CED, 0x1CED}, {0x1CF2, 0x1CF4},
	{0x1CF7, 0x1CF9}, {0x1DC0, 0x1DFF}, {0x200C, 0x200D}, {0x20D0, 0x20F0},
	{0x2CEF, 0x2CF1}, {0x2D7F, 0x2D7F}, {0x2DE0, 0x2DFF}, {0x302A, 0x302F},
	{0x3099, 0x309A}, {0xA66F, 0xA672}, {0xA674, 0xA67D}, {0xA69E, 0xA69F},
	{0xA6F0, 0xA6F1}, {0xA802, 0xA802}, {0xA806, 0xA806}, {0xA80B, 0xA80B},
	{0xA823, 0xA827}, {0xA880, 0xA881}, {0xA8B4, 0xA8C5}, {0xA8E0, 0xA8F1},
	{0xA926, 0xA92D}, {0xA947, 0xA953}, {0xA980, 0xA983}, {0xA9B3, 0xA9C0},
	{0xA9E5, 0xA9E5}, {0xAA29, 0xAA36}, {0xAA43, 0xAA43}, {0xAA4C, 0xAA4D},
	{0xAA7B, 0xAA7D}, {0xAAB0, 0xAAB0}, {0xAAB2, 0xAAB4}, {0xAAB7, 0xAAB8},
	{0xAABE, 0xAABF}, {0xAAC1, 0xAAC1}, {0xAAEB, 0xAAEF}, {0xAAF5, 0xAAF6},
	{0xABE3, 0xABEA}, {0xABEC, 0xABED}, {0xFB1E, 0xFB1E}, {0xFE00, 0xFE0F},
	{0xFE20, 0xFE2F}, {0xFF9E, 0xFF9F},
	{0x101FD, 0x101FD}, {0x102E0, 0x102E0}, {0x10376, 0x1037A},
	{0x10A01, 0x10A03}, {0x10A05, 0x10A06}, {0x10A0C, 0x10A0F},
	{0x10A38, 0x10A3A}, {0x10A3F, 0x10A3F}, {0x10AE5, 0x10AE6},
	{0x11000, 0x11002}, {0x11038, 0x11046}, {0x1107F, 0x11082},
	{0x110B0, 0x110BA}, {0x11100, 0x11102}, {0x11127, 0x11134},
	{0x11173, 0x11173}, {0x11180, 0x11182}, {0x111B3, 0x111C0},
	{0x1122C, 0x11237}, {0x1133C, 0x1133C}, {0x11340, 0x1134D},
	{0x11366, 0x11374}, {0x114B0, 0x114C3}, {0x115AF, 0x115B5},
	{0x115BC, 0x115BD}, {0x115BF, 0x115C0}, {0x115DC, 0x115DD},
	{0x11630, 0x11640}, {0x116AB, 0x116B7}, {0x1171D, 0x1172B},
	{0x1182C, 0x1183A}, {0x119D1, 0x119D7}, {0x119DA, 0x119E0},
	{0x11A01, 0x11A0A}, {0x11A33, 0x11A39}, {0x11A3B, 0x11A3E},
	{0x11A47, 0x11A47}, {0x11A51, 0x11A5B}, {0x11A8A, 0x11A99},
	{0x11C2F, 0x11C36}, {0x11C38, 0x11C3F}, {0x11C92, 0x11CA7},
	{0x11CA9, 0x11CB6}, {0x16AF0, 0x16AF4}, {0x16B30, 0x16B36},
	{0x16F4F, 0x16F4F}, {0x16F51, 0x16F92}, {0x16F93, 0x16F9F},
	{0x1D165, 0x1D169}, {0x1D16D, 0x1D172}, {0x1D17B, 0x1D182},
	{0x1D185, 0x1D18B}, {0x1D1AA, 0x1D1AD}, {0x1D242, 0x1D244},
	{0x1E000, 0x1E006}, {0x1E008, 0x1E018}, {0x1E01B, 0x1E021},
	{0x1E023, 0x1E024}, {0x1E026, 0x1E02A}, {0x1E8D0, 0x1E8D6},
	{0x1E944, 0x1E94A}, {0xE0100, 0xE01EF},
}

// isInTable reports whether r is in an inclusive range table.
func isInTable(r rune, table [][2]rune) bool {
	low, high := 0, len(table)-1
	for low <= high {
		mid := (low + high) / 2
		switch {
		case r < table[mid][0]:
			high = mid - 1
		case r > table[mid][1]:
			low = mid + 1
		default:
			return true
		}
	}
	return false
}

// couldBeEmoji pre-filters segments before the full emoji check.
func couldBeEmoji(segment string) bool {
	runes := []rune(segment)
	first := runes[0]
	return (first >= 0x1F000 && first <= 0x1FBFF) ||
		(first >= 0x2300 && first <= 0x23FF) ||
		(first >= 0x2600 && first <= 0x27BF) ||
		(first >= 0x2B50 && first <= 0x2B55) ||
		strings.ContainsRune(segment, 0xFE0F) ||
		len(runes) > 2
}

// isPrintableASCII reports whether every byte is printable ASCII.
func isPrintableASCII(text string) bool {
	for index := 0; index < len(text); index++ {
		if text[index] < 0x20 || text[index] > 0x7e {
			return false
		}
	}
	return true
}

// graphemeWidth returns the terminal cell width of one grapheme cluster.
func graphemeWidth(segment string) int {
	if segment == "\t" {
		return 3
	}

	runes := []rune(segment)

	// Marks that terminals allocate cells for when attached to a base.
	if len(runes) > 0 && isTerminalSpacingMark(runes[0]) && !isBaseCharacter(runes[0]) {
		return len(runes)
	}

	// Zero-width clusters (ignorable code points, lone controls, marks).
	zeroWidth := true
	for _, r := range runes {
		if !isZeroWidthCodePoint(r) {
			zeroWidth = false
			break
		}
	}
	if zeroWidth {
		return 0
	}

	// Emoji (with the pre-filter) render double width.
	if couldBeEmoji(segment) && isEmojiPresentation(segment) {
		return 2
	}

	// The base visible code point.
	base := stripLeadingNonPrinting(segment)
	baseRunes := []rune(base)
	if len(baseRunes) == 0 {
		return 0
	}
	first := baseRunes[0]

	// Regional indicators render as full-width emoji even isolated.
	if isRegionalIndicator(first) {
		return 2
	}

	width := eastAsianWidth(first)

	// Trailing visible code points terminals may allocate cells for: indic
	// consonants after marks, halfwidth/fullwidth forms, Thai/Lao AM vowels.
	followsMark := false
	for _, char := range baseRunes[1:] {
		switch {
		case isTerminalSpacingMark(char):
			width++
			followsMark = false
		case isCombiningMark(char):
			followsMark = true
		case !isNonPrintingCodePoint(char):
			if followsMark || (char >= 0xFF00 && char <= 0xFFEF) {
				width += eastAsianWidth(char)
			} else if char == 0x0E33 || char == 0x0EB3 {
				width++
			}
			followsMark = false
		}
	}
	return width
}

// eastAsianWidth returns 2 for wide/fullwidth runes, else 1.
func eastAsianWidth(r rune) int {
	kind := width.LookupRune(r).Kind()
	if kind == width.EastAsianWide || kind == width.EastAsianFullwidth {
		return 2
	}
	return 1
}

// isBaseCharacter excludes marks/controls from the terminal-spacing-mark test.
func isBaseCharacter(r rune) bool {
	return isCombiningMark(r) || isNonPrintingCodePoint(r)
}

// isZeroWidthCodePoint covers the upstream zero-width classes: default
// ignorable, control, mark, surrogate.
func isZeroWidthCodePoint(r rune) bool {
	switch {
	case r < 0x20 || (r >= 0x7F && r < 0xA0): // Cc control
		return true
	case r == 0x200B || r == 0x200C || r == 0x200D: // zero-width space/joiners
		return true
	case r >= 0x2060 && r <= 0x2064: // word joiner, invisibles
		return true
	case r == 0xFEFF: // BOM / zero-width no-break space
		return true
	case r >= 0xFE00 && r <= 0xFE0F: // variation selectors
		return true
	case r >= 0xE0100 && r <= 0xE01EF: // variation selectors supplement
		return true
	case r >= 0xD800 && r <= 0xDFFF: // surrogates
		return true
	case r == 0x115F || r == 0x1160: // hangul filler
		return true
	case r >= 0x2065 && r <= 0x206F: // deprecated ignorable
		return true
	case r == 0xFFF9 || r == 0xFFFA || r == 0xFFFB || r == 0xFFFC || r == 0xFFFE || r == 0xFFFF:
		return true
	case r == 0x00AD: // soft hyphen (format)
		return true
	case isCombiningMark(r): // marks (Mn/Me/Mc) are zero-width as isolated clusters
		return true
	}
	return false
}

// isNonPrintingCodePoint covers ignorable/control/format/mark/surrogate.
func isNonPrintingCodePoint(r rune) bool {
	return isZeroWidthCodePoint(r) || r == 0x0E33 || r == 0x0EB3 || isCombiningMark(r)
}

// stripLeadingNonPrinting removes leading ignorable code points.
func stripLeadingNonPrinting(segment string) string {
	runes := []rune(segment)
	index := 0
	for index < len(runes) && (isZeroWidthCodePoint(runes[index]) || isCombiningMark(runes[index])) {
		index++
	}
	return string(runes[index:])
}

// isEmojiPresentation reports emoji presentation: covered by the RGI set in
// upstream; the port checks the presentation selector and the emoji block.
func isEmojiPresentation(segment string) bool {
	runes := []rune(segment)
	// An explicit emoji presentation selector guarantees emoji presentation.
	for _, r := range runes {
		if r == 0xFE0F {
			return true
		}
	}
	// Keycap, regional flags, and ZWJ sequences render as emoji.
	if len(runes) > 1 {
		for _, r := range runes {
			if r == 0x20E3 || isRegionalIndicator(r) || r == 0x200D {
				return true
			}
		}
	}
	// Main emoji presentation ranges.
	first := runes[0]
	switch {
	case first >= 0x1F300 && first <= 0x1F5FF: // misc symbols and pictographs
		return true
	case first >= 0x1F600 && first <= 0x1F64F: // emoticons
		return true
	case first >= 0x1F680 && first <= 0x1F6FF: // transport and map
		return true
	case first >= 0x1F900 && first <= 0x1F9FF: // supplemental symbols
		return true
	case first >= 0x1FA70 && first <= 0x1FAFF: // symbols and pictographs extended-A
		return true
	case first >= 0x1F000 && first <= 0x1F0FF: // mahjong/domino
		return true
	case first == 0x231A || first == 0x231B: // watch/hourglass
		return true
	case first == 0x23E9 || first == 0x23EA || first == 0x23EB || first == 0x23EC:
		return true
	case first == 0x23F0 || first == 0x23F3:
		return true
	case first == 0x25FD || first == 0x25FE:
		return true
	case first == 0x2614 || first == 0x2615:
		return true
	case first == 0x2648 || first == 0x267E || first == 0x267F:
		return true
	case first == 0x26AA || first == 0x26AB:
		return true
	case first >= 0x2690 && first <= 0x26C4 && isKnownEmojiCodePoint(first):
		return true
	case first == 0x2733 || first == 0x2734 || first == 0x2744 || first == 0x2747:
		return true
	case first == 0x2753 || first == 0x2754 || first == 0x2755 || first == 0x2757:
		return true
	case first == 0x2763 || first == 0x2764:
		return true
	case first == 0x2B50 || first == 0x2B55:
		return true
	case first == 0x00A9 || first == 0x00AE: // © ® (with VS16 only upstream)
		return len(runes) > 1 && runes[1] == 0xFE0F
	case first == 0x3030 || first == 0x303D || first == 0x3297 || first == 0x3299:
		return true
	}
	return false
}

// isKnownEmojiCodePoint narrows the 2690-26C4 range to the RGI members.
func isKnownEmojiCodePoint(r rune) bool {
	switch r {
	case 0x2692, 0x2693, 0x2694, 0x2695, 0x2696, 0x2697, 0x2699, 0x269B, 0x269C,
		0x26A0, 0x26A1, 0x26A7, 0x26B0, 0x26B1, 0x26BD, 0x26BE, 0x26C4, 0x26C5,
		0x26AA:
		return true
	}
	return false
}

// isTerminalSpacingMark covers the spacing marks terminals allocate cells for:
// Unicode spacing marks minus the known non-spacing exceptions, plus the
// legacy wcwidth non-spacing exceptions that occupy cells.
func isTerminalSpacingMark(r rune) bool {
	// Exceptions that do NOT occupy cells despite being spacing marks.
	if r == 0x1734 || r == 0x302E || r == 0x302F {
		return false
	}
	// Non-spacing exceptions that DO occupy cells.
	switch r {
	case 0x065F, 0x0F7F, 0x102B, 0x102C, 0x1031, 0x1033, 0x1034, 0x1035,
		0x1038, 0x103A, 0x103B, 0x103C, 0x103D, 0x103E:
		return true
	}
	// Unicode spacing marks (Mc).
	return isInTable(r, spacingMarkRanges)
}

// spacingMarkRanges covers the major Mc (spacing mark) blocks.
var spacingMarkRanges = [][2]rune{
	{0x0903, 0x0903}, {0x093B, 0x093B}, {0x093E, 0x0940}, {0x0949, 0x094C},
	{0x094E, 0x094F}, {0x0982, 0x0983}, {0x09BF, 0x09C0}, {0x09C7, 0x09C8},
	{0x09CB, 0x09CC}, {0x0A03, 0x0A03}, {0x0A3F, 0x0A40}, {0x0ABE, 0x0ABF},
	{0x0B02, 0x0B03}, {0x0B40, 0x0B40}, {0x0B47, 0x0B48}, {0x0B4B, 0x0B4C},
	{0x0BBF, 0x0BBF}, {0x0BC1, 0x0BC2}, {0x0BC6, 0x0BC8}, {0x0BCA, 0x0BCC},
	{0x0C01, 0x0C03}, {0x0C41, 0x0C44}, {0x0C82, 0x0C83}, {0x0CBE, 0x0CBE},
	{0x0CC0, 0x0CC4}, {0x0CC7, 0x0CC8}, {0x0CCA, 0x0CCB}, {0x0D02, 0x0D03},
	{0x0D3F, 0x0D40}, {0x0D46, 0x0D48}, {0x0D4A, 0x0D4C}, {0x0D82, 0x0D83},
	{0x0DDF, 0x0DDF}, {0x0E33, 0x0E33}, {0x0EB3, 0x0EB3}, {0x0F3E, 0x0F3F},
	{0x0F7F, 0x0F7F}, {0x102B, 0x102C}, {0x1031, 0x1031}, {0x1038, 0x1038},
	{0x103B, 0x103C}, {0x1056, 0x1057}, {0x1062, 0x1064}, {0x1067, 0x106D},
	{0x1083, 0x1084}, {0x1087, 0x108C}, {0x108F, 0x108F}, {0x109A, 0x109C},
	{0x17B6, 0x17B6}, {0x17BE, 0x17C5}, {0x17C7, 0x17C8}, {0x1923, 0x1926},
	{0x1929, 0x192B}, {0x1930, 0x1931}, {0x1933, 0x1938}, {0x1A19, 0x1A1A},
	{0x1A55, 0x1A55}, {0x1A57, 0x1A57}, {0x1A6D, 0x1A72}, {0x1B04, 0x1B04},
	{0x1B35, 0x1B35}, {0x1B3B, 0x1B3B}, {0x1B3D, 0x1B41}, {0x1B43, 0x1B44},
	{0x1B82, 0x1B82}, {0x1BA1, 0x1BA1}, {0x1BA6, 0x1BA7}, {0x1BAA, 0x1BAA},
	{0x1BE7, 0x1BE7}, {0x1BEA, 0x1BEC}, {0x1BEE, 0x1BEE}, {0x1BF2, 0x1BF3},
	{0x1C24, 0x1C2B}, {0x1C34, 0x1C35}, {0x1CD0, 0x1CD2}, {0x1CD4, 0x1CE0},
	{0x1CF4, 0x1CF4}, {0x1CF8, 0x1CF9}, {0x20DD, 0x20E0}, {0x20E2, 0x20E4},
	{0x20E7, 0x20E7}, {0x20E9, 0x20E9}, {0x25CC, 0x25CC}, {0xA830, 0xA830},
	{0xFB1E, 0xFB1E},
}

// widthCache caches widths for non-ASCII strings.
const widthCacheSize = 512

var (
	widthCacheMu sync.Mutex
	widthCache   = map[string]int{}
)

// VisibleWidth returns the width of a string in terminal columns: tabs count
// three, terminal sequences are invisible, grapheme clusters use their
// terminal presentation width.
func VisibleWidth(text string) int {
	if text == "" {
		return 0
	}
	if isPrintableASCII(text) {
		return len(text)
	}

	widthCacheMu.Lock()
	cached, ok := widthCache[text]
	widthCacheMu.Unlock()
	if ok {
		return cached
	}

	clean := text
	if strings.Contains(clean, "\t") {
		clean = strings.ReplaceAll(clean, "\t", "   ")
	}
	if strings.Contains(clean, "\x1b") {
		clean = StripTerminalSequences(clean)
	}
	total := 0
	for _, segment := range segmentGraphemes(clean) {
		total += graphemeWidth(segment)
	}

	widthCacheMu.Lock()
	if len(widthCache) >= widthCacheSize {
		// Evict an arbitrary entry (upstream evicts the insertion-oldest).
		for key := range widthCache {
			delete(widthCache, key)
			break
		}
	}
	widthCache[text] = total
	widthCacheMu.Unlock()
	return total
}

// StripTerminalSequences removes ANSI, OSC, and APC control sequences while
// preserving visible text.
func StripTerminalSequences(text string) string {
	if !strings.Contains(text, "\x1b") {
		return text
	}
	var result strings.Builder
	index := 0
	for index < len(text) {
		if code, length := ExtractANSICode(text, index); code != "" {
			index += length
			continue
		}
		result.WriteByte(text[index])
		index++
	}
	return result.String()
}

// ExtractANSICode returns the terminal escape sequence at pos, if any:
// CSI styling/cursor codes, OSC hyperlinks and prompts, APC markers.
// The length covers the whole sequence.
func ExtractANSICode(text string, pos int) (string, int) {
	if pos >= len(text) || text[pos] != '\x1b' {
		return "", 0
	}
	if pos+1 >= len(text) {
		return "", 0
	}
	switch text[pos+1] {
	case '[': // CSI: ESC [ ... final byte in m G K H J
		j := pos + 2
		for j < len(text) {
			c := text[j]
			if c == 'm' || c == 'G' || c == 'K' || c == 'H' || c == 'J' {
				return text[pos : j+1], j + 1 - pos
			}
			j++
		}
		return "", 0
	case ']', '_': // OSC and APC: terminated by BEL or ESC \
		j := pos + 2
		for j < len(text) {
			if text[j] == '\x07' {
				return text[pos : j+1], j + 1 - pos
			}
			if text[j] == '\x1b' && j+1 < len(text) && text[j+1] == '\\' {
				return text[pos : j+2], j + 2 - pos
			}
			j++
		}
		return "", 0
	}
	return "", 0
}

// ---- character classification helpers (src/utils.ts) ----

// isWhitespaceRune mirrors JS /\s/ for the practical set.
func isWhitespaceRune(r rune) bool {
	switch r {
	case ' ', '\t', '\n', '\v', '\f', '\r', 0x00A0, 0x1680, 0x2028, 0x2029, 0x202F, 0x205F, 0x3000, 0xFEFF:
		return true
	}
	return r >= 0x2000 && r <= 0x200A
}

// IsWhitespaceChar reports whether a string is entirely whitespace.
func IsWhitespaceChar(char string) bool {
	for _, r := range char {
		if !isWhitespaceRune(r) {
			return false
		}
	}
	return len(char) > 0
}

// IsPunctuationChar reports whether a character is ASCII punctuation.
func IsPunctuationChar(char string) bool {
	for _, r := range char {
		return punctuationChars[r]
	}
	return false
}

func isLetterRune(r rune) bool { return unicode.IsLetter(r) }

func isDigitRune(r rune) bool { return unicode.IsDigit(r) }

func isMarkRune(r rune) bool { return unicode.IsMark(r) }
