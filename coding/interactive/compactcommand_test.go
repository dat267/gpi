package interactive

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestCompactCommandReportsErrorAndClearsIndicator drives /compact through the
// app wiring on an empty session: the compaction fails ("Nothing to compact")
// and the compaction_end event must clear the status indicator, leaving the
// error visible. Regression for the stuck "Compacting context..." spinner.
func TestCompactCommandReportsErrorAndClearsIndicator(t *testing.T) {
	app, cleanup := newTestApp(t)
	defer cleanup()
	app.Init(context.Background())

	app.Commands.HandleCompactCommand(context.Background(), "")
	time.Sleep(100 * time.Millisecond) // let the render timer settle

	if app.Events.UIState != nil && app.Events.UIState.ActiveStatusIndicator != nil {
		t.Fatalf("compaction status indicator still active after /compact: %T",
			app.Events.UIState.ActiveStatusIndicator)
	}
	chat := renderAppChat(app)
	if !strings.Contains(chat, "Compaction failed") {
		t.Fatalf("chat missing the compaction error; chat=%q", chat)
	}
}
