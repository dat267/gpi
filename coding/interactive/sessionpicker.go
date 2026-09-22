package interactive

import (
	"os"
	"sync"
	"time"

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
	settled := false
	settle := func(path string) {
		once.Do(func() {
			settled = true
			result <- path
		})
	}
	stop := func() { ui.Stop(tui.TuiStopOptions{}) }
	selector := NewSessionSelectorComponent(
		options.CurrentLoader,
		options.AllLoader,
		func(path string) {
			println("ONSELECT", path)
			settle(path)
			stop()
		},
		func() {
			println("ONCANCEL")
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
		// Loader results must apply inside the picker's render pass (Post
		// drains at the start of doRender), never inline from the loader
		// goroutine (stage 4).
		SessionSelectorOptions{Post: ui.Post, ShowRenameHint: false, Keybindings: tui.GetKeybindings()},
	)
	ui.AddChild(selector)
	ui.SetFocus(selector.GetSessionList())

	// D146: the picker is driven by its own loop — terminal input and paints
	// share this goroutine, replacing the renderer's internal render timer.
	rawTerminal, isRaw := terminal.(tui.RawInputTerminal)
	if isRaw {
		rawTerminal.EnableRawInput()
	}
	inputs := make(chan string, 256)
	ui.EnableLoopInput(func(data string) { inputs <- data }, func() {})
	// Post-driven work (loader results) signals the tick channel; the loop
	// below drains and paints it.
	ui.EnableRenderTicks()
	ui.Start()
	defer ui.Stop(tui.TuiStopOptions{})
	// Paint the initial state and drain the constructor's posted load.
	ui.RenderNow(false)

	timer := time.NewTimer(time.Hour)
	timer.Stop()
	defer timer.Stop()

	for !settled {
		// A posted render (loader results, selection updates) paints here.
		select {
		case <-ui.RenderTicks():
			ui.RenderNow(false)
			continue
		default:
		}
		// Arm the wake timer for the next input flush deadline (a lone ESC,
		// an incomplete sequence, a split Kitty response).
		var wake <-chan time.Time
		if isRaw {
			if flushDeadline, ok := rawTerminal.NextInputFlushDeadline(); ok {
				delay := time.Until(flushDeadline)
				if delay <= 0 {
					for _, sequence := range rawTerminal.FlushPendingInput() {
						ui.HandleTerminalInput(sequence)
					}
					ui.RenderNow(false)
					continue
				}
				timer.Reset(delay)
				wake = timer.C
			}
		}

		select {
		case <-ui.RenderTicks():
			ui.RenderNow(false)
		case chunk, ok := <-inputs:
			if !ok {
				continue
			}
			if isRaw {
				for _, sequence := range rawTerminal.FeedInput([]byte(chunk)) {
					ui.HandleTerminalInput(sequence)
				}
			} else {
				println("HANDLE-LEN", len(chunk))
				ui.HandleTerminalInput(chunk)
			}
			ui.RenderNow(false)
		case <-wake:
			if isRaw {
				for _, sequence := range rawTerminal.FlushPendingInput() {
					ui.HandleTerminalInput(sequence)
				}
			}
			ui.RenderNow(false)
		}
	}
	return <-result
}
