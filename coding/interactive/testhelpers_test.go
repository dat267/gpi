package interactive

import (
	"encoding/json"
	"strings"
	"testing"
)

// Test helpers shared by the theme tests (the same JSON normalization used by
// the tui golden tests).

func mustJSON(value any) string {
	var builder strings.Builder
	encoder := json.NewEncoder(&builder)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		panic(err)
	}
	return strings.TrimSuffix(builder.String(), "\n")
}

func stableMDValue(value any) string {
	switch typed := value.(type) {
	case nil:
		return "null"
	case string:
		return mustJSON(typed)
	case bool:
		if typed {
			return "true"
		}
		return "false"
	case float64:
		encoded, _ := json.Marshal(typed)
		return string(encoded)
	case []any:
		parts := make([]string, 0, len(typed))
		for _, item := range typed {
			parts = append(parts, stableMDValue(item))
		}
		return "[" + strings.Join(parts, ",") + "]"
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sortStrings(keys)
		parts := make([]string, 0, len(keys))
		for _, key := range keys {
			parts = append(parts, mustJSON(key)+":"+stableMDValue(typed[key]))
		}
		return "{" + strings.Join(parts, ",") + "}"
	default:
		encoded, err := json.Marshal(typed)
		if err != nil {
			panic(err)
		}
		return string(encoded)
	}
}

func stableJSONValue(t *testing.T, value any) string {
	t.Helper()
	var encoded string
	if text, ok := value.(string); ok {
		encoded = text
	} else {
		encoded = mustJSON(value)
	}
	var parsed any
	if err := json.Unmarshal([]byte(encoded), &parsed); err != nil {
		t.Fatalf("bad JSON %q: %v", encoded, err)
	}
	return stableMDValue(parsed)
}

func sortStrings(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}

func assertPanics(t *testing.T, label string, fn func()) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Fatalf("%s must panic", label)
		}
	}()
	fn()
}

func containsString(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

func mustParseJSON(t *testing.T, text string) any {
	t.Helper()
	var parsed any
	if err := json.Unmarshal([]byte(text), &parsed); err != nil {
		t.Fatalf("bad JSON %q: %v", text, err)
	}
	return parsed
}
