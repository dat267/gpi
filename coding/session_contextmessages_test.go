package coding

import (
	"testing"

	"github.com/dat267/pier/ai"
)

// Upstream's sessionEntryToContextMessages passes `entry.timestamp` to every
// message factory it builds (messages.ts does `new Date(timestamp).getTime()`),
// so a synthetic context message carries the entry's own time instead of the
// moment the transcript happened to be projected.
func TestContextMessagesCarryTheEntryTimestamp(t *testing.T) {
	const stamp = "2026-09-24T05:30:54.488904482Z"
	const stampMS = int64(1790227854488)

	cases := []struct {
		name  string
		entry SessionEntry
	}{
		{"branch summary", SessionEntry{Type: "branch_summary", Summary: "a branch", FromID: "x"}},
		{"compaction", SessionEntry{Type: "compaction", Summary: "a summary", TokensBefore: 10}},
		{"custom message", SessionEntry{Type: "custom_message", CustomType: "note", Content: []byte(`"hello"`)}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			entry := testCase.entry
			entry.Timestamp = stamp
			messages := SessionEntryToContextMessages(&entry)
			if len(messages) != 1 {
				t.Fatalf("messages = %d, want 1", len(messages))
			}
			custom, ok := messages[0].(*ai.CustomMessage)
			if !ok {
				t.Fatalf("message = %T", messages[0])
			}
			if custom.Timestamp != stampMS {
				t.Errorf("timestamp = %d, want %d (the entry's)", custom.Timestamp, stampMS)
			}
		})
	}
}

// A compaction records the system message its summary replaced (upstream
// appendCompaction), and the projection returns it *before* the summary, so a
// compacted session keeps the tool declarations the summary stands in for.
func TestCompactionContextCarriesTheRecordedSystemMessage(t *testing.T) {
	recorded := &ai.SystemMessage{
		Content:   ai.StringOrBlocks{Text: "you are a tool"},
		Timestamp: 42,
	}
	entry := SessionEntry{
		Type: "compaction", Summary: "a summary", TokensBefore: 10,
		Timestamp:         "2026-09-24T05:30:54.488904482Z",
		SystemMessageJSON: mustMarshalJSON(recorded),
	}
	messages := SessionEntryToContextMessages(&entry)
	if len(messages) != 2 {
		t.Fatalf("messages = %d, want the recorded system message and the summary", len(messages))
	}
	system, ok := messages[0].(*ai.SystemMessage)
	if !ok {
		t.Fatalf("first message = %T, want the system message first", messages[0])
	}
	if text := ai.ContentText(system.Content, ""); text != "you are a tool" {
		t.Errorf("system message = %q", text)
	}
	if system.Timestamp != 42 {
		t.Errorf("system timestamp = %d, want the recorded 42", system.Timestamp)
	}
	if _, ok := messages[1].(*ai.CustomMessage); !ok {
		t.Fatalf("second message = %T, want the summary", messages[1])
	}

	// Without a recorded system message there is only the summary, and an entry
	// whose recorded message cannot be read must not lose the summary either.
	bare := entry
	bare.SystemMessageJSON = nil
	if messages := SessionEntryToContextMessages(&bare); len(messages) != 1 {
		t.Errorf("messages = %d, want 1", len(messages))
	}
	broken := entry
	broken.SystemMessageJSON = []byte(`{not json`)
	if messages := SessionEntryToContextMessages(&broken); len(messages) != 1 {
		t.Errorf("messages = %d, want the summary alone", len(messages))
	}
}
