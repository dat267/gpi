package delta

import (
	"fmt"
)

// Port of the validation half of chord/delta: verb/arity/payload checks for
// both vocabularies plus path safety.

// ReservedSegments cannot appear as path segments (prototype-pollution guard).
var ReservedSegments = map[string]bool{"__proto__": true, "constructor": true, "prototype": true}

// UnsafePathError reports an unsafe path segment or an out-of-range index.
type UnsafePathError struct {
	Segment Seg
	Message string
}

func (e *UnsafePathError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return fmt.Sprintf("unsafe path segment: %v", e.Segment)
}

// PathError reports an unresolvable path.
type PathError struct {
	Path Path
	ID   *int
}

func (e *PathError) Error() string {
	if e.ID != nil {
		return fmt.Sprintf("unresolvable path: %d", *e.ID)
	}
	return fmt.Sprintf("unresolvable path: %s", PathKey(e.Path))
}

// AssertSafePath validates segments (reserved strings, non-negative integers).
func AssertSafePath(path Path) error {
	for _, seg := range path {
		switch typed := seg.(type) {
		case string:
			if ReservedSegments[typed] {
				return &UnsafePathError{Segment: typed}
			}
		case int:
			if typed < 0 {
				return &UnsafePathError{Segment: typed}
			}
		case int64:
			if typed < 0 {
				return &UnsafePathError{Segment: typed}
			}
		default:
			return &UnsafePathError{Segment: seg}
		}
	}
	return nil
}

func assertPathArg(path Path, nonEmpty bool) error {
	if nonEmpty && len(path) == 0 {
		return fmt.Errorf("path is empty")
	}
	return AssertSafePath(path)
}

// AssertValidOp validates a decoded op.
//
// Each vocabulary gets the validator that matches it: validating a decoded op
// against the wire grammar would be laxer than the type.
func AssertValidOp(op Op) error {
	switch op.Verb {
	case VerbReplace:
		return nil
	case VerbSet:
		return assertPathArg(op.Path, true)
	case VerbDelete:
		return assertPathArg(op.Path, true)
	case VerbAppend:
		return assertPathArg(op.Path, true)
	case VerbTruncate:
		if op.Count < 0 {
			return fmt.Errorf("t shape")
		}
		return assertPathArg(op.Path, true)
	case VerbSplice:
		if err := assertPathArg(op.Path, false); err != nil {
			return err
		}
		if op.Index < 0 {
			return fmt.Errorf("p index")
		}
		if op.Remove < 0 {
			return fmt.Errorf("p remove")
		}
		if op.Items == nil {
			return fmt.Errorf("p items")
		}
		return nil
	default:
		// Silently skipping an unknown verb is how a newer producer's op vanishes.
		return fmt.Errorf("unknown op verb: %v", op.Verb)
	}
}

// AssertValidWireOp validates a wire op (ids and short forms are legal).
func AssertValidWireOp(op WireOp) error {
	okRef := func() error {
		if op.PathID != nil {
			if *op.PathID < 0 {
				return fmt.Errorf("bad path id")
			}
			return nil
		}
		// A string is not a path: unchecked, it resolves to the root.
		if op.InlinePath == nil {
			return fmt.Errorf("path is not an array")
		}
		return AssertSafePath(op.InlinePath)
	}

	switch op.Verb {
	case VerbReplace:
		return nil
	case VerbSet:
		if op.Short {
			return nil
		}
		return okRef()
	case VerbDelete:
		if op.Short {
			return nil
		}
		return okRef()
	case VerbAppend:
		if !op.Short {
			if err := okRef(); err != nil {
				return err
			}
		}
		return nil
	case VerbTruncate:
		if op.Count < 0 {
			return fmt.Errorf("t count")
		}
		if !op.Short {
			return okRef()
		}
		return nil
	case VerbSplice:
		if op.Index < 0 {
			return fmt.Errorf("p index")
		}
		if op.Remove < 0 {
			return fmt.Errorf("p remove")
		}
		if op.Items == nil {
			return fmt.Errorf("p items")
		}
		if !op.Short {
			return okRef()
		}
		return nil
	case "#":
		if op.DefinitionID == nil || *op.DefinitionID < 0 || op.DefinitionPath == nil {
			return fmt.Errorf("# shape")
		}
		return AssertSafePath(op.DefinitionPath)
	default:
		return fmt.Errorf("unknown op verb: %v", op.Verb)
	}
}

// assertIndexInRange: an index may address an existing element or append
// exactly one past the end, which keeps the value a JsonValue (a sparse array
// does not survive a JSON round trip) and blocks the allocation denial of
// service a single large index would permit.
func assertIndexInRange(parent []any, index int) error {
	if index > len(parent) {
		return &UnsafePathError{Segment: index}
	}
	return nil
}
