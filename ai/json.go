package ai

import (
	"bytes"
	"encoding/json"
)

// MarshalJSON encodes v the way JavaScript's JSON.stringify does: no HTML
// escaping of <, >, and &. encoding/json escapes those by default, which
// would break byte parity with pi's transcripts and wire payloads.
func MarshalJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	// Encode appends a newline; strip it.
	out := buf.Bytes()
	if len(out) > 0 && out[len(out)-1] == '\n' {
		out = out[:len(out)-1]
	}
	return out, nil
}
