package ai

import (
	"encoding/json"
	"testing"
)

// Port of packages/ai/test/text.test.ts.

func testContent() []Content {
	return []Content{
		ThinkingContent{Thinking: "reasoning"},
		TextContent{Text: "first"},
		ToolCall{ID: "1", Name: "read", Arguments: json.RawMessage(`{}`)},
		TextContent{Text: "second"},
	}
}

func TestContentTextExtractsAssistantTextBlocks(t *testing.T) {
	got := ContentText(StringOrBlocks{Blocks: testContent()}, "\n")
	if got != "first\nsecond" {
		t.Fatalf("got %q; want %q", got, "first\nsecond")
	}
}

func TestContentTextSupportsCustomSeparators(t *testing.T) {
	got := ContentText(StringOrBlocks{Blocks: testContent()}, "")
	if got != "firstsecond" {
		t.Fatalf("got %q; want %q", got, "firstsecond")
	}
}

func TestContentTextPassesStringThrough(t *testing.T) {
	got := ContentText(StringOrBlocks{Text: "hello"}, "\n")
	if got != "hello" {
		t.Fatalf("got %q; want %q", got, "hello")
	}
}

func TestContentTextExtractsTextFromToolResultContent(t *testing.T) {
	content := ContentList{
		TextContent{Text: "first"},
		ImageContent{Data: "...", MimeType: "image/png"},
		TextContent{Text: "second"},
	}
	got := ContentText(StringOrBlocks{Blocks: content}, "")
	if got != "firstsecond" {
		t.Fatalf("got %q; want %q", got, "firstsecond")
	}
}
