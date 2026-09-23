package interactive

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/dat267/pier/tui"
)

// Port of src/core/keybindings.ts: the coding-agent keybinding table (the TUI
// bindings plus the app.* bindings), the platform-specific defaults, the
// legacy-name migration, the user config file, and the manager.

// UseWindowsKeybindings reports whether the Windows keybinding variants apply
// (Windows or WSL).
func UseWindowsKeybindings(goos string, env func(string) string) bool {
	if goos == "windows" {
		return true
	}
	if goos == "linux" {
		return env("WSL_DISTRO_NAME") != "" || env("WSL_INTEROP") != ""
	}
	return false
}

// AppKeybindingNames are the app-level keybinding identifiers.
var AppKeybindingNames = []string{
	"app.interrupt", "app.clear", "app.exit", "app.suspend", "app.thinking.cycle",
	"app.thinking.save", "app.model.cycleForward", "app.model.cycleBackward", "app.model.select",
	"app.tools.expand", "app.thinking.toggle", "app.session.toggleNamedFilter", "app.editor.external",
	"app.message.copy", "app.message.followUp", "app.message.dequeue", "app.clipboard.pasteImage",
	"app.session.new", "app.session.tree", "app.session.fork", "app.session.resume",
	"app.tree.foldOrUp", "app.tree.unfoldOrDown", "app.tree.editLabel", "app.tree.toggleLabelTimestamp",
	"app.session.togglePath", "app.session.toggleSort", "app.session.rename", "app.session.delete",
	"app.session.deleteNoninvasive", "app.models.save", "app.models.enableAll", "app.models.clearAll",
	"app.models.toggleProvider", "app.models.reorderUp", "app.models.reorderDown",
	"app.tree.filter.default", "app.tree.filter.noTools", "app.tree.filter.userOnly",
	"app.tree.filter.labeledOnly", "app.tree.filter.all", "app.tree.filter.cycleForward",
	"app.tree.filter.cycleBackward",
}

// Keybindings builds the merged keybinding table for the current platform.
func Keybindings() (map[string]tui.KeybindingDefinition, []string) {
	return KeybindingsFor(runtime.GOOS, os.Getenv)
}

// KeybindingsFor builds the merged table for a platform (testable).
func KeybindingsFor(goos string, env func(string) string) (map[string]tui.KeybindingDefinition, []string) {
	definitions := map[string]tui.KeybindingDefinition{}
	order := make([]string, 0, len(tui.TUIKeybindings)+len(AppKeybindingNames))
	for key, definition := range tui.TUIKeybindings {
		definitions[key] = definition
	}
	for _, key := range tuiKeybindingOrderForOrdering() {
		order = append(order, key)
	}

	windows := UseWindowsKeybindings(goos, env)

	override := func(key string, keys []string, description string) {
		definitions[key] = tui.KeybindingDefinition{DefaultKeys: keys, Description: description}
	}

	undoKeys := []string{"ctrl+-"}
	if goos == "windows" {
		undoKeys = []string{"ctrl+z"}
	} else if windows {
		undoKeys = []string{"alt+z"}
	}
	override("tui.editor.undo", undoKeys, definitions["tui.editor.undo"].Description)

	previousPromptKeys := []string{"ctrl+shift+up", "ctrl+up"}
	if windows {
		previousPromptKeys = []string{"ctrl+up"}
	}
	override("tui.altScreen.previousPrompt", previousPromptKeys, definitions["tui.altScreen.previousPrompt"].Description)

	nextPromptKeys := []string{"ctrl+shift+down", "ctrl+down"}
	if windows {
		nextPromptKeys = []string{"ctrl+down"}
	}
	override("tui.altScreen.nextPrompt", nextPromptKeys, definitions["tui.altScreen.nextPrompt"].Description)

	searchKeys := []string{"ctrl+shift+f"}
	if windows {
		searchKeys = []string{"ctrl+f"}
	}
	override("tui.altScreen.search", searchKeys, definitions["tui.altScreen.search"].Description)

	suspendKeys := []string{"ctrl+z"}
	if goos == "windows" {
		suspendKeys = nil
	}
	cycleBackward := []string{"shift+ctrl+p"}
	if windows {
		cycleBackward = []string{"alt+p"}
	}
	foldOrUp := []string{"ctrl+left", "alt+left"}
	unfoldOrDown := []string{"ctrl+right", "alt+right"}
	if goos == "darwin" {
		foldOrUp = []string{"alt+left", "ctrl+left"}
		unfoldOrDown = []string{"alt+right", "ctrl+right"}
	}
	appDefaults := map[string]tui.KeybindingDefinition{
		"app.interrupt":                 {DefaultKeys: []string{"escape"}, Description: "Cancel or abort"},
		"app.clear":                     {DefaultKeys: []string{"ctrl+c"}, Description: "Clear editor"},
		"app.exit":                      {DefaultKeys: []string{"ctrl+d"}, Description: "Exit when editor is empty"},
		"app.suspend":                   {DefaultKeys: suspendKeys, Description: "Suspend to background"},
		"app.thinking.cycle":            {DefaultKeys: []string{"shift+tab"}, Description: "Cycle thinking level"},
		"app.thinking.save":             {DefaultKeys: []string{"ctrl+s"}, Description: "Save thinking level"},
		"app.model.cycleForward":        {DefaultKeys: []string{"ctrl+p"}, Description: "Cycle to next model"},
		"app.model.cycleBackward":       {DefaultKeys: cycleBackward, Description: "Cycle to previous model"},
		"app.model.select":              {DefaultKeys: []string{"ctrl+l"}, Description: "Open model selector"},
		"app.tools.expand":              {DefaultKeys: []string{"ctrl+o"}, Description: "Toggle tool output"},
		"app.thinking.toggle":           {DefaultKeys: []string{"ctrl+t"}, Description: "Toggle thinking blocks"},
		"app.session.toggleNamedFilter": {DefaultKeys: []string{"ctrl+n"}, Description: "Toggle named session filter"},
		"app.editor.external":           {DefaultKeys: []string{"ctrl+g"}, Description: "Open external editor"},
		"app.message.copy":              {DefaultKeys: []string{"ctrl+x"}, Description: "Copy selection or last assistant message"},
		"app.message.followUp":          {DefaultKeys: followUpKeys(windows), Description: "Queue follow-up message"},
		"app.message.dequeue":           {DefaultKeys: dequeueKeys(windows), Description: "Restore queued messages"},
		"app.clipboard.pasteImage":      {DefaultKeys: pasteImageKeys(windows), Description: "Paste image from clipboard (text fallback)"},
		"app.session.new":               {DefaultKeys: nil, Description: "Start a new session"},
		"app.session.tree":              {DefaultKeys: nil, Description: "Open session tree"},
		"app.session.fork":              {DefaultKeys: nil, Description: "Fork current session"},
		"app.session.resume":            {DefaultKeys: nil, Description: "Resume a session"},
		"app.tree.foldOrUp":             {DefaultKeys: foldOrUp, Description: "Fold tree branch or move up"},
		"app.tree.unfoldOrDown":         {DefaultKeys: unfoldOrDown, Description: "Unfold tree branch or move down"},
		"app.tree.editLabel":            {DefaultKeys: []string{"shift+l"}, Description: "Edit tree label"},
		"app.tree.toggleLabelTimestamp": {DefaultKeys: []string{"shift+t"}, Description: "Toggle tree label timestamps"},
		"app.session.togglePath":        {DefaultKeys: []string{"ctrl+p"}, Description: "Toggle session path display"},
		"app.session.toggleSort":        {DefaultKeys: []string{"ctrl+s"}, Description: "Toggle session sort mode"},
		"app.session.rename":            {DefaultKeys: []string{"ctrl+r"}, Description: "Rename session"},
		"app.session.delete":            {DefaultKeys: []string{"ctrl+d"}, Description: "Delete session"},
		"app.session.deleteNoninvasive": {DefaultKeys: []string{"ctrl+backspace"}, Description: "Delete session when query is empty"},
		"app.models.save":               {DefaultKeys: []string{"ctrl+s"}, Description: "Save model selection"},
		"app.models.enableAll":          {DefaultKeys: []string{"ctrl+a"}, Description: "Enable all models"},
		"app.models.clearAll":           {DefaultKeys: []string{"ctrl+x"}, Description: "Clear all models"},
		"app.models.toggleProvider":     {DefaultKeys: []string{"ctrl+p"}, Description: "Toggle all models for provider"},
		"app.models.reorderUp":          {DefaultKeys: []string{"alt+up"}, Description: "Move model up in priority order"},
		"app.models.reorderDown":        {DefaultKeys: []string{"alt+down"}, Description: "Move model down in priority order"},
		"app.tree.filter.default":       {DefaultKeys: []string{"ctrl+d"}, Description: "Tree filter: default"},
		"app.tree.filter.noTools":       {DefaultKeys: []string{"ctrl+t"}, Description: "Tree filter: hide tool calls"},
		"app.tree.filter.userOnly":      {DefaultKeys: []string{"ctrl+u"}, Description: "Tree filter: user messages only"},
		"app.tree.filter.labeledOnly":   {DefaultKeys: []string{"ctrl+l"}, Description: "Tree filter: labeled only"},
		"app.tree.filter.all":           {DefaultKeys: []string{"ctrl+a"}, Description: "Tree filter: show all"},
		"app.tree.filter.cycleForward":  {DefaultKeys: []string{"ctrl+o"}, Description: "Tree filter: cycle forward"},
		"app.tree.filter.cycleBackward": {DefaultKeys: []string{"shift+ctrl+o"}, Description: "Tree filter: cycle backward"},
	}

	for _, name := range AppKeybindingNames {
		definitions[name] = appDefaults[name]
		order = append(order, name)
	}
	return definitions, order
}

func followUpKeys(windows bool) []string {
	if windows {
		return []string{"ctrl+q"}
	}
	return []string{"alt+enter"}
}

func dequeueKeys(windows bool) []string {
	if windows {
		return []string{"alt+q"}
	}
	return []string{"alt+up"}
}

func pasteImageKeys(windows bool) []string {
	if windows {
		return []string{"alt+v"}
	}
	return []string{"ctrl+v"}
}

// tuiKeybindingOrderForOrdering returns the TUI keybinding declaration order
// by asking the TUI manager for its canonical order.
func tuiKeybindingOrderForOrdering() []string {
	order := make([]string, 0, len(tui.TUIKeybindings))
	manager := tui.NewKeybindingsManager(tui.TUIKeybindings, nil)
	for key := range manager.GetResolvedBindings() {
		order = append(order, key)
	}
	sort.Strings(order)
	return order
}

// KeybindingNameMigrations maps legacy keybinding names to their modern ids.
var KeybindingNameMigrations = map[string]string{
	"cursorUp": "tui.editor.cursorUp", "cursorDown": "tui.editor.cursorDown",
	"cursorLeft": "tui.editor.cursorLeft", "cursorRight": "tui.editor.cursorRight",
	"cursorWordLeft": "tui.editor.cursorWordLeft", "cursorWordRight": "tui.editor.cursorWordRight",
	"cursorLineStart": "tui.editor.cursorLineStart", "cursorLineEnd": "tui.editor.cursorLineEnd",
	"jumpForward": "tui.editor.jumpForward", "jumpBackward": "tui.editor.jumpBackward",
	"pageUp": "tui.editor.pageUp", "pageDown": "tui.editor.pageDown",
	"deleteCharBackward": "tui.editor.deleteCharBackward", "deleteCharForward": "tui.editor.deleteCharForward",
	"deleteWordBackward": "tui.editor.deleteWordBackward", "deleteWordForward": "tui.editor.deleteWordForward",
	"deleteToLineStart": "tui.editor.deleteToLineStart", "deleteToLineEnd": "tui.editor.deleteToLineEnd",
	"yank": "tui.editor.yank", "yankPop": "tui.editor.yankPop", "undo": "tui.editor.undo",
	"newLine": "tui.input.newLine", "submit": "tui.input.submit", "tab": "tui.input.tab",
	"copy": "tui.input.copy", "selectUp": "tui.select.up", "selectDown": "tui.select.down",
	"selectPageUp": "tui.select.pageUp", "selectPageDown": "tui.select.pageDown",
	"selectConfirm": "tui.select.confirm", "selectCancel": "tui.select.cancel",
	"interrupt": "app.interrupt", "clear": "app.clear", "exit": "app.exit", "suspend": "app.suspend",
	"cycleThinkingLevel": "app.thinking.cycle", "cycleModelForward": "app.model.cycleForward",
	"cycleModelBackward": "app.model.cycleBackward", "selectModel": "app.model.select",
	"expandTools": "app.tools.expand", "toggleThinking": "app.thinking.toggle",
	"toggleSessionNamedFilter": "app.session.toggleNamedFilter", "externalEditor": "app.editor.external",
	"followUp": "app.message.followUp", "dequeue": "app.message.dequeue", "pasteImage": "app.clipboard.pasteImage",
	"newSession": "app.session.new", "tree": "app.session.tree", "fork": "app.session.fork",
	"resume": "app.session.resume", "treeFoldOrUp": "app.tree.foldOrUp",
	"treeUnfoldOrDown": "app.tree.unfoldOrDown", "treeEditLabel": "app.tree.editLabel",
	"treeToggleLabelTimestamp": "app.tree.toggleLabelTimestamp", "toggleSessionPath": "app.session.togglePath",
	"toggleSessionSort": "app.session.toggleSort", "renameSession": "app.session.rename",
	"deleteSession": "app.session.delete", "deleteSessionNoninvasive": "app.session.deleteNoninvasive",
}

// MigrateKeybindingsConfig rewrites legacy keybinding names.
func MigrateKeybindingsConfig(rawConfig map[string]json.RawMessage) (map[string]json.RawMessage, bool) {
	config := map[string]json.RawMessage{}
	migrated := false
	for key, value := range rawConfig {
		nextKey := key
		if mapped, ok := KeybindingNameMigrations[key]; ok {
			nextKey = mapped
		}
		if nextKey != key {
			migrated = true
		}
		if key != nextKey {
			if _, exists := rawConfig[nextKey]; exists {
				migrated = true
				continue
			}
		}
		config[nextKey] = value
	}
	return OrderKeybindingsConfig(config), migrated
}

// OrderKeybindingsConfig orders the config by the keybinding table order.
func OrderKeybindingsConfig(config map[string]json.RawMessage) map[string]json.RawMessage {
	definitions, order := Keybindings()
	ordered := map[string]json.RawMessage{}
	for _, key := range order {
		if value, ok := config[key]; ok {
			ordered[key] = value
		}
	}
	var extras []string
	for key := range config {
		if _, ok := ordered[key]; !ok {
			extras = append(extras, key)
		}
	}
	sort.Strings(extras)
	for _, key := range extras {
		ordered[key] = config[key]
	}
	_ = definitions
	return ordered
}

// ToKeybindingsConfig keeps only string and string-array values.
func ToKeybindingsConfig(value map[string]json.RawMessage) map[string][]string {
	config := map[string][]string{}
	for key, raw := range value {
		trimmed := strings.TrimSpace(string(raw))
		if strings.HasPrefix(trimmed, `"`) {
			var single string
			if err := json.Unmarshal(raw, &single); err == nil {
				config[key] = []string{single}
			}
			continue
		}
		var list []string
		if err := json.Unmarshal(raw, &list); err == nil {
			config[key] = list
		}
	}
	return config
}

// LoadRawKeybindingsConfig reads a keybindings.json file.
func LoadRawKeybindingsConfig(path string) (map[string]json.RawMessage, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	var parsed map[string]json.RawMessage
	if err := json.Unmarshal([]byte(stripBOMString(string(data))), &parsed); err != nil {
		return nil, false
	}
	return parsed, true
}

// AppKeybindingsManager is the coding-agent keybindings manager.
type AppKeybindingsManager struct {
	*tui.KeybindingsManager

	configPath string
}

// NewAppKeybindingsManager creates a manager with the merged table.
func NewAppKeybindingsManager(userBindings map[string][]string, configPath string) *AppKeybindingsManager {
	definitions, order := Keybindings()
	return &AppKeybindingsManager{
		KeybindingsManager: tui.NewKeybindingsManagerOrdered(definitions, order, userBindings),
		configPath:         configPath,
	}
}

// CreateAppKeybindings creates the manager from the agent directory.
func CreateAppKeybindings(agentDir string) *AppKeybindingsManager {
	configPath := filepath.Join(agentDir, "keybindings.json")
	return NewAppKeybindingsManager(loadKeybindingsFile(configPath), configPath)
}

// Reload re-reads the user config file.
func (m *AppKeybindingsManager) Reload() {
	if m.configPath == "" {
		return
	}
	m.SetUserBindings(loadKeybindingsFile(m.configPath))
}

// GetEffectiveConfig returns the resolved bindings.
func (m *AppKeybindingsManager) GetEffectiveConfig() map[string][]string {
	return m.GetResolvedBindings()
}

func loadKeybindingsFile(path string) map[string][]string {
	raw, ok := LoadRawKeybindingsConfig(path)
	if !ok {
		return nil
	}
	migrated, _ := MigrateKeybindingsConfig(raw)
	return ToKeybindingsConfig(migrated)
}
