package coding

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/dat267/pier/agent"
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

// requirePosixShell skips when the resolved shell cannot run a POSIX command.
// The tests below assert POSIX semantics (128+N signal codes, `env | grep | wc`
// pipelines). A Windows machine without Git for Windows resolves bash from
// PATH, which may be the WSL launcher stub.
func requirePosixShell(t *testing.T) {
	t.Helper()
	config, err := GetShellConfig("")
	if err != nil {
		t.Skipf("no shell available: %v", err)
	}
	out, err := exec.Command(config.Shell, "-c", "echo posix-probe").Output()
	if err != nil || !strings.Contains(string(out), "posix-probe") {
		t.Skipf("%s does not run POSIX shell commands", config.Shell)
	}
}

// requireShellEnvInjection also requires the shell to receive the child
// environment. Reaching bash through the WSL launcher does not: it runs `echo`
// and friends, but drops cmd.Env, so the exposure assertions below cannot hold
// there (verified by hand: even a plain variable does not arrive).
func requireShellEnvInjection(t *testing.T) {
	t.Helper()
	requirePosixShell(t)
	config, err := GetShellConfig("")
	if err != nil {
		t.Skipf("no shell available: %v", err)
	}
	command := exec.Command(config.Shell, "-c", "echo env-probe=$PIER_SHELL_PROBE")
	command.Env = append(os.Environ(), "PIER_SHELL_PROBE=1")
	out, err := command.Output()
	if err != nil || !strings.Contains(string(out), "env-probe=1") {
		t.Skipf("%s does not receive the child environment", config.Shell)
	}
}

func TestBashSignalKillIsNotSuccess(t *testing.T) {
	requirePosixShell(t)
	// D68: a signal-terminated command must FAIL, never report success.
	dir := t.TempDir()
	tool := CreateBashTool(dir, nil)
	_, err := tool.Execute("c", json.RawMessage(`{"command":"kill -TERM $$"}`), context.Background(), nil)
	if err == nil {
		t.Fatal("signal-terminated command must error")
	}
	// 128+N needs a wait status that carries the signal. Windows has none: the
	// WSL launcher surfaces the raw code (15) instead, so only assert it where
	// the status reports the signal.
	if runtime.GOOS != "windows" && !strings.Contains(err.Error(), "143") {
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
	requirePosixShell(t)
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
	// Abort *after* the command has produced output, not after a fixed delay: the
	// assertion below is that an abort keeps the partial output, and cancelling on
	// a 100ms timer raced the shell's own start-up. On a loaded runner the abort
	// won that race and the test failed wanting "started" in output that had never
	// been echoed. The watchdog is a hang guard, not an expectation.
	started := make(chan struct{}, 1)
	onUpdate := func(result agent.AgentToolResult) {
		for _, content := range result.Content {
			if text, ok := content.(ai.TextContent); ok && strings.Contains(text.Text, "started") {
				select {
				case started <- struct{}{}:
				default:
				}
			}
		}
	}
	go func() {
		select {
		case <-started:
		case <-time.After(15 * time.Second):
		}
		cancel()
	}()
	start := time.Now()
	_, err := tool.Execute("c", json.RawMessage(`{"command":"echo started; sleep 30"}`), ctx, onUpdate)
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
	requireShellEnvInjection(t)
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
