package coding

import (
	ctxpkg "context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/dat267/pier/agent"
	"github.com/dat267/pier/ai"
)

// Round 112 tests: session JSONL export, URL/JSON redaction, bug report
// metadata/diagnostics/bundle, and the model-written bug summary.

func TestSerializeSessionBranch(t *testing.T) {
	manager := newTestSessionManager(t)
	first := manager.AppendMessage(&ai.UserMessage{Content: ai.StringOrBlocks{Text: "hello"}})
	manager.AppendMessage(&ai.AssistantMessage{
		API: ai.APIAnthropicMessages, Provider: "anthropic", Model: "m",
		Content: ai.ContentList{ai.TextContent{Text: "hi"}}, StopReason: ai.StopStop,
	})

	serialized := SerializeSessionBranch(manager, nil)
	lines := strings.Split(strings.TrimRight(serialized, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("lines = %d", len(lines))
	}
	var header SessionHeader
	if err := json.Unmarshal([]byte(lines[0]), &header); err != nil {
		t.Fatal(err)
	}
	if header.Type() != "session" || header.ID != manager.GetSessionID() || header.Version == nil || *header.Version != CurrentSessionVersion {
		t.Fatalf("header = %+v", header)
	}
	// The parent ids are re-chained from the root.
	var firstEntry, secondEntry SessionEntry
	if err := json.Unmarshal([]byte(lines[1]), &firstEntry); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(lines[2]), &secondEntry); err != nil {
		t.Fatal(err)
	}
	if firstEntry.ParentID != nil || secondEntry.ParentID == nil || *secondEntry.ParentID != first {
		t.Fatalf("parent ids = %v %v", firstEntry.ParentID, secondEntry.ParentID)
	}

	// Trailing export-only entries chain after the branch.
	withTrailing := SerializeSessionBranch(manager, func(parentID *string, timestamp string) []*SessionEntry {
		entry := &SessionEntry{Type: "custom"}
		entry.ParentID = parentID
		return []*SessionEntry{entry}
	})
	trailing := strings.Split(strings.TrimRight(withTrailing, "\n"), "\n")
	if len(trailing) != 4 {
		t.Fatalf("trailing lines = %d", len(trailing))
	}
}

func TestExportSessionToJsonl(t *testing.T) {
	manager := newTestSessionManager(t)
	manager.AppendMessage(&ai.UserMessage{Content: ai.StringOrBlocks{Text: "hello"}})

	// A relative path resolves against the process cwd.
	dir := t.TempDir()
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(previous)
	path, err := ExportSessionToJsonl(manager, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(filepath.Base(path), "session-") || !strings.HasSuffix(path, ".jsonl") {
		t.Fatalf("path = %q", path)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"hello"`) {
		t.Fatalf("content = %s", raw)
	}

	// An explicit path is honored, with missing directories created.
	nested := filepath.Join(t.TempDir(), "deep", "dir", "session.jsonl")
	path, err = ExportSessionToJsonl(manager, nested, nil)
	if err != nil {
		t.Fatal(err)
	}
	if path != nested {
		t.Fatalf("path = %q", path)
	}
	if _, err := os.Stat(nested); err != nil {
		t.Fatal(err)
	}
}

func TestRedactURL(t *testing.T) {
	cases := map[string]string{
		"https://x.example.com/path?a=1&b=2":                 "https://x.example.com/path?a=1&b=2",
		"https://user:pass@x.example.com/path":               "https://x.example.com/path",
		"https://x.example.com/?api_key=abc&x=1":             "https://x.example.com/?api_key=%3Credacted%3E&x=1",
		"https://x.example.com/?apiKey=abc":                  "https://x.example.com/?apiKey=%3Credacted%3E",
		"https://x.example.com/?access_token=abc":            "https://x.example.com/?access_token=%3Credacted%3E",
		"postgres://user:secret@db.example.com/d?password=p": "postgres://db.example.com/d?password=%3Credacted%3E",
		"not a url": "not a url",
	}
	for input, want := range cases {
		if got := RedactURL(input); got != want {
			t.Errorf("RedactURL(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestRedactJSONValue(t *testing.T) {
	input := map[string]any{
		"apiKey":        "secret",
		"Authorization": "Bearer x",
		"nested": map[string]any{
			"userToken": "t",
			"safe":      "https://u:p@host/?password=z",
		},
		"list": []any{"https://h/?secret=1", 42},
		"url":  "https://plain.example.com/a",
	}
	redacted := RedactJSONValue(input)
	encoded, err := json.Marshal(redacted)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["apiKey"] != "<redacted>" || decoded["Authorization"] != "<redacted>" {
		t.Fatalf("top-level secrets survived: %s", encoded)
	}
	nested := decoded["nested"].(map[string]any)
	if nested["userToken"] != "<redacted>" {
		t.Fatalf("nested secret survived: %s", encoded)
	}
	// URL credentials are stripped recursively, including inside lists.
	if !strings.Contains(nested["safe"].(string), "https://host/?password=%3Credacted%3E") {
		t.Fatalf("safe = %v", nested["safe"])
	}
	list := decoded["list"].([]any)
	if !strings.Contains(list[0].(string), "secret=%3Credacted%3E") {
		t.Fatalf("list = %v", list)
	}
	if list[1] != float64(42) {
		t.Fatalf("list = %v", list)
	}
	// Safe URLs survive.
	if decoded["url"] != "https://plain.example.com/a" {
		t.Fatalf("url = %v", decoded["url"])
	}
}

func TestCollectBugReportMetadata(t *testing.T) {
	runtime := runtimeWithProviders(t, stubProvider("anthropic"))
	model := runtime.GetModel("anthropic", "anthropic-model")
	metadata := CollectBugReportMetadata(CollectBugReportMetadataOptions{
		SessionID: "session-1", Cwd: "/tmp/work", IncludeSession: true,
		IncludeSummary: true, MessageCount: 7, Model: model,
		ModelRuntime: runtime, ThinkingLevel: ai.ThinkHigh,
		Hint: "  it crashed  ",
	})
	if metadata["schemaVersion"] != BugReportSchemaVersion {
		t.Fatalf("schemaVersion = %v", metadata["schemaVersion"])
	}
	if metadata["hint"] != "it crashed" {
		t.Fatalf("hint = %v", metadata["hint"])
	}
	environment := metadata["environment"].(map[string]any)
	if environment["platform"] == "" || environment["userAgent"] == "" {
		t.Fatalf("environment = %+v", environment)
	}
	session := metadata["session"].(map[string]any)
	if session["id"] != "session-1" || session["cwd"] != "/tmp/work" || session["messageCount"] != 7 {
		t.Fatalf("session = %+v", session)
	}
	provider := metadata["provider"].(map[string]any)
	if provider["id"] != "anthropic" {
		t.Fatalf("provider = %+v", provider)
	}
	if metadata["model"] == nil {
		t.Fatal("model missing")
	}

	// Without a session the cwd is omitted and the hint is null.
	metadata = CollectBugReportMetadata(CollectBugReportMetadataOptions{SessionID: "s2"})
	session = metadata["session"].(map[string]any)
	if _, ok := session["cwd"]; ok {
		t.Fatalf("session = %+v", session)
	}
	if metadata["hint"] != nil || metadata["model"] != nil || metadata["provider"] != nil {
		t.Fatalf("metadata = %+v", metadata)
	}
}

func TestCollectBugReportDiagnostics(t *testing.T) {
	manager := newTestSessionManager(t)
	manager.AppendMessage(&ai.UserMessage{Content: ai.StringOrBlocks{Text: "hi"}})
	errorText := "boom"
	manager.AppendMessage(&ai.AssistantMessage{
		API: ai.APIAnthropicMessages, Provider: "anthropic", Model: "m",
		Content: ai.ContentList{}, StopReason: ai.StopError, ErrorMessage: &errorText,
	})
	manager.AppendMessage(&ai.AssistantMessage{
		API: ai.APIAnthropicMessages, Provider: "anthropic", Model: "m",
		Content: ai.ContentList{ai.TextContent{Text: "fine"}}, StopReason: ai.StopStop,
	})
	diagnostics := CollectBugReportDiagnostics(manager)
	if diagnostics["entryCount"] != 3 || diagnostics["assistantMessageCount"] != 2 {
		t.Fatalf("diagnostics = %+v", diagnostics)
	}
	assistant := diagnostics["assistant"].([]map[string]any)
	if len(assistant) != 1 || assistant[0]["stopReason"] != ai.StopError || assistant[0]["errorMessage"] != "boom" {
		t.Fatalf("assistant = %+v", assistant)
	}
}

func TestBugReportFiles(t *testing.T) {
	bundle := BugReportBundle{
		Metadata:     map[string]any{"id": "report-1"},
		Diagnostics:  map[string]any{"entryCount": 3},
		SessionJSONL: "{\"type\":\"session\"}\n",
		Summary:      "## What the user was doing",
	}
	files := BugReportFiles(bundle)
	names := make([]string, 0, len(files))
	for _, file := range files {
		names = append(names, file.Name)
	}
	if strings.Join(names, ",") != "report.json,diagnostics.json,session.jsonl,summary.md" {
		t.Fatalf("names = %v", names)
	}
	for _, file := range files {
		if !strings.HasSuffix(file.Data, "\n") {
			t.Errorf("%s must end with a newline", file.Name)
		}
	}
	// Optional files are omitted when empty.
	bundle.SessionJSONL = ""
	bundle.Summary = ""
	if len(BugReportFiles(bundle)) != 2 {
		t.Fatal("optional files must be omitted")
	}
	if BugReportArchiveFileName("abc") != "pi-bug-report-abc.zip" {
		t.Fatal("archive name")
	}
}

func TestGenerateBugReportSummary(t *testing.T) {
	var runs atomic.Int64
	var seenPrompt string
	var seenReasoning ai.ThinkingLevel
	streamFn := func(model *ai.Model, context ai.TranscriptContext, options *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
		runs.Add(1)
		seenReasoning = options.Reasoning
		for _, message := range context.Messages {
			if user, ok := message.(*ai.UserMessage); ok {
				seenPrompt = contentTextJoinedNoSep(user.Content)
			}
		}
		stream := ai.NewAssistantMessageEventStream()
		go func() {
			message := &ai.AssistantMessage{
				API: ai.APIAnthropicMessages, Provider: model.Provider, Model: model.ID,
				Content: ai.ContentList{ai.TextContent{Text: "## What went wrong\nit broke"}}, StopReason: ai.StopStop,
			}
			stream.Push(ai.AssistantMessageEvent{Type: ai.EventDone, Reason: ai.StopStop, Message: message})
		}()
		return stream
	}
	messages := []ai.Message{
		&ai.UserMessage{Content: ai.StringOrBlocks{Text: "first"}},
		&ai.AssistantMessage{API: ai.APIAnthropicMessages, Provider: "anthropic", Model: "m",
			Content: ai.ContentList{ai.TextContent{Text: "reply"}}, StopReason: ai.StopStop},
	}
	model := &ai.Model{ID: "m", API: ai.APIAnthropicMessages, Provider: "anthropic",
		Reasoning: true, ContextWindow: 100000, MaxTokens: 8192}
	summary, err := GenerateBugReportSummary(GenerateBugReportSummaryOptions{
		Messages: messages, Hint: "it crashed on start", Model: model,
		Ctx: ctxpkg.Background(), ThinkingLevel: ai.ThinkHigh, StreamFn: streamFn,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(summary, "## What went wrong") {
		t.Fatalf("summary = %q", summary)
	}
	if !strings.Contains(seenPrompt, "<conversation>") || !strings.Contains(seenPrompt, "<user-report>") ||
		!strings.Contains(seenPrompt, "it crashed on start") || !strings.Contains(seenPrompt, "## Steps to reproduce") {
		t.Fatalf("prompt = %q", seenPrompt)
	}
	if seenReasoning != ai.ThinkHigh {
		t.Fatalf("reasoning = %q", seenReasoning)
	}
	if runs.Load() != 1 {
		t.Fatalf("runs = %d", runs.Load())
	}

	// A tool call in the response is an error.
	toolFn := func(model *ai.Model, context ai.TranscriptContext, options *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
		stream := ai.NewAssistantMessageEventStream()
		go func() {
			message := &ai.AssistantMessage{
				API: ai.APIAnthropicMessages, Provider: model.Provider, Model: model.ID,
				Content:    ai.ContentList{ai.ToolCall{ID: "t", Name: "read", Arguments: []byte(`{}`)}},
				StopReason: ai.StopToolUse,
			}
			stream.Push(ai.AssistantMessageEvent{Type: ai.EventDone, Reason: ai.StopToolUse, Message: message})
		}()
		return stream
	}
	if _, err := GenerateBugReportSummary(GenerateBugReportSummaryOptions{
		Messages: messages, Model: model, StreamFn: toolFn,
	}); err == nil || err.Error() != "Bug report summary attempted to call a tool" {
		t.Fatalf("err = %v", err)
	}

	// An empty response is an error.
	emptyFn := func(model *ai.Model, context ai.TranscriptContext, options *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
		stream := ai.NewAssistantMessageEventStream()
		go func() {
			message := &ai.AssistantMessage{
				API: ai.APIAnthropicMessages, Provider: model.Provider, Model: model.ID,
				Content: ai.ContentList{}, StopReason: ai.StopStop,
			}
			stream.Push(ai.AssistantMessageEvent{Type: ai.EventDone, Reason: ai.StopStop, Message: message})
		}()
		return stream
	}
	if _, err := GenerateBugReportSummary(GenerateBugReportSummaryOptions{
		Messages: messages, Model: model, StreamFn: emptyFn,
	}); err == nil || err.Error() != "Bug report summary was empty" {
		t.Fatalf("err = %v", err)
	}
}

func TestSummarizeForBugReport(t *testing.T) {
	var runs atomic.Int64
	model := &ai.Model{ID: "m", API: ai.APIAnthropicMessages, Provider: "anthropic",
		Reasoning: true, ContextWindow: 100000, MaxTokens: 8192}
	session := newControlSession(t, model, nil)
	session.control.ModelRuntime = runtimeWithProviders(t, stubProvider("anthropic"))
	if err := session.control.ModelRuntime.SetRuntimeAPIKey("anthropic", "sk-1", ctxpkg.Background()); err != nil {
		t.Fatal(err)
	}
	session.SetStreamFnForTest(func(failModel *ai.Model, context ai.TranscriptContext, options *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
		runs.Add(1)
		stream := ai.NewAssistantMessageEventStream()
		go func() {
			message := &ai.AssistantMessage{
				API: ai.APIAnthropicMessages, Provider: failModel.Provider, Model: failModel.ID,
				Content: ai.ContentList{ai.TextContent{Text: "report body"}}, StopReason: ai.StopStop,
			}
			stream.Push(ai.AssistantMessageEvent{Type: ai.EventDone, Reason: ai.StopStop, Message: message})
		}()
		return stream
	})
	if err := session.Prompt(ctxpkg.Background(), "hello", nil); err != nil {
		t.Fatal(err)
	}
	summary, err := session.SummarizeForBugReport(ctxpkg.Background(), "hint text")
	if err != nil {
		t.Fatal(err)
	}
	if summary != "report body" || runs.Load() != 1 {
		t.Fatalf("summary = %q runs = %d", summary, runs.Load())
	}

	// Without a model the summary fails.
	session.Agent.SetModel(nil)
	if _, err := session.SummarizeForBugReport(ctxpkg.Background(), ""); err == nil ||
		err.Error() != "No model selected" {
		t.Fatalf("err = %v", err)
	}
}

// SetStreamFnForTest replaces the session stream function (test wiring).
func (s *AgentSession) SetStreamFnForTest(streamFn agent.StreamFn) {
	s.streamFn = streamFn
}
