// Package protocol is a faithful Go port of @earendil-works/pi-protocol
// (pi/packages/protocol): a transport-neutral, length-prefixed CBOR protocol
// for remote pi sessions.
//
// Ground truth: pi/packages/protocol/src at the pinned upstream commit.
package protocol

import (
	"encoding/binary"
	"fmt"
	"math"
	"reflect"
	"unicode/utf8"
)

// Port of cbor/{options,encoder,decoder}.ts: the strict, definite-length
// RFC 8949 subset the protocol uses.

const (
	uint32Base         = 0x1_0000_0000
	maxUint32          = 0xffff_ffff
	maxConfiguredDepth = 512
)

// Safe defaults for untrusted protocol payloads.
const (
	DefaultMaxCborByteLength      = 16 * 1024 * 1024
	DefaultMaxCborContainerLength = 1_000_000
	DefaultMaxCborDepth           = 64
)

// CborOptions bound a CBOR payload. Nil fields use the safe defaults; an
// explicit 0 is a real limit (upstream `??` semantics).
type CborOptions struct {
	// MaxByteLength caps encoded input/output bytes and byte/text string length.
	MaxByteLength *int64
	// MaxContainerLength caps array elements and map entries.
	MaxContainerLength *int64
	// MaxDepth caps recursive item depth.
	MaxDepth *int64
}

// ResolvedCborOptions is the validated option set.
type ResolvedCborOptions struct {
	MaxByteLength      int64
	MaxContainerLength int64
	MaxDepth           int64
}

// CborError is the CBOR codec error type.
type CborError struct{ Message string }

func (e *CborError) Error() string { return e.Message }

func cborErrorf(format string, args ...any) *CborError {
	return &CborError{Message: fmt.Sprintf(format, args...)}
}

func resolveLimit(name string, value int64, maximum int64) (int64, error) {
	if value < 0 || value > maximum {
		return 0, fmt.Errorf("%s must be an integer between 0 and %d", name, maximum)
	}
	return value, nil
}

// ResolveOptions validates and defaults CBOR options.
func ResolveOptions(options *CborOptions) (ResolvedCborOptions, error) {
	byteLength := int64(DefaultMaxCborByteLength)
	containerLength := int64(DefaultMaxCborContainerLength)
	depth := int64(DefaultMaxCborDepth)
	if options != nil {
		if options.MaxByteLength != nil {
			byteLength = *options.MaxByteLength
		}
		if options.MaxContainerLength != nil {
			containerLength = *options.MaxContainerLength
		}
		if options.MaxDepth != nil {
			depth = *options.MaxDepth
		}
	}
	resolved := ResolvedCborOptions{}
	var err error
	if resolved.MaxByteLength, err = resolveLimit("maxByteLength", byteLength, maxUint32); err != nil {
		return ResolvedCborOptions{}, err
	}
	if resolved.MaxContainerLength, err = resolveLimit("maxContainerLength", containerLength, maxUint32); err != nil {
		return ResolvedCborOptions{}, err
	}
	if resolved.MaxDepth, err = resolveLimit("maxDepth", depth, maxConfiguredDepth); err != nil {
		return ResolvedCborOptions{}, err
	}
	return resolved, nil
}

// cborWriter accumulates the encoded payload with a byte cap.
type cborWriter struct {
	buffer        []byte
	maxByteLength int64
}

func newCborWriter(maxByteLength int64) *cborWriter {
	capacity := maxByteLength
	if capacity > 256 {
		capacity = 256
	}
	return &cborWriter{buffer: make([]byte, 0, capacity), maxByteLength: maxByteLength}
}

func (w *cborWriter) ensureCapacity(additional int) error {
	if int64(len(w.buffer)+additional) > w.maxByteLength {
		return cborErrorf("CBOR byte length exceeds configured limit of %d", w.maxByteLength)
	}
	return nil
}

func (w *cborWriter) writeByte(value byte) error {
	if err := w.ensureCapacity(1); err != nil {
		return err
	}
	w.buffer = append(w.buffer, value)
	return nil
}

func (w *cborWriter) writeBytes(bytes []byte) error {
	if err := w.ensureCapacity(len(bytes)); err != nil {
		return err
	}
	w.buffer = append(w.buffer, bytes...)
	return nil
}

func (w *cborWriter) writeUint16(value uint16) error {
	if err := w.ensureCapacity(2); err != nil {
		return err
	}
	w.buffer = append(w.buffer, byte(value>>8), byte(value))
	return nil
}

func (w *cborWriter) writeUint32(value uint32) error {
	if err := w.ensureCapacity(4); err != nil {
		return err
	}
	w.buffer = append(w.buffer, byte(value>>24), byte(value>>16), byte(value>>8), byte(value))
	return nil
}

func (w *cborWriter) writeUint64(value uint64) error {
	if err := w.ensureCapacity(8); err != nil {
		return err
	}
	w.buffer = binary.BigEndian.AppendUint64(w.buffer, value)
	return nil
}

func (w *cborWriter) writeFloat64(value float64) error {
	if err := w.ensureCapacity(9); err != nil {
		return err
	}
	w.buffer = append(w.buffer, 0xfb)
	w.buffer = binary.BigEndian.AppendUint64(w.buffer, math.Float64bits(value))
	return nil
}

func writeArgument(w *cborWriter, majorType byte, value uint64) error {
	prefix := majorType << 5
	switch {
	case value < 24:
		return w.writeByte(prefix | byte(value))
	case value <= 0xff:
		if err := w.writeByte(prefix | 24); err != nil {
			return err
		}
		return w.writeByte(byte(value))
	case value <= 0xffff:
		if err := w.writeByte(prefix | 25); err != nil {
			return err
		}
		return w.writeUint16(uint16(value))
	case value <= maxUint32:
		if err := w.writeByte(prefix | 26); err != nil {
			return err
		}
		return w.writeUint32(uint32(value))
	default:
		if err := w.writeByte(prefix | 27); err != nil {
			return err
		}
		return w.writeUint64(value)
	}
}

func encodeText(w *cborWriter, value string, options ResolvedCborOptions) error {
	if int64(len(value)) > options.MaxByteLength {
		return cborErrorf("CBOR text string length exceeds configured limit of %d", options.MaxByteLength)
	}
	if !utf8.ValidString(value) {
		return cborErrorf("CBOR text strings must contain valid Unicode scalar values")
	}
	if err := writeArgument(w, 3, uint64(len(value))); err != nil {
		return err
	}
	return w.writeBytes([]byte(value))
}

// cycleKey identifies a container by identity (upstream's Set<object>).
type cycleKey struct {
	kind string
	ptr  uintptr
}

func identityOf(value any) (cycleKey, bool) {
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Slice:
		if reflected.Len() == 0 {
			return cycleKey{}, false // an empty container cannot participate in a cycle
		}
		return cycleKey{kind: "slice", ptr: reflected.Pointer()}, true
	case reflect.Map:
		return cycleKey{kind: "map", ptr: uintptr(reflected.UnsafePointer())}, true
	default:
		return cycleKey{}, false
	}
}

func encodeValue(w *cborWriter, value any, options ResolvedCborOptions, depth int64, ancestors map[cycleKey]bool) error {
	if depth > options.MaxDepth {
		return cborErrorf("CBOR nesting depth exceeds configured limit of %d", options.MaxDepth)
	}

	switch typed := value.(type) {
	case nil:
		return w.writeByte(0xf6)
	case bool:
		if typed {
			return w.writeByte(0xf5)
		}
		return w.writeByte(0xf4)
	case int64:
		if typed >= 0 {
			return writeArgument(w, 0, uint64(typed))
		}
		return writeArgument(w, 1, uint64(-1-typed))
	case int:
		return encodeValue(w, int64(typed), options, depth, ancestors)
	case float64:
		if math.IsInf(typed, 0) || math.IsNaN(typed) {
			return cborErrorf("CBOR numbers must be finite")
		}
		if typed == math.Trunc(typed) && typed >= math.MinInt64 && typed <= math.MaxInt64 {
			return encodeValue(w, int64(typed), options, depth, ancestors)
		}
		return w.writeFloat64(typed)
	case string:
		return encodeText(w, typed, options)
	case []byte:
		if int64(len(typed)) > options.MaxByteLength {
			return cborErrorf("CBOR byte string length exceeds configured limit of %d", options.MaxByteLength)
		}
		if err := writeArgument(w, 2, uint64(len(typed))); err != nil {
			return err
		}
		return w.writeBytes(typed)
	case []any:
		key, hasIdentity := identityOf(typed)
		if hasIdentity && ancestors[key] {
			return cborErrorf("CBOR values must not contain cycles")
		}
		if int64(len(typed)) > options.MaxContainerLength {
			return cborErrorf("CBOR array length exceeds configured limit of %d", options.MaxContainerLength)
		}
		if hasIdentity {
			ancestors[key] = true
			defer delete(ancestors, key)
		}
		if err := writeArgument(w, 4, uint64(len(typed))); err != nil {
			return err
		}
		for _, item := range typed {
			if err := encodeValue(w, item, options, depth+1, ancestors); err != nil {
				return err
			}
		}
		return nil
	case map[string]any:
		key, hasIdentity := identityOf(typed)
		if hasIdentity && ancestors[key] {
			return cborErrorf("CBOR values must not contain cycles")
		}
		if int64(len(typed)) > options.MaxContainerLength {
			return cborErrorf("CBOR map length exceeds configured limit of %d", options.MaxContainerLength)
		}
		if hasIdentity {
			ancestors[key] = true
			defer delete(ancestors, key)
		}
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sortStrings(keys)
		if err := writeArgument(w, 5, uint64(len(keys))); err != nil {
			return err
		}
		for _, key := range keys {
			if err := encodeText(w, key, options); err != nil {
				return err
			}
			if err := encodeValue(w, typed[key], options, depth+1, ancestors); err != nil {
				return err
			}
		}
		return nil
	default:
		return cborErrorf("Unsupported CBOR value type: %T", value)
	}
}

// EncodeCbor encodes the protocol's strict, definite-length RFC 8949 subset.
func EncodeCbor(value any, options *CborOptions) ([]byte, error) {
	resolved, err := ResolveOptions(options)
	if err != nil {
		return nil, err
	}
	writer := newCborWriter(resolved.MaxByteLength)
	if err := encodeValue(writer, value, resolved, 0, map[cycleKey]bool{}); err != nil {
		return nil, err
	}
	return writer.buffer, nil
}

// cborReader decodes with the same limits.
type cborReader struct {
	bytes   []byte
	offset  int
	options ResolvedCborOptions
}

func (r *cborReader) decode() (any, error) {
	value, err := r.readItem(0)
	if err != nil {
		return nil, err
	}
	if r.offset != len(r.bytes) {
		return nil, cborErrorf("CBOR payload contains trailing data")
	}
	return value, nil
}

func (r *cborReader) readItem(depth int64) (any, error) {
	if depth > r.options.MaxDepth {
		return nil, cborErrorf("CBOR nesting depth exceeds configured limit of %d", r.options.MaxDepth)
	}
	initial, err := r.readByte()
	if err != nil {
		return nil, err
	}
	majorType := initial >> 5
	additional := initial & 0x1f

	switch majorType {
	case 0:
		// readArgument is capped at the safe-integer range, so int64 is lossless.
		argument, err := r.readArgument(additional)
		if err != nil {
			return nil, err
		}
		return int64(argument), nil
	case 1:
		argument, err := r.readArgument(additional)
		if err != nil {
			return nil, err
		}
		value := -1 - int64(argument)
		if value < math.MinInt64/2 {
			return nil, cborErrorf("Decoded CBOR integer is outside the safe range")
		}
		return value, nil
	case 2:
		length, err := r.readLength(additional, "byte string", r.options.MaxByteLength)
		if err != nil {
			return nil, err
		}
		bytes, err := r.readBytes(int(length))
		if err != nil {
			return nil, err
		}
		out := make([]byte, len(bytes))
		copy(out, bytes)
		return out, nil
	case 3:
		length, err := r.readLength(additional, "text string", r.options.MaxByteLength)
		if err != nil {
			return nil, err
		}
		bytes, err := r.readBytes(int(length))
		if err != nil {
			return nil, err
		}
		if !utf8.Valid(bytes) {
			return nil, cborErrorf("CBOR text string contains invalid UTF-8")
		}
		return string(bytes), nil
	case 4:
		length, err := r.readLength(additional, "array", r.options.MaxContainerLength)
		if err != nil {
			return nil, err
		}
		result := make([]any, 0, length)
		for i := int64(0); i < length; i++ {
			item, err := r.readItem(depth + 1)
			if err != nil {
				return nil, err
			}
			result = append(result, item)
		}
		return result, nil
	case 5:
		length, err := r.readLength(additional, "map", r.options.MaxContainerLength)
		if err != nil {
			return nil, err
		}
		result := make(map[string]any, length)
		for i := int64(0); i < length; i++ {
			key, err := r.readItem(depth + 1)
			if err != nil {
				return nil, err
			}
			keyString, ok := key.(string)
			if !ok {
				return nil, cborErrorf("CBOR map keys must be strings")
			}
			if _, duplicate := result[keyString]; duplicate {
				return nil, cborErrorf("CBOR map contains a duplicate key")
			}
			value, err := r.readItem(depth + 1)
			if err != nil {
				return nil, err
			}
			result[keyString] = value
		}
		return result, nil
	case 6:
		return nil, cborErrorf("CBOR tags are not supported")
	case 7:
		return r.readSimple(additional)
	default:
		return nil, cborErrorf("Malformed CBOR major type")
	}
}

func (r *cborReader) readSimple(additional byte) (any, error) {
	switch additional {
	case 20:
		return false, nil
	case 21:
		return true, nil
	case 22:
		return nil, nil
	case 27:
		bytes, err := r.readBytes(8)
		if err != nil {
			return nil, err
		}
		value := math.Float64frombits(binary.BigEndian.Uint64(bytes))
		if math.IsInf(value, 0) || math.IsNaN(value) {
			return nil, cborErrorf("Decoded CBOR number must be finite")
		}
		if value == math.Trunc(value) && (value > math.MaxInt64 || value < math.MinInt64) {
			return nil, cborErrorf("Decoded CBOR integer is outside the safe range")
		}
		return value, nil
	case 31:
		return nil, cborErrorf("CBOR break marker is not supported")
	default:
		return nil, cborErrorf("Unsupported CBOR simple value or floating-point width")
	}
}

func (r *cborReader) readLength(additional byte, kind string, limit int64) (int64, error) {
	if additional == 31 {
		return 0, cborErrorf("Indefinite-length CBOR %ss are not supported", kind)
	}
	length, err := r.readArgument(additional)
	if err != nil {
		return 0, err
	}
	if int64(length) > limit {
		return 0, cborErrorf("CBOR %s length exceeds configured limit of %d", kind, limit)
	}
	return int64(length), nil
}

func (r *cborReader) readArgument(additional byte) (uint64, error) {
	if additional < 24 {
		return uint64(additional), nil
	}
	switch additional {
	case 24:
		value, err := r.readByte()
		return uint64(value), err
	case 25:
		bytes, err := r.readBytes(2)
		if err != nil {
			return 0, err
		}
		return uint64(bytes[0])*0x100 + uint64(bytes[1]), nil
	case 26:
		bytes, err := r.readBytes(4)
		if err != nil {
			return 0, err
		}
		return uint64(bytes[0])*0x1_000_000 + uint64(bytes[1])*0x1_0000 + uint64(bytes[2])*0x100 + uint64(bytes[3]), nil
	case 27:
		high, err := r.readArgument(26)
		if err != nil {
			return 0, err
		}
		low, err := r.readArgument(26)
		if err != nil {
			return 0, err
		}
		if high > 0x1f_ffff {
			return 0, cborErrorf("Decoded CBOR integer or length is outside the safe range")
		}
		return high*uint32Base + low, nil
	case 31:
		return 0, cborErrorf("Indefinite-length CBOR items are not supported")
	default:
		return 0, cborErrorf("Malformed CBOR additional information")
	}
}

func (r *cborReader) readByte() (byte, error) {
	if r.offset >= len(r.bytes) {
		return 0, cborErrorf("Truncated CBOR payload")
	}
	value := r.bytes[r.offset]
	r.offset++
	return value, nil
}

func (r *cborReader) readBytes(length int) ([]byte, error) {
	if length > len(r.bytes)-r.offset {
		return nil, cborErrorf("Truncated CBOR payload")
	}
	value := r.bytes[r.offset : r.offset+length]
	r.offset += length
	return value, nil
}

// DecodeCbor decodes exactly one item from the strict RFC 8949 subset.
func DecodeCbor(bytes []byte, options *CborOptions) (any, error) {
	resolved, err := ResolveOptions(options)
	if err != nil {
		return nil, err
	}
	if int64(len(bytes)) > resolved.MaxByteLength {
		return nil, cborErrorf("CBOR byte length exceeds configured limit of %d", resolved.MaxByteLength)
	}
	return (&cborReader{bytes: bytes, options: resolved}).decode()
}

func sortStrings(list []string) {
	for i := 1; i < len(list); i++ {
		for j := i; j > 0 && list[j] < list[j-1]; j-- {
			list[j], list[j-1] = list[j-1], list[j]
		}
	}
}
