package coding

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dat267/gpi/ai"
)

// Session manager tests keyed to upstream session-manager.ts semantics.

func newTestSession(t *testing.T) (*SessionManager, string) {
	t.Helper()
	dir := t.TempDir()
	m := NewSessionManager(dir, &SessionManagerOptions{SessionDir: filepath.Join(dir, "sessions")})
	return m, dir
}

func TestSessionManagerNewSessionHeader(t *testing.T) {
	m, dir := newTestSession(t)

	header := m.GetHeader()
	if header == nil || header.Version == nil || *header.Version != CurrentSessionVersion {
		t.Fatalf("header = %+v", header)
	}
	if header.ID != m.GetSessionID() {
		t.Fatal("header id mismatch")
	}
	if header.Cwd != ResolvePath(dir, "", PathInputOptions{}) {
		t.Fatalf("cwd = %q", header.Cwd)
	}
	// The session file is only created once an assistant message arrives.
	if _, err := os.Stat(m.GetSessionFile()); err == nil {
		t.Fatal("file must not exist before the first assistant message")
	}

	m.AppendMessage(createUserMessage("hi"))
	if _, err := os.Stat(m.GetSessionFile()); err == nil {
		t.Fatal("file must still not exist (no assistant message yet)")
	}
	m.AppendMessage(createAssistantMessageT("reply"))
	if _, err := os.Stat(m.GetSessionFile()); err != nil {
		t.Fatal("file must exist after the first assistant message")
	}
	// The file holds ALL entries (backfill on first assistant).
	data, _ := os.ReadFile(m.GetSessionFile())
	lines := strings.Count(string(data), "\n")
	if lines != 3 {
		t.Fatalf("lines = %d; want 3 (header + 2 messages)", lines)
	}
}

func createAssistantMessageT(text string) *ai.AssistantMessage {
	return &ai.AssistantMessage{
		Content: ai.ContentList{ai.TextContent{Text: text}},
		API:     "openai-responses", Provider: "openai", Model: "mock",
		StopReason: ai.StopStop, Timestamp: time.Now().UnixMilli(),
	}
}

func TestSessionManagerTreeAndBranching(t *testing.T) {
	m, _ := newTestSession(t)
	m.AppendMessage(createUserMessage("one"))
	m.AppendMessage(createAssistantMessageT("reply"))
	m.AppendMessage(createUserMessage("two"))

	entries := m.GetEntries()
	if len(entries) != 3 {
		t.Fatalf("entries = %d", len(entries))
	}
	// Chain: entry0.parent = nil; entry1.parent = entry0; entry2.parent = entry1.
	if entries[0].ParentID != nil {
		t.Fatal("root parent must be nil")
	}
	if *entries[1].ParentID != entries[0].ID || *entries[2].ParentID != entries[1].ID {
		t.Fatal("parent chain broken")
	}

	// Branch back to the first entry and append — a sibling branch forms.
	if err := m.Branch(entries[0].ID); err != nil {
		t.Fatal(err)
	}
	m.AppendMessage(createUserMessage("alternative"))
	branch := m.GetBranch("")
	if len(branch) != 2 || branch[0].ID != entries[0].ID {
		t.Fatalf("branch = %d entries", len(branch))
	}

	// The context follows the CURRENT leaf path only.
	context := m.BuildSessionContext()
	if len(context.Messages) != 2 {
		t.Fatalf("context messages = %d; want 2", len(context.Messages))
	}
}

func TestSessionManagerBuildContextWithCompaction(t *testing.T) {
	m, _ := newTestSession(t)
	m.AppendMessage(createUserMessage("old-1"))
	m.AppendMessage(createAssistantMessageT("old-reply"))
	m.AppendMessage(createUserMessage("kept"))
	entries := m.GetEntries()

	m.AppendCompaction("summary of old", entries[2].ID, 1000, nil, false, nil)
	m.AppendMessage(createUserMessage("after compaction"))

	context := m.BuildSessionContext()
	// compaction entry (as summary message) + kept user + after-compaction user.
	roles := messageRoles(context.Messages)
	if len(roles) != 3 {
		t.Fatalf("roles = %v", roles)
	}
	if roles[0] != "compactionSummary" || roles[1] != "user" || roles[2] != "user" {
		t.Fatalf("roles = %v", roles)
	}
	// The kept user message text survives.
	if user, ok := context.Messages[1].(*ai.UserMessage); ok && user.Content.Text != "kept" {
		t.Fatalf("kept = %+v", user)
	}
}

func TestSessionManagerModelAndThinkingLevelChanges(t *testing.T) {
	m, _ := newTestSession(t)
	m.AppendMessage(createUserMessage("hi"))
	m.AppendModelChange("anthropic", "claude-opus-4-5")
	m.AppendThinkingLevelChange("high")

	context := m.BuildSessionContext()
	if context.Model == nil || context.Model.Provider != "anthropic" || context.Model.ModelID != "claude-opus-4-5" {
		t.Fatalf("model = %+v", context.Model)
	}
	if context.ThinkingLevel != "high" {
		t.Fatalf("thinkingLevel = %q", context.ThinkingLevel)
	}
}

func TestSessionManagerPersistAndReload(t *testing.T) {
	dir := t.TempDir()
	m := NewSessionManager(dir, &SessionManagerOptions{SessionDir: filepath.Join(dir, "sessions")})
	m.AppendMessage(createUserMessage("persist me"))
	m.AppendMessage(createAssistantMessageT("persisted"))
	m.AppendSessionInfo("My Session")
	sessionFile := m.GetSessionFile()

	reloaded, err := OpenSession(sessionFile, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.GetSessionID() != m.GetSessionID() {
		t.Fatal("session id mismatch")
	}
	if len(reloaded.GetEntries()) != 3 {
		t.Fatalf("entries = %d", len(reloaded.GetEntries()))
	}
	if reloaded.GetSessionName() != "My Session" {
		t.Fatalf("name = %q", reloaded.GetSessionName())
	}
	// Context equivalence after reload.
	original := m.BuildSessionContext()
	reloadedContext := reloaded.BuildSessionContext()
	if len(original.Messages) != len(reloadedContext.Messages) {
		t.Fatalf("context sizes = %d vs %d", len(original.Messages), len(reloadedContext.Messages))
	}
}

func TestSessionRoundTripByteParity(t *testing.T) {
	// Entry JSON must round-trip through Marshal/Unmarshal identically.
	line := `{"type":"message","id":"abc123","parentId":null,"timestamp":"2026-09-19T03:00:00.000Z","message":{"role":"user","content":"hello","timestamp":1}}`
	entry, err := UnmarshalFileEntry(line)
	if err != nil {
		t.Fatal(err)
	}
	out, err := MarshalFileEntry(*entry)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out) != line {
		t.Fatalf("round trip:\n got %s\nwant %s", out, line)
	}
}

func TestSessionMigrations(t *testing.T) {
	// v1 session: header without version, entries without ids.
	v1 := strings.Join([]string{
		`{"type":"session","id":"v1session","timestamp":"2026-01-01T00:00:00.000Z","cwd":"/tmp"}`,
		`{"type":"message","timestamp":"2026-01-01T00:00:01.000Z","message":{"role":"user","content":"from v1","timestamp":1}}`,
	}, "\n")
	file := filepath.Join(t.TempDir(), "v1.jsonl")
	os.WriteFile(file, []byte(v1), 0o644)

	m, err := OpenSession(file, "", "")
	if err != nil {
		t.Fatal(err)
	}
	header := m.GetHeader()
	if header.Version == nil || *header.Version != CurrentSessionVersion {
		t.Fatalf("version = %v", header.Version)
	}
	// Entries got ids and a parent chain.
	entries := m.GetEntries()
	if len(entries) != 1 || entries[0].ID == "" || entries[0].ParentID != nil {
		t.Fatalf("entries = %+v", entries)
	}
	// The file was rewritten in place.
	data, _ := os.ReadFile(file)
	if !strings.Contains(string(data), `"version":3`) {
		t.Fatalf("file not migrated: %s", data)
	}
}

func TestFindMostRecentSession(t *testing.T) {
	dir := t.TempDir()
	sessions := filepath.Join(dir, "sessions")
	os.MkdirAll(sessions, 0o755)

	writeSession := func(name, cwd string, modTime time.Time) {
		path := filepath.Join(sessions, name)
		header, _ := ai.MarshalJSON(map[string]any{
			"type": "session", "version": 3, "id": name, "timestamp": "2026-09-19T00:00:00.000Z", "cwd": cwd,
		})
		os.WriteFile(path, header, 0o644)
		os.Chtimes(path, modTime, modTime)
	}
	now := time.Now()
	writeSession("old.jsonl", "/other", now.Add(-time.Hour))
	writeSession("new.jsonl", "/project", now)

	if got := FindMostRecentSession(sessions, ""); got == nil || !strings.HasSuffix(*got, "new.jsonl") {
		t.Fatalf("most recent = %v", got)
	}
	// cwd filter excludes the other project.
	if got := FindMostRecentSession(sessions, "/project"); got == nil || !strings.HasSuffix(*got, "new.jsonl") {
		t.Fatalf("cwd filter = %v", got)
	}
	if got := FindMostRecentSession(sessions, "/nothing"); got != nil {
		t.Fatalf("no-match filter = %v", got)
	}
}

func TestListSessions(t *testing.T) {
	dir := t.TempDir()
	sessions := filepath.Join(dir, "sessions")
	os.MkdirAll(sessions, 0o755)

	for i, name := range []string{"a", "b"} {
		path := filepath.Join(sessions, name+".jsonl")
		header, _ := ai.MarshalJSON(map[string]any{
			"type": "session", "version": 3, "id": name,
			"timestamp": "2026-09-19T00:00:00.000Z", "cwd": dir,
		})
		// Message activity time drives 'modified' (upstream buildSessionInfo).
		msg := map[string]any{
			"type": "message", "id": "e" + name, "parentId": nil,
			"timestamp": "2026-09-19T00:00:00.000Z",
			"message":   map[string]any{"role": "user", "content": "hi", "timestamp": 1000 + int64(i)},
		}
		msgLine, _ := ai.MarshalJSON(msg)
		os.WriteFile(path, append(append(header, "\n"...), msgLine...), 0o644)
	}

	list := ListSessions(dir, sessions)
	if len(list) != 2 {
		t.Fatalf("list = %d", len(list))
	}
	// Session b's message activity (timestamp 1001) is newer.
	if list[0].ID != "b" {
		t.Fatalf("newest first: %v (a=%v b=%v)", list[0].ID, list[0].Modified, list[1].Modified)
	}
	if list[0].MessageCount != 1 || list[0].FirstMessage != "hi" {
		t.Fatalf("info = %+v", list[0])
	}
}

func TestSessionInvalidIDRejected(t *testing.T) {
	if err := AssertValidSessionID(""); err == nil {
		t.Fatal("empty id must fail")
	}
	if err := AssertValidSessionID("-leading"); err == nil {
		t.Fatal("leading dash must fail")
	}
	if err := AssertValidSessionID("a b"); err == nil {
		t.Fatal("space must fail")
	}
	if err := AssertValidSessionID("abc-123_x.y"); err != nil {
		t.Fatalf("valid id rejected: %v", err)
	}
}

func createUserMessage(text string) *ai.UserMessage {
	return &ai.UserMessage{Content: ai.StringOrBlocks{Text: text}, Timestamp: time.Now().UnixMilli()}
}

func messageRoles(messages []ai.Message) []string {
	var out []string
	for _, m := range messages {
		out = append(out, ai.RoleOf(m))
	}
	return out
}
