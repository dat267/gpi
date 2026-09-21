package interactive

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/dat267/pier/ai"
	"github.com/dat267/pier/coding"
	"github.com/dat267/pier/tui"
)

// TestTranscriptRendersSessionLoadedEditDiff covers the edit tool's diff for a
// session-loaded tool result: the details arrive as raw JSON from the session
// file (ai.ToolResultMessage.Details), and the renderer must decode them like
// upstream's dynamic typing does. Reproduces the missing-diff rendering where
// only the "edit <path>" header appeared.
func TestTranscriptRendersSessionLoadedEditDiff(t *testing.T) {
	SetCustomThemesDir(t.TempDir())
	SetRegisteredThemes(nil)
	SetTrueColorSupport(true)
	SetStyleColorsEnabled(true)
	InitTheme("dark", false)

	mgr := coding.NewSessionManager(t.TempDir(), nil)
	mgr.AppendMessage(&ai.UserMessage{Content: ai.StringOrBlocks{Text: "please edit README.md"}})
	args, _ := json.Marshal(map[string]any{
		"file_path": "/nonexistent/README.md",
		"edits":     []map[string]string{{"oldText": "old line\n", "newText": "new line\n"}},
	})
	mgr.AppendMessage(&ai.AssistantMessage{
		API: ai.APIAnthropicMessages, Provider: "anthropic", Model: "m",
		Content:    ai.ContentList{ai.ToolCall{ID: "toolu-1", Name: "edit", Arguments: args}},
		StopReason: ai.StopToolUse,
	})
	firstChangedLine := 3
	details, _ := json.Marshal(coding.EditToolDetails{
		Diff:             "--- a/README.md\n+++ b/README.md\n@@ -1 +1 @@\n-old line\n+new line",
		FirstChangedLine: &firstChangedLine,
	})
	mgr.AppendMessage(&ai.ToolResultMessage{
		ToolCallID: "toolu-1", ToolName: "edit",
		Content:   ai.UserContentList{ai.TextContent{Text: "Edit applied successfully"}},
		Details:   details,
		Timestamp: time.Now().UnixMilli(),
	})

	agentSession, err := coding.CreateAgentSession(context.Background(), &coding.CreateAgentSessionOptions{
		Cwd:             mgr.GetCwd(),
		AgentDir:        t.TempDir(),
		SessionManager:  mgr,
		SettingsManager: coding.NewInMemorySettingsManager(nil, coding.SettingsManagerCreateOptions{}),
	})
	if err != nil {
		t.Fatalf("agent session: %v", err)
	}
	r := &TranscriptRenderer{
		Chat:                &tui.Container{},
		SessionInfo:         mgr,
		Session:             &AppSession{AgentSession: agentSession.Session},
		HiddenThinkingLabel: "thinking",
	}
	r.RenderSessionEntries(mgr.BuildContextEntriesForLeaf(), false, false)
	time.Sleep(200 * time.Millisecond) // let the async preview settle

	ansi := regexp.MustCompile(`\x1b\[[0-9;]*m`)
	var rendered []string
	for _, child := range r.Chat.Children {
		for _, line := range child.Render(90) {
			rendered = append(rendered, ansi.ReplaceAllString(line, ""))
		}
	}
	joined := strings.Join(rendered, "\n")
	for _, want := range []string{"edit ", "--- a/README.md", "+new line"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("rendered transcript missing %q:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, `"file_path"`) {
		t.Fatalf("edit tool rendered the generic JSON fallback instead of the edit renderer:\n%s", joined)
	}
}
