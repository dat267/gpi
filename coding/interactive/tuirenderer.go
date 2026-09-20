package interactive

import (
	"github.com/dat267/gpi/tui"
)

// Port of src/modes/interactive/tui-renderer.ts: the composition root that
// builds the regular or fullscreen renderer and the swappable reference.
//
// Divergences: clipboard copying is injected because the native helper is out
// of scope (D106); the browser opener reuses the injected opener (D92).

// InteractiveTuiOptions configure the renderer.
type InteractiveTuiOptions struct {
	TuiMode                string // "regular" | "fullscreen"
	ShowHardwareCursor     bool
	LogDirectory           string
	Terminal               tui.Terminal
	OnRightClickPaste      func()
	FullscreenCopyOnSelect *bool
}

// CopySelectionFn copies text to the clipboard, returning (ok, message).
type CopySelectionFn func(text string) (bool, string)

var clipboardCopier CopySelectionFn

// SetClipboardCopier installs the clipboard copier (D106).
func SetClipboardCopier(copier CopySelectionFn) { clipboardCopier = copier }

// CreateInteractiveTui builds the renderer for the requested mode.
func CreateInteractiveTui(options InteractiveTuiOptions) tui.TUI {
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
		return tui.NewAltScreen(terminal, options.ShowHardwareCursor, options.LogDirectory, tui.AltScreenOptions{
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
	}
	return tui.NewMainScreen(terminal, options.ShowHardwareCursor, options.LogDirectory)
}

// CreateInteractiveTuiReference returns a stable handle that forwards to the
// active renderer (upstream createInteractiveTuiReference).
func CreateInteractiveTuiReference(getTui func() tui.TUI) *tui.TuiReference {
	return tui.NewTuiReference(getTui)
}
