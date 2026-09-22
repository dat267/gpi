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
// handleExport/Import/Share/Bug/Copy/Name/Session/Changelog/Hotkeys/Clear/
// Debug/Compact commands, the easter eggs and stop).
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
	// ShowExtensionConfirm shows a confirm dialog (extension UI seam).
	ShowExtensionConfirm func(ctx context.Context, title string, message string) (bool, error)
	// PromptForMissingCwd resolves a missing-cwd error.
	PromptForMissingCwd func(ctx context.Context, message string) (string, bool)
	// ShareSession runs the session-share flow.
	ShareSession func(ctx context.Context) error
	// CopyToClipboard copies text, returning (ok, message).
	CopyToClipboard func(text string) (bool, string)
	// CopyActiveSelection copies the alt-screen selection.
	CopyActiveSelection func() bool
	// WriteDebugLog writes the debug log.
	WriteDebugLog func(content string) error
	// MarkdownTheme is used for the changelog/hotkeys rendering.
	MarkdownTheme func() tui.MarkdownTheme
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
	if w.ShowExtensionConfirm != nil {
		confirmed, err := w.ShowExtensionConfirm(ctx, "Import session", "Replace current session with "+inputPath+"?")
		if err != nil || !confirmed {
			w.showStatus("Import cancelled")
			return
		}
	}
	if w.ImportFromJSONL == nil {
		w.showStatus("Import cancelled")
		return
	}
	if w.ClearStatusIndicator != nil {
		w.ClearStatusIndicator()
	}
	cancelled, err := w.ImportFromJSONL(ctx, inputPath, "")
	if err == nil {
		if cancelled {
			w.showStatus("Import cancelled")
			return
		}
		w.showStatus("Session imported from: " + inputPath)
		return
	}
	// A missing cwd can be resolved by prompting for one.
	if w.PromptForMissingCwd != nil && strings.Contains(err.Error(), "cwd") {
		selectedCwd, ok := w.PromptForMissingCwd(ctx, err.Error())
		if !ok {
			w.showStatus("Import cancelled")
			return
		}
		cancelled, err = w.ImportFromJSONL(ctx, inputPath, selectedCwd)
		if err == nil {
			if cancelled {
				w.showStatus("Import cancelled")
				return
			}
			w.showStatus("Session imported from: " + inputPath)
			return
		}
	}
	w.showError("Failed to import session: " + err.Error())
}

// HandleShareCommand runs the share flow.
func (w *CommandWiring) HandleShareCommand(ctx context.Context) {
	if w.ShareSession == nil {
		return
	}
	if err := w.ShareSession(ctx); err != nil {
		w.showError(err.Error())
	}
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
func (w *CommandWiring) HandleSessionCommand(now int64) {
	theme := ActiveTheme()
	stats := w.Session.GetSessionStats()
	sessionName := w.SessionInfo.GetSessionName()
	entries := w.SessionInfo.GetEntries()
	cacheWaste := coding.ComputeCacheWaste(entries, w.Session.ModelRuntime())
	usageBreakdown := coding.GetUsageCostBreakdown(entries)

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

	w.Chat.AddChild(tui.NewSpacer(1))
	w.Chat.AddChild(tui.NewText(info.String(), 1, 0, nil))
	w.requestRender()
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
