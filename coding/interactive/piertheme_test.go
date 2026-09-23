package interactive

import (
	"strings"
	"testing"
)

// installPierThemeForTest puts the port's palette in place the way cmd/pier
// does, without the rest of the app's setup.
func installPierThemeForTest(t *testing.T) {
	t.Helper()
	SetCustomThemesDir(t.TempDir())
	SetRegisteredThemes(nil)
	SetTrueColorSupport(true)
	SetStyleColorsEnabled(true)
	InstallPierTheme()
}

// The install must actually take effect: it shadows the embedded upstream
// palettes, which is what makes the difference visible at startup.
func TestInstallPierThemeShadowsUpstream(t *testing.T) {
	SetCustomThemesDir(t.TempDir())
	SetRegisteredThemes(nil)

	upstream, err := GetResolvedThemeColors("dark")
	if err != nil {
		t.Fatal(err)
	}
	if upstream["accent"] != "#8abeb7" {
		t.Fatalf("unregistered dark accent = %q, want the upstream teal", upstream["accent"])
	}

	installPierThemeForTest(t)

	installed, err := GetResolvedThemeColors("dark")
	if err != nil {
		t.Fatal(err)
	}
	if installed["accent"] != "#ffb454" {
		t.Errorf("installed dark accent = %q, want the port's amber", installed["accent"])
	}
	light, err := GetResolvedThemeColors("light")
	if err != nil {
		t.Fatal(err)
	}
	if light["accent"] != "#b45309" {
		t.Errorf("installed light accent = %q", light["accent"])
	}
}

// The point of the palette: the terminal's own background shows through, so no
// background token may resolve to a colour.
func TestPierThemeBackgroundsAreTerminalDefault(t *testing.T) {
	installPierThemeForTest(t)
	for _, name := range []string{"dark", "light"} {
		t.Run(name, func(t *testing.T) {
			theme := GetThemeByName(name)
			if theme == nil {
				t.Fatalf("theme %q did not load", name)
			}
			for key := range backgroundColorKeys {
				ansi, ok := theme.bgColors[key]
				if !ok {
					t.Errorf("%s: no background token %q", name, key)
					continue
				}
				if ansi != "\x1b[49m" {
					t.Errorf("%s: %s = %q, want the terminal default (\\x1b[49m)", name, key, ansi)
				}
			}
			// Bg styles the text and restores the default background after it.
			if got := theme.Bg("selectedBg", "row"); got != "\x1b[49mrow\x1b[49m" {
				t.Errorf("%s: Bg(selectedBg) = %q", name, got)
			}
			// Primary text is the terminal's foreground, for the same reason.
			if got := theme.Fg("text", "hello"); got != "\x1b[39mhello\x1b[39m" {
				t.Errorf("%s: Fg(text) = %q", name, got)
			}
		})
	}
}

// Every token the UI can ask for must exist, or Fg/Bg panic at render time.
func TestPierThemeDefinesEveryToken(t *testing.T) {
	installPierThemeForTest(t)
	for _, name := range []string{"dark", "light"} {
		t.Run(name, func(t *testing.T) {
			theme := GetThemeByName(name)
			if theme == nil {
				t.Fatalf("theme %q did not load", name)
			}
			for _, key := range append(append([]string{}, requiredThemeColors...), optionalThemeColors...) {
				if backgroundColorKeys[key] {
					if _, ok := theme.bgColors[key]; !ok {
						t.Errorf("%s: missing background token %q", name, key)
					}
					continue
				}
				if _, ok := theme.fgColors[key]; !ok {
					t.Errorf("%s: missing token %q", name, key)
				}
			}
		})
	}
}

// A palette that kept upstream's teal would defeat the point of shipping one.
func TestPierThemeIsDistinctFromUpstream(t *testing.T) {
	installPierThemeForTest(t)
	for _, name := range []string{"dark", "light"} {
		colors, err := GetResolvedThemeColors(name)
		if err != nil {
			t.Fatal(err)
		}
		for _, token := range []string{"accent", "borderAccent", "mdHeading", "bashMode", "mdListBullet"} {
			if colors[token] == "" {
				continue
			}
			if strings.HasPrefix(colors[token], "#8abeb7") || strings.HasPrefix(colors[token], "#5f87ff") {
				t.Errorf("%s: %s = %q still carries an upstream colour", name, token, colors[token])
			}
		}
	}
}

// Dark and light are selected the same way as before: by the setting, or by the
// terminal's own background when nothing is configured.
func TestPierThemeSelection(t *testing.T) {
	installPierThemeForTest(t)

	// A bare name resolves to itself.
	if got, ok := ResolveThemeSetting(strPtr("light"), TerminalThemeDark); !ok || got != "light" {
		t.Errorf("ResolveThemeSetting(light) = %q, %v", got, ok)
	}
	// An auto pair resolves against the terminal's background.
	auto := strPtr("light/dark")
	if got, ok := ResolveThemeSetting(auto, TerminalThemeLight); !ok || got != "light" {
		t.Errorf("auto on a light terminal = %q, %v", got, ok)
	}
	if got, ok := ResolveThemeSetting(auto, TerminalThemeDark); !ok || got != "dark" {
		t.Errorf("auto on a dark terminal = %q, %v", got, ok)
	}
	// With nothing configured the terminal decides, and both answers are a
	// theme that exists.
	for _, terminal := range []TerminalTheme{TerminalThemeDark, TerminalThemeLight} {
		name, ok := ResolveThemeSetting(nil, terminal)
		if ok {
			t.Errorf("ResolveThemeSetting(nil) = %q, want the unset setting to fall through", name)
		}
		if GetThemeByName(GetDefaultTheme()) == nil {
			t.Errorf("the default theme %q did not load", GetDefaultTheme())
		}
	}

	// What the app does: install, then init by name.
	InitTheme("light", false)
	if CurrentThemeName() != "light" {
		t.Errorf("current theme = %q", CurrentThemeName())
	}
	if !IsLightTheme(CurrentThemeName()) {
		t.Error("the light theme should classify as light")
	}
}

// The registered palette carries its document, so the export path can still
// resolve tokens that came from memory rather than a file.
func TestPierThemeExportColorsResolve(t *testing.T) {
	installPierThemeForTest(t)

	pageBg, cardBg, infoBg, err := GetThemeExportColors("dark")
	if err != nil {
		t.Fatalf("export colors: %v", err)
	}
	if pageBg != "#101010" || cardBg != "#171717" || infoBg != "#2a2318" {
		t.Errorf("export colors = %q/%q/%q", pageBg, cardBg, infoBg)
	}
	if vars := generateThemeVars("dark"); !strings.Contains(vars, "--accent: #ffb454;") {
		t.Errorf("theme vars missing the accent:\n%s", vars)
	}
}
