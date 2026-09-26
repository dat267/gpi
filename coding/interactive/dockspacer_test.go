package interactive

import (
	"context"
	"testing"

	"github.com/dat267/pier/tui"
)

func TestDockSpacerAtInit(t *testing.T) {
	app, cleanup := newTestAppB(t)
	defer cleanup()
	app.Init(context.Background())
	if len(app.widgetAbove.Children) != 1 {
		t.Fatalf("widgets above = %d children, want the default spacer", len(app.widgetAbove.Children))
	}
	if _, ok := app.widgetAbove.Children[0].(*tui.Spacer); !ok {
		t.Fatalf("child = %T, want Spacer", app.widgetAbove.Children[0])
	}
}
