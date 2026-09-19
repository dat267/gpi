package protocol

import "math"

// Port of src/codec.ts: validate, CBOR-encode, and frame protocol messages;
// decode framed messages incrementally.

// BoundedErrorMessage truncates a codec error message to 500 chars.
func BoundedErrorMessage(err error) string {
	if err == nil {
		return "Unknown codec error"
	}
	message := err.Error()
	if len(message) <= 500 {
		return message
	}
	return message[:497] + "..."
}

// isJSONValue reports whether a decoded value is a JSON value (upstream
// `isJsonValue` from chord): no NaN/Inf floats, no byte strings.
func isJSONValue(value any) bool {
	switch typed := value.(type) {
	case nil, bool, string:
		return true
	case int64, int:
		return true
	case float64:
		return !math.IsNaN(typed) && !math.IsInf(typed, 0)
	case []any:
		for _, item := range typed {
			if !isJSONValue(item) {
				return false
			}
		}
		return true
	case map[string]any:
		for _, item := range typed {
			if !isJSONValue(item) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

// EncodeClientMessage validates and encodes one framed client message.
func EncodeClientMessage(message *ClientMessage, options *FrameDecoderOptions) ([]byte, error) {
	value, err := ClientMessageValue(message)
	if err != nil {
		return nil, err
	}
	return encodeProtocolMessage(value, "client", options)
}

// EncodeServerMessage validates and encodes one framed server message.
func EncodeServerMessage(message *ServerMessage, options *FrameDecoderOptions) ([]byte, error) {
	value, err := ServerMessageValue(message)
	if err != nil {
		return nil, err
	}
	return encodeProtocolMessage(value, "server", options)
}

func encodeProtocolMessage(value map[string]any, kind string, options *FrameDecoderOptions) ([]byte, error) {
	if !isJSONValue(value) {
		return nil, validationErrorf("Invalid %s protocol message", kind)
	}
	maxFrameLength := int64(DefaultMaxFrameLength)
	if options != nil && options.MaxFrameLength != nil {
		maxFrameLength = *options.MaxFrameLength
	}
	payload, err := EncodeCbor(value, &CborOptions{MaxByteLength: &maxFrameLength})
	if err != nil {
		return nil, validationErrorf("Invalid %s protocol frame: %s", kind, BoundedErrorMessage(err))
	}
	frame, err := EncodeFrame(payload)
	if err != nil {
		return nil, validationErrorf("Invalid %s protocol frame: %s", kind, BoundedErrorMessage(err))
	}
	return frame, nil
}

// validatedMessageDecoder frames then validates messages.
type validatedMessageDecoder[T any] struct {
	failed         bool
	frames         *FrameDecoder
	kind           string
	maxFrameLength int64
	parse          func(any) (*T, error)
}

func newValidatedMessageDecoder[T any](kind string, parse func(any) (*T, error), options *FrameDecoderOptions) (*validatedMessageDecoder[T], error) {
	frames, err := NewFrameDecoder(options)
	if err != nil {
		return nil, err
	}
	maxFrameLength := int64(DefaultMaxFrameLength)
	if options != nil && options.MaxFrameLength != nil {
		maxFrameLength = *options.MaxFrameLength
	}
	return &validatedMessageDecoder[T]{
		frames: frames, kind: kind, maxFrameLength: maxFrameLength, parse: parse,
	}, nil
}

func (d *validatedMessageDecoder[T]) push(chunk []byte) ([]*T, error) {
	if d.failed {
		return nil, validationErrorf("%s message decoder has failed", d.kind)
	}
	frames, err := d.frames.Push(chunk)
	if err != nil {
		d.failed = true
		return nil, validationErrorf("Invalid %s protocol frame: %s", d.kind, BoundedErrorMessage(err))
	}
	messages := make([]*T, 0, len(frames))
	for _, frame := range frames {
		value, decodeErr := DecodeCbor(frame, &CborOptions{MaxByteLength: &d.maxFrameLength})
		if decodeErr != nil {
			d.failed = true
			return nil, validationErrorf("Invalid %s protocol frame: %s", d.kind, BoundedErrorMessage(decodeErr))
		}
		message, parseErr := d.parse(value)
		if parseErr != nil {
			d.failed = true
			if _, ok := parseErr.(*ProtocolValidationError); ok {
				return nil, parseErr
			}
			return nil, validationErrorf("Invalid %s protocol frame: %s", d.kind, BoundedErrorMessage(parseErr))
		}
		messages = append(messages, message)
	}
	return messages, nil
}

func (d *validatedMessageDecoder[T]) end() error {
	if d.failed {
		return validationErrorf("%s message decoder has failed", d.kind)
	}
	if err := d.frames.End(); err != nil {
		d.failed = true
		return validationErrorf("Invalid %s protocol framing: %s", d.kind, BoundedErrorMessage(err))
	}
	return nil
}

// ClientMessageDecoder incrementally decodes and validates framed client
// messages.
type ClientMessageDecoder struct {
	inner *validatedMessageDecoder[ClientMessage]
}

// NewClientMessageDecoder builds a client message decoder.
func NewClientMessageDecoder(options *FrameDecoderOptions) (*ClientMessageDecoder, error) {
	inner, err := newValidatedMessageDecoder("client", ParseClientMessage, options)
	if err != nil {
		return nil, err
	}
	return &ClientMessageDecoder{inner: inner}, nil
}

// Push feeds a chunk and returns complete messages.
func (d *ClientMessageDecoder) Push(chunk []byte) ([]*ClientMessage, error) {
	return d.inner.push(chunk)
}

// End verifies the stream did not end mid-frame.
func (d *ClientMessageDecoder) End() error { return d.inner.end() }

// ServerMessageDecoder incrementally decodes and validates framed server
// messages.
type ServerMessageDecoder struct {
	inner *validatedMessageDecoder[ServerMessage]
}

// NewServerMessageDecoder builds a server message decoder.
func NewServerMessageDecoder(options *FrameDecoderOptions) (*ServerMessageDecoder, error) {
	inner, err := newValidatedMessageDecoder("server", ParseServerMessage, options)
	if err != nil {
		return nil, err
	}
	return &ServerMessageDecoder{inner: inner}, nil
}

// Push feeds a chunk and returns complete messages.
func (d *ServerMessageDecoder) Push(chunk []byte) ([]*ServerMessage, error) {
	return d.inner.push(chunk)
}

// End verifies the stream did not end mid-frame.
func (d *ServerMessageDecoder) End() error { return d.inner.end() }
