package tui

import (
	"strings"
	"sync"
	"testing"
)

// fakeTerminal is an in-memory Terminal for tests.
type fakeTerminal struct {
	width  int
	height int
	// mu guards writes: queries and render timers write from other goroutines.
	mu     sync.Mutex
	writes []string
	// onFlush observes FlushWrites for stop-ordering tests.
	onFlush func()
}

func (f *fakeTerminal) appendWrite(data string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writes = append(f.writes, data)
}

// hasWrite reports whether any recorded write contains the substring.
func (f *fakeTerminal) hasWrite(substring string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, write := range f.writes {
		if strings.Contains(write, substring) {
			return true
		}
	}
	return false
}

// writeCount returns the number of recorded writes.
func (f *fakeTerminal) writeCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.writes)
}

// joinedWrites returns all recorded writes concatenated.
func (f *fakeTerminal) joinedWrites() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return strings.Join(f.writes, "")
}

func (f *fakeTerminal) Start(onInput func(string), onResize func()) {}
func (f *fakeTerminal) Stop()                                       {}
func (f *fakeTerminal) DrainInput(maxMs int, idleMs int) error      { return nil }
func (f *fakeTerminal) Write(data string)                           { f.appendWrite(data) }
func (f *fakeTerminal) Columns() int                                { return f.width }
func (f *fakeTerminal) Rows() int                                   { return f.height }
func (f *fakeTerminal) KittyProtocolActive() bool                   { return false }
func (f *fakeTerminal) MoveBy(lines int)                            {}
func (f *fakeTerminal) HideCursor()                                 {}
func (f *fakeTerminal) ShowCursor()                                 {}
func (f *fakeTerminal) ClearLine()                                  {}
func (f *fakeTerminal) ClearFromCursor()                            {}
func (f *fakeTerminal) ClearScreen()                                {}
func (f *fakeTerminal) SetTitle(title string)                       {}
func (f *fakeTerminal) SetProgress(active bool)                     {}

func (f *fakeTerminal) FlushWrites() {
	f.mu.Lock()
	onFlush := f.onFlush
	f.mu.Unlock()
	if onFlush != nil {
		onFlush()
	}
}

// staticComponent renders fixed lines padded to the width.
type staticComponent struct {
	lines []string
}

func (s *staticComponent) Render(width int) []string {
	out := make([]string, 0, len(s.lines))
	for _, line := range s.lines {
		runes := []rune(line)
		if len(runes) >= width {
			out = append(out, string(runes[:width]))
		} else {
			out = append(out, line+strings.Repeat(" ", width-len(runes)))
		}
	}
	return out
}

func (s *staticComponent) Invalidate() {}

// focusComponent records focus transitions.
type focusComponent struct {
	staticComponent
	focused bool
}

func (f *focusComponent) SetFocused(focused bool) { f.focused = focused }
func (f *focusComponent) IsFocused() bool         { return f.focused }

// inputComponent records input.
type inputComponent struct {
	staticComponent
	input []string
}

func (i *inputComponent) HandleInput(data string) { i.input = append(i.input, data) }

// TestResolveOverlayLayout verifies the anchor/percentage/margin resolution
// against the upstream implementation (probed with Node).
func TestResolveOverlayLayout(t *testing.T) {
	width := func(v int) *int { return &v }
	row := func(v int) *int { return &v }

	cases := []struct {
		name      string
		options   *OverlayOptions
		height    int
		termW     int
		termH     int
		wantWidth int
		wantRow   int
		wantCol   int
	}{
		{"default", nil, 3, 40, 20, 40, 8, 0},
		{"width 50%", &OverlayOptions{PercentWidth: "50%"}, 3, 40, 20, 20, 8, 10},
		{"width 30 min 20", &OverlayOptions{Width: width(30), MinWidth: width(20)}, 3, 40, 20, 30, 8, 5},
		{"maxHeight 5", &OverlayOptions{MaxHeight: width(5)}, 10, 40, 20, 40, 7, 0},
		{"maxHeight 50%", &OverlayOptions{PercentMaxHeight: "50%"}, 30, 40, 20, 40, 5, 0},
		{"anchor top-left", &OverlayOptions{Anchor: OverlayAnchorTopLeft}, 3, 40, 20, 40, 0, 0},
		{"anchor bottom-right", &OverlayOptions{Anchor: OverlayAnchorBottomRight}, 3, 40, 20, 40, 17, 0},
		{"row 5 col 10", &OverlayOptions{Row: row(5), Col: row(10)}, 3, 40, 20, 40, 5, 0},
		{"row 25% col 50%", &OverlayOptions{RowPercent: "25%", ColPercent: "50%"}, 4, 40, 20, 40, 4, 0},
		{"margin", &OverlayOptions{Margin: OverlayMargin{Top: 2, Right: 3, Bottom: 1, Left: 4}}, 3, 40, 20, 33, 9, 4},
		{"offset", &OverlayOptions{Anchor: OverlayAnchorCenter, OffsetX: 2, OffsetY: -1}, 3, 40, 20, 40, 7, 0},
		{"clamp", &OverlayOptions{Row: row(999), Col: row(999)}, 3, 40, 20, 40, 17, 0},
		{"width overflow", &OverlayOptions{Width: width(100)}, 3, 40, 20, 40, 8, 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			layout := resolveOverlayLayout(tc.options, tc.height, tc.termW, tc.termH)
			if layout.Width != tc.wantWidth {
				t.Fatalf("width = %d, want %d", layout.Width, tc.wantWidth)
			}
			if layout.Row != tc.wantRow {
				t.Fatalf("row = %d, want %d", layout.Row, tc.wantRow)
			}
			if layout.Col != tc.wantCol {
				t.Fatalf("col = %d, want %d", layout.Col, tc.wantCol)
			}
		})
	}
}

// TestCompositeTuiLine verifies the exact strings produced by upstream.
func TestCompositeTuiLine(t *testing.T) {
	cases := []struct {
		name     string
		base     string
		overlay  string
		startCol int
		width    int
		total    int
		want     string
	}{
		{"plain", "abcdefghij", "XY", 3, 2, 10, "abc" + segmentReset + "XY" + segmentReset + "fghij"},
		{"styled", "\x1b[31mabcdefghij\x1b[0m", "XY", 3, 2, 10,
			"\x1b[31mabc" + segmentReset + "XY" + segmentReset + "\x1b[31mfghij"},
		{"wide", "日本語日本語", "XX", 2, 2, 10, "日" + segmentReset + "XX" + segmentReset + "語日本"},
		{"overflow clipped", "abcdef", "WXYZ", 2, 4, 6, "ab" + segmentReset + "WXYZ" + segmentReset},
		{"image passthrough", "\x1b_Gimg", "XY", 0, 2, 10, "\x1b_Gimg"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := CompositeTuiLine(tc.base, tc.overlay, tc.startCol, tc.width, tc.total); got != tc.want {
				t.Fatalf("got  %q\nwant %q", got, tc.want)
			}
		})
	}
}

// TestCompositeOverlays verifies overlay compositing against upstream: content
// is padded to the terminal height, overlays are placed at the resolved
// rectangle, and the bounds are recorded.
func TestCompositeOverlays(t *testing.T) {
	terminal := &fakeTerminal{width: 20, height: 6}
	renderer := NewRenderer(terminal)
	renderer.AddChild(&staticComponent{lines: []string{"base-line-1", "base-line-2"}})
	renderer.ShowOverlay(&staticComponent{lines: []string{"OVR1", "OVR2"}},
		&OverlayOptions{Width: intPtr(8), Anchor: OverlayAnchorTopLeft})

	out := renderer.CompositeOverlays([]string{"base-line-1", "base-line-2"}, 20, 6)
	if len(out) != 6 {
		t.Fatalf("lines = %d, want 6", len(out))
	}
	wantFirst := segmentReset + "OVR1    " + segmentReset + "e-1         "
	if out[0] != wantFirst {
		t.Fatalf("line 0 = %q\nwant %q", out[0], wantFirst)
	}
	wantSecond := segmentReset + "OVR2    " + segmentReset + "e-2         "
	if out[1] != wantSecond {
		t.Fatalf("line 1 = %q\nwant %q", out[1], wantSecond)
	}
	for i := 2; i < 6; i++ {
		if out[i] != "" {
			t.Fatalf("line %d = %q, want empty", i, out[i])
		}
	}

	handle := renderer.overlayStack[0]
	if !handle.hasBounds {
		t.Fatal("bounds not recorded")
	}
	if *handle.bounds != (OverlayBounds{Row: 0, Col: 0, Width: 8, Height: 2}) {
		t.Fatalf("bounds = %+v", *handle.bounds)
	}
	if !renderer.HasOverlay() {
		t.Fatal("HasOverlay must be true")
	}
}

// TestContainerRenderAndMouse verifies child layout and hit testing.
func TestContainerRenderAndMouse(t *testing.T) {
	first := &staticComponent{lines: []string{"aaa", "bbb"}}
	second := &staticComponent{lines: []string{"ccc"}}
	container := &Container{}
	container.AddChild(first)
	container.AddChild(second)

	lines := container.Render(3)
	if strings.Join(lines, "|") != "aaa|bbb|ccc" {
		t.Fatalf("lines = %q", lines)
	}

	// A hit on the second child's row is forwarded with local coordinates.
	hit := &mouseRecordingComponent{staticComponent: staticComponent{lines: []string{"ccc"}}}
	container.RemoveChild(second)
	container.AddChild(hit)
	container.Render(3)

	event := TuiMouseEvent{Type: MousePress, Button: MouseButtonLeft, Y: 2, Height: 3, Width: 3, ScreenY: 2}
	result := container.HandleMouse(event)
	if result == nil || result.Target.Component != hit {
		t.Fatalf("dispatch failed: %+v", result)
	}
	if hit.last.Y != 0 {
		t.Fatalf("local y = %d, want 0", hit.last.Y)
	}

	// Out-of-bounds events are dropped.
	if got := container.HandleMouse(TuiMouseEvent{Y: 99, Height: 3, Width: 3}); got != nil {
		t.Fatalf("out of bounds = %+v", got)
	}
}

type mouseRecordingComponent struct {
	staticComponent
	last TuiMouseEvent
}

func (m *mouseRecordingComponent) HandleMouse(event TuiMouseEvent) *TuiMouseDispatchResult {
	m.last = event
	return &TuiMouseDispatchResult{TuiMouseEventResult: TuiMouseEventResult{Handled: true}}
}

// TestRendererFocus verifies focus flags and focus changes.
func TestRendererFocus(t *testing.T) {
	renderer := NewRenderer(&fakeTerminal{width: 20, height: 6})
	first := &focusComponent{}
	second := &focusComponent{}
	renderer.AddChild(first)
	renderer.AddChild(second)

	renderer.SetFocus(first)
	if !first.focused || second.focused {
		t.Fatal("focus not applied")
	}
	renderer.SetFocus(second)
	if first.focused || !second.focused {
		t.Fatal("focus not transferred")
	}
	renderer.SetFocus(nil)
	if second.focused {
		t.Fatal("focus not cleared")
	}
	if renderer.GetFocusedComponent() != nil {
		t.Fatal("focused component not nil")
	}
}

// TestRendererInputRouting verifies listeners, the debug key, and forwarding.
func TestRendererInputRouting(t *testing.T) {
	renderer := NewRenderer(&fakeTerminal{width: 20, height: 6})
	target := &inputComponent{}
	renderer.AddChild(target)
	renderer.SetFocus(target)

	var listened []string
	remove := renderer.AddInputListener(func(data string) TuiInputListenerResult {
		listened = append(listened, data)
		return TuiInputListenerResult{Data: strings.ToUpper(data), HasData: true}
	})

	renderer.HandleTerminalInput("x")
	if strings.Join(target.input, ",") != "X" {
		t.Fatalf("input = %q", target.input)
	}

	// A consuming listener stops the propagation.
	renderer.AddInputListener(func(data string) TuiInputListenerResult {
		return TuiInputListenerResult{Consume: true}
	})
	renderer.HandleTerminalInput("y")
	if strings.Join(target.input, ",") != "X" {
		t.Fatalf("consumed input leaked: %q", target.input)
	}

	// Removing the transform listener restores the raw data.
	remove()
	renderer.RemoveInputListener(renderer.inputListeners[0])
	renderer.HandleTerminalInput("z")
	if target.input[len(target.input)-1] != "z" {
		t.Fatalf("input = %q", target.input)
	}
	if len(listened) == 0 {
		t.Fatal("listener not called")
	}

	// The debug key fires the debug callback instead of the component.
	debug := 0
	renderer.OnDebug = func() { debug++ }
	renderer.MatchesDebugKey = func(data string) bool { return data == "debug" }
	renderer.HandleTerminalInput("debug")
	if debug != 1 {
		t.Fatalf("debug = %d", debug)
	}
	before := len(target.input)
	renderer.HandleTerminalInput("debug")
	if len(target.input) != before {
		t.Fatal("debug key leaked to component")
	}

	// Key-release events are filtered unless the component opts in.
	renderer.MatchesDebugKey = nil
	release := "\x1b[97;1:3u"
	renderer.HandleTerminalInput(release)
	if len(target.input) != before {
		t.Fatal("key release leaked")
	}
	wanter := &releaseComponent{inputComponent: inputComponent{}}
	renderer.AddChild(wanter)
	renderer.SetFocus(wanter)
	renderer.HandleTerminalInput(release)
	if len(wanter.input) != 1 {
		t.Fatalf("opted-in release = %q", wanter.input)
	}

	// Bracketed paste containing ":3" is not a release event.
	if IsKeyRelease("\x1b[200~90:62:3F:A5\x1b[201~") {
		t.Fatal("bracketed paste treated as key release")
	}
	if !IsKeyRelease("\x1b[97;1:3u") {
		t.Fatal("release not detected")
	}
	if !IsKeyRepeat("\x1b[97;1:2u") {
		t.Fatal("repeat not detected")
	}
}

type releaseComponent struct {
	inputComponent
}

func (r *releaseComponent) WantsKeyRelease() bool { return true }

// TestRendererScheduling verifies RenderNow and the throttle window.
func TestRendererScheduling(t *testing.T) {
	renderer := NewRenderer(&fakeTerminal{width: 20, height: 6})
	renders := 0
	renderer.DoRender = func() { renders++ }

	renderer.RenderNow(false)
	if renders != 1 {
		t.Fatalf("renders = %d", renders)
	}
	// A plain request schedules a timer but does not render synchronously.
	renderer.RequestRender(false)
	if renders != 1 {
		t.Fatalf("synchronous render: %d", renders)
	}
	renderer.RenderNow(false)
	if renders != 2 {
		t.Fatalf("renders = %d", renders)
	}
}

// TestApplyLineResets verifies reset appending and image pass-through.
func TestApplyLineResets(t *testing.T) {
	renderer := NewRenderer(&fakeTerminal{width: 20, height: 6})
	lines := []string{"plain", "\x1b_Gimage", "with\ttab"}
	out := renderer.ApplyLineResets(lines)
	want := []string{"plain" + segmentReset, "\x1b_Gimage", "with   tab" + segmentReset}
	for i := range want {
		if out[i] != want[i] {
			t.Fatalf("line %d = %q, want %q", i, out[i], want[i])
		}
	}
}

// TestExtractCursorPosition verifies marker positioning and stripping.
func TestExtractCursorPosition(t *testing.T) {
	renderer := NewRenderer(&fakeTerminal{width: 20, height: 6})
	lines := []string{"top", "ab" + CursorMarker + "cd", "日本語" + CursorMarker}
	row, col, ok := renderer.ExtractCursorPosition(lines, 6)
	if !ok || row != 2 || col != 6 {
		t.Fatalf("cursor = (%d,%d,%v), want (2,6,true)", row, col, ok)
	}
	if lines[2] != "日本語" {
		t.Fatalf("marker not stripped: %q", lines[2])
	}

	// Only the bottom `height` lines are scanned.
	lines = []string{"a" + CursorMarker, "bottom"}
	if _, _, ok := renderer.ExtractCursorPosition(lines, 1); ok {
		t.Fatal("marker outside the viewport must be ignored")
	}
}

// TestNormalizeTerminalOutput verifies the Thai/Lao AM decomposition and tab
// expansion (tabs inside escape sequences stay untouched).
func TestNormalizeTerminalOutput(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"plain", "plain"},
		{"a\tb", "a   b"},
		{"\x1b[31m\t\x1b[0m", "\x1b[31m   \x1b[0m"},
		{"\u0e33", "\u0e4d\u0e32"},
		{"\u0eb3", "\u0ecd\u0eb2"},
		{"\u0e01\u0e33", "\u0e01\u0e4d\u0e32"},
	}
	for _, tc := range cases {
		if got := NormalizeTerminalOutput(tc.in); got != tc.want {
			t.Fatalf("NormalizeTerminalOutput(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestOverlayVisibility verifies hidden overlays, the visible callback, and
// focus restoration on hide.
func TestOverlayVisibility(t *testing.T) {
	terminal := &fakeTerminal{width: 40, height: 20}
	renderer := NewRenderer(terminal)
	base := &focusComponent{}
	renderer.AddChild(base)
	renderer.SetFocus(base)

	overlay := &focusComponent{}
	visible := true
	handle := renderer.ShowOverlay(overlay, &OverlayOptions{
		Visible: func(int, int) bool { return visible },
	})

	// Showing an overlay captures focus.
	if renderer.GetFocusedComponent() != Component(overlay) || !overlay.focused {
		t.Fatal("overlay did not capture focus")
	}
	if !renderer.HasOverlay() || !renderer.IsOverlayFocused() {
		t.Fatal("overlay state wrong")
	}

	// Hiding returns focus to the previous component.
	handle.Hide()
	if renderer.GetFocusedComponent() != Component(base) || !base.focused {
		t.Fatal("focus not restored")
	}
	if renderer.HasOverlay() {
		t.Fatal("overlay still present")
	}

	// A non-capturing overlay does not take focus.
	nonCapturing := &focusComponent{}
	second := renderer.ShowOverlay(nonCapturing, &OverlayOptions{NonCapturing: true})
	if renderer.GetFocusedComponent() != Component(base) {
		t.Fatal("non-capturing overlay took focus")
	}
	second.Hide()

	// An invisible overlay is excluded from HasOverlay and focus.
	visible = false
	hidden := &focusComponent{}
	third := renderer.ShowOverlay(hidden, &OverlayOptions{Visible: func(int, int) bool { return visible }})
	if renderer.HasOverlay() {
		t.Fatal("invisible overlay counted")
	}
	if renderer.GetFocusedComponent() != Component(base) {
		t.Fatal("invisible overlay took focus")
	}
	third.Hide()

	// HideOverlay removes the topmost entry and restores focus.
	top := &focusComponent{}
	renderer.ShowOverlay(top, nil)
	renderer.HideOverlay()
	if renderer.GetFocusedComponent() != Component(base) {
		t.Fatal("HideOverlay focus restore failed")
	}
}

// TestOverlayBoundsAndGetBounds verifies handle bounds reporting.
func TestOverlayBoundsAndGetBounds(t *testing.T) {
	renderer := NewRenderer(&fakeTerminal{width: 20, height: 6})
	renderer.AddChild(&staticComponent{lines: []string{"base"}})
	overlay := &staticComponent{lines: []string{"X"}}
	handle := renderer.ShowOverlay(overlay, &OverlayOptions{Width: intPtr(6), Anchor: OverlayAnchorTopLeft})

	if _, ok := handle.GetBounds(); ok {
		t.Fatal("bounds available before a composited render")
	}
	renderer.CompositeOverlays([]string{"base"}, 20, 6)
	bounds, ok := handle.GetBounds()
	if !ok || bounds != (OverlayBounds{Row: 0, Col: 0, Width: 6, Height: 1}) {
		t.Fatalf("bounds = %+v ok=%v", bounds, ok)
	}
	handle.SetHidden(true)
	if _, ok := handle.GetBounds(); ok {
		t.Fatal("hidden overlay reported bounds")
	}
	handle.SetHidden(false)
	if handle.IsHidden() {
		t.Fatal("IsHidden wrong")
	}
	handle.Focus()
	if !handle.IsFocused() {
		t.Fatal("Focus() did not focus")
	}
	handle.Unfocus(nil, false)
	if handle.IsFocused() {
		t.Fatal("Unfocus() did not release focus")
	}
}

func intPtr(v int) *int { return &v }

// TestRendererStopFlushesAfterThePostStopHook pins the exit ordering: the
// post-stop hook queues the alt-screen exit on the async writer, and a caller
// that then writes the resume hint straight to stdout would race it. Stop must
// flush after the hook, so the screen is restored before the hint lands on it
// (the mangled resume-hint bug).
func TestRendererStopFlushesAfterThePostStopHook(t *testing.T) {
	terminal := &fakeTerminal{width: 80, height: 24}
	var order []string
	terminal.onFlush = func() { order = append(order, "flush") }

	renderer := NewRenderer(terminal)
	renderer.OnAfterTerminalStop = func(TuiStopOptions) {
		order = append(order, "hook")
		renderer.Terminal.Write("exit-sequence")
	}
	renderer.Stop(TuiStopOptions{PreserveScreen: true})

	if len(order) != 2 || order[0] != "hook" || order[1] != "flush" {
		t.Fatalf("stop order = %v, want [hook flush]", order)
	}
	if !terminal.hasWrite("exit-sequence") {
		t.Fatal("the post-stop hook's write was not delivered")
	}
}
