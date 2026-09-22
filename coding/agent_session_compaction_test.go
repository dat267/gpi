package coding

import (
	ctxpkg "context"
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/dat267/pier/agent"
	"github.com/dat267/pier/ai"
)

// Round 110 tests: the automatic compaction decision (overflow, recoverable
// length, threshold) and RunAutoCompaction.

// smallCompactionSettings keeps everything before the last turn summarizable.
func smallCompactionSettings() CompactionSettings {
	return CompactionSettings{Enabled: true, ReserveTokens: 100, KeepRecentTokens: 1}
}

func compactionTestSession(t *testing.T, model *ai.Model, streamFn agent.StreamFn, settings CompactionSettings) *AgentSession {
	t.Helper()
	session, err := NewAgentSession(&SessionConfig{
		Cwd: t.TempDir(), Model: model, StreamFn: streamFn, Settings: SessionSettings{Compaction: settings},
	})
	if err != nil {
		t.Fatal(err)
	}
	session.control = &AgentSessionControl{autoCompaction: true, autoRetry: false}
	return session
}

// seedBranch appends two conversation turns to the session and mirrors them
// into agent state.
func seedBranch(t *testing.T, session *AgentSession) {
	t.Helper()
	session.Sessions.AppendMessage(&ai.UserMessage{Content: ai.StringOrBlocks{Text: "first question"}})
	session.Sessions.AppendMessage(&ai.AssistantMessage{
		API: ai.APIAnthropicMessages, Provider: "anthropic", Model: "m",
		Content:    ai.ContentList{ai.TextContent{Text: "first answer with some detail to tokenize"}},
		StopReason: ai.StopStop,
	})
	session.Sessions.AppendMessage(&ai.UserMessage{Content: ai.StringOrBlocks{Text: "second question"}})
	session.Sessions.AppendMessage(&ai.AssistantMessage{
		API: ai.APIAnthropicMessages, Provider: "anthropic", Model: "m",
		Content:    ai.ContentList{ai.TextContent{Text: "second answer"}},
		StopReason: ai.StopStop,
	})
	// Ending on a user message keeps the cut point on a turn start, so the
	// summary is a single LLM call.
	session.Sessions.AppendMessage(&ai.UserMessage{Content: ai.StringOrBlocks{Text: "third question"}})
	session.Agent.SetMessages(session.Sessions.BuildSessionContext().Messages)
}

func TestCheckCompactionDisabled(t *testing.T) {
	var runs atomic.Int64
	model := &ai.Model{ID: "m", API: ai.APIAnthropicMessages, Provider: "anthropic", ContextWindow: 100000, MaxTokens: 8192}
	session := compactionTestSession(t, model, summarizingStreamFn(t, &runs, "summary text that is long enough to pass the usability check"), smallCompactionSettings())
	seedBranch(t, session)
	session.control.autoCompaction = false

	assistant := &ai.AssistantMessage{
		API: ai.APIAnthropicMessages, Provider: "anthropic", Model: "m", Timestamp: 1000,
		StopReason: ai.StopStop, Usage: ai.Usage{Input: 99000, TotalTokens: 99000},
	}
	compacted, err := session.CheckCompaction(ctxpkg.Background(), assistant, true)
	if err != nil || compacted {
		t.Fatalf("compacted = %v err = %v", compacted, err)
	}
	if runs.Load() != 0 {
		t.Fatalf("runs = %d", runs.Load())
	}
	for _, entry := range session.Sessions.GetEntries() {
		if entry.Type == "compaction" {
			t.Fatal("no compaction entry expected")
		}
	}
}

func TestCheckCompactionThreshold(t *testing.T) {
	var runs atomic.Int64
	model := &ai.Model{ID: "m", API: ai.APIAnthropicMessages, Provider: "anthropic", ContextWindow: 100000, MaxTokens: 8192}
	session := compactionTestSession(t, model, summarizingStreamFn(t, &runs, "summary text that is long enough to pass the usability check"), smallCompactionSettings())
	seedBranch(t, session)

	assistant := &ai.AssistantMessage{
		API: ai.APIAnthropicMessages, Provider: "anthropic", Model: "m", Timestamp: timeNowMS(),
		StopReason: ai.StopStop,
		Usage:      ai.Usage{Input: 99950, Output: 10, TotalTokens: 99960},
	}
	compacted, err := session.CheckCompaction(ctxpkg.Background(), assistant, true)
	if err != nil {
		t.Fatal(err)
	}
	// Threshold compaction never retries.
	if compacted {
		t.Fatal("threshold compaction must not continue the agent")
	}
	if runs.Load() != 1 {
		t.Fatalf("runs = %d", runs.Load())
	}
	var found *SessionEntry
	entries := session.Sessions.GetEntries()
	for index := range entries {
		if entries[index].Type == "compaction" {
			found = &entries[index]
		}
	}
	if found == nil || !strings.Contains(found.Summary, "summary text") {
		t.Fatalf("entries = %+v", entries)
	}
	if len(session.Messages()) == 0 {
		t.Fatal("agent messages must be refreshed")
	}

	// A message from before the compaction is stale and skipped.
	stale, err := session.CheckCompaction(ctxpkg.Background(), assistant, true)
	if err != nil || stale {
		t.Fatalf("stale check = %v err = %v", stale, err)
	}
}

func TestCheckCompactionOverflowCompleted(t *testing.T) {
	var runs atomic.Int64
	model := &ai.Model{ID: "m", API: ai.APIAnthropicMessages, Provider: "anthropic", ContextWindow: 1000, MaxTokens: 100}
	session := compactionTestSession(t, model, summarizingStreamFn(t, &runs, "overflow summary text that passes the usability check too"), smallCompactionSettings())
	seedBranch(t, session)

	// Case 2: a successful response beyond the window compacts without retry.
	completed := &ai.AssistantMessage{
		API: ai.APIAnthropicMessages, Provider: "anthropic", Model: "m", Timestamp: timeNowMS(),
		StopReason: ai.StopStop,
		Usage:      ai.Usage{Input: 2000, TotalTokens: 2000},
	}
	compacted, err := session.CheckCompaction(ctxpkg.Background(), completed, true)
	if err != nil {
		t.Fatal(err)
	}
	if compacted {
		t.Fatal("completed overflow must not continue")
	}
	if runs.Load() != 1 {
		t.Fatalf("runs = %d", runs.Load())
	}
}

func TestCheckCompactionOverflowError(t *testing.T) {
	var runs atomic.Int64
	model := &ai.Model{ID: "m", API: ai.APIAnthropicMessages, Provider: "anthropic", ContextWindow: 1000, MaxTokens: 100}
	session := compactionTestSession(t, model, summarizingStreamFn(t, &runs, "recovered summary text that passes the usability check"), smallCompactionSettings())
	seedBranch(t, session)

	// Case 1: an error overflow drops the failed message, compacts, and retries.
	failed := &ai.AssistantMessage{
		API: ai.APIAnthropicMessages, Provider: "anthropic", Model: "m", Timestamp: timeNowMS(),
		StopReason: ai.StopError, ErrorMessage: strPtr("prompt is too long: 2000 tokens > 1000 maximum"),
	}
	messages := append(session.Agent.State().Messages, ai.Message(failed))
	session.Agent.SetMessages(messages)

	compacted, err := session.CheckCompaction(ctxpkg.Background(), failed, true)
	if err != nil {
		t.Fatal(err)
	}
	if !compacted {
		t.Fatal("overflow recovery must continue the agent")
	}
	if runs.Load() != 1 {
		t.Fatalf("runs = %d", runs.Load())
	}
	// The failed message was removed from agent state.
	for _, message := range session.Messages() {
		if assistant, ok := message.(*ai.AssistantMessage); ok && assistant.StopReason == ai.StopError {
			t.Fatalf("failed message must be dropped: %+v", assistant)
		}
	}

	// A second overflow cannot recover again.
	second := &ai.AssistantMessage{
		API: ai.APIAnthropicMessages, Provider: "anthropic", Model: "m", Timestamp: timeNowMS() + 1000,
		StopReason: ai.StopError, ErrorMessage: strPtr("prompt is too long: 2000 tokens > 1000 maximum"),
	}
	session.Agent.SetMessages([]ai.Message{second})
	events := []*SessionEvent{}
	session.Subscribe(func(event *SessionEvent) { events = append(events, event) })
	compacted, err = session.CheckCompaction(ctxpkg.Background(), second, true)
	if err != nil || compacted {
		t.Fatalf("compacted = %v err = %v", compacted, err)
	}
	var failure *SessionEvent
	for _, event := range events {
		if event.Type == SessionCompactionEnd && event.ErrorMessage != "" {
			failure = event
		}
	}
	if failure == nil || !strings.Contains(failure.ErrorMessage, "Context overflow recovery failed") {
		t.Fatalf("events = %+v", events)
	}
}

func TestCheckCompactionGuards(t *testing.T) {
	var runs atomic.Int64
	model := &ai.Model{ID: "m", API: ai.APIAnthropicMessages, Provider: "anthropic", ContextWindow: 1000, MaxTokens: 100}
	session := compactionTestSession(t, model, summarizingStreamFn(t, &runs, "summary text that is long enough to pass the usability check"), smallCompactionSettings())
	seedBranch(t, session)

	// Aborted messages are skipped when the check allows it.
	aborted := &ai.AssistantMessage{
		API: ai.APIAnthropicMessages, Provider: "anthropic", Model: "m", Timestamp: timeNowMS(),
		StopReason: ai.StopAborted,
	}
	if compacted, err := session.CheckCompaction(ctxpkg.Background(), aborted, true); err != nil || compacted {
		t.Fatalf("aborted = %v err = %v", compacted, err)
	}
	// The pre-prompt check includes aborted messages (usage decides); the small
	// usage does not cross the threshold.
	if compacted, err := session.CheckCompaction(ctxpkg.Background(), aborted, false); err != nil || compacted {
		t.Fatalf("aborted with usage = %v err = %v", compacted, err)
	}

	// A different model's overflow is ignored.
	other := &ai.AssistantMessage{
		API: ai.APIOpenAICompletions, Provider: "openai", Model: "other", Timestamp: timeNowMS(),
		StopReason: ai.StopError, ErrorMessage: strPtr("prompt is too long: 2000 tokens > 1000 maximum"),
	}
	if compacted, err := session.CheckCompaction(ctxpkg.Background(), other, true); err != nil || compacted {
		t.Fatalf("other model = %v err = %v", compacted, err)
	}
	if runs.Load() != 0 {
		t.Fatalf("runs = %d", runs.Load())
	}

	// A recoverable length stop (output below the model limit) compacts and
	// retries the turn.
	length := &ai.AssistantMessage{
		API: ai.APIAnthropicMessages, Provider: "anthropic", Model: "m", Timestamp: timeNowMS(),
		StopReason: ai.StopLength, Usage: ai.Usage{Input: 500, Output: 10, TotalTokens: 510},
	}
	if !ai.IsRecoverableLength(length, 100) {
		t.Fatal("length stop must be recoverable")
	}
	compacted, err := session.CheckCompaction(ctxpkg.Background(), length, true)
	if err != nil {
		t.Fatal(err)
	}
	if !compacted {
		t.Fatal("recoverable length must continue")
	}
	if runs.Load() != 1 {
		t.Fatalf("runs = %d", runs.Load())
	}
}

func TestRunAutoCompactionWithoutModel(t *testing.T) {
	var runs atomic.Int64
	session := compactionTestSession(t, nil, summarizingStreamFn(t, &runs, "summary"), smallCompactionSettings())
	session.Agent.SetModel(nil)
	compacted, err := session.RunAutoCompaction(ctxpkg.Background(), CompactionThreshold, false)
	if err != nil || compacted || runs.Load() != 0 {
		t.Fatalf("compacted = %v err = %v runs = %d", compacted, err, runs.Load())
	}
}

func TestAbortCompaction(t *testing.T) {
	var runs atomic.Int64
	model := &ai.Model{ID: "m", API: ai.APIAnthropicMessages, Provider: "anthropic", ContextWindow: 1000, MaxTokens: 100}
	session := compactionTestSession(t, model, summarizingStreamFn(t, &runs, "summary text that is long enough to pass the usability check"), smallCompactionSettings())
	session.AbortCompaction()
	if session.IsCompacting() {
		t.Fatal("no compaction in flight")
	}
}

// summarizingStreamFn answers summarization calls with a fixed summary.
func summarizingStreamFn(t *testing.T, runs *atomic.Int64, text string) agent.StreamFn {
	t.Helper()
	return func(model *ai.Model, context ai.TranscriptContext, options *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
		runs.Add(1)
		stream := ai.NewAssistantMessageEventStream()
		go func() {
			message := &ai.AssistantMessage{
				API: ai.APIAnthropicMessages, Provider: model.Provider, Model: model.ID,
				Content: ai.ContentList{ai.TextContent{Text: text}}, StopReason: ai.StopStop,
				Usage: ai.Usage{Input: 100, Output: 10, TotalTokens: 110},
			}
			stream.Push(ai.AssistantMessageEvent{Type: ai.EventDone, Reason: ai.StopStop, Message: message})
		}()
		return stream
	}
}

func unreachableStreamFn(*ai.Model, ai.TranscriptContext, *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
	panic("streamFn must not be called on this path")
}

// collectCompactionEvents subscribes and returns a function that drains the
// start/end events seen so far.
func collectCompactionEvents(session *AgentSession) func() (starts, ends int, lastEnd *SessionEvent) {
	var mu sync.Mutex
	starts, ends := 0, 0
	var lastEnd *SessionEvent
	session.Subscribe(func(event *SessionEvent) {
		mu.Lock()
		defer mu.Unlock()
		switch event.Type {
		case SessionCompactionStart:
			starts++
		case SessionCompactionEnd:
			ends++
			clone := *event
			lastEnd = &clone
		}
	})
	return func() (int, int, *SessionEvent) {
		mu.Lock()
		defer mu.Unlock()
		return starts, ends, lastEnd
	}
}

// TestCompactSessionTooSmallEmitsEnd asserts the impossible-compaction paths
// still emit compaction_end after compaction_start (upstream routes every
// error through the catch that emits compaction_end); missing it left the
// compaction status indicator mounted forever.
func TestCompactSessionTooSmallEmitsEnd(t *testing.T) {
	session := compactionTestSession(t, &ai.Model{ID: "m", Provider: "anthropic", API: ai.APIAnthropicMessages}, unreachableStreamFn, smallCompactionSettings())
	drain := collectCompactionEvents(session)

	_, err := session.CompactSession(ctxpkg.Background(), "")
	if err == nil {
		t.Fatal("expected an error for a too-small session")
	}
	starts, ends, lastEnd := drain()
	if starts != 1 || ends != 1 {
		t.Fatalf("events: %d starts, %d ends; want 1 of each", starts, ends)
	}
	if lastEnd == nil || !strings.Contains(lastEnd.ErrorMessage, "Nothing to compact") {
		t.Fatalf("last end %+v, want a Nothing-to-compact error message", lastEnd)
	}
	if !session.IsIdle() {
		t.Fatal("session must be idle after a failed compaction")
	}
}

// TestCompactSessionAlreadyCompactedEmitsEnd covers the already-compacted path.
func TestCompactSessionAlreadyCompactedEmitsEnd(t *testing.T) {
	session := compactionTestSession(t, &ai.Model{ID: "m", Provider: "anthropic", API: ai.APIAnthropicMessages}, unreachableStreamFn, smallCompactionSettings())
	drain := collectCompactionEvents(session)
	seedBranch(t, session)

	// Force a compaction entry so PrepareCompaction reports "already compacted".
	session.Sessions.AppendCompaction("summary of everything", "", 100, json.RawMessage("{}"), false, nil)

	_, err := session.CompactSession(ctxpkg.Background(), "")
	if err == nil || !strings.Contains(err.Error(), "Already compacted") {
		t.Fatalf("error %v, want Already compacted", err)
	}
	starts, ends, lastEnd := drain()
	if starts != 1 || ends != 1 {
		t.Fatalf("events: %d starts, %d ends; want 1 of each", starts, ends)
	}
	if lastEnd == nil || !strings.Contains(lastEnd.ErrorMessage, "Already compacted") {
		t.Fatalf("last end %+v, want an Already-compacted error message", lastEnd)
	}
	if !session.IsIdle() {
		t.Fatal("session must be idle after a failed compaction")
	}
}

// TestCompactSessionNoModelEmitsEnd covers the missing-model path.
func TestCompactSessionNoModelEmitsEnd(t *testing.T) {
	session := compactionTestSession(t, nil, unreachableStreamFn, smallCompactionSettings())
	drain := collectCompactionEvents(session)

	_, err := session.CompactSession(ctxpkg.Background(), "")
	if err == nil {
		t.Fatal("expected an error without a model")
	}
	starts, ends, lastEnd := drain()
	if starts != 1 || ends != 1 {
		t.Fatalf("events: %d starts, %d ends; want 1 of each", starts, ends)
	}
	if lastEnd == nil || lastEnd.ErrorMessage == "" {
		t.Fatalf("last end %+v, want an error message", lastEnd)
	}
}

// TestIsCompactingDuringManualCompaction pins the activity state: a compaction
// in flight must report IsCompacting and must not report IsIdle. Upstream
// derives isCompacting from the abort controllers (agent-session.ts
// get isCompacting), so the state has one source; the port read a
// control.compactionActive flag that was never set, leaving the session
// "idle" for the whole compaction except while a branch summary was open.
func TestIsCompactingDuringManualCompaction(t *testing.T) {
	model := &ai.Model{ID: "m", API: ai.APIAnthropicMessages, Provider: "anthropic", ContextWindow: 100000, MaxTokens: 8192}
	session := compactionTestSession(t, model, summarizingStreamFn(t, new(atomic.Int64), "unused"), smallCompactionSettings())
	seedBranch(t, session)

	started := make(chan struct{}, 1)
	release := make(chan struct{})
	session.CompactionStreamFn = func(model *ai.Model, context ai.TranscriptContext, options *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
		stream := ai.NewAssistantMessageEventStream()
		select {
		case started <- struct{}{}:
		default:
		}
		go func() {
			<-release
			message := &ai.AssistantMessage{
				API: ai.APIAnthropicMessages, Provider: model.Provider, Model: model.ID,
				Content:    ai.ContentList{ai.TextContent{Text: "summary text long enough to be usable"}},
				StopReason: ai.StopStop, Usage: ai.Usage{Input: 100, Output: 10, TotalTokens: 110},
			}
			stream.Push(ai.AssistantMessageEvent{Type: ai.EventDone, Reason: ai.StopStop, Message: message})
		}()
		return stream
	}

	done := make(chan error, 1)
	go func() {
		_, err := session.CompactSession(ctxpkg.Background(), "")
		done <- err
	}()

	<-started
	if !session.IsCompacting() {
		t.Fatal("session must report compacting while a compaction is in flight")
	}
	if session.IsIdle() {
		t.Fatal("session must not report idle while compacting")
	}

	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if session.IsCompacting() {
		t.Fatal("session still compacting after the compaction finished")
	}
	if !session.IsIdle() {
		t.Fatal("session must be idle after the compaction finished")
	}
}
