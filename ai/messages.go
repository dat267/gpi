package ai

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
)

// ContentKind discriminates content blocks.
type ContentKind = string

const (
	KindText     ContentKind = "text"
	KindThinking ContentKind = "thinking"
	KindImage    ContentKind = "image"
	KindToolCall ContentKind = "toolCall"
)

// Content is one block of message content: TextContent, ThinkingContent,
// ImageContent, or ToolCall.
type Content interface {
	contentKind() ContentKind
}

// TextSignatureV1 is a versioned OpenAI Responses text signature.
type TextSignatureV1 struct {
	V     int    `json:"v"`
	ID    string `json:"id"`
	Phase string `json:"phase,omitempty"` // "commentary" | "final_answer"
}

// TextContent is a text block.
type TextContent struct {
	Text string `json:"text"`
	// TextSignature carries provider message metadata (legacy id string or
	// TextSignatureV1 JSON), e.g. for OpenAI Responses.
	TextSignature *string `json:"textSignature,omitempty"`
}

func (TextContent) contentKind() ContentKind { return KindText }

// ThinkingContent is a reasoning block.
type ThinkingContent struct {
	Thinking string `json:"thinking"`
	// ThinkingSignature is provider-specific opaque or serialized reasoning
	// replay data.
	ThinkingSignature *string `json:"thinkingSignature,omitempty"`
	// Redacted is true when the thinking content was redacted by safety
	// filters. The opaque encrypted payload is stored in ThinkingSignature
	// so it can be passed back to the API for multi-turn continuity.
	Redacted bool `json:"redacted,omitempty"`
}

func (ThinkingContent) contentKind() ContentKind { return KindThinking }

// ImageContent is a base64-encoded image block.
type ImageContent struct {
	Data     string `json:"data"` // base64 encoded image data
	MimeType string `json:"mimeType"`
}

func (ImageContent) contentKind() ContentKind { return KindImage }

// ToolCall is a model tool invocation.
type ToolCall struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"` // JsonObject
	// ThoughtSignature is Google-specific: opaque signature for reusing
	// thought context.
	ThoughtSignature *string `json:"thoughtSignature,omitempty"`
	// Namespace is the OpenAI Responses namespace for calls to dynamically
	// loaded or namespaced tools.
	Namespace *string `json:"namespace,omitempty"`
}

func (ToolCall) contentKind() ContentKind { return KindToolCall }

// Role discriminates messages.
type Role = string

const (
	RoleSystem     Role = "system"
	RoleUser       Role = "user"
	RoleAssistant  Role = "assistant"
	RoleToolResult Role = "toolResult"
)

// Message is one transcript entry: SystemMessage, UserMessage,
// AssistantMessage, or ToolResultMessage.
type Message interface {
	messageRole() Role
}

// StringOrBlocks models upstream's `string | Content[]` message content.
// The zero value is an empty string; String reports whether it was a string.
type StringOrBlocks struct {
	// Text is set when the content was a plain string.
	Text string
	// Blocks is set when the content was an array of blocks.
	Blocks ContentList
}

// String reports whether the content is a plain string.
func (s StringOrBlocks) String() bool { return s.Blocks == nil }

// MarshalJSON encodes string content as a JSON string and block content as
// an array, matching upstream.
func (s StringOrBlocks) MarshalJSON() ([]byte, error) {
	if s.Blocks == nil {
		return MarshalJSON(s.Text)
	}
	return MarshalJSON(s.Blocks)
}

// UnmarshalJSON decodes both upstream arms. A JSON string becomes Text; an
// array is decoded per block by kind. Block objects of unknown kind are
// kept as raw text-typed errors upstream would reject; here they fail.
func (s *StringOrBlocks) UnmarshalJSON(data []byte) error {
	if len(data) > 0 && data[0] == '"' {
		s.Blocks = nil
		return json.Unmarshal(data, &s.Text)
	}
	var raw []json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	blocks := make([]Content, 0, len(raw))
	for _, r := range raw {
		b, err := unmarshalContentBlock(r)
		if err != nil {
			return err
		}
		blocks = append(blocks, b)
	}
	s.Blocks = blocks
	return nil
}

// UserContent is user message content: text or image blocks.
type UserContent interface {
	Content
	userContent()
}

func (TextContent) userContent()  {}
func (ImageContent) userContent() {}

// SystemMessage carries system instructions and tool declarations at one
// point in the transcript.
//
// The leading system message is the system prompt. Later system messages
// change it: Content adds instructions from that point on, Sections replace
// or remove named prompt sections, and ToolsAdded/ToolsRemoved change the
// tool set. Replaying every system message in order yields the current
// prompt and tools. Providers that accept system messages mid-conversation
// send each one in place; other providers rebuild the leading system
// message from the replayed state.
type SystemMessage struct {
	// Content is instruction text. On the leading message this is the base
	// prompt; later, additional instructions.
	Content StringOrBlocks `json:"content"`
	// Sections are named, ordered prompt sections rendered verbatim after
	// Content. The leading message declares them; later messages replace
	// sections by name, and nil removes one. Avoid integer-like names; JSON
	// objects reorder those.
	//
	// Go maps do not preserve insertion order the way JS objects do, so the
	// declaration order is tracked in sectionsOrder (set by UnmarshalJSON and
	// SetSection) and honored by MarshalJSON.
	Sections map[string]*string `json:"-"`
	// ToolsAdded are complete definitions of tools that become available at
	// this point.
	ToolsAdded []Tool `json:"toolsAdded,omitempty"`
	// ToolsRemoved are tools that stop being available at this point.
	ToolsRemoved []ToolReference `json:"toolsRemoved,omitempty"`
	Timestamp    int64           `json:"timestamp"` // Unix timestamp in milliseconds

	sectionsOrder []string
}

// SetSection sets or removes (value == nil) a named section, preserving
// first-declaration order for new names.
func (m *SystemMessage) SetSection(name string, value *string) {
	if m.Sections == nil {
		m.Sections = map[string]*string{}
	}
	if _, seen := m.Sections[name]; !seen && !m.hasSectionName(name) {
		m.sectionsOrder = append(m.sectionsOrder, name)
	}
	// Note: unlike replay semantics (where nil removes a section), a stored
	// system message keeps nil values in the map — upstream's sections object
	// literally carries "name": null.
	if value == nil {
		m.Sections[name] = nil
	} else {
		m.Sections[name] = value
	}
}

func (m *SystemMessage) hasSectionName(name string) bool {
	for _, n := range m.sectionsOrder {
		if n == name {
			return true
		}
	}
	return false
}

// sectionNames returns section names in declaration order, appending any
// names added to the map directly (sorted, for determinism).
func (m *SystemMessage) sectionNames() []string {
	names := m.sectionsOrder
	if len(names) < len(m.Sections) {
		var extra []string
		for name := range m.Sections {
			if !m.hasSectionName(name) {
				extra = append(extra, name)
			}
		}
		sort.Strings(extra)
		names = append(append([]string{}, names...), extra...)
	}
	return names
}

// SectionOrder returns the section declaration order.
func (m *SystemMessage) SectionOrder() []string { return m.sectionsOrder }

// orderedSections is the upstream JSON shape of `sections`.
type orderedSections []struct {
	Name  string  `json:"name"`
	Value *string `json:"value"`
}

func encodeSections(m *SystemMessage) (json.RawMessage, error) {
	if len(m.Sections) == 0 {
		return nil, nil
	}
	var buf bytes.Buffer
	buf.WriteByte('{')
	first := true
	for _, name := range m.sectionNames() {
		value, ok := m.Sections[name]
		if !ok {
			continue
		}
		if !first {
			buf.WriteByte(',')
		}
		first = false
		key, err := MarshalJSON(name)
		if err != nil {
			return nil, err
		}
		buf.Write(key)
		buf.WriteByte(':')
		if value == nil {
			buf.WriteString("null")
		} else {
			enc, err := MarshalJSON(*value)
			if err != nil {
				return nil, err
			}
			buf.Write(enc)
		}
	}
	buf.WriteByte('}')
	return json.RawMessage(buf.Bytes()), nil
}

func decodeSections(data []byte, m *SystemMessage) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	tok, err := d.Token()
	if err != nil {
		return err
	}
	if delim, ok := tok.(json.Delim); !ok || delim != '{' {
		return fmt.Errorf("ai: sections must be an object")
	}
	for d.More() {
		tok, err := d.Token()
		if err != nil {
			return err
		}
		name, ok := tok.(string)
		if !ok {
			return fmt.Errorf("ai: sections key must be a string")
		}
		var value *string
		var raw json.RawMessage
		if err := d.Decode(&raw); err != nil {
			return err
		}
		if string(raw) != "null" {
			var s string
			if err := json.Unmarshal(raw, &s); err != nil {
				return err
			}
			value = &s
		}
		m.SetSection(name, value)
	}
	_, err = d.Token() // closing '}'
	return err
}

// MarshalJSON encodes the message with upstream field order and key order
// for sections.
func (m *SystemMessage) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	buf.WriteString(`"content":`)
	enc, err := MarshalJSON(m.Content)
	if err != nil {
		return nil, err
	}
	buf.Write(enc)
	if len(m.Sections) > 0 {
		sectionsEnc, err := encodeSections(m)
		if err != nil {
			return nil, err
		}
		if sectionsEnc != nil {
			buf.WriteString(`,"sections":`)
			buf.Write(sectionsEnc)
		}
	}
	if len(m.ToolsAdded) > 0 {
		buf.WriteString(`,"toolsAdded":`)
		enc, err = MarshalJSON(m.ToolsAdded)
		if err != nil {
			return nil, err
		}
		buf.Write(enc)
	}
	if len(m.ToolsRemoved) > 0 {
		buf.WriteString(`,"toolsRemoved":`)
		enc, err = MarshalJSON(m.ToolsRemoved)
		if err != nil {
			return nil, err
		}
		buf.Write(enc)
	}
	ts, _ := MarshalJSON(m.Timestamp)
	buf.WriteString(`,"timestamp":`)
	buf.Write(ts)
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// UnmarshalJSON decodes the upstream shape, capturing section key order.
func (m *SystemMessage) UnmarshalJSON(data []byte) error {
	var wire struct {
		Content      StringOrBlocks  `json:"content"`
		Sections     json.RawMessage `json:"sections"`
		ToolsAdded   []Tool          `json:"toolsAdded"`
		ToolsRemoved []ToolReference `json:"toolsRemoved"`
		Timestamp    int64           `json:"timestamp"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	*m = SystemMessage{
		Content:      wire.Content,
		ToolsAdded:   wire.ToolsAdded,
		ToolsRemoved: wire.ToolsRemoved,
		Timestamp:    wire.Timestamp,
		Sections:     map[string]*string{},
	}
	if len(wire.Sections) > 0 && string(wire.Sections) != "null" {
		if err := decodeSections(wire.Sections, m); err != nil {
			return err
		}
	}
	return nil
}

func (*SystemMessage) messageRole() Role { return RoleSystem }

// UserMessage is a user message.
type UserMessage struct {
	Content   StringOrBlocks `json:"content"` // string | (TextContent|ImageContent)[]
	Timestamp int64          `json:"timestamp"`
}

func (*UserMessage) messageRole() Role { return RoleUser }

// AssistantMessage is a model response.
type AssistantMessage struct {
	Content  ContentList `json:"content"`
	API      Api         `json:"api"`
	Provider ProviderId  `json:"provider"`
	Model    string      `json:"model"`
	// ResponseModel is the concrete model reported by the provider when
	// different from the requested Model.
	ResponseModel *string `json:"responseModel,omitempty"`
	// ResponseID is the provider-specific response/message identifier when
	// the upstream API exposes one.
	ResponseID *string `json:"responseId,omitempty"`
	// ProviderThinkingLevel is the exact provider-native effort level used
	// for this response. Absent for legacy or unmanaged responses.
	ProviderThinkingLevel *string                      `json:"providerThinkingLevel,omitempty"`
	Diagnostics           []AssistantMessageDiagnostic `json:"diagnostics,omitempty"`
	Usage                 Usage                        `json:"usage"`
	StopReason            StopReason                   `json:"stopReason"`
	Deferred              *DeferredHandle              `json:"deferred,omitempty"`
	ErrorMessage          *string                      `json:"errorMessage,omitempty"`
	RawStopReason         *string                      `json:"rawStopReason,omitempty"`
	// EndTurn is the provider indication of whether the model explicitly
	// ended its turn. Preserved for debugging and does not currently affect
	// agent control flow.
	EndTurn   *bool `json:"endTurn,omitempty"`
	Timestamp int64 `json:"timestamp"`
}

func (*AssistantMessage) messageRole() Role { return RoleAssistant }

// ToolResultMessage is a tool execution result fed back to the model.
type ToolResultMessage struct {
	ToolCallID string          `json:"toolCallId"`
	ToolName   string          `json:"toolName"`
	Content    UserContentList `json:"content"` // text and images
	// Details is tool-specific structured output (JsonValue upstream).
	Details json.RawMessage `json:"details,omitempty"`
	// Usage from the tool execution itself, if available. Not part of main
	// LLM context accounting.
	Usage     *Usage `json:"usage,omitempty"`
	IsError   bool   `json:"isError"`
	Timestamp int64  `json:"timestamp"`
}

func (*ToolResultMessage) messageRole() Role { return RoleToolResult }

// ToolReference names a tool that stopped being available.
type ToolReference struct {
	Name string `json:"name"`
}

// Tool is a tool declaration sent to the model. Parameters is the JSON
// Schema object (typebox TSchema upstream).
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
	// ConstrainedSampling mirrors upstream's `false | ConstrainedSamplingConfig`:
	// nil when unset, FalseValue for explicit false, or a *ConstrainedSamplingConfig.
	ConstrainedSampling ConstrainedSamplingValue `json:"-"`
}

// ConstrainedSamplingValue is upstream's optional `false | config` union.
type ConstrainedSamplingValue struct {
	// Set is true when the field was present (including explicit false).
	Set    bool
	False  bool
	Config *ConstrainedSamplingConfig
}

// FalseValue is the explicit `false` arm.
var FalseValue = ConstrainedSamplingValue{Set: true, False: true}

// MarshalJSON implements the upstream field encoding on Tool, including the
// `constrainedSampling: false | config` union.
func (t Tool) MarshalJSON() ([]byte, error) {
	type alias struct {
		Name                string          `json:"name"`
		Description         string          `json:"description"`
		Parameters          json.RawMessage `json:"parameters"`
		ConstrainedSampling json.RawMessage `json:"constrainedSampling,omitempty"`
	}
	a := alias{Name: t.Name, Description: t.Description, Parameters: t.Parameters}
	if t.ConstrainedSampling.Set {
		if t.ConstrainedSampling.False {
			a.ConstrainedSampling = json.RawMessage("false")
		} else {
			enc, err := MarshalJSON(t.ConstrainedSampling.Config)
			if err != nil {
				return nil, err
			}
			a.ConstrainedSampling = enc
		}
	}
	return MarshalJSON(a)
}

// UnmarshalJSON decodes the upstream Tool shape.
func (t *Tool) UnmarshalJSON(data []byte) error {
	var a struct {
		Name                string          `json:"name"`
		Description         string          `json:"description"`
		Parameters          json.RawMessage `json:"parameters"`
		ConstrainedSampling json.RawMessage `json:"constrainedSampling,omitempty"`
	}
	if err := json.Unmarshal(data, &a); err != nil {
		return err
	}
	t.Name, t.Description, t.Parameters = a.Name, a.Description, a.Parameters
	switch {
	case len(a.ConstrainedSampling) == 0 || string(a.ConstrainedSampling) == "null":
		t.ConstrainedSampling = ConstrainedSamplingValue{}
	case string(a.ConstrainedSampling) == "false":
		t.ConstrainedSampling = FalseValue
	default:
		cfg := new(ConstrainedSamplingConfig)
		if err := json.Unmarshal(a.ConstrainedSampling, cfg); err != nil {
			return err
		}
		t.ConstrainedSampling = ConstrainedSamplingValue{Set: true, Config: cfg}
	}
	return nil
}

// ConstrainedSamplingConfig is an optional provider-side constrained
// sampling config for a tool.
//
// The json_schema value roughly maps to the concept of `strict` in APIs
// implemented as json-schema constrained sampling. Grammar variants let
// callers provide provider-specific encodings of the same intended language.
type ConstrainedSamplingConfig struct {
	// Type is "json_schema" or "grammar".
	Type string `json:"type"`
	// Strict applies to the json_schema arm: "prefer" or "require".
	Strict string `json:"strict,omitempty"`
	// Variants applies to the grammar arm.
	Variants map[string]string `json:"variants,omitempty"`
}

// MarshalJSON implements the upstream discriminated-union encoding.
func (c ConstrainedSamplingConfig) MarshalJSON() ([]byte, error) {
	switch c.Type {
	case "json_schema":
		return MarshalJSON(struct {
			Type   string `json:"type"`
			Strict string `json:"strict"`
		}{c.Type, c.Strict})
	case "grammar":
		return MarshalJSON(struct {
			Type     string            `json:"type"`
			Variants map[string]string `json:"variants"`
		}{c.Type, c.Variants})
	default:
		return nil, fmt.Errorf("ai: unknown constrained sampling type %q", c.Type)
	}
}

// UnmarshalJSON decodes both upstream arms of ConstrainedSamplingConfig.
func (c *ConstrainedSamplingConfig) UnmarshalJSON(data []byte) error {
	var probe struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return err
	}
	switch probe.Type {
	case "json_schema":
		var v struct {
			Type   string `json:"type"`
			Strict string `json:"strict"`
		}
		if err := json.Unmarshal(data, &v); err != nil {
			return err
		}
		*c = ConstrainedSamplingConfig{Type: v.Type, Strict: v.Strict}
	case "grammar":
		var v struct {
			Type     string            `json:"type"`
			Variants map[string]string `json:"variants"`
		}
		if err := json.Unmarshal(data, &v); err != nil {
			return err
		}
		*c = ConstrainedSamplingConfig{Type: v.Type, Variants: v.Variants}
	default:
		return fmt.Errorf("ai: unknown constrained sampling type %q", probe.Type)
	}
	return nil
}

// Context is the request input accepted by the public stream entry points
// (Models.stream(), streamSimple(), ...). SystemPrompt and Tools are
// shorthand for a leading system message; NormalizeContext folds them into
// one before the request reaches a provider.
type Context struct {
	SystemPrompt *string
	Messages     []Message
	Tools        []Tool
}

// TranscriptContext is the normalized request context passed to providers
// and API implementations. The prompt and tool declarations are carried by
// the transcript's system messages. Only NormalizeContext produces this, so
// a raw Context cannot reach provider code by accident.
type TranscriptContext struct {
	Messages []Message
}
