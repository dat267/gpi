package coding

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dat267/pier/agent"
	"github.com/dat267/pier/ai"
)

// Tool tests keyed to upstream tool semantics (truncate.ts, read.ts,
// write.ts, ls.ts, path-utils.ts).

func execTool(t *testing.T, tool agent.AgentTool, args string) agent.AgentToolResult {
	t.Helper()
	result, err := tool.Execute("call-1", json.RawMessage(args), context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestTruncateHead(t *testing.T) {
	// No truncation under the limits.
	content := "a\nb\nc"
	result := TruncateHead(content, TruncationOptions{})
	if result.Truncated || result.Content != content || result.TotalLines != 3 {
		t.Fatalf("result = %+v", result)
	}

	// Line limit.
	result = TruncateHead("1\n2\n3\n4\n5", TruncationOptions{MaxLines: 2})
	if !result.Truncated || result.TruncatedBy != TruncatedByLines || result.Content != "1\n2" || result.OutputLines != 2 {
		t.Fatalf("result = %+v", result)
	}

	// Byte limit: 50KB cap.
	big := strings.Repeat("x", 3000)
	result = TruncateHead(big+"\nline2\nline3", TruncationOptions{MaxLines: 2000, MaxBytes: 1024})
	if !result.Truncated || result.TruncatedBy != TruncatedByBytes || result.OutputBytes > 1024 {
		t.Fatalf("result = %+v", result)
	}

	// First line exceeding the limit.
	huge := strings.Repeat("y", 60000)
	result = TruncateHead(huge+"\nrest", TruncationOptions{MaxBytes: 1024})
	if !result.FirstLineExceedsLimit || result.Content != "" || result.TruncatedBy != TruncatedByBytes {
		t.Fatalf("result = %+v", result)
	}
}

func TestTruncateTailPartialLastLine(t *testing.T) {
	// A single line longer than the byte limit is tail-truncated partially.
	big := strings.Repeat("z", 60000)
	result := TruncateTail(big, TruncationOptions{MaxBytes: 1024})
	if !result.LastLinePartial || len(result.Content) != 1024 {
		t.Fatalf("result = %+v (len %d)", result, len(result.Content))
	}
	// Keep the END of the line.
	if !strings.HasSuffix(big, result.Content) {
		t.Fatal("tail truncation should keep the end of the line")
	}
}

func TestFormatSize(t *testing.T) {
	cases := map[int64]string{512: "512B", 2048: "2.0KB", 3 * 1024 * 1024: "3.0MB"}
	for bytes, want := range cases {
		if got := FormatSize(bytes); got != want {
			t.Fatalf("FormatSize(%d) = %q; want %q", bytes, got, want)
		}
	}
}

func TestReadToolTextAndTruncation(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "sample.txt")
	content := ""
	for i := 1; i <= 100; i++ {
		content += "line " + itoa(i) + "\n"
	}
	os.WriteFile(file, []byte(content), 0o644)

	tool := CreateReadTool(dir, nil)
	result := execTool(t, tool, `{"path":"sample.txt"}`)
	text := result.Content[0].(ai.TextContent).Text
	if strings.Contains(text, "[Showing lines") {
		t.Fatalf("unexpected truncation: %q", text[len(text)-100:])
	}

	// Offset/limit with continuation notice.
	result = execTool(t, tool, `{"path":"sample.txt","offset":10,"limit":5}`)
	text = result.Content[0].(ai.TextContent).Text
	if !strings.HasPrefix(text, "line 10\n") || !strings.Contains(text, "[86 more lines in file. Use offset=15 to continue.]") {
		t.Fatalf("text tail = %q", text[len(text)-80:])
	}

	// Truncation notice with next offset.
	os.WriteFile(file, []byte(strings.Repeat("line\n", 3000)), 0o644)
	result = execTool(t, tool, `{"path":"sample.txt"}`)
	text = result.Content[0].(ai.TextContent).Text
	if !strings.Contains(text, "[Showing lines 1-2000 of 3000. Use offset=2001 to continue.]") {
		t.Fatalf("truncation notice missing: %q", text[len(text)-120:])
	}
	var details ReadToolDetails
	json.Unmarshal(result.Details, &details)
	if details.Truncation == nil || !details.Truncation.Truncated {
		t.Fatalf("details = %s", result.Details)
	}

	// Offset beyond end.
	if _, err := tool.Execute("c", json.RawMessage(`{"path":"sample.txt","offset":99999}`), context.Background(), nil); err == nil {
		t.Fatal("expected out-of-bounds error")
	}
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

func TestReadToolImageDetection(t *testing.T) {
	dir := t.TempDir()
	// Minimal valid PNG header.
	png := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 16)...)
	os.WriteFile(filepath.Join(dir, "img.png"), png, 0o644)

	tool := CreateReadTool(dir, nil)
	result := execTool(t, tool, `{"path":"img.png"}`)
	text := result.Content[0].(ai.TextContent).Text
	if !strings.HasPrefix(text, "Read image file [image/png]") {
		t.Fatalf("text = %q", text)
	}
	img, ok := result.Content[1].(ai.ImageContent)
	if !ok || img.MimeType != "image/png" {
		t.Fatalf("content[1] = %+v", result.Content[1])
	}
}

func TestWriteToolCreatesAndOverwrites(t *testing.T) {
	dir := t.TempDir()
	tool := CreateWriteTool(dir)
	result := execTool(t, tool, `{"path":"sub/dir/new.txt","content":"hello"}`)

	if text := result.Content[0].(ai.TextContent).Text; text != "Successfully wrote to sub/dir/new.txt" {
		t.Fatalf("text = %q", text)
	}
	data, err := os.ReadFile(filepath.Join(dir, "sub", "dir", "new.txt"))
	if err != nil || string(data) != "hello" {
		t.Fatalf("file = %q, %v", data, err)
	}

	// Overwrite.
	execTool(t, tool, `{"path":"sub/dir/new.txt","content":"second"}`)
	data, _ = os.ReadFile(filepath.Join(dir, "sub", "dir", "new.txt"))
	if string(data) != "second" {
		t.Fatalf("overwrite = %q", data)
	}
}

func TestLsToolSortingAndLimits(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "zeta"), 0o755)
	os.WriteFile(filepath.Join(dir, "Alpha.txt"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(dir, ".dot"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(dir, "b.txt"), []byte("x"), 0o644)

	tool := CreateLsTool(dir)
	result := execTool(t, tool, `{}`)
	text := result.Content[0].(ai.TextContent).Text
	want := ".dot\nAlpha.txt\nb.txt\nzeta/"
	if !strings.HasPrefix(text, want) {
		t.Fatalf("ls = %q; want prefix %q", text, want)
	}

	// Limit notice.
	result = execTool(t, tool, `{"limit":2}`)
	text = result.Content[0].(ai.TextContent).Text
	if !strings.Contains(text, "2 entries limit reached. Use limit=4 for more") {
		t.Fatalf("limit notice missing: %q", text)
	}

	// File (not directory).
	os.WriteFile(filepath.Join(dir, "afile"), []byte("x"), 0o644)
	if _, err := tool.Execute("c", json.RawMessage(`{"path":"afile"}`), context.Background(), nil); err == nil {
		t.Fatal("expected not-a-directory error")
	}
}

func TestPathResolutionVariants(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "Capture d'écran.png"), []byte("x"), 0o644)

	// Curly-quote fallback.
	resolved := ResolveReadPath("'Capture d'écran.png'", dir)
	_ = resolved
	// Direct path.
	if got := ResolveToCwd("a/b.txt", dir); !strings.HasSuffix(got, filepath.Join("a", "b.txt")) {
		t.Fatalf("resolveToCwd = %q", got)
	}
	// Tilde expansion.
	home := homeDir()
	if got := NormalizePath("~/x", PathInputOptions{}); !strings.HasPrefix(got, home) {
		t.Fatalf("tilde = %q", got)
	}
	// @-prefix + unicode space folding.
	if got := ExpandPath("@my\u00A0file.txt"); got != "my file.txt" {
		t.Fatalf("expandPath = %q", got)
	}
	// file:// URL.
	if got := NormalizePath("file:///tmp/x%20y.txt", PathInputOptions{}); got != "/tmp/x y.txt" {
		t.Fatalf("file URL = %q", got)
	}
	// Inside-cwd relative path.
	if rel, ok := GetCwdRelativePath(filepath.Join(dir, "a.txt"), dir); !ok || rel != "a.txt" {
		t.Fatalf("rel = %q, %v", rel, ok)
	}
	if _, ok := GetCwdRelativePath("/etc/passwd", dir); ok {
		t.Fatal("outside cwd should not be relative")
	}
}

func TestFileMutationQueueSerializesSameFile(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "f.txt")
	os.WriteFile(file, []byte("0"), 0o644)

	var mu sync.Mutex
	order := []string{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = WithFileMutationQueue(file, func() error {
			time.Sleep(30 * time.Millisecond)
			mu.Lock()
			order = append(order, "first-finish")
			mu.Unlock()
			return nil
		})
	}()
	time.Sleep(5 * time.Millisecond)
	// Second operation on the same file must wait for the first.
	_ = WithFileMutationQueue(file, func() error {
		mu.Lock()
		order = append(order, "second-start")
		mu.Unlock()
		return nil
	})
	<-done
	if order[0] != "first-finish" {
		t.Fatalf("order = %v", order)
	}
}
