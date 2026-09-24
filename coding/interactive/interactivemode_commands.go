package interactive

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/dat267/pier/coding"
	"github.com/dat267/pier/tui"
)

// Port of the remaining command handlers of
// src/modes/interactive/interactive-mode.ts (getPathCommandArgument,
// handleExport/Import/Copy/Name/Session/Hotkeys/Clear/Debug/Compact/Reload
// commands, the easter eggs and stop). The share and bug-report flows and
// the changelog command were removed; they are not part of this port.
//
// Divergences: the runtime/clipboard/report collaborators are injected (D125);
// the extension seams stay out of scope (D41).

// CommandSession is the session surface the command handlers need.
type CommandSession interface {
	GetSessionStats() *coding.SessionStats
	GetLastAssistantText() string
	SetSessionName(name string)
	ExportToJsonl(outputPath string) (string, error)
	CompactSession(ctx context.Context, customInstructions string) error
	GetCacheWarmingStatus() *coding.CacheWarmingStatus
	ModelRuntime() *coding.ModelRuntime
	IsStreaming() bool
	IsCompacting() bool
}

// CommandWiring handles the slash commands.
type CommandWiring struct {
	Chat        *tui.Container
	UI          tui.TUI
	Settings    *coding.SettingsManager
	Session     CommandSession
	SessionInfo *coding.SessionManager
	AppName     string
	Platform    string

	// ShowStatus/ShowError/ShowWarning report messages.
	ShowStatus  func(message string)
	ShowError   func(message string)
	ShowWarning func(message string)
	// RequestRender requests a render.
	RequestRender func()
	// ClearStatusIndicator clears the active indicator.
	ClearStatusIndicator func()

	// ExportToHTML exports the session (the HTML exporter is out of scope).
	ExportToHTML func(outputPath string) (string, error)
	// NewSession starts a new session.
	NewSession func(ctx context.Context) (bool, error)
	// ImportFromJSONL imports a session ("" cwd = the session's cwd).
	ImportFromJSONL func(ctx context.Context, inputPath string, cwdOverride string) (bool, error)
	// ShowExtensionConfirm asks a yes/no question; the answer arrives through the
	// callback on the UI loop (upstream's extension-UI confirm, awaited there).
	ShowExtensionConfirm func(ctx context.Context, title string, message string, onAnswer func(confirmed bool))
	// PromptForMissingCwd resolves a missing-cwd error.
	PromptForMissingCwd func(ctx context.Context, issue coding.SessionCwdIssue, onCwd func(cwd string, ok bool))
	// CopyToClipboard copies text, returning (ok, message).
	CopyToClipboard func(text string) (bool, string)
	// CopyActiveSelection copies the alt-screen selection.
	CopyActiveSelection func() bool
	// WriteDebugLog writes the debug log.
	WriteDebugLog func(content string) error
	// MarkdownTheme is used for the changelog/hotkeys rendering.
	MarkdownTheme func() tui.MarkdownTheme

	// Reload hooks (upstream handleReloadCommand; extension mechanics are
	// out of scope, D41).
	EditorContainer *tui.Container
	Editor          tui.Component
	// RunDetached runs the blocking reload work off the UI loop.
	RunDetached func(fn func())
	// ModelDefaultNotice reports the modeldefault sync outcome of the last
	// reload (D151): the notice text, and whether it is a warning. A reload
	// that moved the model must say so rather than change it silently.
	ModelDefaultNotice func() (string, bool)
	// ReloadNow re-reads settings-dependent state off the UI loop (settings
	// file, session queue modes, keybindings, implicit project trust). It
	// returns the models.json error ("" = none) and whether implicit project
	// trust was saved.
	ReloadNow func() (modelsJSONError string, savedTrust bool, err error)
	// ApplyReloadedSettings re-applies settings-dependent UI state on the
	// loop (runtime settings, chat rebuild, themes, autocomplete).
	ApplyReloadedSettings func()
}

func (w *CommandWiring) showStatus(message string) {
	if w.ShowStatus != nil {
		w.ShowStatus(message)
	}
}

func (w *CommandWiring) showError(message string) {
	if w.ShowError != nil {
		w.ShowError(message)
	}
}

func (w *CommandWiring) showWarning(message string) {
	if w.ShowWarning != nil {
		w.ShowWarning(message)
	}
}

func (w *CommandWiring) requestRender() {
	if w.RequestRender != nil {
		w.RequestRender()
	} else if w.UI != nil {
		w.UI.RequestRender(false)
	}
}

func (w *CommandWiring) markdownTheme() tui.MarkdownTheme {
	if w.MarkdownTheme != nil {
		return w.MarkdownTheme()
	}
	return GetMarkdownTheme()
}

// GetPathCommandArgument extracts the path argument of /export or /import.
func GetPathCommandArgument(text string, command string) (string, bool) {
	if text == command {
		return "", false
	}
	if !strings.HasPrefix(text, command+" ") {
		return "", false
	}
	args := strings.TrimLeft(text[len(command)+1:], " \t")
	if args == "" {
		return "", false
	}
	first := args[0]
	if first == '"' || first == '\'' {
		closing := strings.IndexByte(args[1:], first)
		if closing < 0 {
			return "", false
		}
		return args[1 : 1+closing], true
	}
	if index := strings.IndexAny(args, " \t\n\r\f\v"); index >= 0 {
		return args[:index], true
	}
	return args, true
}

// HandleExportCommand exports the session as JSONL or HTML.
func (w *CommandWiring) HandleExportCommand(ctx context.Context, text string) {
	outputPath, hasPath := GetPathCommandArgument(text, "/export")
	if hasPath && strings.HasSuffix(outputPath, ".jsonl") {
		filePath, err := w.Session.ExportToJsonl(outputPath)
		if err != nil {
			w.showError("Failed to export session: " + err.Error())
			return
		}
		w.showStatus("Session exported to: " + filePath)
		return
	}
	if w.ExportToHTML == nil {
		w.showError("Failed to export session: HTML export is not available")
		return
	}
	filePath, err := w.ExportToHTML(outputPath)
	if err != nil {
		w.showError("Failed to export session: " + err.Error())
		return
	}
	w.showStatus("Session exported to: " + filePath)
}

// HandleImportCommand imports a session from JSONL.
func (w *CommandWiring) HandleImportCommand(ctx context.Context, text string) {
	inputPath, ok := GetPathCommandArgument(text, "/import")
	if !ok {
		w.showError("Usage: /import <path.jsonl>")
		return
	}

	// importWith runs the import and, when the session's stored cwd is gone,
	// offers to continue in the fallback before trying again — upstream's
	// await, then await again, as a callback chain.
	var importWith func(cwdOverride string)
	importWith = func(cwdOverride string) {
		if w.ImportFromJSONL == nil {
			w.showStatus("Import cancelled")
			return
		}
		if w.ClearStatusIndicator != nil {
			w.ClearStatusIndicator()
		}
		cancelled, err := w.ImportFromJSONL(ctx, inputPath, cwdOverride)
		if err == nil {
			if cancelled {
				w.showStatus("Import cancelled")
				return
			}
			w.showStatus("Session imported from: " + inputPath)
			return
		}
		var cwdErr *coding.MissingSessionCwdError
		if w.PromptForMissingCwd != nil && errors.As(err, &cwdErr) {
			w.PromptForMissingCwd(ctx, cwdErr.Issue, func(selectedCwd string, selected bool) {
				if !selected {
					w.showStatus("Import cancelled")
					return
				}
				importWith(selectedCwd)
			})
			return
		}
		w.showError("Failed to import session: " + err.Error())
	}

	if w.ShowExtensionConfirm == nil {
		importWith("")
		return
	}
	w.ShowExtensionConfirm(ctx, "Import session", "Replace current session with "+inputPath+"?", func(confirmed bool) {
		if !confirmed {
			w.showStatus("Import cancelled")
			return
		}
		importWith("")
	})
}

// reloadedItems names what a reload re-reads, for the /reload notice. Upstream's
// text leads with "extensions", which the port does not implement (D41) — the
// box and the status line would announce a reload that never happens. The rest
// is real: CommandWiring.ReloadNow re-reads settings, queue modes, context
// files, skills and the system/append prompt files, re-reads keybindings and
// saves implicit project trust, and App.applyReloadedSettings re-applies the
// theme and the settings-dependent UI state.
const reloadedItems = "keybindings, skills, prompts, themes, and context files"

// HandleReloadCommand runs /reload (upstream handleReloadCommand): guard
// streaming/compacting, swap the editor for a reload box, run the reload
// work detached so the box paints, then re-apply settings-dependent state
// and restore the editor. Extension mechanics are out of scope (D41).
func (w *CommandWiring) HandleReloadCommand() {
	if w.Session != nil && w.Session.IsStreaming() {
		w.showWarning("Wait for the current response to finish before reloading.")
		return
	}
	if w.Session != nil && w.Session.IsCompacting() {
		w.showWarning("Wait for compaction to finish before reloading.")
		return
	}
	if w.EditorContainer == nil || w.Editor == nil || w.ReloadNow == nil {
		return
	}

	theme := ActiveTheme()
	reloadBox := &tui.Container{}
	reloadBox.AddChild(NewDynamicBorder(nil))
	reloadBox.AddChild(tui.NewSpacer(1))
	reloadBox.AddChild(tui.NewText(theme.Fg("muted", "Reloading "+reloadedItems+"..."), 1, 0, nil))
	reloadBox.AddChild(tui.NewSpacer(1))
	reloadBox.AddChild(NewDynamicBorder(nil))

	// Upstream swaps the editor, focuses the box and awaits a nextTick so the
	// box paints before the reload; the port runs the reload work detached so
	// the loop keeps painting while it runs.
	w.EditorContainer.Clear()
	w.EditorContainer.AddChild(reloadBox)
	if w.UI != nil {
		w.UI.SetFocus(reloadBox)
		w.UI.RequestRender(true)
	}

	restore := func() {
		w.EditorContainer.Clear()
		w.EditorContainer.AddChild(w.Editor)
		if w.UI != nil {
			w.UI.SetFocus(w.Editor)
			w.UI.RequestRender(false)
		}
	}
	finish := func(modelsJSONError string, savedTrust bool, err error) {
		if err != nil {
			restore()
			w.showError("Reload failed: " + err.Error())
			return
		}
		if w.ApplyReloadedSettings != nil {
			w.ApplyReloadedSettings()
		}
		if modelsJSONError != "" {
			w.showError("models.json error: " + modelsJSONError)
		}
		if savedTrust {
			w.showStatus("Reloaded " + reloadedItems + "; saved project trust")
		} else {
			w.showStatus("Reloaded " + reloadedItems)
		}
		if w.ModelDefaultNotice != nil {
			if message, warning := w.ModelDefaultNotice(); message != "" {
				if warning {
					w.showWarning(message)
				} else {
					w.showStatus(message)
				}
			}
		}
		restore()
	}

	if w.RunDetached != nil {
		w.RunDetached(func() {
			modelsJSONError, savedTrust, err := w.ReloadNow()
			if w.UI != nil {
				w.UI.Post(func() { finish(modelsJSONError, savedTrust, err) })
				return
			}
			finish(modelsJSONError, savedTrust, err)
		})
		return
	}
	finish(w.ReloadNow())
}

// HandleCopyCommand copies the selection or the last assistant message.
func (w *CommandWiring) HandleCopyCommand(flashConfirmation bool, preferSelection bool) {
	if preferSelection {
		if altScreen, ok := tuiConcrete(w.UI).(*tui.AltScreen); ok && !altScreen.GetCopyOnSelect() && altScreen.HasActiveSelection() {
			if w.CopyActiveSelection != nil {
				w.CopyActiveSelection()
				return
			}
			altScreen.CopyActiveSelectionToClipboard()
			return
		}
	}
	text := w.Session.GetLastAssistantText()
	if text == "" {
		w.showError("No agent messages to copy yet.")
		return
	}
	if w.CopyToClipboard == nil {
		w.showError("Clipboard is not available")
		return
	}
	ok, message := w.CopyToClipboard(text)
	if !ok {
		w.showError(message)
		return
	}
	if flashConfirmation {
		if altScreen, isAlt := tuiConcrete(w.UI).(*tui.AltScreen); isAlt {
			altScreen.Flash("Copied!", 0)
			return
		}
	}
	w.showStatus("Copied last agent message to clipboard")
}

// HandleNameCommand sets or prints the session name.
func (w *CommandWiring) HandleNameCommand(text string) {
	name := strings.TrimSpace(strings.TrimPrefix(text, "/name"))
	if name == "" {
		currentName := w.SessionInfo.GetSessionName()
		if currentName != "" {
			theme := ActiveTheme()
			w.Chat.AddChild(tui.NewSpacer(1))
			w.Chat.AddChild(tui.NewText(theme.Fg("dim", "Session name: "+currentName), 1, 0, nil))
		} else {
			w.showWarning("Usage: /name <name>")
		}
		w.requestRender()
		return
	}

	w.Session.SetSessionName(name)
	sessionName := w.SessionInfo.GetSessionName()
	theme := ActiveTheme()
	if sessionName != name {
		w.showWarning("Session name was normalized from " + jsonQuote(name) + " to " + jsonQuote(sessionName))
	}
	w.Chat.AddChild(tui.NewSpacer(1))
	display := sessionName
	if display == "" {
		display = name
	}
	w.Chat.AddChild(tui.NewText(theme.Fg("dim", "Session name set: "+display), 1, 0, nil))
	w.requestRender()
}

func jsonQuote(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

// HandleSessionCommand renders the session info panel.
//
// The panel's numbers come from whole-session scans, and on a large session the
// cold scan is hundreds of milliseconds of JSON work (0.87s on a 19k-entry
// session). Upstream computes the panel inline, which here meant the UI loop
// could not render or route input until it finished. So the text is built off
// the loop and posted back to it: dispatch returns immediately and the panel
// lands on the next pump (D159). What it reads is session state behind mutexes
// (SettingsManager, SessionManager, AgentState, Projection), never the
// component tree.
func (w *CommandWiring) HandleSessionCommand(now int64) {
	if w.Chat == nil || w.UI == nil {
		// No loop to post to (headless wiring): compute inline, as upstream does.
		w.addSessionInfoPanel(w.SessionInfoPanel(now))
		return
	}
	go func() {
		text := w.SessionInfoPanel(now)
		w.UI.Post(func() { w.addSessionInfoPanel(text) })
	}()
}

// addSessionInfoPanel appends a formatted panel to the chat. Runs on the UI loop.
func (w *CommandWiring) addSessionInfoPanel(text string) {
	if w.Chat == nil {
		return
	}
	w.Chat.AddChild(tui.NewSpacer(1))
	w.Chat.AddChild(tui.NewText(text, 1, 0, nil))
	w.requestRender()
}

// SessionInfoPanel formats the session info panel. It only reads mutex-guarded
// session state, so it is safe to run off the UI loop.
func (w *CommandWiring) SessionInfoPanel(now int64) string {
	theme := ActiveTheme()
	stats := w.Session.GetSessionStats()
	sessionName := w.SessionInfo.GetSessionName()
	// These three walks read through the session's message memo; re-parsing the
	// session for a panel cost about 1.5s on a 19k-entry session and froze the UI.
	cacheWaste := w.SessionInfo.ComputeCacheWaste(w.Session.ModelRuntime())
	usageBreakdown := w.SessionInfo.UsageCostBreakdown()

	var info strings.Builder
	info.WriteString(theme.Bold("Session Info") + "\n\n")
	if sessionName != "" {
		info.WriteString(theme.Fg("dim", "Name:") + " " + sessionName + "\n")
	}
	sessionFile := stats.SessionFile
	if sessionFile == "" {
		sessionFile = "In-memory"
	}
	info.WriteString(theme.Fg("dim", "File:") + " " + sessionFile + "\n")
	info.WriteString(theme.Fg("dim", "ID:") + " " + stats.SessionID + "\n\n")
	info.WriteString(theme.Bold("Messages") + "\n")
	info.WriteString(theme.Fg("dim", "Total:") + " " + itoa(stats.TotalMessages) + "\n")
	info.WriteString(theme.Fg("dim", "User:") + " " + itoa(stats.UserMessages) + "\n")
	info.WriteString(theme.Fg("dim", "Assistant:") + " " + itoa(stats.AssistantMessages) + "\n")
	info.WriteString(theme.Fg("dim", "Tools:") + " " + itoa(stats.ToolCalls) + " calls, " +
		itoa(stats.ToolResults) + " results\n\n")
	info.WriteString(theme.Bold("Tokens") + "\n")
	promptTokens := stats.Tokens.Input + stats.Tokens.CacheRead + stats.Tokens.CacheWrite
	info.WriteString(theme.Fg("dim", "Input:") + " " + formatThousands(promptTokens) + "\n")
	if promptTokens > 0 && (stats.Tokens.CacheRead > 0 || stats.Tokens.CacheWrite > 0) {
		hitRate := theme.Fg("dim", "("+formatFixed(float64(stats.Tokens.CacheRead)/float64(promptTokens)*100, 1)+"%)")
		info.WriteString("  " + theme.Fg("dim", "Cached:") + " " + formatThousands(stats.Tokens.CacheRead) + " " + hitRate + "\n")
		written := ""
		if stats.Tokens.CacheWrite > 0 {
			written = " " + theme.Fg("dim", "("+formatThousands(stats.Tokens.CacheWrite)+" written to cache)")
		}
		info.WriteString("  " + theme.Fg("dim", "Uncached:") + " " +
			formatThousands(stats.Tokens.Input+stats.Tokens.CacheWrite) + written + "\n")
	}
	info.WriteString(theme.Fg("dim", "Output:") + " " + formatThousands(stats.Tokens.Output) + "\n")
	info.WriteString(theme.Fg("dim", "Total:") + " " + formatThousands(stats.Tokens.Total) + "\n")

	info.WriteString("\n" + theme.Bold("Cache Warming") + "\n")
	cacheWarmingMode := ""
	if w.Settings != nil {
		cacheWarmingMode = w.Settings.GetCacheWarmingMode()
	}
	info.WriteString(theme.Fg("dim", "Mode:") + " " + cacheWarmingMode + "\n")
	status := w.Session.GetCacheWarmingStatus()
	statusText := "Inactive (cache warming unavailable)"
	if status != nil {
		statusText = coding.FormatCacheWarmingStatus(*status, now)
	}
	info.WriteString(theme.Fg("dim", "Status:") + " " + statusText + "\n")
	if status != nil && status.Decision != nil && status.Decision.EconomicsAvailable {
		info.WriteString(theme.Fg("dim", "Cache miss penalty:") + " $" + formatFixed(status.Decision.MissCost, 3) + "\n")
		info.WriteString(theme.Fg("dim", "Refresh cost:") + " $" + formatFixed(status.Decision.WarmCost, 3) + "\n")
	}

	if stats.Cost > 0 || cacheWaste.MissedTokens > 0 {
		info.WriteString("\n" + theme.Bold("Cost") + "\n")
		info.WriteString(theme.Fg("dim", "Total:") + " $" + formatFixed(stats.Cost, 3))
		if len(usageBreakdown) > 1 {
			for _, entry := range usageBreakdown {
				info.WriteString("\n  " + theme.Fg("dim", entry.Key+":") + " $" + formatFixed(entry.Cost, 3) +
					" " + theme.Fg("dim", "("+FormatTokens(entry.Tokens)+" tokens)"))
			}
		}
		if cacheWaste.MissedTokens > 0 {
			missLabel := itoa(cacheWaste.MissCount) + " misses"
			if cacheWaste.MissCount == 1 {
				missLabel = "1 miss"
			}
			detail := formatThousands(cacheWaste.MissedTokens) + " tokens, " + missLabel
			if cacheWaste.MissedCost >= 0.0001 {
				info.WriteString("\n" + theme.Fg("dim", "Cache Re-billed:") + " $" +
					formatFixed(cacheWaste.MissedCost, 3) + " " + theme.Fg("dim", "("+detail+")"))
			} else {
				info.WriteString("\n" + theme.Fg("dim", "Cache Re-billed:") + " " + detail)
			}
		}
	}

	return info.String()
}

// HandleHotkeysCommand renders the keyboard-shortcut table.
func (w *CommandWiring) HandleHotkeysCommand() {
	theme := ActiveTheme()
	editorKey := func(keybinding tui.Keybinding) string { return KeyDisplayText(keybinding) }
	row := func(key string, action string) string { return "| `" + key + "` | " + action + " |\n" }

	var hotkeys strings.Builder
	hotkeys.WriteString("\n**Navigation**\n| Key | Action |\n|-----|--------|\n")
	hotkeys.WriteString(row(editorKey("tui.editor.cursorUp")+"` / `"+editorKey("tui.editor.cursorDown")+"` / `"+
		editorKey("tui.editor.cursorLeft")+"` / `"+editorKey("tui.editor.cursorRight"), "Move cursor / browse history"))
	hotkeys.WriteString(row(editorKey("tui.editor.cursorWordLeft")+"` / `"+editorKey("tui.editor.cursorWordRight"), "Move by word"))
	hotkeys.WriteString(row(editorKey("tui.editor.cursorLineStart"), "Start of line"))
	hotkeys.WriteString(row(editorKey("tui.editor.cursorLineEnd"), "End of line"))
	hotkeys.WriteString(row(editorKey("tui.editor.jumpForward"), "Jump forward to character"))
	hotkeys.WriteString(row(editorKey("tui.editor.jumpBackward"), "Jump backward to character"))
	hotkeys.WriteString(row(editorKey("tui.editor.pageUp")+"` / `"+editorKey("tui.editor.pageDown"), "Scroll by page"))

	hotkeys.WriteString("\n**Editing**\n| Key | Action |\n|-----|--------|\n")
	hotkeys.WriteString(row(editorKey("tui.input.submit"), "Send message"))
	newLineAction := "New line"
	if w.Platform == "win32" {
		newLineAction += " (Ctrl+Enter on Windows Terminal)"
	}
	hotkeys.WriteString(row(editorKey("tui.input.newLine"), newLineAction))
	hotkeys.WriteString(row(editorKey("tui.editor.deleteWordBackward"), "Delete word backwards"))
	hotkeys.WriteString(row(editorKey("tui.editor.deleteWordForward"), "Delete word forwards"))
	hotkeys.WriteString(row(editorKey("tui.editor.deleteToLineStart"), "Delete to start of line"))
	hotkeys.WriteString(row(editorKey("tui.editor.deleteToLineEnd"), "Delete to end of line"))
	hotkeys.WriteString(row(editorKey("tui.editor.yank"), "Paste the most-recently-deleted text"))
	hotkeys.WriteString(row(editorKey("tui.editor.yankPop"), "Cycle through the deleted text after pasting"))
	hotkeys.WriteString(row(editorKey("tui.editor.undo"), "Undo"))

	hotkeys.WriteString("\n**Other**\n| Key | Action |\n|-----|--------|\n")
	hotkeys.WriteString(row(editorKey("tui.input.tab"), "Path completion / accept autocomplete"))
	hotkeys.WriteString(row(KeyDisplayText("app.interrupt"), "Cancel autocomplete / abort streaming"))
	hotkeys.WriteString(row(KeyDisplayText("app.clear"), "Clear editor (first) / exit (second)"))
	hotkeys.WriteString(row(KeyDisplayText("app.exit"), "Exit (when editor is empty)"))
	hotkeys.WriteString(row(KeyDisplayText("app.suspend"), "Suspend to background"))
	hotkeys.WriteString(row(KeyDisplayText("app.thinking.cycle"), "Cycle thinking level"))
	hotkeys.WriteString(row(KeyDisplayText("app.model.cycleForward")+"` / `"+KeyDisplayText("app.model.cycleBackward"), "Cycle models"))
	hotkeys.WriteString(row(KeyDisplayText("app.model.select"), "Open model selector"))
	hotkeys.WriteString(row(KeyDisplayText("app.tools.expand"), "Toggle tool output expansion"))
	hotkeys.WriteString(row(KeyDisplayText("app.thinking.toggle"), "Toggle thinking block visibility"))
	hotkeys.WriteString(row(KeyDisplayText("app.editor.external"), "Edit message in external editor"))
	hotkeys.WriteString(row(KeyDisplayText("app.message.copy"), "Copy selection or last assistant message"))
	hotkeys.WriteString(row(KeyDisplayText("app.message.followUp"), "Queue follow-up message"))
	hotkeys.WriteString(row(KeyDisplayText("app.message.dequeue"), "Restore queued messages"))
	hotkeys.WriteString(row(KeyDisplayText("app.clipboard.pasteImage"), "Paste image or text from clipboard"))
	hotkeys.WriteString(row("/", "Slash commands"))
	hotkeys.WriteString(row("!", "Run bash command"))
	hotkeys.WriteString(row("!!", "Run bash command (excluded from context)"))

	w.Chat.AddChild(tui.NewSpacer(1))
	w.Chat.AddChild(NewDynamicBorder(nil))
	w.Chat.AddChild(tui.NewText(theme.Bold(theme.Fg("accent", "Keyboard Shortcuts")), 1, 0, nil))
	w.Chat.AddChild(tui.NewSpacer(1))
	w.Chat.AddChild(tui.NewMarkdown(strings.TrimSpace(hotkeys.String()), 1, 1, w.markdownTheme(), nil, tui.MarkdownOptions{}))
	w.Chat.AddChild(NewDynamicBorder(nil))
	w.requestRender()
}

// HandleClearCommand starts a new session.
func (w *CommandWiring) HandleClearCommand(ctx context.Context) {
	if w.ClearStatusIndicator != nil {
		w.ClearStatusIndicator()
	}
	if w.NewSession == nil {
		return
	}
	cancelled, err := w.NewSession(ctx)
	if err != nil {
		w.showError("Failed to create session: " + err.Error())
		return
	}
	if cancelled {
		return
	}
	theme := ActiveTheme()
	w.Chat.AddChild(tui.NewSpacer(1))
	w.Chat.AddChild(tui.NewText(theme.Fg("accent", "✓ New session started"), 1, 1, nil))
	w.requestRender()
}

// HandleDebugCommand writes the debug log.
func (w *CommandWiring) HandleDebugCommand(now string) {
	if w.UI == nil || w.WriteDebugLog == nil {
		return
	}
	terminal := w.UI.GetTerminal()
	width, height := 80, 24
	if terminal != nil {
		width = terminal.Columns()
		height = terminal.Rows()
	}
	allLines := w.UI.Render(width)
	debugLogPath := coding.GetDebugLogPath()
	lines := []string{
		"Debug output at " + now,
		"Terminal: " + itoa(width) + "x" + itoa(height),
		"Total lines: " + itoa(len(allLines)),
		"",
		"=== All rendered lines with visible widths ===",
	}
	for index, line := range allLines {
		encoded, _ := json.Marshal(line)
		lines = append(lines, "["+itoa(index)+"] (w="+itoa(tui.VisibleWidth(line))+") "+string(encoded))
	}
	lines = append(lines, "", "=== Agent messages (JSONL) ===")
	if w.SessionInfo != nil {
		for _, entry := range w.SessionInfo.GetEntries() {
			encoded, _ := json.Marshal(entry)
			lines = append(lines, string(encoded))
		}
	}
	lines = append(lines, "")
	if err := w.WriteDebugLog(strings.Join(lines, "\n")); err != nil {
		w.showError("Failed to write debug log: " + err.Error())
		return
	}
	theme := ActiveTheme()
	w.Chat.AddChild(tui.NewSpacer(1))
	w.Chat.AddChild(tui.NewText(theme.Fg("accent", "✓ Debug log written")+"\n"+theme.Fg("muted", debugLogPath), 1, 1, nil))
	w.requestRender()
}

// HandleArminSaysHi shows the armin easter egg.
func (w *CommandWiring) HandleArminSaysHi(host tui.RenderRequester, seed int64) {
	w.Chat.AddChild(tui.NewSpacer(1))
	w.Chat.AddChild(NewArminComponent(host, "", seed))
	w.requestRender()
}

// HandleDementedDelves shows the Earendil announcement.
func (w *CommandWiring) HandleDementedDelves() {
	w.Chat.AddChild(tui.NewSpacer(1))
	w.Chat.AddChild(NewEarendilAnnouncementComponent())
	w.requestRender()
}

// HandleDaxnuts shows the daxnuts easter egg.
func (w *CommandWiring) HandleDaxnuts(host tui.RenderRequester) {
	w.Chat.AddChild(tui.NewSpacer(1))
	w.Chat.AddChild(NewDaxnutsComponent(host))
	w.requestRender()
}

// CheckDaxnutsEasterEgg shows daxnuts for the OpenCode Kimi K2.5 model.
func (w *CommandWiring) CheckDaxnutsEasterEgg(provider string, modelID string, host tui.RenderRequester) {
	if provider == "opencode" && strings.Contains(strings.ToLower(modelID), "kimi-k2.5") {
		w.HandleDaxnuts(host)
	}
}

// HandleCompactCommand compacts the session (upstream shape: clear the
// indicator, then compact).
func (w *CommandWiring) HandleCompactCommand(ctx context.Context, customInstructions string) {
	w.ClearCompactionStatus()
	w.CompactSession(ctx, customInstructions)
}

// ClearCompactionStatus clears the active status indicator. It touches UI
// state, so it must run on the UI loop.
func (w *CommandWiring) ClearCompactionStatus() {
	if w.ClearStatusIndicator != nil {
		w.ClearStatusIndicator()
	}
}

// CompactSession runs the compaction. Its only UI effects are the session
// events it emits, so it is safe to run off the loop (stage 3: a manual
// compaction no longer blocks input).
func (w *CommandWiring) CompactSession(ctx context.Context, customInstructions string) {
	// Errors are emitted as session events.
	_ = w.Session.CompactSession(ctx, customInstructions)
}

// Stop tears the mode down.
func (w *CommandWiring) Stop(fullscreenExitOutput string, disposeSelector func(), clearExtensionListeners func(), disposeFooter func(), disposeFooterData func(), unsubscribe func(), stopInteractiveTui func(string), unregisterSignals func()) {
	if disposeSelector != nil {
		disposeSelector()
	}
	if w.Settings != nil && w.Settings.GetShowTerminalProgress() && w.UI != nil {
		if terminal := w.UI.GetTerminal(); terminal != nil {
			terminal.SetProgress(false)
		}
	}
	if w.ClearStatusIndicator != nil {
		w.ClearStatusIndicator()
	}
	if clearExtensionListeners != nil {
		clearExtensionListeners()
	}
	if disposeFooter != nil {
		disposeFooter()
	}
	if disposeFooterData != nil {
		disposeFooterData()
	}
	if unsubscribe != nil {
		unsubscribe()
	}
	if stopInteractiveTui != nil {
		stopInteractiveTui(fullscreenExitOutput)
	}
	if unregisterSignals != nil {
		unregisterSignals()
	}
}

// WriteDebugLogFile writes the debug log to the standard path.
func WriteDebugLogFile(content string) error {
	debugLogPath := coding.GetDebugLogPath()
	if err := os.MkdirAll(filepath.Dir(debugLogPath), 0o755); err != nil {
		return err
	}
	return os.WriteFile(debugLogPath, []byte(content), 0o644)
}

var errNotAvailable = errors.New("not available")

// newCommandWiring assembles the CommandWiring (port of the corresponding InteractiveMode wiring).
func newCommandWiring(app *App) *CommandWiring {
	return &CommandWiring{
		Chat:                 app.Chat,
		UI:                   app.UI,
		Settings:             app.Settings,
		Session:              app.commandSession(),
		SessionInfo:          app.SessionMgr,
		AppName:              app.options.AppName,
		Platform:             app.options.Platform,
		ShowStatus:           func(message string) { app.Transcript.ShowStatus(message) },
		ShowError:            func(message string) { app.showError(message) },
		ShowWarning:          func(message string) { app.showWarning(message) },
		RequestRender:        func() { app.UI.RequestRender(false) },
		ClearStatusIndicator: func() { app.UIState.ClearStatusIndicator("", false) },
		MarkdownTheme:        func() tui.MarkdownTheme { return *app.markdownTheme() },
		ModelDefaultNotice: func() (string, bool) {
			result := app.Session.LastModelDefaultSync()
			return result.Message, result.Warning
		},
		ExportToHTML: func(outputPath string) (string, error) {
			themeSetting := app.Settings.GetThemeSetting()
			themeName := ""
			if themeSetting != nil {
				themeName = *themeSetting
			}
			return app.Session.ExportSessionToHTML(outputPath, themeName)
		},

		CopyToClipboard: func(text string) (bool, string) {
			if err := coding.CopyTextToClipboard(text); err != nil {
				return false, err.Error()
			}
			return true, ""
		},
		WriteDebugLog:   WriteDebugLogFile,
		EditorContainer: app.EditorContainer,
		Editor:          app.DefaultEditor,
		// `/new` starts a fresh session. The seam was never assigned, so the
		// command cleared the editor and returned without a word; the keybinding
		// (app.session.new) already used this implementation.
		// The dialogs the import flow asks: upstream reaches them through its
		// extension UI; here they are the selector slot with a callback.
		ShowExtensionConfirm: func(ctx context.Context, title string, message string, onAnswer func(confirmed bool)) {
			app.askConfirm(title, message, onAnswer)
		},
		PromptForMissingCwd: func(ctx context.Context, issue coding.SessionCwdIssue, onCwd func(cwd string, ok bool)) {
			app.askMissingSessionCwd(issue, onCwd)
		},
		// `/import` copies a session file into this project's session directory and
		// switches to it.
		ImportFromJSONL: func(ctx context.Context, inputPath string, cwdOverride string) (bool, error) {
			result, err := app.importFromJSONL(ctx, inputPath, cwdOverride)
			if err != nil {
				return false, err
			}
			return result.Cancelled, nil
		},
		NewSession: func(ctx context.Context) (bool, error) {
			result, err := app.SessionNew(ctx)
			if err != nil {
				return false, err
			}
			return result.Cancelled, nil
		},
		RunDetached: func(fn func()) {
			app.runDetached(func(ctx context.Context) error { fn(); return nil })
		},
		ReloadNow: func() (string, bool, error) {
			// Upstream session.reload and then the mode's follow-ups: settings
			// re-read, queue modes, resource files and the system prompt (the
			// extension runner is out of scope, D41), keybindings, and implicit
			// project trust.
			app.Session.Reload()
			app.Keybindings.Reload()
			savedTrust := app.Trust.MaybeSaveImplicitProjectTrustAfterReload(app.AutoTrustOnReloadCwd)
			return app.Session.ModelRuntime().GetError(), savedTrust, nil
		},
		ApplyReloadedSettings: app.applyReloadedSettings}
}
