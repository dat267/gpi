package interactive

import (
	"github.com/dat267/pier/tui"
)

// Port of src/modes/interactive/chat-viewport.ts: the shared fullscreen
// transcript plus fixed input dock layout.

// ChatViewportOptions configure the viewport.
type ChatViewportOptions struct {
	Document        tui.Component
	PendingMessages tui.Component
	Status          tui.Component
	Editor          tui.Component
	Footer          tui.Component
	WidgetsAbove    tui.Component
	WidgetsBelow    tui.Component

	Scrollbar           tui.ScrollViewScrollbar
	ScrollbarTrackStyle func(text string) string
	ScrollbarThumbStyle func(text string) string
}

// ChatViewport is the composed transcript and dock.
type ChatViewport struct {
	Root       tui.Component
	Transcript *tui.ScrollView
}

// CreateChatViewport builds the shared fullscreen layout.
func CreateChatViewport(options ChatViewportOptions) ChatViewport {
	scrollbar := options.Scrollbar
	if scrollbar == "" {
		scrollbar = tui.ScrollbarAuto
	}
	transcript := tui.NewScrollView(options.Document, tui.ScrollViewOptions{
		Follow:              "end",
		Primary:             true,
		Overscroll:          "chain",
		Scrollbar:           scrollbar,
		ScrollbarTrackStyle: options.ScrollbarTrackStyle,
		ScrollbarThumbStyle: options.ScrollbarThumbStyle,
	})

	dockEntries := []tui.Component{options.PendingMessages, options.Status}
	if options.WidgetsAbove != nil {
		dockEntries = append(dockEntries, options.WidgetsAbove)
	}
	dockEntries = append(dockEntries, options.Editor)
	if options.WidgetsBelow != nil {
		dockEntries = append(dockEntries, options.WidgetsBelow)
	}
	dockEntries = append(dockEntries, options.Footer)

	dock := tui.NewVStack(nil, tui.StackOptions{})
	for _, entry := range dockEntries {
		shrink := 1
		entryOptions := tui.StackEntryOptions{Shrink: &shrink}
		if entry == options.Editor {
			// Upstream: { component: editor, shrink: 1, minSize: 3 } — the
			// editor keeps its top border, one input line, and bottom border
			// even when the dock is squeezed.
			minSize := 3
			entryOptions.MinSize = &minSize
		}
		dock.AddChild(entry, entryOptions)
	}

	root := tui.NewVStack(nil, tui.StackOptions{})
	zero := 0
	one := 1
	grow := 1
	root.AddChild(transcript, tui.StackEntryOptions{
		Basis:   tui.FixedBasis(0),
		Grow:    &grow,
		Shrink:  &one,
		MinSize: &one,
	})
	root.AddChild(dock, tui.StackEntryOptions{
		Basis:   tui.AutoBasis(),
		Grow:    &zero,
		Shrink:  &one,
		MinSize: &one,
	})

	return ChatViewport{Transcript: transcript, Root: root}
}
