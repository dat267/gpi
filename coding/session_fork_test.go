package coding

import (
	"os"
	"strings"
	"testing"

	"github.com/dat267/pier/ai"
)

func TestForkSessionCopiesEntriesUnderNewID(t *testing.T) {
	t.Setenv("PI_CODING_AGENT_DIR", t.TempDir())
	sourceCwd := t.TempDir()
	targetCwd := t.TempDir()
	source := NewSessionManager(sourceCwd, nil)
	source.AppendMessage(&ai.UserMessage{Content: ai.StringOrBlocks{Text: "hello"}})
	source.AppendMessage(&ai.AssistantMessage{
		API:        ai.APIAnthropicMessages,
		Provider:   "anthropic",
		Model:      "m",
		Content:    ai.ContentList{ai.TextContent{Text: "hi"}},
		StopReason: ai.StopStop,
	})
	sourcePath := source.GetSessionFile()

	forked, err := ForkSession(sourcePath, targetCwd, "", nil)
	if err != nil {
		t.Fatalf("ForkSession: %v", err)
	}
	if forked.GetSessionID() == source.GetSessionID() {
		t.Fatal("forked session must have a new id")
	}
	if forked.GetCwd() != targetCwd {
		t.Fatalf("forked cwd %s, want %s", forked.GetCwd(), targetCwd)
	}
	header := forked.GetHeader()
	if header == nil || header.ParentSession == nil || *header.ParentSession != sourcePath {
		t.Fatalf("forked header parentSession %+v, want %s", header, sourcePath)
	}
	// The forked file lives in the target cwd's default session dir.
	if want := DefaultSessionDir(targetCwd, ""); forked.GetSessionDir() != want {
		t.Fatalf("forked dir %s, want %s", forked.GetSessionDir(), want)
	}
	// The source file is untouched and still has its own entries.
	if _, err := os.Stat(sourcePath); err != nil {
		t.Fatalf("source file missing: %v", err)
	}
	if got := len(forked.BuildContextEntriesForLeaf()); got != 2 {
		t.Fatalf("forked entry count %d, want 2 (user + assistant)", got)
	}
}

func TestForkSessionWithExplicitID(t *testing.T) {
	t.Setenv("PI_CODING_AGENT_DIR", t.TempDir())
	source := NewSessionManager(t.TempDir(), nil)
	source.AppendMessage(&ai.UserMessage{Content: ai.StringOrBlocks{Text: "hello"}})
	source.AppendMessage(&ai.AssistantMessage{
		API: ai.APIAnthropicMessages, Provider: "anthropic", Model: "m",
		Content: ai.ContentList{ai.TextContent{Text: "hi"}}, StopReason: ai.StopStop,
	})

	forked, err := ForkSession(source.GetSessionFile(), t.TempDir(), "", &NewSessionOptions{ID: "019463a7-f000-7000-8000-00000000000f"})
	if err != nil {
		t.Fatalf("ForkSession: %v", err)
	}
	if forked.GetSessionID() != "019463a7-f000-7000-8000-00000000000f" {
		t.Fatalf("forked id %s, want the explicit id", forked.GetSessionID())
	}
	if !strings.HasSuffix(forked.GetSessionFile(), "019463a7-f000-7000-8000-00000000000f.jsonl") {
		t.Fatalf("forked file %s should carry the explicit id", forked.GetSessionFile())
	}
}

func TestForkSessionInvalidIDRejected(t *testing.T) {
	t.Setenv("PI_CODING_AGENT_DIR", t.TempDir())
	source := NewSessionManager(t.TempDir(), nil)
	source.AppendMessage(&ai.UserMessage{Content: ai.StringOrBlocks{Text: "hello"}})
	source.AppendMessage(&ai.AssistantMessage{
		API: ai.APIAnthropicMessages, Provider: "anthropic", Model: "m",
		Content: ai.ContentList{ai.TextContent{Text: "hi"}}, StopReason: ai.StopStop,
	})

	if _, err := ForkSession(source.GetSessionFile(), t.TempDir(), "", &NewSessionOptions{ID: "bad id!"}); err == nil {
		t.Fatal("expected an error for an invalid fork id")
	}
}

func TestForkSessionEmptySourceRejected(t *testing.T) {
	t.Setenv("PI_CODING_AGENT_DIR", t.TempDir())
	if _, err := ForkSession("/nonexistent/session.jsonl", t.TempDir(), "", nil); err == nil {
		t.Fatal("expected an error for a missing/empty source")
	}
}
