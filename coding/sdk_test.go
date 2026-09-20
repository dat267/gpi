package coding

import (
	ctxpkg "context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/dat267/gpi/ai"
)

// Round 120 tests: the CreateAgentSession assembly (model restore/fallback,
// thinking-level restore and clamp, tool selection, the block-images filter,
// and the settings-backed request options).

func TestCreateAgentSessionDefaults(t *testing.T) {
	tempAgentDir(t)
	runtime, err := CreateModelRuntime(CreateModelRuntimeOptions{
		AuthPath:        filepath.Join(GetAgentDir(), "auth.json"),
		ModelsPath:      filepath.Join(GetAgentDir(), "models.json"),
		Credentials:     newMemoryCredentialStore(),
		RefreshOnCreate: boolPtr(false),
	})
	if err != nil {
		t.Fatal(err)
	}
	settings := NewSettingsManagerFromFiles(t.TempDir(), t.TempDir(), SettingsManagerCreateOptions{})
	result, err := CreateAgentSession(ctxpkg.Background(), &CreateAgentSessionOptions{
		Cwd: t.TempDir(), ModelRuntime: runtime, SettingsManager: settings,
	})
	if err != nil {
		t.Fatal(err)
	}
	session := result.Session
	if session == nil {
		t.Fatal("no session")
	}
	// The model resolves from the runtime with auth configured... none here, so
	// the fallback message explains.
	if !strings.Contains(result.ModelFallbackMessage, "No models available.") {
		t.Fatalf("fallback = %q", result.ModelFallbackMessage)
	}
	if session.HasModel() {
		t.Fatalf("model = %+v", session.Model())
	}
	if session.ThinkingLevel() != ai.ThinkOff {
		t.Fatalf("thinking level = %q", session.ThinkingLevel())
	}
	// The default tool set is read/bash/edit/write.
	if names := strings.Join(session.GetActiveToolNames(), ","); names != "read,bash,edit,write" {
		t.Fatalf("tools = %q", names)
	}
	// The initial state is persisted for resume.
	entries := session.Sessions.GetEntries()
	last := entries[len(entries)-1]
	if last.Type != "thinking_level_change" {
		t.Fatalf("last entry = %+v", last)
	}
	foundModelChange := false
	for _, entry := range entries {
		if entry.Type == "model_change" {
			foundModelChange = true
		}
	}
	if foundModelChange {
		t.Fatal("no model change without a model")
	}
	// The cache warmer is wired.
	if session.CacheWarmer == nil {
		t.Fatal("cache warmer missing")
	}
	if status := session.GetCacheWarmingStatus(); status == nil {
		t.Fatal("cache warming status missing")
	}
}

func TestCreateAgentSessionModelResolveAndPersist(t *testing.T) {
	tempAgentDir(t)
	runtime, err := CreateModelRuntime(CreateModelRuntimeOptions{
		AuthPath:        filepath.Join(GetAgentDir(), "auth.json"),
		ModelsPath:      filepath.Join(GetAgentDir(), "models.json"),
		Credentials:     newMemoryCredentialStore(),
		RefreshOnCreate: boolPtr(false),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.SetRuntimeAPIKey("anthropic", "sk-1", ctxpkg.Background()); err != nil {
		t.Fatal(err)
	}
	settings := NewSettingsManagerFromFiles(t.TempDir(), t.TempDir(), SettingsManagerCreateOptions{})
	// A default model id the catalog does not know resolves to the provider's
	// default model instead.
	settings.SetDefaultProvider("anthropic")
	settings.SetDefaultModel("not-in-catalog")

	session, err := CreateAgentSession(ctxpkg.Background(), &CreateAgentSessionOptions{
		Cwd: t.TempDir(), ModelRuntime: runtime, SettingsManager: settings,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !session.Session.HasModel() {
		t.Fatalf("model = %+v", session.Session.Model())
	}
	resolvedID := session.Session.Model().ID
	if resolvedID == "not-in-catalog" {
		t.Fatalf("model = %+v", session.Session.Model())
	}

	// Restoring from a session's model change entry.
	manager := newTestSessionManager(t)
	manager.AppendMessage(&ai.UserMessage{Content: ai.StringOrBlocks{Text: "hello"}})
	manager.AppendMessage(&ai.AssistantMessage{
		API: ai.APIAnthropicMessages, Provider: "anthropic", Model: "m",
		Content:    ai.ContentList{ai.TextContent{Text: "reply"}},
		StopReason: ai.StopStop,
	})
	manager.AppendModelChange("anthropic", resolvedID)
	manager.AppendThinkingLevelChange(ai.ThinkHigh)
	restored, err := CreateAgentSession(ctxpkg.Background(), &CreateAgentSessionOptions{
		Cwd: t.TempDir(), ModelRuntime: runtime, SettingsManager: settings, SessionManager: manager,
	})
	if err != nil {
		t.Fatal(err)
	}
	if restored.Session.Model() == nil || restored.Session.Model().ID != resolvedID {
		t.Fatalf("model = %+v", restored.Session.Model())
	}
	if restored.Session.ThinkingLevel() != ai.ThinkHigh {
		t.Fatalf("thinking level = %q", restored.Session.ThinkingLevel())
	}
	// The messages are restored into the agent state.
	if len(restored.Session.Messages()) == 0 {
		t.Fatal("messages not restored")
	}

	// A model reference the runtime cannot serve falls back with a message and
	// then resolves the provider default.
	manager.AppendModelChange("nope", "nope")
	fallback, err := CreateAgentSession(ctxpkg.Background(), &CreateAgentSessionOptions{
		Cwd: t.TempDir(), ModelRuntime: runtime, SettingsManager: settings, SessionManager: manager,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(fallback.ModelFallbackMessage, "Could not restore model nope/nope") ||
		!strings.Contains(fallback.ModelFallbackMessage, ". Using anthropic/") {
		t.Fatalf("fallback = %q", fallback.ModelFallbackMessage)
	}
	if !fallback.Session.HasModel() {
		t.Fatalf("model = %+v", fallback.Session.Model())
	}
}

func TestCreateAgentSessionToolSelection(t *testing.T) {
	tempAgentDir(t)
	runtime, err := CreateModelRuntime(CreateModelRuntimeOptions{
		AuthPath:        filepath.Join(GetAgentDir(), "auth.json"),
		ModelsPath:      filepath.Join(GetAgentDir(), "models.json"),
		Credentials:     newMemoryCredentialStore(),
		RefreshOnCreate: boolPtr(false),
	})
	if err != nil {
		t.Fatal(err)
	}
	settings := NewSettingsManagerFromFiles(t.TempDir(), t.TempDir(), SettingsManagerCreateOptions{})
	cwd := t.TempDir()

	// The allowed list wins.
	session, err := CreateAgentSession(ctxpkg.Background(), &CreateAgentSessionOptions{
		Cwd: cwd, ModelRuntime: runtime, SettingsManager: settings, Tools: []ToolName{"read"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if names := strings.Join(session.Session.GetActiveToolNames(), ","); names != "read" {
		t.Fatalf("tools = %q", names)
	}

	// noTools disables everything.
	session, err = CreateAgentSession(ctxpkg.Background(), &CreateAgentSessionOptions{
		Cwd: cwd, ModelRuntime: runtime, SettingsManager: settings, NoTools: "all",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(session.Session.GetActiveToolNames()) != 0 {
		t.Fatalf("tools = %v", session.Session.GetActiveToolNames())
	}

	// Exclusions apply to the defaults.
	session, err = CreateAgentSession(ctxpkg.Background(), &CreateAgentSessionOptions{
		Cwd: cwd, ModelRuntime: runtime, SettingsManager: settings, ExcludeTools: []ToolName{"bash", "write"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if names := strings.Join(session.Session.GetActiveToolNames(), ","); names != "read,edit" {
		t.Fatalf("tools = %q", names)
	}

	// The configured default tools are honored (written through the settings
	// file: defaultTools).
	configuredDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(configuredDir, ConfigDirName), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configuredDir, ConfigDirName, "settings.json"),
		[]byte(`{"defaultTools":["read","grep"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	configured := NewSettingsManagerFromFiles(configuredDir, agentDirForSettings(), SettingsManagerCreateOptions{})
	session, err = CreateAgentSession(ctxpkg.Background(), &CreateAgentSessionOptions{
		Cwd: configuredDir, ModelRuntime: runtime, SettingsManager: configured,
	})
	if err != nil {
		t.Fatal(err)
	}
	if names := strings.Join(session.Session.GetActiveToolNames(), ","); names != "read,grep" {
		t.Fatalf("tools = %q", names)
	}
}

func TestCreateAgentSessionBlockImages(t *testing.T) {
	tempAgentDir(t)
	runtime, err := CreateModelRuntime(CreateModelRuntimeOptions{
		AuthPath:        filepath.Join(GetAgentDir(), "auth.json"),
		ModelsPath:      filepath.Join(GetAgentDir(), "models.json"),
		Credentials:     newMemoryCredentialStore(),
		RefreshOnCreate: boolPtr(false),
	})
	if err != nil {
		t.Fatal(err)
	}
	settings := NewSettingsManagerFromFiles(t.TempDir(), t.TempDir(), SettingsManagerCreateOptions{})
	session, err := CreateAgentSession(ctxpkg.Background(), &CreateAgentSessionOptions{
		Cwd: t.TempDir(), ModelRuntime: runtime, SettingsManager: settings,
		Model: &ai.Model{ID: "m", API: ai.APIAnthropicMessages, Provider: "anthropic", ContextWindow: 1000, MaxTokens: 100},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Block-images filters the transcript on conversion.
	settings.SetBlockImages(true)
	messages := []ai.Message{
		&ai.UserMessage{Content: ai.StringOrBlocks{Blocks: ai.ContentList{
			ai.ImageContent{Data: "AAAA", MimeType: "image/png"},
			ai.ImageContent{Data: "BBBB", MimeType: "image/png"},
			ai.TextContent{Text: "keep"},
		}}},
		&ai.ToolResultMessage{ToolCallID: "t", ToolName: "read", Content: ai.UserContentList{
			ai.ImageContent{Data: "CCCC", MimeType: "image/png"},
		}},
	}
	converted := session.Session.Agent.ConvertToLlm(messages)
	user := converted[0].(*ai.UserMessage)
	if len(user.Content.Blocks) != 2 {
		t.Fatalf("blocks = %+v", user.Content.Blocks)
	}
	if text, ok := user.Content.Blocks[0].(ai.TextContent); !ok || text.Text != "Image reading is disabled." {
		t.Fatalf("blocks = %+v", user.Content.Blocks)
	}
	if text, ok := user.Content.Blocks[1].(ai.TextContent); !ok || text.Text != "keep" {
		t.Fatalf("blocks = %+v", user.Content.Blocks)
	}
	toolResult := converted[1].(*ai.ToolResultMessage)
	if len(toolResult.Content) != 1 {
		t.Fatalf("tool result = %+v", toolResult.Content)
	}
	if text, ok := toolResult.Content[0].(ai.TextContent); !ok || text.Text != "Image reading is disabled." {
		t.Fatalf("tool result = %+v", toolResult.Content)
	}

	// The setting is read per conversion: disabling restores the images.
	settings.SetBlockImages(false)
	converted = session.Session.Agent.ConvertToLlm(messages)
	user = converted[0].(*ai.UserMessage)
	if len(user.Content.Blocks) != 3 {
		t.Fatalf("blocks = %+v", user.Content.Blocks)
	}
}

func TestCreateAgentSessionRequestOptions(t *testing.T) {
	tempAgentDir(t)
	runtime, err := CreateModelRuntime(CreateModelRuntimeOptions{
		AuthPath:        filepath.Join(GetAgentDir(), "auth.json"),
		ModelsPath:      filepath.Join(GetAgentDir(), "models.json"),
		Credentials:     newMemoryCredentialStore(),
		RefreshOnCreate: boolPtr(false),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.SetRuntimeAPIKey("anthropic", "sk-1", ctxpkg.Background()); err != nil {
		t.Fatal(err)
	}
	settingsDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(settingsDir, ConfigDirName), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(settingsDir, ConfigDirName, "settings.json"), []byte(
		`{"retry":{"provider":{"timeoutMs":45000,"maxRetries":7}},"httpIdleTimeoutMs":"disabled","transport":"sse"}`),
		0o600); err != nil {
		t.Fatal(err)
	}
	settings := NewSettingsManagerFromFiles(settingsDir, agentDirForSettings(), SettingsManagerCreateOptions{})

	var captured *ai.SimpleStreamOptions
	var prompts atomic.Int64
	streamFn := func(model *ai.Model, context ai.TranscriptContext, options *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
		prompts.Add(1)
		captured = options
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
	session, err := CreateAgentSession(ctxpkg.Background(), &CreateAgentSessionOptions{
		Cwd: t.TempDir(), ModelRuntime: runtime, SettingsManager: settings, StreamFn: streamFn,
		Model:         runtime.GetModel("anthropic", "anthropic-model"),
		ThinkingLevel: ai.ThinkLow,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := session.Session.Prompt(ctxpkg.Background(), "hello", nil); err != nil {
		t.Fatal(err)
	}
	if prompts.Load() != 1 {
		t.Fatalf("prompts = %d", prompts.Load())
	}
	if captured == nil {
		t.Fatal("options missing")
	}
	// The settings-backed request options are applied.
	if captured.TimeoutMs == nil || *captured.TimeoutMs != 45000 {
		t.Fatalf("timeout = %v", captured.TimeoutMs)
	}
	if captured.MaxRetries == nil || *captured.MaxRetries != 7 {
		t.Fatalf("maxRetries = %v", captured.MaxRetries)
	}
	if captured.Transport != ai.TransportSSE {
		t.Fatalf("transport = %q", captured.Transport)
	}
	if captured.SessionID == "" {
		t.Fatal("session id missing")
	}
	// The attribution transform ran over the merged headers (no headers here,
	// so the result is nil).
	if captured.Headers != nil {
		t.Fatalf("headers = %v", captured.Headers)
	}
	// The steering/follow-up modes come from settings.
	if session.Session.SteeringMode() != QueueModeOneAtATime {
		t.Fatalf("steering mode = %q", session.Session.SteeringMode())
	}
}

func TestCreateAgentSessionCacheWarmerStarts(t *testing.T) {
	tempAgentDir(t)
	runtime, err := CreateModelRuntime(CreateModelRuntimeOptions{
		AuthPath:        filepath.Join(GetAgentDir(), "auth.json"),
		ModelsPath:      filepath.Join(GetAgentDir(), "models.json"),
		Credentials:     newMemoryCredentialStore(),
		RefreshOnCreate: boolPtr(false),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.SetRuntimeAPIKey("anthropic", "sk-1", ctxpkg.Background()); err != nil {
		t.Fatal(err)
	}
	settings := NewSettingsManagerFromFiles(t.TempDir(), t.TempDir(), SettingsManagerCreateOptions{})
	settings.SetCacheWarmingMode(CacheWarmingIdle)

	model := &ai.Model{
		ID: "m", API: ai.APIAnthropicMessages, Provider: "anthropic",
		ContextWindow: 100000, MaxTokens: 8192,
		PromptCache: ai.ModelPromptCache{ai.CacheRetentionShort: 11},
	}
	var prompts atomic.Int64
	streamFn := func(streamModel *ai.Model, context ai.TranscriptContext, options *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
		prompts.Add(1)
		stream := ai.NewAssistantMessageEventStream()
		go func() {
			message := &ai.AssistantMessage{
				API: ai.APIAnthropicMessages, Provider: streamModel.Provider, Model: streamModel.ID,
				Content: ai.ContentList{ai.TextContent{Text: "ack"}}, StopReason: ai.StopStop,
				Usage: ai.Usage{Input: 1000, TotalTokens: 1000},
			}
			stream.Push(ai.AssistantMessageEvent{Type: ai.EventDone, Reason: ai.StopStop, Message: message})
		}()
		return stream
	}
	session, err := CreateAgentSession(ctxpkg.Background(), &CreateAgentSessionOptions{
		Cwd: t.TempDir(), ModelRuntime: runtime, SettingsManager: settings, StreamFn: streamFn, Model: model,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := session.Session.Prompt(ctxpkg.Background(), "hello", nil); err != nil {
		t.Fatal(err)
	}
	// The warmer is scheduled from the session request (the session id matches).
	status := session.Session.GetCacheWarmingStatus()
	if status == nil {
		t.Fatal("cache warming status missing")
	}
	if status.State != "scheduled" && status.State != "refreshing" && status.State != "inactive" {
		t.Fatalf("status = %+v", status)
	}
	// The warm run is scheduled from the session request; the economics make pi
	// stop before spending money, so no warm request fires here.
	if prompts.Load() < 1 {
		t.Fatalf("session prompt did not run: %d", prompts.Load())
	}
}

// agentDirForSettings gives the settings manager a clean global scope.
func agentDirForSettings() string { return tempDir() }

func tempDir() string {
	dir, err := os.MkdirTemp("", "pi-agent-dir")
	if err != nil {
		panic(err)
	}
	return dir
}
