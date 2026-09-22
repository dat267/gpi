package coding

import (
	ctxpkg "context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dat267/pier/agent"
	"github.com/dat267/pier/ai"
)

// Round 107 tests: the prompt pipeline (options, expansion, guards, streaming
// behaviors, custom message buffering, abort/waitForIdle).

// recordingStreamFn answers every prompt with a fixed assistant message.
func recordingStreamFn(t *testing.T, prompts *atomic.Int64) agent.StreamFn {
	t.Helper()
	return func(model *ai.Model, context ai.TranscriptContext, options *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
		prompts.Add(1)
		stream := ai.NewAssistantMessageEventStream()
		go func() {
			message := &ai.AssistantMessage{
				API: ai.APIAnthropicMessages, Provider: model.Provider, Model: model.ID,
				Content: ai.ContentList{ai.TextContent{Text: "ack"}}, StopReason: ai.StopStop,
			}
			stream.Push(ai.AssistantMessageEvent{Type: ai.EventDone, Reason: ai.StopStop, Message: message})
		}()
		return stream
	}
}

func newPromptSession(t *testing.T, model *ai.Model, streamFn agent.StreamFn) *AgentSession {
	t.Helper()
	session, err := NewAgentSession(&SessionConfig{
		Cwd: t.TempDir(), Model: model, StreamFn: streamFn,
		Tools: []agent.AgentTool{testTool("read")},
	})
	if err != nil {
		t.Fatal(err)
	}
	session.control = &AgentSessionControl{autoCompaction: true, autoRetry: true, Settings: nil}
	return session
}

func TestPromptSendsUserMessage(t *testing.T) {
	var prompts atomic.Int64
	model := &ai.Model{ID: "m", API: ai.APIAnthropicMessages, Provider: "anthropic", ContextWindow: 100000, MaxTokens: 8192}
	session := newPromptSession(t, model, recordingStreamFn(t, &prompts))

	var preflight []bool
	err := session.Prompt(ctxpkg.Background(), "hello", &PromptOptions{
		PreflightResult: func(ok bool) { preflight = append(preflight, ok) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if prompts.Load() != 1 {
		t.Fatalf("prompts = %d", prompts.Load())
	}
	if len(preflight) != 1 || !preflight[0] {
		t.Fatalf("preflight = %v", preflight)
	}
	messages := session.Messages()
	user := firstUserMessage(messages)
	if user == nil || contentTextJoinedNoSep(user.Content) != "hello" {
		t.Fatalf("messages = %+v", messages)
	}
}

func TestPromptExpandsSkillAndTemplate(t *testing.T) {
	var prompts atomic.Int64
	model := &ai.Model{ID: "m", API: ai.APIAnthropicMessages, Provider: "anthropic", ContextWindow: 100000}
	session := newPromptSession(t, model, recordingStreamFn(t, &prompts))

	// A skill command expands into the skill block plus args.
	skillDir := t.TempDir()
	skillPath := filepath.Join(skillDir, "SKILL.md")
	if err := os.WriteFile(skillPath, []byte("---\nname: demo\n---\n\nDo the demo thing.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	session.SystemPromptOptions.Skills = []Skill{{Name: "demo", FilePath: skillPath, BaseDir: skillDir}}
	session.control.PromptTemplates = []PromptTemplate{{Name: "greet", Content: "Hello $1"}}

	if err := session.Prompt(ctxpkg.Background(), "/skill:demo extra args", nil); err != nil {
		t.Fatal(err)
	}
	messages := session.Messages()
	user := firstUserMessage(messages)
	if user == nil {
		t.Fatalf("no user message: %+v", messages)
	}
	text := contentTextJoinedNoSep(user.Content)
	if !strings.Contains(text, `<skill name="demo"`) || !strings.Contains(text, "Do the demo thing.") ||
		!strings.Contains(text, "extra args") {
		t.Fatalf("text = %q", text)
	}
	if strings.Contains(text, "name: demo") {
		t.Fatalf("frontmatter must be stripped: %q", text)
	}

	// An unknown skill passes through unchanged.
	if err := session.Prompt(ctxpkg.Background(), "/skill:missing args", nil); err != nil {
		t.Fatal(err)
	}
	messages = session.Messages()
	last := lastUserMessage(messages)
	if contentTextJoinedNoSep(last.Content) != "/skill:missing args" {
		t.Fatalf("text = %q", contentTextJoinedNoSep(last.Content))
	}

	// Prompt templates expand.
	if err := session.Prompt(ctxpkg.Background(), "/greet world", nil); err != nil {
		t.Fatal(err)
	}
	messages = session.Messages()
	last = lastUserMessage(messages)
	if !strings.Contains(contentTextJoinedNoSep(last.Content), "Hello world") {
		t.Fatalf("text = %q", contentTextJoin(last.Content))
	}

	// Expansion can be disabled.
	disabled := false
	if err := session.Prompt(ctxpkg.Background(), "/greet world", &PromptOptions{ExpandPromptTemplates: &disabled}); err != nil {
		t.Fatal(err)
	}
	messages = session.Messages()
	last = lastUserMessage(messages)
	if contentTextJoinedNoSep(last.Content) != "/greet world" {
		t.Fatalf("text = %q", contentTextJoinedNoSep(last.Content))
	}
}

func TestPromptModelValidation(t *testing.T) {
	var prompts atomic.Int64
	runtime := runtimeWithProviders(t, stubProvider("anthropic"))
	settings := NewSettingsManagerFromFiles(t.TempDir(), t.TempDir(), SettingsManagerCreateOptions{})
	session := newPromptSession(t, runtime.GetModel("anthropic", "anthropic-model"), recordingStreamFn(t, &prompts))
	session.control = &AgentSessionControl{ModelRuntime: runtime, Settings: settings}

	// Without auth the provider guidance message is returned.
	err := session.Prompt(ctxpkg.Background(), "hello", nil)
	if err == nil || !strings.HasPrefix(err.Error(), "No API key found for anthropic.") {
		t.Fatalf("err = %v", err)
	}
	if prompts.Load() != 0 {
		t.Fatal("no prompt expected")
	}

	// With a runtime key the prompt runs.
	if err := runtime.SetRuntimeAPIKey("anthropic", "sk-1", ctxpkg.Background()); err != nil {
		t.Fatal(err)
	}
	if err := session.Prompt(ctxpkg.Background(), "hello", nil); err != nil {
		t.Fatal(err)
	}
	if prompts.Load() != 1 {
		t.Fatalf("prompts = %d", prompts.Load())
	}

	// A session without a selected model reports the guidance message.
	modelless := newPromptSession(t, nil, recordingStreamFn(t, &prompts))
	modelless.Agent.SetModel(nil)
	if err := modelless.Prompt(ctxpkg.Background(), "hi", nil); err == nil ||
		!strings.Contains(err.Error(), "No model selected.") {
		t.Fatalf("err = %v", err)
	}
}

func TestPromptStreamingBehavior(t *testing.T) {
	var prompts atomic.Int64
	model := &ai.Model{ID: "m", API: ai.APIAnthropicMessages, Provider: "anthropic", ContextWindow: 100000}
	session := newPromptSession(t, model, recordingStreamFn(t, &prompts))

	// Fake an active run.
	session.prompt().mu.Lock()
	session.prompt().runActive = true
	session.prompt().mu.Unlock()

	// Without a behavior the prompt is rejected.
	var preflight []bool
	if err := session.Prompt(ctxpkg.Background(), "queued", &PromptOptions{
		PreflightResult: func(ok bool) { preflight = append(preflight, ok) },
	}); err == nil || !strings.Contains(err.Error(), "Specify streamingBehavior") {
		t.Fatalf("err = %v", err)
	}
	if len(preflight) != 1 || preflight[0] {
		t.Fatalf("preflight = %v", preflight)
	}

	// steer queues, followUp queues.
	if err := session.Prompt(ctxpkg.Background(), "steered", &PromptOptions{StreamingBehavior: "steer"}); err != nil {
		t.Fatal(err)
	}
	if err := session.Prompt(ctxpkg.Background(), "followed", &PromptOptions{StreamingBehavior: "followUp"}); err != nil {
		t.Fatal(err)
	}
	if steering := session.GetSteeringMessages(); len(steering) != 1 || steering[0] != "steered" {
		t.Fatalf("steering = %v", steering)
	}
	if followUp := session.GetFollowUpMessages(); len(followUp) != 1 || followUp[0] != "followed" {
		t.Fatalf("followUp = %v", followUp)
	}
	if prompts.Load() != 0 {
		t.Fatal("queued prompts must not run")
	}
}

func TestPromptCompactionGuard(t *testing.T) {
	var prompts atomic.Int64
	model := &ai.Model{ID: "m", API: ai.APIAnthropicMessages, Provider: "anthropic", ContextWindow: 100000}
	session := newPromptSession(t, model, recordingStreamFn(t, &prompts))
	// A compaction in flight is represented by its abort controller
	// (upstream's compactionAbortController), the single source IsCompacting
	// derives from.
	session.mu.Lock()
	session.compactionCancel = func() {}
	session.mu.Unlock()
	if err := session.Prompt(ctxpkg.Background(), "hello", nil); err == nil ||
		!strings.Contains(err.Error(), "Cannot submit a prompt while compaction is in progress") {
		t.Fatalf("err = %v", err)
	}
	session.mu.Lock()
	session.compactionCancel = nil
	session.mu.Unlock()
}

func TestSendUserMessageAndCustomMessages(t *testing.T) {
	var prompts atomic.Int64
	model := &ai.Model{ID: "m", API: ai.APIAnthropicMessages, Provider: "anthropic", ContextWindow: 100000}
	session := newPromptSession(t, model, recordingStreamFn(t, &prompts))

	// sendUserMessage normalizes content and does not expand templates.
	if err := session.SendUserMessage(ctxpkg.Background(), "plain", ""); err != nil {
		t.Fatal(err)
	}
	if prompts.Load() != 1 {
		t.Fatalf("prompts = %d", prompts.Load())
	}
	if err := session.SendUserMessage(ctxpkg.Background(), ai.ContentList{
		ai.TextContent{Text: "with"}, ai.ImageContent{MimeType: "image/png", Data: "AAAA"},
	}, ""); err != nil {
		t.Fatal(err)
	}
	messages := session.Messages()
	last := lastUserMessage(messages)
	if contentTextJoinedNoSep(last.Content) != "with" || len(last.Content.Blocks) != 2 {
		t.Fatalf("content = %+v", last.Content)
	}

	// A custom message with triggerTurn runs a turn; without it while idle it is
	// appended only.
	custom := &ai.CustomMessage{Role: RoleCustom, Content: []byte(`{"text":"note"}`), Timestamp: 1}
	if err := session.SendMessage(ctxpkg.Background(), custom, &SendMessageOptions{TriggerTurn: boolPtr(false)}); err != nil {
		t.Fatal(err)
	}
	entries := session.Sessions.GetEntries()
	if entries[len(entries)-1].Type != "custom_message" {
		t.Fatalf("entries = %+v", entries[len(entries)-1])
	}

	// While streaming with triggerTurn=false the message is buffered until the
	// turn ends.
	before := prompts.Load()
	session.prompt().mu.Lock()
	session.prompt().runActive = true
	session.prompt().mu.Unlock()
	buffered := &ai.CustomMessage{Role: RoleCustom, Content: []byte(`{"text":"deferred"}`), Timestamp: 2}
	if err := session.SendMessage(ctxpkg.Background(), buffered, &SendMessageOptions{TriggerTurn: boolPtr(false)}); err != nil {
		t.Fatal(err)
	}
	if prompts.Load() != before {
		t.Fatal("buffered message must not run a turn")
	}
	session.prompt().mu.Lock()
	session.prompt().runActive = false
	session.prompt().mu.Unlock()
	session.FlushPendingCustomMessages()
	entries = session.Sessions.GetEntries()
	if entries[len(entries)-1].Type != "custom_message" {
		t.Fatalf("entries = %+v", entries[len(entries)-1])
	}

	// Next-turn messages are injected with the next prompt.
	session.QueueNextTurnMessage(&ai.CustomMessage{Role: RoleCustom, Content: []byte(`{"text":"extra"}`), Timestamp: 3})
	if err := session.Prompt(ctxpkg.Background(), "after", nil); err != nil {
		t.Fatal(err)
	}
	messages = session.Messages()
	foundExtra := false
	for _, message := range messages {
		if custom, ok := message.(*ai.CustomMessage); ok && strings.Contains(string(custom.Content), "extra") {
			foundExtra = true
		}
	}
	if !foundExtra {
		t.Fatalf("next-turn message missing: %+v", messages)
	}
}

func TestAbortAndWaitForIdle(t *testing.T) {
	var prompts atomic.Int64
	model := &ai.Model{ID: "m", API: ai.APIAnthropicMessages, Provider: "anthropic", ContextWindow: 100000}
	session := newPromptSession(t, model, recordingStreamFn(t, &prompts))

	// Idle sessions return immediately.
	start := time.Now()
	if err := session.WaitForIdle(ctxpkg.Background()); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > 50*time.Millisecond {
		t.Fatalf("idle wait took %v", elapsed)
	}

	// A fake run makes WaitForIdle block until the run is released.
	session.prompt().mu.Lock()
	session.prompt().runActive = true
	session.prompt().mu.Unlock()
	done := make(chan error, 1)
	go func() { done <- session.WaitForIdle(ctxpkg.Background()) }()
	select {
	case err := <-done:
		t.Fatalf("wait returned early: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	session.prompt().mu.Lock()
	session.prompt().runActive = false
	session.prompt().mu.Unlock()
	session.resolveIdleWaitIfIdle()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("wait did not resolve")
	}

	// A cancelled context releases the waiter.
	session.prompt().mu.Lock()
	session.prompt().runActive = true
	session.prompt().mu.Unlock()
	ctx, cancel := ctxpkg.WithCancel(ctxpkg.Background())
	cancel()
	if err := session.WaitForIdle(ctx); err == nil {
		t.Fatal("cancelled wait must error")
	}
	session.prompt().mu.Lock()
	session.prompt().runActive = false
	session.prompt().mu.Unlock()
	session.resolveIdleWaitIfIdle()

	// Abort clears retry state and aborts the agent.
	session.mu.Lock()
	session.retryAttempt = 3
	session.mu.Unlock()
	session.Abort(ctxpkg.Background())
	if session.IsRetrying() {
		t.Fatal("abort must clear retry")
	}
}

// contentTextJoin is a test helper for the joined text of content.
func contentTextJoin(content ai.StringOrBlocks) string {
	if content.String() {
		return content.Text
	}
	var parts []string
	for _, block := range content.Blocks {
		if text, ok := block.(ai.TextContent); ok {
			parts = append(parts, text.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// firstUserMessage returns the first user message in a transcript.
func firstUserMessage(messages []ai.Message) *ai.UserMessage {
	for _, message := range messages {
		if user, ok := message.(*ai.UserMessage); ok {
			return user
		}
	}
	return nil
}

// lastUserMessage returns the most recent user message in a transcript.
func lastUserMessage(messages []ai.Message) *ai.UserMessage {
	for index := len(messages) - 1; index >= 0; index-- {
		if user, ok := messages[index].(*ai.UserMessage); ok {
			return user
		}
	}
	return nil
}
