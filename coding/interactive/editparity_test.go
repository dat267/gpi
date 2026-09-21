package interactive

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dat267/pier/coding"
)

// TestEditToolRenderUpstreamParity pins the edit tool block's rendered bytes
// against upstream. The golden was produced by driving upstream's
// ToolExecutionComponent + editRenderers with the same file, edits, result
// and width via node (probe: /tmp/parity/editprobe.mjs, node 26 type
// stripping) and dumping component.render(80) as JSON lines:
//
//	"=== component render(80) ===" section of the probe output.
//
// It covers both reported visuals: the bg-tinted box around the
// "edit <path>" header (upstream draws it too: 2 tinted padding lines +
// header line inside toolSuccessBg) and the diff line formatting
// ("-1 ", "+1 ", " 2 " context normalization).
func TestEditToolRenderUpstreamParity(t *testing.T) {
	dir := "/tmp/pier_parity"
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Skipf("cannot create %s: %v", dir, err)
	}
	file := filepath.Join(dir, "f.txt")
	content := "line 1: keep\nline 2: old value\nline 3: keep\nline 4: old value again\n"
	if err := os.WriteFile(file, []byte(content), 0o644); err != nil {
		t.Skipf("cannot write %s: %v", file, err)
	}

	SetTrueColorSupport(true)
	SetStyleColorsEnabled(true)
	SetCustomThemesDir(t.TempDir())
	SetRegisteredThemes(nil)
	InitTheme("dark", false)

	oldContent := "line 1: keep\nline 2: old value\nline 3: keep\nline 4: old value again\n"
	newContent := "line 1: multi\nline 2: old value\nline 3: multi too\nline 4: old value again\n"
	diffString, _, _ := coding.GenerateDiffString(oldContent, newContent, 3)

	component := NewToolExecutionComponent("edit", "call-1",
		map[string]any{
			"path": file,
			"edits": []any{
				map[string]any{"oldText": "line 1: keep", "newText": "line 1: multi"},
				map[string]any{"oldText": "line 3: keep", "newText": "line 3: multi too"},
			},
		},
		ToolExecutionOptions{}, &editRenderers, nil, dir)

	// Wait for the async preview worker (computeEditsPreview) to publish.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if state, ok := component.rendererState.(*editCallComponent); ok && state.snapshotPreview() != nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	first := 1
	component.UpdateResult(&SortToolResultContent{
		Content: []ToolResultContent{{Type: "text", Text: "Edited " + file}},
		Details: &coding.EditToolDetails{Diff: diffString, Patch: "", FirstChangedLine: &first},
	}, false)

	var got []string
	for _, line := range component.Render(80) {
		b, err := json.Marshal(line)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		got = append(got, string(b))
	}

	want, err := os.ReadFile(filepath.Join("testdata", "edittool_parity_golden.txt"))
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	expected := splitGoldenLines(string(want))
	if len(got) != len(expected) {
		t.Fatalf("line count = %d, want %d\ngot:\n%s", len(got), len(expected), joinQuoted(got))
	}
	for i := range got {
		if got[i] != expected[i] {
			t.Fatalf("line %d:\n got %s\nwant %s", i, got[i], expected[i])
		}
	}
}

func splitGoldenLines(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}

func joinQuoted(lines []string) string {
	out := ""
	for _, l := range lines {
		out += l + "\n"
	}
	return out
}
