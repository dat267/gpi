package interactive

import (
	"context"
	"strings"

	"github.com/dat267/pier/ai"
	"github.com/dat267/pier/coding"
	"github.com/dat267/pier/tui"
)

// Port of the queue/pending-message management and the small key actions of
// src/modes/interactive/interactive-mode.ts (getAllQueuedMessages,
// clearAllQueues, updatePendingMessagesDisplay, restoreQueuedMessagesToEditor,
// queueCompactionMessage, flushCompactionQueue, flushPendingBashComponents,
// the tool-output/thinking toggles and the editor border color).
//
// Divergences: the extension-command check is always false because extension
// mechanics are out of scope (D41/D114).

// QueueSession is the session surface the queue controller needs.
type QueueSession interface {
	IsStreaming() bool
	IsCompacting() bool
	GetSteeringMessages() []string
	GetFollowUpMessages() []string
	ClearQueue() (steering []string, followUp []string)
	Prompt(ctx context.Context, text string, options *coding.PromptOptions) error
	Steer(message ai.Message)
	FollowUp(message ai.Message)
	Abort(ctx context.Context)
	CycleThinkingLevel(options coding.ModelMutationOptions) (ai.ThinkingLevel, bool)
	CycleModel(ctx context.Context, direction string, options coding.ModelMutationOptions) (*coding.ModelCycleResult, error)
	SupportsThinking() bool
	ThinkingLevel() ai.ThinkingLevel
}

// QueueController manages the queued-message display and the editor actions.
type QueueController struct {
	UI            tui.TUI
	Session       QueueSession
	Settings      *coding.SettingsManager
	Editor        *CustomEditor
	Chat          *tui.Container
	PendingBash   []tui.Component
	PendingParent *tui.Container

	// ShowStatus reports a transient status line.
	ShowStatus func(message string)
	// ShowError reports an error.
	ShowError func(message string)
	// ShowWarning reports a warning.
	ShowWarning func(message string)
	// RequestRender requests a render.
	RequestRender func()

	// deferredThinking holds the messages a visibility sweep has not rebuilt
	// yet, oldest-first; MaterializeThinkingChunk drains it.
	deferredThinking []*AssistantMessageComponent
	// pendingThinkingHide is the visibility those messages are waiting for.
	pendingThinkingHide bool

	// compactionQueuedMessages are the messages queued during compaction.
	compactionQueuedMessages []CompactionQueuedMessage

	isBashMode bool
}

// NewQueueController creates the controller.
func NewQueueController(ui tui.TUI, session QueueSession, settings *coding.SettingsManager, editor *CustomEditor, chat *tui.Container, pendingParent *tui.Container) *QueueController {
	return &QueueController{
		UI: ui, Session: session, Settings: settings, Editor: editor, Chat: chat, PendingParent: pendingParent,
	}
}

// ClearCompactionQueue drops the messages queued during a compaction without
// sending them (upstream resetting compactionQueuedMessages when a session is
// replaced: they belong to the session that is going away).
func (c *QueueController) ClearCompactionQueue() {
	c.compactionQueuedMessages = nil
}

// SetBashMode updates the bash-mode flag (drives the editor border color).
func (c *QueueController) SetBashMode(isBashMode bool) {
	if c.isBashMode == isBashMode {
		return
	}
	c.isBashMode = isBashMode
	c.UpdateEditorBorderColor()
}

// IsBashMode reports the bash-mode flag.
func (c *QueueController) IsBashMode() bool { return c.isBashMode }

// CompactionQueuedMessages returns the compaction queue (test helper).
func (c *QueueController) CompactionQueuedMessages() []CompactionQueuedMessage {
	return append([]CompactionQueuedMessage{}, c.compactionQueuedMessages...)
}

func (c *QueueController) requestRender() {
	if c.RequestRender != nil {
		c.RequestRender()
	} else if c.UI != nil {
		c.UI.RequestRender(false)
	}
}

func (c *QueueController) showStatus(message string) {
	if c.ShowStatus != nil {
		c.ShowStatus(message)
	}
}

func (c *QueueController) showError(message string) {
	if c.ShowError != nil {
		c.ShowError(message)
	}
}

func (c *QueueController) showWarning(message string) {
	if c.ShowWarning != nil {
		c.ShowWarning(message)
	}
}

// GetAllQueuedMessages returns the session queues plus the compaction queue.
func (c *QueueController) GetAllQueuedMessages() (steering []string, followUp []string) {
	steering = append(steering, c.Session.GetSteeringMessages()...)
	followUp = append(followUp, c.Session.GetFollowUpMessages()...)
	for _, message := range c.compactionQueuedMessages {
		if message.Mode == "steer" {
			steering = append(steering, message.Text)
		} else {
			followUp = append(followUp, message.Text)
		}
	}
	return steering, followUp
}

// ClearAllQueues clears both the session queues and the compaction queue.
func (c *QueueController) ClearAllQueues() (steering []string, followUp []string) {
	sessionSteering, sessionFollowUp := c.Session.ClearQueue()
	steering = append(steering, sessionSteering...)
	followUp = append(followUp, sessionFollowUp...)
	for _, message := range c.compactionQueuedMessages {
		if message.Mode == "steer" {
			steering = append(steering, message.Text)
		} else {
			followUp = append(followUp, message.Text)
		}
	}
	c.compactionQueuedMessages = nil
	return steering, followUp
}

// HandleFollowUp is the app.message.followUp action (alt+enter): queue the
// editor's text as a follow-up message while the agent streams, act like Enter
// when it is idle, and queue the message for after the compaction while one
// runs (port of handleFollowUp).
func (c *QueueController) HandleFollowUp(ctx context.Context) {
	text := strings.TrimSpace(c.Editor.GetExpandedText())
	if text == "" {
		return
	}

	if c.Session.IsCompacting() {
		// Extension commands execute immediately upstream; extension mechanics
		// are out of scope (D41/D114), so IsExtensionCommand is always false.
		if c.IsExtensionCommand(text) {
			c.Editor.AddToHistory(text)
			c.Editor.SetText("")
			_ = c.Session.Prompt(ctx, text, nil)
		} else {
			c.QueueCompactionMessage(text, "followUp")
		}
		return
	}

	if c.Session.IsStreaming() {
		c.Editor.AddToHistory(text)
		c.Editor.SetText("")
		if err := c.Session.Prompt(ctx, text, &coding.PromptOptions{StreamingBehavior: "followUp"}); err != nil {
			// Upstream awaits the prompt without a handler, which surfaces as an
			// unhandled rejection; the port reports it.
			c.showError(err.Error())
		}
		c.UpdatePendingMessagesDisplay()
		c.requestRender()
		return
	}

	// Nothing is running: alt+enter behaves like Enter.
	if c.Editor.OnSubmit != nil {
		c.Editor.SetText("")
		c.Editor.OnSubmit(text)
	}
}

// UpdatePendingMessagesDisplay re-renders the pending-message container.
func (c *QueueController) UpdatePendingMessagesDisplay() {
	if c.PendingParent == nil {
		return
	}
	theme := ActiveTheme()
	c.PendingParent.Clear()
	steering, followUp := c.GetAllQueuedMessages()
	if len(steering) == 0 && len(followUp) == 0 {
		return
	}
	c.PendingParent.AddChild(tui.NewSpacer(1))
	for _, message := range steering {
		c.PendingParent.AddChild(tui.NewTruncatedText(theme.Fg("dim", "Steering: "+message), 1, 0))
	}
	for _, message := range followUp {
		c.PendingParent.AddChild(tui.NewTruncatedText(theme.Fg("dim", "Follow-up: "+message), 1, 0))
	}
	dequeueHint := KeyDisplayText("app.message.dequeue")
	c.PendingParent.AddChild(tui.NewTruncatedText(
		theme.Fg("dim", "↳ "+dequeueHint+" to edit all queued messages"), 1, 0))
}

// RestoreQueuedMessagesToEditor moves the queued messages into the editor.
func (c *QueueController) RestoreQueuedMessagesToEditor(abort bool, currentText string, hasCurrentText bool) int {
	steering, followUp := c.ClearAllQueues()
	allQueued := append(append([]string{}, steering...), followUp...)
	if len(allQueued) == 0 {
		c.UpdatePendingMessagesDisplay()
		if abort {
			c.Session.Abort(context.Background())
		}
		return 0
	}
	queuedText := strings.Join(allQueued, "\n\n")
	editorText := c.Editor.GetText()
	if hasCurrentText {
		editorText = currentText
	}
	parts := []string{}
	if strings.TrimSpace(queuedText) != "" {
		parts = append(parts, queuedText)
	}
	if strings.TrimSpace(editorText) != "" {
		parts = append(parts, editorText)
	}
	c.Editor.SetText(strings.Join(parts, "\n\n"))
	c.UpdatePendingMessagesDisplay()
	if abort {
		c.Session.Abort(context.Background())
	}
	return len(allQueued)
}

// QueueCompactionMessage queues a message for after compaction.
func (c *QueueController) QueueCompactionMessage(text string, mode string) {
	c.compactionQueuedMessages = append(c.compactionQueuedMessages, CompactionQueuedMessage{Text: text, Mode: mode})
	if c.Editor != nil {
		c.Editor.AddToHistory(text)
		c.Editor.SetText("")
	}
	c.UpdatePendingMessagesDisplay()
	c.showStatus("Queued message for after compaction")
}

// IsExtensionCommand reports whether the text is an extension command
// (always false: extension mechanics are out of scope, D41/D114).
func (c *QueueController) IsExtensionCommand(text string) bool { return false }

// FlushCompactionQueue sends the queued messages after compaction.
func (c *QueueController) FlushCompactionQueue(ctx context.Context, willRetry bool) {
	if len(c.compactionQueuedMessages) == 0 {
		return
	}
	queuedMessages := append([]CompactionQueuedMessage{}, c.compactionQueuedMessages...)
	c.compactionQueuedMessages = nil
	c.UpdatePendingMessagesDisplay()

	restoreQueue := func(err error) {
		c.Session.ClearQueue()
		c.compactionQueuedMessages = queuedMessages
		c.UpdatePendingMessagesDisplay()
		suffix := ""
		if len(queuedMessages) > 1 {
			suffix = "s"
		}
		c.showError("Failed to send queued message" + suffix + ": " + err.Error())
	}

	if willRetry {
		for _, message := range queuedMessages {
			if c.IsExtensionCommand(message.Text) {
				_ = c.Session.Prompt(ctx, message.Text, nil)
			} else if message.Mode == "followUp" {
				c.Session.FollowUp(userMessage(message.Text))
			} else {
				c.Session.Steer(userMessage(message.Text))
			}
		}
		c.UpdatePendingMessagesDisplay()
		return
	}

	firstPromptIndex := -1
	for index, message := range queuedMessages {
		if !c.IsExtensionCommand(message.Text) {
			firstPromptIndex = index
			break
		}
	}
	if firstPromptIndex == -1 {
		for _, message := range queuedMessages {
			_ = c.Session.Prompt(ctx, message.Text, nil)
		}
		return
	}

	preCommands := queuedMessages[:firstPromptIndex]
	firstPrompt := queuedMessages[firstPromptIndex]
	rest := queuedMessages[firstPromptIndex+1:]

	for _, message := range preCommands {
		_ = c.Session.Prompt(ctx, message.Text, nil)
	}

	if err := c.Session.Prompt(ctx, firstPrompt.Text, &coding.PromptOptions{StreamingBehavior: firstPrompt.Mode}); err != nil {
		restoreQueue(err)
	}

	for _, message := range rest {
		if c.IsExtensionCommand(message.Text) {
			_ = c.Session.Prompt(ctx, message.Text, nil)
		} else if message.Mode == "followUp" {
			c.Session.FollowUp(userMessage(message.Text))
		} else {
			c.Session.Steer(userMessage(message.Text))
		}
	}
	c.UpdatePendingMessagesDisplay()
}

// FlushPendingBashComponents moves pending bash components into the chat.
func (c *QueueController) FlushPendingBashComponents() {
	for _, component := range c.PendingBash {
		if c.PendingParent != nil {
			c.PendingParent.RemoveChild(component)
		}
		if c.Chat != nil {
			c.Chat.AddChild(component)
		}
	}
	c.PendingBash = nil
}

// HandleDequeue restores the queued messages and reports the outcome.
func (c *QueueController) HandleDequeue() {
	restored := c.RestoreQueuedMessagesToEditor(false, "", false)
	if restored == 0 {
		c.showStatus("No queued messages to restore")
		return
	}
	suffix := ""
	if restored > 1 {
		suffix = "s"
	}
	c.showStatus("Restored " + itoa(restored) + " queued message" + suffix + " to editor")
}

// UpdateEditorBorderColor applies the bash/thinking border color.
func (c *QueueController) UpdateEditorBorderColor() {
	if c.Editor == nil {
		return
	}
	theme := ActiveTheme()
	if c.isBashMode {
		c.Editor.BorderColor = theme.GetBashModeBorderColor()
	} else {
		level := "off"
		if c.Session != nil {
			if current := string(c.Session.ThinkingLevel()); current != "" {
				level = current
			}
		}
		c.Editor.BorderColor = theme.GetThinkingBorderColor(level)
	}
	c.requestRender()
}

// CycleThinkingLevel advances the thinking level.
func (c *QueueController) CycleThinkingLevel() {
	level, ok := c.Session.CycleThinkingLevel(coding.ModelMutationOptions{})
	if !ok {
		c.showStatus("Current model does not support thinking")
		return
	}
	c.UpdateEditorBorderColor()
	c.showStatus("Thinking level: " + string(level))
}

// CycleModel switches to the next/previous model in scope.
func (c *QueueController) CycleModel(ctx context.Context, direction string) (*coding.ModelCycleResult, error) {
	result, err := c.Session.CycleModel(ctx, direction, coding.ModelMutationOptions{})
	if err != nil {
		c.showError(err.Error())
		return nil, err
	}
	if result == nil {
		c.showStatus("Only one model available")
		return nil, nil
	}
	c.UpdateEditorBorderColor()
	thinking := ""
	if result.Model != nil && result.Model.Reasoning && string(result.ThinkingLevel) != "off" {
		thinking = " (thinking: " + string(result.ThinkingLevel) + ")"
	}
	name := ""
	if result.Model != nil {
		name = result.Model.ID
	}
	c.showStatus("Switched to " + name + thinking)
	return result, nil
}

// ToggleToolOutputExpansion flips the tool-output expansion state.
func (c *QueueController) ToggleToolOutputExpansion(expanded *bool, setExpanded func(bool)) {
	setExpanded(!*expanded)
}

// SetToolsExpanded updates the expansion state of the header and chat children.
func (c *QueueController) SetToolsExpanded(expanded bool, current *bool, header tui.Component, loadedResources *tui.Container) {
	if expanded == *current {
		return
	}
	*current = expanded
	if expandable, ok := IsExpandable(header); ok {
		expandable.SetExpanded(expanded)
	}
	for _, container := range []*tui.Container{loadedResources, c.Chat} {
		if container == nil {
			continue
		}
		for _, child := range container.Children {
			if expandable, ok := IsExpandable(child); ok {
				expandable.SetExpanded(expanded)
			}
		}
	}
	state := "collapsed"
	if expanded {
		state = "expanded"
	}
	c.showStatus("Tool output: " + state)
}

// UpdateThinkingBlockVisibility re-renders the assistant messages.
//
// The sweep is the expensive half of ctrl+t: rebuilding a message drops its
// rendered cache, and the next paint re-renders it, so a transcript-wide sweep
// costs about half a millisecond per message with thinking in it — a visible
// freeze on a long session. Only the tail (what is on screen) is rebuilt here;
// the rest drains through MaterializeThinkingChunk on the loop beat, the same
// way a large session replay materialises (see TranscriptRenderer).
func (c *QueueController) UpdateThinkingBlockVisibility(hideThinkingBlock bool) {
	if c.Chat == nil {
		return
	}
	c.deferredThinking = c.deferredThinking[:0]
	c.pendingThinkingHide = hideThinkingBlock

	children := c.Chat.Children
	remaining := thinkingSweepWindowComponents
	for index := len(children) - 1; index >= 0; index-- {
		assistant, ok := children[index].(*AssistantMessageComponent)
		if !ok {
			continue
		}
		if remaining > 0 {
			remaining--
			assistant.SetHideThinkingBlock(hideThinkingBlock)
			continue
		}
		c.deferredThinking = append(c.deferredThinking, assistant)
	}
	c.requestRender()
}

// MaterializeThinkingChunk applies the pending visibility to one small chunk of
// deferred messages and reports whether more remains. The chunk is small because
// the next paint renders every message it touches: a large chunk would only move
// the freeze into that frame.
func (c *QueueController) MaterializeThinkingChunk() bool {
	if len(c.deferredThinking) == 0 {
		return false
	}
	for n := 0; n < thinkingSweepChunkComponents && len(c.deferredThinking) > 0; n++ {
		last := len(c.deferredThinking) - 1
		assistant := c.deferredThinking[last]
		c.deferredThinking[last] = nil
		c.deferredThinking = c.deferredThinking[:last]
		assistant.SetHideThinkingBlock(c.pendingThinkingHide)
	}
	c.requestRender()
	return len(c.deferredThinking) > 0
}

const (
	// thinkingSweepWindowComponents is how many trailing messages a visibility
	// sweep rebuilds synchronously: enough to cover the screen.
	thinkingSweepWindowComponents = 8
	// thinkingSweepChunkComponents is how many deferred messages are rebuilt per
	// loop beat.
	thinkingSweepChunkComponents = 8
)

// ToggleThinkingBlockVisibility flips the thinking-block visibility.
func (c *QueueController) ToggleThinkingBlockVisibility(hideThinkingBlock *bool) {
	*hideThinkingBlock = !*hideThinkingBlock
	if c.Settings != nil {
		c.Settings.SetHideThinkingBlock(*hideThinkingBlock)
	}
	c.UpdateThinkingBlockVisibility(*hideThinkingBlock)
	state := "visible"
	if *hideThinkingBlock {
		state = "hidden"
	}
	c.showStatus("Thinking blocks: " + state)
}

// userMessage wraps text in a user message.
func userMessage(text string) ai.Message {
	return &ai.UserMessage{Content: ai.StringOrBlocks{Text: text}}
}
