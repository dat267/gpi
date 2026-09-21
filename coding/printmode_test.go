package coding

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/dat267/pier/ai"
)

// imagesFromTranscript collects the image blocks of the last user message.
func imagesFromTranscript(context ai.TranscriptContext) []ai.ImageContent {
	var images []ai.ImageContent
	for _, message := range context.Messages {
		user, ok := message.(*ai.UserMessage)
		if !ok {
			continue
		}
		for _, block := range user.Content.Blocks {
			if image, ok := block.(ai.ImageContent); ok {
				images = append(images, image)
			}
		}
	}
	return images
}

// Tests for modes/print-mode.ts (the extension-free core).

func newPrintModeSession(t *testing.T, responses ...*ai.AssistantMessage) (*AgentSession, *SessionManager) {
	t.Helper()
	dir := t.TempDir()
	sessions := NewSessionManager(dir, &SessionManagerOptions{SessionDir: dir})
	sessions.NewSession(&NewSessionOptions{ID: "print-mode-session"})
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

func TestRunPrintModeTextOutput(t *testing.T) {
	session, _ := newPrintModeSession(t, createAssistantMessageT("the answer"))
	var stdout, stderr bytes.Buffer

	exitCode := RunPrintMode(session, PrintModeOptions{
		Mode:           CLIModeText,
		InitialMessage: "say the answer",
		Stdout:         &stdout,
		Stderr:         &stderr,
	})
	if exitCode != 0 {
		t.Fatalf("exit = %d stderr = %q", exitCode, stderr.String())
	}
	if stdout.String() != "the answer\n" {
		t.Fatalf("stdout = %q", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q", stderr.String())
	}

	// Multiple prompts run in order.
	session, _ = newPrintModeSession(t, createAssistantMessageT("first"), createAssistantMessageT("second"))
	stdout.Reset()
	stderr.Reset()
	exitCode = RunPrintMode(session, PrintModeOptions{
		Mode:           CLIModeText,
		InitialMessage: "one",
		Messages:       []string{"two"},
		Stdout:         &stdout,
		Stderr:         &stderr,
	})
	if exitCode != 0 {
		t.Fatalf("exit = %d stderr = %q", exitCode, stderr.String())
	}
	// Text mode prints only the final assistant message.
	if stdout.String() != "second\n" {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestRunPrintModeTextFailures(t *testing.T) {
	errorMessage := "provider failure"
	failed := createAssistantMessageT("")
	failed.StopReason = ai.StopError
	failed.ErrorMessage = &errorMessage
	session, _ := newPrintModeSession(t, failed)

	var stdout, stderr bytes.Buffer
	exitCode := RunPrintMode(session, PrintModeOptions{Mode: CLIModeText, InitialMessage: "go", Stdout: &stdout, Stderr: &stderr})
	if exitCode != 1 {
		t.Fatalf("exit = %d", exitCode)
	}
	if strings.TrimSpace(stderr.String()) != "provider failure" {
		t.Fatalf("stderr = %q", stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q", stdout.String())
	}

	// Without an error message the stop reason is reported.
	aborted := createAssistantMessageT("")
	aborted.StopReason = ai.StopAborted
	session, _ = newPrintModeSession(t, aborted)
	stdout.Reset()
	stderr.Reset()
	exitCode = RunPrintMode(session, PrintModeOptions{Mode: CLIModeText, InitialMessage: "go", Stdout: &stdout, Stderr: &stderr})
	if exitCode != 1 || strings.TrimSpace(stderr.String()) != "Request aborted" {
		t.Fatalf("exit = %d stderr = %q", exitCode, stderr.String())
	}
}

func TestRunPrintModeJSONStream(t *testing.T) {
	session, sessions := newPrintModeSession(t, createAssistantMessageT("json answer"))
	var stdout, stderr bytes.Buffer

	exitCode := RunPrintMode(session, PrintModeOptions{
		Mode:           CLIModeJSON,
		InitialMessage: "answer in json",
		Stdout:         &stdout,
		Stderr:         &stderr,
	})
	if exitCode != 0 {
		t.Fatalf("exit = %d stderr = %q", exitCode, stderr.String())
	}

	lines := strings.Split(strings.TrimRight(stdout.String(), "\n"), "\n")
	if len(lines) < 3 {
		t.Fatalf("lines = %#v", lines)
	}
	// The first line is the session header.
	var header map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &header); err != nil {
		t.Fatalf("header: %v", err)
	}
	if header["id"] != "print-mode-session" {
		t.Fatalf("header = %#v", header)
	}
	if header["cwd"] != sessions.GetCwd() {
		t.Fatalf("header cwd = %#v", header["cwd"])
	}

	// Every following line is a JSON event object.
	types := map[string]bool{}
	for _, line := range lines[1:] {
		var event map[string]any
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("event line %q: %v", line, err)
		}
		eventType, ok := event["type"].(string)
		if !ok {
			t.Fatalf("event has no type: %#v", event)
		}
		types[eventType] = true
	}
	for _, expected := range []string{SessionAgentStart, SessionMessageStart, SessionMessageEnd, SessionAgentEnd} {
		if !types[expected] {
			t.Fatalf("missing event %q in %#v", expected, types)
		}
	}

	// Text mode prints nothing extra in JSON mode.
	if strings.Contains(stdout.String(), "json answer\njson answer") {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestRunPrintModeForwardsInitialImages(t *testing.T) {
	dir := t.TempDir()
	sessions := NewSessionManager(dir, &SessionManagerOptions{SessionDir: dir})

	var mu sync.Mutex
	var seenImages [][]ai.ImageContent
	streamFn := func(model *ai.Model, context ai.TranscriptContext, options *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
		stream := ai.NewAssistantMessageEventStream()
		mu.Lock()
		// The images reach the provider through the transcript context's last
		// user message.
		images := imagesFromTranscript(context)
		seenImages = append(seenImages, images)
		mu.Unlock()
		go func() {
			stream.Push(ai.AssistantMessageEvent{Type: ai.EventDone, Reason: ai.StopStop, Message: createAssistantMessageT("ok")})
		}()
		return stream
	}
	session, err := NewAgentSession(&SessionConfig{
		Cwd:      dir,
		Model:    &ai.Model{ID: "mock", API: "openai-responses", Provider: "openai", ContextWindow: 200000},
		StreamFn: streamFn,
		Sessions: sessions,
	})
	if err != nil {
		t.Fatal(err)
	}

	image := ai.ImageContent{Data: "abc", MimeType: "image/png"}
	var stdout, stderr bytes.Buffer
	exitCode := RunPrintMode(session, PrintModeOptions{
		Mode:           CLIModeText,
		InitialMessage: "what is this",
		InitialImages:  []ai.ImageContent{image},
		Stdout:         &stdout,
		Stderr:         &stderr,
	})
	if exitCode != 0 {
		t.Fatalf("exit = %d stderr = %q", exitCode, stderr.String())
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seenImages) != 1 || len(seenImages[0]) != 1 || seenImages[0][0].Data != "abc" {
		t.Fatalf("images = %#v", seenImages)
	}
}

func TestRunPrintModePromptFailure(t *testing.T) {
	// A stream that terminates with an error makes the prompt fail.
	dir := t.TempDir()
	sessions := NewSessionManager(dir, &SessionManagerOptions{SessionDir: dir})
	streamFn := func(model *ai.Model, context ai.TranscriptContext, options *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
		stream := ai.NewAssistantMessageEventStream()
		go func() {
			message := createAssistantMessageT("")
			message.StopReason = ai.StopError
			stream.Push(ai.AssistantMessageEvent{Type: ai.EventError, Reason: ai.StopError, Error: message})
		}()
		return stream
	}
	session, err := NewAgentSession(&SessionConfig{
		Cwd:      dir,
		Model:    &ai.Model{ID: "mock", API: "openai-responses", Provider: "openai", ContextWindow: 200000},
		StreamFn: streamFn,
		Sessions: sessions,
	})
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	exitCode := RunPrintMode(session, PrintModeOptions{Mode: CLIModeText, InitialMessage: "go", Stdout: &stdout, Stderr: &stderr})
	if exitCode != 1 || stderr.Len() == 0 {
		t.Fatalf("exit = %d stderr = %q", exitCode, stderr.String())
	}

	// A nil session fails rather than panicking.
	if exitCode := RunPrintMode(nil, PrintModeOptions{Mode: CLIModeText}); exitCode != 1 {
		t.Fatalf("exit = %d", exitCode)
	}
}

func TestFormatJSONEventLine(t *testing.T) {
	session, _ := newPrintModeSession(t, createAssistantMessageT("line"))
	var events []*SessionEvent
	unsubscribe := session.Subscribe(func(event *SessionEvent) { events = append(events, event) })
	defer unsubscribe()
	if err := session.PromptText(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	if len(events) == 0 {
		t.Fatal("no events")
	}
	for _, event := range events {
		line, err := FormatJSONEventLine(event)
		if err != nil {
			t.Fatalf("event %s: %v", event.Type, err)
		}
		if strings.Contains(line, "\n") {
			t.Fatalf("line must be single-line: %q", line)
		}
		var decoded map[string]any
		if err := json.Unmarshal([]byte(line), &decoded); err != nil {
			t.Fatalf("line %q: %v", line, err)
		}
		if decoded["type"] != event.Type {
			t.Fatalf("type = %#v", decoded["type"])
		}
	}
}
