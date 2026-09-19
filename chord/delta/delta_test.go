package delta

import (
	"strings"
	"testing"
)

// delta tests keyed to upstream (chord/src/delta/index.ts): diff minimality,
// apply semantics, validation, and the wire codec.

func mustDiff(t *testing.T, before, after any) []Op {
	t.Helper()
	ops, err := Diff(before, after, nil)
	if err != nil {
		t.Fatal(err)
	}
	return ops
}

func TestDiffSetAndDelete(t *testing.T) {
	// A single member change emits a set at that path.
	ops := mustDiff(t, map[string]any{"a": float64(1), "b": float64(2)}, map[string]any{"a": float64(1), "b": float64(3)})
	if len(ops) != 1 || ops[0].Verb != VerbSet || len(ops[0].Path) != 1 || ops[0].Path[0] != "b" {
		t.Fatalf("ops = %+v", ops)
	}
	// A removed member emits a delete.
	ops = mustDiff(t, map[string]any{"a": float64(1), "b": float64(2)}, map[string]any{"a": float64(1)})
	if len(ops) != 1 || ops[0].Verb != VerbDelete || ops[0].Path[0] != "b" {
		t.Fatalf("ops = %+v", ops)
	}
	// A root replacement uses `r`.
	ops = mustDiff(t, map[string]any{"a": float64(1)}, map[string]any{"b": float64(2)})
	// (a→deleted b→added: a set and a delete at the object level, not a root r)
	if len(ops) != 2 {
		t.Fatalf("ops = %+v", ops)
	}
	// Root type change emits r.
	ops = mustDiff(t, "text", map[string]any{"a": float64(1)})
	if len(ops) != 1 || ops[0].Verb != VerbReplace {
		t.Fatalf("ops = %+v", ops)
	}
}

func TestDiffStringOps(t *testing.T) {
	// Appending emits `a` with the delta text.
	ops := mustDiff(t, map[string]any{"s": "hello"}, map[string]any{"s": "hello world"})
	if len(ops) != 1 || ops[0].Verb != VerbAppend || ops[0].Text != " world" {
		t.Fatalf("ops = %+v", ops)
	}
	// A middle insertion shares neither prefix nor suffix, so it emits a set
	// (upstream diffString only compacts appends and shared suffix/prefix runs).
	ops = mustDiff(t, map[string]any{"s": "abcdef"}, map[string]any{"s": "abcXYZdef"})
	if len(ops) != 1 || ops[0].Verb != VerbSet {
		t.Fatalf("ops = %+v", ops)
	}
	// A shared suffix that is also the new value's prefix emits truncate + append.
	ops = mustDiff(t, map[string]any{"s": "hello world"}, map[string]any{"s": "world peace"})
	if len(ops) != 2 || ops[0].Verb != VerbTruncate || ops[0].Count != 6 ||
		ops[1].Verb != VerbAppend || ops[1].Text != " peace" {
		t.Fatalf("ops = %+v", ops)
	}
	// A pure suffix drop emits truncate only.
	ops = mustDiff(t, map[string]any{"s": "abcdef"}, map[string]any{"s": "def"})
	if len(ops) != 1 || ops[0].Verb != VerbTruncate || ops[0].Count != 3 {
		t.Fatalf("ops = %+v", ops)
	}
	// An unrelated change emits a set.
	ops = mustDiff(t, map[string]any{"s": "aaaa"}, map[string]any{"s": "bbbb"})
	if len(ops) != 1 || ops[0].Verb != VerbSet || ops[0].Value != "bbbb" {
		t.Fatalf("ops = %+v", ops)
	}
}

func TestDiffArrayOps(t *testing.T) {
	// Append emits a splice with the appended items.
	ops := mustDiff(t, []any{float64(1)}, []any{float64(1), float64(2)})
	if len(ops) != 1 || ops[0].Verb != VerbSplice || ops[0].Index != 1 || ops[0].Remove != 0 || len(ops[0].Items) != 1 {
		t.Fatalf("ops = %+v", ops)
	}
	// Prepend emits a splice at index 0.
	ops = mustDiff(t, []any{float64(2)}, []any{float64(1), float64(2)})
	if len(ops) != 1 || ops[0].Verb != VerbSplice || ops[0].Index != 0 || len(ops[0].Items) != 1 {
		t.Fatalf("ops = %+v", ops)
	}
	// Removal emits a splice with remove count.
	ops = mustDiff(t, []any{float64(1), float64(2), float64(3)}, []any{float64(1), float64(3)})
	if len(ops) != 1 || ops[0].Verb != VerbSplice || ops[0].Index != 1 || ops[0].Remove != 1 {
		t.Fatalf("ops = %+v", ops)
	}
	// Emptied array emits r (a root-level replace for the array path).
	ops = mustDiff(t, []any{float64(1)}, []any{})
	if len(ops) != 1 || ops[0].Verb != VerbReplace {
		t.Fatalf("ops = %+v", ops)
	}
	// Same length, element change emits a nested set.
	ops = mustDiff(t, []any{float64(1), float64(2)}, []any{float64(1), float64(9)})
	if len(ops) != 1 || ops[0].Verb != VerbSet || ops[0].Path[0] != 1 {
		t.Fatalf("ops = %+v", ops)
	}
}

func TestDiffReservedSegmentFallsBackToSet(t *testing.T) {
	// Reserved keys force a whole-value replacement rather than a nested path
	// (the object sits at the root here, so the op is `r`).
	ops := mustDiff(t, map[string]any{"safe": float64(1)}, map[string]any{"__proto__": float64(2)})
	if len(ops) != 1 || ops[0].Verb != VerbReplace {
		t.Fatalf("ops = %+v", ops)
	}
	// Nested under a key, the same guard emits a set at that key.
	ops = mustDiff(t, map[string]any{"o": map[string]any{"safe": float64(1)}},
		map[string]any{"o": map[string]any{"__proto__": float64(2)}})
	if len(ops) != 1 || ops[0].Verb != VerbSet || ops[0].Path[0] != "o" {
		t.Fatalf("ops = %+v", ops)
	}
}

func TestApplyRoundTrip(t *testing.T) {
	cases := []struct {
		name          string
		before, after any
	}{
		{"object member", map[string]any{"a": float64(1), "b": float64(2)}, map[string]any{"a": float64(1), "b": float64(3)}},
		{"delete member", map[string]any{"a": float64(1), "b": float64(2)}, map[string]any{"a": float64(1)}},
		{"string append", map[string]any{"s": "hi"}, map[string]any{"s": "hi there"}},
		{"string middle", map[string]any{"s": "abcdef"}, map[string]any{"s": "abcXYZdef"}},
		{"array append", []any{float64(1)}, []any{float64(1), float64(2), float64(3)}},
		{"array prepend", []any{float64(2)}, []any{float64(1), float64(2)}},
		{"array remove", []any{float64(1), float64(2), float64(3)}, []any{float64(3)}},
		{"nested", map[string]any{"o": map[string]any{"n": []any{float64(1)}}}, map[string]any{"o": map[string]any{"n": []any{float64(1), float64(2)}}}},
		{"root replace", "text", map[string]any{"a": float64(1)}},
		{"array emptied", []any{float64(1)}, []any{}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			ops := mustDiff(t, testCase.before, testCase.after)
			applied, err := Apply(cloneForTest(testCase.before), ops)
			if err != nil {
				t.Fatal(err)
			}
			if !jsonEqual(applied, testCase.after) {
				t.Fatalf("applied = %#v; want %#v (ops %+v)", applied, testCase.after, ops)
			}
			// Immutable application leaves the input untouched and matches.
			frozen := cloneForTest(testCase.before)
			snapshot := cloneForTest(testCase.before)
			immutable, err := ApplyImmutable(frozen, ops)
			if err != nil {
				t.Fatal(err)
			}
			if !jsonEqual(immutable, testCase.after) {
				t.Fatalf("immutable = %#v; want %#v", immutable, testCase.after)
			}
			if !jsonEqual(frozen, snapshot) {
				t.Fatalf("ApplyImmutable mutated its input: %#v -> %#v", snapshot, frozen)
			}
		})
	}
}

func cloneForTest(value any) any {
	switch typed := value.(type) {
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = cloneForTest(item)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			out[key] = cloneForTest(item)
		}
		return out
	default:
		return value
	}
}

func TestApplyValidation(t *testing.T) {
	// A set path that does not resolve fails.
	if _, err := Apply(map[string]any{}, []Op{{Verb: VerbSet, Path: Path{"missing", "deep"}, Value: float64(1)}}); err == nil {
		t.Fatal("unresolvable path must fail")
	}
	// A reserved segment is unsafe.
	if _, err := Apply(map[string]any{}, []Op{{Verb: VerbSet, Path: Path{"__proto__"}, Value: float64(1)}}); err == nil {
		t.Fatal("reserved segment must fail")
	}
	// An index past the end (beyond append-one) is rejected.
	if _, err := Apply([]any{}, []Op{{Verb: VerbSet, Path: Path{5}, Value: float64(1)}}); err == nil {
		t.Fatal("out-of-range index must fail")
	}
	// Append-one past the end is legal.
	if _, err := Apply([]any{float64(1)}, []Op{{Verb: VerbSet, Path: Path{1}, Value: float64(2)}}); err != nil {
		t.Fatalf("append-one failed: %v", err)
	}
	// Root delete is impossible by type, but an empty delete path is invalid.
	if err := AssertValidOp(Op{Verb: VerbDelete, Path: Path{}}); err == nil {
		t.Fatal("empty path delete must be invalid")
	}
	// Unknown verb.
	if err := AssertValidOp(Op{Verb: "z"}); err == nil {
		t.Fatal("unknown verb must be invalid")
	}
}

func TestAppendAndTruncateSemantics(t *testing.T) {
	// `a` appends to a string; `t` drops the last N bytes.
	applied, err := Apply(map[string]any{"s": "abc"}, []Op{{Verb: VerbAppend, Path: Path{"s"}, Text: "def"}})
	if err != nil || applied.(map[string]any)["s"] != "abcdef" {
		t.Fatalf("append = %v, %v", applied, err)
	}
	applied, err = Apply(map[string]any{"s": "abcdef"}, []Op{{Verb: VerbTruncate, Path: Path{"s"}, Count: 2}})
	if err != nil || applied.(map[string]any)["s"] != "abcd" {
		t.Fatalf("truncate = %v, %v", applied, err)
	}
	// Appending to a non-string fails.
	if _, err := Apply(map[string]any{"s": float64(1)}, []Op{{Verb: VerbAppend, Path: Path{"s"}, Text: "x"}}); err == nil {
		t.Fatal("append to non-string must fail")
	}
}

func TestOverlap(t *testing.T) {
	// Longest suffix of a that prefixes b.
	if got := Overlap("abcdef", "defghi", 1000, 0, 0); got != 3 {
		t.Fatalf("overlap = %d; want 3", got)
	}
	if got := Overlap("abc", "xyz", 1000, 0, 0); got != 0 {
		t.Fatalf("overlap = %d; want 0", got)
	}
	// Empty inputs.
	if got := Overlap("", "abc", 1000, 0, 0); got != 0 {
		t.Fatalf("overlap = %d", got)
	}
	// scan=0 disables the scan.
	if got := Overlap("abc", "cdef", 0, 0, 0); got != 0 {
		t.Fatalf("overlap = %d", got)
	}
	// Repetitive input with a candidate cap gives up (returns 0) rather than
	// scanning forever.
	if got := Overlap(strings.Repeat("a", 100), strings.Repeat("a", 50), 1000, 0, 1); got != 0 {
		t.Fatalf("capped overlap = %d; want 0", got)
	}
	// The result always satisfies the suffix/prefix property.
	for _, pair := range [][2]string{{"xyzabc", "abc123"}, {"aaaa", "aaab"}, {"hello", "lo world"}} {
		n := Overlap(pair[0], pair[1], 1000, 0, 0)
		if pair[0][len(pair[0])-n:] != pair[1][:n] {
			t.Fatalf("invalid overlap %d for %v", n, pair)
		}
	}
}

func TestEncoderDecoderRoundTrip(t *testing.T) {
	// Consecutive ops on one path use the short form; a path used again after
	// another path interns an id.
	ops := []Op{
		{Verb: VerbSet, Path: Path{"a"}, Value: float64(1)},
		{Verb: VerbAppend, Path: Path{"s"}, Text: "x"},
		{Verb: VerbAppend, Path: Path{"s"}, Text: "y"},      // short form
		{Verb: VerbSet, Path: Path{"a"}, Value: float64(2)}, // second use: interned id
	}
	encoder := NewEncoder()
	wire := encoder.Encode(ops)

	if !wire[2].Short {
		t.Fatalf("consecutive same-path op should be short: %+v", wire[2])
	}
	// Interning emits the definition op followed by the referencing op.
	if wire[3].Verb != "#" || wire[3].DefinitionPath[0] != "a" {
		t.Fatalf("expected a path definition: %+v", wire[3])
	}
	if wire[4].PathID == nil {
		t.Fatalf("second use of a path should reference the interned id: %+v", wire[4])
	}
	if *wire[4].PathID != *wire[3].DefinitionID {
		t.Fatalf("id mismatch: %+v vs %+v", wire[4], wire[3])
	}

	decoder := NewDecoder()
	decoded, err := decoder.Decode(wire)
	if err != nil {
		t.Fatal(err)
	}
	if len(decoded) != len(ops) {
		t.Fatalf("decoded = %d ops; want %d", len(decoded), len(ops))
	}
	for i := range ops {
		if decoded[i].Verb != ops[i].Verb || PathKey(decoded[i].Path) != PathKey(ops[i].Path) {
			t.Fatalf("op %d = %+v; want %+v", i, decoded[i], ops[i])
		}
	}
	// Applying either vocabulary produces the same state.
	before := map[string]any{"a": float64(0), "s": ""}
	fromOps, err := Apply(cloneForTest(before), ops)
	if err != nil {
		t.Fatal(err)
	}
	fromWire, err := Apply(cloneForTest(before), decoded)
	if err != nil {
		t.Fatal(err)
	}
	if !jsonEqual(fromOps, fromWire) {
		t.Fatalf("apply mismatch: %#v vs %#v", fromOps, fromWire)
	}
}

func TestEncoderResetsOnBase(t *testing.T) {
	// Interning resets after a replacement (a recovery point).
	encoder := NewEncoder()
	wire := encoder.Encode([]Op{
		{Verb: VerbSet, Path: Path{"a"}, Value: float64(1)},
		{Verb: VerbSet, Path: Path{"a"}, Value: float64(2)}, // interned
		{Verb: VerbReplace, Value: map[string]any{}},
		{Verb: VerbSet, Path: Path{"a"}, Value: float64(3)}, // must be inline again
	})
	last := wire[len(wire)-1]
	if last.PathID != nil || last.Short {
		t.Fatalf("post-base op must inline its path: %+v", last)
	}
}

func TestWireTupleAndParse(t *testing.T) {
	ops := []Op{
		{Verb: VerbReplace, Value: map[string]any{"a": float64(1)}},
		{Verb: VerbSet, Path: Path{"a", "b"}, Value: float64(2)},
		{Verb: VerbAppend, Path: Path{"a", "b"}, Text: "!"},
		{Verb: VerbTruncate, Path: Path{"t"}, Count: 1},
		{Verb: VerbDelete, Path: Path{"d"}},
		{Verb: VerbSplice, Path: Path{"xs"}, Index: 1, Remove: 2, Items: []any{float64(9)}},
	}
	encoder := NewEncoder()
	wire := encoder.Encode(ops)
	for _, wireOp := range wire {
		tuple := wireOp.WireTuple()
		parsed, err := ParseWireOp(tuple)
		if err != nil {
			t.Fatalf("parse %v: %v", tuple, err)
		}
		// Round trip through the tuple form is stable.
		reparsed, err := ParseWireOp(parsed.WireTuple())
		if err != nil {
			t.Fatalf("reparse: %v", err)
		}
		if reparsed.Verb != parsed.Verb {
			t.Fatalf("verb drift: %v vs %v", reparsed.Verb, parsed.Verb)
		}
	}
	// Short forms.
	short, err := ParseWireOp([]any{"d"})
	if err != nil || !short.Short || short.Verb != VerbDelete {
		t.Fatalf("short delete = %+v, %v", short, err)
	}
	short, err = ParseWireOp([]any{"a", "text"})
	if err != nil || !short.Short || short.Text != "text" {
		t.Fatalf("short append = %+v, %v", short, err)
	}
	// Invalid tuples.
	for _, invalid := range []any{
		"not a tuple",
		[]any{},
		[]any{"s"},
		[]any{"z", []any{"a"}},
		[]any{"t", []any{"p"}, float64(-1)},
		[]any{"p", []any{"a"}, float64(0), float64(0)},
	} {
		if _, err := ParseWireOp(invalid); err == nil {
			t.Fatalf("invalid tuple accepted: %v", invalid)
		}
	}
}

func TestWireOpRejectsUnsafePaths(t *testing.T) {
	// A string is not a path (upstream's prototype-escape guard).
	if err := AssertValidWireOp(WireOp{Verb: VerbSet, InlinePath: Path{"__proto__"}, Value: float64(1)}); err == nil {
		t.Fatal("reserved segment in wire path must fail")
	}
	id := -1
	if err := AssertValidWireOp(WireOp{Verb: VerbSet, PathID: &id, Value: float64(1)}); err == nil {
		t.Fatal("negative path id must fail")
	}
}

func TestDecoderRejectsUnknownID(t *testing.T) {
	unusedID := 7
	decoder := NewDecoder()
	if _, err := decoder.Decode([]WireOp{{Verb: VerbSet, PathID: &unusedID, Value: float64(1)}}); err == nil ||
		!strings.Contains(err.Error(), "unresolvable path") {
		t.Fatalf("err = %v", err)
	}
	// A short form without a previous path fails.
	decoder = NewDecoder()
	if _, err := decoder.Decode([]WireOp{{Verb: VerbDelete, Short: true}}); err == nil {
		t.Fatal("short form without a previous path must fail")
	}
}

func TestIsBaseAndIsReplace(t *testing.T) {
	if !IsBase([]Op{{Verb: VerbReplace, Value: nil}}) {
		t.Fatal("r-first batch is a base")
	}
	if IsBase([]Op{{Verb: VerbSet, Path: Path{"a"}, Value: float64(1)}}) {
		t.Fatal("non-r batch is not a base")
	}
	if IsBase(nil) {
		t.Fatal("empty batch is not a base")
	}
	if !IsReplace(Op{Verb: VerbReplace}) || IsReplace(Op{Verb: VerbSet}) {
		t.Fatal("IsReplace")
	}
}
