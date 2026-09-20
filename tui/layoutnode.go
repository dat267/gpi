package tui

// Port of src/layout-node.ts: the layout-node registry used by the layout
// engine to describe stack and scroll containers.
//
// Upstream tags components with a symbol-keyed method and models the node as
// a TS union. Go uses a provider interface and a struct with a Kind
// discriminator (divergence D54).

// LayoutViewport is the available viewport for visibility checks.
type LayoutViewport struct {
	Width  int
	Height int
}

// Basis is a stack entry's size basis: either "auto" or a fixed size.
type Basis struct {
	Mode  string // "auto" or "fixed"
	Value int
}

// AutoBasis returns an auto basis.
func AutoBasis() *Basis { return &Basis{Mode: "auto"} }

// FixedBasis returns a fixed basis.
func FixedBasis(value int) *Basis { return &Basis{Mode: "fixed", Value: value} }

// StackLayoutEntry describes one stack child.
type StackLayoutEntry struct {
	Component Component
	Basis     *Basis // nil is treated as auto
	Grow      int
	Shrink    int
	MinSize   int
	MaxSize   int
	Visible   func(viewport LayoutViewport) bool
}

// StackLayoutNode is a vstack/hstack layout node.
type StackLayoutNode struct {
	Type    string // "vstack" | "hstack"
	Entries []StackLayoutEntry
	Gap     int
	Align   string // "stretch" | "start" | "center" | "end"
}

// ScrollLayoutState is the scroll-view state the layout engine drives.
type ScrollLayoutState interface {
	ScrollTop() int
	Primary() bool
	Overscroll() string // "chain" | "contain"
	ViewportHeight() int
	GetContentWidth(width int) int
	UpdateLayout(contentHeight int, viewportHeight int, requestRender func())
}

// ScrollLayoutNode is a scroll container layout node.
type ScrollLayoutNode struct {
	Type      string // "scroll"
	Component Component
	State     ScrollLayoutState
}

// LayoutNode is the union of the stack and scroll nodes.
type LayoutNode struct {
	Kind   string // "vstack" | "hstack" | "scroll"
	Stack  *StackLayoutNode
	Scroll *ScrollLayoutNode
}

// LayoutNodeProvider is implemented by components that participate in layout
// (upstream's LAYOUT_NODE-keyed method).
type LayoutNodeProvider interface {
	LayoutNode() LayoutNode
}

// GetLayoutNode returns the component's layout node, if it provides one.
func GetLayoutNode(component Component) (LayoutNode, bool) {
	provider, ok := component.(LayoutNodeProvider)
	if !ok {
		return LayoutNode{}, false
	}
	return provider.LayoutNode(), true
}
