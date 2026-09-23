package interactive

import (
	"context"
	"strings"
	"time"

	"github.com/dat267/pier/coding"
	"github.com/dat267/pier/tui"
)

// Port of the key-handler wiring and the editor submit handler of
// src/modes/interactive/interactive-mode.ts (setupKeyHandlers,
// handleStartupSubmit, setupEditorSubmitHandler).
//
// Divergences: the command handlers and the clipboard/browser helpers are
// injected as function values (D115); the extension-command branch is absent
// (D41).

// KeySession is the session surface the key handlers need.
type KeySession interface {
	IsStreaming() bool
	IsBashRunning() bool
	AbortBash()
}

// KeyWiring wires the app keybindings onto the editor.
type KeyWiring struct {
	Session  KeySession
	Editor   *CustomEditor
	Settings *coding.SettingsManager
	UI       tui.TUI
	Queue    *QueueController

	// Action handlers.
	OnClear              func()
	OnExit               func()
	OnSuspend            func()
	OnThinkingCycle      func()
	OnModelCycleForward  func()
	OnModelCycleBackward func()
	OnDebug              func()
	OnModelSelect        func()
	OnToolsExpand        func()
	OnThinkingToggle     func()
	OnExternalEditor     func()
	OnCopy               func()
	OnFollowUp           func()
	OnDequeue            func()
	OnSessionNew         func()
	OnSessionTree        func()
	OnSessionFork        func()
	OnSessionResume      func()
	OnPasteImage         func()

	// ShowTreeSelector and ShowUserMessageSelector are used by the double-escape
	// action.
	ShowTreeSelector        func()
	ShowUserMessageSelector func()

	lastEscapeTimeMS int64
}

// SetupKeyHandlers registers the app action handlers and the editor callbacks.
func (w *KeyWiring) SetupKeyHandlers(nowMS func() int64) {
	if w.Editor == nil {
		return
	}
	w.Editor.OnEscape = func() { w.HandleEscape(nowMS) }

	register := func(action tui.Keybinding, handler func()) {
		if handler != nil {
			w.Editor.OnAction(action, handler)
		}
	}
	register("app.clear", w.OnClear)
	register("app.exit", w.OnExit)
	register("app.suspend", w.OnSuspend)
	register("app.thinking.cycle", w.OnThinkingCycle)
	register("app.model.cycleForward", w.OnModelCycleForward)
	register("app.model.cycleBackward", w.OnModelCycleBackward)
	register("app.model.select", w.OnModelSelect)
	register("app.tools.expand", w.OnToolsExpand)
	register("app.thinking.toggle", w.OnThinkingToggle)
	register("app.editor.external", w.OnExternalEditor)
	register("app.message.copy", w.OnCopy)
	register("app.message.followUp", w.OnFollowUp)
	register("app.message.dequeue", w.OnDequeue)
	register("app.session.new", w.OnSessionNew)
	register("app.session.tree", w.OnSessionTree)
	register("app.session.fork", w.OnSessionFork)
	register("app.session.resume", w.OnSessionResume)

	if w.OnExit != nil {
		w.Editor.OnCtrlD = w.OnExit
	}
	if w.OnPasteImage != nil {
		w.Editor.OnPasteImage = w.OnPasteImage
	}
	w.Editor.OnChange = func(text string) {
		if w.Queue != nil {
			w.Queue.SetBashMode(strings.HasPrefix(strings.TrimLeft(text, " \t"), "!"))
		}
	}
}

// HandleEscape implements the escape handler (interrupt, bash abort, bash-mode
// exit and the double-escape tree/fork action).
func (w *KeyWiring) HandleEscape(nowMS func() int64) {
	switch {
	case w.Session != nil && w.Session.IsStreaming():
		if w.Queue != nil {
			w.Queue.RestoreQueuedMessagesToEditor(true, "", false)
		}
	case w.Session != nil && w.Session.IsBashRunning():
		w.Session.AbortBash()
	case w.Queue != nil && w.Queue.IsBashMode():
		if w.Editor != nil {
			w.Editor.SetText("")
		}
		w.Queue.SetBashMode(false)
	case w.Editor != nil && strings.TrimSpace(w.Editor.GetText()) == "":
		if w.Settings == nil {
			return
		}
		action := w.Settings.GetDoubleEscapeAction()
		if action == "none" {
			return
		}
		now := int64(0)
		if nowMS != nil {
			now = nowMS()
		}
		if now-w.lastEscapeTimeMS < 500 {
			if action == "tree" {
				if w.ShowTreeSelector != nil {
					w.ShowTreeSelector()
				}
			} else if w.ShowUserMessageSelector != nil {
				w.ShowUserMessageSelector()
			}
			w.lastEscapeTimeMS = 0
		} else {
			w.lastEscapeTimeMS = now
		}
	}
}

// LastEscapeTimeMS returns the last escape timestamp (test helper).
func (w *KeyWiring) LastEscapeTimeMS() int64 { return w.lastEscapeTimeMS }

// SubmitSession is the session surface the submit handler needs.
type SubmitSession interface {
	IsStreaming() bool
	IsCompacting() bool
	IsBashRunning() bool
	Prompt(ctx context.Context, text string, options *coding.PromptOptions) error
}

// SubmitHandlers are the slash-command handlers (injected; D115).
type SubmitHandlers struct {
	ShowSettingsSelector    func()
	ShowModelsSelector      func() error
	HandleModelCommand      func(searchTerm string) error
	HandleThinkingCommand   func(searchTerm string)
	HandleExportCommand     func(text string) error
	HandleImportCommand     func(text string) error
	HandleCopyCommand       func() error
	HandleNameCommand       func(text string)
	HandleSessionCommand    func()
	HandleHotkeysCommand    func()
	ShowUserMessageSelector func()
	HandleCloneCommand      func() error
	ShowTreeSelector        func()
	ShowTrustSelector       func()
	HandleLoginCommand      func(providerRef string) error
	ShowOAuthSelector       func(mode string)
	HandleClearCommand      func() error
	HandleCompactCommand    func(customInstructions string) error
	HandleReloadCommand     func() error
	HandleDebugCommand      func()
	HandleArminSaysHi       func()
	HandleDementedDelves    func()
	ShowSessionSelector     func()
	Shutdown                func() error
	HandleBashCommand       func(command string, excludeFromContext bool) error
}

// SubmitWiring handles editor submissions.
type SubmitWiring struct {
	Editor   *CustomEditor
	Session  SubmitSession
	Settings *coding.SettingsManager
	Queue    *QueueController
	Handlers SubmitHandlers

	// OnInput receives the submitted text (nil queues into PendingUserInputs).
	OnInput func(text string)
	// PendingUserInputs collects the text when OnInput is nil.
	PendingUserInputs *[]string
	// ShowStatus reports a status line.
	ShowStatus func(message string)
	// ShowWarning reports a warning.
	ShowWarning func(message string)
	// RequestRender requests a render.
	RequestRender func()
}

// HandleStartupSubmit shows the startup-in-progress status.
func (w *SubmitWiring) HandleStartupSubmit(text string) {
	if w.Editor != nil {
		w.Editor.SetText(text)
	}
	if w.ShowStatus != nil {
		w.ShowStatus("Startup is still in progress")
	}
}

// HandleSubmit processes one editor submission.
func (w *SubmitWiring) HandleSubmit(ctx context.Context, text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	editor := w.Editor
	clearEditor := func() {
		if editor != nil {
			editor.SetText("")
		}
	}
	addHistory := func(value string) {
		if editor != nil {
			editor.AddToHistory(value)
		}
	}

	// Command dispatch (order matches upstream).
	switch {
	case text == "/settings":
		if w.Handlers.ShowSettingsSelector != nil {
			w.Handlers.ShowSettingsSelector()
		}
		clearEditor()
		return
	case text == "/scoped-models":
		clearEditor()
		if w.Handlers.ShowModelsSelector != nil {
			_ = w.Handlers.ShowModelsSelector()
		}
		return
	case text == "/model" || strings.HasPrefix(text, "/model "):
		searchTerm := ""
		if strings.HasPrefix(text, "/model ") {
			searchTerm = strings.TrimSpace(text[len("/model "):])
		}
		clearEditor()
		if w.Handlers.HandleModelCommand != nil {
			_ = w.Handlers.HandleModelCommand(searchTerm)
		}
		return
	case text == "/thinking" || strings.HasPrefix(text, "/thinking "):
		searchTerm := ""
		if strings.HasPrefix(text, "/thinking ") {
			searchTerm = strings.TrimSpace(text[len("/thinking "):])
		}
		clearEditor()
		if w.Handlers.HandleThinkingCommand != nil {
			w.Handlers.HandleThinkingCommand(searchTerm)
		}
		return
	case text == "/export" || strings.HasPrefix(text, "/export "):
		if w.Handlers.HandleExportCommand != nil {
			_ = w.Handlers.HandleExportCommand(text)
		}
		clearEditor()
		return
	case text == "/import" || strings.HasPrefix(text, "/import "):
		if w.Handlers.HandleImportCommand != nil {
			_ = w.Handlers.HandleImportCommand(text)
		}
		clearEditor()
		return
	case text == "/copy":
		if w.Handlers.HandleCopyCommand != nil {
			_ = w.Handlers.HandleCopyCommand()
		}
		clearEditor()
		return
	case text == "/name" || strings.HasPrefix(text, "/name "):
		if w.Handlers.HandleNameCommand != nil {
			w.Handlers.HandleNameCommand(text)
		}
		clearEditor()
		return
	case text == "/session":
		if w.Handlers.HandleSessionCommand != nil {
			w.Handlers.HandleSessionCommand()
		}
		clearEditor()
		return
	case text == "/hotkeys":
		if w.Handlers.HandleHotkeysCommand != nil {
			w.Handlers.HandleHotkeysCommand()
		}
		clearEditor()
		return
	case text == "/fork":
		if w.Handlers.ShowUserMessageSelector != nil {
			w.Handlers.ShowUserMessageSelector()
		}
		clearEditor()
		return
	case text == "/clone":
		clearEditor()
		if w.Handlers.HandleCloneCommand != nil {
			_ = w.Handlers.HandleCloneCommand()
		}
		return
	case text == "/tree":
		if w.Handlers.ShowTreeSelector != nil {
			w.Handlers.ShowTreeSelector()
		}
		clearEditor()
		return
	case text == "/trust":
		if w.Handlers.ShowTrustSelector != nil {
			w.Handlers.ShowTrustSelector()
		}
		clearEditor()
		return
	case text == "/login" || strings.HasPrefix(text, "/login "):
		providerRef := ""
		if strings.HasPrefix(text, "/login ") {
			providerRef = strings.TrimSpace(text[len("/login "):])
		}
		clearEditor()
		if w.Handlers.HandleLoginCommand != nil {
			_ = w.Handlers.HandleLoginCommand(providerRef)
		}
		return
	case text == "/logout":
		if w.Handlers.ShowOAuthSelector != nil {
			w.Handlers.ShowOAuthSelector("logout")
		}
		clearEditor()
		return
	case text == "/new":
		clearEditor()
		if w.Handlers.HandleClearCommand != nil {
			_ = w.Handlers.HandleClearCommand()
		}
		return
	case text == "/compact" || strings.HasPrefix(text, "/compact "):
		customInstructions := ""
		if strings.HasPrefix(text, "/compact ") {
			customInstructions = strings.TrimSpace(text[len("/compact "):])
		}
		clearEditor()
		if w.Handlers.HandleCompactCommand != nil {
			_ = w.Handlers.HandleCompactCommand(customInstructions)
		}
		return
	case text == "/reload":
		clearEditor()
		if w.Handlers.HandleReloadCommand != nil {
			_ = w.Handlers.HandleReloadCommand()
		}
		return
	case text == "/debug":
		if w.Handlers.HandleDebugCommand != nil {
			w.Handlers.HandleDebugCommand()
		}
		clearEditor()
		return
	case text == "/arminsayshi":
		if w.Handlers.HandleArminSaysHi != nil {
			w.Handlers.HandleArminSaysHi()
		}
		clearEditor()
		return
	case text == "/dementedelves":
		if w.Handlers.HandleDementedDelves != nil {
			w.Handlers.HandleDementedDelves()
		}
		clearEditor()
		return
	case text == "/resume":
		if w.Handlers.ShowSessionSelector != nil {
			w.Handlers.ShowSessionSelector()
		}
		clearEditor()
		return
	case text == "/quit":
		clearEditor()
		if w.Handlers.Shutdown != nil {
			_ = w.Handlers.Shutdown()
		}
		return
	}

	// Bash commands.
	if strings.HasPrefix(text, "!") {
		isExcluded := strings.HasPrefix(text, "!!")
		command := strings.TrimSpace(text[1:])
		if isExcluded {
			command = strings.TrimSpace(text[2:])
		}
		if command != "" {
			if w.Session != nil && w.Session.IsBashRunning() {
				if w.ShowWarning != nil {
					w.ShowWarning("A bash command is already running. Press Esc to cancel it first.")
				}
				if editor != nil {
					editor.SetText(text)
				}
				return
			}
			addHistory(text)
			if w.Handlers.HandleBashCommand != nil {
				_ = w.Handlers.HandleBashCommand(command, isExcluded)
			}
			if w.Queue != nil {
				w.Queue.SetBashMode(false)
			}
			return
		}
	}

	// Queue input during compaction: upstream queueCompactionMessage queues
	// the message (steer mode) for after compaction — the session rejects
	// prompts while compacting, so a raw Prompt would silently drop it.
	if w.Session != nil && w.Session.IsCompacting() {
		if w.Queue != nil {
			w.Queue.QueueCompactionMessage(text, "steer")
			return
		}
		addHistory(text)
		clearEditor()
		_ = w.Session.Prompt(ctx, text, nil)
		return
	}

	// Streaming input steers the running turn.
	if w.Session != nil && w.Session.IsStreaming() {
		addHistory(text)
		clearEditor()
		_ = w.Session.Prompt(ctx, text, &coding.PromptOptions{StreamingBehavior: "steer"})
		if w.Queue != nil {
			w.Queue.UpdatePendingMessagesDisplay()
		}
		if w.RequestRender != nil {
			w.RequestRender()
		}
		return
	}

	// Normal submission.
	if w.Queue != nil {
		w.Queue.FlushPendingBashComponents()
	}
	if w.OnInput != nil {
		w.OnInput(text)
	} else if w.PendingUserInputs != nil {
		*w.PendingUserInputs = append(*w.PendingUserInputs, text)
	}
	addHistory(text)
}

// newKeyWiring assembles the KeyWiring (port of the corresponding InteractiveMode wiring).
func newKeyWiring(app *App) *KeyWiring {
	wiring := &KeyWiring{
		Session: app.Session,
		Editor:  app.DefaultEditor,
		OnPasteImage: func() {
			// Upstream handleClipboardPaste pastes a clipboard image first and
			// falls back to text; image transports are out of scope (D41
			// scope note in AGENTS.md), so the text path is ported.
			text, err := readClipboardText()
			if err != nil || text == "" {
				return
			}
			app.DefaultEditor.InsertTextAtCursor(text)
			app.UI.RequestRender(false)
		},
		Settings: app.Settings,
		UI:       app.UI,
		Queue:    app.Queue,
		OnExit:   func() { app.Lifecycle.Shutdown(false) },
		OnSuspend: func() {
			app.Lifecycle.HandleCtrlZ(func(message string) { app.Transcript.ShowStatus(message) }, nil)
		},
		OnThinkingCycle:      func() { app.Queue.CycleThinkingLevel() },
		OnModelCycleForward:  func() { _, _ = app.Queue.CycleModel(context.Background(), "forward") },
		OnModelCycleBackward: func() { _, _ = app.Queue.CycleModel(context.Background(), "backward") },
		OnModelSelect:        func() { app.Models.ShowModelSelector(context.Background(), "") },
		OnToolsExpand: func() {
			expanded := app.Display.ToolOutputExpanded
			app.Queue.ToggleToolOutputExpansion(&expanded, func(value bool) {
				app.Queue.SetToolsExpanded(value, &app.Display.ToolOutputExpanded, app.UIState.BuiltInHeader, app.LoadedResourcesContainer)
			})
		},
		OnThinkingToggle: func() {
			// Pass the live display flag, not a copy: it is what the next toggle
			// reads, so a copy left it stale and the second press re-derived the
			// same state — the setting stuck after one press.
			app.Queue.ToggleThinkingBlockVisibility(&app.Display.HideThinkingBlock)
		},
		OnFollowUp:      func() { app.Queue.HandleFollowUp(context.Background()) },
		OnDequeue:       app.Queue.HandleDequeue,
		OnSessionTree:   func() { app.Selectors.ShowTreeSelector(context.Background(), "", false) },
		OnSessionFork:   func() { app.Selectors.ShowUserMessageSelector(context.Background()) },
		OnSessionResume: app.Sessions.ShowSessionSelector,
		OnSessionNew: func() {
			if _, err := app.SessionNew(context.Background()); err != nil {
				app.showWarning(err.Error())
			}
		},
		ShowTreeSelector:        func() { app.Selectors.ShowTreeSelector(context.Background(), "", false) },
		ShowUserMessageSelector: func() { app.Selectors.ShowUserMessageSelector(context.Background()) },
	}
	wiring.OnClear = func() { app.Lifecycle.HandleCtrlC(func() { app.DefaultEditor.SetText("") }) }
	return wiring
}

// newSubmitWiring assembles the SubmitWiring (port of the corresponding InteractiveMode wiring).
func newSubmitWiring(app *App) *SubmitWiring {
	return &SubmitWiring{
		Editor:        app.DefaultEditor,
		Session:       app.Session,
		Settings:      app.Settings,
		Queue:         app.Queue,
		OnInput:       app.Startup.QueueUserInput,
		ShowStatus:    func(message string) { app.Transcript.ShowStatus(message) },
		ShowWarning:   func(message string) { app.showWarning(message) },
		RequestRender: func() { app.UI.RequestRender(false) },
		Handlers: SubmitHandlers{
			ShowSettingsSelector: app.SettingsW.ShowSettingsSelector,
			ShowModelsSelector:   func() error { app.Models.ShowModelsSelector(context.Background()); return nil },
			HandleModelCommand: func(searchTerm string) error {
				app.Models.ShowModelSelector(context.Background(), searchTerm)
				return nil
			},
			HandleThinkingCommand: app.Selectors.HandleThinkingCommand,
			HandleExportCommand:   func(text string) error { app.Commands.HandleExportCommand(context.Background(), text); return nil },
			HandleImportCommand:   func(text string) error { app.Commands.HandleImportCommand(context.Background(), text); return nil },
			HandleCopyCommand:     func() error { app.Commands.HandleCopyCommand(false, false); return nil },
			HandleNameCommand:     app.Commands.HandleNameCommand,
			// `!command` from the editor. Upstream emits a user_bash extension event
			// first; extension mechanics are out of scope (D41), so the built-in
			// execution is the whole path. The command deliberately runs off the UI
			// loop — upstream does not await it either — and every component
			// mutation is posted back, because the renderer paints concurrently with
			// this goroutine.
			HandleBashCommand: func(command string, excludeFromContext bool) error {
				app.runDetached(func(ctx context.Context) error {
					component := NewBashExecutionComponent(command, app.UI, excludeFromContext)
					deferred := app.Session.IsStreaming()
					app.UI.Post(func() {
						if deferred {
							app.Queue.PendingBash = append(app.Queue.PendingBash, component)
							app.PendingMessages.AddChild(component)
						} else {
							app.Chat.AddChild(component)
						}
						app.UI.RequestRender(false)
					})

					result, err := app.Session.ExecuteBash(ctx, command, func(chunk string) {
						app.UI.Post(func() {
							component.AppendOutput(chunk)
							app.UI.RequestRender(false)
						})
					}, &coding.ExecuteBashOptions{ExcludeFromContext: excludeFromContext})
					if err != nil {
						app.UI.Post(func() {
							component.SetComplete(nil, false, nil, "")
							app.showError("Bash command failed: " + err.Error())
						})
						return err
					}

					var truncation *coding.TruncationResult
					if result.Truncated {
						truncation = &coding.TruncationResult{Truncated: true, Content: result.Output}
					}
					app.UI.Post(func() {
						component.SetComplete(result.ExitCode, result.Cancelled, truncation, result.FullOutputPath)
						// The result also joins the session history, which is what puts it in
						// the transcript's replay (and keeps it out of the model's context
						// for the `!!` form).
						app.Session.RecordBashResult(command, *result, excludeFromContext)
						app.UI.RequestRender(false)
					})
					return nil
				})
				return nil
			},
			HandleSessionCommand:    func() { app.Commands.HandleSessionCommand(time.Now().UnixMilli()) },
			HandleHotkeysCommand:    app.Commands.HandleHotkeysCommand,
			ShowUserMessageSelector: func() { app.Selectors.ShowUserMessageSelector(context.Background()) },
			ShowTreeSelector:        func() { app.Selectors.ShowTreeSelector(context.Background(), "", false) },
			ShowTrustSelector:       app.Selectors.ShowTrustSelector,
			HandleLoginCommand: func(providerRef string) error {
				app.Auth.HandleLoginCommand(context.Background(), providerRef)
				return nil
			},
			ShowOAuthSelector:  func(mode string) { app.Auth.ShowOAuthSelector(context.Background(), mode) },
			HandleClearCommand: func() error { app.Commands.HandleClearCommand(context.Background()); return nil },
			HandleCompactCommand: func(instructions string) error {
				// The indicator is UI state (loop side); the compaction itself
				// only emits session events. It runs detached, not through
				// RunWork's single slot: upstream session.compact() aborts the
				// active run and compacts immediately, while a queued work item
				// would leave a mid-run /compact inert until the turn finished
				// on its own.
				app.Commands.ClearCompactionStatus()
				app.runDetached(func(ctx context.Context) error {
					app.Commands.CompactSession(ctx, instructions)
					return nil
				})
				return nil
			},
			HandleReloadCommand: func() error { app.Commands.HandleReloadCommand(); return nil },
			HandleDebugCommand: func() {
				app.Commands.HandleDebugCommand(time.Now().UTC().Format("2006-01-02T15:04:05.000Z"))
			},
			HandleArminSaysHi:    func() { app.Commands.HandleArminSaysHi(app.UI, time.Now().UnixNano()) },
			HandleDementedDelves: app.Commands.HandleDementedDelves,
			ShowSessionSelector:  app.Sessions.ShowSessionSelector,
			Shutdown:             func() error { app.Lifecycle.Shutdown(false); return nil },
		},
	}
}
