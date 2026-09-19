package protocol

import (
	"encoding/binary"
	"fmt"
)

// Port of src/framing.ts: unsigned 32-bit big-endian length-prefixed frames.

const (
	frameHeaderLength = 4
	maxUint32Value    = 0xffff_ffff
	payloadBlockSize  = 64 * 1024
)

// DefaultMaxFrameLength is the default upper bound for one framed payload.
const DefaultMaxFrameLength = 16 * 1024 * 1024

// FrameDecoderOptions bound a frame decoder.
type FrameDecoderOptions struct {
	// MaxFrameLength is the maximum payload length. Nil uses the default; an
	// explicit 0 rejects every non-empty frame.
	MaxFrameLength *int64
}

// FrameError is the framing error type.
type FrameError struct{ Message string }

func (e *FrameError) Error() string { return e.Message }

func frameErrorf(format string, args ...any) *FrameError {
	return &FrameError{Message: fmt.Sprintf(format, args...)}
}

func resolveMaxFrameLength(options *FrameDecoderOptions) (int, error) {
	value := DefaultMaxFrameLength
	if options != nil && options.MaxFrameLength != nil {
		value = int(*options.MaxFrameLength)
	}
	if value < 0 || int64(value) > maxUint32Value {
		return 0, fmt.Errorf("maxFrameLength must be an integer between 0 and %d", maxUint32Value)
	}
	return value, nil
}

// EncodeFrame prefixes a payload with its unsigned 32-bit big-endian length.
func EncodeFrame(payload []byte) ([]byte, error) {
	if len(payload) > maxUint32Value {
		return nil, fmt.Errorf("Frame payload exceeds the unsigned 32-bit length limit")
	}
	frame := make([]byte, frameHeaderLength+len(payload))
	binary.BigEndian.PutUint32(frame[:4], uint32(len(payload)))
	copy(frame[4:], payload)
	return frame, nil
}

type decoderState int

const (
	decoderOpen decoderState = iota
	decoderEnded
	decoderFailed
)

// FrameDecoder incrementally splits arbitrary byte chunks into
// length-prefixed payloads.
type FrameDecoder struct {
	header         [frameHeaderLength]byte
	headerLength   int
	maxFrameLength int
	payload        []byte
	expectedLength int
	hasExpected    bool
	state          decoderState
}

// NewFrameDecoder builds a decoder.
func NewFrameDecoder(options *FrameDecoderOptions) (*FrameDecoder, error) {
	maxFrameLength, err := resolveMaxFrameLength(options)
	if err != nil {
		return nil, err
	}
	return &FrameDecoder{maxFrameLength: maxFrameLength}, nil
}

// MaxFrameLength returns the configured limit (used by the codec).
func (d *FrameDecoder) MaxFrameLength() int { return d.maxFrameLength }

// Push feeds a chunk and returns any complete payloads.
func (d *FrameDecoder) Push(chunk []byte) ([][]byte, error) {
	if d.state == decoderEnded {
		return nil, frameErrorf("Frame decoder has ended")
	}
	if d.state == decoderFailed {
		return nil, frameErrorf("Frame decoder has failed")
	}

	var frames [][]byte
	chunkOffset := 0
	for chunkOffset < len(chunk) {
		if !d.hasExpected {
			headerBytes := min(frameHeaderLength-d.headerLength, len(chunk)-chunkOffset)
			copy(d.header[d.headerLength:], chunk[chunkOffset:chunkOffset+headerBytes])
			d.headerLength += headerBytes
			chunkOffset += headerBytes
			if d.headerLength < frameHeaderLength {
				continue
			}

			frameLength := int(binary.BigEndian.Uint32(d.header[:]))
			d.headerLength = 0
			if frameLength > d.maxFrameLength {
				return nil, d.fail(frameErrorf("Frame length %d exceeds configured limit of %d", frameLength, d.maxFrameLength))
			}
			if frameLength == 0 {
				frames = append(frames, []byte{})
				continue
			}
			d.expectedLength = frameLength
			d.hasExpected = true
			d.payload = make([]byte, 0, min(frameLength, payloadBlockSize))
		}

		payloadBytes := min(d.expectedLength-len(d.payload), len(chunk)-chunkOffset)
		d.payload = append(d.payload, chunk[chunkOffset:chunkOffset+payloadBytes]...)
		chunkOffset += payloadBytes

		if len(d.payload) == d.expectedLength {
			frames = append(frames, d.payload)
			d.payload = nil
			d.expectedLength = 0
			d.hasExpected = false
		}
	}
	return frames, nil
}

// End verifies the stream did not end mid-frame.
func (d *FrameDecoder) End() error {
	if d.state == decoderEnded {
		return frameErrorf("Frame decoder has ended")
	}
	if d.state == decoderFailed {
		return frameErrorf("Frame decoder has failed")
	}
	if d.headerLength != 0 || d.hasExpected {
		return d.fail(frameErrorf("Truncated frame at end of stream"))
	}
	d.state = decoderEnded
	return nil
}

func (d *FrameDecoder) fail(err error) error {
	d.state = decoderFailed
	d.headerLength = 0
	d.payload = nil
	d.expectedLength = 0
	d.hasExpected = false
	return err
}
