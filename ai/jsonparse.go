package ai

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf16"
)

// Port of utils/json-parse.ts.
//
// D-row D3: upstream's partial fallback uses the npm `partial-json` package.
// The Go port implements the subset of partial-JSON semantics the streaming
// path needs (truncated objects/arrays/strings keep their complete prefix
// fields); verify byte-for-byte against partial-json before relying on
// exotic inputs.

const validJSONEscapes = `"\/bfnrt"`

func isControlCharacter(c byte) bool { return c <= 0x1f }

func escapeControlCharacter(c byte) string {
	switch c {
	case '\b':
		return `\b`
	case '\f':
		return `\f`
	case '\n':
		return `\n`
	case '\r':
		return `\r`
	case '\t':
		return `\t`
	default:
		return fmt.Sprintf(`\u%04x`, c)
	}
}

// RepairJSON repairs malformed JSON string literals by escaping raw control
// characters inside strings and doubling backslashes before invalid escape
// characters (port of repairJson, operating on UTF-16 code units like the
// JS original).
func RepairJSON(jsonText string) string {
	units := utf16.Encode([]rune(jsonText))
	var repaired []uint16
	inString := false

	// JS string indexing is by UTF-16 unit.
	at := func(i int) (uint16, bool) {
		if i < 0 || i >= len(units) {
			return 0, false
		}
		return units[i], true
	}

	for index := 0; index < len(units); index++ {
		char := units[index]

		if !inString {
			repaired = append(repaired, char)
			if char == '"' {
				inString = true
			}
			continue
		}

		if char == '"' {
			repaired = append(repaired, char)
			inString = false
			continue
		}

		if char == '\\' {
			nextChar, ok := at(index + 1)
			if !ok {
				repaired = append(repaired, '\\', '\\')
				continue
			}

			if nextChar == 'u' {
				// Slice of 4 units after \u.
				valid := index+6 <= len(units)
				if valid {
					for _, d := range units[index+2 : index+6] {
						hexOK := (d >= '0' && d <= '9') || (d >= 'a' && d <= 'f') || (d >= 'A' && d <= 'F')
						if !hexOK {
							valid = false
							break
						}
					}
				}
				if valid {
					repaired = append(repaired, units[index:index+6]...)
					index += 5
					continue
				}
			}

			// nextChar is a single code unit for the escape set.
			nc := string(utf16.Decode([]uint16{nextChar}))
			if len(nc) == 1 && strings.ContainsRune(validJSONEscapes, rune(nc[0])) {
				repaired = append(repaired, '\\', nextChar)
				index++
				continue
			}

			repaired = append(repaired, '\\', '\\')
			continue
		}

		if char < 0x20 {
			// Control character: escape it.
			enc, _ := MarshalJSON(string(utf16.Decode([]uint16{char})))
			// enc is a quoted JSON string like "\u0001" or "\b".
			s := string(enc)
			inner := s[1 : len(s)-1]
			for _, r := range inner {
				repaired = append(repaired, uint16(r))
			}
		} else {
			repaired = append(repaired, char)
		}
	}

	return string(utf16.Decode(repaired))
}

// ParseJSONWithRepair parses JSON, retrying once with RepairJSON when the
// repair changes the input (port of parseJsonWithRepair).
func ParseJSONWithRepair(jsonText string, v any) error {
	if err := json.Unmarshal([]byte(jsonText), v); err == nil {
		return nil
	} else {
		repaired := RepairJSON(jsonText)
		if repaired != jsonText {
			return json.Unmarshal([]byte(repaired), v)
		}
		return err
	}
}

// parseStreamingJSONInto attempts to parse potentially incomplete JSON
// during streaming. Always produces a usable value (empty map when parsing
// fails entirely); port of parseStreamingJson.
func parseStreamingJSONInto(partialJSON string, v any) {
	if JSTrim(partialJSON) == "" {
		return
	}
	if err := ParseJSONWithRepair(partialJSON, v); err == nil {
		return
	}
	if result, err := partialParse(partialJSON); err == nil {
		if result != nil {
			mustReencode(result, v)
		}
		return
	}
	if result, err := partialParse(RepairJSON(partialJSON)); err == nil && result != nil {
		mustReencode(result, v)
	}
}

func mustReencode(partial any, v any) {
	enc, err := MarshalJSON(partial)
	if err != nil {
		return
	}
	_ = json.Unmarshal(enc, v)
}

// partialParse parses truncated JSON, keeping complete prefix fields
// (truncated strings/objects/arrays). It mirrors the npm partial-json
// package's default behavior for the streaming cases pi relies on.
func partialParse(text string) (any, error) {
	p := &partialParser{units: utf16.Encode([]rune(text))}
	v, err := p.parseValue()
	if err != nil {
		return nil, err
	}
	// Trailing whitespace only is fine; anything else is left partial.
	p.skipWhitespace()
	return v, nil
}

type partialParser struct {
	units []uint16
	pos   int
}

func (p *partialParser) peek() (uint16, bool) {
	if p.pos >= len(p.units) {
		return 0, false
	}
	return p.units[p.pos], true
}

func (p *partialParser) skipWhitespace() {
	for p.pos < len(p.units) {
		u := p.units[p.pos]
		if u == ' ' || u == '\t' || u == '\n' || u == '\r' {
			p.pos++
		} else {
			return
		}
	}
}

func (p *partialParser) parseValue() (any, error) {
	p.skipWhitespace()
	u, ok := p.peek()
	if !ok {
		return nil, fmt.Errorf("partial json: unexpected end of input")
	}
	switch u {
	case '{':
		return p.parseObject()
	case '[':
		return p.parseArray()
	case '"':
		return p.parsePartialString()
	default:
		return p.parseLiteral()
	}
}

func (p *partialParser) parseObject() (any, error) {
	p.pos++ // '{'
	out := map[string]any{}
	for {
		p.skipWhitespace()
		u, ok := p.peek()
		if !ok {
			return out, nil // truncated after '{' or ','
		}
		if u == '}' {
			p.pos++
			return out, nil
		}
		if u == ',' {
			p.pos++
			continue
		}
		if u != '"' {
			return out, nil // partial — stop with what we have
		}
		key, ok := p.tryParseFullString()
		if !ok {
			return out, nil
		}
		p.skipWhitespace()
		u, ok = p.peek()
		if !ok {
			return out, nil
		}
		if u != ':' {
			return out, nil
		}
		p.pos++
		value, err := p.parseValue()
		if err != nil {
			return out, nil
		}
		out[key] = value
		p.skipWhitespace()
		if u2, ok := p.peek(); ok && u2 == ',' {
			p.pos++
			continue
		}
		if u2, ok := p.peek(); !ok || u2 != '}' {
			return out, nil // truncated value list
		}
	}
}

func (p *partialParser) parseArray() (any, error) {
	p.pos++ // '['
	out := []any{}
	for {
		p.skipWhitespace()
		u, ok := p.peek()
		if !ok {
			return out, nil
		}
		if u == ']' {
			p.pos++
			return out, nil
		}
		if u == ',' {
			p.pos++
			continue
		}
		value, err := p.parseValue()
		if err != nil {
			return out, nil
		}
		out = append(out, value)
		p.skipWhitespace()
		if u2, ok := p.peek(); ok && u2 == ',' {
			p.pos++
			continue
		}
		if u2, ok := p.peek(); !ok || u2 != ']' {
			return out, nil
		}
	}
}

// parsePartialString parses a possibly-truncated JSON string; an
// unterminated string yields the complete prefix (partial-json semantics).
func (p *partialParser) parsePartialString() (string, error) {
	p.pos++ // opening quote
	var out []uint16
	for p.pos < len(p.units) {
		u := p.units[p.pos]
		if u == '"' {
			p.pos++
			return string(utf16.Decode(out)), nil
		}
		if u == '\\' {
			if p.pos+1 < len(p.units) {
				next := p.units[p.pos+1]
				switch next {
				case '"', '\\', '/', 'b', 'f', 'n', 'r', 't':
					out = append(out, u, next)
					p.pos += 2
					continue
				case 'u':
					if p.pos+6 <= len(p.units) {
						out = append(out, p.units[p.pos:p.pos+6]...)
						p.pos += 6
						continue
					}
					// Truncated \u escape: stop with the prefix.
					return string(utf16.Decode(out)), nil
				default:
					// Invalid escape: treat literally (repair upstream handles
					// this before partial parse).
					out = append(out, u)
					p.pos++
					continue
				}
			}
			return string(utf16.Decode(out)), nil
		}
		out = append(out, u)
		p.pos++
	}
	return string(utf16.Decode(out)), nil
}

// tryParseFullString parses a complete string; ok is false when truncated.
func (p *partialParser) tryParseFullString() (string, bool) {
	start := p.pos
	s, err := p.parsePartialString()
	if err != nil {
		return "", false
	}
	_ = start
	// Distinguish complete from truncated: parsePartialString returns on
	// closing quote (pos past it) or end of input. Check the last consumed
	// unit was a quote by re-examining: if pos <= len(units) and the
	// preceding unit is '"' it was complete.
	if p.pos-1 < len(p.units) && p.units[p.pos-1] == '"' {
		return s, true
	}
	return "", false
}

func (p *partialParser) parseLiteral() (any, error) {
	rest := string(utf16.Decode(p.units[p.pos:]))
	for _, lit := range []struct {
		name  string
		value any
	}{
		{"true", true},
		{"false", false},
		{"null", nil},
	} {
		if strings.HasPrefix(rest, lit.name) {
			p.pos += len(lit.name)
			return lit.value, nil
		}
	}
	// Numbers: consume valid numeric characters.
	end := 0
	for end < len(rest) {
		c := rest[end]
		if (c >= '0' && c <= '9') || c == '-' || c == '+' || c == '.' || c == 'e' || c == 'E' {
			end++
		} else {
			break
		}
	}
	if end == 0 {
		return nil, fmt.Errorf("partial json: unexpected character %q", rest[0])
	}
	numText := rest[:end]
	p.pos += end
	var f float64
	if err := json.Unmarshal([]byte(numText), &f); err != nil {
		return nil, err
	}
	return f, nil
}
