package interactive

import (
	"os"
	"sync"

	"github.com/dat267/pier/coding"
	"github.com/dat267/pier/tui"
)

// SelectSessionOptions are SelectSession inputs.
type SelectSessionOptions struct {
	Settings      *coding.SettingsManager
	CurrentLoader SessionsLoader
	AllLoader     SessionsLoader
	// Terminal defaults to the process terminal (tests inject a fake).
	Terminal tui.Terminal
	// Exit overrides process exit for the picker's exit action (tests).
	Exit func(code int)
}

// SelectSession runs the standalone --resume session picker (upstream
// cli/session-picker.ts selectSession over createStartupTui: a main-screen
// renderer hosting the session selector). It returns the selected session
// path, or "" when the picker is cancelled.
func SelectSession(options SelectSessionOptions) string {
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
	settle := func(path string) {
		once.Do(func() { result <- path })
	}
	stop := func() { ui.Stop(tui.TuiStopOptions{}) }
	selector := NewSessionSelectorComponent(
		options.CurrentLoader,
		options.AllLoader,
		func(path string) {
			settle(path)
			stop()
		},
		func() {
			settle("")
			stop()
		},
		func() {
			stop()
			exit := options.Exit
			if exit == nil {
				exit = os.Exit
			}
			exit(0)
		},
		func() { ui.RequestRender(false) },
		// The picker renders on its own timer goroutine; loader results must
		// apply inside that render pass (Post drains at the start of doRender),
		// never inline from the loader goroutine (stage 4).
		SessionSelectorOptions{Post: ui.Post, ShowRenameHint: false, Keybindings: tui.GetKeybindings()},
	)
	ui.AddChild(selector)
	ui.SetFocus(selector.GetSessionList())
	ui.Start()
	return <-result
}
