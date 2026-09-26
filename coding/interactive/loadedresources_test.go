package interactive

import (
	"strings"
	"testing"
)

// TestLoadedThemesSection pins the Themes section's input: built-in themes (no
// source path) are not listed, custom themes are, and their names reach the
// compact list in registration order.
func TestLoadedThemesSection(t *testing.T) {
	// formatScopeGroups resolves the active theme for the scope headings.
	SetCustomThemesDir(t.TempDir())
	SetRegisteredThemes(nil)
	SetTrueColorSupport(true)
	SetStyleColorsEnabled(true)
	InitTheme("dark", false)

	names, body := loadedThemes([]ThemeInfo{
		{Name: "dark", Path: ""},
		{Name: "projtheme", Path: "/p/.pi/themes/projtheme.json"},
		{Name: "usertheme", Path: "/u/.pi/themes/usertheme.json"},
	})
	if len(names) != 2 {
		t.Fatalf("names = %#v, want the two custom themes", names)
	}
	if names[0] != "projtheme" || names[1] != "usertheme" {
		t.Errorf("names = %#v, want registration order", names)
	}
	if strings.Contains(body, "dark") {
		t.Errorf("a built-in theme reached the expanded body: %q", body)
	}
	for _, want := range []string{"projtheme.json", "usertheme.json"} {
		if !strings.Contains(body, want) {
			t.Errorf("the expanded body is missing %q: %q", want, body)
		}
	}
}

// TestLoadedThemesSectionSkipsBuiltinsOnly covers the empty case: a registry with
// no custom themes produces no section at all.
func TestLoadedThemesSectionSkipsBuiltinsOnly(t *testing.T) {
	SetCustomThemesDir(t.TempDir())
	SetRegisteredThemes(nil)
	SetTrueColorSupport(true)
	SetStyleColorsEnabled(true)
	InitTheme("dark", false)

	names, body := loadedThemes([]ThemeInfo{{Name: "dark"}, {Name: "light"}})
	if len(names) != 0 || body != "" {
		t.Fatalf("built-ins produced a section: names=%#v body=%q", names, body)
	}
	if names, body := loadedThemes(nil); len(names) != 0 || body != "" {
		t.Fatalf("an empty registry produced a section: names=%#v body=%q", names, body)
	}
}
