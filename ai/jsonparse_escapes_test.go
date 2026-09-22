package ai

import (
	"encoding/json"
	"testing"
)

// TestParseStreamingEscapes pins the partial-JSON string decoder to upstream's
// parseStreamingJson (verified against the installed pi bundle + partial-json):
// complete escapes decode to their characters as soon as they arrive, while an
// invalid or truncated escape stops the string at that point.
func TestParseStreamingEscapes(t *testing.T) {
	cases := []struct {
		name string
		json string
		want map[string]any
	}{
		{"tab", `{"command":"echo\tfoo`, map[string]any{"command": "echo\tfoo"}},
		{"tabs", `{"command":"a\tb\tc`, map[string]any{"command": "a\tb\tc"}},
		{"newline", `{"command":"line1\nline2`, map[string]any{"command": "line1\nline2"}},
		{"backslash", `{"path":"C:\\temp\\x`, map[string]any{"path": `C:\temp\x`}},
		{"quote", `{"command":"quote \"q\" and tab\there`, map[string]any{"command": "quote \"q\" and tab\there"}},
		{"unicode", `{"command":"emoji \u00e9 \ud83d\ude00`, map[string]any{"command": "emoji \u00e9 \U0001F600"}},
		{"trailing backslash escape", `{"command":"trailing backslash\\`, map[string]any{"command": `trailing backslash\`}},
		{"solidus", `{"command":"x\/y`, map[string]any{"command": "x/y"}},
		{"backspace formfeed carriage", `{"command":"a\b\f\rb`, map[string]any{"command": "a\b\f\rb"}},

		// Invalid or truncated escapes stop the string (upstream drops the rest).
		{"invalid escape", `{"command":"bad \q escape`, map[string]any{"command": "bad "}},
		{"invalid unicode", `{"command":"x\uZZZZy`, map[string]any{"command": "x"}},
		{"truncated unicode", `{"command":"x\u12`, map[string]any{"command": "x"}},
		{"lone backslash", `{"command":"x\`, map[string]any{"command": "x"}},

		// Escapes in a later incomplete field still decode.
		{"with timeout", `{"command":"a\tb","timeout":30`, map[string]any{"command": "a\tb", "timeout": float64(30)}},
		{"array value", `{"command":["a\tb"],"x":1`, map[string]any{"command": []any{"a\tb"}, "x": float64(1)}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got map[string]any
			parseStreamingJSONInto(tc.json, &got)
			if !equalJSONMaps(got, tc.want) {
				t.Fatalf("parseStreamingJSONInto(%s) = %#v, want %#v", tc.json, got, tc.want)
			}
			// The renderer path re-encodes the parsed value; it must round-trip.
			var again map[string]any
			parseStreamingJSONInto(string(parseStreamingArgs(json.RawMessage(tc.json))), &again)
			if !equalJSONMaps(again, tc.want) {
				t.Fatalf("parseStreamingArgs(%s) round-trip = %#v, want %#v", tc.json, again, tc.want)
			}
		})
	}
}

func equalJSONMaps(a, b map[string]any) bool {
	left, _ := json.Marshal(a)
	right, _ := json.Marshal(b)
	return string(left) == string(right)
}
