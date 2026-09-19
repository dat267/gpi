package ai

import (
	"encoding/json"
	"reflect"
	"testing"
)

// Tests for the JSON layer: byte compatibility with pi's on-disk transcript
// format. Upstream shape reference: packages/ai/src/types.ts.

func TestContentBlockRoundTrip(t *testing.T) {
	content := ContentList{
		TextContent{Text: "hello"},
		ThinkingContent{Thinking: "hmm", ThinkingSignature: strptr("sig"), Redacted: false},
		ImageContent{Data: "aGk=", MimeType: "image/png"},
		ToolCall{ID: "call1", Name: "read", Arguments: json.RawMessage(`{"path":"x"}`)},
	}
	enc, err := MarshalJSON(content)
	if err != nil {
		t.Fatal(err)
	}
	want := `[{"type":"text","text":"hello"},` +
		`{"type":"thinking","thinking":"hmm","thinkingSignature":"sig"},` +
		`{"type":"image","data":"aGk=","mimeType":"image/png"},` +
		`{"type":"toolCall","id":"call1","name":"read","arguments":{"path":"x"}}]`
	if string(enc) != want {
		t.Fatalf("marshal:\n got %s\nwant %s", enc, want)
	}
	var back ContentList
	if err := json.Unmarshal(enc, &back); err != nil {
		t.Fatal(err)
	}
	if len(back) != 4 {
		t.Fatalf("decoded %d blocks; want 4", len(back))
	}
	if _, ok := back[0].(TextContent); !ok {
		t.Fatalf("block 0 = %T; want TextContent", back[0])
	}
	if _, ok := back[1].(ThinkingContent); !ok {
		t.Fatalf("block 1 = %T; want ThinkingContent", back[1])
	}
	if _, ok := back[2].(ImageContent); !ok {
		t.Fatalf("block 2 = %T; want ImageContent", back[2])
	}
	if _, ok := back[3].(ToolCall); !ok {
		t.Fatalf("block 3 = %T; want ToolCall", back[3])
	}
}

func TestRedactedThinkingRoundTrip(t *testing.T) {
	enc, err := json.Marshal(ThinkingContent{Thinking: "", ThinkingSignature: strptr("encrypted"), Redacted: true})
	if err != nil {
		t.Fatal(err)
	}
	if string(enc) != `{"thinking":"","thinkingSignature":"encrypted","redacted":true}` {
		t.Fatalf("got %s", enc)
	}
}

func strptr(s string) *string { return &s }

func TestSystemMessageSectionOrderPreserved(t *testing.T) {
	raw := `{"content":"base","sections":{"zeta":"Z","alpha":"A","mid":null},"timestamp":0}`
	var msg SystemMessage
	if err := json.Unmarshal([]byte(raw), &msg); err != nil {
		t.Fatal(err)
	}
	order := msg.SectionOrder()
	if !reflect.DeepEqual(order, []string{"zeta", "alpha", "mid"}) {
		t.Fatalf("section order = %v; want [zeta alpha mid]", order)
	}
	// Nil section removes; re-encoding preserves declaration order.
	enc, err := MarshalJSON(&msg)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"content":"base","sections":{"zeta":"Z","alpha":"A","mid":null},"timestamp":0}`
	if string(enc) != want {
		t.Fatalf("marshal:\n got %s\nwant %s", enc, want)
	}
}

func TestSystemMessageReplayThenEncode(t *testing.T) {
	// A replayed system message (GetCurrentSystemMessage) encodes with
	// sections first-set order and toolsAdded.
	m := GetCurrentSystemMessage([]Message{
		&SystemMessage{
			Content:   StringOrBlocks{Text: ""},
			Sections:  map[string]*string{},
			Timestamp: 0,
		},
	})
	// Upstream: timestamp ??= 0 makes the timestamp defined, so an empty
	// system message still replays into a message.
	if m == nil {
		t.Fatal("empty-content system message should still replay")
	}
	if m.Content.Text != "" {
		t.Fatalf("content = %q; want empty", m.Content.Text)
	}
	// With no system message at all, the replay is nil.
	if got := GetCurrentSystemMessage([]Message{&UserMessage{Timestamp: 1}}); got != nil {
		t.Fatalf("no system messages should replay nil; got %+v", got)
	}

	head := &SystemMessage{Content: StringOrBlocks{Text: "base"}, Timestamp: 0}
	head.SetSection("rules", strptr("<rules>old</rules>"))
	head.SetSection("docs", strptr("<docs>d</docs>"))
	head.ToolsAdded = []Tool{{Name: "t1", Description: "d", Parameters: json.RawMessage(`{"type":"object"}`)}}
	late := &SystemMessage{Content: StringOrBlocks{Text: "updated"}, Timestamp: 2}
	late.SetSection("rules", strptr("<rules>new</rules>"))
	late.SetSection("docs", nil)

	replayed := GetCurrentSystemMessage([]Message{head, late})
	if replayed == nil {
		t.Fatal("replay should exist")
	}
	if replayed.Content.Text != "base\n\nupdated" {
		t.Fatalf("content = %q", replayed.Content.Text)
	}
	if got := GetSystemMessageText(replayed); got != "base\n\nupdated\n\n<rules>new</rules>" {
		t.Fatalf("text = %q", got)
	}
	// docs was removed, rules updated, order preserved.
	enc, err := MarshalJSON(replayed)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"content":"base\n\nupdated","sections":{"rules":"<rules>new</rules>"},"toolsAdded":[{"name":"t1","description":"d","parameters":{"type":"object"}}],"timestamp":0}`
	if string(enc) != want {
		t.Fatalf("marshal:\n got %s\nwant %s", enc, want)
	}
}

func TestMessageRoleRoundTrip(t *testing.T) {
	messages := []Message{
		&SystemMessage{Content: StringOrBlocks{Text: "prompt"}, Timestamp: 0},
		&UserMessage{Content: StringOrBlocks{Text: "hi"}, Timestamp: 1},
		&UserMessage{Content: StringOrBlocks{Blocks: ContentList{
			TextContent{Text: "look"},
			ImageContent{Data: "aGk=", MimeType: "image/jpeg"},
		}}, Timestamp: 2},
		&AssistantMessage{
			Content: ContentList{
				ThinkingContent{Thinking: "hmm"},
				TextContent{Text: "answer"},
				ToolCall{ID: "c1", Name: "bash", Arguments: json.RawMessage(`{"cmd":"ls"}`)},
			},
			API:        APIAnthropicMessages,
			Provider:   "anthropic",
			Model:      "claude-opus-4-5",
			StopReason: StopToolUse,
			Usage: Usage{
				Input: 10, Output: 5, CacheRead: 2, CacheWrite: 3,
				TotalTokens: 20,
				Cost:        UsageCost{Input: 1, Output: 2, CacheRead: 0.1, CacheWrite: 0.2, Total: 3.3},
			},
			Timestamp: 3,
		},
		&ToolResultMessage{
			ToolCallID: "c1",
			ToolName:   "bash",
			Content:    UserContentList{TextContent{Text: "out"}},
			IsError:    false,
			Timestamp:  4,
		},
	}
	enc, err := MarshalMessages(messages)
	if err != nil {
		t.Fatal(err)
	}
	// Spot-check role discrimination and key names.
	var probe []map[string]json.RawMessage
	for _, e := range enc {
		var m map[string]json.RawMessage
		if err := json.Unmarshal(e, &m); err != nil {
			t.Fatal(err)
		}
		probe = append(probe, m)
	}
	roles := []string{"system", "user", "user", "assistant", "toolResult"}
	for i, want := range roles {
		var role string
		json.Unmarshal(probe[i]["role"], &role)
		if role != want {
			t.Fatalf("message %d role = %q; want %q", i, role, want)
		}
	}
	if _, ok := probe[3]["toolCallId"]; ok {
		t.Fatal("assistant message should not have toolCallId")
	}
	if _, ok := probe[4]["toolCallId"]; !ok {
		t.Fatal("toolResult message should have toolCallId")
	}

	back, err := UnmarshalMessages(enc)
	if err != nil {
		t.Fatal(err)
	}
	if len(back) != len(messages) {
		t.Fatalf("decoded %d; want %d", len(back), len(messages))
	}
	for i := range messages {
		if back[i].messageRole() != messages[i].messageRole() {
			t.Fatalf("message %d role mismatch", i)
		}
	}
	reenc, err := MarshalMessages(back)
	if err != nil {
		t.Fatal(err)
	}
	for i := range enc {
		if string(reenc[i]) != string(enc[i]) {
			t.Fatalf("re-encode mismatch at %d:\n got %s\nwant %s", i, reenc[i], enc[i])
		}
	}
}

func TestUserContentListRejectsToolCall(t *testing.T) {
	raw := `[{"type":"toolCall","id":"1","name":"x","arguments":{}}]`
	var l UserContentList
	if err := json.Unmarshal([]byte(raw), &l); err == nil {
		t.Fatal("expected error for toolCall in user content")
	}
}

func TestToolConstrainedSamplingUnion(t *testing.T) {
	tool := Tool{Name: "t", Description: "d", Parameters: json.RawMessage(`{}`),
		ConstrainedSampling: FalseValue}
	enc, err := MarshalJSON(tool)
	if err != nil {
		t.Fatal(err)
	}
	if string(enc) != `{"name":"t","description":"d","parameters":{},"constrainedSampling":false}` {
		t.Fatalf("got %s", enc)
	}
	var back Tool
	if err := json.Unmarshal(enc, &back); err != nil {
		t.Fatal(err)
	}
	if !back.ConstrainedSampling.Set || !back.ConstrainedSampling.False {
		t.Fatalf("constrainedSampling = %+v; want explicit false", back.ConstrainedSampling)
	}

	tool2 := Tool{Name: "t", Description: "d", Parameters: json.RawMessage(`{}`),
		ConstrainedSampling: ConstrainedSamplingValue{Set: true,
			Config: &ConstrainedSamplingConfig{Type: "json_schema", Strict: "require"}}}
	enc2, _ := MarshalJSON(tool2)
	if string(enc2) != `{"name":"t","description":"d","parameters":{},"constrainedSampling":{"type":"json_schema","strict":"require"}}` {
		t.Fatalf("got %s", enc2)
	}
	var back2 Tool
	if err := json.Unmarshal(enc2, &back2); err != nil {
		t.Fatal(err)
	}
	if back2.ConstrainedSampling.Config == nil || back2.ConstrainedSampling.Config.Strict != "require" {
		t.Fatalf("constrainedSampling = %+v", back2.ConstrainedSampling)
	}
}

func TestNormalizeContext(t *testing.T) {
	// Empty stays empty.
	ctx := NormalizeContext(Context{})
	if len(ctx.Messages) != 0 {
		t.Fatalf("empty context produced %d messages", len(ctx.Messages))
	}
	// Prompt and tools fold into a leading system message with timestamp 0.
	prompt := "p"
	ctx = NormalizeContext(Context{
		SystemPrompt: &prompt,
		Tools:        []Tool{{Name: "t", Description: "d", Parameters: json.RawMessage(`{}`)}},
		Messages:     []Message{&UserMessage{Content: StringOrBlocks{Text: "u"}, Timestamp: 5}},
	})
	if len(ctx.Messages) != 2 {
		t.Fatalf("messages = %d; want 2", len(ctx.Messages))
	}
	head := ctx.Messages[0].(*SystemMessage)
	if head.Content.Text != "p" || head.Timestamp != 0 || len(head.ToolsAdded) != 1 {
		t.Fatalf("head = %+v", head)
	}
}
