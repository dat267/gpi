package interactive

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestCompactCommandReportsErrorAndClearsIndicator drives /compact on an empty
// session: the compaction fails ("Nothing to compact"), the compaction_end
// event must clear the status indicator and the error must surface in the
// chat. Regression for the stuck "Compacting context..." spinner.
//
// The command is invoked before the loop starts, so its events are queued and
// drained by the loop (stage 1: producers only enqueue). Running the command
// while the loop applies events would exercise the D136-class input race that
// stage 3 removes, not this behaviour.
func TestCompactCommandReportsErrorAndClearsIndicator(t *testing.T) {
	app, cleanup := newTestApp(t)
	defer cleanup()

	ctx, cancel := context.WithCancel(context.Background())
	app.Init(ctx)
	app.commands.HandleCompactCommand(ctx, "")

	done := make(chan struct{})
	go func() {
		defer close(done)
		app.Run(ctx)
	}()

	// Container reads are lock-guarded, so the assertion can run while the
	// loop drains the queued events.
	waitForConditionWithin(t, func() bool {
		return strings.Contains(renderAppChat(app), "Compaction failed")
	}, 6*time.Second)

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("run loop did not exit within 2s of cancellation")
	}
	// The loop has stopped, so the indicator state is safe to read.
	if app.events.UIState != nil && app.events.UIState.ActiveStatusIndicator != nil {
		t.Fatalf("compaction status indicator still active after /compact: %T",
			app.events.UIState.ActiveStatusIndicator)
	}
}
