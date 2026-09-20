package coding

import (
	ctxpkg "context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dat267/gpi/agent"
	"github.com/dat267/gpi/ai"
)

// Round 108 tests: the auto-retry loop, post-run continuation, and the
// context-overflow exclusion.

// erroringStreamFn fails the first failures times, then answers.
func erroringStreamFn(failures int64, errorMessage string) agent.StreamFn {
	var calls atomic.Int64
	return func(model *ai.Model, context ai.TranscriptContext, options *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
		call := calls.Add(1)
		stream := ai.NewAssistantMessageEventStream()
		go func() {
			if call <= failures {
				message := &ai.AssistantMessage{
					API: ai.APIAnthropicMessages, Provider: model.Provider, Model: model.ID,
					StopReason: ai.StopError, ErrorMessage: &errorMessage,
				}
				stream.Push(ai.AssistantMessageEvent{Type: ai.EventError, Reason: ai.StopError, Error: message})
				return
			}
			message := &ai.AssistantMessage{
				API: ai.APIAnthropicMessages, Provider: model.Provider, Model: model.ID,
				Content: ai.ContentList{ai.TextContent{Text: "recovered"}}, StopReason: ai.StopStop,
			}
			stream.Push(ai.AssistantMessageEvent{Type: ai.EventDone, Reason: ai.StopStop, Message: message})
		}()
		return stream
	}
}

func retrySession(t *testing.T, policy *ai.RetryPolicy, streamFn agent.StreamFn) *AgentSession {
	t.Helper()
	session, err := NewAgentSession(&SessionConfig{
		Cwd: t.TempDir(),
		Model: &ai.Model{ID: "m", API: ai.APIAnthropicMessages, Provider: "anthropic",
			ContextWindow: 100000, MaxTokens: 8192},
		StreamFn: streamFn,
		Settings: SessionSettings{Retry: policy, Compaction: DefaultCompactionSettings},
	})
	if err != nil {
		t.Fatal(err)
	}
	session.control = &AgentSessionControl{autoCompaction: false, autoRetry: true}
	return session
}

func TestRetryRecoversFromTransientError(t *testing.T) {
	session := retrySession(t, &ai.RetryPolicy{Enabled: true, MaxRetries: 3, BaseDelayMS: 1}, erroringStreamFn(1, "server overloaded"))
	events := []*SessionEvent{}
	session.Subscribe(func(event *SessionEvent) { events = append(events, event) })

	if err := session.Prompt(ctxpkg.Background(), "hello", nil); err != nil {
		t.Fatal(err)
	}
	// The retry ran and the second attempt succeeded; a successful response
	// resets the counter and reports the successful retry.
	if session.RetryAttempt() != 0 {
		t.Fatalf("attempt = %d", session.RetryAttempt())
	}
	if text := session.GetLastAssistantText(); text != "recovered" {
		t.Fatalf("text = %q", text)
	}
	var start, success *SessionEvent
	for _, event := range events {
		switch {
		case event.Type == SessionAutoRetryStart:
			start = event
		case event.Type == SessionAutoRetryEnd && event.Success:
			success = event
		}
	}
	if start == nil || start.Attempt != 1 || start.MaxAttempts != 3 || start.DelayMS <= 0 ||
		start.ErrorMessage != "server overloaded" {
		t.Fatalf("start = %+v", start)
	}
	if success == nil || success.Attempt != 1 {
		t.Fatalf("success = %+v", success)
	}
	// The error assistant message is removed from agent state but kept in the
	// session history.
	stateMessages := session.Messages()
	for _, message := range stateMessages {
		if assistant, ok := message.(*ai.AssistantMessage); ok && assistant.StopReason == ai.StopError {
			t.Fatalf("error message must be dropped from state: %+v", assistant)
		}
	}
	sessionEntries := session.Sessions.GetEntries()
	foundError := false
	for _, entry := range sessionEntries {
		if entry.Type == "message" && strings.Contains(string(entry.Message), "server overloaded") {
			foundError = true
		}
	}
	if !foundError {
		t.Fatal("the error message must stay in session history")
	}
}

func TestRetryExhaustion(t *testing.T) {
	session := retrySession(t, &ai.RetryPolicy{Enabled: true, MaxRetries: 1, BaseDelayMS: 1}, erroringStreamFn(10, "still overloaded"))
	events := []*SessionEvent{}
	session.Subscribe(func(event *SessionEvent) { events = append(events, event) })

	if err := session.Prompt(ctxpkg.Background(), "hello", nil); err != nil {
		t.Fatal(err)
	}
	var ends []*SessionEvent
	for _, event := range events {
		if event.Type == SessionAutoRetryEnd {
			ends = append(ends, event)
		}
	}
	if len(ends) != 1 || ends[0].Success || ends[0].Attempt != 1 || ends[0].ErrorMessage != "still overloaded" {
		t.Fatalf("ends = %+v", ends)
	}
	if session.RetryAttempt() != 0 {
		t.Fatalf("attempt = %d", session.RetryAttempt())
	}
}

func TestRetryDisabled(t *testing.T) {
	session := retrySession(t, &ai.RetryPolicy{Enabled: false, MaxRetries: 3, BaseDelayMS: 1}, erroringStreamFn(1, "overloaded"))
	if err := session.Prompt(ctxpkg.Background(), "hello", nil); err != nil {
		t.Fatal(err)
	}
	if session.RetryAttempt() != 0 {
		t.Fatalf("attempt = %d", session.RetryAttempt())
	}
	if text := session.GetLastAssistantText(); text != "" {
		t.Fatalf("text = %q", text)
	}
}

func TestContextOverflowIsNotRetried(t *testing.T) {
	model := &ai.Model{ID: "m", API: ai.APIAnthropicMessages, Provider: "anthropic", ContextWindow: 1000, MaxTokens: 100}
	session := retrySession(t, &ai.RetryPolicy{Enabled: true, MaxRetries: 3, BaseDelayMS: 1}, erroringStreamFn(1, "overloaded"))
	session.Agent.SetModel(model)

	// A context-length error is not retryable.
	overflow := &ai.AssistantMessage{
		API: ai.APIAnthropicMessages, Provider: "anthropic", Model: "m",
		StopReason: ai.StopError, ErrorMessage: strPtr("This model's maximum context length is 4096 tokens"),
	}
	if session.IsRetryableError(overflow) {
		t.Fatal("context length errors must not be retried")
	}
	// A length stop reason is an overflow too.
	length := &ai.AssistantMessage{
		API: ai.APIAnthropicMessages, Provider: "anthropic", Model: "m", StopReason: ai.StopLength,
	}
	if session.IsRetryableError(length) {
		t.Fatal("length stops must not be retried")
	}
	// Usage beyond the context window is an overflow as well.
	overUsage := &ai.AssistantMessage{
		API: ai.APIAnthropicMessages, Provider: "anthropic", Model: "m",
		StopReason: ai.StopError, ErrorMessage: strPtr("bad request"),
		Usage: ai.Usage{Input: 2000, TotalTokens: 2000},
	}
	if session.IsRetryableError(overUsage) {
		t.Fatal("usage beyond the window must not be retried")
	}
	// A transient error is retryable.
	transient := &ai.AssistantMessage{
		API: ai.APIAnthropicMessages, Provider: "anthropic", Model: "m",
		StopReason: ai.StopError, ErrorMessage: strPtr("overloaded_error"),
	}
	if !session.IsRetryableError(transient) {
		t.Fatal("overloaded errors must be retried")
	}
	if session.IsRetryableError(nil) {
		t.Fatal("nil is not retryable")
	}
}

func TestAbortRetry(t *testing.T) {
	// A long backoff makes the retry abortable.
	session := retrySession(t, &ai.RetryPolicy{Enabled: true, MaxRetries: 3, BaseDelayMS: 5000}, erroringStreamFn(10, "overloaded"))
	events := []*SessionEvent{}
	session.Subscribe(func(event *SessionEvent) { events = append(events, event) })

	done := make(chan error, 1)
	go func() { done <- session.Prompt(ctxpkg.Background(), "hello", nil) }()

	// Wait for the retry to be scheduled, then abort it.
	deadline := time.Now().Add(2 * time.Second)
	for !session.IsRetrying() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !session.IsRetrying() {
		t.Fatal("retry did not start")
	}
	session.AbortRetry()

	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("abort did not release the retry")
	}
	var cancelled *SessionEvent
	for _, event := range events {
		if event.Type == SessionAutoRetryEnd && event.ErrorMessage == "Retry cancelled" {
			cancelled = event
		}
	}
	if cancelled == nil || cancelled.Success || cancelled.Attempt != 1 {
		t.Fatalf("events = %+v", events)
	}
	if session.RetryAttempt() != 0 || session.IsRetrying() {
		t.Fatalf("attempt = %d retrying = %v", session.RetryAttempt(), session.IsRetrying())
	}
}

func TestRetrySettingsToggle(t *testing.T) {
	dir := t.TempDir()
	settings := NewSettingsManagerFromFiles(dir, dir, SettingsManagerCreateOptions{})
	session := retrySession(t, &ai.RetryPolicy{Enabled: true, MaxRetries: 2, BaseDelayMS: 1}, erroringStreamFn(0, ""))
	session.control.Settings = settings

	if !session.RetryEnabled() {
		t.Fatal("retry enabled by default")
	}
	if !session.AutoRetryEnabled() {
		t.Fatal("auto retry toggle")
	}
	session.SetRetryEnabled(false)
	if session.RetryEnabled() {
		t.Fatal("retry must be disabled")
	}
	if settings.GetRetryEnabled() {
		t.Fatal("setting must be persisted")
	}
	// The settings-backed policy drives PrepareRetry.
	session.SetRetryEnabled(true)
	message := &ai.AssistantMessage{
		API: ai.APIAnthropicMessages, Provider: "anthropic", Model: "m",
		StopReason: ai.StopError, ErrorMessage: strPtr("overloaded_error"),
	}
	retry, err := session.PrepareRetry(ctxpkg.Background(), message)
	if err != nil || !retry {
		t.Fatalf("retry = %v err = %v", retry, err)
	}
	if session.RetryAttempt() != 1 {
		t.Fatalf("attempt = %d", session.RetryAttempt())
	}
}

func TestPostRunQueuedContinuation(t *testing.T) {
	var calls atomic.Int64
	streamFn := func(model *ai.Model, context ai.TranscriptContext, options *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
		call := calls.Add(1)
		stream := ai.NewAssistantMessageEventStream()
		go func() {
			message := &ai.AssistantMessage{
				API: ai.APIAnthropicMessages, Provider: model.Provider, Model: model.ID,
				Content: ai.ContentList{ai.TextContent{Text: "turn"}}, StopReason: ai.StopStop,
			}
			if call == 1 {
				// Queue a continuation from the agent_end path.
				message.Content = ai.ContentList{ai.ToolCall{ID: "t1", Name: "read", Arguments: []byte(`{}`)}}
				message.StopReason = ai.StopToolUse
			}
			stream.Push(ai.AssistantMessageEvent{Type: ai.EventDone, Reason: message.StopReason, Message: message})
		}()
		return stream
	}
	session := retrySession(t, &ai.RetryPolicy{Enabled: false}, streamFn)
	session.control = &AgentSessionControl{autoCompaction: false}

	if err := session.Prompt(ctxpkg.Background(), "hello", nil); err != nil {
		t.Fatal(err)
	}
	// The tool call ran and the follow-up turn (queued by the agent loop) ran
	// through the post-run continuation.
	if calls.Load() < 2 {
		t.Fatalf("calls = %d", calls.Load())
	}
}

func TestRetryWithNoPolicy(t *testing.T) {
	// Without control settings and without a configured policy there is nothing
	// to retry.
	session := retrySession(t, nil, erroringStreamFn(1, "overloaded"))
	session.control = nil
	if session.RetryEnabled() {
		t.Fatal("no policy means disabled")
	}
	if err := session.Prompt(ctxpkg.Background(), "hello", nil); err != nil {
		t.Fatal(err)
	}
	if session.RetryAttempt() != 0 {
		t.Fatalf("attempt = %d", session.RetryAttempt())
	}
}
