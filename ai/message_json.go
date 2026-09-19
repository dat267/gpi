package ai

import (
	"encoding/json"
	"fmt"
)

// ContentList is a JSON array of discriminated content blocks.
type ContentList []Content

// MarshalJSON encodes each block with its `type` discriminator first,
// matching upstream key order.
func (l ContentList) MarshalJSON() ([]byte, error) {
	out := make([]json.RawMessage, 0, len(l))
	for _, c := range l {
		enc, err := marshalContentBlock(c)
		if err != nil {
			return nil, err
		}
		out = append(out, enc)
	}
	return MarshalJSON(out)
}

// UnmarshalJSON decodes blocks by their `type` discriminator.
func (l *ContentList) UnmarshalJSON(data []byte) error {
	var raw []json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	blocks := make(ContentList, 0, len(raw))
	for _, r := range raw {
		b, err := unmarshalContentBlock(r)
		if err != nil {
			return err
		}
		blocks = append(blocks, b)
	}
	*l = blocks
	return nil
}

// UserContentList is a JSON array of text/image content blocks.
type UserContentList []UserContent

// MarshalJSON encodes each block with its `type` discriminator.
func (l UserContentList) MarshalJSON() ([]byte, error) {
	out := make([]json.RawMessage, 0, len(l))
	for _, c := range l {
		enc, err := marshalContentBlock(c)
		if err != nil {
			return nil, err
		}
		out = append(out, enc)
	}
	return MarshalJSON(out)
}

// UnmarshalJSON decodes blocks by their `type` discriminator, rejecting
// thinking/toolCall blocks that are not valid user content.
func (l *UserContentList) UnmarshalJSON(data []byte) error {
	var raw []json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	blocks := make(UserContentList, 0, len(raw))
	for _, r := range raw {
		b, err := unmarshalContentBlock(r)
		if err != nil {
			return err
		}
		uc, ok := b.(UserContent)
		if !ok {
			return fmt.Errorf("ai: %q content is not valid user content", b.contentKind())
		}
		blocks = append(blocks, uc)
	}
	*l = blocks
	return nil
}

// marshalContentBlock encodes one block with its type discriminator as the
// first key, matching upstream key order.
func marshalContentBlock(c Content) (json.RawMessage, error) {
	var kind ContentKind
	switch v := c.(type) {
	case TextContent:
		kind = v.contentKind()
	case *TextContent:
		kind = v.contentKind()
	case ThinkingContent:
		kind = v.contentKind()
	case *ThinkingContent:
		kind = v.contentKind()
	case ImageContent:
		kind = v.contentKind()
	case *ImageContent:
		kind = v.contentKind()
	case ToolCall:
		kind = v.contentKind()
	case *ToolCall:
		kind = v.contentKind()
	default:
		return nil, fmt.Errorf("ai: unknown content block %T", c)
	}
	enc, err := MarshalJSON(c)
	if err != nil {
		return nil, err
	}
	// Splice the discriminator in as the first key, preserving the struct's
	// field order for the rest (Go maps would sort keys, breaking parity).
	kindEnc, err := MarshalJSON(kind)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 0, len(enc)+len(kindEnc)+8)
	out = append(out, '{')
	out = append(out, `"type":`...)
	out = append(out, kindEnc...)
	if len(enc) > 2 { // non-empty object body
		out = append(out, ',')
		out = append(out, enc[1:]...)
	} else {
		out = append(out, '}')
	}
	return out, nil
}

// unmarshalContentBlock decodes one block by its type discriminator.
func unmarshalContentBlock(data json.RawMessage) (Content, error) {
	var probe struct {
		Type ContentKind `json:"type"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return nil, err
	}
	switch probe.Type {
	case KindText:
		var b TextContent
		if err := json.Unmarshal(data, &b); err != nil {
			return nil, err
		}
		return b, nil
	case KindThinking:
		var b ThinkingContent
		if err := json.Unmarshal(data, &b); err != nil {
			return nil, err
		}
		return b, nil
	case KindImage:
		var b ImageContent
		if err := json.Unmarshal(data, &b); err != nil {
			return nil, err
		}
		return b, nil
	case KindToolCall:
		var b ToolCall
		if err := json.Unmarshal(data, &b); err != nil {
			return nil, err
		}
		return b, nil
	default:
		return nil, fmt.Errorf("ai: unknown content block type %q", probe.Type)
	}
}

// MarshalMessage encodes one message with upstream's role discrimination.
func MarshalMessage(m Message) (json.RawMessage, error) {
	enc, err := MarshalJSON(m)
	if err != nil {
		return nil, err
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(enc, &probe); err != nil {
		return nil, err
	}
	probe["role"], err = MarshalJSON(m.messageRole())
	if err != nil {
		return nil, err
	}
	return MarshalJSON(probe)
}

// UnmarshalMessage decodes one message by its role discriminator.
func UnmarshalMessage(data json.RawMessage) (Message, error) {
	var probe struct {
		Role Role `json:"role"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return nil, err
	}
	switch probe.Role {
	case RoleSystem:
		var m SystemMessage
		if err := json.Unmarshal(data, &m); err != nil {
			return nil, err
		}
		return &m, nil
	case RoleUser:
		var m UserMessage
		if err := json.Unmarshal(data, &m); err != nil {
			return nil, err
		}
		return &m, nil
	case RoleAssistant:
		var m AssistantMessage
		if err := json.Unmarshal(data, &m); err != nil {
			return nil, err
		}
		return &m, nil
	case RoleToolResult:
		var m ToolResultMessage
		if err := json.Unmarshal(data, &m); err != nil {
			return nil, err
		}
		return &m, nil
	default:
		// Unknown roles persist as custom messages (upstream AgentMessage
		// extensibility).
		var m CustomMessage
		if err := json.Unmarshal(data, &m); err != nil {
			return nil, fmt.Errorf("ai: unknown message role %q", probe.Role)
		}
		m.Extra = data
		return &m, nil
	}
}

// MarshalMessages encodes a transcript.
func MarshalMessages(messages []Message) ([]json.RawMessage, error) {
	out := make([]json.RawMessage, 0, len(messages))
	for _, m := range messages {
		enc, err := MarshalMessage(m)
		if err != nil {
			return nil, err
		}
		out = append(out, enc)
	}
	return out, nil
}

// UnmarshalMessages decodes a transcript.
func UnmarshalMessages(data []json.RawMessage) ([]Message, error) {
	out := make([]Message, 0, len(data))
	for _, enc := range data {
		m, err := UnmarshalMessage(enc)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, nil
}
