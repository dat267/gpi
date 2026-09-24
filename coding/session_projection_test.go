package coding

import (
	"regexp"
	"runtime"
	"testing"

	"github.com/dat267/pier/ai"
)

// newProjectionTestSession builds a session with enough messages that resolving
// the projection allocates visibly. The size is a compromise: the properties
// under test are ratios (window vs session, warm memo vs cold), so a few
// hundred pairs discriminate as well as a few thousand while the race detector
// — which slows the whole-materialization path ~10x — keeps the package under
// ten seconds. Do not grow it back without re-measuring the package.
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
	for i := 0; i < 400; i++ {
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

	// Every append changes the version and forces one resolution. Asserted as
	// identity rather than as an allocation budget: a cache hit hands back the
	// slice it holds, a fresh resolution builds a new one. (An allocation
	// threshold sat here first and turned into a fixture-size measurement — 99
	// objects against a limit of 100 — which failed about one run in five.)
	before := m.Projection()
	m.AppendMessage(createUserMessage("after the cache was warm"))
	after := m.Projection()
	if len(before.Messages) == 0 || len(after.Messages) == 0 {
		t.Fatal("the projection lost its messages")
	}
	if &after.Messages[0] == &before.Messages[0] {
		t.Fatal("an append was served from the cached projection")
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

// TestProjectionDecodesOnlyTheAppendedEntries is the point of the message
// cache: a projection after an append must decode the appended entries, not the
// whole session. Measured as allocations, with the append itself in both
// samples so only the resolution differs.
func TestProjectionDecodesOnlyTheAppendedEntries(t *testing.T) {
	m := newProjectionTestSession(t)
	_ = m.Projection()
	m.AppendMessage(createUserMessage("after the memo was warm"))

	incremental := testing.AllocsPerRun(5, func() {
		m.AppendMessage(createUserMessage("after the memo was warm"))
		_ = m.Projection()
	})
	// A reset memo forces the full decode of the whole session that the
	// resolution used to pay on every append.
	cold := testing.AllocsPerRun(5, func() {
		m.messages.reset()
		m.AppendMessage(createUserMessage("with a cold memo"))
		_ = m.Projection()
	})
	t.Logf("allocations per append+projection: incremental %.0f, cold %.0f", incremental, cold)
	if cold < incremental*5 {
		t.Fatalf("the memo did not carry the session: incremental %.0f, cold %.0f allocations", incremental, cold)
	}
}

// TestProjectionCacheMatchesTheReference pins the cached resolution against the
// reference walk: the memo must not change what a compaction window keeps,
// including the system messages it drops before firstKeptEntryId.
func TestProjectionCacheMatchesTheReference(t *testing.T) {
	m := newTestSessionForProjection(t)
	m.AppendMessage(&ai.SystemMessage{Content: ai.StringOrBlocks{Text: "prompt one"}, Timestamp: 1})
	for i := 0; i < 50; i++ {
		m.AppendMessage(createUserMessage("old message"))
		m.AppendMessage(createAssistantMessageT("old reply"))
	}
	entries := m.GetEntries()
	m.AppendCompaction("summary of the old history", entries[len(entries)-4].ID, 1000, nil, false, nil)
	m.AppendMessage(&ai.SystemMessage{Content: ai.StringOrBlocks{Text: "prompt two"}, Timestamp: 2})
	for i := 0; i < 5; i++ {
		m.AppendMessage(createUserMessage("new message"))
	}

	cached := m.Projection()
	reference := BuildSessionContext(m.GetEntries(), m.GetLeafID(), m.byID)
	if len(cached.Messages) != len(reference.Messages) {
		t.Fatalf("cached %d messages, reference %d", len(cached.Messages), len(reference.Messages))
	}
	for i := range cached.Messages {
		cachedJSON, err := ai.MarshalMessage(cached.Messages[i])
		if err != nil {
			t.Fatal(err)
		}
		referenceJSON, err := ai.MarshalMessage(reference.Messages[i])
		if err != nil {
			t.Fatal(err)
		}
		if string(normalizeProjectedTimestamp(cachedJSON)) != string(normalizeProjectedTimestamp(referenceJSON)) {
			t.Fatalf("message %d: cached %s, reference %s", i, cachedJSON, referenceJSON)
		}
	}
	for i := range cached.Entries {
		if cached.Entries[i].ID != reference.Entries[i].ID {
			t.Fatalf("entry %d: cached %s, reference %s", i, cached.Entries[i].ID, reference.Entries[i].ID)
		}
	}

	// The entry list the transcript renders goes through the same cache.
	cachedEntries := m.BuildContextEntriesForLeaf()
	referenceEntries := BuildContextEntries(m.GetEntries(), m.GetLeafID(), m.byID)
	if len(cachedEntries) != len(referenceEntries) {
		t.Fatalf("cached %d entries, reference %d", len(cachedEntries), len(referenceEntries))
	}
	for i := range cachedEntries {
		if cachedEntries[i].ID != referenceEntries[i].ID {
			t.Fatalf("entry %d: cached %s, reference %s", i, cachedEntries[i].ID, referenceEntries[i].ID)
		}
	}
}

// normalizeProjectedTimestamp blanks a projected message's timestamp for
// comparison. The port stamps the synthetic context messages (compaction,
// branch summary, custom) at projection time, where upstream passes the entry's
// own timestamp, so two resolutions a millisecond apart differ. Message entries
// carry their stored timestamp and are compared as they are. The divergence is
// documented in session_messagecache.go.
func normalizeProjectedTimestamp(raw []byte) []byte {
	return projectedTimestampPattern.ReplaceAll(raw, []byte(`"timestamp":0`))
}

// projectedTimestampPattern matches a timestamp value at any depth: the
// synthetic messages carry one at the top level and inside "content".
var projectedTimestampPattern = regexp.MustCompile(`"timestamp":(?:"[^"]*"|[0-9]+)`)

// TestCompactedProjectionCostsTheWindowNotTheSession guards the pointer-path
// resolution: resolving a compacted session's projection must copy what the
// window keeps, not the whole entry tree. The old resolution copied every entry
// and rebuilt an id map, which measured ~800 KB of allocation on a 2000-message
// session; the window here is eight entries (the compaction, the six it kept, and the message after it).
func TestCompactedProjectionCostsTheWindowNotTheSession(t *testing.T) {
	m := newTestSessionForProjection(t)
	for i := 0; i < 500; i++ {
		m.AppendMessage(createUserMessage("a message with enough text that copying it is visible in the measurement"))
		m.AppendMessage(createAssistantMessageT("a reply with enough text that copying it is visible too"))
	}
	entries := m.GetEntries()
	firstKept := entries[len(entries)-6].ID
	m.AppendCompaction("summary of the compacted-away history", firstKept, 100000, nil, false, nil)
	m.AppendMessage(createUserMessage("after the compaction"))

	_ = m.Projection()
	m.mu.Lock()
	m.projection.valid = false
	m.mu.Unlock()

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	context := m.Projection()
	runtime.ReadMemStats(&after)
	allocated := after.TotalAlloc - before.TotalAlloc
	t.Logf("projection of a %d-entry compacted session allocated %d bytes and kept %d entries",
		len(entries)+1, allocated, len(context.Entries))
	if len(context.Entries) != 8 {
		t.Fatalf("window kept %d entries; want 8", len(context.Entries))
	}
	if allocated > 200_000 {
		t.Fatalf("the projection copied the session (%d bytes for an 8-entry window)", allocated)
	}
}
