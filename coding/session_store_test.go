package coding

import (
	"encoding/json"
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
