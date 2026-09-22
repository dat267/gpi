package coding

import (
	"context"
	"strings"
	"testing"

	"github.com/dat267/pier/ai"
)

// Steering queued while streaming is built with BLOCK content (queueSteer):
// the dequeue-removal hook must match those messages too, not just
// Text-content ones, or the dock banner keeps showing the steered message
// after it is pushed into the transcript.
func TestSteerBlockContentRemovedOnMessageStart(t *testing.T) {
	dir := t.TempDir()
	session, err := NewAgentSession(&SessionConfig{
		Cwd: dir, Model: &ai.Model{ID: "mock", API: "openai-responses", Provider: "openai", ContextWindow: 200000},
		StreamFn: mockSessionStreamFn(createAssistantMessageT("r1"), createAssistantMessageT("r2")),
		Sessions: NewSessionManager(dir, &SessionManagerOptions{SessionDir: dir}),
	})
	if err != nil {
		t.Fatal(err)
	}

	var queueUpdates []string
	session.Subscribe(func(event *SessionEvent) {
		if event.Type == SessionQueueUpdate {
			queueUpdates = append(queueUpdates, strings.Join(event.Steering, "|"))
		}
	})

	// What queueSteer builds: block content, Text empty.
	session.Steer(&ai.UserMessage{Content: ai.StringOrBlocks{Blocks: ai.ContentList{ai.TextContent{Text: "steered blocks"}}}})
	if got := session.GetSteeringMessages(); len(got) != 1 || got[0] != "steered blocks" {
		t.Fatalf("queued = %v", got)
	}

	if err := session.PromptText(context.Background(), "start"); err != nil {
		t.Fatal(err)
	}
	if len(session.steeringMessages) != 0 {
		t.Fatalf("steering display queue = %v", session.steeringMessages)
	}
	if len(queueUpdates) < 2 || queueUpdates[len(queueUpdates)-1] != "" {
		t.Fatalf("final queue update = %v", queueUpdates)
	}
}
