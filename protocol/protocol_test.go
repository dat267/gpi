package protocol

import (
	"bytes"
	"encoding/hex"
	"math"
	"strings"
	"testing"
)

// Protocol tests keyed to upstream (protocol/src): framing, the strict CBOR
// subset, message validation, and the codec round trip.

func TestEncodeFrame(t *testing.T) {
	frame, err := EncodeFrame([]byte{0xaa, 0xbb})
	if err != nil {
		t.Fatal(err)
	}
	want := "00000002aabb"
	if hex.EncodeToString(frame) != want {
		t.Fatalf("frame = %x; want %s", frame, want)
	}
	// Empty payload: header only.
	frame, _ = EncodeFrame(nil)
	if hex.EncodeToString(frame) != "00000000" {
		t.Fatalf("empty frame = %x", frame)
	}
}

func TestFrameDecoderChunkedDelivery(t *testing.T) {
	decoder, err := NewFrameDecoder(nil)
	if err != nil {
		t.Fatal(err)
	}
	// Two frames fed byte-by-byte.
	first, _ := EncodeFrame([]byte("one"))
	second, _ := EncodeFrame([]byte("two"))
	stream := append(append([]byte{}, first...), second...)

	var collected [][]byte
	for i := 0; i < len(stream); i++ {
		frames, err := decoder.Push(stream[i : i+1])
		if err != nil {
			t.Fatal(err)
		}
		collected = append(collected, frames...)
	}
	if err := decoder.End(); err != nil {
		t.Fatal(err)
	}
	if len(collected) != 2 || string(collected[0]) != "one" || string(collected[1]) != "two" {
		t.Fatalf("collected = %q", collected)
	}
}

func TestFrameDecoderLimitsAndErrors(t *testing.T) {
	maxLength := int64(4)
	decoder, _ := NewFrameDecoder(&FrameDecoderOptions{MaxFrameLength: &maxLength})
	oversized, _ := EncodeFrame([]byte("toolong"))
	if _, err := decoder.Push(oversized); err == nil || !strings.Contains(err.Error(), "exceeds configured limit of 4") {
		t.Fatalf("err = %v", err)
	}
	// A failed decoder stays failed.
	if _, err := decoder.Push([]byte{0}); err == nil || !strings.Contains(err.Error(), "has failed") {
		t.Fatalf("err = %v", err)
	}

	// Truncated frame at end.
	truncated, _ := NewFrameDecoder(nil)
	frame, _ := EncodeFrame([]byte("hello"))
	if _, err := truncated.Push(frame[:5]); err != nil {
		t.Fatal(err)
	}
	if err := truncated.End(); err == nil || !strings.Contains(err.Error(), "Truncated frame") {
		t.Fatalf("err = %v", err)
	}

	// Zero-length frames are delivered.
	zero, _ := NewFrameDecoder(nil)
	frames, err := zero.Push([]byte{0, 0, 0, 0})
	if err != nil || len(frames) != 1 || len(frames[0]) != 0 {
		t.Fatalf("frames = %v, %v", frames, err)
	}
}

func TestCborRoundTrip(t *testing.T) {
	value := map[string]any{
		"type":     "request",
		"id":       "req-1",
		"number":   int64(42),
		"negative": int64(-7),
		"float":    1.5,
		"flag":     true,
		"nothing":  nil,
		"list":     []any{int64(1), "two"},
		"nested":   map[string]any{"inner": "value"},
	}
	encoded, err := EncodeCbor(value, nil)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeCbor(encoded, nil)
	if err != nil {
		t.Fatal(err)
	}
	roundTrip, ok := decoded.(map[string]any)
	if !ok {
		t.Fatalf("decoded = %T", decoded)
	}
	if roundTrip["id"] != "req-1" || roundTrip["number"] != int64(42) || roundTrip["negative"] != int64(-7) {
		t.Fatalf("decoded = %v", roundTrip)
	}
	if roundTrip["float"] != 1.5 || roundTrip["flag"] != true || roundTrip["nothing"] != nil {
		t.Fatalf("decoded = %v", roundTrip)
	}
	list, _ := roundTrip["list"].([]any)
	if len(list) != 2 || list[1] != "two" {
		t.Fatalf("list = %v", roundTrip["list"])
	}

	// Canonical key order: the encoder sorts map keys, so identical values
	// encode identically regardless of insertion order.
	reordered, _ := EncodeCbor(map[string]any{
		"nested": map[string]any{"inner": "value"}, "list": []any{int64(1), "two"},
		"nothing": nil, "flag": true, "float": 1.5, "negative": int64(-7),
		"number": int64(42), "id": "req-1", "type": "request",
	}, nil)
	if !bytes.Equal(encoded, reordered) {
		t.Fatalf("canonical encoding differs:\n%x\n%x", encoded, reordered)
	}
}

func TestCborHeaderEncodings(t *testing.T) {
	// Small ints use the short form; 24..255 uses one argument byte.
	encoded, _ := EncodeCbor(int64(10), nil)
	if !bytes.Equal(encoded, []byte{0x0a}) {
		t.Fatalf("small int = %x", encoded)
	}
	encoded, _ = EncodeCbor(int64(200), nil)
	if !bytes.Equal(encoded, []byte{0x18, 0xc8}) {
		t.Fatalf("u8 int = %x", encoded)
	}
	encoded, _ = EncodeCbor(int64(70000), nil)
	if !bytes.Equal(encoded, []byte{0x1a, 0x00, 0x01, 0x11, 0x70}) {
		t.Fatalf("u32 int = %x", encoded)
	}
	// Text strings: major type 3 with the byte length.
	encoded, _ = EncodeCbor("hi", nil)
	if !bytes.Equal(encoded, []byte{0x62, 'h', 'i'}) {
		t.Fatalf("text = %x", encoded)
	}
	// Byte strings: major type 2.
	encoded, _ = EncodeCbor([]byte{0xaa}, nil)
	if !bytes.Equal(encoded, []byte{0x41, 0xaa}) {
		t.Fatalf("bytes = %x", encoded)
	}
}

func TestCborDecoderRejections(t *testing.T) {
	// Trailing data.
	if _, err := DecodeCbor([]byte{0x01, 0x01}, nil); err == nil || !strings.Contains(err.Error(), "trailing data") {
		t.Fatalf("err = %v", err)
	}
	// Indefinite length.
	if _, err := DecodeCbor([]byte{0x5f}, nil); err == nil || !strings.Contains(err.Error(), "Indefinite-length") {
		t.Fatalf("err = %v", err)
	}
	// Tags unsupported.
	if _, err := DecodeCbor([]byte{0xc0, 0x01}, nil); err == nil || !strings.Contains(err.Error(), "tags are not supported") {
		t.Fatalf("err = %v", err)
	}
	// Break marker.
	if _, err := DecodeCbor([]byte{0xff}, nil); err == nil || !strings.Contains(err.Error(), "break marker") {
		t.Fatalf("err = %v", err)
	}
	// Truncated payload.
	if _, err := DecodeCbor([]byte{0x62, 'h'}, nil); err == nil || !strings.Contains(err.Error(), "Truncated") {
		t.Fatalf("err = %v", err)
	}
	// Non-string map key.
	if _, err := DecodeCbor([]byte{0xa1, 0x01, 0x01}, nil); err == nil || !strings.Contains(err.Error(), "keys must be strings") {
		t.Fatalf("err = %v", err)
	}
	// Duplicate map key.
	if _, err := DecodeCbor([]byte{0xa2, 0x61, 'a', 0x01, 0x61, 'a', 0x02}, nil); err == nil || !strings.Contains(err.Error(), "duplicate key") {
		t.Fatalf("err = %v", err)
	}
	// Invalid UTF-8 in a text string.
	if _, err := DecodeCbor([]byte{0x61, 0xff}, nil); err == nil || !strings.Contains(err.Error(), "invalid UTF-8") {
		t.Fatalf("err = %v", err)
	}
	// Depth limit.
	depth := int64(2)
	if _, err := DecodeCbor([]byte{0x81, 0x81, 0x81, 0x01}, &CborOptions{MaxDepth: &depth}); err == nil ||
		!strings.Contains(err.Error(), "nesting depth") {
		t.Fatalf("err = %v", err)
	}
	// Byte-length limit caps string lengths.
	limit := int64(2)
	if _, err := DecodeCbor([]byte{0x63, 'a', 'b', 'c'}, &CborOptions{MaxByteLength: &limit}); err == nil {
		t.Fatal("string byte length limit should reject")
	}
	// And the whole input.
	if _, err := DecodeCbor([]byte{0x18, 0x2a}, &CborOptions{MaxByteLength: intPtr(1)}); err == nil {
		t.Fatal("input byte length limit should reject")
	}
}

func TestEncodeCborRejections(t *testing.T) {
	nan := math.NaN()
	if _, err := EncodeCbor(nan, nil); err == nil || !strings.Contains(err.Error(), "finite") {
		t.Fatalf("err = %v", err)
	}
	if _, err := EncodeCbor(struct{ A int }{1}, nil); err == nil || !strings.Contains(err.Error(), "Unsupported CBOR value type") {
		t.Fatalf("err = %v", err)
	}
	// Cycle detection (a map that contains itself).
	cyclic := map[string]any{}
	cyclic["self"] = cyclic
	if _, err := EncodeCbor(map[string]any{"cycle": cyclic}, nil); err == nil ||
		!strings.Contains(err.Error(), "cycles") {
		t.Fatal("cycles must be rejected")
	}
	// Container length limit.
	containerMax := int64(1)
	if _, err := EncodeCbor([]any{int64(1), int64(2)}, &CborOptions{MaxContainerLength: &containerMax}); err == nil ||
		!strings.Contains(err.Error(), "array length exceeds") {
		t.Fatalf("err = %v", err)
	}
}

func TestParseClientMessages(t *testing.T) {
	// Hello.
	message, err := ParseClientMessage(map[string]any{"type": "hello", "version": int64(8)})
	if err != nil || message.Hello == nil || message.Hello.Version != 8 {
		t.Fatalf("hello = %+v, %v", message, err)
	}
	// Unknown properties are rejected (additionalProperties: false).
	if _, err := ParseClientMessage(map[string]any{"type": "hello", "version": int64(8), "extra": true}); err == nil {
		t.Fatal("unknown property must be rejected")
	}
	// Negative version.
	if _, err := ParseClientMessage(map[string]any{"type": "hello", "version": int64(-1)}); err == nil {
		t.Fatal("negative version must be rejected")
	}

	// Server target request.
	message, err = ParseClientMessage(map[string]any{
		"type": "request", "id": "req-1",
		"target": map[string]any{"serverId": "123e4567-e89b-42d3-a456-426614174000"},
		"call":   "sessions.list",
	})
	if err != nil {
		t.Fatal(err)
	}
	if message.Request == nil || message.Request.Call != "sessions.list" || message.Request.Target.IsSessionTarget() {
		t.Fatalf("request = %+v", message.Request)
	}

	// Session target requires BOTH ids.
	if _, err := ParseClientMessage(map[string]any{
		"type": "request", "id": "req-2",
		"target": map[string]any{"serverId": "123e4567-e89b-42d3-a456-426614174000", "sessionId": "s-1"},
	}); err == nil {
		t.Fatal("partial session target must be rejected")
	}
	message, err = ParseClientMessage(map[string]any{
		"type": "cancel", "id": "req-3",
		"target": map[string]any{
			"serverId":  "123e4567-e89b-42d3-a456-426614174000",
			"sessionId": "s-1", "attachmentId": "a-1",
		},
	})
	if err != nil || !message.Cancel.Target.IsSessionTarget() {
		t.Fatalf("cancel = %+v, %v", message, err)
	}

	// Bad server id shape (not uuid v4).
	if _, err := ParseClientMessage(map[string]any{
		"type": "request", "id": "req-4", "target": map[string]any{"serverId": "not-a-uuid"},
	}); err == nil {
		t.Fatal("invalid serverId must be rejected")
	}
	// Empty id.
	if _, err := ParseClientMessage(map[string]any{
		"type": "request", "id": "", "target": map[string]any{"serverId": "123e4567-e89b-42d3-a456-426614174000"},
	}); err == nil {
		t.Fatal("empty id must be rejected")
	}
	// Unknown type.
	if _, err := ParseClientMessage(map[string]any{"type": "nope"}); err == nil {
		t.Fatal("unknown type must be rejected")
	}
}

func TestParseServerMessages(t *testing.T) {
	// Hello carries the current version only.
	if _, err := ParseServerMessage(map[string]any{
		"type": "hello", "version": int64(ProtocolVersion), "serverId": "123e4567-e89b-42d3-a456-426614174000",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := ParseServerMessage(map[string]any{
		"type": "hello", "version": int64(7), "serverId": "123e4567-e89b-42d3-a456-426614174000",
	}); err == nil {
		t.Fatal("stale version must be rejected")
	}
	// hello_error.
	message, err := ParseServerMessage(map[string]any{
		"type": "hello_error", "error": map[string]any{"code": "unsupported_version", "message": "bad"},
	})
	if err != nil || message.HelloError.Error.Code != "unsupported_version" {
		t.Fatalf("hello_error = %+v, %v", message, err)
	}
	// Response ok with optional result.
	message, err = ParseServerMessage(map[string]any{"type": "response", "id": "r1", "ok": true, "result": "done"})
	if err != nil || !message.Response.OK || message.Response.Result != "done" {
		t.Fatalf("response = %+v, %v", message, err)
	}
	// Response failure requires an error.
	if _, err := ParseServerMessage(map[string]any{"type": "response", "id": "r1", "ok": false}); err == nil {
		t.Fatal("failed response requires an error")
	}
	if _, err := ParseServerMessage(map[string]any{
		"type": "response", "id": "r1", "ok": false,
		"error": map[string]any{"code": "c", "message": "m"},
	}); err != nil {
		t.Fatal(err)
	}
	// service_update.
	message, err = ParseServerMessage(map[string]any{
		"type": "service_update", "subscriptionId": "sub-1", "update": map[string]any{"kind": "x"},
	})
	if err != nil || message.ServiceUpdate.SubscriptionID != "sub-1" {
		t.Fatalf("service_update = %+v, %v", message, err)
	}
	// attachment: null clears the route; a target sets it.
	message, err = ParseServerMessage(map[string]any{"type": "attachment", "attachment": nil})
	if err != nil || message.Attachment.Attachment != nil {
		t.Fatalf("null attachment = %+v, %v", message, err)
	}
	message, err = ParseServerMessage(map[string]any{
		"type": "attachment",
		"attachment": map[string]any{
			"serverId": "123e4567-e89b-42d3-a456-426614174000", "sessionId": "s", "attachmentId": "a",
		},
	})
	if err != nil || message.Attachment.Attachment == nil || !message.Attachment.Attachment.IsSessionTarget() {
		t.Fatalf("attachment = %+v, %v", message, err)
	}
}

func TestCodecFrameRoundTrip(t *testing.T) {
	// Client request round trip through CBOR + framing.
	original := &ClientMessage{Type: ClientMessageRequest, Request: &RequestEnvelope{
		ID:     "req-1",
		Target: RpcTarget{ServerID: "123e4567-e89b-42d3-a456-426614174000"},
		Call:   map[string]any{"method": "sessions.list", "count": int64(3)},
	}}
	frame, err := EncodeClientMessage(original, nil)
	if err != nil {
		t.Fatal(err)
	}

	decoder, _ := NewClientMessageDecoder(nil)
	// Feed in two chunks to exercise the incremental decoder.
	messages, err := decoder.Push(frame[:3])
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 0 {
		t.Fatalf("partial chunk produced %d messages", len(messages))
	}
	messages, err = decoder.Push(frame[3:])
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 {
		t.Fatalf("messages = %d", len(messages))
	}
	decoded := messages[0]
	if decoded.Request.ID != "req-1" || decoded.Request.Target.ServerID != original.Request.Target.ServerID {
		t.Fatalf("decoded = %+v", decoded)
	}
	call, _ := decoded.Request.Call.(map[string]any)
	if call["method"] != "sessions.list" || call["count"] != int64(3) {
		t.Fatalf("call = %v", decoded.Request.Call)
	}
	if err := decoder.End(); err != nil {
		t.Fatal(err)
	}
}

func TestCodecServerRoundTrip(t *testing.T) {
	original := &ServerMessage{Type: ServerMessageResponse, Response: &ResponseEnvelope{
		ID: "req-1", OK: true, Result: map[string]any{"sessions": []any{"a", "b"}},
	}}
	frame, err := EncodeServerMessage(original, nil)
	if err != nil {
		t.Fatal(err)
	}
	decoder, _ := NewServerMessageDecoder(nil)
	messages, err := decoder.Push(frame)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 || !messages[0].Response.OK {
		t.Fatalf("messages = %+v", messages)
	}
	result, _ := messages[0].Response.Result.(map[string]any)
	sessions, _ := result["sessions"].([]any)
	if len(sessions) != 2 || sessions[0] != "a" {
		t.Fatalf("result = %v", messages[0].Response.Result)
	}
}

func TestCodecRejectsInvalidFrames(t *testing.T) {
	// A well-framed but invalid message (unknown type).
	payload, _ := EncodeCbor(map[string]any{"type": "bogus"}, nil)
	frame, _ := EncodeFrame(payload)
	decoder, _ := NewClientMessageDecoder(nil)
	if _, err := decoder.Push(frame); err == nil || !strings.Contains(err.Error(), "unknown type") {
		t.Fatalf("err = %v", err)
	}
	// The decoder stays failed afterwards.
	if _, err := decoder.Push(frame); err == nil || !strings.Contains(err.Error(), "has failed") {
		t.Fatalf("err = %v", err)
	}

	// Non-JSON values (byte strings) are rejected on encode.
	if _, err := EncodeClientMessage(&ClientMessage{Type: ClientMessageRequest, Request: &RequestEnvelope{
		ID:     "r",
		Target: RpcTarget{ServerID: "123e4567-e89b-42d3-a456-426614174000"},
		Call:   []byte{0x01},
	}}, nil); err == nil {
		t.Fatal("byte-string payloads must be rejected")
	}

	// Frame limit on encode.
	maxLength := int64(4)
	if _, err := EncodeServerMessage(&ServerMessage{Type: ServerMessageServiceUpdate,
		ServiceUpdate: &ServiceEventEnvelope{SubscriptionID: "sub", Update: strings.Repeat("x", 100)}},
		&FrameDecoderOptions{MaxFrameLength: &maxLength}); err == nil || !strings.Contains(err.Error(), "exceeds configured limit") {
		t.Fatalf("err = %v", err)
	}
}

func TestBoundedErrorMessage(t *testing.T) {
	long := &FrameError{Message: strings.Repeat("x", 600)}
	got := BoundedErrorMessage(long)
	if len(got) != 500 || !strings.HasSuffix(got, "...") {
		t.Fatalf("bounded = %d chars", len(got))
	}
	if BoundedErrorMessage(nil) != "Unknown codec error" {
		t.Fatal("nil error message")
	}
}

func intPtr(n int64) *int64 { return &n }
