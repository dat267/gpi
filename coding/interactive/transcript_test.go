package interactive

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/dat267/gpi/ai"
	"github.com/dat267/gpi/coding"
	"github.com/dat267/gpi/tui"
)

type transcriptTestSettings struct {
	showCacheMiss bool
}

func newTranscriptTestRenderer(t *testing.T, showCacheMiss bool) (*TranscriptRenderer, *coding.SettingsManager) {
	t.Helper()
	SetCustomThemesDir(t.TempDir())
	SetRegisteredThemes(nil)
	SetTrueColorSupport(true)
	SetStyleColorsEnabled(true)
	InitTheme("dark", false)

	settings := coding.NewInMemorySettingsManager(nil, coding.SettingsManagerCreateOptions{})
	settings.SetShowCacheMissNotices(showCacheMiss)
	renderer := NewTranscriptRenderer(&tui.Container{}, nil, settings, nil, nil)
	return renderer, settings
}

func renderChat(t *testing.T, chat *tui.Container) []string {
	t.Helper()
	lines := chat.Render(100)
	for index, line := range lines {
		lines[index] = strings.TrimRight(line, " ")
	}
	return lines
}

// TestTranscriptUserMessages covers the user-message rendering paths.
func TestTranscriptUserMessages(t *testing.T) {
	renderer, _ := newTranscriptTestRenderer(t, false)
	renderer.MarkdownTheme = nil

	// Plain user message: no leading spacer on an empty chat.
	renderer.AddMessageToChat(&ai.UserMessage{Content: ai.StringOrBlocks{Text: "hello"}}, false)
	chat := renderer.Chat
	if len(chat.Children) != 1 {
		t.Fatalf("children = %d", len(chat.Children))
	}

	// A second user message adds a spacer first.
	renderer.AddMessageToChat(&ai.UserMessage{Content: ai.StringOrBlocks{Text: "again"}}, false)
	if len(chat.Children) != 3 {
		t.Fatalf("children = %d", len(chat.Children))
	}

	// Empty content is skipped.
	before := len(chat.Children)
	renderer.AddMessageToChat(&ai.UserMessage{Content: ai.StringOrBlocks{Text: ""}}, false)
	if len(chat.Children) != before {
		t.Fatal("empty user message rendered")
	}

	if got := renderer.GetUserMessageText(&ai.UserMessage{Content: ai.StringOrBlocks{Text: "abc"}}); got != "abc" {
		t.Fatalf("text = %q", got)
	}
	blocks := ai.StringOrBlocks{Blocks: ai.ContentList{
		ai.TextContent{Text: "one"}, ai.TextContent{Text: "two"},
	}}
	if got := renderer.GetUserMessageText(&ai.UserMessage{Content: blocks}); got != "onetwo" {
		t.Fatalf("block text = %q", got)
	}
	if got := renderer.GetUserMessageText(&ai.AssistantMessage{}); got != "" {
		t.Fatalf("assistant text = %q", got)
	}

	// History population.
	editor := NewCustomEditor(editorTestHost{}, tui.EditorTheme{}, NewAppKeybindingsManager(nil, ""), CustomEditorOptions{})
	renderer.Editor = editor
	renderer.AddMessageToChat(&ai.UserMessage{Content: ai.StringOrBlocks{Text: "remember me"}}, true)
	editor.HandleInput("\x1b[A")
	if editor.GetText() != "remember me" {
		t.Fatalf("history navigation = %q", editor.GetText())
	}
}

// TestTranscriptSkillBlock covers the skill-block split.
func TestTranscriptSkillBlock(t *testing.T) {
	renderer, _ := newTranscriptTestRenderer(t, false)
	text := "<skill name=\"debug\" location=\"/skills/debug/SKILL.md\">\ninstructions\n</skill>\n\nuser question"
	renderer.AddMessageToChat(&ai.UserMessage{Content: ai.StringOrBlocks{Text: text}}, false)
	// skill component + spacer + user message
	if len(renderer.Chat.Children) != 3 {
		t.Fatalf("children = %d", len(renderer.Chat.Children))
	}
	lines := strings.Join(renderChat(t, renderer.Chat), "\n")
	if !strings.Contains(lines, "[skill]") || !strings.Contains(lines, "user question") {
		t.Fatalf("skill render = %q", lines)
	}
}

// TestTranscriptSpecialMessages covers the custom-role messages.
func TestTranscriptSpecialMessages(t *testing.T) {
	renderer, _ := newTranscriptTestRenderer(t, false)

	renderer.AddMessageToChat(coding.CreateBashExecutionMessage("ls -la", "file.txt", 0, false, false, "", false, 1), false)
	if len(renderer.Chat.Children) != 1 {
		t.Fatalf("bash children = %d", len(renderer.Chat.Children))
	}
	bashLines := strings.Join(renderChat(t, renderer.Chat), "\n")
	if !strings.Contains(bashLines, "ls -la") {
		t.Fatalf("bash render = %q", bashLines)
	}

	renderer.AddMessageToChat(coding.CreateCompactionSummaryMessage("compacted", 12000, 2), false)
	if len(renderer.Chat.Children) != 3 { // spacer + component
		t.Fatalf("compaction children = %d", len(renderer.Chat.Children))
	}

	renderer.AddMessageToChat(coding.CreateBranchSummaryMessage("branched", "abc", 3), false)
	if len(renderer.Chat.Children) != 5 {
		t.Fatalf("branch children = %d", len(renderer.Chat.Children))
	}

	// Custom messages only render when display is true.
	before := len(renderer.Chat.Children)
	renderer.AddMessageToChat(coding.CreateCustomMessage("note", "hidden", false, 4), false)
	if len(renderer.Chat.Children) != before {
		t.Fatal("hidden custom message rendered")
	}
	renderer.AddMessageToChat(coding.CreateCustomMessage("note", "shown", true, 5), false)
	if len(renderer.Chat.Children) != before+1 {
		t.Fatalf("custom children = %d", len(renderer.Chat.Children))
	}

	// Assistant, toolResult and system messages.
	renderer.AddMessageToChat(&ai.AssistantMessage{Content: ai.ContentList{ai.TextContent{Text: "hi"}}, StopReason: ai.StopStop}, false)
	if len(renderer.Chat.Children) != before+2 {
		t.Fatalf("assistant children = %d", len(renderer.Chat.Children))
	}
	renderer.AddMessageToChat(&ai.ToolResultMessage{ToolCallID: "t"}, false)
	renderer.AddMessageToChat(&ai.SystemMessage{}, false)
	if len(renderer.Chat.Children) != before+2 {
		t.Fatal("toolResult/system rendered")
	}
}

// TestTranscriptStatusLines covers showStatus/showManagedToolStatus.
func TestTranscriptStatusLines(t *testing.T) {
	renderer, _ := newTranscriptTestRenderer(t, false)

	renderer.ShowStatus("first")
	if len(renderer.Chat.Children) != 2 { // spacer + text
		t.Fatalf("children = %d", len(renderer.Chat.Children))
	}
	// A consecutive status replaces the previous text.
	renderer.ShowStatus("second")
	if len(renderer.Chat.Children) != 2 {
		t.Fatalf("children after replace = %d", len(renderer.Chat.Children))
	}
	lines := strings.Join(renderChat(t, renderer.Chat), "\n")
	if !strings.Contains(lines, "second") || strings.Contains(lines, "first") {
		t.Fatalf("status render = %q", lines)
	}
	// A non-status child in between appends a new status.
	renderer.Chat.AddChild(tui.NewText("interruption", 0, 0, nil))
	renderer.ShowStatus("third")
	if len(renderer.Chat.Children) != 5 {
		t.Fatalf("children after append = %d", len(renderer.Chat.Children))
	}

	managed, _ := newTranscriptTestRenderer(t, false)
	managed.ShowManagedToolStatus(ManagedToolStatus{Type: "info", Message: "installing"})
	if len(managed.Chat.Children) != 2 { // spacer + text
		t.Fatalf("managed children = %d", len(managed.Chat.Children))
	}
	managed.ShowManagedToolStatus(ManagedToolStatus{Type: "warning", Message: "slow"})
	lines = strings.Join(renderChat(t, managed.Chat), "\n")
	if !strings.Contains(lines, "Warning: slow") {
		t.Fatalf("managed render = %q", lines)
	}
}

// TestTranscriptNotices covers the cache/compaction notices.
func TestTranscriptNotices(t *testing.T) {
	renderer, settings := newTranscriptTestRenderer(t, false)

	// The setting gate lives in MaybeShowCacheMissNotice (upstream's
	// addCacheMissNotice is unconditional).
	renderer.SessionInfo = coding.NewSessionManager("/tmp/proj", &coding.SessionManagerOptions{Persist: boolPtr(false)})
	renderer.MaybeShowCacheMissNotice(&ai.AssistantMessage{})
	if len(renderer.Chat.Children) != 0 {
		t.Fatal("notice rendered while disabled")
	}
	settings.SetShowCacheMissNotices(true)

	// Below both thresholds: nothing.
	renderer.AddCacheMissNotice(coding.CacheMiss{MissedTokens: 100, MissedCost: 0.001})
	if len(renderer.Chat.Children) != 0 {
		t.Fatal("insignificant notice rendered")
	}
	// Token threshold.
	renderer.AddCacheMissNotice(coding.CacheMiss{MissedTokens: 25000, MissedCost: 0.02})
	if len(renderer.Chat.Children) != 2 {
		t.Fatalf("children = %d", len(renderer.Chat.Children))
	}
	lines := strings.Join(renderChat(t, renderer.Chat), "\n")
	if !strings.Contains(lines, "Cache miss: 25k tokens re-billed (~$0.02)") {
		t.Fatalf("notice = %q", lines)
	}
	// Model-switch label.
	renderer.AddCacheMissNotice(coding.CacheMiss{MissedTokens: 30000, MissedCost: 0.5, ModelChanged: true})
	lines = strings.Join(renderChat(t, renderer.Chat), "\n")
	if !strings.Contains(lines, "Cache miss after model switch") {
		t.Fatalf("model switch notice = %q", lines)
	}
	// Idle label (rounded minutes).
	renderer.AddCacheMissNotice(coding.CacheMiss{MissedTokens: 30000, MissedCost: 0.5, IdleMs: 10 * 60 * 1000})
	lines = strings.Join(renderChat(t, renderer.Chat), "\n")
	if !strings.Contains(lines, "Cache miss after 10m idle") {
		t.Fatalf("idle notice = %q", lines)
	}

	// Compaction cost notices.
	renderer.AddCompactionCostNotice(CompactionCostNotice{Kind: "compaction", Usage: ai.Usage{
		Input: 1000, Output: 500, CacheRead: 500, CacheWrite: 0,
		Cost: ai.UsageCost{Total: 0.0315},
	}})
	lines = strings.Join(renderChat(t, renderer.Chat), "\n")
	if !strings.Contains(lines, "Compaction: 2.0k tokens billed (~$0.03)") {
		t.Fatalf("compaction notice = %q", lines)
	}
	// Below the cost threshold: no suffix.
	renderer.AddCompactionCostNotice(CompactionCostNotice{Kind: "branch_summary", Usage: ai.Usage{
		Input: 100, Cost: ai.UsageCost{Total: 0.001},
	}})
	lines = strings.Join(renderChat(t, renderer.Chat), "\n")
	if !strings.Contains(lines, "Branch summary: 100 tokens billed") || strings.Contains(lines, "Branch summary: 100 tokens billed (~") {
		t.Fatalf("branch notice = %q", lines)
	}
}

// TestTranscriptThinkingDropNotice covers the dropped-thinking diagnostics.
func TestTranscriptThinkingDropNotice(t *testing.T) {
	renderer, settings := newTranscriptTestRenderer(t, false)
	settings.SetShowCacheMissNotices(true)

	diagnostic := func(count int) ai.AssistantMessageDiagnostic {
		transformations := make([]map[string]any, 0, count)
		for i := 0; i < count; i++ {
			transformations = append(transformations, map[string]any{"type": "thinking_dropped"})
		}
		details, _ := json.Marshal(map[string]any{"transformations": transformations})
		return ai.AssistantMessageDiagnostic{Type: "anthropic_input_transformations", Details: details}
	}

	message := &ai.AssistantMessage{Diagnostics: []ai.AssistantMessageDiagnostic{diagnostic(2)}}
	if got := CountDroppedThinkingBlocks(message); got != 2 {
		t.Fatalf("dropped = %d", got)
	}
	other := &ai.AssistantMessage{Diagnostics: []ai.AssistantMessageDiagnostic{{Type: "other"}}}
	if got := CountDroppedThinkingBlocks(other); got != 0 {
		t.Fatalf("dropped other = %d", got)
	}

	// With an empty branch the previous count is zero, so the notice shows.
	renderer.SessionInfo = coding.NewSessionManager("/tmp/proj", &coding.SessionManagerOptions{Persist: boolPtr(false)})
	renderer.MaybeShowThinkingDropNotice(message)
	lines := strings.Join(renderChat(t, renderer.Chat), "\n")
	if !strings.Contains(lines, "Anthropic dropped 2 thinking blocks") {
		t.Fatalf("notice = %q", lines)
	}

	// With a branch whose last assistant already dropped >= count: no notice.
	dir := t.TempDir()
	manager := coding.NewSessionManager("/tmp/proj", &coding.SessionManagerOptions{
		SessionDir: dir, Persist: boolPtr(false),
	})
	manager.AppendMessage(ai.Message(&ai.AssistantMessage{Diagnostics: []ai.AssistantMessageDiagnostic{diagnostic(2)}}))
	renderer2 := NewTranscriptRenderer(&tui.Container{}, nil, settings, nil, manager)
	renderer2.MaybeShowThinkingDropNotice(message)
	if len(renderer2.Chat.Children) != 0 {
		t.Fatal("notice shown when the previous count matched")
	}
}

// TestTranscriptRenderSessionEntries covers the entry projection.
func TestTranscriptRenderSessionEntries(t *testing.T) {
	renderer, settings := newTranscriptTestRenderer(t, false)
	settings.SetShowCacheMissNotices(true)

	usage := ai.Usage{Input: 10, Output: 5, Cost: ai.UsageCost{Total: 0.5}}
	entries := []coding.SessionEntry{
		{Type: "custom", CustomType: "note"},
		{Type: "usage", Kind: "cache_warm", Usage: &usage},
		{Type: "compaction", Summary: "compacted", TokensBefore: 1000, Usage: &usage},
	}
	// Custom entries without a renderer are skipped.
	renderer.RenderSessionEntries(entries, false, false)
	lines := strings.Join(renderChat(t, renderer.Chat), "\n")
	if !strings.Contains(lines, "Compaction: ") {
		t.Fatalf("missing compaction cost notice: %q", lines)
	}
	if !strings.Contains(lines, "[compaction]") {
		t.Fatalf("missing compaction summary: %q", lines)
	}
}

// transcriptTestSession implements TranscriptSession.
type transcriptTestSession struct {
	retryAttempt int
}

func (s *transcriptTestSession) GetToolRenderers(string) *ToolRenderers { return nil }
func (s *transcriptTestSession) GetRetryAttempt() int                   { return s.retryAttempt }
func (s *transcriptTestSession) GetModelPriceSource() coding.ModelPriceSource {
	return nil
}

// TestTranscriptToolCalls covers the assistant tool-call rendering and the
// pending-tool matching.
func TestTranscriptToolCalls(t *testing.T) {
	renderer, _ := newTranscriptTestRenderer(t, false)
	renderer.Session = &transcriptTestSession{}
	renderer.SessionInfo = coding.NewSessionManager("/tmp/proj", &coding.SessionManagerOptions{Persist: boolPtr(false)})

	arguments, _ := json.Marshal(map[string]any{"command": "ls"})
	assistant := &ai.AssistantMessage{
		Content:    ai.ContentList{ai.TextContent{Text: "running"}, ai.ToolCall{ID: "t1", Name: "bash", Arguments: arguments}},
		StopReason: ai.StopStop,
	}
	renderer.RenderSessionItems([]RenderSessionItem{{Message: assistant}}, false, false)
	if len(renderer.Chat.Children) != 2 { // assistant + tool component
		t.Fatalf("children = %d", len(renderer.Chat.Children))
	}
	if len(renderer.PendingTools()) != 1 {
		t.Fatalf("pending = %d", len(renderer.PendingTools()))
	}
	if _, ok := renderer.PendingTools()["t1"]; !ok {
		t.Fatal("pending tool missing")
	}

	// The matching tool result clears the pending tool.
	toolResult := &ai.ToolResultMessage{
		ToolCallID: "t1", ToolName: "bash",
		Content: ai.UserContentList{ai.TextContent{Text: "output"}},
	}
	renderer.RenderSessionItems([]RenderSessionItem{{Message: assistant}, {Message: toolResult}}, false, false)
	if len(renderer.PendingTools()) != 0 {
		t.Fatalf("pending after result = %d", len(renderer.PendingTools()))
	}

	// An aborted assistant with a tool call renders the aborted error.
	renderer.Session = &transcriptTestSession{retryAttempt: 2}
	aborted := &ai.AssistantMessage{
		Content:    ai.ContentList{ai.ToolCall{ID: "t2", Name: "bash", Arguments: arguments}},
		StopReason: ai.StopAborted,
	}
	renderer.RenderSessionItems([]RenderSessionItem{{Message: aborted}}, false, false)
	if len(renderer.PendingTools()) != 0 {
		t.Fatal("aborted tool should not stay pending")
	}
	lines := strings.Join(renderChat(t, renderer.Chat), "\n")
	if !strings.Contains(lines, "Aborted after 2 retry attempts") {
		t.Fatalf("aborted notice = %q", lines)
	}

	// An errored assistant renders the error message.
	errorMessage := "boom"
	errored := &ai.AssistantMessage{
		Content:      ai.ContentList{ai.ToolCall{ID: "t3", Name: "bash", Arguments: arguments}},
		StopReason:   ai.StopError,
		ErrorMessage: &errorMessage,
	}
	renderer.RenderSessionItems([]RenderSessionItem{{Message: errored}}, false, false)
	lines = strings.Join(renderChat(t, renderer.Chat), "\n")
	if !strings.Contains(lines, "boom") {
		t.Fatalf("error notice = %q", lines)
	}
}
