package interactive

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/dat267/pier/coding"
)

// TestThemeBootRejectsSourcesBeforeCapabilities makes D154 executable: a theme
// bakes its 256-colour or truecolor escapes when it is created, so the
// capability stage has to precede anything that loads one. The rule used to be
// a comment beside the call site.
func TestThemeBootRejectsSourcesBeforeCapabilities(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("applying theme sources before the capability stage was allowed")
		}
	}()
	NewThemeBoot().ApplySources(ThemeSources{})
}

// TestThemeBootCapabilitiesInstallThePalette pins the division of labour
// between the two stages: the capability stage registers the port's palette
// (that is all it does — loading one is the source stage's job), so a later
// name resolves against it.
func TestThemeBootCapabilitiesInstallThePalette(t *testing.T) {
	SetCustomThemesDir(t.TempDir())
	boot := NewThemeBoot()
	boot.EnableCapabilities()
	if _, ok := registeredThemesGet("dark"); !ok {
		t.Fatalf("the palette was not registered: %v", AvailableThemes())
	}

	boot.ApplySources(ThemeSources{})
	if CurrentTheme() == nil {
		t.Fatal("no theme after the source stage")
	}
}

// TestThemeBootAppliesTheNamedTheme pins that a named theme becomes the current
// one, from the paths the caller resolved.
func TestThemeBootAppliesTheNamedTheme(t *testing.T) {
	SetCustomThemesDir(t.TempDir())
	dir := t.TempDir()
	writeThemeFile(t, dir, "projtheme.json", "projtheme", "#ff0000")

	boot := NewThemeBoot()
	boot.EnableCapabilities()
	boot.ApplySources(ThemeSources{ThemeName: "projtheme", ThemePaths: []string{dir}})
	if got := CurrentThemeName(); got != "projtheme" {
		t.Fatalf("theme = %q, want projtheme", got)
	}
}

// TestThemeBootProjectThemesNeedTrust pins the rule cmd used to carry inline,
// and the one the trust-before/after pair in cmd exists for: a project's own
// theme directory is a source only once the project is trusted, and it becomes
// active when the settings name it.
func TestThemeBootProjectThemesNeedTrust(t *testing.T) {
	SetCustomThemesDir(t.TempDir())
	project := t.TempDir()
	projectThemes := filepath.Join(project, coding.ConfigDirName, "themes")
	if err := os.MkdirAll(projectThemes, 0o755); err != nil {
		t.Fatal(err)
	}
	writeThemeFile(t, projectThemes, "projtheme.json", "projtheme", "#ff0000")

	boot := NewThemeBoot()
	boot.EnableCapabilities()

	// Untrusted: the project's theme directory is not a source.
	boot.ApplySources(ThemeSources{Cwd: project, ThemeName: "projtheme"})
	if containsTheme(AvailableThemes(), "projtheme") {
		t.Fatalf("an untrusted project's theme was discovered: %v", AvailableThemes())
	}

	// Trusted: discovered, and active because it was named.
	boot.ApplySources(ThemeSources{Cwd: project, ThemeName: "projtheme", Trusted: true})
	if !containsTheme(AvailableThemes(), "projtheme") {
		t.Fatalf("the trusted project's theme was not discovered: %v", AvailableThemes())
	}
	if got := CurrentThemeName(); got != "projtheme" {
		t.Fatalf("theme = %q, want the project's own", got)
	}

	// NoThemes drops discovery, the project's themes included, and keeps the
	// explicitly named paths.
	boot.ApplySources(ThemeSources{Cwd: project, ThemeName: "projtheme", Trusted: true, NoThemes: true})
	if containsTheme(AvailableThemes(), "projtheme") {
		t.Fatalf("NoThemes kept the discovered project theme: %v", AvailableThemes())
	}
}

func containsTheme(names []string, want string) bool {
	for _, name := range names {
		if name == want {
			return true
		}
	}
	return false
}
