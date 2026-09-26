package interactive

import (
	"context"
	"testing"
	"time"

	"github.com/dat267/pier/tui"
)

// TestCustomEditorEmbeddedStatusAnimates pins the loop-mode animation seam:
// the working/compaction indicator is embedded in the editor's border and is
// not a container child, so the renderer's animation walk can only find it if
// the editor itself reports as an Animator. Regression for the frozen
// "⠋ Compacting context..." frame (no repaints happen during compaction, so
// the time-derived frame never advanced).
func TestCustomEditorEmbeddedStatusAnimates(t *testing.T) {
	SetTrueColorSupport(true)
	SetStyleColorsEnabled(true)
	SetRegisteredThemes(nil)
	InitTheme("dark", false)
	editor := NewCustomEditor(nil, tui.EditorTheme{}, nil, CustomEditorOptions{EmbedWorkingStatus: true})
	indicator := NewCompactionStatusIndicator(nil, "manual")
	editor.SetWorkingStatusIndicator(indicator)

	animator, ok := any(editor).(tui.Animator)
	if !ok {
		t.Fatal("CustomEditor with an embedded status indicator must implement tui.Animator")
	}
	want, _ := animator.AnimationFrame(time.Now())
	if !want {
		t.Fatal("embedded status indicator must want animation frames")
	}

	editor.SetWorkingStatusIndicator(nil)
	want, _ = animator.AnimationFrame(time.Now())
	if want {
		t.Fatal("editor without an embedded indicator must not want animation frames")
	}
}

// TestCompactionIndicatorDrivesAnimation drives the app seam: after the
// compaction status indicator is shown (embedded in the editor border), the
// renderer's NextAnimation query must report a pending frame so the run loop
// keeps painting the spinner.
func TestCompactionIndicatorDrivesAnimation(t *testing.T) {
	app, cleanup := newTestApp(t)
	defer cleanup()
	app.Init(context.Background())

	app.uiState.ShowStatusIndicator(NewCompactionStatusIndicator(app.ui, "manual"))
	want, _ := app.ui.NextAnimation()
	if !want {
		t.Fatal("renderer animation walk must see the editor-embedded compaction indicator")
	}
	app.uiState.ClearStatusIndicator("", false)
	want, _ = app.ui.NextAnimation()
	if want {
		t.Fatal("no animation wanted after the indicator is cleared")
	}
}
