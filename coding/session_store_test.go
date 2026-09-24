package coding

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"
)

// TestCustomEntryDataField guards D129: the custom entry payload serializes as
// `data` (upstream CustomEntry.data), not `details`.
func TestCustomEntryDataField(t *testing.T) {
	manager := NewSessionManager("/tmp/proj", &SessionManagerOptions{Persist: boolPtr(false)})
	payload := json.RawMessage(`{"hello":"world"}`)
	manager.AppendCustomEntry("note", payload)

	entries := manager.GetEntries()
	if len(entries) != 1 {
		t.Fatalf("entries = %d", len(entries))
	}
	entry := entries[0]
	if string(entry.Data) != string(payload) {
		t.Fatalf("data = %s", entry.Data)
	}
	if len(entry.Details) != 0 {
		t.Fatalf("details should be empty: %s", entry.Details)
	}

	// The serialized line uses `data` and round-trips.
	line, err := MarshalFileEntry(FileEntry{Entry: &entry})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(line, `"data":{"hello":"world"}`) || strings.Contains(line, `"details"`) {
		t.Fatalf("line = %s", line)
	}
	parsed, err := UnmarshalFileEntry(line)
	if err != nil || parsed.Entry == nil {
		t.Fatalf("parse: %v", err)
	}
	if string(parsed.Entry.Data) != string(payload) {
		t.Fatalf("parsed data = %s", parsed.Entry.Data)
	}
}

// entryIDPattern is upstream's short-id shape (generateId slices a random UUID
// to eight hex characters).
var entryIDPattern = regexp.MustCompile(`^[0-9a-f]{8}$`)

// TestEntryIDsAreShortAndRandom pins that shape. generateID sliced a UUIDv7,
// whose leading characters are the millisecond timestamp, so every id generated
// in the same ~4.3 s window matched, the retry loop burned 100 UUIDs, and the
// fallback handed out full-length ids: port sessions are full of 32-hex entry
// ids where upstream has 8.
func TestEntryIDsAreShortAndRandom(t *testing.T) {
	m := newTestSessionForProjection(t)
	for i := 0; i < 8; i++ {
		m.AppendMessage(createUserMessage("a message that gets an entry id"))
	}

	seen := map[string]bool{}
	for _, entry := range m.GetEntries() {
		if !entryIDPattern.MatchString(entry.ID) {
			t.Fatalf("entry %q has id %q; want eight hex characters", entry.Type, entry.ID)
		}
		if seen[entry.ID] {
			t.Fatalf("duplicate entry id %q", entry.ID)
		}
		seen[entry.ID] = true
	}
}
