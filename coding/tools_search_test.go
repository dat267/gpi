package coding

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dat267/gpi/ai"
)

// find/grep tests against the real fd/rg binaries (the tools' default
// backends). Skipped when the binary is unavailable.

func requireBinary(t *testing.T, name string) {
	t.Helper()
	if _, err := exec.LookPath(name); err != nil {
		t.Skipf("%s not installed", name)
	}
}

func TestFindToolGlob(t *testing.T) {
	requireBinary(t, "fd")
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "src", "deep"), 0o755)
	os.WriteFile(filepath.Join(dir, "src", "a.ts"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(dir, "src", "deep", "b.spec.ts"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(dir, "readme.md"), []byte("x"), 0o644)

	tool := CreateFindTool(dir)
	result := execTool(t, tool, `{"pattern":"*.ts"}`)
	text := result.Content[0].(ai.TextContent).Text
	if !strings.Contains(text, "src/a.ts") || strings.Contains(text, "readme.md") {
		t.Fatalf("find = %q", text)
	}

	// Path-containing glob.
	result = execTool(t, tool, `{"pattern":"src/**/*.spec.ts"}`)
	text = result.Content[0].(ai.TextContent).Text
	if !strings.Contains(text, "src/deep/b.spec.ts") {
		t.Fatalf("find full-path = %q", text)
	}

	// No match.
	result = execTool(t, tool, `{"pattern":"*.nope"}`)
	text = result.Content[0].(ai.TextContent).Text
	if text != "No files found matching pattern" {
		t.Fatalf("text = %q", text)
	}
}

func TestFindToolRespectsGitignore(t *testing.T) {
	requireBinary(t, "fd")
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "ignored"), 0o755)
	os.WriteFile(filepath.Join(dir, "ignored", "x.ts"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(dir, "keep.ts"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("ignored/\n"), 0o644)
	os.MkdirAll(filepath.Join(dir, ".git"), 0o755) // make it a git repo

	tool := CreateFindTool(dir)
	result := execTool(t, tool, `{"pattern":"*.ts"}`)
	text := result.Content[0].(ai.TextContent).Text
	if strings.Contains(text, "ignored") {
		t.Fatalf("gitignored file surfaced: %q", text)
	}
	if !strings.Contains(text, "keep.ts") {
		t.Fatalf("text = %q", text)
	}
}

func TestFindRelativizePosix(t *testing.T) {
	if got := RelativizeFindResultPath("/base/sub/f.txt", "/base"); got != "sub/f.txt" {
		t.Fatalf("relativize = %q", got)
	}
	if got := RelativizeFindResultPath("already/relative.txt", "/base"); got != "already/relative.txt" {
		t.Fatalf("relative = %q", got)
	}
}

func TestGrepToolMatches(t *testing.T) {
	requireBinary(t, "rg")
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("alpha one\nbeta two\nalpha three\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "b.txt"), []byte("nothing here\n"), 0o644)

	tool := CreateGrepTool(dir)
	result := execTool(t, tool, `{"pattern":"alpha"}`)
	text := result.Content[0].(ai.TextContent).Text
	if !strings.Contains(text, "a.txt:1: alpha one") || !strings.Contains(text, "a.txt:3: alpha three") {
		t.Fatalf("grep = %q", text)
	}
	if strings.Contains(text, "b.txt") {
		t.Fatalf("non-matching file in output: %q", text)
	}

	// Literal (non-regex) search: pattern with regex metachars.
	os.WriteFile(filepath.Join(dir, "c.txt"), []byte("a.b\naxb\n"), 0o644)
	result = execTool(t, tool, `{"pattern":"a.b","literal":true}`)
	text = result.Content[0].(ai.TextContent).Text
	if strings.Contains(text, "axb") || !strings.Contains(text, "a.b") {
		t.Fatalf("literal grep = %q", text)
	}

	// ignoreCase.
	result = execTool(t, tool, `{"pattern":"ALPHA","ignoreCase":true}`)
	text = result.Content[0].(ai.TextContent).Text
	if !strings.Contains(text, "a.txt:1:") {
		t.Fatalf("ignoreCase grep = %q", text)
	}

	// glob filter.
	result = execTool(t, tool, `{"pattern":"alpha","glob":"b.txt"}`)
	text = result.Content[0].(ai.TextContent).Text
	if text != "No matches found" {
		t.Fatalf("glob grep = %q", text)
	}

	// Context lines.
	result = execTool(t, tool, `{"pattern":"beta two","context":1}`)
	text = result.Content[0].(ai.TextContent).Text
	if !strings.Contains(text, "a.txt-1- alpha one") || !strings.Contains(text, "a.txt:2: beta two") || !strings.Contains(text, "a.txt-3- alpha three") {
		t.Fatalf("context grep = %q", text)
	}

	// Long-line truncation.
	os.WriteFile(filepath.Join(dir, "long.txt"), []byte(strings.Repeat("x", 600)+"NEEDLE\n"), 0o644)
	result = execTool(t, tool, `{"pattern":"NEEDLE"}`)
	text = result.Content[0].(ai.TextContent).Text
	if !strings.Contains(text, "... [truncated]") {
		t.Fatalf("long line = %q", text)
	}
	var details GrepToolDetails
	json.Unmarshal(result.Details, &details)
	if details.LinesTruncated == nil || !*details.LinesTruncated {
		t.Fatalf("details = %s", result.Details)
	}
}

func TestGrepToolErrors(t *testing.T) {
	requireBinary(t, "rg")
	dir := t.TempDir()
	tool := CreateGrepTool(dir)

	// Missing path.
	_, err := tool.Execute("c", json.RawMessage(`{"pattern":"x","path":"nope/"}`), context.Background(), nil)
	if err == nil || !strings.Contains(err.Error(), "Path not found") {
		t.Fatalf("err = %v", err)
	}

	// Bad regex is an rg error surfaced in-band.
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("x"), 0o644)
	_, err = tool.Execute("c", json.RawMessage(`{"pattern":"[unclosed"}`), context.Background(), nil)
	if err == nil {
		t.Fatal("bad regex should error")
	}
}
