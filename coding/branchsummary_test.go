package coding

import (
	ctxpkg "context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/dat267/gpi/agent"
	"github.com/dat267/gpi/ai"
)

// Round 106 tests: branch summarization and session tree navigation.

func branchEntries() []SessionEntry {
	parent := "root"
	return []SessionEntry{
		{Type: "message", SessionEntryBase: SessionEntryBase{ID: "root", Timestamp: "2024-01-01T00:00:00Z"},
			Message: mustJSONMessage(&ai.UserMessage{Content: ai.StringOrBlocks{Text: "start"}})},
		{Type: "message", SessionEntryBase: SessionEntryBase{ID: "a1", ParentID: &parent, Timestamp: "2024-01-01T00:01:00Z"},
			Message: mustJSONMessage(&ai.AssistantMessage{
				API: ai.APIAnthropicMessages, Provider: "anthropic", Model: "m",
				Content: ai.ContentList{
					ai.TextContent{Text: "reading"},
					ai.ToolCall{ID: "call-1", Name: "read", Arguments: json.RawMessage(`{"path":"/tmp/a.txt"}`)},
				},
				StopReason: ai.StopStop,
			})},
		{Type: "message", SessionEntryBase: SessionEntryBase{ID: "a2", ParentID: strPtr("a1"), Timestamp: "2024-01-01T00:02:00Z"},
			Message: mustJSONMessage(&ai.ToolResultMessage{
				ToolCallID: "call-1", ToolName: "read",
				Content: ai.UserContentList{ai.TextContent{Text: "file body"}},
			})},
		{Type: "message", SessionEntryBase: SessionEntryBase{ID: "a3", ParentID: strPtr("a2"), Timestamp: "2024-01-01T00:03:00Z"},
			Message: mustJSONMessage(&ai.AssistantMessage{
				API: ai.APIAnthropicMessages, Provider: "anthropic", Model: "m",
				Content: ai.ContentList{
					ai.ToolCall{ID: "call-2", Name: "edit", Arguments: json.RawMessage(`{"path":"/tmp/b.txt"}`)},
				},
				StopReason: ai.StopStop,
			})},
	}
}

func mustJSONMessage(message ai.Message) json.RawMessage {
	encoded, err := ai.MarshalMessage(message)
	if err != nil {
		panic(err)
	}
	return encoded
}

func sessionWithBranch(t *testing.T) *SessionManager {
	t.Helper()
	manager := NewSessionManager(t.TempDir(), nil)
	parent := "root"
	manager.AppendMessage(&ai.UserMessage{Content: ai.StringOrBlocks{Text: "start"}})
	rootID := manager.GetEntries()[0].ID
	parent = rootID
	manager.AppendMessage(&ai.AssistantMessage{
		API: ai.APIAnthropicMessages, Provider: "anthropic", Model: "m",
		Content: ai.ContentList{
			ai.TextContent{Text: "reading"},
			ai.ToolCall{ID: "call-1", Name: "read", Arguments: json.RawMessage(`{"path":"/tmp/a.txt"}`)},
		},
		StopReason: ai.StopStop,
	})
	manager.AppendMessage(&ai.ToolResultMessage{
		ToolCallID: "call-1", ToolName: "read", Content: ai.UserContentList{ai.TextContent{Text: "file body"}},
	})
	manager.AppendMessage(&ai.AssistantMessage{
		API: ai.APIAnthropicMessages, Provider: "anthropic", Model: "m",
		Content: ai.ContentList{
			ai.ToolCall{ID: "call-2", Name: "edit", Arguments: json.RawMessage(`{"path":"/tmp/b.txt"}`)},
		},
		StopReason: ai.StopStop,
	})
	entries := manager.GetEntries()
	_ = entries
	_ = parent
	return manager
}

func TestCollectEntriesForBranchSummary(t *testing.T) {
	manager := NewSessionManager(t.TempDir(), nil)
	first := manager.AppendMessage(&ai.UserMessage{Content: ai.StringOrBlocks{Text: "root"}})
	second := manager.AppendMessage(&ai.AssistantMessage{
		API: ai.APIAnthropicMessages, Provider: "anthropic", Model: "m",
		Content: ai.ContentList{ai.TextContent{Text: "branch one"}}, StopReason: ai.StopStop,
	})
	third := manager.AppendMessage(&ai.UserMessage{Content: ai.StringOrBlocks{Text: "leaf"}})

	// No old leaf means nothing to summarize.
	if result := CollectEntriesForBranchSummary(manager, "", third); len(result.Entries) != 0 || result.CommonAncestorID != "" {
		t.Fatalf("result = %+v", result)
	}

	// Walking from the leaf back to the root yields every entry chronologically.
	result := CollectEntriesForBranchSummary(manager, third, first)
	if len(result.Entries) != 2 {
		t.Fatalf("entries = %+v", result.Entries)
	}
	if result.Entries[0].ID != second || result.Entries[1].ID != third {
		t.Fatalf("entries = %+v", result.Entries)
	}
	if !result.HasAncestor || result.CommonAncestorID != first {
		t.Fatalf("result = %+v", result)
	}
}

func TestPrepareBranchEntries(t *testing.T) {
	entries := branchEntries()
	preparation := PrepareBranchEntries(entries, 0)
	// The tool result entry is skipped; the branch summary and compaction
	// summaries would be included when present.
	for _, message := range preparation.Messages {
		if _, ok := message.(*ai.ToolResultMessage); ok {
			t.Fatalf("tool results must be skipped: %+v", preparation.Messages)
		}
	}
	if len(preparation.Messages) != 3 {
		t.Fatalf("messages = %+v", preparation.Messages)
	}
	// File ops are collected from the tool calls.
	readFiles, modifiedFiles := ComputeFileLists(preparation.FileOps)
	if strings.Join(readFiles, ",") != "/tmp/a.txt" {
		t.Fatalf("readFiles = %v", readFiles)
	}
	if strings.Join(modifiedFiles, ",") != "/tmp/b.txt" {
		t.Fatalf("modifiedFiles = %v", modifiedFiles)
	}
	if preparation.TotalTokens <= 0 {
		t.Fatalf("totalTokens = %d", preparation.TotalTokens)
	}

	// A tiny budget drops older messages but keeps the newest.
	tiny := PrepareBranchEntries(entries, 1)
	if len(tiny.Messages) != 0 {
		t.Fatalf("messages = %+v", tiny.Messages)
	}
}

func TestPrepareBranchEntriesCumulativeFileOps(t *testing.T) {
	details, err := ai.MarshalJSON(BranchSummaryDetails{
		ReadFiles: []string{"/tmp/nested.txt"}, ModifiedFiles: []string{"/tmp/written.txt"},
	})
	if err != nil {
		t.Fatal(err)
	}
	entries := []SessionEntry{
		{Type: "branch_summary", Summary: "nested", Details: details},
	}
	preparation := PrepareBranchEntries(entries, 0)
	readFiles, modifiedFiles := ComputeFileLists(preparation.FileOps)
	if strings.Join(readFiles, ",") != "/tmp/nested.txt" {
		t.Fatalf("readFiles = %v", readFiles)
	}
	if strings.Join(modifiedFiles, ",") != "/tmp/written.txt" {
		t.Fatalf("modifiedFiles = %v", modifiedFiles)
	}

	// Extension-generated summaries do not contribute file ops.
	fromHook := true
	entries = []SessionEntry{
		{Type: "branch_summary", Summary: "nested", Details: details, FromHook: &fromHook},
	}
	preparation = PrepareBranchEntries(entries, 0)
	readFiles, modifiedFiles = ComputeFileLists(preparation.FileOps)
	if len(readFiles) != 0 || len(modifiedFiles) != 0 {
		t.Fatalf("readFiles = %v modifiedFiles = %v", readFiles, modifiedFiles)
	}
}

func TestGenerateBranchSummary(t *testing.T) {
	entries := branchEntries()
	calls := 0
	streamFn := func(model *ai.Model, context ai.TranscriptContext, options *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
		calls++
		stream := ai.NewAssistantMessageEventStream()
		go func() {
			message := &ai.AssistantMessage{
				API: ai.APIAnthropicMessages, Provider: "anthropic", Model: model.ID,
				Content:    ai.ContentList{ai.TextContent{Text: "## Goal\ndo the thing"}},
				StopReason: ai.StopStop,
				Usage:      ai.Usage{Input: 100, Output: 20, TotalTokens: 120},
			}
			stream.Push(ai.AssistantMessageEvent{Type: ai.EventDone, Reason: ai.StopStop, Message: message})
		}()
		return stream
	}
	model := &ai.Model{ID: "m", API: ai.APIAnthropicMessages, Provider: "anthropic", ContextWindow: 100000, MaxTokens: 8192}
	result, err := GenerateBranchSummary(entries, GenerateBranchSummaryOptions{
		Model: model, StreamFn: streamFn, Ctx: ctxpkg.Background(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("calls = %d", calls)
	}
	if !strings.HasPrefix(result.Summary, "The user explored a different conversation branch") ||
		!strings.Contains(result.Summary, "## Goal") {
		t.Fatalf("summary = %q", result.Summary)
	}
	if !strings.Contains(result.Summary, "/tmp/a.txt") || !strings.Contains(result.Summary, "/tmp/b.txt") {
		t.Fatalf("summary = %q", result.Summary)
	}
	if result.Usage == nil || result.Usage.Input != 100 {
		t.Fatalf("usage = %+v", result.Usage)
	}
	if strings.Join(result.ReadFiles, ",") != "/tmp/a.txt" || strings.Join(result.ModifiedFiles, ",") != "/tmp/b.txt" {
		t.Fatalf("files = %v %v", result.ReadFiles, result.ModifiedFiles)
	}

	// No content to summarize.
	empty, err := GenerateBranchSummary(nil, GenerateBranchSummaryOptions{Model: model, StreamFn: streamFn})
	if err != nil || empty.Summary != "No content to summarize" {
		t.Fatalf("result = %+v err = %v", empty, err)
	}

	// Custom instructions are appended, or replace the prompt when asked.
	var promptText string
	capture := func(model *ai.Model, context ai.TranscriptContext, options *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
		for _, message := range context.Messages {
			if user, ok := message.(*ai.UserMessage); ok {
				promptText = contentTextJoinedNoSep(user.Content)
			}
		}
		stream := ai.NewAssistantMessageEventStream()
		go func() {
			message := &ai.AssistantMessage{
				API: ai.APIAnthropicMessages, Provider: "anthropic", Model: model.ID,
				Content: ai.ContentList{ai.TextContent{Text: "body"}}, StopReason: ai.StopStop,
			}
			stream.Push(ai.AssistantMessageEvent{Type: ai.EventDone, Reason: ai.StopStop, Message: message})
		}()
		return stream
	}
	if _, err := GenerateBranchSummary(entries, GenerateBranchSummaryOptions{
		Model: model, StreamFn: capture, CustomInstructions: "focus on tests",
	}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(promptText, "Additional focus: focus on tests") {
		t.Fatalf("prompt = %q", promptText)
	}
	if _, err := GenerateBranchSummary(entries, GenerateBranchSummaryOptions{
		Model: model, StreamFn: capture, CustomInstructions: "only this", ReplaceInstructions: true,
	}); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(promptText, "\n\nonly this") {
		t.Fatalf("prompt = %q", promptText)
	}

	// An aborted response reports the abort.
	abortFn := func(model *ai.Model, context ai.TranscriptContext, options *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
		stream := ai.NewAssistantMessageEventStream()
		go func() {
			message := &ai.AssistantMessage{
				API: ai.APIAnthropicMessages, Provider: "anthropic", Model: model.ID,
				StopReason: ai.StopAborted,
			}
			stream.Push(ai.AssistantMessageEvent{Type: ai.EventError, Reason: ai.StopAborted, Error: message})
		}()
		return stream
	}
	aborted, err := GenerateBranchSummary(entries, GenerateBranchSummaryOptions{Model: model, StreamFn: abortFn})
	if err != nil || !aborted.Aborted {
		t.Fatalf("result = %+v err = %v", aborted, err)
	}

	// A tool call in the response is an error.
	toolFn := func(model *ai.Model, context ai.TranscriptContext, options *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
		stream := ai.NewAssistantMessageEventStream()
		go func() {
			message := &ai.AssistantMessage{
				API: ai.APIAnthropicMessages, Provider: "anthropic", Model: model.ID,
				Content:    ai.ContentList{ai.ToolCall{ID: "t", Name: "read", Arguments: json.RawMessage(`{}`)}},
				StopReason: ai.StopToolUse,
			}
			stream.Push(ai.AssistantMessageEvent{Type: ai.EventDone, Reason: ai.StopToolUse, Message: message})
		}()
		return stream
	}
	toolResult, err := GenerateBranchSummary(entries, GenerateBranchSummaryOptions{Model: model, StreamFn: toolFn})
	if err != nil || toolResult.Error != "Branch summarization attempted to call a tool" {
		t.Fatalf("result = %+v err = %v", toolResult, err)
	}

	// An errored response reports the summarization failure.
	errorFn := func(model *ai.Model, context ai.TranscriptContext, options *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
		stream := ai.NewAssistantMessageEventStream()
		go func() {
			errorText := "boom"
			message := &ai.AssistantMessage{
				API: ai.APIAnthropicMessages, Provider: "anthropic", Model: model.ID,
				StopReason: ai.StopError, ErrorMessage: &errorText,
			}
			stream.Push(ai.AssistantMessageEvent{Type: ai.EventError, Reason: ai.StopError, Error: message})
		}()
		return stream
	}
	failed, err := GenerateBranchSummary(entries, GenerateBranchSummaryOptions{Model: model, StreamFn: errorFn})
	if err != nil || failed.Error == "" || !strings.Contains(failed.Error, "Branch summarization") {
		t.Fatalf("result = %+v err = %v", failed, err)
	}
}

func TestNavigateTree(t *testing.T) {
	manager := sessionWithBranch(t)
	entries := manager.GetEntries()
	rootID := entries[0].ID
	leafID := entries[len(entries)-1].ID

	session, err := NewAgentSession(&SessionConfig{
		Cwd: t.TempDir(), Sessions: manager,
		Model:    &ai.Model{ID: "m", API: ai.APIAnthropicMessages, Provider: "anthropic", ContextWindow: 100000, MaxTokens: 8192},
		StreamFn: stubStreamFn,
	})
	if err != nil {
		t.Fatal(err)
	}
	session.control = &AgentSessionControl{}

	// Navigating to the current leaf is a no-op.
	result, err := session.NavigateTree(ctxpkg.Background(), leafID, NavigateTreeOptions{})
	if err != nil || result.Cancelled || result.EditorText != "" {
		t.Fatalf("result = %+v err = %v", result, err)
	}

	// Navigating to the root user message resets the leaf and returns the text.
	result, err = session.NavigateTree(ctxpkg.Background(), rootID, NavigateTreeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result.EditorText != "start" {
		t.Fatalf("editorText = %q", result.EditorText)
	}
	if leaf := session.Sessions.GetLeafID(); leaf != nil {
		t.Fatalf("leaf = %v", *leaf)
	}

	// An unknown target is an error.
	if _, err := session.NavigateTree(ctxpkg.Background(), "missing", NavigateTreeOptions{}); err == nil ||
		!strings.Contains(err.Error(), "Entry missing not found") {
		t.Fatalf("err = %v", err)
	}

	// Summarizing requires a model.
	modelless, err := NewAgentSession(&SessionConfig{Cwd: t.TempDir(), Sessions: manager, StreamFn: stubStreamFn})
	if err != nil {
		t.Fatal(err)
	}
	modelless.control = &AgentSessionControl{}
	if _, err := modelless.NavigateTree(ctxpkg.Background(), entries[1].ID, NavigateTreeOptions{Summarize: true}); err == nil ||
		err.Error() != "No model available for summarization" {
		t.Fatalf("err = %v", err)
	}
}

func TestNavigateTreeWithSummary(t *testing.T) {
	manager := sessionWithBranch(t)
	entries := manager.GetEntries()
	rootID := entries[0].ID
	leafID := entries[len(entries)-1].ID

	session, err := NewAgentSession(&SessionConfig{
		Cwd: t.TempDir(), Sessions: manager,
		Model:    &ai.Model{ID: "m", API: ai.APIAnthropicMessages, Provider: "anthropic", ContextWindow: 100000, MaxTokens: 8192},
		StreamFn: stubStreamFn,
	})
	if err != nil {
		t.Fatal(err)
	}
	session.control = &AgentSessionControl{}
	session.CompactionStreamFn = func(model *ai.Model, context ai.TranscriptContext, options *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
		stream := ai.NewAssistantMessageEventStream()
		go func() {
			message := &ai.AssistantMessage{
				API: ai.APIAnthropicMessages, Provider: "anthropic", Model: model.ID,
				Content:    ai.ContentList{ai.TextContent{Text: "branch summary body"}},
				StopReason: ai.StopStop,
			}
			stream.Push(ai.AssistantMessageEvent{Type: ai.EventDone, Reason: ai.StopStop, Message: message})
		}()
		return stream
	}

	result, err := session.NavigateTree(ctxpkg.Background(), rootID, NavigateTreeOptions{
		Summarize: true, Label: "explored",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.SummaryEntry == nil || !strings.Contains(result.SummaryEntry.Summary, "branch summary body") {
		t.Fatalf("summaryEntry = %+v", result.SummaryEntry)
	}
	// The summary records the branch position it left.
	if result.SummaryEntry.FromID != leafID {
		t.Fatalf("summaryEntry = %+v (wanted fromId %s)", result.SummaryEntry, leafID)
	}
	// The details carry the file lists.
	var details BranchSummaryDetails
	if err := json.Unmarshal(result.SummaryEntry.Details, &details); err != nil {
		t.Fatal(err)
	}
	if strings.Join(details.ReadFiles, ",") != "/tmp/a.txt" {
		t.Fatalf("details = %+v", details)
	}
	// A label entry follows the summary.
	after := session.Sessions.GetEntries()
	last := after[len(after)-1]
	if last.Type != "label" || last.Label == nil || *last.Label != "explored" {
		t.Fatalf("last = %+v", last)
	}

	// Without summarize, the label attaches to the target entry.
	if _, err := session.NavigateTree(ctxpkg.Background(), entries[1].ID, NavigateTreeOptions{Label: "target"}); err != nil {
		t.Fatal(err)
	}
	after = session.Sessions.GetEntries()
	last = after[len(after)-1]
	if last.Type != "label" || last.TargetID != entries[1].ID {
		t.Fatalf("last = %+v", last)
	}
}

func TestRestoreToolsFromTranscript(t *testing.T) {
	manager := NewSessionManager(t.TempDir(), nil)
	session, err := NewAgentSession(&SessionConfig{
		Cwd: t.TempDir(), Sessions: manager,
		Model:    &ai.Model{ID: "m", API: ai.APIAnthropicMessages, Provider: "anthropic"},
		StreamFn: stubStreamFn,
	})
	if err != nil {
		t.Fatal(err)
	}
	session.control = &AgentSessionControl{Tools: map[string]AgentToolDefinition{
		"read": {Tool: testTool("read")},
		"bash": {Tool: testTool("bash")},
	}}
	// The transcript's current system message declares the tools.
	prompt := &ai.SystemMessage{
		Content:    ai.StringOrBlocks{Text: "system"},
		ToolsAdded: []ai.Tool{{Name: "read", Description: "read", Parameters: json.RawMessage(`{}`)}, {Name: "unknown", Parameters: json.RawMessage(`{}`)}},
	}
	manager.AppendMessage(prompt)
	session.RestoreToolsFromTranscript()
	if names := session.GetActiveToolNames(); strings.Join(names, ",") != "read" {
		t.Fatalf("active = %v", names)
	}

	// With no system message in the transcript nothing changes.
	empty := NewSessionManager(t.TempDir(), nil)
	session2, err := NewAgentSession(&SessionConfig{Cwd: t.TempDir(), Sessions: empty, Model: session.Model(), StreamFn: stubStreamFn})
	if err != nil {
		t.Fatal(err)
	}
	session2.control = &AgentSessionControl{Tools: map[string]AgentToolDefinition{"read": {Tool: testTool("read")}}}
	session2.Agent.SetTools([]agent.AgentTool{testTool("read")})
	session2.RestoreToolsFromTranscript()
	if names := session2.GetActiveToolNames(); len(names) != 1 {
		t.Fatalf("active = %v", names)
	}
}
