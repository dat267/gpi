package coding

import (
	"testing"

	"github.com/dat267/pier/ai"
)

// newProjectionTestSession builds a session with enough messages that resolving
// the projection allocates visibly.
// newTestSessionForProjection is a small persisted-free session for the
// correctness tests.
func newTestSessionForProjection(t *testing.T) *SessionManager {
	t.Helper()
	tempAgentDir(t)
	persist := false
	return NewSessionManager(t.TempDir(), &SessionManagerOptions{SessionDir: t.TempDir(), Persist: &persist})
}

func newProjectionTestSession(t *testing.T) *SessionManager {
	t.Helper()
	persist := false
	m := NewSessionManager(t.TempDir(), &SessionManagerOptions{SessionDir: t.TempDir(), Persist: &persist})
	m.AppendMessage(&ai.SystemMessage{Content: ai.StringOrBlocks{Text: "system prompt"}, Timestamp: 1})
	m.AppendModelChange("anthropic", "claude-opus-4-5")
	for i := 0; i < 2000; i++ {
		m.AppendMessage(createUserMessage("a message with enough text that decoding it allocates"))
		m.AppendMessage(createAssistantMessageT("a reply with enough text that decoding it allocates"))
	}
	return m
}

// TestProjectionIsCachedUntilTheBranchChanges covers the module's cache: the
// projection decodes every message in the window, so repeat calls must be
// served from the cache until an append or a leaf move changes the version.
func TestProjectionIsCachedUntilTheBranchChanges(t *testing.T) {
	m := newProjectionTestSession(t)

	// Warm the cache: AllocsPerRun itself runs the function once first, so the
	// uncached resolution has to be measured through a version change.
	_ = m.Projection()
	hit := testing.AllocsPerRun(20, func() { _ = m.Projection() })
	if hit > 20 {
		t.Fatalf("a cached projection allocated %.0f objects per call; want ~0", hit)
	}

	// Every append changes the version and forces one resolution.
	rebuild := testing.AllocsPerRun(1, func() {
		m.AppendMessage(createUserMessage("after the cache was warm"))
		_ = m.Projection()
	})
	if rebuild < 100 {
		t.Fatalf("an append did not invalidate the projection (%.0f allocations)", rebuild)
	}

	// A leaf move invalidates it too.
	m.ResetLeaf()
	m.AppendMessage(createUserMessage("on a new root"))
	if context := m.Projection(); len(context.Messages) == 0 {
		t.Fatal("the projection lost the new branch")
	}
}

// TestProjectionSeesTheLatestAppend keeps the cache honest: the resolved
// messages always reflect the current branch.
func TestProjectionSeesTheLatestAppend(t *testing.T) {
	m := newTestSessionForProjection(t)
	m.AppendMessage(createUserMessage("first"))
	_ = m.Projection()
	m.AppendMessage(createUserMessage("second"))

	context := m.Projection()
	found := false
	for _, message := range context.Messages {
		if user, ok := message.(*ai.UserMessage); ok && user.Content.Text == "second" {
			found = true
		}
	}
	if !found {
		t.Fatalf("the cached projection missed the append: %d messages", len(context.Messages))
	}
	// The entries travel with the projection (upstream's buildSessionProjection
	// returns them too), so consumers stop re-walking the branch.
	if len(context.Entries) != len(context.Messages) {
		t.Fatalf("entries = %d, messages = %d", len(context.Entries), len(context.Messages))
	}
}

// TestProjectionSlicesAreSafeToAppendTo guards the cache against callers that
// append to the returned slices (the agent does exactly that with Messages).
func TestProjectionSlicesAreSafeToAppendTo(t *testing.T) {
	m := newTestSessionForProjection(t)
	m.AppendMessage(createUserMessage("one"))
	first := m.Projection()
	before := len(first.Messages)
	beforeEntries := len(first.Entries)

	first.Messages = append(first.Messages, createUserMessage("smuggled in"))
	first.Entries = append(first.Entries, SessionEntry{ID: "smuggled"})

	second := m.Projection()
	if len(second.Messages) != before || len(second.Entries) != beforeEntries {
		t.Fatalf("callers could write into the cached projection: %d messages, %d entries",
			len(second.Messages), len(second.Entries))
	}
}

// TestLatestCompactionMatchesTheBranch pins the accessor that replaced
// GetLatestCompactionEntry(GetBranch("")) at its call sites.
func TestLatestCompactionMatchesTheBranch(t *testing.T) {
	m := newTestSessionForProjection(t)
	m.AppendMessage(createUserMessage("old"))
	m.AppendMessage(createAssistantMessageT("reply"))
	entries := m.GetEntries()
	if got := m.LatestCompaction(); got != nil {
		t.Fatalf("latest compaction = %+v before one was appended", got)
	}
	id := m.AppendCompaction("summary", entries[len(entries)-1].ID, 1000, nil, false, nil)
	got := m.LatestCompaction()
	if got == nil || got.ID != id {
		t.Fatalf("latest compaction = %+v, want %s", got, id)
	}
	reference := GetLatestCompactionEntry(m.GetBranch(""))
	if reference == nil || reference.ID != got.ID {
		t.Fatalf("accessor %+v disagrees with the branch walk %+v", got, reference)
	}
}

// TestCurrentSystemMessageMatchesTheProjection pins the accessor that replaced
// GetCurrentSystemMessage(BuildSessionContext().Messages) at its call sites.
func TestCurrentSystemMessageMatchesTheProjection(t *testing.T) {
	m := newTestSessionForProjection(t)
	if got := m.CurrentSystemMessage(); got != nil {
		t.Fatalf("system message = %+v before one was appended", got)
	}
	m.AppendMessage(&ai.SystemMessage{Content: ai.StringOrBlocks{Text: "prompt one"}, Timestamp: 1})
	m.AppendMessage(createUserMessage("hi"))
	m.AppendMessage(&ai.SystemMessage{Content: ai.StringOrBlocks{Text: "prompt two"}, Timestamp: 2})

	got := m.CurrentSystemMessage()
	reference := ai.GetCurrentSystemMessage(m.Projection().Messages)
	if got == nil || reference == nil {
		t.Fatalf("system message = %+v, projection = %+v", got, reference)
	}
	if got.Content.Text != reference.Content.Text {
		t.Fatalf("accessor %q disagrees with the projection %q", got.Content.Text, reference.Content.Text)
	}
}
