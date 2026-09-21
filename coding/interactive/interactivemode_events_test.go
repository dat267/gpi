package interactive

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/dat267/pier/agent"
	"github.com/dat267/pier/ai"
	"github.com/dat267/pier/coding"
	"github.com/dat267/pier/tui"
)

// eventTestSession implements EventSession.
type eventTestSession struct {
	retryAttempt      int
	abortRetryCalls   int
	abortCompactCalls int
}

func (s *eventTestSession) RetryAttempt() int { return s.retryAttempt }
func (s *eventTestSession) AbortRetry()       { s.abortRetryCalls++ }
func (s *eventTestSession) AbortCompaction()  { s.abortCompactCalls++ }
func (s *eventTestSession) IsStreaming() bool { return false }

func newEventTestDispatcher(t *testing.T) (*EventDispatcher, *eventTestSession, *TranscriptRenderer, *InteractiveUIState) {
	t.Helper()
	SetCustomThemesDir(t.TempDir())
	SetRegisteredThemes(nil)
	SetTrueColorSupport(true)
	SetStyleColorsEnabled(true)
	InitTheme("dark", false)

	settings := coding.NewInMemorySettingsManager(nil, coding.SettingsManagerCreateOptions{})
	chat := &tui.Container{}
	transcript := NewTranscriptRenderer(chat, nil, settings, nil, nil)
	screen := tui.NewMainScreen(&fakeRendererTerminal{width: 80, height: 24}, false, "")
	screen.DisableAutoRender()
	uiState := NewInteractiveUIState(screen)
	uiState.StatusContainer = &tui.Container{}
	session := &eventTestSession{}
	dispatcher := NewEventDispatcher(transcript, uiState, nil, settings, session, nil, nil)
	return dispatcher, session, transcript, uiState
}

func agentEvent(eventType string, message ai.Message) *agent.AgentEvent {
	return &agent.AgentEvent{Type: eventType, Message: message}
}

// TestEventDispatcherLifecycle covers the agent/turn lifecycle events.
func TestEventDispatcherLifecycle(t *testing.T) {
	dispatcher, _, _, uiState := newEventTestDispatcher(t)

	initCalls := 0
	dispatcher.Init = func() { initCalls++ }
	progress := []bool{}
	dispatcher.TerminalProgress = func(active bool) { progress = append(progress, active) }

	dispatcher.HandleEvent(&coding.SessionEvent{Type: coding.SessionAgentStart})
	if initCalls != 1 {
		t.Fatalf("init calls = %d", initCalls)
	}
	// A second event does not re-init.
	dispatcher.HandleEvent(&coding.SessionEvent{Type: coding.SessionAgentStart})
	if initCalls != 1 {
		t.Fatalf("init calls = %d", initCalls)
	}

	dispatcher.Settings.SetShowTerminalProgress(true)
	dispatcher.HandleEvent(&coding.SessionEvent{Type: coding.SessionTurnStart})
	if uiState.ActiveStatusIndicator == nil || uiState.ActiveStatusIndicator.IndicatorKind() != StatusWorking {
		t.Fatal("working indicator not shown")
	}
	uiState.WorkingVisible = false
	dispatcher.HandleEvent(&coding.SessionEvent{Type: coding.SessionTurnStart})
	if uiState.ActiveStatusIndicator != nil {
		t.Fatal("working indicator not cleared")
	}
	uiState.WorkingVisible = true

	settled := 0
	dispatcher.CheckShutdownRequested = func() { settled++ }
	dispatcher.HandleEvent(&coding.SessionEvent{Type: coding.SessionAgentSettled})
	if settled != 1 {
		t.Fatalf("settled = %d", settled)
	}

	dispatcher.HandleEvent(&coding.SessionEvent{Type: coding.SessionAgentEnd})
	if got := progress; len(got) != 3 || got[0] != true || got[1] != true || got[2] != false {
		t.Fatalf("progress = %v", got)
	}
}

// TestEventDispatcherMessages covers the message lifecycle.
func TestEventDispatcherMessages(t *testing.T) {
	dispatcher, _, transcript, _ := newEventTestDispatcher(t)

	// User message start renders into the chat.
	dispatcher.HandleEvent(&coding.SessionEvent{
		Type:  coding.SessionMessageStart,
		Agent: agentEvent("message_start", &ai.UserMessage{Content: ai.StringOrBlocks{Text: "hello"}}),
	})
	if len(transcript.Chat.Children) != 1 {
		t.Fatalf("children = %d", len(transcript.Chat.Children))
	}

	// Assistant start creates the streaming component.
	assistant := &ai.AssistantMessage{Content: ai.ContentList{ai.TextContent{Text: "hi"}}, StopReason: ai.StopStop}
	dispatcher.HandleEvent(&coding.SessionEvent{
		Type:  coding.SessionMessageStart,
		Agent: agentEvent("message_start", assistant),
	})
	if dispatcher.StreamingComponent() == nil {
		t.Fatal("streaming component missing")
	}
	if transcript.StreamingComponent != dispatcher.StreamingComponent() {
		t.Fatal("transcript streaming component not synced")
	}

	// A message update with a tool call adds a tool component.
	arguments, _ := json.Marshal(map[string]any{"command": "ls"})
	withTool := &ai.AssistantMessage{
		Content:    ai.ContentList{ai.TextContent{Text: "running"}, ai.ToolCall{ID: "t1", Name: "bash", Arguments: arguments}},
		StopReason: ai.StopStop,
	}
	dispatcher.HandleEvent(&coding.SessionEvent{
		Type:  coding.SessionMessageUpdate,
		Agent: agentEvent("message_update", withTool),
	})
	if len(dispatcher.PendingTools()) != 1 {
		t.Fatalf("pending = %d", len(dispatcher.PendingTools()))
	}
	// A second update with different args reuses the component.
	moreArgs, _ := json.Marshal(map[string]any{"command": "ls -la"})
	updated := &ai.AssistantMessage{
		Content:    ai.ContentList{ai.ToolCall{ID: "t1", Name: "bash", Arguments: moreArgs}},
		StopReason: ai.StopStop,
	}
	dispatcher.HandleEvent(&coding.SessionEvent{
		Type:  coding.SessionMessageUpdate,
		Agent: agentEvent("message_update", updated),
	})
	if len(dispatcher.PendingTools()) != 1 {
		t.Fatalf("pending after update = %d", len(dispatcher.PendingTools()))
	}

	// message_end clears the streaming state.
	dispatcher.HandleEvent(&coding.SessionEvent{
		Type:  coding.SessionMessageEnd,
		Agent: agentEvent("message_end", updated),
	})
	if dispatcher.StreamingComponent() != nil {
		t.Fatal("streaming component not cleared")
	}
	// A successful stop keeps the tools pending until their result arrives
	// (upstream only marks the args complete).
	if len(dispatcher.PendingTools()) != 1 {
		t.Fatalf("pending after end = %d", len(dispatcher.PendingTools()))
	}
	lines := strings.Join(renderChat(t, transcript.Chat), "\n")
	if !strings.Contains(lines, "bash") {
		t.Fatalf("tool render = %q", lines)
	}

	// User message_end is ignored.
	before := len(transcript.Chat.Children)
	dispatcher.HandleEvent(&coding.SessionEvent{
		Type:  coding.SessionMessageEnd,
		Agent: agentEvent("message_end", &ai.UserMessage{Content: ai.StringOrBlocks{Text: "x"}}),
	})
	if len(transcript.Chat.Children) != before {
		t.Fatal("user message_end changed the chat")
	}
}

// TestEventDispatcherAbortedMessage covers the aborted/error synthesis.
func TestEventDispatcherAbortedMessage(t *testing.T) {
	dispatcher, session, transcript, _ := newEventTestDispatcher(t)
	session.retryAttempt = 3

	assistant := &ai.AssistantMessage{Content: ai.ContentList{}, StopReason: ai.StopStop}
	dispatcher.HandleEvent(&coding.SessionEvent{
		Type: coding.SessionMessageStart, Agent: agentEvent("message_start", assistant),
	})
	arguments, _ := json.Marshal(map[string]any{"command": "ls"})
	withTool := &ai.AssistantMessage{
		Content:    ai.ContentList{ai.ToolCall{ID: "t1", Name: "bash", Arguments: arguments}},
		StopReason: ai.StopAborted,
	}
	dispatcher.HandleEvent(&coding.SessionEvent{
		Type: coding.SessionMessageUpdate, Agent: agentEvent("message_update", withTool),
	})
	if len(dispatcher.PendingTools()) != 1 {
		t.Fatalf("pending = %d", len(dispatcher.PendingTools()))
	}
	dispatcher.HandleEvent(&coding.SessionEvent{
		Type: coding.SessionMessageEnd, Agent: agentEvent("message_end", withTool),
	})
	if len(dispatcher.PendingTools()) != 0 {
		t.Fatal("pending tools not cleared on abort")
	}
	lines := strings.Join(renderChat(t, transcript.Chat), "\n")
	if !strings.Contains(lines, "Aborted after 3 retry attempts") {
		t.Fatalf("aborted render = %q", lines)
	}

	// Error stop suggests a bug report.
	suggested := 0
	dispatcher.SuggestBugReport = func() { suggested++ }
	errorMessage := "boom"
	errored := &ai.AssistantMessage{Content: ai.ContentList{}, StopReason: ai.StopError, ErrorMessage: &errorMessage}
	dispatcher.HandleEvent(&coding.SessionEvent{Type: coding.SessionMessageStart, Agent: agentEvent("message_start", errored)})
	dispatcher.HandleEvent(&coding.SessionEvent{Type: coding.SessionMessageEnd, Agent: agentEvent("message_end", errored)})
	if suggested != 1 {
		t.Fatalf("bug report suggestions = %d", suggested)
	}
}

// TestEventDispatcherToolExecution covers the tool execution events.
func TestEventDispatcherToolExecution(t *testing.T) {
	dispatcher, _, transcript, _ := newEventTestDispatcher(t)

	arguments, _ := json.Marshal(map[string]any{"command": "ls"})
	dispatcher.HandleEvent(&coding.SessionEvent{
		Type: coding.SessionToolExecutionStart,
		Agent: &agent.AgentEvent{
			Type: "tool_execution_start", ToolCallID: "t1", ToolName: "bash", Args: arguments,
		},
	})
	if len(dispatcher.PendingTools()) != 1 {
		t.Fatalf("pending = %d", len(dispatcher.PendingTools()))
	}

	dispatcher.HandleEvent(&coding.SessionEvent{
		Type: coding.SessionToolExecutionUpdate,
		Agent: &agent.AgentEvent{
			Type: "tool_execution_update", ToolCallID: "t1",
			PartialResult: agent.AgentToolResult{Content: []ai.Content{ai.TextContent{Text: "partial"}}},
		},
	})
	lines := strings.Join(renderChat(t, transcript.Chat), "\n")
	if !strings.Contains(lines, "partial") {
		t.Fatalf("partial render = %q", lines)
	}

	dispatcher.HandleEvent(&coding.SessionEvent{
		Type: coding.SessionToolExecutionEnd,
		Agent: &agent.AgentEvent{
			Type: "tool_execution_end", ToolCallID: "t1",
			Result:  agent.AgentToolResult{Content: []ai.Content{ai.TextContent{Text: "done"}}},
			IsError: false,
		},
	})
	if len(dispatcher.PendingTools()) != 0 {
		t.Fatal("pending tools not cleared")
	}
	lines = strings.Join(renderChat(t, transcript.Chat), "\n")
	if !strings.Contains(lines, "done") {
		t.Fatalf("result render = %q", lines)
	}
}

// TestEventDispatcherEntryAppended covers the entry_appended routing.
func TestEventDispatcherEntryAppended(t *testing.T) {
	dispatcher, _, transcript, _ := newEventTestDispatcher(t)
	dispatcher.Settings.SetShowCacheMissNotices(true)

	// A custom entry without a renderer is skipped.
	dispatcher.HandleEvent(&coding.SessionEvent{
		Type: coding.SessionEntryAppended, Entry: &coding.SessionEntry{Type: "custom", CustomType: "note"},
	})
	if len(transcript.Chat.Children) != 0 {
		t.Fatal("custom entry rendered without a renderer")
	}

	// A cache-warm usage entry renders a line.
	dispatcher.HandleEvent(&coding.SessionEvent{
		Type:  coding.SessionEntryAppended,
		Entry: &coding.SessionEntry{Type: "usage", Kind: "cache_warm", Usage: &ai.Usage{Input: 1}},
	})
	if len(transcript.Chat.Children) != 2 { // spacer + text
		t.Fatalf("children = %d", len(transcript.Chat.Children))
	}
}

// TestEventDispatcherRetries covers the retry and summarization events.
func TestEventDispatcherRetries(t *testing.T) {
	dispatcher, session, _, uiState := newEventTestDispatcher(t)
	editor := NewCustomEditor(editorTestHost{}, tui.EditorTheme{}, NewAppKeybindingsManager(nil, ""), CustomEditorOptions{})
	dispatcher.Editor = editor
	baseEscape := func() {}
	editor.OnEscape = baseEscape

	dispatcher.HandleEvent(&coding.SessionEvent{Type: coding.SessionAutoRetryStart, Attempt: 1, MaxAttempts: 3, DelayMS: 100})
	if uiState.ActiveStatusIndicator == nil || uiState.ActiveStatusIndicator.IndicatorKind() != StatusRetry {
		t.Fatal("retry indicator not shown")
	}
	editor.OnEscape()
	if session.abortRetryCalls != 1 {
		t.Fatalf("abort retry calls = %d", session.abortRetryCalls)
	}

	dispatcher.HandleEvent(&coding.SessionEvent{Type: coding.SessionAutoRetryEnd, Attempt: 3, Success: false, ErrorMessage: "nope"})
	if uiState.ActiveStatusIndicator != nil {
		t.Fatal("retry indicator not cleared")
	}
	// The original escape handler is restored.
	restored := 0
	editor.OnEscape = func() { restored++ }
	dispatcher.HandleEvent(&coding.SessionEvent{Type: coding.SessionAutoRetryStart, Attempt: 1, MaxAttempts: 3})
	dispatcher.HandleEvent(&coding.SessionEvent{Type: coding.SessionAutoRetryEnd, Attempt: 1, Success: true})
	editor.OnEscape()
	if restored != 1 {
		t.Fatalf("restored handler calls = %d", restored)
	}

	// Summarization retry events drive the status indicators.
	dispatcher.HandleEvent(&coding.SessionEvent{Type: coding.SessionSummarizationRetryScheduled, Attempt: 1, MaxAttempts: 2, DelayMS: 50, ErrorMessage: "boom"})
	if uiState.ActiveStatusIndicator == nil || uiState.ActiveStatusIndicator.IndicatorKind() != StatusRetry {
		t.Fatal("summarization retry indicator not shown")
	}
	dispatcher.HandleEvent(&coding.SessionEvent{Type: coding.SessionSummarizationRetryAttemptStart, Source: "branchSummary"})
	if uiState.ActiveStatusIndicator == nil || uiState.ActiveStatusIndicator.IndicatorKind() != StatusBranchSummary {
		t.Fatalf("branch summary indicator = %v", uiState.ActiveStatusIndicator)
	}
	dispatcher.HandleEvent(&coding.SessionEvent{Type: coding.SessionSummarizationRetryAttemptStart, Source: "compaction", Reason: coding.CompactionThreshold})
	if uiState.ActiveStatusIndicator == nil || uiState.ActiveStatusIndicator.IndicatorKind() != StatusCompaction {
		t.Fatalf("compaction indicator = %v", uiState.ActiveStatusIndicator)
	}
	// summarization_retry_finished only clears a *retry* indicator; the
	// compaction/branch-summary indicator stays until its own end event
	// (upstream's kind filter behaves the same).
	dispatcher.HandleEvent(&coding.SessionEvent{Type: coding.SessionSummarizationRetryFinished})
	if uiState.ActiveStatusIndicator == nil || uiState.ActiveStatusIndicator.IndicatorKind() != StatusCompaction {
		t.Fatalf("indicator after finished = %v", uiState.ActiveStatusIndicator)
	}
}

// TestEventDispatcherCompaction covers the compaction events.
func TestEventDispatcherCompaction(t *testing.T) {
	dispatcher, session, transcript, uiState := newEventTestDispatcher(t)
	editor := NewCustomEditor(editorTestHost{}, tui.EditorTheme{}, NewAppKeybindingsManager(nil, ""), CustomEditorOptions{})
	dispatcher.Editor = editor

	dispatcher.HandleEvent(&coding.SessionEvent{Type: coding.SessionCompactionStart, Reason: coding.CompactionManual})
	if uiState.ActiveStatusIndicator == nil || uiState.ActiveStatusIndicator.IndicatorKind() != StatusCompaction {
		t.Fatal("compaction indicator not shown")
	}
	editor.OnEscape()
	if session.abortCompactCalls != 1 {
		t.Fatalf("abort compaction calls = %d", session.abortCompactCalls)
	}

	errors := []string{}
	dispatcher.ShowError = func(message string) { errors = append(errors, message) }
	dispatcher.HandleEvent(&coding.SessionEvent{Type: coding.SessionCompactionEnd, Reason: coding.CompactionManual, Aborted: true})
	if uiState.ActiveStatusIndicator != nil {
		t.Fatal("compaction indicator not cleared")
	}
	if len(errors) != 1 || errors[0] != "Compaction cancelled" {
		t.Fatalf("errors = %v", errors)
	}

	// Auto-compaction cancellation shows a status instead.
	dispatcher.HandleEvent(&coding.SessionEvent{Type: coding.SessionCompactionStart, Reason: coding.CompactionThreshold})
	dispatcher.HandleEvent(&coding.SessionEvent{Type: coding.SessionCompactionEnd, Reason: coding.CompactionThreshold, Aborted: true})
	lines := strings.Join(renderChat(t, transcript.Chat), "\n")
	if !strings.Contains(lines, "Auto-compaction cancelled") {
		t.Fatalf("status = %q", lines)
	}

	// An error message is rendered in the chat for auto-compaction.
	dispatcher.HandleEvent(&coding.SessionEvent{Type: coding.SessionCompactionStart, Reason: coding.CompactionOverflow})
	dispatcher.HandleEvent(&coding.SessionEvent{Type: coding.SessionCompactionEnd, Reason: coding.CompactionOverflow, ErrorMessage: "overflow failed"})
	lines = strings.Join(renderChat(t, transcript.Chat), "\n")
	if !strings.Contains(lines, "overflow failed") {
		t.Fatalf("error render = %q", lines)
	}

	// A successful compaction rebuilds the chat from the session context. The
	// context's first entry must be the latest compaction.
	manager := coding.NewSessionManager("/tmp/proj", &coding.SessionManagerOptions{Persist: boolPtr(false)})
	manager.AppendMessage(ai.Message(&ai.UserMessage{Content: ai.StringOrBlocks{Text: "old"}}))
	manager.AppendCompaction("earlier", "", 10, nil, false, nil)
	dispatcher.SessionInfo = manager
	dispatcher.Transcript.SessionInfo = manager
	flushed := []bool{}
	dispatcher.FlushCompactionQueue = func(willRetry bool) { flushed = append(flushed, willRetry) }

	dispatcher.Settings.SetShowCacheMissNotices(true)
	usage := ai.Usage{Input: 100, Cost: ai.UsageCost{Total: 0.5}}
	dispatcher.HandleEvent(&coding.SessionEvent{
		Type: coding.SessionCompactionEnd, Reason: coding.CompactionManual,
		Result: &coding.CompactionResult{Summary: "summarized", TokensBefore: 500, Usage: &usage},
	})
	if len(flushed) != 1 || flushed[0] {
		t.Fatalf("flushed = %v", flushed)
	}
	lines = strings.Join(renderChat(t, transcript.Chat), "\n")
	if !strings.Contains(lines, "[compaction]") || !strings.Contains(lines, "Compaction: ") {
		t.Fatalf("compaction render = %q", lines)
	}
}
