package coding

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/dat267/pier/agent"
	"github.com/dat267/pier/ai"
)

// RunPrintMode drives an AgentSession to completion headlessly: text mode
// prints the last assistant message's text; json mode streams the session
// header plus one JSON object per session event, with the cumulative partial
// snapshots stripped from message_update (upstream toJsonEvent).

func captureStdout(t *testing.T, run func()) string {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	saved := os.Stdout
	os.Stdout = writer
	defer func() { os.Stdout = saved }()
	run()
	_ = writer.Close()
	out := make([]byte, 64*1024)
	n, _ := reader.Read(out)
	_ = reader.Close()
	return string(out[:n])
}

func newPrintSession(t *testing.T, responses ...*ai.AssistantMessage) (*AgentSession, *SessionManager) {
	t.Helper()
	dir := t.TempDir()
	sessions := NewSessionManager(dir, &SessionManagerOptions{SessionDir: dir})
	session, err := NewAgentSession(&SessionConfig{
		Cwd:      dir,
		Model:    &ai.Model{ID: "mock", API: "openai-responses", Provider: "openai", ContextWindow: 200000},
		StreamFn: mockSessionStreamFn(responses...),
		Sessions: sessions,
	})
	if err != nil {
		t.Fatal(err)
	}
	return session, sessions
}

func TestPrintModeTextPrintsTheFinalResponse(t *testing.T) {
	session, sessions := newPrintSession(t, createAssistantMessageT("hello from print"))
	var exitCode int
	output := captureStdout(t, func() {
		exitCode = RunPrintMode(session, sessions, PrintModeOptions{
			Mode:           CLIModeText,
			InitialMessage: "say hello",
		})
	})
	if exitCode != 0 {
		t.Fatalf("exit code = %d", exitCode)
	}
	if strings.TrimSpace(output) != "hello from print" {
		t.Fatalf("output = %q", output)
	}
	// The transcript survives: the prompt and response persisted. GetEntries
	// excludes the header line, so this is user + assistant.
	sessions.FlushWrites()
	entries := sessions.GetEntries()
	if len(entries) != 2 {
		t.Fatalf("entries = %d", len(entries))
	}
}

func TestPrintModeTextReportsAnErroredResponse(t *testing.T) {
	errored := createAssistantMessageT("")
	errored.StopReason = ai.StopError
	message := "provider exploded"
	errored.ErrorMessage = &message
	session, sessions := newPrintSession(t, errored)
	var exitCode int
	output := captureStdout(t, func() {
		exitCode = RunPrintMode(session, sessions, PrintModeOptions{
			Mode:           CLIModeText,
			InitialMessage: "break",
		})
	})
	if exitCode != 1 {
		t.Fatalf("exit code = %d", exitCode)
	}
	if strings.Contains(output, "provider exploded") {
		t.Fatalf("error went to stdout: %q", output)
	}
}

func TestPrintModeJSONStreamsHeaderAndEvents(t *testing.T) {
	session, sessions := newPrintSession(t, createAssistantMessageT("reply"))
	var exitCode int
	output := captureStdout(t, func() {
		exitCode = RunPrintMode(session, sessions, PrintModeOptions{
			Mode:           CLIModeJSON,
			InitialMessage: "hi",
		})
	})
	if exitCode != 0 {
		t.Fatalf("exit code = %d", exitCode)
	}
	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) < 3 { // header + at least agent_start..agent_end
		t.Fatalf("too few lines: %d", len(lines))
	}
	var header map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &header); err != nil {
		t.Fatalf("header line: %v", err)
	}
	if header["type"] != "session" {
		t.Fatalf("header = %#v", header)
	}
	types := make([]string, 0, len(lines)-1)
	sawPartial := false
	for _, line := range lines[1:] {
		var event map[string]any
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("event line %q: %v", line, err)
		}
		typ, _ := event["type"].(string)
		types = append(types, typ)
		if inner, ok := event["assistantMessageEvent"].(map[string]any); ok {
			if _, has := inner["partial"]; has {
				sawPartial = true
			}
		}
	}
	if sawPartial {
		t.Fatal("message_update events must not carry cumulative partial snapshots")
	}
	if types[0] != "agent_start" {
		t.Fatalf("first event = %q", types[0])
	}
	if types[len(types)-1] != "agent_end" {
		t.Fatalf("last event = %q", types[len(types)-1])
	}
	for _, wanted := range []string{"message_start", "message_end", "turn_end"} {
		found := false
		for _, typ := range types {
			if typ == wanted {
				found = true
			}
		}
		if !found {
			t.Fatalf("missing %s event in %v", wanted, types)
		}
	}
}

func TestMarshalJSONSessionEventStripsPartialAndNamesToolCalls(t *testing.T) {
	assistant := createAssistantMessageT("")
	assistant.Content = ai.ContentList{ai.ToolCall{ID: "call_1", Name: "bash"}}
	event := &SessionEvent{
		Type: SessionMessageUpdate,
		Agent: &agent.AgentEvent{
			Type:                  agent.MessageUpdate,
			Message:               assistant,
			AssistantMessageEvent: &ai.AssistantMessageEvent{Type: ai.EventToolcallStart, ContentIndex: 0, Partial: assistant},
		},
	}
	encoded, err := MarshalJSONSessionEvent(event)
	if err != nil {
		t.Fatal(err)
	}
	var shaped map[string]any
	if err := json.Unmarshal(encoded, &shaped); err != nil {
		t.Fatal(err)
	}
	inner, ok := shaped["assistantMessageEvent"].(map[string]any)
	if !ok {
		t.Fatalf("assistantMessageEvent = %#v", shaped)
	}
	if _, has := inner["partial"]; has {
		t.Fatal("partial must be stripped")
	}
	if inner["id"] != "call_1" || inner["toolName"] != "bash" {
		t.Fatalf("toolcall identity missing: %#v", inner)
	}
	if shaped["usage"] == nil {
		t.Fatal("usage must stay (constant size)")
	}
}
