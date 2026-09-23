package coding

import (
	"testing"

	"github.com/dat267/pier/ai"
)

// A branch fork's leaf is the entry it was taken at. That is the whole point:
// the session format stores no leaf marker, so a session's leaf is its last
// entry — which is why the fork copies the *path* to the chosen entry rather
// than every entry.
func TestForkSessionAtEntryKeepsTheChosenLeaf(t *testing.T) {
	t.Setenv("PI_CODING_AGENT_DIR", t.TempDir())
	cwd := t.TempDir()
	source := NewSessionManager(cwd, nil)
	source.AppendMessage(&ai.UserMessage{Content: ai.StringOrBlocks{Text: "first"}})
	source.AppendMessage(&ai.AssistantMessage{
		API: ai.APIAnthropicMessages, Provider: "anthropic", Model: "m",
		Content: ai.ContentList{ai.TextContent{Text: "hi"}}, StopReason: ai.StopStop,
	})
	source.AppendMessage(&ai.UserMessage{Content: ai.StringOrBlocks{Text: "second"}})

	entries := source.GetEntries()
	if len(entries) != 3 {
		t.Fatalf("source entries = %d, want 3", len(entries))
	}
	firstUserID := entries[0].ID

	t.Run("at the first message", func(t *testing.T) {
		forked, err := ForkSessionAtEntry(source, firstUserID, cwd, "", nil)
		if err != nil {
			t.Fatal(err)
		}
		if leaf := forked.GetLeafID(); leaf == nil || *leaf != firstUserID {
			t.Errorf("leaf = %v, want %q", leaf, firstUserID)
		}
		if got := len(forked.GetEntries()); got != 1 {
			t.Errorf("entries = %d, want the single entry at the fork point", got)
		}
		if forked.GetSessionID() == source.GetSessionID() {
			t.Error("the fork kept the source session id")
		}
	})

	t.Run("at the current leaf keeps everything", func(t *testing.T) {
		leaf := source.GetLeafID()
		forked, err := ForkSessionAtEntry(source, *leaf, cwd, "", nil)
		if err != nil {
			t.Fatal(err)
		}
		if got := len(forked.GetEntries()); got != 3 {
			t.Errorf("entries = %d, want 3", got)
		}
		if forked.GetLeafID() == nil || *forked.GetLeafID() != entries[2].ID {
			t.Errorf("leaf = %v, want the last entry", forked.GetLeafID())
		}
	})

	t.Run("an empty target forks an empty session", func(t *testing.T) {
		forked, err := ForkSessionAtEntry(source, "", cwd, "", nil)
		if err != nil {
			t.Fatal(err)
		}
		if got := len(forked.GetEntries()); got != 0 {
			t.Errorf("entries = %d, want none", got)
		}
		if forked.GetLeafID() != nil {
			t.Errorf("leaf = %v, want none", forked.GetLeafID())
		}
	})

	t.Run("an unknown entry is rejected", func(t *testing.T) {
		if _, err := ForkSessionAtEntry(source, "no-such-entry", cwd, "", nil); err == nil {
			t.Error("an unknown entry id should be an error")
		}
	})

	// The source is untouched.
	if got := len(source.GetEntries()); got != 3 {
		t.Errorf("source entries = %d, want 3", got)
	}
}
