package tui

import "sync"

// Port of src/keybindings.ts: the keybinding definitions, the manager with
// user overrides and conflict detection, and the global accessor.

// Keybinding is a keybinding identifier such as "tui.editor.cursorLeft".
type Keybinding = string

// KeybindingDefinition is a keybinding's default keys and description.
type KeybindingDefinition struct {
	DefaultKeys []string
	Description string
}

// TUIKeybindings are the built-in TUI keybindings.
var TUIKeybindings = map[Keybinding]KeybindingDefinition{
	"tui.editor.cursorUp":           {DefaultKeys: []string{"up"}, Description: "Move cursor up"},
	"tui.editor.cursorDown":         {DefaultKeys: []string{"down"}, Description: "Move cursor down"},
	"tui.editor.historyPrevious":    {DefaultKeys: nil, Description: "Select previous prompt history entry"},
	"tui.editor.historyNext":        {DefaultKeys: nil, Description: "Select next prompt history entry"},
	"tui.editor.cursorLeft":         {DefaultKeys: []string{"left", "ctrl+b"}, Description: "Move cursor left"},
	"tui.editor.cursorRight":        {DefaultKeys: []string{"right", "ctrl+f"}, Description: "Move cursor right"},
	"tui.editor.cursorWordLeft":     {DefaultKeys: []string{"alt+left", "ctrl+left", "alt+b"}, Description: "Move cursor word left"},
	"tui.editor.cursorWordRight":    {DefaultKeys: []string{"alt+right", "ctrl+right", "alt+f"}, Description: "Move cursor word right"},
	"tui.editor.cursorLineStart":    {DefaultKeys: []string{"home", "ctrl+home", "ctrl+a"}, Description: "Move to line start"},
	"tui.editor.cursorLineEnd":      {DefaultKeys: []string{"end", "ctrl+end", "ctrl+e"}, Description: "Move to line end"},
	"tui.editor.jumpForward":        {DefaultKeys: []string{"ctrl+]"}, Description: "Jump forward to character"},
	"tui.editor.jumpBackward":       {DefaultKeys: []string{"ctrl+alt+]"}, Description: "Jump backward to character"},
	"tui.editor.pageUp":             {DefaultKeys: []string{"pageUp", "ctrl+pageUp"}, Description: "Page up"},
	"tui.editor.pageDown":           {DefaultKeys: []string{"pageDown", "ctrl+pageDown"}, Description: "Page down"},
	"tui.editor.deleteCharBackward": {DefaultKeys: []string{"backspace"}, Description: "Delete character backward"},
	"tui.editor.deleteCharForward":  {DefaultKeys: []string{"delete", "ctrl+d"}, Description: "Delete character forward"},
	"tui.editor.deleteWordBackward": {DefaultKeys: []string{"ctrl+w", "alt+backspace"}, Description: "Delete word backward"},
	"tui.editor.deleteWordForward":  {DefaultKeys: []string{"alt+d", "alt+delete"}, Description: "Delete word forward"},
	"tui.editor.deleteToLineStart":  {DefaultKeys: []string{"ctrl+u"}, Description: "Delete to line start"},
	"tui.editor.deleteToLineEnd":    {DefaultKeys: []string{"ctrl+k"}, Description: "Delete to line end"},
	"tui.editor.yank":               {DefaultKeys: []string{"ctrl+y"}, Description: "Yank"},
	"tui.editor.yankPop":            {DefaultKeys: []string{"alt+y"}, Description: "Yank pop"},
	"tui.editor.undo":               {DefaultKeys: []string{"ctrl+-"}, Description: "Undo"},
	"tui.input.newLine":             {DefaultKeys: []string{"shift+enter", "ctrl+j"}, Description: "Insert newline"},
	"tui.input.submit":              {DefaultKeys: []string{"enter"}, Description: "Submit input"},
	"tui.input.tab":                 {DefaultKeys: []string{"tab"}, Description: "Tab / autocomplete"},
	"tui.input.copy":                {DefaultKeys: []string{"ctrl+c"}, Description: "Copy selection"},
	"tui.select.up":                 {DefaultKeys: []string{"up"}, Description: "Move selection up"},
	"tui.select.down":               {DefaultKeys: []string{"down"}, Description: "Move selection down"},
	"tui.select.pageUp":             {DefaultKeys: []string{"pageUp"}, Description: "Selection page up"},
	"tui.select.pageDown":           {DefaultKeys: []string{"pageDown"}, Description: "Selection page down"},
	"tui.select.confirm":            {DefaultKeys: []string{"enter"}, Description: "Confirm selection"},
	"tui.select.cancel":             {DefaultKeys: []string{"escape", "ctrl+c"}, Description: "Cancel selection"},
	// These intentionally shadow the unmodified editor bindings in fullscreen mode.
	"tui.altScreen.pageUp":         {DefaultKeys: []string{"pageUp"}, Description: "Scroll viewport up one page"},
	"tui.altScreen.pageDown":       {DefaultKeys: []string{"pageDown"}, Description: "Scroll viewport down one page"},
	"tui.altScreen.halfPageUp":     {DefaultKeys: nil, Description: "Scroll viewport up half a page"},
	"tui.altScreen.halfPageDown":   {DefaultKeys: nil, Description: "Scroll viewport down half a page"},
	"tui.altScreen.lineUp":         {DefaultKeys: nil, Description: "Scroll viewport up one line"},
	"tui.altScreen.lineDown":       {DefaultKeys: nil, Description: "Scroll viewport down one line"},
	"tui.altScreen.previousPrompt": {DefaultKeys: []string{"ctrl+shift+up", "ctrl+up"}, Description: "Jump to previous semantic prompt"},
	"tui.altScreen.nextPrompt":     {DefaultKeys: []string{"ctrl+shift+down", "ctrl+down"}, Description: "Jump to next semantic prompt"},
	"tui.altScreen.search":         {DefaultKeys: []string{"ctrl+shift+f"}, Description: "Search the primary scroll view"},
	"tui.altScreen.searchNext":     {DefaultKeys: []string{"enter", "ctrl+g"}, Description: "Select the next search match"},
	"tui.altScreen.searchPrevious": {DefaultKeys: []string{"shift+enter", "ctrl+shift+g"}, Description: "Select the previous search match"},
	"tui.altScreen.searchClose":    {DefaultKeys: []string{"escape"}, Description: "Close transcript search"},
	"tui.altScreen.top":            {DefaultKeys: []string{"home"}, Description: "Scroll viewport to top"},
	"tui.altScreen.bottom":         {DefaultKeys: []string{"end"}, Description: "Scroll viewport to bottom"},
}

// tuiKeybindingOrder is the declaration order of the built-in keybindings
// (upstream object literal order).
var tuiKeybindingOrder = []Keybinding{
	"tui.editor.cursorUp",
	"tui.editor.cursorDown",
	"tui.editor.historyPrevious",
	"tui.editor.historyNext",
	"tui.editor.cursorLeft",
	"tui.editor.cursorRight",
	"tui.editor.cursorWordLeft",
	"tui.editor.cursorWordRight",
	"tui.editor.cursorLineStart",
	"tui.editor.cursorLineEnd",
	"tui.editor.jumpForward",
	"tui.editor.jumpBackward",
	"tui.editor.pageUp",
	"tui.editor.pageDown",
	"tui.editor.deleteCharBackward",
	"tui.editor.deleteCharForward",
	"tui.editor.deleteWordBackward",
	"tui.editor.deleteWordForward",
	"tui.editor.deleteToLineStart",
	"tui.editor.deleteToLineEnd",
	"tui.editor.yank",
	"tui.editor.yankPop",
	"tui.editor.undo",
	"tui.input.newLine",
	"tui.input.submit",
	"tui.input.tab",
	"tui.input.copy",
	"tui.select.up",
	"tui.select.down",
	"tui.select.pageUp",
	"tui.select.pageDown",
	"tui.select.confirm",
	"tui.select.cancel",
	"tui.altScreen.pageUp",
	"tui.altScreen.pageDown",
	"tui.altScreen.halfPageUp",
	"tui.altScreen.halfPageDown",
	"tui.altScreen.lineUp",
	"tui.altScreen.lineDown",
	"tui.altScreen.previousPrompt",
	"tui.altScreen.nextPrompt",
	"tui.altScreen.search",
	"tui.altScreen.searchNext",
	"tui.altScreen.searchPrevious",
	"tui.altScreen.searchClose",
	"tui.altScreen.top",
	"tui.altScreen.bottom",
}

// KeybindingConflict is a key claimed by multiple user keybindings.
type KeybindingConflict struct {
	Key         string
	Keybindings []string
}

func normalizeKeys(keys []string) []string {
	seen := map[string]bool{}
	var result []string
	for _, key := range keys {
		if seen[key] {
			continue
		}
		seen[key] = true
		result = append(result, key)
	}
	return result
}

// KeybindingsManager resolves keybindings with user overrides.
type KeybindingsManager struct {
	mu           sync.Mutex
	definitions  map[Keybinding]KeybindingDefinition
	userBindings map[string][]string
	keysByID     map[Keybinding][]string
	conflicts    []KeybindingConflict
	order        []Keybinding
}

// NewKeybindingsManager creates a manager. Upstream's definitions object has
// declaration order; Go maps do not, so the order follows the canonical
// TUIKeybindings order when the definitions map is the built-in one
// (divergence D60).
func NewKeybindingsManager(definitions map[Keybinding]KeybindingDefinition, userBindings map[string][]string) *KeybindingsManager {
	manager := &KeybindingsManager{
		definitions:  definitions,
		userBindings: userBindings,
		keysByID:     map[Keybinding][]string{},
	}
	manager.order = canonicalBindingOrder(definitions)
	manager.rebuild()
	return manager
}

// canonicalBindingOrder returns a stable iteration order for a definition
// map: the built-in order when possible, otherwise sorted keys.
func canonicalBindingOrder(definitions map[Keybinding]KeybindingDefinition) []Keybinding {
	order := make([]Keybinding, 0, len(definitions))
	if len(definitions) == len(TUIKeybindings) {
		builtin := true
		for key := range definitions {
			if _, ok := TUIKeybindings[key]; !ok {
				builtin = false
				break
			}
		}
		if builtin {
			for _, key := range tuiKeybindingOrder {
				if _, ok := definitions[key]; ok {
					order = append(order, key)
				}
			}
			return order
		}
	}
	keys := make([]string, 0, len(definitions))
	for key := range definitions {
		keys = append(keys, key)
	}
	sortStrings(keys)
	for _, key := range keys {
		order = append(order, key)
	}
	return order
}

func (m *KeybindingsManager) rebuild() {
	m.keysByID = map[Keybinding][]string{}
	m.conflicts = nil

	userClaims := map[string]map[Keybinding]bool{}
	for _, keybinding := range m.order {
		keys, ok := m.userBindings[keybinding]
		if !ok {
			continue
		}
		if _, defined := m.definitions[keybinding]; !defined {
			continue
		}
		for _, key := range normalizeKeys(keys) {
			claimants, ok := userClaims[key]
			if !ok {
				claimants = map[Keybinding]bool{}
				userClaims[key] = claimants
			}
			claimants[keybinding] = true
		}
	}

	claimedKeys := make([]string, 0, len(userClaims))
	for key := range userClaims {
		claimedKeys = append(claimedKeys, key)
	}
	sortStrings(claimedKeys)
	for _, key := range claimedKeys {
		claimants := userClaims[key]
		if len(claimants) > 1 {
			names := make([]string, 0, len(claimants))
			for name := range claimants {
				names = append(names, name)
			}
			sortStrings(names)
			m.conflicts = append(m.conflicts, KeybindingConflict{Key: key, Keybindings: names})
		}
	}

	for _, id := range m.order {
		definition := m.definitions[id]
		userKeys, hasUserKeys := m.userBindings[id]
		if hasUserKeys {
			m.keysByID[id] = normalizeKeys(userKeys)
		} else {
			m.keysByID[id] = normalizeKeys(definition.DefaultKeys)
		}
	}
}

// Matches reports whether the input matches any key bound to the keybinding.
func (m *KeybindingsManager) Matches(data string, keybinding Keybinding) bool {
	m.mu.Lock()
	keys := append([]string(nil), m.keysByID[keybinding]...)
	m.mu.Unlock()
	for _, key := range keys {
		if MatchesKey(data, key) {
			return true
		}
	}
	return false
}

// GetKeys returns the resolved keys for a keybinding.
func (m *KeybindingsManager) GetKeys(keybinding Keybinding) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.keysByID[keybinding]...)
}

// GetDefinition returns a keybinding's definition.
func (m *KeybindingsManager) GetDefinition(keybinding Keybinding) KeybindingDefinition {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.definitions[keybinding]
}

// GetConflicts returns the user keybinding conflicts.
func (m *KeybindingsManager) GetConflicts() []KeybindingConflict {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]KeybindingConflict, 0, len(m.conflicts))
	for _, conflict := range m.conflicts {
		out = append(out, KeybindingConflict{
			Key:         conflict.Key,
			Keybindings: append([]string(nil), conflict.Keybindings...),
		})
	}
	return out
}

// SetUserBindings replaces the user bindings and rebuilds.
func (m *KeybindingsManager) SetUserBindings(userBindings map[string][]string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.userBindings = userBindings
	m.rebuild()
}

// GetUserBindings returns a copy of the user bindings.
func (m *KeybindingsManager) GetUserBindings() map[string][]string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string][]string{}
	for key, value := range m.userBindings {
		out[key] = append([]string(nil), value...)
	}
	return out
}

// GetResolvedBindings returns every keybinding's resolved keys.
func (m *KeybindingsManager) GetResolvedBindings() map[string][]string {
	m.mu.Lock()
	defer m.mu.Unlock()
	resolved := map[string][]string{}
	for _, id := range m.order {
		resolved[id] = append([]string(nil), m.keysByID[id]...)
	}
	return resolved
}

func sortStrings(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}

var globalKeybindingsState struct {
	mu          sync.Mutex
	keybindings *KeybindingsManager
}

// SetKeybindings installs the global keybindings manager.
func SetKeybindings(keybindings *KeybindingsManager) {
	globalKeybindingsState.mu.Lock()
	defer globalKeybindingsState.mu.Unlock()
	globalKeybindingsState.keybindings = keybindings
}

// GetKeybindings returns the global keybindings manager, creating the default
// one on first use.
func GetKeybindings() *KeybindingsManager {
	globalKeybindingsState.mu.Lock()
	defer globalKeybindingsState.mu.Unlock()
	if globalKeybindingsState.keybindings == nil {
		globalKeybindingsState.keybindings = NewKeybindingsManager(TUIKeybindings, nil)
	}
	return globalKeybindingsState.keybindings
}
