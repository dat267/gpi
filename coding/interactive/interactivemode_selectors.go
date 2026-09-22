package interactive

import (
	"context"
	"strings"

	"github.com/dat267/pier/ai"
	"github.com/dat267/pier/coding"
	"github.com/dat267/pier/tui"
)

// Port of the selector wiring of src/modes/interactive/interactive-mode.ts
// (showSelector, disposeActiveSelector, the thinking/user-message/trust/tree
// selector constructors and their command handlers).
//
// Divergences: the fork/navigate/dialog collaborators are injected function
// values (D116); the extension dialogs (showExtensionSelector/Editor) are
// seams (D41).

// SelectorSession is the session surface the selector wiring needs.
type SelectorSession interface {
	ThinkingLevel() ai.ThinkingLevel
	GetAvailableThinkingLevels() []ai.ThinkingLevel
	SetThinkingLevel(level ai.ThinkingLevel, options ...coding.ModelMutationOptions)
	GetUserMessagesForForking() []coding.UserMessageFork
	NavigateTree(ctx context.Context, targetID string, options coding.NavigateTreeOptions) (*coding.NavigateTreeResult, error)
	IsStreaming() bool
	IsCompacting() bool
	Abort(ctx context.Context)
	AbortBranchSummary()
}

// SelectorSlot manages the editor-replacement selector slot (upstream
// showSelector/disposeActiveSelector).
type SelectorSlot struct {
	UI              tui.TUI
	EditorContainer *tui.Container
	Editor          tui.Component

	activeToken   *int
	activeDispose func()
	nextToken     int
}

// NewSelectorSlot creates the slot.
func NewSelectorSlot(ui tui.TUI, editorContainer *tui.Container, editor tui.Component) *SelectorSlot {
	return &SelectorSlot{UI: ui, EditorContainer: editorContainer, Editor: editor}
}

// CreatedSelector is a created selector.
type CreatedSelector struct {
	Component tui.Component
	Focus     tui.Component
	Dispose   func()
}

// DisposeActiveSelector disposes the active selector without restoring the
// editor.
func (s *SelectorSlot) DisposeActiveSelector() {
	dispose := s.activeDispose
	s.activeToken = nil
	s.activeDispose = nil
	if dispose != nil {
		dispose()
	}
}

// Show replaces the editor with a selector.
func (s *SelectorSlot) Show(create func(done func()) CreatedSelector) {
	token := s.nextToken
	s.nextToken++
	done := func() {
		if s.activeDispose != nil {
			s.activeDispose()
		}
		if s.activeToken == nil || *s.activeToken != token {
			return
		}
		s.activeToken = nil
		s.activeDispose = nil
		if s.EditorContainer != nil {
			s.EditorContainer.Clear()
			if s.Editor != nil {
				s.EditorContainer.AddChild(s.Editor)
			}
		}
		if s.UI != nil && s.Editor != nil {
			s.UI.SetFocus(s.Editor)
		}
	}
	created := create(done)
	s.DisposeActiveSelector()
	s.activeToken = &token
	s.activeDispose = created.Dispose
	if s.EditorContainer != nil {
		s.EditorContainer.Clear()
		s.EditorContainer.AddChild(created.Component)
	}
	if s.UI != nil && created.Focus != nil {
		s.UI.SetFocus(created.Focus)
	}
	if s.UI != nil {
		s.UI.RequestRender(false)
	}
}

// HasActiveSelector reports whether a selector is shown (test helper).
func (s *SelectorSlot) HasActiveSelector() bool { return s.activeToken != nil }

// ActiveSelectorComponent returns the shown component (test helper).
func (s *SelectorSlot) ActiveSelectorComponent() tui.Component {
	if s.EditorContainer == nil || len(s.EditorContainer.Children) == 0 {
		return nil
	}
	return s.EditorContainer.Children[0]
}

// SelectorForkResult is the runtime fork outcome.
type SelectorForkResult struct {
	Cancelled    bool
	SelectedText *string
}

// SelectorWiring wires the selector command handlers.
type SelectorWiring struct {
	Slot        *SelectorSlot
	Session     SelectorSession
	Settings    *coding.SettingsManager
	SessionInfo *coding.SessionManager

	// RuntimeFork forks the session at an entry (runtimeHost.fork).
	RuntimeFork func(ctx context.Context, entryID string, atPosition bool) (*SelectorForkResult, error)
	// ShowStatus reports a status line.
	ShowStatus func(message string)
	// ShowError reports an error.
	ShowError func(message string)
	// UpdateEditorBorderColor refreshes the editor border.
	UpdateEditorBorderColor func()
	// AgentDir is the project trust store directory.
	AgentDir string
	// TerminalRows returns the terminal height.
	TerminalRows func() int
	// ShowExtensionSelector and ShowExtensionEditor are the extension dialogs
	// (seams; D41).
	ShowExtensionSelector func(ctx context.Context, title string, options []string) (string, bool)
	ShowExtensionEditor   func(ctx context.Context, title string) (string, bool)
	// ShowStatusIndicator/ShowTreeSelector are used by the tree navigation.
	ShowStatusIndicator func(kind StatusIndicatorKind)
	// RestoreQueuedMessagesToEditor restores the queue before an abort.
	RestoreQueuedMessagesToEditor func()
	// OnEditorText sets the editor text after a fork.
	OnEditorText func(text string)
}

func (w *SelectorWiring) showStatus(message string) {
	if w.ShowStatus != nil {
		w.ShowStatus(message)
	}
}

func (w *SelectorWiring) showError(message string) {
	if w.ShowError != nil {
		w.ShowError(message)
	}
}

// ShowThinkingSelector opens the thinking-level selector.
func (w *SelectorWiring) ShowThinkingSelector() {
	w.Slot.Show(func(done func()) CreatedSelector {
		selectLevel := func(level ai.ThinkingLevel, persist bool) {
			w.SelectThinkingLevel(level, persist)
			done()
		}
		current := string(w.Session.ThinkingLevel())
		if current == "" {
			current = string(coding.DefaultThinkingLevel)
		}
		defaultLevel := string(coding.DefaultThinkingLevel)
		if w.Settings != nil {
			if value := w.Settings.GetDefaultThinkingLevel(); value != nil && *value != "" {
				defaultLevel = *value
			}
		}
		selector := NewThinkingSelectorComponent(current, w.Session.GetAvailableThinkingLevels(),
			func(level string) { selectLevel(level, false) },
			func() {
				done()
				if w.Slot.UI != nil {
					w.Slot.UI.RequestRender(false)
				}
			},
			func(level string) { selectLevel(level, true) },
			defaultLevel)
		return CreatedSelector{Component: selector, Focus: selector}
	})
}

// HandleThinkingCommand handles `/thinking [level]`.
func (w *SelectorWiring) HandleThinkingCommand(searchTerm string) {
	availableLevels := w.Session.GetAvailableThinkingLevels()
	if strings.TrimSpace(searchTerm) == "" {
		w.ShowThinkingSelector()
		return
	}
	normalized := strings.ToLower(strings.TrimSpace(searchTerm))
	for _, level := range availableLevels {
		if strings.ToLower(string(level)) == normalized {
			w.SelectThinkingLevel(level, false)
			return
		}
	}
	names := make([]string, 0, len(availableLevels))
	for _, level := range availableLevels {
		names = append(names, string(level))
	}
	w.showError("Unknown thinking level \"" + searchTerm + "\". Available levels: " + strings.Join(names, ", ") + ".")
}

// SelectThinkingLevel sets the thinking level.
func (w *SelectorWiring) SelectThinkingLevel(level ai.ThinkingLevel, persist bool) {
	w.Session.SetThinkingLevel(level, coding.ModelMutationOptions{Persist: persist})
	if w.UpdateEditorBorderColor != nil {
		w.UpdateEditorBorderColor()
	}
	if persist {
		w.showStatus("Default thinking level: " + string(level))
		return
	}
	w.showStatus("Thinking level: " + string(level))
}

// ShowUserMessageSelector opens the fork-from-message selector.
func (w *SelectorWiring) ShowUserMessageSelector(ctx context.Context) {
	userMessages := w.Session.GetUserMessagesForForking()
	if len(userMessages) == 0 {
		w.showStatus("No messages to fork from")
		return
	}
	initialSelectedID := ""
	if len(userMessages) > 0 {
		initialSelectedID = userMessages[len(userMessages)-1].EntryID
	}
	items := make([]UserMessageItem, 0, len(userMessages))
	for _, message := range userMessages {
		items = append(items, UserMessageItem{ID: message.EntryID, Text: message.Text})
	}
	w.Slot.Show(func(done func()) CreatedSelector {
		var post func(func())
		if w.Slot.UI != nil {
			post = w.Slot.UI.Post
		}
		selector := NewUserMessageSelectorComponent(items,
			func(entryID string) {
				done()
				if w.RuntimeFork == nil {
					return
				}
				result, err := w.RuntimeFork(ctx, entryID, false)
				if err != nil {
					w.showError(err.Error())
					return
				}
				if result != nil && result.Cancelled {
					if w.Slot.UI != nil {
						w.Slot.UI.RequestRender(false)
					}
					return
				}
				if result != nil && w.OnEditorText != nil {
					selectedText := ""
					if result.SelectedText != nil {
						selectedText = *result.SelectedText
					}
					w.OnEditorText(selectedText)
				}
				w.showStatus("Forked to new session")
			},
			func() {
				done()
				if w.Slot.UI != nil {
					w.Slot.UI.RequestRender(false)
				}
			},
			initialSelectedID, post)
		return CreatedSelector{Component: selector, Focus: selector.GetMessageList()}
	})
}

// ShowTrustSelector opens the project trust selector.
func (w *SelectorWiring) ShowTrustSelector() {
	if w.SessionInfo == nil {
		return
	}
	cwd := w.SessionInfo.GetCwd()
	store := coding.NewProjectTrustStore(w.AgentDir)
	savedDecision := store.GetEntry(cwd)
	projectTrusted := true
	if w.Settings != nil {
		projectTrusted = w.Settings.IsProjectTrusted()
	}
	w.Slot.Show(func(done func()) CreatedSelector {
		selector := NewTrustSelectorComponent(TrustSelectorOptions{
			Cwd:            cwd,
			SavedDecision:  savedDecision,
			ProjectTrusted: projectTrusted,
			OnSelect: func(selection TrustSelection) {
				_ = store.SetMany(selection.Updates)
				done()
				state := "untrusted"
				if selection.Trusted {
					state = "trusted"
				}
				w.showStatus("Saved trust decision: " + state + ". Restart " + coding.AppName + " for this to take effect.")
			},
			OnCancel: func() {
				done()
				if w.Slot.UI != nil {
					w.Slot.UI.RequestRender(false)
				}
			},
		})
		return CreatedSelector{Component: selector, Focus: selector}
	})
}

// ShowTreeSelector opens the session tree selector and handles navigation.
func (w *SelectorWiring) ShowTreeSelector(ctx context.Context, initialSelectedID string, hasInitial bool) {
	if w.SessionInfo == nil {
		return
	}
	tree := w.SessionInfo.GetTree()
	realLeafID := w.SessionInfo.GetLeafID()
	if len(tree) == 0 {
		w.showStatus("No entries in session")
		return
	}
	terminalHeight := 40
	if w.TerminalRows != nil {
		terminalHeight = w.TerminalRows()
	}
	w.Slot.Show(func(done func()) CreatedSelector {
		selector := NewTreeSelectorComponent(tree, realLeafID, terminalHeight,
			func(entryID string) {
				w.handleTreeSelection(ctx, done, entryID)
			},
			func() {
				done()
				if w.Slot.UI != nil {
					w.Slot.UI.RequestRender(false)
				}
			},
			TreeSelectorOptions{})
		return CreatedSelector{Component: selector, Focus: selector}
	})
}

func (w *SelectorWiring) handleTreeSelection(ctx context.Context, done func(), entryID string) {
	if w.SessionInfo != nil && w.SessionInfo.GetLeafID() != nil && *w.SessionInfo.GetLeafID() == entryID {
		done()
		w.showStatus("Already at this point")
		return
	}

	done() // Close the selector first.

	wantsSummary := false
	customInstructions := ""
	// The summarization dialog is part of the extension UI surface; when it is
	// not wired the port skips the prompt (equivalent to the skip-prompt
	// setting), D116.
	promptForSummary := w.ShowExtensionSelector != nil
	if w.Settings != nil && w.Settings.GetBranchSummarySkipPrompt() {
		promptForSummary = false
	}
	if promptForSummary {
		for {
			choice, ok := w.showExtensionSelector(ctx, "Summarize branch?", []string{
				"No summary", "Summarize", "Summarize with custom prompt",
			})
			if !ok {
				// Escape: re-show the tree selector with the same selection.
				w.ShowTreeSelector(ctx, entryID, true)
				return
			}
			wantsSummary = choice != "No summary"
			if choice == "Summarize with custom prompt" {
				instructions, ok := w.showExtensionEditor(ctx, "Custom summarization instructions")
				if !ok {
					continue
				}
				customInstructions = instructions
			}
			break
		}
	}

	if w.Session.IsStreaming() {
		if w.RestoreQueuedMessagesToEditor != nil {
			w.RestoreQueuedMessagesToEditor()
		}
		w.Session.Abort(ctx)
	}
	if w.Session.IsCompacting() {
		w.showError("Wait for the current compaction or tree navigation to finish before navigating the session tree.")
		return
	}

	showingSummaryIndicator := false
	if wantsSummary {
		w.Session.AbortBranchSummary()
		if w.ShowStatusIndicator != nil {
			w.ShowStatusIndicator(StatusBranchSummary)
		}
		showingSummaryIndicator = true
		if w.Slot.UI != nil {
			w.Slot.UI.RequestRender(false)
		}
	}
	_ = showingSummaryIndicator

	result, err := w.Session.NavigateTree(ctx, entryID, coding.NavigateTreeOptions{
		Summarize: wantsSummary, CustomInstructions: customInstructions,
	})
	if err != nil {
		w.showError(err.Error())
		return
	}
	if result == nil {
		return
	}
	if result.Aborted {
		w.showStatus("Branch summarization cancelled")
		w.ShowTreeSelector(ctx, entryID, true)
		return
	}
	if result.Cancelled {
		w.showStatus("Navigation cancelled")
		return
	}
	w.showStatus("Navigated to selected point")
}

func (w *SelectorWiring) showExtensionSelector(ctx context.Context, title string, options []string) (string, bool) {
	if w.ShowExtensionSelector == nil {
		return "", false
	}
	return w.ShowExtensionSelector(ctx, title, options)
}

func (w *SelectorWiring) showExtensionEditor(ctx context.Context, title string) (string, bool) {
	if w.ShowExtensionEditor == nil {
		return "", false
	}
	return w.ShowExtensionEditor(ctx, title)
}

// newSelectorWiring assembles the SelectorWiring (port of the corresponding InteractiveMode wiring).
func newSelectorWiring(app *App) *SelectorWiring {
	return &SelectorWiring{
		Slot:                    app.Slot,
		Session:                 app.Session,
		Settings:                app.Settings,
		SessionInfo:             app.SessionMgr,
		AgentDir:                app.options.AgentDir,
		ShowStatus:              func(message string) { app.Transcript.ShowStatus(message) },
		ShowError:               func(message string) { app.showError(message) },
		UpdateEditorBorderColor: func() { app.updateEditorBorderColor() },
		TerminalRows:            func() int { return app.UI.GetTerminal().Rows() },
		ShowStatusIndicator:     func(kind StatusIndicatorKind) {},
		RestoreQueuedMessagesToEditor: func() {
			text := app.DefaultEditor.GetText()
			app.Queue.RestoreQueuedMessagesToEditor(true, text, text != "")
		},
		OnEditorText: func(text string) { app.DefaultEditor.SetText(text) },
	}
}
