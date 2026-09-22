package tui

import (
	"context"
	"strings"
	"testing"
)

// Upstream dispatchMouseEvent returns a result untouched once it already
// carries a target ("if (\"target\" in result) return result"): nested
// dispatches own the target/focus resolution and outer levels must not
// re-stamp focus to themselves. The port overwrote FocusTarget at every
// nesting level, so a mouse press on the editor's autocomplete popup let the
// outermost container claim keyboard focus; tui.Container has no HandleInput,
// so every keystroke was dropped (fullscreen input box froze after clicking
// the suggestion list).

type dispatchProbeComponent struct {
	Component
	result *TuiMouseDispatchResult
}

func (c *dispatchProbeComponent) HandleMouse(event TuiMouseEvent) *TuiMouseDispatchResult {
	return c.result
}

// TestDispatchMouseEventPreservesNestedTarget pins upstream's early return:
// a result that already carries a target is returned as-is, without
// re-stamping FocusTarget to the outer component.
func TestDispatchMouseEventPreservesNestedTarget(t *testing.T) {
	inner := &dispatchProbeComponent{result: &TuiMouseDispatchResult{
		TuiMouseEventResult: TuiMouseEventResult{Handled: true, Focus: true},
	}}
	nested := DispatchMouseEvent(inner, TuiMouseEvent{X: 0, Y: 0, Width: 10, Height: 5, ScreenX: 0, ScreenY: 0})
	if nested == nil || nested.FocusTarget != Component(inner) {
		t.Fatal("nested dispatch did not stamp focus target")
	}

	outer := &dispatchProbeComponent{result: nested}
	result := DispatchMouseEvent(outer, TuiMouseEvent{X: 0, Y: 0, Width: 10, Height: 5, ScreenX: 0, ScreenY: 0})
	if result.FocusTarget != Component(inner) {
		t.Fatalf("outer dispatch re-stamped FocusTarget: got %T, want inner", result.FocusTarget)
	}
	if result.Target.Component != Component(inner) {
		t.Fatalf("outer dispatch re-stamped Target: got %T, want inner", result.Target.Component)
	}
}

// autocompleteMouseProvider feeds the editor popup.
type autocompleteMouseProvider struct{}

func (p *autocompleteMouseProvider) TriggerCharacters() []string { return []string{"/", "@", "#"} }

func (p *autocompleteMouseProvider) GetSuggestions(ctx context.Context, lines []string, cursorLine int, cursorCol int, force bool) *AutocompleteSuggestions {
	if len(lines) == 0 || !strings.HasPrefix(lines[0], "/mo") {
		return nil
	}
	return &AutocompleteSuggestions{
		Prefix: "/mo",
		Items: []AutocompleteItem{
			{Value: "/model", Description: "model"},
			{Value: "/mount", Description: "mount"},
		},
	}
}

func (p *autocompleteMouseProvider) ApplyCompletion(lines []string, line, col int, item AutocompleteItem, prefix string) CompletionResult {
	text := item.Value
	lines[0] = text
	return CompletionResult{Lines: lines, CursorLine: 0, CursorCol: len(text)}
}

// TestEditorAutocompleteMousePressFocusesInput pins the user-visible bug: a
// mouse press on the autocomplete popup must leave keyboard focus on a
// component that handles input (the editor), not on the suggestion list.
func TestEditorAutocompleteMousePressFocusesInput(t *testing.T) {
	host := &editorHost{rows: 10}
	editor := NewEditor(host, editorTestTheme(), EditorOptions{})
	editor.SetAutocompleteProvider(&autocompleteMouseProvider{})
	editor.SetText("/m")
	editor.HandleInput("o")

	// Dispatch through a container, mirroring the fullscreen chat document
	// chain where the outermost box re-stamped focus onto itself.
	container := &Container{}
	container.AddChild(editor)

	// Find the popup rows: the editor renders text lines then the list.
	lines := editor.Render(40)
	popupStart := -1
	for i, line := range lines {
		if strings.Contains(line, "/model") {
			popupStart = i
			break
		}
	}
	if popupStart < 0 {
		t.Fatalf("popup not rendered: %q", strings.Join(lines, "|"))
	}

	result := DispatchMouseEvent(container, TuiMouseEvent{
		Type: MousePress, Button: MouseButtonLeft,
		X: 2, Y: popupStart, Width: 40, Height: len(lines),
		ScreenX: 2, ScreenY: popupStart,
	})
	if result == nil {
		t.Fatal("popup press was not handled")
	}
	if result.FocusTarget != Component(editor) {
		t.Fatalf("focus target is %T, want the editor (keys would be dropped)", result.FocusTarget)
	}
}
