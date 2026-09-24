package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dat267/pier/coding"
	"github.com/dat267/pier/coding/interactive"
)

// A project's own themes are one of the resources trust gates: upstream's resource
// loader discovers <cwd>/.pi/themes alongside the agent's themes directory, and
// only for a trusted project. The port installed its sources before the trust
// decision, against the directory pier was started in, and never revisited them —
// so a trusted project's themes were unreachable even when its settings named one.
func TestThemeSourcesFollowProjectTrust(t *testing.T) {
	previousDir := interactive.CustomThemesDir()
	previousTheme := interactive.CurrentThemeName()
	t.Cleanup(func() {
		interactive.SetCustomThemesDir(previousDir)
		interactive.InitTheme(previousTheme, false)
	})

	agentDir := t.TempDir()
	project := t.TempDir()
	writeTheme := func(name, body string) {
		dir := filepath.Join(project, coding.ConfigDirName, "themes")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name+".json"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// A valid theme document: the shipped dark palette under another name (the
	// loader validates the whole token set).
	builtin, err := os.ReadFile(filepath.Join(moduleRootForTest(t), "coding", "interactive", "themes", "dark.json"))
	if err != nil {
		t.Fatal(err)
	}
	writeTheme("projtheme", strings.Replace(string(builtin), `"name": "dark"`, `"name": "projtheme"`, 1))
	if err := os.MkdirAll(filepath.Join(project, coding.ConfigDirName), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, coding.ConfigDirName, "settings.json"),
		[]byte(`{"theme":"projtheme"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	args := &coding.Args{}
	runtimeSettings := coding.NewSettingsManagerFromFiles(project, agentDir, coding.SettingsManagerCreateOptions{})

	// Untrusted: the project's theme directory is not a source, and its theme
	// setting is not read either.
	untrusted := false
	untrustedSettings := coding.NewSettingsManagerFromFiles(project, agentDir, coding.SettingsManagerCreateOptions{ProjectTrusted: &untrusted})
	applyThemeSources(args, untrustedSettings, agentDir, project, false)
	if containsTheme(interactive.AvailableThemes(), "projtheme") {
		t.Errorf("an untrusted project's theme was discovered: %v", interactive.AvailableThemes())
	}

	// Trusted: the theme is discovered and it is the active one, because the
	// project settings name it.
	applyThemeSources(args, runtimeSettings, agentDir, project, true)
	if !containsTheme(interactive.AvailableThemes(), "projtheme") {
		t.Fatalf("the trusted project's theme was not discovered: %v", interactive.AvailableThemes())
	}
	if got := interactive.CurrentThemeName(); got != "projtheme" {
		t.Errorf("theme = %q, want the project's own", got)
	}

	// --no-themes keeps the named paths but drops discovery, project themes
	// included (upstream's noThemes).
	applyThemeSources(&coding.Args{NoThemes: true}, runtimeSettings, agentDir, project, true)
	if containsTheme(interactive.AvailableThemes(), "projtheme") {
		t.Errorf("--no-themes kept the discovered project theme: %v", interactive.AvailableThemes())
	}
}

func containsTheme(themes []string, want string) bool {
	for _, theme := range themes {
		if theme == want {
			return true
		}
	}
	return false
}

// moduleRootForTest walks up to the module root.
func moduleRootForTest(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("module root not found")
		}
		dir = parent
	}
}
