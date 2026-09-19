package durable

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// durable tests keyed to upstream (types.ts, memory-storage.ts): commit
// sequences, id ownership, pagination cursors, task status indexing, input
// deduplication, and fork-aware history scans.

func idPtr(id Id) *Id         { return &id }
func strPtr(s string) *string { return &s }

func mustCommit(t *testing.T, storage *MemoryStorage, writes ...StorageWrite) Seq {
	t.Helper()
	seq, err := storage.Commit(context.Background(), writes)
	if err != nil {
		t.Fatal(err)
	}
	return seq
}

func conversationWrite(id Id) StorageWrite {
	return StorageWrite{Type: "conversation", Conversation: &ConversationRecord{ID: id}}
}

func entryWrite(id, conversationID Id, head *Id) StorageWrite {
	return StorageWrite{Type: "entry", Entry: &EntryRecord{ID: id, ConversationID: conversationID, Kind: "message", Head: head}}
}

func TestCommitSequencesAndMintedIDs(t *testing.T) {
	storage := NewMemoryStorage()
	// The root conversation reserves id 1, so minting starts at 2.
	first, err := storage.MintID(context.Background())
	if err != nil || first != 2 {
		t.Fatalf("first mint = %d, %v", first, err)
	}
	second, _ := storage.MintID(context.Background())
	if second != 3 {
		t.Fatalf("second mint = %d", second)
	}

	// Commit sequences start at 1 and increase.
	if seq := mustCommit(t, storage, conversationWrite(first)); seq != 1 {
		t.Fatalf("seq = %d", seq)
	}
	if seq := mustCommit(t, storage); seq != 2 {
		t.Fatalf("seq = %d", seq)
	}

	// A commit with explicit ids advances the minted namespace past them.
	mustCommit(t, storage, conversationWrite(20))
	next, _ := storage.MintID(context.Background())
	if next != 21 {
		t.Fatalf("mint after explicit id = %d; want 21", next)
	}
}

func TestImmutableIDOwnership(t *testing.T) {
	storage := NewMemoryStorage()
	mustCommit(t, storage, conversationWrite(2))

	// Re-creating a conversation id fails.
	if _, err := storage.Commit(context.Background(), []StorageWrite{conversationWrite(2)}); err == nil ||
		!strings.Contains(err.Error(), "already belongs to conversation") {
		t.Fatalf("err = %v", err)
	}
	// An entry id colliding with a conversation fails.
	if _, err := storage.Commit(context.Background(), []StorageWrite{
		entryWrite(2, 2, nil),
	}); err == nil || !strings.Contains(err.Error(), "already belongs to conversation") {
		t.Fatalf("err = %v", err)
	}
	// The same id written twice in one batch as conversations fails.
	if _, err := storage.Commit(context.Background(), []StorageWrite{conversationWrite(5), conversationWrite(5)}); err == nil ||
		!strings.Contains(err.Error(), "written more than once") {
		t.Fatalf("err = %v", err)
	}
	// Two record types for one id in one batch fails.
	if _, err := storage.Commit(context.Background(), []StorageWrite{
		{Type: "task", Task: &TaskRecord{ID: 7, State: TaskState{Status: TaskPending}}},
		{Type: "input", Input: &Input{ID: 7, Status: InputQueued}},
	}); err == nil || !strings.Contains(err.Error(), "two record types") {
		t.Fatalf("err = %v", err)
	}
	// Task and input records may be REPLACED (upsert), unlike conversations
	// and entries.
	mustCommit(t, storage,
		StorageWrite{Type: "task", Task: &TaskRecord{ID: 9, ConversationID: 2, Kind: "t", State: TaskState{Status: TaskPending}}})
	mustCommit(t, storage,
		StorageWrite{Type: "task", Task: &TaskRecord{ID: 9, ConversationID: 2, Kind: "t", State: TaskState{Status: TaskRunning}}})
	task, err := storage.Task(context.Background(), 9)
	if err != nil || task.State.Status != TaskRunning {
		t.Fatalf("task = %+v, %v", task, err)
	}
}

func TestScanConversationsPagination(t *testing.T) {
	storage := NewMemoryStorage()
	for _, id := range []Id{2, 4, 6, 8} {
		mustCommit(t, storage, conversationWrite(id))
	}
	first, err := storage.ScanConversations(context.Background(), nil, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Items) != 2 || first.Items[0].ID != 2 || first.Items[1].ID != 4 {
		t.Fatalf("first page = %+v", first.Items)
	}
	if first.Next == nil {
		t.Fatal("expected a continuation cursor")
	}
	second, err := storage.ScanConversations(context.Background(), first.Next, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Items) != 2 || second.Items[0].ID != 6 || second.Items[1].ID != 8 {
		t.Fatalf("second page = %+v", second.Items)
	}
	// The final page has no cursor (exactly `limit` values remain).
	if second.Next != nil {
		t.Fatalf("unexpected cursor: %v", second.Next)
	}
	// A short page also has no cursor.
	third, _ := storage.ScanConversations(context.Background(), nil, 10)
	if len(third.Items) != 4 || third.Next != nil {
		t.Fatalf("full scan = %+v", third)
	}
}

func TestScanEntriesAndHeadMarkers(t *testing.T) {
	storage := NewMemoryStorage()
	mustCommit(t, storage, conversationWrite(2))
	// Entries 10..13; entries 10 and 12 are head markers.
	mustCommit(t, storage,
		entryWrite(10, 2, idPtr(10)),
		entryWrite(11, 2, nil),
		entryWrite(12, 2, idPtr(10)),
		entryWrite(13, 2, nil),
	)
	// The entry lookup reports the persisting commit.
	entry, err := storage.Entry(context.Background(), 11)
	if err != nil || entry == nil || entry.CommitSeq != 2 {
		t.Fatalf("entry = %+v, %v", entry, err)
	}
	if missing, _ := storage.Entry(context.Background(), 99); missing != nil {
		t.Fatal("missing entry must be nil")
	}

	// Entries scan newest-first with the inclusive bounds.
	page, err := storage.ScanEntries(context.Background(), EntryQuery{ConversationID: 2}, nil, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 2 || page.Items[0].ID != 13 || page.Items[1].ID != 12 {
		t.Fatalf("page = %+v", page.Items)
	}
	if page.Next == nil {
		t.Fatal("expected a cursor")
	}
	minEntry := Id(11)
	bounded, err := storage.ScanEntries(context.Background(), EntryQuery{
		ConversationID: 2, MinEntryID: &minEntry,
	}, nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range bounded.Items {
		if item.ID < 11 {
			t.Fatalf("entry below the minimum: %+v", item)
		}
	}

	// The head marker is the newest marker at or below the cutoff.
	marker, err := storage.FindLatestHeadMarker(context.Background(), 2, nil)
	if err != nil || marker == nil || marker.ID != 12 {
		t.Fatalf("marker = %+v, %v", marker, err)
	}
	cutoff := Id(11)
	marker, err = storage.FindLatestHeadMarker(context.Background(), 2, &cutoff)
	if err != nil || marker == nil || marker.ID != 10 {
		t.Fatalf("marker at cutoff = %+v, %v", marker, err)
	}
	// An unknown conversation fails loudly.
	if _, err := storage.FindLatestHeadMarker(context.Background(), 99, nil); err == nil {
		t.Fatal("unknown conversation must fail")
	}
}

func TestForkAwareHistoryScans(t *testing.T) {
	storage := NewMemoryStorage()
	parent := &ConversationRecord{ID: 2}
	// Conversation 3 forks from conversation 2 at entry 11 (inclusive).
	fork := &ConversationRecord{ID: 3, Parent: &ConversationParent{ConversationID: 2, At: 11}}
	mustCommit(t, storage,
		StorageWrite{Type: "conversation", Conversation: parent},
		StorageWrite{Type: "conversation", Conversation: fork},
	)
	mustCommit(t, storage,
		entryWrite(10, 2, idPtr(10)),
		entryWrite(11, 2, nil),
		entryWrite(12, 2, nil),
		entryWrite(13, 3, idPtr(10)),
	)

	// The fork sees its own entry plus the parent's history through the fork
	// point (entry 11 and older), newest-first.
	page, err := storage.ScanEntries(context.Background(), EntryQuery{ConversationID: 3}, nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	var ids []Id
	for _, item := range page.Items {
		ids = append(ids, item.ID)
	}
	want := []Id{13, 11, 10}
	if len(ids) != len(want) {
		t.Fatalf("fork history = %v; want %v", ids, want)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("fork history = %v; want %v", ids, want)
		}
	}

	// A head marker in the fork falls back to the parent's marker.
	marker, err := storage.FindLatestHeadMarker(context.Background(), 3, nil)
	if err != nil || marker == nil || marker.ID != 13 {
		t.Fatalf("marker = %+v, %v", marker, err)
	}
	marker, err = storage.FindLatestHeadMarker(context.Background(), 3, idPtr(12))
	if err != nil || marker == nil || marker.ID != 10 {
		t.Fatalf("marker through the parent = %+v, %v", marker, err)
	}
}

func TestTaskScanningAndStatusIndex(t *testing.T) {
	storage := NewMemoryStorage()
	writeTask := func(id Id, status, kind string, background, abort bool) StorageWrite {
		return StorageWrite{Type: "task", Task: &TaskRecord{
			ID: id, ConversationID: 2, Kind: kind, Background: background, AbortRequested: abort,
			State: TaskState{Status: status},
		}}
	}
	mustCommit(t, storage,
		writeTask(2, TaskPending, "a", false, false),
		writeTask(3, TaskRunning, "b", true, true),
		writeTask(4, TaskTerminal, "a", false, false),
	)
	// Status filter.
	running := TaskRunning
	page, err := storage.ScanTasks(context.Background(), TaskQuery{Status: &running}, nil, 10)
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != 3 {
		t.Fatalf("running = %+v, %v", page.Items, err)
	}
	// Kind filter.
	kind := "a"
	byKind, err := storage.ScanTasks(context.Background(), TaskQuery{Kind: &kind}, nil, 10)
	if err != nil || len(byKind.Items) != 2 {
		t.Fatalf("byKind = %+v, %v", byKind.Items, err)
	}
	// Combined filters.
	notBackground := false
	combined, err := storage.ScanTasks(context.Background(), TaskQuery{Kind: &kind, Background: &notBackground}, nil, 10)
	if err != nil || len(combined.Items) != 2 {
		t.Fatalf("combined = %+v, %v", combined.Items, err)
	}
	// A status change moves the id between status indexes.
	mustCommit(t, storage, writeTask(2, TaskTerminal, "a", false, false))
	terminal := TaskTerminal
	page, _ = storage.ScanTasks(context.Background(), TaskQuery{Status: &terminal}, nil, 10)
	if len(page.Items) != 2 || page.Items[0].ID != 2 || page.Items[1].ID != 4 {
		t.Fatalf("terminal = %+v", page.Items)
	}
	pending := TaskPending
	page, _ = storage.ScanTasks(context.Background(), TaskQuery{Status: &pending}, nil, 10)
	if len(page.Items) != 0 {
		t.Fatalf("pending = %+v", page.Items)
	}
	// Pagination over filtered tasks.
	first, _ := storage.ScanTasks(context.Background(), TaskQuery{}, nil, 2)
	if len(first.Items) != 2 || first.Next == nil {
		t.Fatalf("first = %+v", first)
	}
	second, err := storage.ScanTasks(context.Background(), TaskQuery{}, first.Next, 2)
	if err != nil || len(second.Items) != 1 {
		t.Fatalf("second = %+v, %v", second.Items, err)
	}
}

func TestInputLifecycleAndDeduplication(t *testing.T) {
	storage := NewMemoryStorage()
	mustCommit(t, storage, StorageWrite{Type: "input", Input: &Input{
		ID: 2, ConversationID: 10, RequestID: strPtr("req-1"), Status: InputQueued,
	}})
	// The request key dedupes within the conversation.
	found, err := storage.InputByRequest(context.Background(), 10, "req-1")
	if err != nil || found == nil || found.ID != 2 || found.Status != InputQueued {
		t.Fatalf("found = %+v, %v", found, err)
	}
	if missing, _ := storage.InputByRequest(context.Background(), 10, "nope"); missing != nil {
		t.Fatal("unknown request must be nil")
	}
	if missing, _ := storage.InputByRequest(context.Background(), 11, "req-1"); missing != nil {
		t.Fatal("request keys are conversation-scoped")
	}

	// Advancing the lifecycle replaces the whole record.
	mustCommit(t, storage, StorageWrite{Type: "input", Input: &Input{
		ID: 2, ConversationID: 10, RequestID: strPtr("req-1"), Status: InputPlaced, Entry: idPtr(20),
	}})
	found, _ = storage.Input(context.Background(), 2)
	if found.Status != InputPlaced || found.Entry == nil || *found.Entry != 20 {
		t.Fatalf("input = %+v", found)
	}

	// A replacement that drops the request key clears the index.
	mustCommit(t, storage, StorageWrite{Type: "input", Input: &Input{
		ID: 2, ConversationID: 10, Status: InputUnanswered, Reason: strPtr("aborted"),
	}})
	if cleared, _ := storage.InputByRequest(context.Background(), 10, "req-1"); cleared != nil {
		t.Fatalf("request index not cleared: %+v", cleared)
	}
	found, _ = storage.Input(context.Background(), 2)
	if found.Reason == nil || *found.Reason != "aborted" {
		t.Fatalf("input = %+v", found)
	}
}

func TestDetachedValues(t *testing.T) {
	storage := NewMemoryStorage()
	record := &ConversationRecord{ID: 2}
	mustCommit(t, storage, StorageWrite{Type: "conversation", Conversation: record})
	// Mutating the caller's record after the commit must not change storage.
	record.ID = 99
	stored, _ := storage.Conversation(context.Background(), 2)
	if stored == nil || stored.ID != 2 {
		t.Fatalf("stored = %+v", stored)
	}
	// Mutating a returned record must not corrupt storage either.
	stored.ID = 77
	again, _ := storage.Conversation(context.Background(), 2)
	if again.ID != 2 {
		t.Fatalf("storage aliased a returned value: %+v", again)
	}

	// Entry model messages round-trip through the clone boundary.
	entry := &EntryRecord{ID: 3, ConversationID: 2, Kind: "message",
		Data: json.RawMessage(`{"note":"keep"}`)}
	mustCommit(t, storage, StorageWrite{Type: "entry", Entry: entry})
	got, _ := storage.Entry(context.Background(), 3)
	var data map[string]any
	if err := json.Unmarshal(got.Entry.Data, &data); err != nil || data["note"] != "keep" {
		t.Fatalf("entry data = %s, %v", got.Entry.Data, err)
	}
}

func TestCloseRejectsEveryOperation(t *testing.T) {
	storage := NewMemoryStorage()
	if err := storage.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := storage.MintID(ctx); err == nil {
		t.Fatal("mintId must reject")
	}
	if _, err := storage.Commit(ctx, nil); err == nil {
		t.Fatal("commit must reject")
	}
	if _, err := storage.Conversation(ctx, 1); err == nil {
		t.Fatal("conversation must reject")
	}
	if _, err := storage.ScanConversations(ctx, nil, 1); err == nil {
		t.Fatal("scanConversations must reject")
	}
	if _, err := storage.Entry(ctx, 1); err == nil {
		t.Fatal("entry must reject")
	}
	if _, err := storage.FindLatestHeadMarker(ctx, 1, nil); err == nil {
		t.Fatal("findLatestHeadMarker must reject")
	}
	if _, err := storage.ScanEntries(ctx, EntryQuery{ConversationID: 1}, nil, 1); err == nil {
		t.Fatal("scanEntries must reject")
	}
	if _, err := storage.Task(ctx, 1); err == nil {
		t.Fatal("task must reject")
	}
	if _, err := storage.ScanTasks(ctx, TaskQuery{}, nil, 1); err == nil {
		t.Fatal("scanTasks must reject")
	}
	if _, err := storage.Input(ctx, 1); err == nil {
		t.Fatal("input must reject")
	}
	if _, err := storage.InputByRequest(ctx, 1, "x"); err == nil {
		t.Fatal("inputByRequest must reject")
	}
}

func TestRootConversationID(t *testing.T) {
	if RootConversationID != 1 {
		t.Fatalf("root conversation id = %d", RootConversationID)
	}
	// The root conversation can be created explicitly with id 1.
	storage := NewMemoryStorage()
	mustCommit(t, storage, conversationWrite(RootConversationID))
	root, err := storage.Conversation(context.Background(), RootConversationID)
	if err != nil || root == nil || root.ID != 1 {
		t.Fatalf("root = %+v, %v", root, err)
	}
}
