package delta

import (
	"strings"

	"github.com/dat267/pier/chord"
)

// Port of the diff engine half of chord/delta: minimal op emission between
// two JSON values.

// missingValue marks an absent member in the diff walk.
type missingValue struct{}

var missing = missingValue{}

func isMissing(value any) bool {
	_, ok := value.(missingValue)
	return ok
}

// jsonEqual is a deep equality over JSON values.
func jsonEqual(left, right chord.JsonValue) bool {
	if isMissing(left) || isMissing(right) {
		return isMissing(left) && isMissing(right)
	}
	switch leftTyped := left.(type) {
	case nil:
		return right == nil
	case bool:
		rightTyped, ok := right.(bool)
		return ok && leftTyped == rightTyped
	case string:
		rightTyped, ok := right.(string)
		return ok && leftTyped == rightTyped
	case int:
		return numbersEqual(float64(leftTyped), right)
	case int64:
		return numbersEqual(float64(leftTyped), right)
	case float64:
		return numbersEqual(leftTyped, right)
	case []any:
		rightTyped, ok := right.([]any)
		if !ok || len(leftTyped) != len(rightTyped) {
			return false
		}
		for i := range leftTyped {
			if !jsonEqual(leftTyped[i], rightTyped[i]) {
				return false
			}
		}
		return true
	case map[string]any:
		rightTyped, ok := right.(map[string]any)
		if !ok || len(leftTyped) != len(rightTyped) {
			return false
		}
		for key, value := range leftTyped {
			other, exists := rightTyped[key]
			if !exists || !jsonEqual(value, other) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

func numbersEqual(left float64, right chord.JsonValue) bool {
	switch typed := right.(type) {
	case int:
		return left == float64(typed)
	case int64:
		return left == float64(typed)
	case float64:
		return left == typed
	default:
		return false
	}
}

func emitSet(path Path, value chord.JsonValue, out *[]Op) {
	snapshot := chord.CloneJSON(value)
	if len(path) == 0 {
		*out = append(*out, Op{Verb: VerbReplace, Value: snapshot})
		return
	}
	*out = append(*out, Op{Verb: VerbSet, Path: clonePath(path), Value: snapshot})
}

func emitDelete(path Path, out *[]Op) error {
	if len(path) == 0 {
		return &UnsafePathError{Message: "the tracked root cannot be deleted"}
	}
	*out = append(*out, Op{Verb: VerbDelete, Path: clonePath(path)})
	return nil
}

func clonePath(path Path) Path {
	out := make(Path, len(path))
	copy(out, path)
	return out
}

// diffString emits a set, a tail append, or a truncate+append pair.
//
// NOT `strings.HasPrefix(after, before)`: upstream avoids the char-by-char
// cons-string walk; Go strings are flat, but the append and shared-suffix
// paths still produce the compact ops.
func diffString(before, after string, path Path, scan int, out *[]Op) {
	if before == after {
		return
	}
	if len(path) == 0 {
		emitSet(path, after, out)
		return
	}
	at := clonePath(path)
	if len(after) > len(before) && after[:len(before)] == before {
		*out = append(*out, Op{Verb: VerbAppend, Path: at, Text: after[len(before):]})
		return
	}
	shared := Overlap(before, after, scan, 0, 0)
	if shared == 0 {
		*out = append(*out, Op{Verb: VerbSet, Path: at, Value: after})
		return
	}
	*out = append(*out, Op{Verb: VerbTruncate, Path: clonePath(at), Count: len(before) - shared})
	if len(after) > shared {
		*out = append(*out, Op{Verb: VerbAppend, Path: clonePath(at), Text: after[len(after)-len(after)+shared:]})
	}
}

// diffValue emits the ops transforming before into after at path.
func diffValue(before, after chord.JsonValue, path Path, scan int, out *[]Op) error {
	if isMissing(before) {
		if !isMissing(after) {
			emitSet(path, after, out)
		}
		return nil
	}
	if isMissing(after) {
		return emitDelete(path, out)
	}
	if jsonEqual(before, after) {
		return nil
	}
	if beforeString, ok := before.(string); ok {
		if afterString, ok := after.(string); ok {
			diffString(beforeString, afterString, path, scan, out)
			return nil
		}
	}
	beforeArray, beforeIsArray := before.([]any)
	afterArray, afterIsArray := after.([]any)
	if beforeIsArray && afterIsArray {
		return diffArray(beforeArray, afterArray, path, scan, out)
	}
	beforeObject, beforeIsObject := before.(map[string]any)
	afterObject, afterIsObject := after.(map[string]any)
	if beforeIsObject && afterIsObject {
		return diffObject(beforeObject, afterObject, path, scan, out)
	}
	emitSet(path, after, out)
	return nil
}

func diffObject(before, after map[string]any, path Path, scan int, out *[]Op) error {
	for key := range before {
		if ReservedSegments[key] {
			emitSet(path, after, out)
			return nil
		}
	}
	for key := range after {
		if ReservedSegments[key] {
			emitSet(path, after, out)
			return nil
		}
	}
	// Deterministic key order: upstream uses JS insertion order; Go maps are
	// unordered, so keys sort (D12: op ORDER within a batch can differ from
	// upstream for multi-key objects; op SEMANTICS are identical).
	keys := sortedKeys(after)
	for _, key := range keys {
		beforeValue, exists := before[key]
		if !exists {
			beforeValue = missing
		}
		if err := diffValue(beforeValue, after[key], append(clonePath(path), key), scan, out); err != nil {
			return err
		}
	}
	for _, key := range sortedKeys(before) {
		if _, exists := after[key]; !exists {
			if err := emitDelete(append(clonePath(path), key), out); err != nil {
				return err
			}
		}
	}
	return nil
}

func diffArray(before, after []any, path Path, scan int, out *[]Op) error {
	if len(before) == len(after) {
		for index := range after {
			if err := diffValue(before[index], after[index], append(clonePath(path), index), scan, out); err != nil {
				return err
			}
		}
		return nil
	}

	prefix := 0
	for prefix < len(before) && prefix < len(after) && jsonEqual(before[prefix], after[prefix]) {
		prefix++
	}
	suffix := 0
	for suffix < len(before)-prefix && suffix < len(after)-prefix &&
		jsonEqual(before[len(before)-1-suffix], after[len(after)-1-suffix]) {
		suffix++
	}
	shorter := min(len(before), len(after))
	if prefix+suffix == shorter {
		remove := len(before) - prefix - suffix
		items := cloneItems(after[prefix : len(after)-suffix])
		if prefix == 0 && remove == len(before) {
			emitSet(path, after, out)
			return nil
		}
		*out = append(*out, Op{Verb: VerbSplice, Path: clonePath(path), Index: prefix, Remove: remove, Items: items})
		return nil
	}

	// Structural movement combined with retained-index edits has no unique
	// alignment; preserve the retained index deltas and express only the tail
	// length change structurally.
	for index := 0; index < shorter; index++ {
		if err := diffValue(before[index], after[index], append(clonePath(path), index), scan, out); err != nil {
			return err
		}
	}
	switch {
	case len(after) > len(before):
		*out = append(*out, Op{Verb: VerbSplice, Path: clonePath(path), Index: len(before),
			Items: cloneItems(after[len(before):])})
	case len(before) > len(after):
		if len(after) == 0 {
			emitSet(path, after, out)
			return nil
		}
		*out = append(*out, Op{Verb: VerbSplice, Path: clonePath(path), Index: len(after),
			Remove: len(before) - len(after), Items: []chord.JsonValue{}})
	}
	return nil
}

func cloneItems(items []any) []chord.JsonValue {
	out := make([]chord.JsonValue, len(items))
	for i, item := range items {
		out[i] = chord.CloneJSON(item)
	}
	return out
}

func sortedKeys(value map[string]any) []string {
	keys := make([]string, 0, len(value))
	for key := range value {
		keys = append(keys, key)
	}
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && strings.Compare(keys[j], keys[j-1]) < 0; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}

// Diff computes the ops transforming before into after (the diff engine the
// tracker uses; upstream exposes it only through the Proxy).
func Diff(before, after chord.JsonValue, options *TrackerOptions) ([]Op, error) {
	scan := DefaultMaxOverlapScan
	if options != nil && options.MaxOverlapScan != 0 {
		scan = options.MaxOverlapScan
	}
	var out []Op
	if err := diffValue(before, after, Path{}, scan, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// TrackerOptions mirrors upstream TrackerOptions.
type TrackerOptions struct {
	MaxOverlapScan int
}

// DefaultMaxOverlapScan is upstream's 65536-byte cap.
const DefaultMaxOverlapScan = 65_536
