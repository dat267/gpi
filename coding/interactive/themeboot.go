package interactive

import (
	"path/filepath"

	"github.com/dat267/pier/coding"
)

// This file owns the theme boot sequence: what the theme subsystem has to be
// told, and in the only order that works. The rules used to live as comments
// beside the call sites in cmd, spread over two packages; six revert commits
// later they are here, where a violation is a panic and a test.
//
//   - The capability stage precedes anything that loads a theme: a theme bakes
//     its 256-colour or truecolor escapes when it is created, and the port's
//     palette is registered under the upstream names (D154).
//   - The source stage installs discovery before the name is resolved, because
//     resolving it is what reads those sources.
//   - The project's own theme directory is a source only once the project is
//     trusted, so boot applies the source stage twice: once before the trust
//     decision (which can read neither the project's themes nor its theme
//     setting) and again after it.
//
// The fourth constraint in this area — the terminal background query may only
// be written once the terminal is in raw mode (D165) — is enforced by its own
// seam: RunWiring.OnStarted, which runs after the terminal is started.

// ThemeSources is the input to one application of the source stage: everything
// the theme subsystem needs to know that the caller has already resolved.
type ThemeSources struct {
	// AgentDir is the agent's directory; its themes subdirectory is always a
	// source.
	AgentDir string
	// Cwd is the working directory whose project-local themes (under
	// coding.ConfigDirName) become a source once Trusted.
	Cwd string
	// ThemeName is the theme to load, resolved by the caller from the settings,
	// a --theme/--use-theme override, or the built-in default. Empty asks for
	// the environment-detected default.
	ThemeName string
	// ThemePaths are the additional theme files and directories the caller
	// resolved from the settings and the command line.
	ThemePaths []string
	// Trusted permits the project's own theme directory as a source.
	Trusted bool
	// NoThemes drops discovery entirely and keeps the named paths (upstream's
	// noThemes).
	NoThemes bool
}

// ThemeBoot is the theme boot sequence. One per run: the caller performs the
// capability stage once and the source stage as many times as the trust
// decision requires.
type ThemeBoot struct {
	capabilities bool
}

// NewThemeBoot creates a boot that has performed no stage yet.
func NewThemeBoot() *ThemeBoot { return &ThemeBoot{} }

// EnableCapabilities performs the capability stage: colour depth, the style
// helpers, and the port's palette. The order matters — the palette bakes its
// escapes on creation, so the capability switch has to precede the install
// (D154). It is idempotent, and it must precede ApplySources.
func (b *ThemeBoot) EnableCapabilities() {
	SetTrueColorSupport(true)
	SetStyleColorsEnabled(true)
	InstallPierTheme()
	b.capabilities = true
}

// ApplySources performs the source stage: install the discovery sources, then
// resolve and load the named theme. It panics when the capability stage has not
// run yet, because that is a programming error rather than a runtime condition
// (the same way SetRegisteredThemes panics on an invalid theme name).
func (b *ThemeBoot) ApplySources(in ThemeSources) {
	if !b.capabilities {
		panic("interactive: ThemeBoot.ApplySources before EnableCapabilities " +
			"(D154: a theme bakes its colour escapes when it is created)")
	}
	paths := append([]string{}, in.ThemePaths...)
	if in.Trusted && !in.NoThemes {
		paths = append(paths, filepath.Join(in.Cwd, coding.ConfigDirName, "themes"))
	}
	SetCustomThemeSources(CustomThemeSources{
		Dir:         filepath.Join(in.AgentDir, "themes"),
		Paths:       paths,
		NoDiscovery: in.NoThemes,
	})
	// The watcher stays off: the interactive theme controller owns watching, and
	// boot happens before there is a UI loop to deliver a change on.
	InitTheme(in.ThemeName, false)
}
