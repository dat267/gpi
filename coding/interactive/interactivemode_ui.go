package interactive

import (
	"sort"

	"github.com/dat267/pier/coding"
	"github.com/dat267/pier/tui"
)

// Port of the extension-UI surface of src/modes/interactive/interactive-mode.ts
// (status indicators, widgets, custom footer/header and terminal input
// listeners). The extension runner itself is out of scope (D41), so this is a
// standalone state machine driven by the mode.
//
// Divergence D111: the extension widget/header factories receive the TUI and
// theme through function values (upstream's `(tui, thm) => Component`).

// MaxWidgetLines is the maximum total widget line count.
const MaxWidgetLines = 10

// InteractiveUIState holds the status/widget/footer/header state.
type InteractiveUIState struct {
	UI      tui.TUI
	TuiMode string

	FooterData *coding.FooterDataProvider
	Footer     *FooterComponent

	StatusContainer *tui.Container
	ChatContainer   *tui.Container

	WidgetContainerAbove *tui.Container
	WidgetContainerBelow *tui.Container
	FooterContainer      *tui.Container
	HeaderContainer      *tui.Container
	BuiltInHeader        tui.Component

	DefaultEditor      *CustomEditor
	Editor             tui.Component
	StreamingComponent *AssistantMessageComponent

	DefaultWorkingMessage      string
	DefaultHiddenThinkingLabel string

	WorkingMessage          string
	WorkingIndicatorOptions *tui.LoaderIndicatorOptions
	WorkingVisible          bool

	Display *DisplayOptions

	ActiveStatusIndicator StatusIndicatorLike

	activeWorkingEmbedded bool
	hiddenThinkingLabel   string
	extensionWidgetsAbove map[string]tui.Component
	extensionWidgetsBelow map[string]tui.Component
	customFooter          tui.Component
	customHeader          tui.Component

	inputSubscriptions map[int]func() // subscription id -> unsubscribe
	nextSubscription   int
}

// NewInteractiveUIState creates the state machine.
func NewInteractiveUIState(ui tui.TUI) *InteractiveUIState {
	return &InteractiveUIState{
		UI:                         ui,
		WorkingVisible:             true,
		extensionWidgetsAbove:      map[string]tui.Component{},
		extensionWidgetsBelow:      map[string]tui.Component{},
		inputSubscriptions:         map[int]func(){},
		DefaultWorkingMessage:      "Working",
		DefaultHiddenThinkingLabel: defaultHiddenThinkingLabel,
		Display:                    &DisplayOptions{},
	}
}

func (s *InteractiveUIState) requestRender() {
	if s.UI != nil {
		s.UI.RequestRender(false)
	}
}

// SetExtensionStatus sets or clears an extension status text.
func (s *InteractiveUIState) SetExtensionStatus(key string, text *string) {
	if s.FooterData != nil {
		s.FooterData.SetExtensionStatus(key, text)
	}
	if s.Footer != nil {
		s.Footer.Invalidate()
	}
	s.requestRender()
}

// SetEditorWorkingStatusIndicator sets the embedded status on the working
// status editor, reporting whether it was applied.
func (s *InteractiveUIState) SetEditorWorkingStatusIndicator(indicator *StatusIndicator) bool {
	if s.DefaultEditor != nil {
		s.DefaultEditor.SetWorkingStatusIndicator(nil)
	}
	editor, ok := IsWorkingStatusEditor(s.Editor)
	if !ok {
		return false
	}
	editor.SetWorkingStatusIndicator(indicator)
	return true
}

// ShowStatusIndicator shows a status indicator (embedded in the editor border
// when possible, otherwise in the status container).
func (s *InteractiveUIState) ShowStatusIndicator(indicator StatusIndicatorLike) {
	if s.ActiveStatusIndicator != nil {
		s.ActiveStatusIndicator.Dispose()
	}
	s.ActiveStatusIndicator = indicator
	s.activeWorkingEmbedded = false
	if s.StatusContainer != nil {
		s.StatusContainer.Clear()
	}
	s.SetEditorWorkingStatusIndicator(nil)
	if embedded, ok := indicator.(*StatusIndicator); ok && s.SetEditorWorkingStatusIndicator(embedded) {
		s.activeWorkingEmbedded = true
		return
	}
	if s.StatusContainer != nil {
		s.StatusContainer.AddChild(indicator)
	}
}

// ClearStatusIndicator clears the active indicator (optionally only when the
// kind matches). In regular mode with clear-on-shrink an idle status is shown.
func (s *InteractiveUIState) ClearStatusIndicator(kind StatusIndicatorKind, hasKind bool) {
	if hasKind && (s.ActiveStatusIndicator == nil || s.ActiveStatusIndicator.IndicatorKind() != kind) {
		return
	}
	cleared := s.ActiveStatusIndicator
	clearedWasEmbedded := s.activeWorkingEmbedded
	if cleared != nil {
		cleared.Dispose()
	}
	s.ActiveStatusIndicator = nil
	s.activeWorkingEmbedded = false
	if s.StatusContainer != nil {
		s.StatusContainer.Clear()
	}
	s.SetEditorWorkingStatusIndicator(nil)
	clearOnShrink := s.UI != nil && s.UI.GetClearOnShrink()
	if cleared != nil && !clearedWasEmbedded && s.TuiMode == "regular" && clearOnShrink {
		if s.StatusContainer != nil {
			s.StatusContainer.AddChild(&IdleStatus{})
		}
	}
}

// ShowWorkingStatusIndicator shows the working spinner.
func (s *InteractiveUIState) ShowWorkingStatusIndicator(thinkingLevel string) {
	var colorFn func(string) string
	if editor, ok := IsWorkingStatusEditor(s.Editor); ok {
		// Resolve the editor's border colour per render, the way upstream does
		// (text => (this.editor.borderColor ?? …)(text)). UpdateEditorBorderColor
		// swaps that colour whenever the thinking level, the model or bash mode
		// changes, so a captured function leaves the spinner and its message in
		// the previous colour while the border lines they sit between move on.
		colorFn = func(text string) string {
			if borderColor := editor.WorkingBorderColor(); borderColor != nil {
				return borderColor(text)
			}
			return ActiveTheme().GetThinkingBorderColor(thinkingLevel)(text)
		}
	}
	message := s.WorkingMessage
	if message == "" {
		message = s.DefaultWorkingMessage
	}
	s.ShowStatusIndicator(NewWorkingStatusIndicator(s.UI, message, s.WorkingIndicatorOptions, colorFn))
}

// SetWorkingVisible toggles the working indicator.
func (s *InteractiveUIState) SetWorkingVisible(visible bool, isStreaming bool) {
	s.WorkingVisible = visible
	if !visible {
		s.ClearStatusIndicator(StatusWorking, true)
		s.requestRender()
		return
	}
	if isStreaming && (s.ActiveStatusIndicator == nil || s.ActiveStatusIndicator.IndicatorKind() != StatusWorking) {
		s.ShowWorkingStatusIndicator("")
	}
	s.requestRender()
}

// SetWorkingIndicator updates the animation options.
func (s *InteractiveUIState) SetWorkingIndicator(options *tui.LoaderIndicatorOptions) {
	s.WorkingIndicatorOptions = options
	if s.ActiveStatusIndicator != nil && s.ActiveStatusIndicator.IndicatorKind() == StatusWorking {
		if working, ok := s.ActiveStatusIndicator.(*StatusIndicator); ok {
			working.SetIndicator(options)
		}
	}
	s.requestRender()
}

// SetHiddenThinkingLabel updates the hidden-thinking label everywhere.
func (s *InteractiveUIState) SetHiddenThinkingLabel(label *string) {
	if label == nil {
		s.hiddenThinkingLabel = s.DefaultHiddenThinkingLabel
	} else {
		s.hiddenThinkingLabel = *label
	}
	if s.ChatContainer != nil {
		for _, child := range s.ChatContainer.Children {
			if assistant, ok := child.(*AssistantMessageComponent); ok {
				assistant.SetHiddenThinkingLabel(s.hiddenThinkingLabel)
			}
		}
	}
	if s.StreamingComponent != nil {
		s.StreamingComponent.SetHiddenThinkingLabel(s.hiddenThinkingLabel)
	}
	s.requestRender()
}

// ExtensionWidgetOptions configure a widget.
type ExtensionWidgetOptions struct {
	Placement string // "aboveEditor" | "belowEditor"
}

// SetExtensionWidget sets a widget from string lines (nil removes it).
func (s *InteractiveUIState) SetExtensionWidget(key string, content []string, hasContent bool, options ExtensionWidgetOptions) {
	placement := options.Placement
	if placement == "" {
		placement = "aboveEditor"
	}
	removeExisting := func(widgets map[string]tui.Component) {
		if existing, ok := widgets[key]; ok {
			disposeComponent(existing)
			delete(widgets, key)
		}
	}
	removeExisting(s.extensionWidgetsAbove)
	removeExisting(s.extensionWidgetsBelow)

	if !hasContent {
		s.RenderWidgets()
		return
	}

	container := &tui.Container{}
	limit := len(content)
	if limit > MaxWidgetLines {
		limit = MaxWidgetLines
	}
	for _, line := range content[:limit] {
		container.AddChild(tui.NewText(line, 1, 0, nil))
	}
	if len(content) > MaxWidgetLines {
		container.AddChild(tui.NewText(ActiveTheme().Fg("muted", "... (widget truncated)"), 1, 0, nil))
	}

	target := s.extensionWidgetsAbove
	if placement == "belowEditor" {
		target = s.extensionWidgetsBelow
	}
	target[key] = container
	s.RenderWidgets()
}

// SetExtensionWidgetComponent sets a widget from a component factory.
func (s *InteractiveUIState) SetExtensionWidgetComponent(key string, component tui.Component, hasComponent bool, options ExtensionWidgetOptions) {
	placement := options.Placement
	if placement == "" {
		placement = "aboveEditor"
	}
	removeExisting := func(widgets map[string]tui.Component) {
		if existing, ok := widgets[key]; ok {
			disposeComponent(existing)
			delete(widgets, key)
		}
	}
	removeExisting(s.extensionWidgetsAbove)
	removeExisting(s.extensionWidgetsBelow)
	if !hasComponent {
		s.RenderWidgets()
		return
	}
	target := s.extensionWidgetsAbove
	if placement == "belowEditor" {
		target = s.extensionWidgetsBelow
	}
	target[key] = component
	s.RenderWidgets()
}

func disposeComponent(component tui.Component) {
	type disposable interface{ Dispose() }
	if value, ok := component.(disposable); ok {
		value.Dispose()
	}
}

// ClearExtensionWidgets disposes and removes all widgets.
func (s *InteractiveUIState) ClearExtensionWidgets() {
	for _, widget := range s.extensionWidgetsAbove {
		disposeComponent(widget)
	}
	for _, widget := range s.extensionWidgetsBelow {
		disposeComponent(widget)
	}
	s.extensionWidgetsAbove = map[string]tui.Component{}
	s.extensionWidgetsBelow = map[string]tui.Component{}
	s.RenderWidgets()
}

// WidgetKeysAbove returns the above-editor widget keys in insertion order
// (sorted for determinism; upstream preserves Map insertion order).
func (s *InteractiveUIState) WidgetKeysAbove() []string {
	return sortedWidgetKeys(s.extensionWidgetsAbove)
}

// WidgetKeysBelow returns the below-editor widget keys.
func (s *InteractiveUIState) WidgetKeysBelow() []string {
	return sortedWidgetKeys(s.extensionWidgetsBelow)
}

func sortedWidgetKeys(widgets map[string]tui.Component) []string {
	keys := make([]string, 0, len(widgets))
	for key := range widgets {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// RenderWidgets renders both widget containers.
func (s *InteractiveUIState) RenderWidgets() {
	if s.WidgetContainerAbove == nil || s.WidgetContainerBelow == nil {
		return
	}
	s.RenderWidgetContainer(s.WidgetContainerAbove, s.extensionWidgetsAbove, true, true)
	s.RenderWidgetContainer(s.WidgetContainerBelow, s.extensionWidgetsBelow, false, false)
	s.requestRender()
}

// RenderWidgetContainer renders one widget container.
func (s *InteractiveUIState) RenderWidgetContainer(container *tui.Container, widgets map[string]tui.Component, spacerWhenEmpty bool, leadingSpacer bool) {
	container.Clear()
	if len(widgets) == 0 {
		if spacerWhenEmpty {
			container.AddChild(tui.NewSpacer(1))
		}
		return
	}
	if leadingSpacer {
		container.AddChild(tui.NewSpacer(1))
	}
	for _, key := range sortedWidgetKeys(widgets) {
		container.AddChild(widgets[key])
	}
}

// SetExtensionFooter sets a custom footer or restores the built-in one.
func (s *InteractiveUIState) SetExtensionFooter(factory func() tui.Component) {
	if s.customFooter != nil {
		disposeComponent(s.customFooter)
	}
	if s.FooterContainer != nil {
		s.FooterContainer.Clear()
	}
	if factory != nil {
		s.customFooter = factory()
		if s.FooterContainer != nil {
			s.FooterContainer.AddChild(s.customFooter)
		}
	} else {
		s.customFooter = nil
		if s.FooterContainer != nil && s.Footer != nil {
			s.FooterContainer.AddChild(s.Footer)
		}
	}
	s.requestRender()
}

// SetExtensionHeader sets a custom header or restores the built-in one.
func (s *InteractiveUIState) SetExtensionHeader(factory func() tui.Component) {
	if s.BuiltInHeader == nil {
		return
	}
	if s.customHeader != nil {
		disposeComponent(s.customHeader)
	}
	current := s.customHeader
	if current == nil {
		current = s.BuiltInHeader
	}
	index := -1
	if s.HeaderContainer != nil {
		for i, child := range s.HeaderContainer.Children {
			if child == current {
				index = i
				break
			}
		}
	}

	if factory != nil {
		s.customHeader = factory()
		if expandable, ok := IsExpandable(s.customHeader); ok {
			expandable.SetExpanded(s.Display.ToolOutputExpanded)
		}
		if s.HeaderContainer != nil {
			if index != -1 {
				s.HeaderContainer.Children[index] = s.customHeader
			} else {
				s.HeaderContainer.Children = append([]tui.Component{s.customHeader}, s.HeaderContainer.Children...)
			}
		}
	} else {
		s.customHeader = nil
		if expandable, ok := IsExpandable(s.BuiltInHeader); ok {
			expandable.SetExpanded(s.Display.ToolOutputExpanded)
		}
		if s.HeaderContainer != nil && index != -1 {
			s.HeaderContainer.Children[index] = s.BuiltInHeader
		}
	}
	s.requestRender()
}

// AddExtensionTerminalInputListener registers a terminal input listener and
// returns an unsubscribe function.
func (s *InteractiveUIState) AddExtensionTerminalInputListener(handler tui.TuiInputListener) func() {
	if s.UI == nil {
		return func() {}
	}
	subscriptionID := s.nextSubscription
	s.nextSubscription++
	unsubscribe := s.UI.AddInputListener(handler)
	s.inputSubscriptions[subscriptionID] = unsubscribe
	return func() {
		unsubscribe()
		delete(s.inputSubscriptions, subscriptionID)
	}
}

// RebindExtensionTerminalInputListeners re-registers the listeners (used after
// a renderer swap).
func (s *InteractiveUIState) RebindExtensionTerminalInputListeners(handler tui.TuiInputListener) {
	for _, unsubscribe := range s.inputSubscriptions {
		unsubscribe()
	}
	s.inputSubscriptions = map[int]func(){}
	_ = handler
}

// ClearExtensionTerminalInputListeners removes all listeners.
func (s *InteractiveUIState) ClearExtensionTerminalInputListeners() {
	for _, unsubscribe := range s.inputSubscriptions {
		unsubscribe()
	}
	s.inputSubscriptions = map[int]func(){}
}

// ResetExtensionUI resets the extension-facing UI state.
func (s *InteractiveUIState) ResetExtensionUI() {
	if s.UI != nil {
		s.UI.HideOverlay()
	}
	s.ClearExtensionTerminalInputListeners()
	s.SetExtensionFooter(nil)
	s.SetExtensionHeader(nil)
	s.ClearExtensionWidgets()
	if s.FooterData != nil {
		s.FooterData.ClearExtensionStatuses()
	}
	if s.Footer != nil {
		s.Footer.Invalidate()
	}
	s.WorkingMessage = ""
	s.WorkingVisible = true
	s.SetWorkingIndicator(nil)
	if s.ActiveStatusIndicator != nil && s.ActiveStatusIndicator.IndicatorKind() == StatusWorking {
		if working, ok := s.ActiveStatusIndicator.(*StatusIndicator); ok {
			working.SetMessage(s.DefaultWorkingMessage + " (" + KeyText("app.interrupt") + " to interrupt)")
		}
	}
	s.SetHiddenThinkingLabel(nil)
}
