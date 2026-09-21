package delta

import (
	"github.com/dat267/pier/chord"
)

// Port of the applier half of chord/delta.

// Apply applies decoded ops to a plain mutable value and returns the value,
// because `r` replaces it outright and cannot be done in place.
//
// Ops are adopted, not copied (upstream's ownership rule): the consumer owns
// the batch it was handed.
func Apply(target chord.JsonValue, ops []Op) (chord.JsonValue, error) {
	root := target
	for _, op := range ops {
		if err := AssertValidOp(op); err != nil {
			return nil, err
		}
		if op.Verb == VerbReplace {
			root = op.Value
			continue
		}
		if err := AssertSafePath(op.Path); err != nil {
			return nil, err
		}

		if op.Verb == VerbSplice {
			var container chord.JsonValue
			if len(op.Path) == 0 {
				container = root
			} else {
				resolved, err := resolve(root, op.Path)
				if err != nil {
					return nil, err
				}
				container = resolved
			}
			array, ok := container.([]any)
			if !ok {
				return nil, &PathError{Path: op.Path}
			}
			array, err := spliceArray(array, op.Index, op.Remove, op.Items)
			if err != nil {
				return nil, err
			}
			if len(op.Path) == 0 {
				root = array
			} else if err := writeChild(root, op.Path, array); err != nil {
				return nil, err
			}
			continue
		}

		// s/d/a/t can never target the root.
		parentPath := op.Path[:len(op.Path)-1]
		parentValue, err := resolve(root, parentPath)
		if err != nil {
			return nil, err
		}
		key := op.Path[len(op.Path)-1]

		if array, isArray := parentValue.([]any); isArray {
			index, ok := key.(int)
			if !ok {
				return nil, &UnsafePathError{Segment: key}
			}
			if err := assertIndexInRange(array, index); err != nil {
				return nil, err
			}
			switch op.Verb {
			case VerbSet:
				if index == len(array) {
					array = append(array, op.Value)
				} else {
					array[index] = op.Value
				}
			case VerbDelete:
				if index >= len(array) {
					return nil, &PathError{Path: op.Path}
				}
				array = append(array[:index], array[index+1:]...)
			case VerbAppend:
				current, ok := array[index].(string)
				if !ok {
					return nil, &PathError{Path: op.Path}
				}
				array[index] = current + op.Text
			case VerbTruncate:
				current, ok := array[index].(string)
				if !ok {
					return nil, &PathError{Path: op.Path}
				}
				array[index] = truncateString(current, op.Count)
			}
			if len(parentPath) == 0 {
				root = array
			} else {
				if err := writeChild(root, parentPath, array); err != nil {
					return nil, err
				}
			}
			continue
		}

		object, isObject := parentValue.(map[string]any)
		if !isObject {
			return nil, &PathError{Path: op.Path}
		}
		name, ok := key.(string)
		if !ok {
			return nil, &UnsafePathError{Segment: key}
		}
		switch op.Verb {
		case VerbSet:
			object[name] = op.Value
		case VerbDelete:
			delete(object, name)
		case VerbAppend:
			current, ok := object[name].(string)
			if !ok {
				return nil, &PathError{Path: op.Path}
			}
			object[name] = current + op.Text
		case VerbTruncate:
			current, ok := object[name].(string)
			if !ok {
				return nil, &PathError{Path: op.Path}
			}
			object[name] = truncateString(current, op.Count)
		}
	}
	return root, nil
}

// truncateString drops the last count bytes (upstream String.slice(0, -count)).
func truncateString(value string, count int) string {
	if count == 0 {
		return value
	}
	if count >= len(value) {
		return ""
	}
	return value[:len(value)-count]
}

// spliceArray replaces remove elements at index with items.
func spliceArray(array []any, index, remove int, items []chord.JsonValue) ([]any, error) {
	if index > len(array) {
		return nil, &UnsafePathError{Segment: index}
	}
	if index+remove > len(array) {
		return nil, &PathError{Path: Path{index}}
	}
	out := make([]any, 0, len(array)-remove+len(items))
	out = append(out, array[:index]...)
	for _, item := range items {
		out = append(out, item)
	}
	out = append(out, array[index+remove:]...)
	return out, nil
}

// writeChild writes a replaced child container back into its parent chain.
func writeChild(root chord.JsonValue, parentPath Path, value chord.JsonValue) error {
	if len(parentPath) == 0 {
		return nil
	}
	parent, err := resolve(root, parentPath[:len(parentPath)-1])
	if err != nil {
		return err
	}
	key := parentPath[len(parentPath)-1]
	switch typed := parent.(type) {
	case []any:
		index, ok := key.(int)
		if !ok || index < 0 || index >= len(typed) {
			return &PathError{Path: parentPath}
		}
		typed[index] = value
	case map[string]any:
		name, ok := key.(string)
		if !ok {
			return &UnsafePathError{Segment: key}
		}
		typed[name] = value
	default:
		return &PathError{Path: parentPath}
	}
	return nil
}

// ApplyImmutable applies decoded operations without mutating the previous
// value.
func ApplyImmutable(target chord.JsonValue, ops []Op) (chord.JsonValue, error) {
	root := target
	for _, op := range ops {
		if op.Verb == VerbReplace {
			if err := AssertValidOp(op); err != nil {
				return nil, err
			}
			root = op.Value
			continue
		}
		copyPath := op.Path
		if op.Verb != VerbSplice {
			copyPath = op.Path[:len(op.Path)-1]
		}
		copied, err := copyContainers(root, copyPath)
		if err != nil {
			return nil, err
		}
		root, err = Apply(copied, []Op{op})
		if err != nil {
			return nil, err
		}
	}
	return root, nil
}

// copyContainers shallow-copies every container along the path so an
// immutable update can be applied without mutating the previous value.
func copyContainers(root chord.JsonValue, path Path) (chord.JsonValue, error) {
	copiedRoot, err := copyContainer(root, path)
	if err != nil {
		return nil, err
	}
	source := root
	destination := copiedRoot
	for _, segment := range path {
		sourceObject, ok := source.(map[string]any)
		sourceArray, isArray := source.([]any)
		var child chord.JsonValue
		switch {
		case isArray:
			index, ok := segment.(int)
			if !ok || index < 0 || index >= len(sourceArray) {
				return nil, &PathError{Path: path}
			}
			child = sourceArray[index]
		case ok:
			name, ok := segment.(string)
			if !ok {
				return nil, &UnsafePathError{Segment: segment}
			}
			value, exists := sourceObject[name]
			if !exists {
				return nil, &PathError{Path: path}
			}
			child = value
		default:
			return nil, &PathError{Path: path}
		}
		copiedChild, err := copyContainer(child, path)
		if err != nil {
			return nil, err
		}
		if err := assignSegment(destination, segment, copiedChild); err != nil {
			return nil, err
		}
		source = child
		destination = copiedChild
	}
	return copiedRoot, nil
}

func copyContainer(value chord.JsonValue, path Path) (chord.JsonValue, error) {
	switch typed := value.(type) {
	case []any:
		out := make([]any, len(typed))
		copy(out, typed)
		return out, nil
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			out[key] = item
		}
		return out, nil
	default:
		return nil, &PathError{Path: path}
	}
}

func assignSegment(container chord.JsonValue, segment Seg, value chord.JsonValue) error {
	switch typed := container.(type) {
	case []any:
		index, ok := segment.(int)
		if !ok || index < 0 || index >= len(typed) {
			return &UnsafePathError{Segment: segment}
		}
		typed[index] = value
		return nil
	case map[string]any:
		name, ok := segment.(string)
		if !ok {
			return &UnsafePathError{Segment: segment}
		}
		typed[name] = value
		return nil
	default:
		return &PathError{Path: Path{segment}}
	}
}

func resolveValue(root chord.JsonValue, path Path) (chord.JsonValue, error) {
	node := root
	for _, segment := range path {
		switch typed := node.(type) {
		case []any:
			index, ok := segment.(int)
			if !ok {
				return nil, &UnsafePathError{Segment: segment}
			}
			if index < 0 || index >= len(typed) {
				return nil, &PathError{Path: path}
			}
			node = typed[index]
		case map[string]any:
			name, ok := segment.(string)
			if !ok {
				return nil, &UnsafePathError{Segment: segment}
			}
			value, exists := typed[name]
			if !exists {
				return nil, &PathError{Path: path}
			}
			node = value
		default:
			return nil, &PathError{Path: path}
		}
	}
	return node, nil
}

func resolve(root chord.JsonValue, path Path) (chord.JsonValue, error) {
	node, err := resolveValue(root, path)
	if err != nil {
		return nil, err
	}
	switch node.(type) {
	case []any, map[string]any:
		return node, nil
	default:
		return nil, &PathError{Path: path}
	}
}
