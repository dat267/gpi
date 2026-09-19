package coding

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Edit tool tests keyed to upstream's match semantics (the tool description
// itself is the spec: unique, non-overlapping, matched against the original).

func writeTestFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	os.WriteFile(path, []byte(content), 0o644)
	return path
}

func TestEditSingleReplacement(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, dir, "f.txt", "alpha\nbeta\ngamma\n")

	tool := CreateEditTool(dir)
	result, err := tool.Execute("c", json.RawMessage(`{"path":"f.txt","edits":[{"oldText":"beta","newText":"BETA"}]}`), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "f.txt"))
	if string(data) != "alpha\nBETA\ngamma\n" {
		t.Fatalf("file = %q", data)
	}
	var details EditToolDetails
	json.Unmarshal(result.Details, &details)
	// The diff format is +/- <lineNum> <line>.
	if !strings.Contains(details.Diff, "-2 beta") || !strings.Contains(details.Diff, "+2 BETA") {
		t.Fatalf("diff = %q", details.Diff)
	}
	if details.FirstChangedLine == nil || *details.FirstChangedLine != 2 {
		t.Fatalf("firstChangedLine = %v", details.FirstChangedLine)
	}
	if !strings.HasPrefix(details.Patch, "====") || !strings.Contains(details.Patch, "@@ -1,3 +1,3 @@") {
		t.Fatalf("patch = %q", details.Patch)
	}
}

func TestEditMatchedAgainstOriginalNotIncremental(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, dir, "f.txt", "a\nb\nc\n")

	// Two edits both matched against the original; disjoint order-independent.
	tool := CreateEditTool(dir)
	_, err := tool.Execute("c", json.RawMessage(`{"path":"f.txt","edits":[
		{"oldText":"c","newText":"C"},
		{"oldText":"a","newText":"A"}]}`), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "f.txt"))
	if string(data) != "A\nb\nC\n" {
		t.Fatalf("file = %q", data)
	}
}

func TestEditUniqueRequirement(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, dir, "f.txt", "dup\ndup\nunique\n")

	tool := CreateEditTool(dir)
	_, err := tool.Execute("c", json.RawMessage(`{"path":"f.txt","edits":[{"oldText":"dup","newText":"x"}]}`), nil, nil)
	if err == nil || !strings.Contains(err.Error(), "must be unique") || !strings.Contains(err.Error(), "Found 2 occurrences") {
		t.Fatalf("err = %v", err)
	}
}

func TestEditOverlapDetection(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, dir, "f.txt", "hello world\n")

	tool := CreateEditTool(dir)
	_, err := tool.Execute("c", json.RawMessage(`{"path":"f.txt","edits":[
		{"oldText":"hello wo","newText":"1"},
		{"oldText":"lo wor","newText":"2"}]}`), nil, nil)
	if err == nil || !strings.Contains(err.Error(), "overlap in f.txt") {
		t.Fatalf("err = %v", err)
	}
}

func TestEditNotFound(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, dir, "f.txt", "content\n")

	tool := CreateEditTool(dir)
	_, err := tool.Execute("c", json.RawMessage(`{"path":"f.txt","edits":[{"oldText":"missing","newText":"x"}]}`), nil, nil)
	if err == nil || !strings.Contains(err.Error(), "must match exactly including all whitespace") {
		t.Fatalf("err = %v", err)
	}

	// Multi-edit error names the index.
	_, err = tool.Execute("c", json.RawMessage(`{"path":"f.txt","edits":[
		{"oldText":"content","newText":"1"},
		{"oldText":"nope","newText":"2"}]}`), nil, nil)
	if err == nil || !strings.Contains(err.Error(), "edits[1]") {
		t.Fatalf("err = %v", err)
	}
}

func TestEditNoChangeError(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, dir, "f.txt", "same\n")

	tool := CreateEditTool(dir)
	_, err := tool.Execute("c", json.RawMessage(`{"path":"f.txt","edits":[{"oldText":"same","newText":"same"}]}`), nil, nil)
	if err == nil || !strings.Contains(err.Error(), "produced identical content") {
		t.Fatalf("err = %v", err)
	}
}

func TestEditEmptyOldText(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, dir, "f.txt", "x\n")

	tool := CreateEditTool(dir)
	_, err := tool.Execute("c", json.RawMessage(`{"path":"f.txt","edits":[{"oldText":"","newText":"y"}]}`), nil, nil)
	if err == nil || !strings.Contains(err.Error(), "oldText must not be empty") {
		t.Fatalf("err = %v", err)
	}
}

func TestEditFuzzyMatching(t *testing.T) {
	dir := t.TempDir()
	// Trailing whitespace + smart quotes in the FILE; clean oldText.
	writeTestFile(t, dir, "f.txt", "say \u201Chello\u201D   \nnext\n")

	tool := CreateEditTool(dir)
	// oldText uses plain quotes and no trailing spaces — fuzzy match must find it.
	_, err := tool.Execute("c", json.RawMessage(`{"path":"f.txt","edits":[{"oldText":"say \"hello\"","newText":"said hi"}]}`), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "f.txt"))
	// The fuzzy path preserves unchanged lines byte-for-byte; the touched
	// line is rewritten from the normalized base.
	if !strings.Contains(string(data), "said hi") || !strings.Contains(string(data), "next\n") {
		t.Fatalf("file = %q", data)
	}
}

func TestEditCRLFPreserved(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, dir, "f.txt", "alpha\r\nbeta\r\n")

	tool := CreateEditTool(dir)
	_, err := tool.Execute("c", json.RawMessage(`{"path":"f.txt","edits":[{"oldText":"beta","newText":"B"}]}`), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "f.txt"))
	if string(data) != "alpha\r\nB\r\n" {
		t.Fatalf("file = %q", data)
	}
}

func TestEditBOMPreserved(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, dir, "f.txt", "\uFEFFalpha\n")

	tool := CreateEditTool(dir)
	_, err := tool.Execute("c", json.RawMessage(`{"path":"f.txt","edits":[{"oldText":"alpha","newText":"A"}]}`), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "f.txt"))
	if string(data) != "\uFEFFA\n" {
		t.Fatalf("file = %q", data)
	}
}

func TestPrepareEditArgumentsShims(t *testing.T) {
	// JSON-string edits.
	got := PrepareEditArguments(json.RawMessage(`{"path":"p","edits":"[{\"oldText\":\"a\",\"newText\":\"b\"}]"}`))
	var decoded struct {
		Edits []Edit `json:"edits"`
	}
	json.Unmarshal(got, &decoded)
	if len(decoded.Edits) != 1 || decoded.Edits[0].OldText != "a" {
		t.Fatalf("string edits = %s", got)
	}

	// Single edit object.
	got = PrepareEditArguments(json.RawMessage(`{"path":"p","edits":{"oldText":"a","newText":"b"}}`))
	json.Unmarshal(got, &decoded)
	if len(decoded.Edits) != 1 || decoded.Edits[0].NewText != "b" {
		t.Fatalf("single object = %s", got)
	}

	// Legacy flat shape.
	got = PrepareEditArguments(json.RawMessage(`{"path":"p","oldText":"a","newText":"b"}`))
	json.Unmarshal(got, &decoded)
	if len(decoded.Edits) != 1 || decoded.Edits[0].OldText != "a" {
		t.Fatalf("legacy = %s", got)
	}
}
