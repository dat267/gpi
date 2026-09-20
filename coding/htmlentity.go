package coding

import (
	"strconv"
	"strings"
)

// Port of utils/html.ts: HTML entity decoding for the ANSI-to-HTML exporter.

// HtmlDecodedEntity is one decoded entity.
type HtmlDecodedEntity struct {
	Text   string
	Length int
}

// DecodeHtmlEntity decodes a named or numeric entity body (without & and ;).
// ok is false for unknown or out-of-range entities.
func DecodeHtmlEntity(entity string) (string, bool) {
	switch entity {
	case "amp":
		return "&", true
	case "lt":
		return "<", true
	case "gt":
		return ">", true
	case "quot":
		return "\"", true
	case "apos":
		return "'", true
	}

	if strings.HasPrefix(entity, "#x") || strings.HasPrefix(entity, "#X") {
		return decodeHtmlCodePoint(entity[2:], 16)
	}
	if strings.HasPrefix(entity, "#") {
		return decodeHtmlCodePoint(entity[1:], 10)
	}
	return "", false
}

func decodeHtmlCodePoint(digits string, base int) (string, bool) {
	codePoint, err := strconv.ParseInt(digits, base, 64)
	if err != nil || codePoint < 0 || codePoint > 0x10FFFF {
		return "", false
	}
	return string(rune(codePoint)), true
}

// DecodeHtmlEntityAt decodes the entity that starts at index in html (the
// caller has seen the '&'). ok is false when no well-formed entity ends within
// sixteen characters.
func DecodeHtmlEntityAt(html string, index int) (HtmlDecodedEntity, bool) {
	if index < 0 || index >= len(html) || html[index] != '&' {
		return HtmlDecodedEntity{}, false
	}
	rest := html[index+1:]
	semicolon := strings.Index(rest, ";")
	if semicolon == -1 || semicolon > 16 {
		return HtmlDecodedEntity{}, false
	}
	text, ok := DecodeHtmlEntity(rest[:semicolon])
	if !ok {
		return HtmlDecodedEntity{}, false
	}
	return HtmlDecodedEntity{Text: text, Length: semicolon + 2}, true
}
