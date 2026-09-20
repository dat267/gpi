package coding

import (
	ctxpkg "context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dat267/gpi/agent"
	"github.com/dat267/gpi/ai"
)

// Round 105 tests: the AgentSession state/control surface (tools, queue modes,
// model and thinking management, auto toggles, bash state, context usage).

func testTool(name string) agent.AgentTool {
	return agent.AgentTool{
		Name: name, Description: name + " tool",
		Parameters: json.RawMessage(`{"type":"object"}`),
		Execute: func(toolCallID string, params json.RawMessage, ctx ctxpkg.Context, onUpdate func(agent.AgentToolResult)) (agent.AgentToolResult, error) {
			return agent.AgentToolResult{}, nil
		},
	}
}

func newControlSession(t *testing.T, model *ai.Model, settings *SettingsManager) *AgentSession {
	t.Helper()
	session, err := NewAgentSession(&SessionConfig{
		Cwd:           t.TempDir(),
		Model:         model,
		StreamFn:      stubStreamFn,
		Tools:         []agent.AgentTool{testTool("read")},
		ThinkingLevel: ai.ThinkMedium,
	})
	if err != nil {
		t.Fatal(err)
	}
	control := &AgentSessionControl{Settings: settings, Tools: map[string]AgentToolDefinition{
		"read":  {Tool: testTool("read"), PromptGuidelines: []string{"read carefully"}},
		"bash":  {Tool: testTool("bash")},
		"edit":  {Tool: testTool("edit")},
		"write": {Tool: testTool("write")},
	}}
	control.autoCompaction = true
	control.autoRetry = true
	session.control = control
	return session
}

func TestSessionToolManagement(t *testing.T) {
	session := newControlSession(t, &ai.Model{
		ID: "m", API: ai.APIAnthropicMessages, Provider: "anthropic",
		Reasoning: true, ContextWindow: 1000, MaxTokens: 100,
	}, nil)

	if names := session.GetActiveToolNames(); strings.Join(names, ",") != "read" {
		t.Fatalf("active = %v", names)
	}
	all := session.GetAllTools()
	if len(all) != 4 {
		t.Fatalf("all = %+v", all)
	}
	// Sorted by name.
	if all[0].Name != "bash" || all[1].Name != "edit" {
		t.Fatalf("all = %+v", all)
	}
	var read ToolInfo
	for _, entry := range all {
		if entry.Name == "read" {
			read = entry
		}
	}
	if read.Description != "read tool" || len(read.PromptGuidelines) != 1 || read.PromptGuidelines[0] != "read carefully" {
		t.Fatalf("read = %+v", read)
	}
	if session.GetToolDefinition("read") == nil {
		t.Fatal("definition missing")
	}
	if session.GetToolDefinition("missing") != nil {
		t.Fatal("unknown tool must be nil")
	}

	// Unknown names are ignored when activating.
	session.SetActiveToolsByName([]string{"bash", "missing", "edit"})
	if names := session.GetActiveToolNames(); strings.Join(names, ",") != "bash,edit" {
		t.Fatalf("active = %v", names)
	}
	if session.SystemPromptOptions == nil || strings.Join(session.SystemPromptOptions.SelectedTools, ",") != "bash,edit" {
		t.Fatalf("prompt options = %+v", session.SystemPromptOptions)
	}
}

func TestSessionQueueModes(t *testing.T) {
	dir := t.TempDir()
	settingsPath := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(settingsPath, []byte(`{"steeringMode":"one-at-a-time","followUpMode":"all"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	settings := NewSettingsManagerFromFiles(dir, dir, SettingsManagerCreateOptions{})
	session := newControlSession(t, &ai.Model{ID: "m", API: ai.APIAnthropicMessages, Provider: "anthropic"}, settings)

	// Settings drive the modes on sync.
	session.SetSteeringMode(QueueModeAll)
	session.SetFollowUpMode(QueueModeOneAtATime)
	session.SyncQueueModesFromSettings()
	if session.SteeringMode() != QueueModeOneAtATime || session.FollowUpMode() != QueueModeAll {
		t.Fatalf("modes = %q %q", session.SteeringMode(), session.FollowUpMode())
	}

	// Queueing records the texts and clearing reports them.
	session.Steer(&ai.UserMessage{Content: ai.StringOrBlocks{Text: "steer one"}})
	session.FollowUp(&ai.UserMessage{Content: ai.StringOrBlocks{Text: "follow one"}})
	if session.PendingMessageCount() != 2 {
		t.Fatalf("pending = %d", session.PendingMessageCount())
	}
	if steering := session.GetSteeringMessages(); len(steering) != 1 || steering[0] != "steer one" {
		t.Fatalf("steering = %v", steering)
	}
	if followUp := session.GetFollowUpMessages(); len(followUp) != 1 || followUp[0] != "follow one" {
		t.Fatalf("followUp = %v", followUp)
	}
	steering, followUp := session.ClearQueue()
	if len(steering) != 1 || len(followUp) != 1 || session.PendingMessageCount() != 0 {
		t.Fatalf("cleared = %v %v pending = %d", steering, followUp, session.PendingMessageCount())
	}
}

func TestSessionModelAndThinking(t *testing.T) {
	dir := t.TempDir()
	settings := NewSettingsManagerFromFiles(dir, dir, SettingsManagerCreateOptions{})
	model := &ai.Model{
		ID: "m", API: ai.APIAnthropicMessages, Provider: "anthropic",
		Reasoning: true, ContextWindow: 1000, MaxTokens: 100,
		ThinkingLevelMap: ai.ThinkingLevelMap{ai.ThinkMax: nil},
	}
	session := newControlSession(t, model, settings)

	if !session.SupportsThinking() {
		t.Fatal("model supports thinking")
	}
	levels := session.GetAvailableThinkingLevels()
	if len(levels) == 0 {
		t.Fatal("levels must not be empty")
	}
	found := false
	for _, level := range levels {
		if level == ai.ThinkOff {
			found = true
		}
	}
	if !found {
		t.Fatalf("levels = %v", levels)
	}

	// Setting a supported level records a thinking-level change entry.
	session.SetThinkingLevel(ai.ThinkHigh, ModelMutationOptions{})
	if session.ThinkingLevel() != ai.ThinkHigh {
		t.Fatalf("level = %q", session.ThinkingLevel())
	}
	entries := session.Sessions.GetEntries()
	last := entries[len(entries)-1]
	if last.Type != "thinking_level_change" || last.ThinkingLevel != ai.ThinkHigh {
		t.Fatalf("last entry = %+v", last)
	}

	// Setting the same level again appends nothing.
	countBefore := len(entries)
	session.SetThinkingLevel(ai.ThinkHigh, ModelMutationOptions{})
	if len(session.Sessions.GetEntries()) != countBefore {
		t.Fatal("unchanged level must not append")
	}

	// A level the model does not support is clamped.
	session.SetThinkingLevel(ai.ThinkMax, ModelMutationOptions{})
	if session.ThinkingLevel() == ai.ThinkMax {
		t.Fatalf("level = %q (must be clamped)", session.ThinkingLevel())
	}

	// Persistence writes the requested level and model defaults.
	session.SetThinkingLevel(ai.ThinkLow, ModelMutationOptions{Persist: true})
	if got := settings.GetDefaultThinkingLevel(); got == nil || *got != ai.ThinkLow {
		t.Fatalf("default level = %v", got)
	}
	if err := session.SetModel(ctxpkg.Background(), model, ModelMutationOptions{Persist: true}); err != nil {
		t.Fatal(err)
	}
	provider := settings.GetDefaultProvider()
	modelID := settings.GetDefaultModel()
	if provider == nil || modelID == nil || *modelID == "" || *provider == "" {
		t.Fatalf("default model = %v/%v", provider, modelID)
	}

	// Cycling advances through the levels.
	next, ok := session.CycleThinkingLevel(ModelMutationOptions{})
	if !ok {
		t.Fatal("cycle must succeed")
	}
	if next != session.ThinkingLevel() {
		t.Fatalf("next = %q level = %q", next, session.ThinkingLevel())
	}

	// A model without reasoning does not cycle.
	session.Agent.SetModel(&ai.Model{ID: "plain", API: ai.APIAnthropicMessages, Provider: "anthropic", ContextWindow: 100, MaxTokens: 10})
	if session.SupportsThinking() {
		t.Fatal("plain model must not support thinking")
	}
	if _, ok := session.CycleThinkingLevel(ModelMutationOptions{}); ok {
		t.Fatal("cycle must fail without thinking")
	}
	// A non-reasoning model supports only "off" (upstream clamps to it).
	if levels := session.GetAvailableThinkingLevels(); len(levels) != 1 || levels[0] != ai.ThinkOff {
		t.Fatalf("levels = %v", levels)
	}
	// A nil model falls back to every level.
	session.Agent.SetModel(nil)
	if levels := session.GetAvailableThinkingLevels(); len(levels) != len(ThinkingLevelOptions) {
		t.Fatalf("levels = %v", levels)
	}
	if session.SupportsThinking() {
		t.Fatal("nil model must not support thinking")
	}
}

func TestSessionModelSwitchAuth(t *testing.T) {
	base := stubProvider("anthropic")
	runtime := runtimeWithProviders(t, base)
	settings := NewSettingsManagerFromFiles(t.TempDir(), t.TempDir(), SettingsManagerCreateOptions{})
	session := newControlSession(t, &ai.Model{ID: "m", API: ai.APIAnthropicMessages, Provider: "anthropic"}, settings)
	session.control.ModelRuntime = runtime

	// Without configured auth the switch is rejected.
	model := runtime.GetModel("anthropic", "anthropic-model")
	if err := session.SetModel(ctxpkg.Background(), model, ModelMutationOptions{}); err == nil ||
		!strings.Contains(err.Error(), "No API key for anthropic/anthropic-model") {
		t.Fatalf("err = %v", err)
	}
	// With a runtime key it succeeds and appends a model-change entry.
	if err := runtime.SetRuntimeAPIKey("anthropic", "sk-1", ctxpkg.Background()); err != nil {
		t.Fatal(err)
	}
	if err := session.SetModel(ctxpkg.Background(), model, ModelMutationOptions{}); err != nil {
		t.Fatal(err)
	}
	if session.Model().ID != "anthropic-model" {
		t.Fatalf("model = %+v", session.Model())
	}
	entries := session.Sessions.GetEntries()
	last := entries[len(entries)-1]
	if last.Type != "model_change" && last.Type != "thinking_level_change" {
		t.Fatalf("last entry = %+v", last)
	}

	// Cycling requires at least two available models.
	if result, err := session.CycleModel(ctxpkg.Background(), "forward", ModelMutationOptions{}); err != nil || result != nil {
		t.Fatalf("result = %+v err = %v", result, err)
	}

	// With two available models cycling moves on.
	other := stubProvider("beta")
	runtime.defaults["beta"] = other
	runtime.builtins["beta"] = other
	runtime.recomposeProvider("beta")
	if err := runtime.SetRuntimeAPIKey("beta", "sk-2", ctxpkg.Background()); err != nil {
		t.Fatal(err)
	}
	result, err := session.CycleModel(ctxpkg.Background(), "forward", ModelMutationOptions{})
	if err != nil || result == nil || result.IsScoped {
		t.Fatalf("result = %+v err = %v", result, err)
	}
	first := result.Model.ID
	result, err = session.CycleModel(ctxpkg.Background(), "backward", ModelMutationOptions{})
	if err != nil || result == nil || result.Model.ID == first {
		t.Fatalf("result = %+v err = %v", result, err)
	}

	// Scoped models take precedence, and scoped thinking levels are applied.
	session.SetScopedModels([]ScopedModel{
		{Model: runtime.GetModel("anthropic", "anthropic-model")},
		{Model: runtime.GetModel("beta", "beta-model"), ThinkingLevel: ai.ThinkHigh, HasThinking: true},
	})
	if scoped := session.ScopedModels(); len(scoped) != 2 {
		t.Fatalf("scoped = %+v", scoped)
	}
	result, err = session.CycleModel(ctxpkg.Background(), "forward", ModelMutationOptions{})
	if err != nil || result == nil || !result.IsScoped {
		t.Fatalf("result = %+v err = %v", result, err)
	}
	// Persisting a model outside the scope appends it.
	outside := &ai.Model{ID: "outside", API: ai.APIAnthropicMessages, Provider: "anthropic", Reasoning: false}
	runtime.defaults["anthropic"] = base
	if err := runtime.SetRuntimeAPIKey("anthropic", "sk-1", ctxpkg.Background()); err != nil {
		t.Fatal(err)
	}
	session.addPersistedDefaultToNonEmptyScope(outside)
	if scoped := session.ScopedModels(); len(scoped) != 3 {
		t.Fatalf("scoped = %+v", scoped)
	}
}

func TestSessionAutoTogglesAndBash(t *testing.T) {
	session := newControlSession(t, &ai.Model{ID: "m", API: ai.APIAnthropicMessages, Provider: "anthropic"}, nil)
	if !session.AutoCompactionEnabled() || !session.AutoRetryEnabled() {
		t.Fatal("toggles defaulted")
	}
	session.SetAutoCompactionEnabled(false)
	session.SetAutoRetryEnabled(false)
	if session.AutoCompactionEnabled() || session.AutoRetryEnabled() {
		t.Fatal("toggles not applied")
	}

	// Retry state: isRetrying reflects an in-flight retry sleep (upstream's
	// abort controller), while retryAttempt is the attempt counter.
	if session.IsRetrying() || session.RetryAttempt() != 0 {
		t.Fatal("no retry expected")
	}
	session.mu.Lock()
	session.retryAttempt = 2
	session.mu.Unlock()
	if session.IsRetrying() || session.RetryAttempt() != 2 {
		t.Fatalf("retry = %v %d", session.IsRetrying(), session.RetryAttempt())
	}
	session.AbortRetry()
	if session.IsRetrying() {
		t.Fatal("abort retry")
	}

	// Bash recording: a result recorded while idle lands in agent state and
	// session history as a message entry.
	exitCode := 1
	session.RecordBashResult("echo hi", BashResult{Output: "hi", ExitCode: &exitCode, Truncated: true, FullOutputPath: "/tmp/full"}, false)
	if session.HasPendingBashMessages() {
		t.Fatal("idle results are not deferred")
	}
	entries := session.Sessions.GetEntries()
	last := entries[len(entries)-1]
	if last.Type != "message" || !strings.Contains(string(last.Message), "bashExecution") ||
		!strings.Contains(string(last.Message), "echo hi") {
		t.Fatalf("entry = %+v", last)
	}
	foundBash := false
	for _, message := range session.Messages() {
		if custom, ok := message.(*ai.CustomMessage); ok && custom.Role == RoleBashExecution {
			foundBash = true
		}
	}
	if !foundBash {
		t.Fatal("bash message missing from agent state")
	}

	// While streaming the result is deferred until the flush.
	session.prompt().mu.Lock()
	session.prompt().runActive = true
	session.prompt().mu.Unlock()
	session.RecordBashResult("echo deferred", BashResult{Output: "deferred"}, false)
	if !session.HasPendingBashMessages() {
		t.Fatal("streaming results are deferred")
	}
	session.prompt().mu.Lock()
	session.prompt().runActive = false
	session.prompt().mu.Unlock()
	session.FlushPendingBashMessages()
	if session.HasPendingBashMessages() {
		t.Fatal("flush must clear pending")
	}
	entries = session.Sessions.GetEntries()
	last = entries[len(entries)-1]
	if !strings.Contains(string(last.Message), "echo deferred") {
		t.Fatalf("flushed entry = %+v", last)
	}
}

func TestSessionContextUsageAndStats(t *testing.T) {
	model := &ai.Model{ID: "m", API: ai.APIAnthropicMessages, Provider: "anthropic", ContextWindow: 1000, MaxTokens: 100}
	session := newControlSession(t, model, nil)

	// With no messages the estimate is zero and the percent is defined.
	usage := session.GetContextUsage()
	if usage == nil || usage.ContextWindow != 1000 || usage.Tokens == nil || usage.Percent == nil {
		t.Fatalf("usage = %+v", usage)
	}
	// Without a model there is no usage report.
	session.Agent.SetModel(nil)
	if usage := session.GetContextUsage(); usage != nil {
		t.Fatalf("usage = %+v", usage)
	}
	// Without a context window there is no report either.
	session.Agent.SetModel(&ai.Model{ID: "m", API: ai.APIAnthropicMessages, Provider: "anthropic"})
	if usage := session.GetContextUsage(); usage != nil {
		t.Fatalf("usage = %+v", usage)
	}

	// After a compaction without post-compaction assistant usage the count is
	// unknown (upstream's null).
	session.Agent.SetModel(model)
	session.Sessions.AppendCompaction("summary", "", 42, nil, false, nil)
	if usage := session.GetContextUsage(); usage == nil || usage.Tokens != nil || usage.Percent != nil || usage.ContextWindow != 1000 {
		t.Fatalf("usage = %+v", usage)
	}

	// Stats aggregate entries.
	assistant := &ai.AssistantMessage{
		API: ai.APIAnthropicMessages, Provider: "anthropic", Model: "m",
		Content: ai.ContentList{ai.TextContent{Text: "hi"}}, StopReason: ai.StopStop,
		Usage: ai.Usage{Input: 10, Output: 5, TotalTokens: 15, Cost: ai.UsageCost{Total: 0.01}},
	}
	session.Sessions.AppendMessage(assistant)
	session.Agent.SetMessages([]ai.Message{assistant})
	stats := session.GetSessionStats()
	if stats.AssistantMessages != 1 || stats.Tokens.Input != 10 || stats.Tokens.Output != 5 ||
		stats.Tokens.Total != 15 || stats.Cost != 0.01 {
		t.Fatalf("stats = %+v", stats)
	}
	if stats.SessionID == "" {
		t.Fatalf("stats = %+v", stats)
	}
	if session.GetLastAssistantText() != "hi" {
		t.Fatalf("last text = %q", session.GetLastAssistantText())
	}
	if stats.ContextUsage == nil {
		t.Fatalf("stats = %+v", stats)
	}
}

func TestSessionNamesAndForking(t *testing.T) {
	session := newControlSession(t, &ai.Model{ID: "m", API: ai.APIAnthropicMessages, Provider: "anthropic"}, nil)
	if session.SessionName() != "" {
		t.Fatalf("name = %q", session.SessionName())
	}
	events := []*SessionEvent{}
	session.Subscribe(func(event *SessionEvent) { events = append(events, event) })
	session.SetSessionName("my session")
	if session.SessionName() != "my session" {
		t.Fatalf("name = %q", session.SessionName())
	}
	found := false
	for _, event := range events {
		if event.Type == SessionInfoChanged {
			found = true
		}
	}
	if !found {
		t.Fatalf("events = %+v", events)
	}

	// Forking lists user messages with text only.
	session.Sessions.AppendMessage(&ai.UserMessage{Content: ai.StringOrBlocks{Text: "first"}})
	session.Sessions.AppendMessage(&ai.UserMessage{Content: ai.StringOrBlocks{Text: "  "}})
	session.Sessions.AppendMessage(&ai.UserMessage{Content: ai.StringOrBlocks{Text: "third"}})
	// contentText does not trim, so whitespace-only messages are still listed.
	forks := session.GetUserMessagesForForking()
	if len(forks) != 3 || forks[0].Text != "first" || forks[1].Text != "  " || forks[2].Text != "third" {
		t.Fatalf("forks = %+v", forks)
	}
	if forks[0].EntryID == "" {
		t.Fatalf("forks = %+v", forks)
	}

	// Dispose clears listeners and aborts.
	session.Dispose()
	if !session.control.disposed {
		t.Fatal("dispose must mark the control block")
	}
	session.emit(&SessionEvent{Type: SessionQueueUpdate})
	if len(events) != 1 {
		t.Fatalf("events after dispose = %+v", events)
	}
}

func TestSessionDefaultsWithoutControl(t *testing.T) {
	// A session built without the optional control block degrades gracefully.
	session, err := NewAgentSession(&SessionConfig{
		Cwd:      t.TempDir(),
		Model:    &ai.Model{ID: "m", API: ai.APIAnthropicMessages, Provider: "anthropic"},
		StreamFn: stubStreamFn,
	})
	if err != nil {
		t.Fatal(err)
	}
	if session.IsCompacting() || session.AutoCompactionEnabled() || session.AutoRetryEnabled() {
		t.Fatal("no control block means no toggles")
	}
	if tools := session.GetAllTools(); len(tools) != 0 {
		t.Fatalf("tools = %+v", tools)
	}
	if session.GetToolDefinition("read") != nil {
		t.Fatal("no definitions without a registry")
	}
	session.SetActiveToolsByName([]string{"read"})
	session.RecordBashResult("cmd", BashResult{}, false)
	session.AbortBash()
	if session.HasPendingBashMessages() || session.IsBashRunning() {
		t.Fatal("no bash runs without execution")
	}
}

// stubStreamFn answers every prompt with a fixed assistant message.
func stubStreamFn(model *ai.Model, context ai.TranscriptContext, options *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
	stream := ai.NewAssistantMessageEventStream()
	go func() {
		message := &ai.AssistantMessage{
			API: model.API, Provider: model.Provider, Model: model.ID,
			Content: ai.ContentList{ai.TextContent{Text: "ack"}}, StopReason: ai.StopStop,
		}
		stream.Push(ai.AssistantMessageEvent{Type: ai.EventDone, Reason: ai.StopStop, Message: message})
	}()
	return stream
}
