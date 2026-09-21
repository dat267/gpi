package coding

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dat267/pier/ai"
)

// Bash tool tests keyed to upstream semantics (bash.ts): output truncation
// with full-output temp file, exit codes, signal terminations (128+N),
// timeouts, and aborts.

func TestBashSimpleOutput(t *testing.T) {
	dir := t.TempDir()
	tool := CreateBashTool(dir, nil)
	result, err := tool.Execute("c", json.RawMessage(`{"command":"echo hello"}`), context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	// Untruncated output passes through raw (trailing newline included).
	if text := result.Content[0].(ai.TextContent).Text; strings.TrimSpace(text) != "hello" {
		t.Fatalf("text = %q", text)
	}
}

func TestBashStdoutAndStderr(t *testing.T) {
	dir := t.TempDir()
	tool := CreateBashTool(dir, nil)
	result, err := tool.Execute("c", json.RawMessage(`{"command":"echo out; echo err 1>&2"}`), context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	text := result.Content[0].(ai.TextContent).Text
	if !strings.Contains(text, "out") || !strings.Contains(text, "err") {
		t.Fatalf("text = %q", text)
	}
}

func TestBashNonZeroExitIsError(t *testing.T) {
	dir := t.TempDir()
	tool := CreateBashTool(dir, nil)
	_, err := tool.Execute("c", json.RawMessage(`{"command":"echo before; exit 3"}`), context.Background(), nil)
	if err == nil || !strings.Contains(err.Error(), "before") || !strings.Contains(err.Error(), "Command exited with code 3") {
		t.Fatalf("err = %v", err)
	}
}

func TestBashSignalKillIsNotSuccess(t *testing.T) {
	// D68: a signal-terminated command must FAIL, never report success.
	dir := t.TempDir()
	tool := CreateBashTool(dir, nil)
	_, err := tool.Execute("c", json.RawMessage(`{"command":"kill -TERM $$"}`), context.Background(), nil)
	if err == nil {
		t.Fatal("signal-terminated command must error")
	}
	if !strings.Contains(err.Error(), "143") {
		t.Fatalf("expected 128+15=143, err = %v", err)
	}
}

func TestBashTruncationKeepsTail(t *testing.T) {
	dir := t.TempDir()
	tool := CreateBashTool(dir, nil)
	// 3000 lines → tail-truncated to the last 2000 with the notice.
	result, err := tool.Execute("c", json.RawMessage(`{"command":"seq 1 3000"}`), context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	text := result.Content[0].(ai.TextContent).Text
	if !strings.Contains(text, "[Showing lines 1001-3000 of 3000. Full output:") {
		t.Fatalf("notice missing: %q", text[len(text)-120:])
	}
	// The tail content is the END of the output.
	if !strings.Contains(text, "\n3000") {
		t.Fatal("tail content missing")
	}
	// Details carry the truncation and full-output path.
	var details BashToolDetails
	json.Unmarshal(result.Details, &details)
	if details.Truncation == nil || !details.Truncation.Truncated || details.FullOutputPath == nil {
		t.Fatalf("details = %s", result.Details)
	}
	// The temp file holds the FULL output.
	full, err := os.ReadFile(*details.FullOutputPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(full), "\n1\n") && !strings.HasPrefix(string(full), "1\n") {
		t.Fatalf("full output missing head: %q", string(full)[:20])
	}
}

func TestBashTimeout(t *testing.T) {
	dir := t.TempDir()
	tool := CreateBashTool(dir, nil)
	start := time.Now()
	_, err := tool.Execute("c", json.RawMessage(`{"command":"echo partial; sleep 30","timeout":1}`), context.Background(), nil)
	elapsed := time.Since(start)
	if err == nil || !strings.Contains(err.Error(), "Command timed out after 1 seconds") {
		t.Fatalf("err = %v", err)
	}
	// The partial output survives into the error.
	if !strings.Contains(err.Error(), "partial") {
		t.Fatalf("partial output missing: %v", err)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("timeout not honored: %v", elapsed)
	}
}

func TestBashAbort(t *testing.T) {
	dir := t.TempDir()
	tool := CreateBashTool(dir, nil)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	_, err := tool.Execute("c", json.RawMessage(`{"command":"echo started; sleep 30"}`), ctx, nil)
	if err == nil || !strings.Contains(err.Error(), "Command aborted") {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(err.Error(), "started") {
		t.Fatalf("partial output missing: %v", err)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("abort not prompt")
	}
}

func TestBashWorkingDirectoryAndMissingCwd(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "sub"), 0o755)
	tool := CreateBashTool(filepath.Join(dir, "sub"), nil)
	result, err := tool.Execute("c", json.RawMessage(`{"command":"pwd"}`), context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if text := result.Content[0].(ai.TextContent).Text; !strings.HasSuffix(strings.TrimSpace(text), "sub") {
		t.Fatalf("pwd = %q", text)
	}

	missing := CreateBashTool(filepath.Join(dir, "nope"), nil)
	if _, err := missing.Execute("c", json.RawMessage(`{"command":"echo hi"}`), context.Background(), nil); err == nil ||
		!strings.Contains(err.Error(), "Working directory does not exist") {
		t.Fatalf("missing cwd err = %v", err)
	}
}

func TestBashSessionEnvStripped(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PI_MODEL", "leak")
	tool := CreateBashTool(dir, nil)
	result, err := tool.Execute("c", json.RawMessage(`{"command":"env | grep '^PI_MODEL=' | wc -l"}`), context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if text := result.Content[0].(ai.TextContent).Text; strings.TrimSpace(text) != "0" {
		t.Fatalf("PI_MODEL leaked: %q", text)
	}

	// Explicit session env exposure.
	exposing := CreateBashTool(dir, &BashToolOptions{SessionEnv: map[string]string{"PI_MODEL": "gpt-5"}})
	result, err = exposing.Execute("c", json.RawMessage(`{"command":"echo $PI_MODEL"}`), context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if text := result.Content[0].(ai.TextContent).Text; strings.TrimSpace(text) != "gpt-5" {
		t.Fatalf("PI_MODEL = %q", text)
	}
}
