package interactive

import (
	"github.com/dat267/pier/coding"
	"github.com/dat267/pier/tui"
)

// Port of src/modes/interactive/tui-renderer.ts: the composition root that
// builds the regular or fullscreen renderer and the swappable reference.
//
// Divergences: the native (Bun FFI) clipboard has no Go counterpart (D106),
// so copying uses the ported platform-command implementation; the browser
// opener reuses the injected opener (D92).

// InteractiveTuiOptions configure the renderer.
type InteractiveTuiOptions struct {
	TuiMode                string // "regular" | "fullscreen"
	ShowHardwareCursor     bool
	LogDirectory           string
	Terminal               tui.Terminal
	OnRightClickPaste      func()
	FullscreenCopyOnSelect *bool
	// OnDebug runs the debug command. Upstream's renderer matches the global
	// debug key and calls ui.onDebug, which interactive-mode.ts points at
	// handleDebugCommand, so shift+ctrl+d and /debug do the same thing.
	OnDebug func()
}

// debugKeyPattern is the global debug key upstream matches in its renderer
// ("shift+ctrl+d", tui.ts).
const debugKeyPattern tui.KeyId = "shift+ctrl+d"

// CopySelectionFn copies text to the clipboard, returning (ok, message).
type CopySelectionFn func(text string) (bool, string)

var clipboardCopier CopySelectionFn

// clipboardScreen holds the screen created by createInteractiveTui so the
// async clipboard failure can flash through Post. Written once during startup,
// before any copy goroutine can read it (the go statement orders it).
var clipboardScreen tui.TUI

// SetClipboardCopier installs the clipboard copier (D106).
func SetClipboardCopier(copier CopySelectionFn) { clipboardCopier = copier }

// ClipboardReadFn reads plain text from the system clipboard.
type ClipboardReadFn func() (string, error)

var clipboardReader ClipboardReadFn = coding.ReadClipboardText

// SetClipboardReader installs the clipboard reader (D106 test seam).
func SetClipboardReader(reader ClipboardReadFn) { clipboardReader = reader }

// readClipboardText runs the configured clipboard reader.
func readClipboardText() (string, error) {
	if clipboardReader == nil {
		return coding.ReadClipboardText()
	}
	return clipboardReader()
}

// CreateInteractiveTui builds the renderer for the requested mode. The
// interactive mode drives rendering from its own loop, so the renderer's
// internal timer is replaced by the tick channel (stage 2).
func CreateInteractiveTui(options InteractiveTuiOptions) tui.TUI {
	// The selection copier is a process-wide seam (D106); wire the real
	// clipboard implementation once. The subprocess can hang for up to its
	// timeout, so it runs off the loop: the selection flashes "Copied!"
	// optimistically and a failure is marshaled back through Post to flash in
	// place of the error message the synchronous form would have shown.
	SetClipboardCopier(func(text string) (bool, string) {
		coding.CopyTextToClipboardAsync(text, func(err error) {
			if err == nil || clipboardScreen == nil {
				return
			}
			clipboardScreen.Post(func() {
				if altscreen, isAlt := clipboardScreen.(*tui.AltScreen); isAlt {
					altscreen.Flash(err.Error(), 0)
				}
			})
		})
		return true, ""
	})
	return withRenderTicks(createInteractiveTui(options))
}

// withRenderTicks switches a freshly created renderer to loop-driven rendering.
func withRenderTicks(screen tui.TUI) tui.TUI {
	if screen != nil {
		screen.EnableRenderTicks()
	}
	return screen
}

func createInteractiveTui(options InteractiveTuiOptions) tui.TUI {
	terminal := options.Terminal
	if terminal == nil {
		terminal = tui.NewProcessTerminal(nil, nil)
	}
	if options.TuiMode == "fullscreen" {
		styleSearchMatch := func(text string) string {
			theme := ActiveTheme()
			return theme.Bg("searchMatchBg", theme.Fg("searchMatchText", text))
		}
		copyOnSelect := options.FullscreenCopyOnSelect
		screen := tui.NewAltScreen(terminal, options.ShowHardwareCursor, options.LogDirectory, tui.AltScreenOptions{
			SearchMatchStyle: func(text string) string {
				return ActiveTheme().Underline(styleSearchMatch(text))
			},
			SearchCurrentMatchStyle: func(text string) string {
				theme := ActiveTheme()
				return theme.Bold(theme.Inverse(styleSearchMatch(text)))
			},
			SearchNavigationButtonStyle: func(text string, hovered bool) string {
				if hovered {
					return ActiveTheme().Underline(text)
				}
				return text
			},
			ScrollToEndIndicator: func() string {
				shortcut := KeyDisplayText("tui.altScreen.bottom")
				label := " ↓ Jump to latest message"
				if shortcut != "" {
					label += " · " + shortcut
				}
				label += " "
				theme := ActiveTheme()
				return theme.Bg("selectedBg", theme.Fg("text", label))
			},
			OpenURL:           func(url string) { browserOpener(url) },
			OnRightClickPaste: options.OnRightClickPaste,
			CopyOnSelect:      copyOnSelect,
			CopySelection: func(text string) (bool, bool, string) {
				if clipboardCopier == nil {
					return false, false, ""
				}
				ok, message := clipboardCopier(text)
				return true, ok, message
			},
		})
		installDebugKey(screen.Renderer, options.OnDebug)
		clipboardScreen = screen
		return screen
	}
	screen := tui.NewMainScreen(terminal, options.ShowHardwareCursor, options.LogDirectory)
	installDebugKey(screen.Renderer, options.OnDebug)
	clipboardScreen = screen
	return screen
}

// installDebugKey gives a renderer the global debug key (upstream's
// `matchesKey(data, "shift+ctrl+d") && this.onDebug` in tui.ts) and the callback
// that key runs. Both screens embed the renderer type that carries them, and the
// reference wrapper hides them, so this happens on the concrete screen.
func installDebugKey(renderer *tui.Renderer, onDebug func()) {
	if renderer == nil {
		return
	}
	renderer.MatchesDebugKey = func(data string) bool { return tui.MatchesKey(data, debugKeyPattern) }
	renderer.OnDebug = onDebug
}

// CreateInteractiveTuiReference returns a stable handle that forwards to the
// active renderer (upstream createInteractiveTuiReference).
func CreateInteractiveTuiReference(getTui func() tui.TUI) *tui.TuiReference {
	return tui.NewTuiReference(getTui)
}
