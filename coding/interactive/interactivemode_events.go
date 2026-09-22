package interactive

import (
	"context"

	"github.com/dat267/pier/agent"
	"github.com/dat267/pier/ai"
	"github.com/dat267/pier/coding"
	"github.com/dat267/pier/tui"
)

// Port of the agent-session event handling of
// src/modes/interactive/interactive-mode.ts: subscribeToAgent + handleEvent.
//
// Divergences: the mode's collaborators are injected as function values
// (D113); extension-related events are absent because extension mechanics are
// out of scope (D41).

// EventSession is the session surface the dispatcher needs.
type EventSession interface {
	RetryAttempt() int
	AbortRetry()
	AbortCompaction()
	IsStreaming() bool
}

// EventDispatcher handles the session events for the interactive mode.
type EventDispatcher struct {
	Transcript  *TranscriptRenderer
	UIState     *InteractiveUIState
	Footer      *FooterComponent
	Settings    *coding.SettingsManager
	Session     EventSession
	SessionInfo *coding.SessionManager
	Editor      *CustomEditor

	// TerminalProgress toggles the OSC 9;4 progress indicator.
	TerminalProgress func(active bool)
	// ShowError reports an error to the user.
	ShowError func(message string)
	// UpdatePendingMessagesDisplay refreshes the queued steering/follow-up
	// banner. Runs on queue_update so consumed messages clear the banner
	// (upstream parity: updatePendingMessagesDisplay).
	UpdatePendingMessagesDisplay func()
	// FlushCompactionQueue drains the queued compaction messages.
	FlushCompactionQueue func(willRetry bool)
	// StartWork runs blocking work off the UI loop. The flush can start a
	// turn, so it must not run inside event application (which would stop the
	// loop from draining that turn's events). Nil runs inline (tests).
	StartWork func(fn func(context.Context) error)
	// CheckShutdownRequested handles a pending shutdown.
	CheckShutdownRequested func()
	// Init initializes the mode on the first event.
	Init func()

	// Initialized reports whether init already ran.
	Initialized bool

	Display       *DisplayOptions
	MarkdownTheme *tui.MarkdownTheme
	Transformers  []MarkdownTransformer

	streamingComponent *AssistantMessageComponent
	streamingMessage   *ai.AssistantMessage
	pendingTools       map[string]*ToolExecutionComponent

	retryEscapeHandler          func()
	hasRetryEscapeHandler       bool
	autoCompactionEscapeHandler func()
	hasAutoCompactionEscape     bool
}

// NewEventDispatcher creates the dispatcher.
func NewEventDispatcher(transcript *TranscriptRenderer, uiState *InteractiveUIState, footer *FooterComponent, settings *coding.SettingsManager, session EventSession, sessionInfo *coding.SessionManager, editor *CustomEditor) *EventDispatcher {
	return &EventDispatcher{
		Transcript:   transcript,
		UIState:      uiState,
		Footer:       footer,
		Settings:     settings,
		Session:      session,
		SessionInfo:  sessionInfo,
		Editor:       editor,
		Display:      &DisplayOptions{},
		pendingTools: map[string]*ToolExecutionComponent{},
	}
}

// StreamingComponent returns the in-progress assistant component (test helper).
func (d *EventDispatcher) StreamingComponent() *AssistantMessageComponent {
	return d.streamingComponent
}

// PendingTools returns the pending tool components (test helper).
func (d *EventDispatcher) PendingTools() map[string]*ToolExecutionComponent { return d.pendingTools }

func (d *EventDispatcher) requestRender() {
	if d.Transcript != nil {
		d.Transcript.requestRender()
	}
}

// HandleEvent processes one session event.
func (d *EventDispatcher) HandleEvent(event *coding.SessionEvent) {
	if !d.Initialized {
		d.Initialized = true
		if d.Init != nil {
			d.Init()
		}
	}
	if d.Footer != nil {
		d.Footer.Invalidate()
	}

	switch event.Type {
	case coding.SessionAgentStart:
		d.pendingTools = map[string]*ToolExecutionComponent{}
		if d.hasRetryEscapeHandler && d.Editor != nil {
			d.Editor.OnEscape = d.retryEscapeHandler
			d.retryEscapeHandler = nil
			d.hasRetryEscapeHandler = false
		}

	case coding.SessionTurnStart:
		if d.Settings != nil && d.Settings.GetShowTerminalProgress() && d.TerminalProgress != nil {
			d.TerminalProgress(true)
		}
		if d.UIState != nil {
			if d.UIState.WorkingVisible {
				if d.UIState.ActiveStatusIndicator == nil || d.UIState.ActiveStatusIndicator.IndicatorKind() != StatusWorking {
					d.UIState.ShowWorkingStatusIndicator(d.thinkingLevel())
				}
			} else {
				d.UIState.ClearStatusIndicator("", false)
			}
		}
		d.requestRender()

	case coding.SessionQueueUpdate:
		if d.UpdatePendingMessagesDisplay != nil {
			d.UpdatePendingMessagesDisplay()
		}
		d.requestRender()

	case coding.SessionEntryAppended:
		if event.Entry == nil {
			return
		}
		if event.Entry.Type == "custom" && d.Transcript != nil {
			d.Transcript.AddCustomEntryToChat(event.Entry)
			d.requestRender()
		} else if event.Entry.Type == "usage" && event.Entry.Kind == "cache_warm" && d.Transcript != nil {
			d.Transcript.AddCacheWarmingUsage(event.Entry)
			d.requestRender()
		}

	case coding.SessionInfoChanged:
		if d.Footer != nil {
			d.Footer.Invalidate()
		}
		d.requestRender()

	case coding.SessionThinkingLevelChanged:
		if d.Footer != nil {
			d.Footer.Invalidate()
		}

	case coding.SessionMessageStart:
		d.handleMessageStart(event)

	case coding.SessionMessageUpdate:
		d.handleMessageUpdate(event)

	case coding.SessionMessageEnd:
		d.handleMessageEnd(event)

	case coding.SessionBashExecutionUpdate:
		// The bash execution callback handles the TUI output rendering.

	case coding.SessionToolExecutionStart:
		d.handleToolExecutionStart(event)

	case coding.SessionToolExecutionUpdate:
		d.handleToolExecutionUpdate(event)

	case coding.SessionToolExecutionEnd:
		d.handleToolExecutionEnd(event)

	case coding.SessionAgentEnd:
		if d.Settings != nil && d.Settings.GetShowTerminalProgress() && d.TerminalProgress != nil {
			d.TerminalProgress(false)
		}
		if d.UIState != nil {
			d.UIState.ClearStatusIndicator(StatusWorking, true)
		}
		if d.streamingComponent != nil && d.Transcript != nil {
			d.Transcript.Chat.RemoveChild(d.streamingComponent)
			d.streamingComponent = nil
			d.streamingMessage = nil
		}
		d.pendingTools = map[string]*ToolExecutionComponent{}
		d.requestRender()

	case coding.SessionAgentSettled:
		if d.CheckShutdownRequested != nil {
			d.CheckShutdownRequested()
		}

	case coding.SessionCompactionStart:
		d.handleCompactionStart(event)

	case coding.SessionCompactionEnd:
		d.handleCompactionEnd(event)

	case coding.SessionAutoRetryStart:
		if d.Editor != nil {
			d.retryEscapeHandler = d.Editor.OnEscape
			d.hasRetryEscapeHandler = true
			d.Editor.OnEscape = func() {
				if d.Session != nil {
					d.Session.AbortRetry()
				}
			}
		}
		if d.UIState != nil {
			d.UIState.ShowStatusIndicator(NewRetryStatusIndicator(d.UIState.UI, event.Attempt, event.MaxAttempts, int(event.DelayMS)))
		}
		d.requestRender()

	case coding.SessionAutoRetryEnd:
		if d.hasRetryEscapeHandler && d.Editor != nil {
			d.Editor.OnEscape = d.retryEscapeHandler
			d.retryEscapeHandler = nil
			d.hasRetryEscapeHandler = false
		}
		if d.UIState != nil {
			d.UIState.ClearStatusIndicator(StatusRetry, true)
		}
		if !event.Success {
			if d.ShowError != nil {
				finalError := event.ErrorMessage
				if finalError == "" {
					finalError = "Unknown error"
				}
				d.ShowError("Retry failed after " + itoa(event.Attempt) + " attempts: " + finalError)
			}
		}
		d.requestRender()

	case coding.SessionSummarizationRetryScheduled:
		if d.ShowError != nil && event.ErrorMessage != "" {
			d.ShowError(event.ErrorMessage)
		}
		if d.UIState != nil {
			d.UIState.ShowStatusIndicator(NewRetryStatusIndicator(d.UIState.UI, event.Attempt, event.MaxAttempts, int(event.DelayMS)))
		}
		d.requestRender()

	case coding.SessionSummarizationRetryAttemptStart:
		if d.UIState != nil {
			d.UIState.ClearStatusIndicator(StatusRetry, true)
			if event.Source == "branchSummary" {
				d.UIState.ShowStatusIndicator(NewBranchSummaryStatusIndicator(d.UIState.UI))
			} else {
				d.UIState.ShowStatusIndicator(NewCompactionStatusIndicator(d.UIState.UI, event.Reason))
			}
		}
		d.requestRender()

	case coding.SessionSummarizationRetryFinished:
		if d.UIState != nil {
			d.UIState.ClearStatusIndicator(StatusRetry, true)
		}
		d.requestRender()
	}
}

func (d *EventDispatcher) thinkingLevel() string {
	if d.SessionInfo != nil {
		if model := d.SessionInfo.GetEntries(); len(model) > 0 {
			_ = model
		}
	}
	return ""
}

func (d *EventDispatcher) handleMessageStart(event *coding.SessionEvent) {
	message := eventMessage(event)
	if message == nil {
		return
	}
	switch typed := message.(type) {
	case *ai.CustomMessage:
		if typed.Role == coding.RoleCustom && d.Transcript != nil {
			d.Transcript.AddMessageToChat(message, false)
			d.requestRender()
		}
	case *ai.UserMessage:
		if d.Transcript != nil {
			d.Transcript.AddMessageToChat(message, false)
			d.requestRender()
		}
	case *ai.AssistantMessage:
		if d.Transcript == nil {
			return
		}
		d.streamingComponent = NewAssistantMessageComponent(nil, d.Display.HideThinkingBlock, d.MarkdownTheme,
			d.Display.HiddenThinkingLabel, d.Display.OutputPad, d.Transformers)
		d.streamingMessage = typed
		d.Transcript.Chat.AddChild(d.streamingComponent)
		d.Transcript.StreamingComponent = d.streamingComponent
		d.streamingComponent.UpdateContent(d.streamingMessage, true)
		d.requestRender()
	}
}

func (d *EventDispatcher) handleMessageUpdate(event *coding.SessionEvent) {
	message := eventMessage(event)
	assistant, ok := message.(*ai.AssistantMessage)
	if !ok || d.streamingComponent == nil {
		return
	}
	d.streamingMessage = assistant
	d.streamingComponent.UpdateContent(d.streamingMessage, true)
	d.syncToolCallComponents(assistant)
	d.requestRender()
}

// syncToolCallComponents adds or updates the tool components for an assistant
// message's tool calls.
func (d *EventDispatcher) syncToolCallComponents(assistant *ai.AssistantMessage) {
	for _, content := range assistant.Content {
		toolCall, ok := content.(ai.ToolCall)
		if !ok {
			continue
		}
		if existing, ok := d.pendingTools[toolCall.ID]; ok {
			existing.UpdateArgs(toolCall.Arguments)
			continue
		}
		component := d.newToolComponent(toolCall.Name, toolCall.ID, toolCall.Arguments)
		if component == nil {
			continue
		}
		d.pendingTools[toolCall.ID] = component
	}
}

func (d *EventDispatcher) newToolComponent(toolName string, toolCallID string, args any) *ToolExecutionComponent {
	if d.Transcript == nil {
		return nil
	}
	var definition *ToolRenderers
	if d.Transcript.Session != nil {
		definition = d.Transcript.Session.GetToolRenderers(toolName)
	}
	options := ToolExecutionOptions{}
	if d.Settings != nil {
		showImages := d.Settings.GetShowImages()
		options.ShowImages = &showImages
		options.ImageWidthCells = d.Settings.GetImageWidthCells()
	}
	cwd := ""
	if d.SessionInfo != nil {
		cwd = d.SessionInfo.GetCwd()
	}
	component := NewToolExecutionComponent(toolName, toolCallID, args, options, definition, d.Transcript.UI, cwd)
	component.SetExpanded(d.Display.ToolOutputExpanded)
	d.Transcript.Chat.AddChild(component)
	return component
}

func (d *EventDispatcher) handleMessageEnd(event *coding.SessionEvent) {
	message := eventMessage(event)
	if _, ok := message.(*ai.UserMessage); ok {
		return
	}
	assistant, ok := message.(*ai.AssistantMessage)
	if !ok || d.streamingComponent == nil {
		return
	}
	d.streamingMessage = assistant
	errorMessage := ""
	if assistant.StopReason == ai.StopAborted {
		retryAttempt := 0
		if d.Session != nil {
			retryAttempt = d.Session.RetryAttempt()
		}
		if retryAttempt > 0 {
			suffix := "s"
			if retryAttempt == 1 {
				suffix = ""
			}
			errorMessage = "Aborted after " + itoa(retryAttempt) + " retry attempt" + suffix
		} else {
			errorMessage = "Operation aborted"
		}
		assistant.ErrorMessage = &errorMessage
	}
	d.streamingComponent.UpdateContent(d.streamingMessage, false)

	if assistant.StopReason == ai.StopAborted || assistant.StopReason == ai.StopError {
		if errorMessage == "" {
			if assistant.ErrorMessage != nil && *assistant.ErrorMessage != "" {
				errorMessage = *assistant.ErrorMessage
			} else {
				errorMessage = "Error"
			}
		}
		for _, component := range d.pendingTools {
			component.UpdateResult(&SortToolResultContent{
				Content: []ToolResultContent{{Type: "text", Text: errorMessage}},
				IsError: true,
			}, false)
		}
		d.pendingTools = map[string]*ToolExecutionComponent{}
	} else {
		for _, component := range d.pendingTools {
			component.SetArgsComplete()
		}
		if d.Transcript != nil {
			d.Transcript.MaybeShowThinkingDropNotice(assistant)
			d.Transcript.MaybeShowCacheMissNotice(assistant)
		}
	}
	d.streamingComponent = nil
	d.streamingMessage = nil
	if d.Transcript != nil {
		d.Transcript.StreamingComponent = nil
	}
	if d.Footer != nil {
		d.Footer.Invalidate()
	}
	d.requestRender()
}

func (d *EventDispatcher) handleToolExecutionStart(event *coding.SessionEvent) {
	agentEvent := event.Agent
	if agentEvent == nil {
		return
	}
	component, ok := d.pendingTools[agentEvent.ToolCallID]
	if !ok {
		component = d.newToolComponent(agentEvent.ToolName, agentEvent.ToolCallID, agentEvent.Args)
		if component == nil {
			return
		}
		d.pendingTools[agentEvent.ToolCallID] = component
	}
	component.MarkExecutionStarted()
	d.requestRender()
}

func (d *EventDispatcher) handleToolExecutionUpdate(event *coding.SessionEvent) {
	agentEvent := event.Agent
	if agentEvent == nil {
		return
	}
	component, ok := d.pendingTools[agentEvent.ToolCallID]
	if !ok {
		return
	}
	result := sortToolResultFromAgent(agentEvent.PartialResult)
	result.IsError = false
	component.UpdateResult(result, true)
	d.requestRender()
}

func (d *EventDispatcher) handleToolExecutionEnd(event *coding.SessionEvent) {
	agentEvent := event.Agent
	if agentEvent == nil {
		return
	}
	component, ok := d.pendingTools[agentEvent.ToolCallID]
	if !ok {
		return
	}
	result := sortToolResultFromAgent(agentEvent.Result)
	result.IsError = agentEvent.IsError
	component.UpdateResult(result, false)
	delete(d.pendingTools, agentEvent.ToolCallID)
	d.requestRender()
}

func (d *EventDispatcher) handleCompactionStart(event *coding.SessionEvent) {
	if d.Settings != nil && d.Settings.GetShowTerminalProgress() && d.TerminalProgress != nil {
		d.TerminalProgress(true)
	}
	if d.Editor != nil {
		d.autoCompactionEscapeHandler = d.Editor.OnEscape
		d.hasAutoCompactionEscape = true
		d.Editor.OnEscape = func() {
			if d.Session != nil {
				d.Session.AbortCompaction()
			}
		}
	}
	if d.UIState != nil {
		d.UIState.ShowStatusIndicator(NewCompactionStatusIndicator(d.UIState.UI, event.Reason))
	}
	d.requestRender()
}

func (d *EventDispatcher) handleCompactionEnd(event *coding.SessionEvent) {
	if d.Settings != nil && d.Settings.GetShowTerminalProgress() && d.TerminalProgress != nil {
		d.TerminalProgress(false)
	}
	if d.hasAutoCompactionEscape && d.Editor != nil {
		d.Editor.OnEscape = d.autoCompactionEscapeHandler
		d.autoCompactionEscapeHandler = nil
		d.hasAutoCompactionEscape = false
	}
	if d.UIState != nil {
		d.UIState.ClearStatusIndicator(StatusCompaction, true)
	}
	switch {
	case event.Aborted:
		if event.Reason == coding.CompactionManual {
			if d.ShowError != nil {
				d.ShowError("Compaction cancelled")
			}
		} else if d.Transcript != nil {
			d.Transcript.ShowStatus("Auto-compaction cancelled")
		}
	case event.Result != nil:
		if d.Transcript != nil && d.SessionInfo != nil {
			entries := d.SessionInfo.BuildContextEntriesForLeaf()
			if len(entries) > 0 && entries[0].Type == "compaction" {
				d.Transcript.Chat.Clear()
				d.Transcript.RenderSessionEntries(entries[1:], false, false)
				d.Transcript.AddMessageToChat(coding.CreateCompactionSummaryMessage(
					event.Result.Summary, event.Result.TokensBefore, 0), false)
				if event.Result.Usage != nil {
					d.Transcript.AddCompactionCostNotice(CompactionCostNotice{
						Type: "compaction_cost", Kind: "compaction", Usage: *event.Result.Usage,
					})
				}
			}
		}
		if d.Footer != nil {
			d.Footer.Invalidate()
		}
	case event.ErrorMessage != "":
		if event.Reason == coding.CompactionManual {
			if d.ShowError != nil {
				d.ShowError(event.ErrorMessage)
			}
		} else if d.Transcript != nil {
			theme := ActiveTheme()
			d.Transcript.Chat.AddChild(tui.NewSpacer(1))
			d.Transcript.Chat.AddChild(tui.NewText(theme.Fg("error", event.ErrorMessage), 1, 0, nil))
		}
	}
	if d.FlushCompactionQueue != nil {
		flush := d.FlushCompactionQueue
		if d.StartWork != nil {
			d.StartWork(func(context.Context) error {
				flush(event.WillRetry)
				return nil
			})
		} else {
			flush(event.WillRetry)
		}
	}
	d.requestRender()
}

// eventMessage extracts the message from a session event.
func eventMessage(event *coding.SessionEvent) ai.Message {
	if event.Agent == nil {
		return nil
	}
	return event.Agent.Message
}

// sortToolResultFromAgent converts an agent tool result into the render payload.
func sortToolResultFromAgent(result agent.AgentToolResult) *SortToolResultContent {
	converted := &SortToolResultContent{}
	for _, block := range result.Content {
		if text, ok := block.(ai.TextContent); ok {
			converted.Content = append(converted.Content, ToolResultContent{Type: "text", Text: text.Text})
		}
	}
	if result.Details != nil {
		converted.Details = result.Details
	}
	return converted
}
