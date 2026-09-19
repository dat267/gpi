package delta

import (
	"fmt"

	"github.com/dat267/gpi/chord"
)

// Port of the codec half of chord/delta: path interning and arity omission
// between the tracker and a boundary.
//
// ONE PAIR PER INDEPENDENT STATE STREAM. Every decoder must observe exactly
// the batches encoded by its matching encoder, beginning with that state's
// base.

// Encoder interns paths on their second use and omits repeated paths.
type Encoder struct {
	seen        map[string]bool
	ids         map[string]int
	nextID      int
	previous    string
	hasPrevious bool
}

// NewEncoder builds an encoder.
func NewEncoder() *Encoder {
	return &Encoder{seen: map[string]bool{}, ids: map[string]int{}}
}

// Encode compresses decoded ops into wire ops.
func (e *Encoder) Encode(ops []Op) []WireOp {
	// Arity omission is scoped to a batch; ids are the only cross-batch state.
	e.previous = ""
	e.hasPrevious = false
	out := make([]WireOp, 0, len(ops))
	for _, op := range ops {
		if op.Verb == VerbReplace {
			out = append(out, WireOp{Verb: VerbReplace, Value: op.Value})
			// A base batch is a recovery point: everything after it must be
			// self-contained, so the dictionary resets.
			e.seen = map[string]bool{}
			e.ids = map[string]int{}
			e.nextID = 0
			e.previous = ""
			e.hasPrevious = false
			continue
		}
		path := op.Path
		key := PathKey(path)

		// Same path as the previous op: drop the ref entirely.
		if e.hasPrevious && key == e.previous {
			out = append(out, e.shortForm(op))
			continue
		}

		var pathID *int
		var inlinePath Path
		if existing, ok := e.ids[key]; ok {
			id := existing
			pathID = &id
		} else if e.seen[key] {
			id := e.nextID
			e.nextID++
			e.ids[key] = id
			out = append(out, WireOp{Verb: "#", DefinitionID: &id, DefinitionPath: clonePath(path)})
			pathID = &id
		} else {
			e.seen[key] = true // first use: inline
			inlinePath = clonePath(path)
		}

		entry := WireOp{
			Verb: op.Verb, PathID: pathID, InlinePath: inlinePath,
			Value: op.Value, Text: op.Text, Count: op.Count,
			Index: op.Index, Remove: op.Remove, Items: op.Items,
		}
		out = append(out, entry)
		e.previous = key
		e.hasPrevious = true
	}
	return out
}

func (e *Encoder) shortForm(op Op) WireOp {
	return WireOp{
		Verb: op.Verb, Short: true,
		Value: op.Value, Text: op.Text, Count: op.Count,
		Index: op.Index, Remove: op.Remove, Items: op.Items,
	}
}

// Decoder resolves interned paths/short forms back into decoded ops.
type Decoder struct {
	paths map[int]Path
}

// NewDecoder builds a decoder.
func NewDecoder() *Decoder {
	return &Decoder{paths: map[int]Path{}}
}

// Decode expands wire ops into decoded ops.
func (d *Decoder) Decode(wire []WireOp) ([]Op, error) {
	var previous Path
	hasPrevious := false
	out := make([]Op, 0, len(wire))
	for _, op := range wire {
		if err := AssertValidWireOp(op); err != nil {
			return nil, err
		}
		if op.Verb == "#" {
			if op.DefinitionID == nil {
				return nil, fmt.Errorf("# shape")
			}
			if err := AssertSafePath(op.DefinitionPath); err != nil {
				return nil, err
			}
			d.paths[*op.DefinitionID] = clonePath(op.DefinitionPath)
			continue
		}
		if op.Verb == VerbReplace {
			out = append(out, Op{Verb: VerbReplace, Value: op.Value})
			d.paths = map[int]Path{}
			previous, hasPrevious = nil, false
			continue
		}

		var path Path
		if op.Short {
			if !hasPrevious {
				return nil, &PathError{Path: Path{}}
			}
			path = previous
		} else if op.PathID != nil {
			resolved, ok := d.paths[*op.PathID]
			if !ok {
				return nil, &PathError{ID: op.PathID}
			}
			path = resolved
			previous, hasPrevious = path, true
		} else {
			path = op.InlinePath
			previous, hasPrevious = path, true
		}

		if op.Verb != VerbSplice && len(path) == 0 {
			return nil, &PathError{Path: path}
		}
		entry := Op{
			Verb: op.Verb, Path: clonePath(path),
			Value: op.Value, Text: op.Text, Count: op.Count,
			Index: op.Index, Remove: op.Remove, Items: op.Items,
		}
		out = append(out, entry)
	}
	return out, nil
}

// Tuple renders a decoded op in the JSON tuple form.
func (op Op) Tuple() []any {
	switch op.Verb {
	case VerbReplace:
		return []any{"r", op.Value}
	case VerbSet:
		return []any{"s", pathTuple(op.Path), op.Value}
	case VerbDelete:
		return []any{"d", pathTuple(op.Path)}
	case VerbAppend:
		return []any{"a", pathTuple(op.Path), op.Text}
	case VerbTruncate:
		return []any{"t", pathTuple(op.Path), op.Count}
	case VerbSplice:
		items := make([]any, len(op.Items))
		copy(items, op.Items)
		return []any{"p", pathTuple(op.Path), op.Index, op.Remove, items}
	default:
		return []any{op.Verb}
	}
}

// WireTuple renders a wire op in the JSON tuple form.
func (op WireOp) WireTuple() []any {
	if op.Verb == "#" {
		id := 0
		if op.DefinitionID != nil {
			id = *op.DefinitionID
		}
		return []any{"#", id, pathTuple(op.DefinitionPath)}
	}
	ref := func() any {
		if op.Short {
			return nil // omitted
		}
		if op.PathID != nil {
			return *op.PathID
		}
		return pathTuple(op.InlinePath)
	}
	switch op.Verb {
	case VerbReplace:
		return []any{"r", op.Value}
	case VerbSet:
		if op.Short {
			return []any{"s", op.Value}
		}
		return []any{"s", ref(), op.Value}
	case VerbDelete:
		if op.Short {
			return []any{"d"}
		}
		return []any{"d", ref()}
	case VerbAppend:
		if op.Short {
			return []any{"a", op.Text}
		}
		return []any{"a", ref(), op.Text}
	case VerbTruncate:
		if op.Short {
			return []any{"t", op.Count}
		}
		return []any{"t", ref(), op.Count}
	case VerbSplice:
		items := make([]any, len(op.Items))
		copy(items, op.Items)
		if op.Short {
			return []any{"p", op.Index, op.Remove, items}
		}
		return []any{"p", ref(), op.Index, op.Remove, items}
	default:
		return []any{op.Verb}
	}
}

// ParseWireOp converts a JSON/CBOR tuple value into a wire op.
func ParseWireOp(value any) (WireOp, error) {
	tuple, ok := value.([]any)
	if !ok || len(tuple) == 0 {
		return WireOp{}, fmt.Errorf("op is not a tuple")
	}
	verb, ok := tuple[0].(string)
	if !ok {
		return WireOp{}, fmt.Errorf("op verb is not a string")
	}
	op := WireOp{Verb: verb}
	switch verb {
	case VerbReplace:
		if len(tuple) != 2 {
			return WireOp{}, fmt.Errorf("r arity")
		}
		op.Value = tuple[1]
	case VerbSet:
		switch len(tuple) {
		case 3:
			if err := op.setRef(tuple[1]); err != nil {
				return WireOp{}, err
			}
			op.Value = tuple[2]
		case 2:
			op.Short = true
			op.Value = tuple[1]
		default:
			return WireOp{}, fmt.Errorf("s arity")
		}
	case VerbDelete:
		switch len(tuple) {
		case 2:
			if err := op.setRef(tuple[1]); err != nil {
				return WireOp{}, err
			}
		case 1:
			op.Short = true
		default:
			return WireOp{}, fmt.Errorf("d arity")
		}
	case VerbAppend:
		switch len(tuple) {
		case 3:
			if err := op.setRef(tuple[1]); err != nil {
				return WireOp{}, err
			}
			text, ok := tuple[2].(string)
			if !ok {
				return WireOp{}, fmt.Errorf("a value")
			}
			op.Text = text
		case 2:
			op.Short = true
			text, ok := tuple[1].(string)
			if !ok {
				return WireOp{}, fmt.Errorf("a value")
			}
			op.Text = text
		default:
			return WireOp{}, fmt.Errorf("a arity")
		}
	case VerbTruncate:
		switch len(tuple) {
		case 3:
			if err := op.setRef(tuple[1]); err != nil {
				return WireOp{}, err
			}
			count, err := intValue(tuple[2])
			if err != nil {
				return WireOp{}, fmt.Errorf("t count")
			}
			op.Count = count
		case 2:
			op.Short = true
			count, err := intValue(tuple[1])
			if err != nil {
				return WireOp{}, fmt.Errorf("t count")
			}
			op.Count = count
		default:
			return WireOp{}, fmt.Errorf("t arity")
		}
	case VerbSplice:
		var indexValue, removeValue, itemsValue any
		switch len(tuple) {
		case 5:
			if err := op.setRef(tuple[1]); err != nil {
				return WireOp{}, err
			}
			indexValue, removeValue, itemsValue = tuple[2], tuple[3], tuple[4]
		case 4:
			op.Short = true
			indexValue, removeValue, itemsValue = tuple[1], tuple[2], tuple[3]
		default:
			return WireOp{}, fmt.Errorf("p arity")
		}
		index, err := intValue(indexValue)
		if err != nil {
			return WireOp{}, fmt.Errorf("p index")
		}
		remove, err := intValue(removeValue)
		if err != nil {
			return WireOp{}, fmt.Errorf("p remove")
		}
		items, ok := itemsValue.([]any)
		if !ok {
			return WireOp{}, fmt.Errorf("p items")
		}
		op.Index, op.Remove = index, remove
		op.Items = make([]chord.JsonValue, len(items))
		copy(op.Items, items)
	case "#":
		if len(tuple) != 3 {
			return WireOp{}, fmt.Errorf("# shape")
		}
		id, err := intValue(tuple[1])
		if err != nil || id < 0 {
			return WireOp{}, fmt.Errorf("# shape")
		}
		path, err := pathValue(tuple[2])
		if err != nil {
			return WireOp{}, err
		}
		op.DefinitionID, op.DefinitionPath = &id, path
	default:
		return WireOp{}, fmt.Errorf("unknown op verb: %v", verb)
	}
	if err := AssertValidWireOp(op); err != nil {
		return WireOp{}, err
	}
	return op, nil
}

func (op *WireOp) setRef(value any) error {
	switch typed := value.(type) {
	case int:
		id := typed
		op.PathID = &id
		return nil
	case int64:
		id := int(typed)
		op.PathID = &id
		return nil
	default:
		path, err := pathValue(value)
		if err != nil {
			return err
		}
		op.InlinePath = path
		return nil
	}
}

func intValue(value any) (int, error) {
	switch typed := value.(type) {
	case int:
		return typed, nil
	case int64:
		return int(typed), nil
	case float64:
		if typed == float64(int(typed)) {
			return int(typed), nil
		}
	}
	return 0, fmt.Errorf("not an integer")
}

func pathValue(value any) (Path, error) {
	list, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("path is not an array")
	}
	path := make(Path, 0, len(list))
	for _, segment := range list {
		switch typed := segment.(type) {
		case string:
			path = append(path, typed)
		case int:
			path = append(path, typed)
		case int64:
			path = append(path, int(typed))
		case float64:
			if typed == float64(int(typed)) {
				path = append(path, int(typed))
				continue
			}
			return nil, fmt.Errorf("path segment is not a string or integer")
		default:
			return nil, fmt.Errorf("path segment is not a string or integer")
		}
	}
	return path, nil
}

func pathTuple(path Path) []any {
	out := make([]any, len(path))
	copy(out, path)
	return out
}
