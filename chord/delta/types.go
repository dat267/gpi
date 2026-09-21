// Package delta is a Go port of chord/delta (pi/packages/chord/src/delta):
// operation-log change tracking over plain JSON.
//
// Ground truth: pi/packages/chord/src/delta/index.ts at the pinned upstream
// commit.
//
// D-row D11: upstream's Op/WireOp are heterogeneous tuples and its Tracker is
// a JavaScript Proxy that intercepts every mutation. Go has neither, so ops
// are structs (with the tuple JSON/CBOR forms produced by Tuple) and the
// tracker is an explicit-mutation API over the same diff engine. The diff
// engine, validation, applier, and wire codec are line-for-line ports.
package delta

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/dat267/pier/chord"
)

// Seg is one path segment.
type Seg = any // string or int

// Path is a sequence of segments.
type Path = []Seg

// OpVerb enumerates the decoded op verbs.
type OpVerb = string

const (
	VerbReplace  OpVerb = "r"
	VerbSet      OpVerb = "s"
	VerbDelete   OpVerb = "d"
	VerbAppend   OpVerb = "a"
	VerbTruncate OpVerb = "t"
	VerbSplice   OpVerb = "p"
)

// Op is one decoded operation: paths inline, no ids, no short forms.
//
// `r` is the only op that replaces a whole value; `s`/`d`/`a`/`t` cannot target
// the root.
type Op struct {
	Verb OpVerb

	// Path is set for every verb except `r`.
	Path Path

	// Value is the payload for `r` and `s`.
	Value chord.JsonValue

	// Text is the appended text for `a`.
	Text string

	// Count is the truncation count for `t`.
	Count int

	// Index/Remove/Items are the splice payload for `p`.
	Index  int
	Remove int
	Items  []chord.JsonValue
}

// WireOp is one wire operation: a path inline or by id, with the two
// compressions (["#", id, path] definitions and arity-omitted short forms).
type WireOp struct {
	Verb OpVerb

	// InlinePath is set when the op carries an inline path.
	InlinePath Path
	// PathID is set when the op references an interned path.
	PathID *int

	// Short reports the arity-omitted form (reuses the previous op's path).
	Short bool

	// Definition carries ["#", id, path].
	DefinitionID   *int
	DefinitionPath Path

	// Payload fields mirror Op.
	Value  chord.JsonValue
	Text   string
	Count  int
	Index  int
	Remove int
	Items  []chord.JsonValue
}

// IsReplace reports whether an op replaces the whole value.
func IsReplace(op Op) bool { return op.Verb == VerbReplace }

// IsWireReplace reports whether a wire op replaces the whole value.
func IsWireReplace(op WireOp) bool { return op.Verb == VerbReplace }

// IsBase reports whether a batch begins with a replacement (Flush guarantees
// `r` is at index 0 or absent).
func IsBase(ops []Op) bool { return len(ops) > 0 && ops[0].Verb == VerbReplace }

// IsWireBase is the wire-vocabulary form.
func IsWireBase(ops []WireOp) bool { return len(ops) > 0 && ops[0].Verb == VerbReplace }

// Overlap returns the longest suffix of a that is a prefix of b, bounded by
// scan; probe/maxCandidates mirror upstream's tuned parameters.
//
// The result is always correct: for the returned n,
// a[len(a)-n:] == b[:n].
func Overlap(a, b string, scan int, probe int, maxCandidates int) int {
	if probe == 0 {
		probe = 64
	}
	if maxCandidates == 0 {
		maxCandidates = 8
	}
	if len(a) == 0 || len(b) == 0 || scan == 0 {
		return 0
	}
	tail := a
	if len(a) > scan {
		tail = a[len(a)-scan:]
	}

	// A probe of length h can only find overlaps of at least h, so try a long
	// head first (few candidates, catches the large overlaps a rolling window
	// produces), then one character, which finds any overlap.
	for _, h := range []int{min(probe, len(b)), 1} {
		head := b[:h]
		tried := 0
		for searchFrom := 0; searchFrom <= len(tail)-h; {
			k := strings.Index(tail[searchFrom:], head)
			if k == -1 {
				break
			}
			k += searchFrom
			tried++
			if tried > maxCandidates {
				break
			}
			n := len(tail) - k
			if n <= len(b) && tail[k:] == b[:n] {
				return n
			}
			searchFrom = k + 1
		}
		if h == 1 {
			break
		}
	}
	return 0
}

// PathKey renders a path for interning (upstream JSON.stringify(path)).
func PathKey(path Path) string {
	var builder strings.Builder
	builder.WriteByte('[')
	for i, seg := range path {
		if i > 0 {
			builder.WriteByte(',')
		}
		switch typed := seg.(type) {
		case string:
			encoded, _ := json.Marshal(typed)
			builder.Write(encoded)
		case int:
			fmt.Fprintf(&builder, "%d", typed)
		default:
			fmt.Fprintf(&builder, "%v", typed)
		}
	}
	builder.WriteByte(']')
	return builder.String()
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
