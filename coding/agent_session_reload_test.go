package coding

import (
	ctxpkg "context"
	"path/filepath"
	"strings"
	"testing"
)

// reloadSessionForTests assembles a session over file-backed settings so Reload
// can be exercised against the files the session was built from.
func reloadSessionForTests(t *testing.T, cwd, agentDir string) (*AgentSession, *SettingsManager) {
	t.Helper()
	runtime, err := CreateModelRuntime(CreateModelRuntimeOptions{
		AuthPath:        filepath.Join(agentDir, "auth.json"),
		ModelsPath:      filepath.Join(agentDir, "models.json"),
		Credentials:     newMemoryCredentialStore(),
		RefreshOnCreate: boolPtr(false),
	})
	if err != nil {
		t.Fatal(err)
	}
	settings := NewSettingsManagerFromFiles(cwd, agentDir, SettingsManagerCreateOptions{})
	sessions := NewSessionManager(cwd, nil)
	result, err := CreateAgentSession(ctxpkg.Background(), &CreateAgentSessionOptions{
		Cwd: cwd, AgentDir: agentDir, ModelRuntime: runtime, SettingsManager: settings,
		SessionManager: sessions,
	})
	if err != nil {
		t.Fatal(err)
	}
	return result.Session, settings
}

// TestReloadRereadsResourceFiles covers upstream AgentSession.reload's resource
// half: settings and the ported resource files are re-read, and the system
// prompt is rebuilt from them.
func TestReloadRereadsResourceFiles(t *testing.T) {
	tempAgentDir(t)
	agentDir := GetAgentDir()
	cwd := t.TempDir()
	session, _ := reloadSessionForTests(t, cwd, agentDir)

	before := session.SystemPrompt()
	if strings.Contains(before, "added later") || strings.Contains(before, "tdd") {
		t.Fatalf("the session prompt must not hold resources written later:\n%s", before)
	}

	// Resources appear after the session was created: only a reload sees them.
	writePromptTestFile(t, filepath.Join(agentDir, "APPEND_SYSTEM.md"), "added later")
	writePromptTestFile(t, filepath.Join(agentDir, "AGENTS.md"), "project context added later")
	writePromptTestFile(t, filepath.Join(agentDir, "skills", "tdd", "SKILL.md"),
		"---\nname: tdd\ndescription: Test-driven development\n---\n\nWrite the failing test first.\n")

	session.Reload()

	prompt := session.SystemPrompt()
	for _, want := range []string{"added later", "project context added later", "tdd"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("reload did not pick up %q:\n%s", want, prompt)
		}
	}
	if paths := session.PromptSourcePaths(); len(paths) != 1 || !strings.HasSuffix(paths[0], "APPEND_SYSTEM.md") {
		t.Fatalf("prompt source paths = %#v", paths)
	}
	found := false
	for _, file := range session.ContextFiles() {
		if strings.HasSuffix(file.Path, "AGENTS.md") {
			found = true
		}
	}
	if !found {
		t.Fatalf("context files = %#v", session.ContextFiles())
	}
	skills := session.Skills()
	if len(skills) != 1 || skills[0].Name != "tdd" {
		t.Fatalf("skills = %#v", skills)
	}
}

// TestReloadKeepsTheActiveTools covers the other half of upstream's
// _buildRuntime call: the reload re-applies the active tool selection to the
// prompt instead of falling back to the default set.
func TestReloadKeepsTheActiveTools(t *testing.T) {
	tempAgentDir(t)
	cwd := t.TempDir()
	runtime, err := CreateModelRuntime(CreateModelRuntimeOptions{
		AuthPath:        filepath.Join(GetAgentDir(), "auth.json"),
		ModelsPath:      filepath.Join(GetAgentDir(), "models.json"),
		Credentials:     newMemoryCredentialStore(),
		RefreshOnCreate: boolPtr(false),
	})
	if err != nil {
		t.Fatal(err)
	}
	settings := NewSettingsManagerFromFiles(cwd, GetAgentDir(), SettingsManagerCreateOptions{})
	result, err := CreateAgentSession(ctxpkg.Background(), &CreateAgentSessionOptions{
		Cwd: cwd, AgentDir: GetAgentDir(), ModelRuntime: runtime, SettingsManager: settings,
		SessionManager: NewSessionManager(cwd, nil),
		Tools:          []ToolName{ToolNameRead, ToolNameGrep},
	})
	if err != nil {
		t.Fatal(err)
	}
	session := result.Session
	selected := func() []string {
		if session.SystemPromptOptions == nil {
			t.Fatal("session prompt options missing")
		}
		return session.SystemPromptOptions.SelectedTools
	}
	if names := session.ActiveToolNames(); len(names) != 2 {
		t.Fatalf("active tools = %v", names)
	}
	// The prompt is built from the selected tool names, so they are recorded at
	// session creation (upstream _buildRuntime passes activeToolNames).
	if got := strings.Join(selected(), ","); got != "read,grep" {
		t.Fatalf("selected tools = %q", got)
	}

	session.Reload()

	if names := session.ActiveToolNames(); len(names) != 2 {
		t.Fatalf("reload changed the active tools: %v", names)
	}
	if got := strings.Join(selected(), ","); got != "read,grep" {
		t.Fatalf("reload lost the tool selection: %q", got)
	}
}

// TestReloadRereadsSettings covers the settings half: a settings.json change on
// disk only takes effect through Reload, and the settings-backed queue modes
// are re-applied with it.
func TestReloadRereadsSettings(t *testing.T) {
	tempAgentDir(t)
	agentDir := GetAgentDir()
	cwd := t.TempDir()
	session, _ := reloadSessionForTests(t, cwd, agentDir)

	if session.SteeringMode() == "all" {
		t.Fatal("unexpected steering mode before reload")
	}
	writePromptTestFile(t, filepath.Join(agentDir, "settings.json"), `{"steeringMode":"all","followUpMode":"all"}`)

	session.Reload()

	if mode := session.SteeringMode(); mode != "all" {
		t.Fatalf("steering mode after reload = %q", mode)
	}
	if mode := session.FollowUpMode(); mode != "all" {
		t.Fatalf("follow-up mode after reload = %q", mode)
	}
}

// TestReloadRereadsTheTrustGate pins that a reload re-resolves project trust:
// an untrusted project's .pi/APPEND_SYSTEM.md must not be read on reload either.
func TestReloadRereadsTheTrustGate(t *testing.T) {
	tempAgentDir(t)
	agentDir := GetAgentDir()
	cwd := t.TempDir()
	untrusted := false
	settings := NewSettingsManagerFromFiles(cwd, agentDir, SettingsManagerCreateOptions{ProjectTrusted: &untrusted})
	runtime, err := CreateModelRuntime(CreateModelRuntimeOptions{
		AuthPath:        filepath.Join(agentDir, "auth.json"),
		ModelsPath:      filepath.Join(agentDir, "models.json"),
		Credentials:     newMemoryCredentialStore(),
		RefreshOnCreate: boolPtr(false),
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := CreateAgentSession(ctxpkg.Background(), &CreateAgentSessionOptions{
		Cwd: cwd, AgentDir: agentDir, ModelRuntime: runtime, SettingsManager: settings,
		SessionManager: NewSessionManager(cwd, nil),
	})
	if err != nil {
		t.Fatal(err)
	}
	session := result.Session
	// Both a global file and an untrusted project file appear after creation:
	// the reload reads the global one and skips the project's.
	writePromptTestFile(t, filepath.Join(agentDir, "APPEND_SYSTEM.md"), "global append")
	writePromptTestFile(t, filepath.Join(cwd, ConfigDirName, "APPEND_SYSTEM.md"), "project append")

	session.Reload()

	prompt := session.SystemPrompt()
	if !strings.Contains(prompt, "global append") {
		t.Fatalf("reload did not re-read the agent-dir prompt file:\n%s", prompt)
	}
	if strings.Contains(prompt, "project append") {
		t.Fatalf("an untrusted project's prompt file was read on reload:\n%s", prompt)
	}
}
