package coding

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeProjectSettings writes a project settings file with one theme value.
func writeProjectSettings(t *testing.T, cwd, theme string) {
	t.Helper()
	dir := filepath.Join(cwd, ConfigDirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"theme":"` + theme + `"}`
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A session switch can land in another directory, and the runtime is cwd-bound
// upstream: createRuntime builds a settings manager for the session's cwd and
// resolves that cwd's trust. This port keeps one manager per process, so the
// project half is re-pointed instead.
func TestSettingsManagerRebindProject(t *testing.T) {
	agentDir := t.TempDir()
	globalPath := filepath.Join(agentDir, "settings.json")
	if err := os.WriteFile(globalPath, []byte(`{"defaultModel":"global-model"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cwdA, cwdB := t.TempDir(), t.TempDir()
	writeProjectSettings(t, cwdA, "alpha-theme")
	writeProjectSettings(t, cwdB, "beta-theme")

	manager := NewSettingsManagerFromFiles(cwdA, agentDir, SettingsManagerCreateOptions{})
	if manager.Cwd() != NormalizePath(cwdA, PathInputOptions{}) {
		t.Fatalf("cwd = %q", manager.Cwd())
	}
	if theme := manager.GetTheme(); theme == nil || *theme != "alpha-theme" {
		t.Fatalf("theme = %v", theme)
	}

	// Re-pointed at B: B's project settings apply, A's are gone, and the global
	// scope is untouched.
	manager.RebindProject(cwdB, true)
	if manager.Cwd() != NormalizePath(cwdB, PathInputOptions{}) {
		t.Fatalf("cwd = %q", manager.Cwd())
	}
	if theme := manager.GetTheme(); theme == nil || *theme != "beta-theme" {
		t.Fatalf("theme = %v", theme)
	}
	if model := manager.GetDefaultModel(); model == nil || *model != "global-model" {
		t.Fatalf("global settings lost: defaultModel = %v", model)
	}
	if !manager.IsProjectTrusted() {
		t.Fatal("trusted project must stay trusted")
	}

	// Untrusted: the project half is dropped, and there is no trust decision for
	// it in the manager.
	manager.RebindProject(cwdA, false)
	if manager.IsProjectTrusted() {
		t.Fatal("rebind must carry the new trust decision")
	}
	if theme := manager.GetTheme(); theme != nil {
		t.Fatalf("untrusted project settings were read: %v", *theme)
	}
	if model := manager.GetDefaultModel(); model == nil || *model != "global-model" {
		t.Fatalf("global settings lost: defaultModel = %v", model)
	}

	// A trusted rebind back to a project with no settings file is fine, and
	// writes now target that cwd's file.
	empty := t.TempDir()
	manager.RebindProject(empty, true)
	manager.SetProjectThemePaths([]string{"written-theme-path"})
	raw, err := os.ReadFile(filepath.Join(empty, ConfigDirName, "settings.json"))
	if err != nil {
		t.Fatalf("write went to the wrong project: %v", err)
	}
	if !strings.Contains(string(raw), "written-theme-path") {
		t.Fatalf("settings file = %s", raw)
	}
}
