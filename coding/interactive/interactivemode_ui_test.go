package interactive

import (
	"strings"
	"testing"

	"github.com/dat267/gpi/tui"
)

func newUIStateTestState(t *testing.T, embedStatus bool) (*InteractiveUIState, *CustomEditor) {
	t.Helper()
	SetCustomThemesDir(t.TempDir())
	SetRegisteredThemes(nil)
	SetTrueColorSupport(true)
	SetStyleColorsEnabled(true)
	InitTheme("dark", false)

	screen := tui.NewMainScreen(&fakeRendererTerminal{width: 80, height: 24}, false, "")
	state := NewInteractiveUIState(screen)
	state.StatusContainer = &tui.Container{}
	state.ChatContainer = &tui.Container{}
	state.WidgetContainerAbove = &tui.Container{}
	state.WidgetContainerBelow = &tui.Container{}
	state.FooterContainer = &tui.Container{}
	state.HeaderContainer = &tui.Container{}
	editor := NewCustomEditor(editorTestHost{}, tui.EditorTheme{}, NewAppKeybindingsManager(nil, ""), CustomEditorOptions{
		EmbedWorkingStatus: embedStatus,
	})
	state.Editor = editor
	state.DefaultEditor = editor
	return state, editor
}

func renderContainer(t *testing.T, container *tui.Container) []string {
	t.Helper()
	lines := container.Render(30)
	for index, line := range lines {
		lines[index] = strings.TrimRight(line, " ")
	}
	return lines
}

// TestUIStateWidgets covers the widget lifecycle and rendering.
func TestUIStateWidgets(t *testing.T) {
	state, _ := newUIStateTestState(t, false)

	// Empty above container keeps its spacer.
	state.RenderWidgets()
	if got := renderContainer(t, state.WidgetContainerAbove); len(got) != 1 || got[0] != "" {
		t.Fatalf("empty above = %v", got)
	}
	if got := renderContainer(t, state.WidgetContainerBelow); len(got) != 0 {
		t.Fatalf("empty below = %v", got)
	}

	// A string widget renders its lines with padding and a leading spacer.
	state.SetExtensionWidget("w1", []string{"one", "two"}, true, ExtensionWidgetOptions{})
	got := renderContainer(t, state.WidgetContainerAbove)
	if len(got) != 3 {
		t.Fatalf("above lines = %v", got)
	}
	if got[0] != "" || strings.TrimSpace(got[1]) != "one" || strings.TrimSpace(got[2]) != "two" {
		t.Fatalf("above = %v", got)
	}

	// A below-editor widget has no leading spacer.
	state.SetExtensionWidget("w2", []string{"below"}, true, ExtensionWidgetOptions{Placement: "belowEditor"})
	below := renderContainer(t, state.WidgetContainerBelow)
	if len(below) != 1 || strings.TrimSpace(below[0]) != "below" {
		t.Fatalf("below = %v", below)
	}
	if keys := state.WidgetKeysAbove(); len(keys) != 1 || keys[0] != "w1" {
		t.Fatalf("above keys = %v", keys)
	}
	if keys := state.WidgetKeysBelow(); len(keys) != 1 || keys[0] != "w2" {
		t.Fatalf("below keys = %v", keys)
	}

	// Truncation at the line limit adds the note.
	many := make([]string, 0, MaxWidgetLines+3)
	for i := 0; i < MaxWidgetLines+3; i++ {
		many = append(many, "line")
	}
	state.SetExtensionWidget("w3", many, true, ExtensionWidgetOptions{})
	rendered := strings.Join(renderContainer(t, state.WidgetContainerAbove), "\n")
	if !strings.Contains(rendered, "... (widget truncated)") {
		t.Fatalf("missing truncation note: %q", rendered)
	}
	// w1 + w3 above: spacer + w1 (2 lines) + w3 (10 lines + note).
	if count := strings.Count(rendered, "line"); count != MaxWidgetLines {
		t.Fatalf("truncated line count = %d", count)
	}

	// Replacing a widget removes the old one (same placement).
	state.SetExtensionWidget("w3", []string{"replaced"}, true, ExtensionWidgetOptions{})
	rendered = strings.Join(renderContainer(t, state.WidgetContainerAbove), "\n")
	if strings.Contains(rendered, "truncated") || !strings.Contains(rendered, "replaced") {
		t.Fatalf("replace failed: %q", rendered)
	}

	// Removing with hasContent=false and clearing everything.
	state.SetExtensionWidget("w3", nil, false, ExtensionWidgetOptions{})
	if keys := state.WidgetKeysAbove(); len(keys) != 1 || keys[0] != "w1" {
		t.Fatalf("above keys after remove = %v", keys)
	}
	state.ClearExtensionWidgets()
	if keys := state.WidgetKeysAbove(); len(keys) != 0 {
		t.Fatalf("above keys after clear = %v", keys)
	}
	if keys := state.WidgetKeysBelow(); len(keys) != 0 {
		t.Fatalf("below keys after clear = %v", keys)
	}
}

// TestUIStateStatus covers the status indicator transitions.
func TestUIStateStatus(t *testing.T) {
	state, editor := newUIStateTestState(t, true)

	working := NewWorkingStatusIndicator(state.UI, "Working...", nil, nil)
	state.ShowStatusIndicator(working)
	if state.ActiveStatusIndicator != working {
		t.Fatal("indicator not stored")
	}
	if !state.activeWorkingEmbedded {
		t.Fatal("indicator should be embedded")
	}
	if editor.GetWorkingStatusIndicator() != working {
		t.Fatal("indicator not set on the editor")
	}
	if len(state.StatusContainer.Children) != 0 {
		t.Fatal("status container should stay empty when embedded")
	}

	// A non-working kind is filtered out when a kind is given.
	state.ClearStatusIndicator(StatusRetry, true)
	if state.ActiveStatusIndicator == nil {
		t.Fatal("mismatched kind cleared the indicator")
	}

	// Clearing the working indicator removes it from the editor.
	state.ClearStatusIndicator(StatusWorking, true)
	if state.ActiveStatusIndicator != nil || editor.GetWorkingStatusIndicator() != nil {
		t.Fatal("indicator not cleared")
	}
	if len(state.StatusContainer.Children) != 0 {
		t.Fatal("idle status added after an embedded indicator")
	}

	// Without an embedding editor the indicator lands in the status container
	// and clearing it shows the idle status in regular mode with clear-on-shrink.
	plain, _ := newUIStateTestState(t, false)
	plain.TuiMode = "regular"
	plain.UI.SetClearOnShrink(true)
	indicator := NewWorkingStatusIndicator(plain.UI, "Working...", nil, nil)
	plain.ShowStatusIndicator(indicator)
	if len(plain.StatusContainer.Children) != 1 {
		t.Fatal("indicator not added to the container")
	}
	plain.ClearStatusIndicator(StatusWorking, true)
	if len(plain.StatusContainer.Children) != 1 {
		t.Fatal("idle status not added")
	}
	if _, ok := plain.StatusContainer.Children[0].(*IdleStatus); !ok {
		t.Fatalf("idle status type = %T", plain.StatusContainer.Children[0])
	}

	// clear-on-shrink off -> nothing added.
	noShrink, _ := newUIStateTestState(t, false)
	noShrink.TuiMode = "regular"
	noShrink.UI.SetClearOnShrink(false)
	noShrink.ShowStatusIndicator(NewWorkingStatusIndicator(noShrink.UI, "Working...", nil, nil))
	noShrink.ClearStatusIndicator(StatusWorking, true)
	if len(noShrink.StatusContainer.Children) != 0 {
		t.Fatal("idle status added with clear-on-shrink off")
	}

	// setWorkingVisible(false) clears the working indicator.
	visibleState, _ := newUIStateTestState(t, true)
	visibleState.ShowWorkingStatusIndicator("high")
	if visibleState.ActiveStatusIndicator == nil {
		t.Fatal("working indicator not shown")
	}
	visibleState.SetWorkingVisible(false, true)
	if visibleState.ActiveStatusIndicator != nil {
		t.Fatal("working indicator not cleared")
	}
	visibleState.SetWorkingVisible(true, true)
	if visibleState.ActiveStatusIndicator == nil || visibleState.ActiveStatusIndicator.Kind != StatusWorking {
		t.Fatal("working indicator not restored while streaming")
	}

	// setWorkingIndicator updates the active working indicator's options.
	options := &tui.LoaderIndicatorOptions{Frames: []string{"*"}, HasFrames: true, IntervalMS: 1000}
	visibleState.SetWorkingIndicator(options)
	if visibleState.WorkingIndicatorOptions != options {
		t.Fatal("options not stored")
	}
}

// TestUIStateHiddenThinkingLabel covers the label propagation.
func TestUIStateHiddenThinkingLabel(t *testing.T) {
	state, _ := newUIStateTestState(t, false)
	assistant := NewAssistantMessageComponent(nil, false, nil, "", 0, nil)
	state.ChatContainer.AddChild(assistant)
	state.SetHiddenThinkingLabel(strPtr("Hidden"))
	if assistant.hiddenThinkingLabel != "Hidden" {
		t.Fatalf("label = %q", assistant.hiddenThinkingLabel)
	}
	state.SetHiddenThinkingLabel(nil)
	if assistant.hiddenThinkingLabel != state.DefaultHiddenThinkingLabel {
		t.Fatalf("default label = %q", assistant.hiddenThinkingLabel)
	}
}

// TestUIStateFooterAndHeader covers the custom footer/header swapping.
func TestUIStateFooterAndHeader(t *testing.T) {
	state, _ := newUIStateTestState(t, false)
	state.Footer = NewFooterComponent(&fakeFooterSession{}, &fakeFooterData{statuses: map[string]string{}})
	state.SetExtensionFooter(nil)
	if len(state.FooterContainer.Children) != 1 || state.FooterContainer.Children[0] != tui.Component(state.Footer) {
		t.Fatal("built-in footer not restored")
	}
	custom := &staticRendererComponent{lines: []string{"custom footer"}}
	state.SetExtensionFooter(func() tui.Component { return custom })
	if len(state.FooterContainer.Children) != 1 || state.FooterContainer.Children[0] != tui.Component(custom) {
		t.Fatal("custom footer not installed")
	}
	state.SetExtensionFooter(nil)
	if state.FooterContainer.Children[0] != tui.Component(state.Footer) {
		t.Fatal("built-in footer not restored after custom")
	}

	builtIn := &staticRendererComponent{lines: []string{"header"}}
	state.BuiltInHeader = builtIn
	state.HeaderContainer.AddChild(builtIn)
	customHeader := NewExpandableText(func() string { return "collapsed" }, func() string { return "expanded" }, false, 0, 0)
	state.ToolOutputExpanded = true
	state.SetExtensionHeader(func() tui.Component { return customHeader })
	if len(state.HeaderContainer.Children) != 1 || state.HeaderContainer.Children[0] != tui.Component(customHeader) {
		t.Fatal("custom header not installed")
	}
	if lines := customHeader.Render(20); strings.TrimSpace(lines[0]) != "expanded" {
		t.Fatalf("custom header expansion = %v", lines)
	}
	state.SetExtensionHeader(nil)
	if state.HeaderContainer.Children[0] != tui.Component(builtIn) {
		t.Fatal("built-in header not restored")
	}
}

// TestUIStateInputListeners covers the terminal input listener registry.
func TestUIStateInputListeners(t *testing.T) {
	state, _ := newUIStateTestState(t, false)
	calls := 0
	unsubscribe := state.AddExtensionTerminalInputListener(func(data string) tui.TuiInputListenerResult {
		calls++
		return tui.TuiInputListenerResult{}
	})
	if len(state.inputSubscriptions) != 1 {
		t.Fatalf("subscriptions = %d", len(state.inputSubscriptions))
	}
	unsubscribe()
	if len(state.inputSubscriptions) != 0 {
		t.Fatalf("subscriptions after unsubscribe = %d", len(state.inputSubscriptions))
	}
	state.AddExtensionTerminalInputListener(func(string) tui.TuiInputListenerResult { return tui.TuiInputListenerResult{} })
	state.ClearExtensionTerminalInputListeners()
	if len(state.inputSubscriptions) != 0 {
		t.Fatal("subscriptions not cleared")
	}
}

// TestUIStateReset covers the reset surface.
func TestUIStateReset(t *testing.T) {
	state, _ := newUIStateTestState(t, true)
	state.FooterData = nil
	state.SetExtensionWidget("w", []string{"x"}, true, ExtensionWidgetOptions{})
	state.ShowWorkingStatusIndicator("off")
	state.SetHiddenThinkingLabel(strPtr("Custom"))
	state.ResetExtensionUI()
	if len(state.WidgetKeysAbove()) != 0 {
		t.Fatal("widgets not cleared")
	}
	if state.WorkingVisible != true || state.WorkingMessage != "" {
		t.Fatal("working state not reset")
	}
	if state.ActiveStatusIndicator == nil || state.ActiveStatusIndicator.Kind != StatusWorking {
		t.Fatal("working indicator lost")
	}
	if state.hiddenThinkingLabel != state.DefaultHiddenThinkingLabel {
		t.Fatalf("thinking label = %q", state.hiddenThinkingLabel)
	}
}
