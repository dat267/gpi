package coding

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The session index scanner loads a session by extracting per-line metadata
// and the message member's byte span without a structural JSON decode: message
// entries are 98% of a real session file's bytes and are complete with just
// their base fields plus the raw message. The scanner must be exactly
// equivalent to the full decode for metadata, and must fall back (ok=false) on
// any line whose shape it does not fully understand.

func TestScanSessionLineMatchesFullDecode(t *testing.T) {
	cases := []struct {
		name    string
		line    string
		wantOK  bool
		wantTyp string
		wantID  string
	}{
		{"plain message", `{"type":"message","id":"e1","parentId":null,"timestamp":"t","message":{"role":"user","content":"hi"}}`, true, "message", "e1"},
		{"null parent", `{"type":"message","id":"e2","parentId":null,"timestamp":"t","message":{}}`, true, "message", "e2"},
		{"string parent", `{"type":"message","id":"e3","parentId":"e2","timestamp":"t","message":{}}`, true, "message", "e3"},
		{"nested id keys", `{"type":"message","id":"e4","parentId":null,"timestamp":"t","message":{"role":"assistant","content":[{"type":"tool_call","id":"nested-id"}]}}`, true, "message", "e4"},
		{"member order shuffled", `{"message":{"role":"user"},"timestamp":"t","id":"e5","parentId":null,"type":"message"}`, true, "message", "e5"},
		{"duplicate keys rejected like the decoder", `{"type":"message","type":"compaction","id":"e6","parentId":null,"timestamp":"t","message":{}}`, false, "", ""},
		{"empty object", `{"type":"message","id":"e7","parentId":null,"timestamp":"t","message":{}}`, true, "message", "e7"},
		{"escaped string falls back", `{"type":"message","id":"e\t8","parentId":null,"timestamp":"t","message":{}}`, false, "", ""},
		{"extra member falls back", `{"type":"message","id":"e9","parentId":null,"timestamp":"t","message":{},"agent":"sub"}`, false, "", ""},
		{"escaped key falls back", `{"type":"message","i\\u0064":"e10","parentId":null,"timestamp":"t","message":{}}`, false, "", ""},
		{"not an object", `["type"]`, false, "", ""},
		{"truncated", `{"type":"message","id":"e11"`, false, "", ""},
		{"trailing whitespace", "  {\"type\":\"message\",\"id\":\"e12\",\"parentId\":null,\"timestamp\":\"t\",\"message\":{}}  ", true, "message", "e12"},
	}
	for _, tc := range cases {
		meta, ok := scanSessionLine([]byte(tc.line))
		if ok != tc.wantOK {
			t.Errorf("%s: scan ok=%v, want %v", tc.name, ok, tc.wantOK)
			continue
		}
		if !tc.wantOK {
			continue
		}
		if meta.typ != tc.wantTyp || meta.id != tc.wantID {
			t.Errorf("%s: meta type=%q id=%q, want %q %q", tc.name, meta.typ, meta.id, tc.wantTyp, tc.wantID)
		}
		// The scanned metadata must agree with a full decode of the same line.
		entry, err := UnmarshalFileEntry(tc.line)
		if err != nil {
			t.Fatalf("%s: full decode failed: %v", tc.name, err)
		}
		decoded := entry.Entry
		if decoded == nil {
			t.Fatalf("%s: full decode produced no entry", tc.name)
		}
		if decoded.ID != meta.id || decoded.Type != meta.typ || decoded.Timestamp != meta.timestamp {
			t.Errorf("%s: metadata disagrees with full decode", tc.name)
		}
		if (decoded.ParentID == nil) != (meta.parentID == nil) ||
			(meta.parentID != nil && *decoded.ParentID != *meta.parentID) {
			t.Errorf("%s: parentId disagrees with full decode", tc.name)
		}
		if tc.wantTyp == "message" && string(decoded.Message) != string(meta.message) {
			t.Errorf("%s: message span disagrees with full decode", tc.name)
		}
	}
}

func TestScanSessionIndexEquivalence(t *testing.T) {
	lines := []string{
		`{"type":"session","version":3,"id":"s1","timestamp":"t0","cwd":"/tmp"}`,
		`{"type":"message","id":"m1","parentId":null,"timestamp":"t1","message":{"role":"user","content":"hello"}}`,
		`{"type":"message","id":"m2","parentId":"m1","timestamp":"t2","message":{"role":"assistant","content":[{"type":"text","text":"**bold** and ` + "`" + `code` + "`" + `"}]}}`,
		`{"type":"model_change","id":"mc1","parentId":"m2","timestamp":"t3","provider":"anthropic","modelId":"claude"}`,
		`{"type":"message","id":"m3","parentId":"mc1","timestamp":"t4","message":{"role":"user","content":"next"}}`,
		`{"type":"compaction","id":"c1","parentId":"m3","timestamp":"t5","summary":"summary","firstKeptEntryId":"m3"}`,
		`{"type":"custom","id":"cu1","parentId":"c1","timestamp":"t6","customType":"x","data":{"k":1}}`,
		`{"type":"message","id":"m4","parentId":"c1","timestamp":"t7","message":{"role":"assistant","content":"done"}}`,
		`not json at all`,
		`{"type":"message","id":"m5","parentId":"m4","timestamp":"t8","message":{"role":"user","content":"tail"},"agent":"x"}`,
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	eager, err := LoadEntriesFromFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lazy, data, fast, err := LoadEntriesFromFileBuffered(path)
	if err != nil {
		t.Fatal(err)
	}
	if !fast {
		t.Fatalf("current-version file should take the scanner fast path")
	}
	if len(eager) != len(lazy) {
		t.Fatalf("entry count differs: eager=%d lazy=%d", len(eager), len(lazy))
	}
	for i := range eager {
		e, l := eager[i], lazy[i]
		if (e.Header == nil) != (l.Header == nil) {
			t.Fatalf("line %d: header presence differs", i)
		}
		if e.Header != nil && e.Header.ID != l.Header.ID {
			t.Fatalf("line %d: header id differs", i)
		}
		if e.Entry == nil || l.Entry == nil {
			if e.Entry == nil && l.Entry == nil {
				continue // header line, compared above
			}
			t.Fatalf("line %d: missing entry", i)
		}
		if e.Entry.ID != l.Entry.ID || e.Entry.Type != l.Entry.Type || e.Entry.Timestamp != l.Entry.Timestamp {
			t.Fatalf("line %d: metadata differs", i)
		}
		if (e.Entry.ParentID == nil) != (l.Entry.ParentID == nil) ||
			(l.Entry.ParentID != nil && *e.Entry.ParentID != *l.Entry.ParentID) {
			t.Fatalf("line %d: parent differs", i)
		}
		if string(e.Entry.Message) != string(l.Entry.Message) {
			t.Fatalf("line %d: message raw differs", i)
		}
		if string(e.Entry.Raw()) != string(l.Entry.Raw()) {
			t.Fatalf("line %d: raw line differs", i)
		}
	}
	// The buffer backs every subslice: it must outlive the entries and the
	// subslices must alias it (otherwise the zero-copy claim is false).
	if data == nil {
		t.Fatal("no buffer returned")
	}
	found := false
	for _, e := range lazy {
		if e.Entry != nil && len(e.Entry.Message) > 0 {
			idx := strings.Index(string(data), string(e.Entry.Message))
			if idx < 0 {
				t.Fatal("message raw is not a subslice of the file buffer")
			}
			found = true
			break
		}
	}
	if !found {
		t.Fatal("no message entry to check")
	}
}

// TestLoadEntriesBufferedAllocatesLikeTheFile bounds the load cost: the eager
// decode allocated ~7x the file size (structural decode of every line plus a
// full-line raw copy each); the buffered scanner keeps the file once and
// decodes only the tiny non-message lines.
func TestLoadEntriesBufferedAllocatesLikeTheFile(t *testing.T) {
	var b strings.Builder
	b.WriteString(`{"type":"session","version":3,"id":"s1","timestamp":"t0","cwd":"/tmp"}` + "\n")
	content := `{"type":"message","id":"m%06d","parentId":null,"timestamp":"t","message":{"role":"user","content":"` +
		strings.Repeat("x", 1800) + `"}}`
	for i := 0; i < 2000; i++ {
		b.WriteString(fmt.Sprintf(content, i))
		b.WriteString("\n")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	entries, data, fast, err := LoadEntriesFromFileBuffered(path)
	runtime.ReadMemStats(&after)
	if err != nil || !fast || len(entries) != 2001 {
		t.Fatalf("load: err=%v fast=%v entries=%d", err, fast, len(entries))
	}
	allocated := after.TotalAlloc - before.TotalAlloc
	fileSize := len(data)
	if allocated > uint64(3*fileSize) {
		t.Fatalf("loading %d KB allocated %d MB, want under 3x the file size (%d MB)",
			fileSize>>10, allocated>>20, (3*fileSize)>>20)
	}
}
