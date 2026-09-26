package coding

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

// setupAuthCommandDir points the agent dir at a temp dir and seeds auth.json.
func setupAuthCommandDir(t *testing.T, content string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", dir)
	writeAuthFile(t, filepath.Join(dir, "auth.json"), content)
}

func TestRunAuthCommandPrintAPIKey(t *testing.T) {
	setupAuthCommandDir(t, `{"anthropic":{"type":"api_key","key":"sk-secret"}}`)
	var stdout, stderr bytes.Buffer
	handled, code := RunAuthCommand([]string{"auth", "print-api-key", "--provider", "anthropic"}, &stdout, &stderr)
	if !handled {
		t.Fatal("auth was not handled")
	}
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr.String())
	}
	if stdout.String() != "sk-secret\n" {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestRunAuthCommandPrintBearerToken(t *testing.T) {
	setupAuthCommandDir(t, `{"openai-codex":{"type":"oauth","refresh":"r","access":"bearer-token","expires":4102444800000}}`)
	var stdout, stderr bytes.Buffer
	handled, code := RunAuthCommand([]string{"auth", "print-bearer-token", "--provider", "openai-codex"}, &stdout, &stderr)
	if !handled || code != 0 {
		t.Fatalf("handled=%v exit=%d stderr=%s", handled, code, stderr.String())
	}
	if stdout.String() != "bearer-token\n" {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestRunAuthCommandPrintAPIKeyRejectsOAuth(t *testing.T) {
	setupAuthCommandDir(t, `{"openai-codex":{"type":"oauth","refresh":"r","access":"bearer-token","expires":4102444800000}}`)
	var stdout, stderr bytes.Buffer
	handled, code := RunAuthCommand([]string{"auth", "print-api-key", "--provider", "openai-codex"}, &stdout, &stderr)
	if !handled || code != 1 {
		t.Fatalf("handled=%v exit=%d", handled, code)
	}
	if !strings.Contains(stderr.String(), "configured with OAuth, not an API key") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestRunAuthCommandCheckJSON(t *testing.T) {
	setupAuthCommandDir(t, `{"anthropic":{"type":"api_key","key":"sk-secret"}}`)
	var stdout, stderr bytes.Buffer
	handled, code := RunAuthCommand([]string{"auth", "check", "--provider", "anthropic", "--json"}, &stdout, &stderr)
	if !handled || code != 0 {
		t.Fatalf("handled=%v exit=%d stderr=%s", handled, code, stderr.String())
	}
	var result map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("output is not JSON: %v (%q)", err, stdout.String())
	}
	if result["status"] != "ready" || result["provider"] != "anthropic" || result["authType"] != "api_key" {
		t.Fatalf("result = %+v", result)
	}
}

func TestRunAuthCommandCheckNotReady(t *testing.T) {
	setupAuthCommandDir(t, `{"openai":{"type":"api_key","key":"sk-other"}}`)
	var stdout, stderr bytes.Buffer
	handled, code := RunAuthCommand([]string{"auth", "check", "--provider", "anthropic"}, &stdout, &stderr)
	if !handled || code != 1 {
		t.Fatalf("handled=%v exit=%d", handled, code)
	}
	if strings.TrimSpace(stdout.String()) != "not_ready" {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestRunAuthCommandHelp(t *testing.T) {
	var stdout, stderr bytes.Buffer
	handled, code := RunAuthCommand([]string{"auth", "help"}, &stdout, &stderr)
	if !handled || code != 0 {
		t.Fatalf("handled=%v exit=%d", handled, code)
	}
	if !strings.Contains(stdout.String(), "print-api-key") || !strings.Contains(stdout.String(), "auth check") {
		t.Fatalf("help = %q", stdout.String())
	}
}

func TestRunAuthCommandIgnoresNonAuth(t *testing.T) {
	var stdout, stderr bytes.Buffer
	handled, _ := RunAuthCommand([]string{"hello", "world"}, &stdout, &stderr)
	if handled {
		t.Fatal("a non-auth invocation must not be handled")
	}
}
