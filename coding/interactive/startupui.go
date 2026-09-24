package interactive

import (
	"sync"
	"time"

	"github.com/dat267/pier/coding"
	"github.com/dat267/pier/tui"
)

// Port of cli/startup-ui.ts: the prompts that run before the app's own TUI.
// createStartupTui builds a main-screen renderer of its own — the same pattern
// SelectSession uses for the --resume picker — so a question can be asked before
// any project resource has been loaded. That is what project trust needs: the
// answer decides whether the project's .pi settings and resources are read at
// all, so it has to come first.
//
// The startup renderer's theme loading (upstream loadStartupThemes, which
// resolves theme resources through the package manager) has no port here: the
// CLI installs the port's palette before this runs (D154), and the theme paths
// from --theme and the global settings are registered with it.

// StartupSelectorOptions are ShowStartupSelector inputs.
type StartupSelectorOptions struct {
	// Settings supplies the terminal capability flags. The bootstrap settings
	// manager is the right one to pass: the project's settings are not readable
	// before the question this prompt asks.
	Settings *coding.SettingsManager
	// Terminal defaults to the process terminal (tests inject a fake).
	Terminal tui.Terminal
}

// ShowStartupSelector asks a question with a select list and returns the chosen
// label. ok is false when the prompt was cancelled or dismissed. It mirrors
// upstream showStartupSelector, including the settle delay that keeps the app's
// TUI from inheriting a half-cleared screen.
func ShowStartupSelector(options StartupSelectorOptions, title string, choices []string) (string, bool) {
	terminal := options.Terminal
	if terminal == nil {
		terminal = tui.NewProcessTerminal(nil, nil)
	}
	showHardwareCursor := false
	clearOnShrink := false
	if options.Settings != nil {
		showHardwareCursor = options.Settings.GetShowHardwareCursor()
		clearOnShrink = options.Settings.GetClearOnShrink()
	}
	ui := tui.NewMainScreen(terminal, showHardwareCursor, coding.GetAgentDir())
	ui.SetClearOnShrink(clearOnShrink)

	result := make(chan string, 1)
	var once sync.Once
	settled := false
	settle := func(label string) {
		once.Do(func() {
			// clearStartupTui: blank the screen and let the terminal settle, then
			// hand it over to the app's TUI.
			ui.Clear()
			ui.RequestRender(false)
			time.Sleep(25 * time.Millisecond)
			settled = true
			result <- label
		})
	}
	selector := NewExtensionSelectorComponent(title, choices,
		func(label string) { settle(label) },
		func() { settle("") },
	)
	ui.AddChild(selector)
	ui.SetFocus(selector)
	RunStartupScreenLoop(ui, terminal, func() bool { return settled })

	chosen := <-result
	return chosen, chosen != ""
}
