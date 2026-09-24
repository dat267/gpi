package interactive

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/dat267/pier/coding"
)

// makeTrustRequiringProject creates a project directory whose .pi holds a
// project setting, which is what makes trust-requiring resources exist upstream.
func makeTrustRequiringProject(t *testing.T, theme string) string {
	t.Helper()
	cwd := t.TempDir()
	dir := filepath.Join(cwd, coding.ConfigDirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"theme":"` + theme + `"}`
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return cwd
}

func projectThemeOf(manager *coding.SettingsManager) string {
	project := manager.GetProjectSettings()
	if project == nil || project.Theme == nil {
		return ""
	}
	return *project.Theme
}

// A session switch can land in another directory, and the runtime is cwd-bound:
// upstream's createRuntime resolves that directory's project trust and builds its
// settings manager, so a session from a project that was never trusted does not
// get that project's settings or resources. This port keeps one settings manager
// per process (D160), so it re-points it — without a prompt, because upstream
// passes hasUI false for any runtime but the initial one.
func TestSwitchResolvesProjectTrustForTheNewCwd(t *testing.T) {
	decisions := map[string]*bool{
		"undecided":           nil,
		"store says trust":    boolRef(true),
		"store says no trust": boolRef(false),
		"override":            nil, // with --approve below
	}
	for _, name := range []string{"undecided", "store says trust", "store says no trust", "override"} {
		t.Run(name, func(t *testing.T) {
			targetCwd := makeTrustRequiringProject(t, "alpha-theme")
			target := makePersistedSession(t, targetCwd, "from the other project")
			app, cleanup := newTestApp(t)
			defer cleanup()

			decision := decisions[name]
			if decision != nil {
				store := coding.NewProjectTrustStore(app.options.AgentDir)
				if err := store.Set(targetCwd, decision); err != nil {
					t.Fatal(err)
				}
			}
			if name == "override" {
				approved := true
				app.options.ProjectTrustOverride = &approved
			}

			if _, err := app.SwitchSession(context.Background(), target.GetSessionFile(), ""); err != nil {
				t.Fatalf("SwitchSession: %v", err)
			}
			wantTrusted := name == "store says trust" || name == "override"
			if got := app.Settings.IsProjectTrusted(); got != wantTrusted {
				t.Errorf("project trusted = %v, want %v", got, wantTrusted)
			}
			if got := projectThemeOf(app.Settings); got != expectedProjectTheme(wantTrusted) {
				t.Errorf("project theme = %q, want %q", got, expectedProjectTheme(wantTrusted))
			}
			if app.Settings.Cwd() != coding.NormalizePath(targetCwd, coding.PathInputOptions{}) {
				t.Errorf("settings cwd = %q, want %q", app.Settings.Cwd(), targetCwd)
			}
		})
	}
}

func expectedProjectTheme(trusted bool) string {
	if trusted {
		return "alpha-theme"
	}
	return ""
}

func boolRef(value bool) *bool { return &value }

// A switch within the same project must not re-resolve anything: the project's
// trust was decided at startup, and re-asking the store here could only disagree
// with what the user already answered.
func TestSwitchWithinTheSameCwdKeepsTheTrustDecision(t *testing.T) {
	app, cleanup := newTestApp(t)
	defer cleanup()
	cwd := app.SessionMgr.GetCwd()

	target := makePersistedSession(t, cwd, "same project")
	store := coding.NewProjectTrustStore(app.options.AgentDir)
	if err := store.Set(cwd, boolRef(false)); err != nil {
		t.Fatal(err)
	}
	if _, err := app.SwitchSession(context.Background(), target.GetSessionFile(), ""); err != nil {
		t.Fatalf("SwitchSession: %v", err)
	}
	if !app.Settings.IsProjectTrusted() {
		t.Fatal("a same-cwd switch re-resolved trust from the store")
	}
}

// A project's trust answer is remembered for the run (upstream main.ts's
// projectTrustByCwd): a project resolved once is not re-read from the store, so
// switching back and forth cannot change an answer the user already gave — a
// store the session itself rewrote (say, /trust on another project) included.
func TestSwitchReusesTheTrustDecisionPerProject(t *testing.T) {
	app, cleanup := newTestApp(t)
	defer cleanup()

	first := makeTrustRequiringProject(t, "alpha-theme")
	firstSession := makePersistedSession(t, first, "first")
	if _, err := app.SwitchSession(context.Background(), firstSession.GetSessionFile(), ""); err != nil {
		t.Fatal(err)
	}
	if app.Settings.IsProjectTrusted() {
		t.Fatal("an undecided project must be untrusted")
	}

	// A decision for that project appears after the fact...
	if err := coding.NewProjectTrustStore(app.options.AgentDir).Set(first, boolRef(true)); err != nil {
		t.Fatal(err)
	}
	// ...and a switch away and back must not pick it up: the answer is already
	// remembered for the run.
	elsewhere := makeTrustRequiringProject(t, "beta-theme")
	elsewhereSession := makePersistedSession(t, elsewhere, "elsewhere")
	if _, err := app.SwitchSession(context.Background(), elsewhereSession.GetSessionFile(), ""); err != nil {
		t.Fatal(err)
	}
	if _, err := app.SwitchSession(context.Background(), firstSession.GetSessionFile(), ""); err != nil {
		t.Fatal(err)
	}
	if app.Settings.IsProjectTrusted() {
		t.Fatal("the remembered answer was re-resolved from the store")
	}
	if got := projectThemeOf(app.Settings); got != "" {
		t.Errorf("project theme = %q, want the project still ignored", got)
	}
}
