package interactive

import (
	"context"
	"testing"

	"github.com/dat267/gpi/tui"
)

func TestDockSpacerAtInit(t *testing.T) {
	app, cleanup := newTestAppB(t)
	defer cleanup()
	app.Init(context.Background())
	if len(app.WidgetAbove.Children) != 1 {
		t.Fatalf("widgets above = %d children, want the default spacer", len(app.WidgetAbove.Children))
	}
	if _, ok := app.WidgetAbove.Children[0].(*tui.Spacer); !ok {
		t.Fatalf("child = %T, want Spacer", app.WidgetAbove.Children[0])
	}
}
