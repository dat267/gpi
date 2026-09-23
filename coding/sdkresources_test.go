package coding

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// --skill paths load even under --no-skills: refusing discovery is not refusing
// what was asked for explicitly (upstream resourceLoader keeps its
// additionalSkillPaths when noSkills is set).
func TestCreateAgentSessionSkillPaths(t *testing.T) {
	tempAgentDir(t)
	runtime := testSessionRuntime(t)
	settings := NewSettingsManagerFromFiles(t.TempDir(), t.TempDir(), SettingsManagerCreateOptions{})
	cwd := t.TempDir()
	skillDir := t.TempDir()
	writeSkill(t, skillDir, "extra", "an extra skill")

	skillNames := func(options *CreateAgentSessionOptions) []string {
		t.Helper()
		options.Cwd = cwd
		options.ModelRuntime = runtime
		options.SettingsManager = settings
		created, err := CreateAgentSession(context.Background(), options)
		if err != nil {
			t.Fatal(err)
		}
		names := make([]string, 0, len(created.Session.Skills()))
		for _, skill := range created.Session.Skills() {
			names = append(names, skill.Name)
		}
		return names
	}

	got := skillNames(&CreateAgentSessionOptions{SkillPaths: []string{skillDir}})
	if len(got) != 1 || got[0] != "extra" {
		t.Fatalf("skills = %v, want [extra]", got)
	}

	// Discovery is off, but the explicit path is still honored.
	got = skillNames(&CreateAgentSessionOptions{SkillPaths: []string{skillDir}, NoSkills: true})
	if len(got) != 1 || got[0] != "extra" {
		t.Errorf("skills with NoSkills = %v, want [extra]", got)
	}

	// ...and with nothing explicit, NoSkills really does mean nothing.
	if got := skillNames(&CreateAgentSessionOptions{NoSkills: true}); len(got) != 0 {
		t.Errorf("skills with NoSkills alone = %v, want none", got)
	}
}

func TestCreateAgentSessionNoContextFiles(t *testing.T) {
	tempAgentDir(t)
	runtime := testSessionRuntime(t)
	settings := NewSettingsManagerFromFiles(t.TempDir(), t.TempDir(), SettingsManagerCreateOptions{})
	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, "AGENTS.md"), []byte("project rules\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	contextFileCount := func(options *CreateAgentSessionOptions) int {
		t.Helper()
		options.Cwd = cwd
		options.ModelRuntime = runtime
		options.SettingsManager = settings
		created, err := CreateAgentSession(context.Background(), options)
		if err != nil {
			t.Fatal(err)
		}
		return len(created.Session.ContextFiles())
	}

	if count := contextFileCount(&CreateAgentSessionOptions{}); count == 0 {
		t.Fatal("the project AGENTS.md should be discovered by default")
	}
	if count := contextFileCount(&CreateAgentSessionOptions{NoContextFiles: true}); count != 0 {
		t.Errorf("context files = %d, want 0 with NoContextFiles", count)
	}
}

// testSessionRuntime builds a runtime with no network and no stored models,
// which is all the resource-loading paths need.
func testSessionRuntime(t *testing.T) *ModelRuntime {
	t.Helper()
	runtime, err := CreateModelRuntime(CreateModelRuntimeOptions{
		AuthPath:        filepath.Join(GetAgentDir(), "auth.json"),
		ModelsPath:      filepath.Join(GetAgentDir(), "models.json"),
		Credentials:     newMemoryCredentialStore(),
		RefreshOnCreate: boolPtr(false),
	})
	if err != nil {
		t.Fatal(err)
	}
	return runtime
}
