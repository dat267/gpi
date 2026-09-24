package coding

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The cache-scan seed runs in the background after a lazy load, because it
// parses every message's JSON (~250ms on a 55MB session) and the load path must
// not pay it. Until it installs, statistics are empty and appends queue in
// order; the combined state must match an eager consume exactly.

func seedFixture(t *testing.T) (path string, entries []FileEntry) {
	t.Helper()
	lines := []string{
		`{"type":"session","version":3,"id":"s1","timestamp":"t0","cwd":"/tmp"}`,
		`{"type":"message","id":"m1","parentId":null,"timestamp":"t1","message":{"role":"user","content":"hello"}}`,
		`{"type":"message","id":"m2","parentId":"m1","timestamp":"t2","message":{"role":"assistant","content":[{"type":"text","text":"hi"}],"usage":{"input":100,"output":10,"cacheRead":0,"cacheWrite":0},"provider":"anthropic","model":"claude","timestamp":"t2"}}`,
		`{"type":"message","id":"m3","parentId":"m2","timestamp":"t3","message":{"role":"user","content":"again"}}`,
		`{"type":"message","id":"m4","parentId":"m3","timestamp":"t4","message":{"role":"assistant","content":[{"type":"text","text":"answer"}],"usage":{"input":5000,"output":10,"cacheRead":0,"cacheWrite":0},"provider":"anthropic","model":"claude","timestamp":"t4"}}`,
	}
	dir := t.TempDir()
	path = filepath.Join(dir, "session.jsonl")
	if err := os.WriteFile(path, []byte(strings2Join(lines)), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, line := range lines {
		if entry, err := UnmarshalFileEntry(line); err == nil && entry.Entry != nil {
			entries = append(entries, *entry)
		}
	}
	return path, entries
}

func strings2Join(lines []string) string {
	out := ""
	for _, l := range lines {
		out += l + "\n"
	}
	return out
}

func waitForSeed(t *testing.T, m *SessionManager) {
	t.Helper()
	select {
	case <-m.cacheSeedDone:
	case <-time.After(5 * time.Second):
		t.Fatal("cache seed did not finish")
	}
}

func TestCacheSeedMatchesEagerConsume(t *testing.T) {
	path, _ := seedFixture(t)
	m, err := OpenSession(path, filepath.Dir(path), "/tmp")
	if err != nil {
		t.Fatal(err)
	}
	waitForSeed(t, m)
	lazyTotals := m.ComputeCacheWaste(nil)

	eager := InMemorySession("/tmp", nil, nil)
	_, entries := seedFixture(t)
	eager.loadEntries(entries, nil)
	eagerTotals := eager.ComputeCacheWaste(nil)

	if lazyTotals != eagerTotals {
		t.Fatalf("lazy seed totals differ from eager consume: %+v vs %+v", lazyTotals, eagerTotals)
	}
}

func TestAppendWhileSeedingQueuesInOrder(t *testing.T) {
	path, _ := seedFixture(t)
	m, err := OpenSession(path, filepath.Dir(path), "/tmp")
	if err != nil {
		t.Fatal(err)
	}
	// Append while the seed is in flight; whether this lands before or after
	// the install, the final state must match an eager consume of everything.
	appended := &SessionEntry{
		Type:             "message",
		SessionEntryBase: SessionEntryBase{ID: "m5", Timestamp: "t5"},
		Message:          mustRaw(`{"role":"assistant","content":[{"type":"text","text":"late"}],"usage":{"input":9000,"output":1,"cacheRead":0,"cacheWrite":0},"provider":"anthropic","model":"claude","timestamp":"t5"}`),
	}
	m.mu.Lock()
	m.appendEntry(appended)
	m.mu.Unlock()
	waitForSeed(t, m)

	lazyTotals := m.ComputeCacheWaste(nil)

	eager := InMemorySession("/tmp", nil, nil)
	_, entries := seedFixture(t)
	eager.loadEntries(entries, nil)
	eager.mu.Lock()
	eager.appendEntry(appended)
	eager.mu.Unlock()
	eagerTotals := eager.ComputeCacheWaste(nil)

	if lazyTotals != eagerTotals {
		t.Fatalf("append-during-seed totals differ from eager: %+v vs %+v", lazyTotals, eagerTotals)
	}
}

func mustRaw(s string) json.RawMessage { return json.RawMessage(s) }
