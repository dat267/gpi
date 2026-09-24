package interactive

import (
	"time"

	"github.com/dat267/pier/tui"
)

// RunStartupScreenLoop drives a startup screen to completion: terminal input and
// paints share this goroutine, replacing the renderer's internal render timer
// (D146). The caller's settled callback ends the loop; the UI is stopped here.
// SelectSession and ShowStartupSelector both run their screen this way.
func RunStartupScreenLoop(ui *tui.MainScreen, terminal tui.Terminal, settled func() bool) {
	rawTerminal, isRaw := terminal.(tui.RawInputTerminal)
	if isRaw {
		rawTerminal.EnableRawInput()
	}
	inputs := make(chan string, 256)
	ui.EnableLoopInput(func(data string) { inputs <- data }, func() {})
	// Post-driven work signals the tick channel; the loop below drains and paints
	// it, so loader results never apply inline from another goroutine.
	ui.EnableRenderTicks()
	ui.Start()
	defer ui.Stop(tui.TuiStopOptions{})
	// Paint the initial state and drain anything the constructor posted.
	ui.RenderNow(false)

	timer := time.NewTimer(time.Hour)
	timer.Stop()
	defer timer.Stop()

	for !settled() {
		select {
		case <-ui.RenderTicks():
			ui.RenderNow(false)
			continue
		default:
		}
		// Arm the wake timer for the next input flush deadline (a lone ESC, an
		// incomplete sequence, a split Kitty response).
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
}
