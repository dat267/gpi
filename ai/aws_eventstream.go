package ai

import (
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"io"
)

// The AWS event-stream binary protocol used by Bedrock ConverseStream. Upstream
// hands the response to the AWS SDK's async iterator; the Go port decodes the
// frames directly (12-byte prelude, headers, payload, 4-byte message CRC).

const (
	awsEventStreamPreludeLength = 12
	awsEventStreamMinMessageLen = 16

	// Header value type ids.
	awsHeaderBoolTrue  = 0
	awsHeaderBoolFalse = 1
	awsHeaderByte      = 2
	awsHeaderShort     = 3
	awsHeaderInt       = 4
	awsHeaderLong      = 5
	awsHeaderByteArray = 6
	awsHeaderString    = 7
	awsHeaderTimestamp = 8
	awsHeaderUUID      = 9
)

// AWSEventStreamMessage is one decoded event-stream message.
type AWSEventStreamMessage struct {
	Headers map[string]any
	Payload []byte
}

// AWSEventStreamError reports a malformed frame.
type AWSEventStreamError struct {
	Message string
}

func (e *AWSEventStreamError) Error() string { return e.Message }

// ReadAWSEventStreamMessage reads one message, returning io.EOF at a clean end.
func ReadAWSEventStreamMessage(reader io.Reader) (*AWSEventStreamMessage, error) {
	prelude := make([]byte, awsEventStreamPreludeLength)
	if _, err := io.ReadFull(reader, prelude); err != nil {
		return nil, err
	}
	totalLength := binary.BigEndian.Uint32(prelude[0:4])
	headersLength := binary.BigEndian.Uint32(prelude[4:8])
	preludeCRC := binary.BigEndian.Uint32(prelude[8:12])
	if actual := crc32.ChecksumIEEE(prelude[:8]); actual != preludeCRC {
		return nil, &AWSEventStreamError{Message: "AWS event stream prelude checksum mismatch"}
	}
	if totalLength < awsEventStreamMinMessageLen || totalLength > 16<<20 {
		return nil, &AWSEventStreamError{Message: fmt.Sprintf("AWS event stream message length %d is invalid", totalLength)}
	}
	if headersLength > totalLength-awsEventStreamMinMessageLen {
		return nil, &AWSEventStreamError{Message: "AWS event stream headers length is invalid"}
	}

	rest := make([]byte, totalLength-awsEventStreamPreludeLength)
	if _, err := io.ReadFull(reader, rest); err != nil {
		if err == io.ErrUnexpectedEOF {
			return nil, io.ErrUnexpectedEOF
		}
		return nil, err
	}
	messageCRC := binary.BigEndian.Uint32(rest[len(rest)-4:])
	if actual := crc32.ChecksumIEEE(append(append([]byte{}, prelude...), rest[:len(rest)-4]...)); actual != messageCRC {
		return nil, &AWSEventStreamError{Message: "AWS event stream message checksum mismatch"}
	}

	headers, err := parseAWSEventStreamHeaders(rest[:headersLength])
	if err != nil {
		return nil, err
	}
	payload := rest[headersLength : len(rest)-4]
	return &AWSEventStreamMessage{Headers: headers, Payload: payload}, nil
}

// parseAWSEventStreamHeaders decodes the header block.
func parseAWSEventStreamHeaders(raw []byte) (map[string]any, error) {
	headers := map[string]any{}
	offset := 0
	for offset < len(raw) {
		nameLength, next, err := readAWSVarint(raw, offset)
		if err != nil {
			return nil, err
		}
		offset = next
		if offset+int(nameLength) > len(raw) {
			return nil, &AWSEventStreamError{Message: "AWS event stream header name is truncated"}
		}
		name := string(raw[offset : offset+int(nameLength)])
		offset += int(nameLength)
		if offset >= len(raw) {
			return nil, &AWSEventStreamError{Message: "AWS event stream header type is truncated"}
		}
		valueType := raw[offset]
		offset++
		value, next, err := parseAWSEventStreamHeaderValue(raw, offset, valueType)
		if err != nil {
			return nil, err
		}
		offset = next
		headers[name] = value
	}
	return headers, nil
}

func parseAWSEventStreamHeaderValue(raw []byte, offset int, valueType byte) (any, int, error) {
	need := func(count int) error {
		if offset+count > len(raw) {
			return &AWSEventStreamError{Message: "AWS event stream header value is truncated"}
		}
		return nil
	}
	switch valueType {
	case awsHeaderBoolTrue:
		return true, offset, nil
	case awsHeaderBoolFalse:
		return false, offset, nil
	case awsHeaderByte:
		if err := need(1); err != nil {
			return nil, 0, err
		}
		return int8(raw[offset]), offset + 1, nil
	case awsHeaderShort:
		if err := need(2); err != nil {
			return nil, 0, err
		}
		return int16(binary.BigEndian.Uint16(raw[offset:])), offset + 2, nil
	case awsHeaderInt:
		if err := need(4); err != nil {
			return nil, 0, err
		}
		return int32(binary.BigEndian.Uint32(raw[offset:])), offset + 4, nil
	case awsHeaderLong, awsHeaderTimestamp:
		if err := need(8); err != nil {
			return nil, 0, err
		}
		return int64(binary.BigEndian.Uint64(raw[offset:])), offset + 8, nil
	case awsHeaderByteArray, awsHeaderString:
		length, next, err := readAWSVarint(raw, offset)
		if err != nil {
			return nil, 0, err
		}
		if next+int(length) > len(raw) {
			return nil, 0, &AWSEventStreamError{Message: "AWS event stream header value is truncated"}
		}
		bytes := raw[next : next+int(length)]
		if valueType == awsHeaderString {
			return string(bytes), next + int(length), nil
		}
		return append([]byte{}, bytes...), next + int(length), nil
	case awsHeaderUUID:
		if err := need(16); err != nil {
			return nil, 0, err
		}
		return raw[offset : offset+16], offset + 16, nil
	default:
		return nil, 0, &AWSEventStreamError{Message: fmt.Sprintf("AWS event stream header type %d is unknown", valueType)}
	}
}

// readAWSVarint decodes the event-stream variable-length integer (a big-endian
// varint with the high bit of the last byte as the terminator).
func readAWSVarint(raw []byte, offset int) (uint64, int, error) {
	var value uint64
	for index := 0; index < 4; index++ {
		if offset+index >= len(raw) {
			return 0, 0, &AWSEventStreamError{Message: "AWS event stream varint is truncated"}
		}
		current := raw[offset+index]
		value = value<<7 | uint64(current&0x7f)
		if current&0x80 != 0 {
			return value, offset + index + 1, nil
		}
	}
	return 0, 0, &AWSEventStreamError{Message: "AWS event stream varint is too long"}
}
