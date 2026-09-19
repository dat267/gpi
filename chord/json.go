// Package chord is a partial Go port of @earendil-works/chord
// (pi/packages/chord). This round ports the JSON value contract and
// chord/delta; the service layer, bundler, facets, and durable runtime are
// queued.
//
// Ground truth: pi/packages/chord/src at the pinned upstream commit.
package chord

import (
	"math"
	"reflect"
)

// JsonValue is a strict JSON value: nil, bool, string, finite float64/int64,
// []any, or map[string]any.
type JsonValue = any

// maxJsonDepth is upstream's recursion cap in isJsonValue.
const maxJsonDepth = 512

// IsJSONValue reports whether a value is finite strict JSON with plain
// objects (map[string]any with string keys) and no cycles.
func IsJSONValue(value any) bool {
	return checkJSONValue(value, map[cycleIdentity]bool{}, 0)
}

// cycleIdentity identifies a container by identity for cycle detection
// (upstream's Set<object>).
type cycleIdentity struct {
	kind reflect.Kind
	ptr  uintptr
}

func identityOf(value any) (cycleIdentity, bool) {
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Slice:
		if reflected.Len() == 0 {
			return cycleIdentity{}, false
		}
		return cycleIdentity{kind: reflect.Slice, ptr: reflected.Pointer()}, true
	case reflect.Map:
		return cycleIdentity{kind: reflect.Map, ptr: uintptr(reflected.UnsafePointer())}, true
	default:
		return cycleIdentity{}, false
	}
}

func checkJSONValue(value any, ancestors map[cycleIdentity]bool, depth int) bool {
	if depth > maxJsonDepth {
		return false
	}
	switch typed := value.(type) {
	case nil, bool, string:
		return true
	case int64, int, int32:
		return true
	case float64:
		return !math.IsNaN(typed) && !math.IsInf(typed, 0)
	case float32:
		value64 := float64(typed)
		return !math.IsNaN(value64) && !math.IsInf(value64, 0)
	case []any:
		identity, hasIdentity := identityOf(typed)
		if hasIdentity && ancestors[identity] {
			return false
		}
		if hasIdentity {
			ancestors[identity] = true
			defer delete(ancestors, identity)
		}
		for _, item := range typed {
			if !checkJSONValue(item, ancestors, depth+1) {
				return false
			}
		}
		return true
	case map[string]any:
		identity, hasIdentity := identityOf(typed)
		if hasIdentity && ancestors[identity] {
			return false
		}
		if hasIdentity {
			ancestors[identity] = true
			defer delete(ancestors, identity)
		}
		for _, item := range typed {
			if !checkJSONValue(item, ancestors, depth+1) {
				return false
			}
		}
		return true
	default:
		// Non-JSON Go values (structs, byte slices, channels, ...) are rejected,
		// matching upstream's plain-object/test-array checks.
		return false
	}
}

// CloneJSON deep-copies a JSON value (upstream cloneJson). Nil-safe.
func CloneJSON(value JsonValue) JsonValue {
	switch typed := value.(type) {
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = CloneJSON(item)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			out[key] = CloneJSON(item)
		}
		return out
	default:
		return value
	}
}
