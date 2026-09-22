package interactive

// defaultHiddenThinkingLabel is shown in place of a hidden thinking block.
const defaultHiddenThinkingLabel = "Thinking..."

// DisplayOptions is the single owner of the display values the renderers need:
// output padding, thinking-block visibility, the hidden-thinking label, and
// tool-output expansion. The transcript renderer, event dispatcher, run wiring,
// trust wiring and UI state all reference one value, so a settings change or a
// ctrl+o toggle updates every reader at once.
//
// Before this existed each struct kept its own copy, assigned by hand in
// NewApp: TranscriptRenderer.ToolOutputExpanded and
// EventDispatcher.ToolOutputExpanded were never assigned (newly created tool
// and custom-entry components always started collapsed), and
// RunWiring.OutputPad went stale on /reload.
type DisplayOptions struct {
	// OutputPad is the horizontal padding for status/error lines.
	OutputPad int
	// HideThinkingBlock hides assistant thinking blocks.
	HideThinkingBlock bool
	// HiddenThinkingLabel is shown in place of a hidden thinking block.
	HiddenThinkingLabel string
	// ToolOutputExpanded is the tool-output/header expansion state.
	ToolOutputExpanded bool
}
