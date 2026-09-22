package coding

import (
	ctxpkg "context"
	"path/filepath"
	"strings"
	"testing"
)

// promptSessionForTests assembles a session over the temp agent dir the caller
// set up with tempAgentDir, returning the built prompt and the session cwd.
func promptSessionForTests(t *testing.T, options *CreateAgentSessionOptions) (string, string) {
	t.Helper()
	agentDir := GetAgentDir()
	runtime, err := CreateModelRuntime(CreateModelRuntimeOptions{
		AuthPath:        filepath.Join(agentDir, "auth.json"),
		ModelsPath:      filepath.Join(agentDir, "models.json"),
		Credentials:     newMemoryCredentialStore(),
		RefreshOnCreate: boolPtr(false),
	})
	if err != nil {
		t.Fatal(err)
	}
	settings := NewSettingsManagerFromFiles(t.TempDir(), t.TempDir(), SettingsManagerCreateOptions{})
	if options == nil {
		options = &CreateAgentSessionOptions{}
	}
	cwd := t.TempDir()
	options.Cwd = cwd
	options.AgentDir = agentDir
	options.ModelRuntime = runtime
	options.SettingsManager = settings
	result, err := CreateAgentSession(ctxpkg.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	return result.Session.SystemPrompt(), cwd
}

// TestCreateAgentSessionLoadsSystemPromptFiles pins the resource-loader wiring:
// the agent dir's SYSTEM.md replaces the base preamble and APPEND_SYSTEM.md is
// appended before the project context, both resolved when the session is
// created.
func TestCreateAgentSessionLoadsSystemPromptFiles(t *testing.T) {
	tempAgentDir(t)
	agentDir := GetAgentDir()
	writePromptTestFile(t, filepath.Join(agentDir, "SYSTEM.md"), "You are a custom assistant.")
	writePromptTestFile(t, filepath.Join(agentDir, "APPEND_SYSTEM.md"), "Always answer briefly.")

	prompt, cwd := promptSessionForTests(t, nil)
	if !strings.Contains(prompt, "You are a custom assistant.") {
		t.Fatalf("SYSTEM.md was not loaded into the prompt:\n%s", prompt)
	}
	if strings.Contains(prompt, "You are an expert coding assistant") {
		t.Fatalf("SYSTEM.md must replace the default preamble:\n%s", prompt)
	}
	if !strings.Contains(prompt, "Always answer briefly.") {
		t.Fatalf("APPEND_SYSTEM.md was not appended to the prompt:\n%s", prompt)
	}
	// The append lands before the environment/project context sections.
	appendIndex, cwdIndex := strings.Index(prompt, "Always answer briefly."), strings.LastIndex(prompt, cwd)
	if cwdIndex == -1 || appendIndex > cwdIndex {
		t.Fatalf("append section order: append@%d cwd@%d\n%s", appendIndex, cwdIndex, prompt)
	}
}

// TestCreateAgentSessionPromptFlagsOverrideFiles pins the CLI precedence: the
// flag sources (text or a file path) win over the discovered files, and several
// append sources join with a blank line.
func TestCreateAgentSessionPromptFlagsOverrideFiles(t *testing.T) {
	tempAgentDir(t)
	agentDir := GetAgentDir()
	writePromptTestFile(t, filepath.Join(agentDir, "SYSTEM.md"), "file base")
	writePromptTestFile(t, filepath.Join(agentDir, "APPEND_SYSTEM.md"), "file append")

	base := "flag base"
	prompt, _ := promptSessionForTests(t, &CreateAgentSessionOptions{
		SystemPrompt:       &base,
		AppendSystemPrompt: []string{"one", "two"},
	})
	if !strings.Contains(prompt, "flag base") || strings.Contains(prompt, "file base") {
		t.Fatalf("the --system-prompt source did not replace the file:\n%s", prompt)
	}
	if !strings.Contains(prompt, "one\n\ntwo") || strings.Contains(prompt, "file append") {
		t.Fatalf("the --append-system-prompt sources did not replace the file:\n%s", prompt)
	}
}
