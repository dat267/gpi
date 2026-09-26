package tui

import "testing"

// These tests drive the overlay stack and its focus-restore state machine
// directly. The machine asks the outside world only two questions — is an entry
// visible on this terminal, and is a component still mounted — so a fake host
// replaces the renderer and the terminal entirely.

type fakeOverlayHost struct {
	cols, rows int
	mounted    map[Component]bool
}

// overlayVisible answers the terminal-dependent half of visibility: an entry's
// Visible callback. The stack itself handles the hidden flag and nil cases.
func (h *fakeOverlayHost) overlayVisible(entry *overlayEntry) bool {
	if entry.options == nil || entry.options.Visible == nil {
		return true
	}
	return entry.options.Visible(h.cols, h.rows)
}

func (h *fakeOverlayHost) componentMounted(component Component) bool {
	return h.mounted[component]
}

func newTestOverlayStack() (*overlayStack, *fakeOverlayHost, *focusComponent) {
	root := &focusComponent{staticComponent: staticComponent{lines: []string{"root"}}}
	host := &fakeOverlayHost{cols: 80, rows: 24, mounted: map[Component]bool{root: true}}
	stack := newOverlayStack(host)
	stack.SetFocus(root)
	return stack, host, root
}

func mountedFocusComponent(host *fakeOverlayHost, line string) *focusComponent {
	component := &focusComponent{staticComponent: staticComponent{lines: []string{line}}}
	host.mounted[component] = true
	return component
}

// TestOverlayStackRestoresFocusWhenTheTopOverlayHides covers the removal path:
// hiding the top overlay hands focus to the next visible one, and hiding the
// last returns focus to whatever was focused before the first overlay was shown.
func TestOverlayStackRestoresFocusWhenTheTopOverlayHides(t *testing.T) {
	stack, host, root := newTestOverlayStack()
	first := mountedFocusComponent(host, "first")
	second := mountedFocusComponent(host, "second")

	stack.Push(first, nil)
	stack.SetFocus(first)
	if stack.Focused() != Component(first) {
		t.Fatalf("focus after showing the first overlay = %v", stack.Focused())
	}
	if !first.focused {
		t.Fatal("the overlay was not told it has focus")
	}

	stack.Push(second, nil)
	stack.SetFocus(second)

	stack.HideTop()
	if stack.Focused() != Component(first) {
		t.Fatalf("focus after hiding the top overlay = %v, want the next visible one", stack.Focused())
	}
	stack.HideTop()
	if stack.Focused() != Component(root) {
		t.Fatalf("focus after hiding the last overlay = %v, want the pre-overlay focus", stack.Focused())
	}
	if !root.focused {
		t.Fatal("the pre-overlay component was not told it has focus again")
	}
}

// TestOverlayStackNonCapturingKeepsFocus covers the non-capturing overlay: it
// never takes focus and is never the topmost capturing overlay.
func TestOverlayStackNonCapturingKeepsFocus(t *testing.T) {
	stack, host, root := newTestOverlayStack()
	overlay := mountedFocusComponent(host, "overlay")

	entry := stack.Push(overlay, &OverlayOptions{NonCapturing: true})
	if stack.Focused() != Component(root) {
		t.Fatalf("focus = %v, want the root to keep it", stack.Focused())
	}
	if top := stack.TopmostVisible(); top != nil {
		t.Fatalf("topmost visible = %v, want none for a non-capturing overlay", top.component)
	}
	// Focusing it explicitly still works, and it then reports as an overlay.
	if !stack.FocusEntry(entry) {
		t.Fatal("FocusEntry refused a visible non-capturing overlay")
	}
	if stack.Focused() != Component(overlay) || !stack.IsFocused() {
		t.Fatalf("focus = %v, IsFocused = %v", stack.Focused(), stack.IsFocused())
	}
}

// TestOverlayStackRetargetsPreFocus covers the nested-overlay removal rule: an
// inner overlay whose pre-focus was the removed outer one must fall back to the
// outer one's own pre-focus instead of pointing at a component that is gone.
func TestOverlayStackRetargetsPreFocus(t *testing.T) {
	stack, host, root := newTestOverlayStack()
	outer := mountedFocusComponent(host, "outer")
	inner := mountedFocusComponent(host, "inner")

	outerEntry := stack.Push(outer, nil)
	stack.SetFocus(outer)
	innerEntry := stack.Push(inner, nil)
	stack.SetFocus(inner)

	if !stack.Hide(outerEntry) {
		t.Fatal("the outer overlay was not in the stack")
	}
	if innerEntry.preFocus != Component(root) {
		t.Fatalf("inner pre-focus = %v, want the root", innerEntry.preFocus)
	}
	// Hiding the inner overlay now restores to the root, not to the removed outer.
	stack.HideTop()
	if stack.Focused() != Component(root) {
		t.Fatalf("focus = %v, want the root", stack.Focused())
	}
}

// TestOverlayStackReconcileKeepsFocusWhileBlocked covers the blocked restore: a
// component that took focus from an overlay keeps it, and the overlay's focus is
// not resumed behind its back.
func TestOverlayStackReconcileKeepsFocusWhileBlocked(t *testing.T) {
	stack, host, _ := newTestOverlayStack()
	overlay := mountedFocusComponent(host, "overlay")
	other := mountedFocusComponent(host, "other")

	stack.Push(overlay, nil)
	stack.SetFocus(overlay)
	stack.SetFocus(other)

	if stack.Focused() != Component(other) {
		t.Fatalf("focus = %v, want the component that took it", stack.Focused())
	}
	stack.ReconcileFocus()
	if stack.Focused() != Component(other) {
		t.Fatalf("focus moved while blocked: %v", stack.Focused())
	}
	// Once focus leaves the blocker, the overlay's focus resumes.
	stack.SetFocus(nil)
	if stack.Focused() != Component(overlay) {
		t.Fatalf("focus = %v, want the overlay to resume", stack.Focused())
	}
	stack.ReconcileFocus()
	if stack.Focused() != Component(overlay) {
		t.Fatalf("focus after reconcile = %v, want the overlay", stack.Focused())
	}
}

// TestOverlayStackReconcileHandsFocusBackWhenInvisible covers a visibility
// callback turning false under the focused overlay (a resize): the next input
// pass hands focus back to what the overlay saved.
func TestOverlayStackReconcileHandsFocusBackWhenInvisible(t *testing.T) {
	stack, host, root := newTestOverlayStack()
	overlay := mountedFocusComponent(host, "overlay")

	visible := true
	stack.Push(overlay, &OverlayOptions{Visible: func(int, int) bool { return visible }})
	stack.SetFocus(overlay)
	if !stack.IsFocused() {
		t.Fatal("the overlay should be focused while visible")
	}

	visible = false
	stack.ReconcileFocus()
	if stack.Focused() != Component(root) {
		t.Fatalf("focus = %v, want the root after the overlay became invisible", stack.Focused())
	}
	if stack.IsFocused() {
		t.Fatal("an invisible overlay must not report as focused")
	}
}

// TestOverlayStackHidingTheFocusedOverlayHandsFocusBack covers the visibility
// flag path (as opposed to removal): hiding re-focuses, showing again re-focuses
// it and bumps its focus order.
func TestOverlayStackHidingTheFocusedOverlayHandsFocusBack(t *testing.T) {
	stack, host, root := newTestOverlayStack()
	overlay := mountedFocusComponent(host, "overlay")

	entry := stack.Push(overlay, nil)
	stack.SetFocus(overlay)

	stack.SetHidden(entry, true, false)
	if stack.Focused() != Component(root) {
		t.Fatalf("focus = %v, want the root while hidden", stack.Focused())
	}
	order := entry.focusOrder

	stack.SetHidden(entry, false, false)
	if stack.Focused() != Component(overlay) {
		t.Fatalf("focus = %v, want the overlay once shown again", stack.Focused())
	}
	if entry.focusOrder <= order {
		t.Fatalf("focus order = %d, want it bumped above %d", entry.focusOrder, order)
	}
}

// TestOverlayStackUnfocusMovesToTheTarget covers the handle's Unfocus: an overlay
// that holds focus gives it to the requested component, and the overlay's focus
// is not restored later.
func TestOverlayStackUnfocusMovesToTheTarget(t *testing.T) {
	stack, host, _ := newTestOverlayStack()
	overlay := mountedFocusComponent(host, "overlay")
	target := mountedFocusComponent(host, "target")

	entry := stack.Push(overlay, nil)
	stack.SetFocus(overlay)

	stack.Unfocus(entry, target, true)
	if stack.Focused() != Component(target) {
		t.Fatalf("focus = %v, want the requested target", stack.Focused())
	}
	stack.ReconcileFocus()
	if stack.Focused() != Component(target) {
		t.Fatalf("focus after reconcile = %v, want the target", stack.Focused())
	}
}

// TestOverlayStackQuerySurface covers the small queries the renderer and the
// overlay handle ask: stack contents, visibility, and whether an entry can take
// focus at all.
func TestOverlayStackQuerySurface(t *testing.T) {
	stack, host, _ := newTestOverlayStack()
	overlay := mountedFocusComponent(host, "overlay")

	if stack.HasEntries() || stack.HasVisible() {
		t.Fatal("an empty stack reports entries")
	}
	entry := stack.Push(overlay, nil)
	if !stack.HasEntries() || !stack.HasVisible() {
		t.Fatal("a pushed visible overlay is not reported")
	}
	if stack.Find(overlay) != entry {
		t.Fatal("Find did not return the pushed entry")
	}
	if stack.IsFocused() {
		t.Fatal("IsFocused is true before anything focuses the overlay")
	}
	if stack.Visible(entry) != true || stack.Visible(nil) != false {
		t.Fatalf("Visible(entry)=%v Visible(nil)=%v", stack.Visible(entry), stack.Visible(nil))
	}

	if !stack.Hide(entry) {
		t.Fatal("Hide reported a missing entry")
	}
	if stack.HasEntries() {
		t.Fatal("the stack still has entries after Hide")
	}
	if stack.Hide(entry) {
		t.Fatal("Hide reported success for an already removed entry")
	}
	if stack.FocusEntry(entry) {
		t.Fatal("FocusEntry focused a removed entry")
	}
}
