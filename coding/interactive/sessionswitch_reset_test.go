package interactive

import (
	"context"
	"strings"
	"testing"

	"github.com/dat267/pier/ai"
	"github.com/dat267/pier/tui"
)

// A session replacement resets the view (upstream rebindCurrentSession calls
// renderCurrentSessionState: clear the loaded-resources container, the chat, the
// pending-message container, the streaming component and the pending tools, then
// renderInitialMessages). Switching re-asks the new project's trust and redraws
// the untrusted-project warning, because the initial render is what draws it.
func TestSwitchSessionResetsTheTranscriptState(t *testing.T) {
	app, cleanup := newTestApp(t)
	defer cleanup()

	// State a previous session left behind.
	app.LoadedResourcesContainer.AddChild(tui.NewText("old skill", 0, 0, nil))
	app.PendingMessages.AddChild(tui.NewText("queued while compacting", 0, 0, nil))
	app.Transcript.StreamingComponent = NewAssistantMessageComponent(
		&ai.AssistantMessage{Content: ai.ContentList{ai.TextContent{Text: "half"}}},
		false, app.Transcript.MarkdownTheme, "", 1, nil)
	app.Transcript.pendingTools["stale"] = &ToolExecutionComponent{}
	app.Queue.compactionQueuedMessages = []CompactionQueuedMessage{{Text: "queued for compaction", Mode: "steer"}}

	project := makeTrustRequiringProject(t, "alpha-theme")
	target := makePersistedSession(t, project, "from the other project")

	if _, err := app.SwitchSession(context.Background(), target.GetSessionFile(), ""); err != nil {
		t.Fatalf("SwitchSession: %v", err)
	}

	if len(app.LoadedResourcesContainer.Children) != 0 {
		t.Errorf("loaded resources kept %d children", len(app.LoadedResourcesContainer.Children))
	}
	if len(app.PendingMessages.Children) != 0 {
		t.Errorf("pending messages kept %d children", len(app.PendingMessages.Children))
	}
	if app.Transcript.StreamingComponent != nil {
		t.Error("the streaming component survived the switch")
	}
	if len(app.Queue.compactionQueuedMessages) != 0 {
		t.Errorf("compaction queue survived the switch: %v", app.Queue.compactionQueuedMessages)
	}
	if len(app.Transcript.pendingTools) != 0 {
		t.Errorf("pending tools survived the switch: %v", app.Transcript.pendingTools)
	}

	// The untrusted project's warning comes back with the initial render — the
	// switch re-resolved trust for that directory (D160).
	rendered := app.Chat.Render(120)
	if !strings.Contains(strings.Join(rendered, "\n"), "This project is not trusted") {
		t.Errorf("no trust warning after switching into an untrusted project:\n%s", strings.Join(rendered, "\n"))
	}
	// And the new session's messages are on screen.
	if !strings.Contains(strings.Join(rendered, "\n"), "from the other project") {
		t.Errorf("the switched-to session was not rendered:\n%s", strings.Join(rendered, "\n"))
	}
}
