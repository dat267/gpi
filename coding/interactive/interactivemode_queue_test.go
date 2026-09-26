package interactive

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/dat267/pier/ai"
	"github.com/dat267/pier/coding"
	"github.com/dat267/pier/tui"
)

// queueTestSession implements QueueSession.
type queueTestSession struct {
	steering []string
	followUp []string

	prompted      []string
	steered       []string
	followed      []string
	abortCalls    int
	cleared       int
	thinking      ai.ThinkingLevel
	promptErr     error
	streaming     bool
	compacting    bool
	promptOptions []*coding.PromptOptions
}

func (s *queueTestSession) IsStreaming() bool  { return s.streaming }
func (s *queueTestSession) IsCompacting() bool { return s.compacting }

func (s *queueTestSession) GetSteeringMessages() []string { return append([]string{}, s.steering...) }
func (s *queueTestSession) GetFollowUpMessages() []string { return append([]string{}, s.followUp...) }
func (s *queueTestSession) ClearQueue() ([]string, []string) {
	s.cleared++
	steering, followUp := s.steering, s.followUp
	s.steering, s.followUp = nil, nil
	return steering, followUp
}
func (s *queueTestSession) Prompt(_ context.Context, text string, options *coding.PromptOptions) error {
	s.prompted = append(s.prompted, text)
	s.promptOptions = append(s.promptOptions, options)
	return s.promptErr
}
func (s *queueTestSession) Steer(message ai.Message) {
	if user, ok := message.(*ai.UserMessage); ok {
		s.steered = append(s.steered, user.Content.Text)
	}
}
func (s *queueTestSession) FollowUp(message ai.Message) {
	if user, ok := message.(*ai.UserMessage); ok {
		s.followed = append(s.followed, user.Content.Text)
	}
}
func (s *queueTestSession) Abort(context.Context) { s.abortCalls++ }
func (s *queueTestSession) AbortAsync()           { s.abortCalls++ }
func (s *queueTestSession) CycleThinkingLevel(coding.ModelMutationOptions) (ai.ThinkingLevel, bool) {
	return s.thinking, s.thinking != ""
}
func (s *queueTestSession) CycleModel(context.Context, string, coding.ModelMutationOptions) (*coding.ModelCycleResult, error) {
	return nil, nil
}
func (s *queueTestSession) SupportsThinking() bool          { return true }
func (s *queueTestSession) ThinkingLevel() ai.ThinkingLevel { return s.thinking }

func newQueueTestController(t *testing.T) (*QueueController, *queueTestSession, *tui.Container) {
	t.Helper()
	SetCustomThemesDir(t.TempDir())
	SetRegisteredThemes(nil)
	SetTrueColorSupport(true)
	SetStyleColorsEnabled(true)
	InitTheme("dark", false)

	settings := coding.NewInMemorySettingsManager(nil, coding.SettingsManagerCreateOptions{})
	editor := NewCustomEditor(editorTestHost{}, tui.EditorTheme{}, NewAppKeybindingsManager(nil, ""), CustomEditorOptions{})
	chat := &tui.Container{}
	pending := &tui.Container{}
	session := &queueTestSession{thinking: "high"}
	controller := NewQueueController(nil, session, settings, editor, chat, pending)
	return controller, session, pending
}

// TestQueuePendingDisplay covers the pending-message rendering.
func TestQueuePendingDisplay(t *testing.T) {
	controller, session, pending := newQueueTestController(t)

	// Empty queues render nothing.
	controller.UpdatePendingMessagesDisplay()
	if len(pending.Children) != 0 {
		t.Fatalf("children = %d", len(pending.Children))
	}

	session.steering = []string{"steer one"}
	session.followUp = []string{"follow two"}
	controller.QueueCompactionMessage("queued during compaction", "steer")

	steering, followUp := controller.GetAllQueuedMessages()
	if len(steering) != 2 || steering[0] != "steer one" || steering[1] != "queued during compaction" {
		t.Fatalf("steering = %v", steering)
	}
	if len(followUp) != 1 || followUp[0] != "follow two" {
		t.Fatalf("followUp = %v", followUp)
	}

	controller.UpdatePendingMessagesDisplay()
	// spacer + 2 steering + 1 follow-up + hint
	if len(pending.Children) != 5 {
		t.Fatalf("children = %d", len(pending.Children))
	}
	lines := strings.Join(pending.Render(80), "\n")
	if !strings.Contains(lines, "Steering: steer one") || !strings.Contains(lines, "Follow-up: follow two") ||
		!strings.Contains(lines, "to edit all queued messages") {
		t.Fatalf("pending render = %q", lines)
	}
	// The queued compaction message cleared the editor and was recorded in
	// history.
	if controller.Editor.GetText() != "" {
		t.Fatalf("editor text = %q", controller.Editor.GetText())
	}
	controller.Editor.HandleInput("\x1b[A")
	if controller.Editor.GetText() != "queued during compaction" {
		t.Fatalf("history = %q", controller.Editor.GetText())
	}
}

// TestQueueRestore covers restoring the queued messages into the editor.
func TestQueueRestore(t *testing.T) {
	controller, session, _ := newQueueTestController(t)

	// Nothing queued: abort still runs.
	restored := controller.RestoreQueuedMessagesToEditor(true, "", false)
	if restored != 0 || session.abortCalls != 1 {
		t.Fatalf("restored = %d, aborts = %d", restored, session.abortCalls)
	}

	session.steering = []string{"one"}
	session.followUp = []string{"two"}
	controller.Editor.SetText("current draft")
	restored = controller.RestoreQueuedMessagesToEditor(false, "", false)
	if restored != 2 {
		t.Fatalf("restored = %d", restored)
	}
	if got := controller.Editor.GetText(); got != "one\n\ntwo\n\ncurrent draft" {
		t.Fatalf("editor = %q", got)
	}
	steering, followUp := controller.GetAllQueuedMessages()
	if len(steering) != 0 || len(followUp) != 0 {
		t.Fatal("queues not cleared")
	}

	// An explicit current text overrides the editor content.
	session.steering = []string{"x"}
	controller.Editor.SetText("ignored")
	controller.RestoreQueuedMessagesToEditor(false, "explicit", true)
	if got := controller.Editor.GetText(); got != "x\n\nexplicit" {
		t.Fatalf("editor = %q", got)
	}

	// Dequeue status messages.
	controller.HandleDequeue()
	controller.QueueCompactionMessage("later", "followUp")
	controller.HandleDequeue()
}

// TestQueueFlush covers the compaction-queue flush paths.
func TestQueueFlush(t *testing.T) {
	controller, session, _ := newQueueTestController(t)

	// Empty queue: no-op.
	controller.FlushCompactionQueue(context.Background(), false)
	if len(session.prompted) != 0 {
		t.Fatalf("prompted = %v", session.prompted)
	}

	// willRetry queues everything through steer/followUp.
	controller.QueueCompactionMessage("steer me", "steer")
	controller.QueueCompactionMessage("follow me", "followUp")
	controller.FlushCompactionQueue(context.Background(), true)
	if len(session.steered) != 1 || session.steered[0] != "steer me" {
		t.Fatalf("steered = %v", session.steered)
	}
	if len(session.followed) != 1 || session.followed[0] != "follow me" {
		t.Fatalf("followed = %v", session.followed)
	}

	// The first non-extension message is prompted; the rest are queued.
	controller.QueueCompactionMessage("prompt me", "steer")
	controller.QueueCompactionMessage("then follow", "followUp")
	controller.FlushCompactionQueue(context.Background(), false)
	if len(session.prompted) != 1 || session.prompted[0] != "prompt me" {
		t.Fatalf("prompted = %v", session.prompted)
	}
	if len(session.followed) != 2 || session.followed[1] != "then follow" {
		t.Fatalf("followed = %v", session.followed)
	}

	// A prompt error restores the queue.
	session.promptErr = errors.New("boom")
	errorsShown := []string{}
	controller.ShowError = func(message string) { errorsShown = append(errorsShown, message) }
	controller.QueueCompactionMessage("failing", "steer")
	controller.FlushCompactionQueue(context.Background(), false)
	if len(errorsShown) != 1 || !strings.Contains(errorsShown[0], "Failed to send queued message: boom") {
		t.Fatalf("errors = %v", errorsShown)
	}
	if queued := controller.CompactionQueuedMessages(); len(queued) != 1 || queued[0].Text != "failing" {
		t.Fatalf("queue = %v", queued)
	}
}

// TestQueueToggles covers the expansion/thinking/border actions.
func TestQueueToggles(t *testing.T) {
	controller, _, _ := newQueueTestController(t)
	statuses := []string{}
	controller.ShowStatus = func(message string) { statuses = append(statuses, message) }

	// Tool expansion flips the state and the expandable children.
	chat := controller.Chat
	expandable := NewExpandableText(func() string { return "collapsed" }, func() string { return "expanded" }, false, 0, 0)
	chat.AddChild(expandable)
	expanded := false
	controller.ToggleToolOutputExpansion(&expanded, func(value bool) {
		controller.SetToolsExpanded(value, &expanded, nil, nil)
	})
	if !expanded {
		t.Fatal("not expanded")
	}
	if lines := expandable.Render(20); strings.TrimSpace(lines[0]) != "expanded" {
		t.Fatalf("expandable = %v", lines)
	}
	if len(statuses) != 1 || statuses[0] != "Tool output: expanded" {
		t.Fatalf("statuses = %v", statuses)
	}

	// Thinking-block visibility.
	assistant := NewAssistantMessageComponent(nil, false, nil, "", 0, nil)
	chat.AddChild(assistant)
	hideThinking := false
	controller.ToggleThinkingBlockVisibility(&hideThinking)
	if !hideThinking {
		t.Fatal("thinking not hidden")
	}
	if !assistant.hideThinkingBlock {
		t.Fatal("assistant not updated")
	}
	if statuses[len(statuses)-1] != "Thinking blocks: hidden" {
		t.Fatalf("statuses = %v", statuses)
	}
	if !controller.Settings.GetHideThinkingBlock() {
		t.Fatal("setting not persisted")
	}

	// Bash mode border color.
	controller.SetBashMode(true)
	if !controller.IsBashMode() || controller.Editor.BorderColor == nil {
		t.Fatal("bash mode not applied")
	}
	controller.SetBashMode(false)

	// Thinking level cycling.
	controller.CycleThinkingLevel()
	if statuses[len(statuses)-1] != "Thinking level: high" {
		t.Fatalf("statuses = %v", statuses)
	}
}

// TestQueuePendingBashComponents covers moving bash components to the chat.
func TestQueuePendingBashComponents(t *testing.T) {
	controller, _, pending := newQueueTestController(t)
	component := tui.NewText("bash", 0, 0, nil)
	pending.AddChild(component)
	controller.PendingBash = []tui.Component{component}

	controller.FlushPendingBashComponents()
	if len(pending.Children) != 0 {
		t.Fatal("pending not cleared")
	}
	if len(controller.Chat.Children) != 1 {
		t.Fatal("chat not updated")
	}
	if len(controller.PendingBash) != 0 {
		t.Fatal("pending list not cleared")
	}
}

// TestHandleFollowUpQueuesWhileStreaming covers alt+enter during a run: the
// editor text becomes a queued follow-up (streamingBehavior followUp), the
// editor is cleared, and the pending display is refreshed.
func TestHandleFollowUpQueuesWhileStreaming(t *testing.T) {
	controller, session, pending := newQueueTestController(t)
	session.streaming = true
	controller.Editor.SetText("  a queued follow-up  ")

	controller.HandleFollowUp(context.Background())

	if len(session.prompted) != 1 || session.prompted[0] != "a queued follow-up" {
		t.Fatalf("prompted = %v", session.prompted)
	}
	if len(session.promptOptions) != 1 || session.promptOptions[0] == nil ||
		session.promptOptions[0].StreamingBehavior != "followUp" {
		t.Fatalf("prompt options = %#v", session.promptOptions)
	}
	if controller.Editor.GetText() != "" {
		t.Fatalf("editor = %q; want cleared", controller.Editor.GetText())
	}
	if session.cleared != 0 {
		t.Fatal("queueing a follow-up must not clear the session queues")
	}
	_ = pending
}

// TestHandleFollowUpSubmitsWhenIdle: with nothing running, alt+enter acts like
// Enter instead of queueing.
func TestHandleFollowUpSubmitsWhenIdle(t *testing.T) {
	controller, session, _ := newQueueTestController(t)
	submitted := ""
	controller.Editor.OnSubmit = func(text string) { submitted = text }
	controller.Editor.SetText("just submit me")

	controller.HandleFollowUp(context.Background())

	if submitted != "just submit me" {
		t.Fatalf("submitted = %q", submitted)
	}
	if len(session.prompted) != 0 {
		t.Fatalf("prompted = %v; want no queueing when idle", session.prompted)
	}
	if controller.Editor.GetText() != "" {
		t.Fatalf("editor = %q; want cleared", controller.Editor.GetText())
	}
}

// TestHandleFollowUpQueuesDuringCompaction: a compaction queues the follow-up
// for after it finishes (upstream queueCompactionMessage).
func TestHandleFollowUpQueuesDuringCompaction(t *testing.T) {
	controller, session, _ := newQueueTestController(t)
	session.compacting = true
	statuses := []string{}
	controller.ShowStatus = func(message string) { statuses = append(statuses, message) }
	controller.Editor.SetText("for after the compaction")

	controller.HandleFollowUp(context.Background())

	queued := controller.CompactionQueuedMessages()
	if len(queued) != 1 || queued[0].Text != "for after the compaction" || queued[0].Mode != "followUp" {
		t.Fatalf("queued = %#v", queued)
	}
	if len(session.prompted) != 0 {
		t.Fatalf("prompted = %v; want the compaction queue", session.prompted)
	}
	if len(statuses) != 1 || !strings.Contains(statuses[0], "after compaction") {
		t.Fatalf("statuses = %v", statuses)
	}
	if controller.Editor.GetText() != "" {
		t.Fatalf("editor = %q; want cleared", controller.Editor.GetText())
	}
}

// TestHandleFollowUpIgnoresEmptyText: whitespace-only input does nothing.
func TestHandleFollowUpIgnoresEmptyText(t *testing.T) {
	controller, session, _ := newQueueTestController(t)
	session.streaming = true
	controller.Editor.SetText("   \n  ")

	controller.HandleFollowUp(context.Background())

	if len(session.prompted) != 0 {
		t.Fatalf("prompted = %v", session.prompted)
	}
}

// TestHandleDequeueRestoresQueuedMessages covers alt+up: the queued messages go
// back into the editor with a status line (upstream handleDequeue).
func TestHandleDequeueRestoresQueuedMessages(t *testing.T) {
	controller, session, _ := newQueueTestController(t)
	statuses := []string{}
	controller.ShowStatus = func(message string) { statuses = append(statuses, message) }
	session.steering = []string{"steer me"}
	session.followUp = []string{"follow me"}

	controller.HandleDequeue()

	text := controller.Editor.GetText()
	if !strings.Contains(text, "steer me") || !strings.Contains(text, "follow me") {
		t.Fatalf("editor = %q", text)
	}
	if len(statuses) != 1 || statuses[0] != "Restored 2 queued messages to editor" {
		t.Fatalf("statuses = %v", statuses)
	}
}

// TestHandleDequeueWithoutQueuedMessages reports that there is nothing to
// restore.
func TestHandleDequeueWithoutQueuedMessages(t *testing.T) {
	controller, _, _ := newQueueTestController(t)
	statuses := []string{}
	controller.ShowStatus = func(message string) { statuses = append(statuses, message) }

	controller.HandleDequeue()

	if len(statuses) != 1 || statuses[0] != "No queued messages to restore" {
		t.Fatalf("statuses = %v", statuses)
	}
}
