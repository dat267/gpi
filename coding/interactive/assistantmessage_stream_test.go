package interactive

import (
	"strings"
	"testing"

	"github.com/dat267/pier/ai"
	"github.com/dat267/pier/coding"
	"github.com/dat267/pier/tui"
)

func streamingAssistant(text string, stop ai.StopReason) *ai.AssistantMessage {
	return &ai.AssistantMessage{
		API: ai.APIAnthropicMessages, Provider: "test", Model: "m", StopReason: stop,
		Content: ai.ContentList{ai.TextContent{Text: text}},
	}
}

func markdownChildren(component *AssistantMessageComponent) []*tui.Markdown {
	var out []*tui.Markdown
	for _, child := range component.contentContainer.Children {
		if markdown, ok := child.(*tui.Markdown); ok {
			out = append(out, markdown)
		}
	}
	return out
}

// TestAssistantMessageReusesBlockComponents pins that an update mutates the
// existing block components; recreating them would drop the Markdown render
// cache that makes streaming cheap.
func TestAssistantMessageReusesBlockComponents(t *testing.T) {
	newRendererTestTheme(t)
	component := NewAssistantMessageComponent(streamingAssistant("first paragraph", ai.StopPending), false, nil, "", 0, nil)
	before := markdownChildren(component)
	if len(before) != 1 {
		t.Fatalf("markdown children = %d, want 1", len(before))
	}

	component.UpdateContent(streamingAssistant("first paragraph and more", ai.StopPending), true)
	after := markdownChildren(component)
	if len(after) != 1 {
		t.Fatalf("markdown children = %d, want 1", len(after))
	}
	if before[0] != after[0] {
		t.Fatal("the text block was rebuilt instead of reused")
	}
	if after[0].Text != "first paragraph and more" {
		t.Fatalf("reused markdown text = %q", after[0].Text)
	}
}

// TestAssistantMessageStreamingMatchesFreshBuild requires the incrementally
// updated component to render exactly like one built from scratch, over every
// prefix of a rich body.
func TestAssistantMessageStreamingMatchesFreshBuild(t *testing.T) {
	newRendererTestTheme(t)
	body := "# Heading\n\nIntro with **bold** and `code`.\n\n- one\n- two\n\n```go\nfunc f() int { return 1 }\n```\n\nA final paragraph that keeps growing."
	streaming := NewAssistantMessageComponent(nil, false, nil, "", 1, nil)
	for n := 1; n <= len(body); n++ {
		message := streamingAssistant(body[:n], ai.StopPending)
		streaming.UpdateContent(message, true)
		got := strings.Join(streaming.Render(60), "\n")

		fresh := NewAssistantMessageComponent(nil, false, nil, "", 1, nil)
		fresh.UpdateContent(message, true)
		want := strings.Join(fresh.Render(60), "\n")

		if got != want {
			t.Fatalf("prefix %d:\n got %q\nwant %q", n, got, want)
		}
	}
}

// TestAssistantMessageReuseReflectsOutputPad guards the mutable render inputs
// that a reused component must pick up again.
func TestAssistantMessageReuseReflectsOutputPad(t *testing.T) {
	newRendererTestTheme(t)
	component := NewAssistantMessageComponent(streamingAssistant("hello world", ai.StopStop), false, nil, "", 0, nil)
	component.SetOutputPad(3)
	lines := markdownChildren(component)[0].Render(40)
	found := false
	for _, line := range lines {
		if !strings.Contains(line, "hello world") {
			continue
		}
		found = true
		if !strings.HasPrefix(line, "   hello world") {
			t.Fatalf("padded line = %q, want three leading spaces", line)
		}
	}
	if !found {
		t.Fatalf("no content line in %q", lines)
	}
}

// TestAssistantMessageThinkingReuse covers the thinking run: hidden and
// visible variants stay correct across updates.
func TestAssistantMessageThinkingReuse(t *testing.T) {
	newRendererTestTheme(t)
	withThinking := func(thinking, text string) *ai.AssistantMessage {
		return &ai.AssistantMessage{
			API: ai.APIAnthropicMessages, Provider: "test", Model: "m", StopReason: ai.StopPending,
			Content: ai.ContentList{
				ai.ThinkingContent{Thinking: thinking},
				ai.TextContent{Text: text},
			},
		}
	}
	component := NewAssistantMessageComponent(withThinking("step one", "answer"), true, nil, "Thinking...", 0, nil)
	hidden := strings.Join(component.Render(60), "\n")
	if !strings.Contains(coding.StripAnsi(hidden), "Thinking...") {
		t.Fatalf("hidden thinking render = %q", hidden)
	}

	component.UpdateContent(withThinking("step one step two", "answer grows"), true)
	hidden = strings.Join(component.Render(60), "\n")
	if !strings.Contains(coding.StripAnsi(hidden), "Thinking...") {
		t.Fatalf("hidden thinking render after update = %q", hidden)
	}
	if !strings.Contains(coding.StripAnsi(hidden), "answer grows") {
		t.Fatalf("text after update = %q", hidden)
	}
}
