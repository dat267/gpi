package tui

// Port of the overlay stack and its focus-restore state machine (upstream's
// TuiBase overlay/focus surface; D55 covers the childrenHolder divergence in the
// mount check).
//
// The stack owns the entries, the focused component, the focus order and the
// inactive|eligible|blocked restore union. The two things the machine cannot
// answer itself — whether an entry is visible on the current terminal, and
// whether a component is still mounted — arrive through the unexported
// overlayHost seam, so the machine is testable with no renderer and no terminal.
// The renderer keeps its public overlay methods and forwards here.

// overlayHost is the renderer's half of the machine's questions. The methods are
// unexported so the renderer needs no new public surface to satisfy it.
type overlayHost interface {
	// overlayVisible answers the terminal-dependent half of visibility: an
	// entry's Visible callback, given the current terminal size. The stack
	// handles the hidden flag and the nil cases itself.
	overlayVisible(entry *overlayEntry) bool
	// componentMounted reports whether a component is still attached to the
	// mounted tree.
	componentMounted(component Component) bool
}

type overlayEntry struct {
	component  Component
	options    *OverlayOptions
	preFocus   Component
	hidden     bool
	focusOrder int
	bounds     *OverlayBounds
	hasBounds  bool
}

// overlayFocusRestoreState models upstream's inactive|eligible|blocked union.
type overlayFocusRestoreState struct {
	status   string // "inactive" | "eligible" | "blocked"
	overlay  *overlayEntry
	blocked  Component
	resumeTo bool // true = restore-overlay, false = focus-target
	target   Component
}

type overlayStack struct {
	host       overlayHost
	entries    []*overlayEntry
	focus      Component
	focusOrder int
	restore    overlayFocusRestoreState
}

func newOverlayStack(host overlayHost) *overlayStack {
	return &overlayStack{host: host, restore: overlayFocusRestoreState{status: "inactive"}}
}

// ---- queries ----

// Focused returns the component that holds keyboard focus.
func (s *overlayStack) Focused() Component { return s.focus }

// Entries returns the stack in paint order (bottom first).
func (s *overlayStack) Entries() []*overlayEntry { return s.entries }

// HasEntries reports whether the overlay stack is non-empty.
func (s *overlayStack) HasEntries() bool { return len(s.entries) > 0 }

// HasVisible reports whether any overlay is visible.
func (s *overlayStack) HasVisible() bool {
	for _, entry := range s.entries {
		if s.Visible(entry) {
			return true
		}
	}
	return false
}

// IsFocused reports whether the focused component is a visible overlay.
func (s *overlayStack) IsFocused() bool {
	entry := s.Find(s.focus)
	return entry != nil && s.Visible(entry)
}

// Find returns the entry for a component, or nil when it is not an overlay.
func (s *overlayStack) Find(component Component) *overlayEntry {
	if component == nil {
		return nil
	}
	for _, entry := range s.entries {
		if entry.component == component {
			return entry
		}
	}
	return nil
}

// IsOverlayComponent reports whether a component is an overlay in the stack.
func (s *overlayStack) IsOverlayComponent(component Component) bool {
	return s.Find(component) != nil
}

// Visible reports whether an entry is currently visible: not hidden, and its
// own Visible callback satisfied for the current terminal.
func (s *overlayStack) Visible(entry *overlayEntry) bool {
	if entry == nil || entry.hidden {
		return false
	}
	if entry.options != nil && entry.options.Visible != nil {
		return s.host.overlayVisible(entry)
	}
	return true
}

// TopmostVisible finds the visual-frontmost visible capturing overlay, if any.
func (s *overlayStack) TopmostVisible() *overlayEntry {
	var topmost *overlayEntry
	for _, overlay := range s.entries {
		if overlay.options != nil && overlay.options.NonCapturing {
			continue
		}
		if !s.Visible(overlay) {
			continue
		}
		if topmost == nil || overlay.focusOrder > topmost.focusOrder {
			topmost = overlay
		}
	}
	return topmost
}

// Contains reports whether an entry is still in the stack.
func (s *overlayStack) Contains(entry *overlayEntry) bool { return s.indexOf(entry) != -1 }

func (s *overlayStack) indexOf(entry *overlayEntry) int {
	for i, existing := range s.entries {
		if existing == entry {
			return i
		}
	}
	return -1
}

// ---- focus ----

// SetFocus sets keyboard focus.
func (s *overlayStack) SetFocus(component Component) {
	s.setFocus(component, "clear")
}

// setFocus is upstream's setFocus(component, overlayFocusRestore) with its
// restore modes: "clear" drops a pending restore, "preserve" keeps it.
func (s *overlayStack) setFocus(component Component, restoreMode string) {
	previousFocus := s.focus
	nextFocus := component

	previousFocusedOverlay := (*overlayEntry)(nil)
	if previousFocus != nil {
		if entry := s.Find(previousFocus); entry != nil && s.Visible(entry) {
			previousFocusedOverlay = entry
		}
	}
	nextFocusIsOverlay := nextFocus != nil && s.IsOverlayComponent(nextFocus)
	restoreStatus, restoreOverlay, restoreBlockedBy, restoreResumeTo, restoreTarget := s.visibleRestore()

	if nextFocus != nil && !nextFocusIsOverlay {
		if restoreStatus == "blocked" && restoreBlockedBy == previousFocus {
			if !restoreResumeTo || !s.host.componentMounted(restoreBlockedBy) {
				nextFocus = s.resolveBlockedResume(restoreOverlay, restoreResumeTo, restoreTarget)
			} else {
				s.restore = overlayFocusRestoreState{
					status: "blocked", overlay: restoreOverlay, blocked: nextFocus,
					resumeTo: restoreResumeTo, target: restoreTarget,
				}
			}
		} else if previousFocusedOverlay != nil && restoreStatus != "inactive" &&
			restoreOverlay == previousFocusedOverlay && !s.IsFocusAncestor(previousFocusedOverlay, nextFocus) {
			s.restore = overlayFocusRestoreState{
				status: "blocked", overlay: previousFocusedOverlay, blocked: nextFocus, resumeTo: true,
			}
		}
	} else if nextFocus == nil {
		if restoreStatus == "blocked" && restoreBlockedBy == previousFocus {
			nextFocus = s.resolveBlockedResume(restoreOverlay, restoreResumeTo, restoreTarget)
		} else if restoreMode == "clear" {
			s.ClearRestore()
		}
	}

	if focusable, ok := s.focus.(Focusable); ok && s.focus != nil {
		focusable.SetFocused(false)
	}

	s.focus = nextFocus

	if focusable, ok := nextFocus.(Focusable); ok && nextFocus != nil {
		focusable.SetFocused(true)
	}

	if nextFocus != nil {
		if entry := s.Find(nextFocus); entry != nil && s.Visible(entry) {
			s.restore = overlayFocusRestoreState{status: "eligible", overlay: entry}
		}
	}
}

// ReconcileFocus applies the restore rules before input is dispatched: a focused
// overlay that stopped being visible hands focus back, and a pending restore
// either resumes or is abandoned.
func (s *overlayStack) ReconcileFocus() {
	// If the focused component is an overlay, verify it is still visible
	// (visibility can change due to terminal resize or a visible() callback).
	focusedOverlay := s.Find(s.focus)
	if focusedOverlay != nil && !s.Visible(focusedOverlay) {
		if topVisible := s.TopmostVisible(); topVisible != nil {
			s.setFocus(topVisible.component, "clear")
		} else {
			s.setFocus(focusedOverlay.preFocus, "preserve")
		}
	}

	if s.IsOverlayComponent(s.focus) {
		return
	}
	status, overlay, blockedBy, resumeTo, target := s.visibleRestore()
	switch status {
	case "eligible":
		s.setFocus(overlay.component, "clear")
	case "blocked":
		if blockedBy != s.focus {
			if resumeTo {
				s.setFocus(overlay.component, "clear")
			} else {
				s.ClearRestore()
				s.setFocus(target, "clear")
			}
		}
	}
}

// ClearRestore drops a pending focus restore.
func (s *overlayStack) ClearRestore() {
	s.restore = overlayFocusRestoreState{status: "inactive"}
}

func (s *overlayStack) clearRestoreFor(overlay *overlayEntry) {
	if s.restore.status != "inactive" && s.restore.overlay == overlay {
		s.ClearRestore()
	}
}

func (s *overlayStack) visibleRestore() (string, *overlayEntry, Component, bool, Component) {
	state := s.restore
	if state.status == "inactive" {
		return "inactive", nil, nil, false, nil
	}
	if s.indexOf(state.overlay) == -1 || !s.Visible(state.overlay) {
		return "inactive", nil, nil, false, nil
	}
	return state.status, state.overlay, state.blocked, state.resumeTo, state.target
}

func (s *overlayStack) resolveBlockedResume(overlay *overlayEntry, resumeTo bool, target Component) Component {
	if overlay != nil && resumeTo {
		return overlay.component
	}
	s.ClearRestore()
	return target
}

// IsFocusAncestor reports whether a component is an ancestor of an overlay's
// pre-focus chain, following the overlays that were focused before it.
func (s *overlayStack) IsFocusAncestor(entry *overlayEntry, component Component) bool {
	visited := map[Component]bool{}
	current := entry.preFocus
	for current != nil && !visited[current] {
		visited[current] = true
		if current == component {
			return true
		}
		if parent := s.Find(current); parent != nil {
			current = parent.preFocus
		} else {
			current = nil
		}
	}
	return false
}

// ---- overlay lifecycle ----

// Push adds an overlay and returns its entry, recording what had focus.
func (s *overlayStack) Push(component Component, options *OverlayOptions) *overlayEntry {
	s.focusOrder++
	entry := &overlayEntry{
		component:  component,
		options:    options,
		preFocus:   s.focus,
		focusOrder: s.focusOrder,
	}
	s.entries = append(s.entries, entry)
	return entry
}

func (s *overlayStack) bumpFocusOrder(entry *overlayEntry) {
	s.focusOrder++
	entry.focusOrder = s.focusOrder
}

// retargetPreFocus re-points the overlays that saved a removed overlay as their
// pre-focus, so they fall back to what it had saved.
func (s *overlayStack) retargetPreFocus(removed *overlayEntry) {
	for _, overlay := range s.entries {
		if overlay != removed && overlay.preFocus == removed.component {
			overlay.preFocus = removed.preFocus
		}
	}
}

// refocusAfterRemoval hands focus on when the removed overlay held it.
func (s *overlayStack) refocusAfterRemoval(entry *overlayEntry) {
	if s.focus != entry.component {
		return
	}
	if topVisible := s.TopmostVisible(); topVisible != nil {
		s.setFocus(topVisible.component, "clear")
	} else {
		s.setFocus(entry.preFocus, "clear")
	}
}

// Hide removes an entry, dropping its pending restore and re-pointing the
// overlays that depended on it. It reports whether the entry was in the stack.
func (s *overlayStack) Hide(entry *overlayEntry) bool {
	index := s.indexOf(entry)
	if index == -1 {
		return false
	}
	s.clearRestoreFor(entry)
	s.retargetPreFocus(entry)
	s.entries = append(s.entries[:index], s.entries[index+1:]...)
	s.refocusAfterRemoval(entry)
	return true
}

// HideTop removes the topmost overlay and returns it, or nil when empty.
func (s *overlayStack) HideTop() *overlayEntry {
	if len(s.entries) == 0 {
		return nil
	}
	overlay := s.entries[len(s.entries)-1]
	s.clearRestoreFor(overlay)
	s.retargetPreFocus(overlay)
	s.entries = s.entries[:len(s.entries)-1]
	s.refocusAfterRemoval(overlay)
	return overlay
}

// SetHidden hides or shows an entry, with the focus consequences: hiding the
// focused overlay hands focus on, and showing a capturing overlay takes it.
func (s *overlayStack) SetHidden(entry *overlayEntry, hidden bool, nonCapturing bool) bool {
	if entry.hidden == hidden {
		return false
	}
	entry.hidden = hidden
	if hidden {
		s.clearRestoreFor(entry)
		if s.focus == entry.component {
			s.refocusAfterRemoval(entry)
		}
	} else if !nonCapturing && s.Visible(entry) {
		s.bumpFocusOrder(entry)
		s.setFocus(entry.component, "clear")
	}
	return true
}

// FocusEntry focuses an entry. It reports whether the entry could take focus
// (it must still be in the stack and visible).
func (s *overlayStack) FocusEntry(entry *overlayEntry) bool {
	if s.indexOf(entry) == -1 || !s.Visible(entry) {
		return false
	}
	s.bumpFocusOrder(entry)
	s.setFocus(entry.component, "clear")
	return true
}

// Unfocus applies the handle's Unfocus: an overlay that holds focus (or has a
// pending restore) gives it to the requested target when there is one, and stops
// claiming it otherwise. It reports whether it applied anything.
func (s *overlayStack) Unfocus(entry *overlayEntry, target Component, hasTarget bool) bool {
	isFocused := s.focus == entry.component
	state := s.restore
	hasPendingRestore := state.status != "inactive" && state.overlay == entry
	if !isFocused && !hasPendingRestore {
		return false
	}
	if state.status == "blocked" && state.overlay == entry && s.focus == state.blocked {
		if hasTarget {
			s.restore = overlayFocusRestoreState{
				status: "blocked", overlay: entry, blocked: state.blocked, target: target,
			}
		} else {
			s.ClearRestore()
		}
		return true
	}
	s.clearRestoreFor(entry)
	if isFocused || hasTarget {
		topVisible := s.TopmostVisible()
		fallbackTarget := entry.preFocus
		if topVisible != nil && topVisible != entry {
			fallbackTarget = topVisible.component
		}
		if hasTarget {
			s.setFocus(target, "clear")
		} else {
			s.setFocus(fallbackTarget, "clear")
		}
	}
	return true
}
