package coding

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/dat267/pier/ai"
)

// Port of the prompt pipeline of core/agent-session.ts: prompt with streaming
// behaviors, skill/prompt-template expansion, pending-message flushing, model
// and auth validation, pre-send compaction, custom-message buffering,
// sendUserMessage, abort, and waitForIdle.
//
// D41: the extension input handlers, extension commands, and before-agent-start
// hooks are extension mechanics and omitted.

// PromptOptions configure a prompt submission.
type PromptOptions struct {
	// ExpandPromptTemplates expands /skill: and /template commands (default true).
	ExpandPromptTemplates *bool
	// StreamingBehavior queues the prompt when a run is active: "steer" or
	// "followUp". Without it, prompting during a run is an error.
	StreamingBehavior string
	// Images are image attachments (upstream ImageContent[]).
	Images []ai.ImageContent
	// Source labels the input origin ("interactive", "extension", "rpc").
	Source string
	// SessionID is the request's session id; warming restarts only from
	// session requests (compaction and summaries use their own routing ids).
	SessionID string
	// PreflightResult observes whether the prompt was queued/executed (true) or
	// dropped (false).
	PreflightResult func(ok bool)
}

// InputSource names where user input came from.
type InputSource = string

const (
	InputSourceInteractive InputSource = "interactive"
	InputSourceExtension   InputSource = "extension"
	InputSourceRPC         InputSource = "rpc"
)

// promptState is the session's prompt buffering state.
type promptState struct {
	mu sync.Mutex
	// pendingCustomMessages await the end of the current turn.
	pendingCustomMessages []*ai.CustomMessage
	// pendingNextTurnMessages are injected with the next user prompt.
	pendingNextTurnMessages []ai.Message
	// runActive mirrors upstream's _isAgentRunActive.
	runActive bool
	// idleWait resolves once the session becomes idle.
	idleWait chan struct{}
}

func (s *AgentSession) prompt() *promptState {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.promptState == nil {
		s.promptState = &promptState{}
	}
	return s.promptState
}

// cacheContextIsCurrent reports whether the live transcript still extends the
// warmed request's prefix (upstream cacheContextIsCurrent; message identity is
// approximated by comparing the transcript length and model).
func (s *AgentSession) cacheContextIsCurrent() func() bool {
	requestModel := s.Model()
	messages := s.Agent.State().Messages
	return func() bool {
		currentModel := s.Model()
		currentMessages := s.Agent.State().Messages
		return ai.ModelsAreEqual(currentModel, requestModel) &&
			len(messages) <= len(currentMessages)
	}
}

// GetCacheWarmingStatus reports the warmer's state.
func (s *AgentSession) GetCacheWarmingStatus() *CacheWarmingStatus {
	if s.CacheWarmer == nil {
		return nil
	}
	status := s.CacheWarmer.Status()
	return &status
}

// SetCacheWarmingMode persists the warming mode and reconciles the warmer.
func (s *AgentSession) SetCacheWarmingMode(mode CacheWarmingMode) {
	if s.control != nil && s.control.Settings != nil {
		s.control.Settings.SetCacheWarmingMode(mode)
	}
	if s.CacheWarmer != nil {
		s.CacheWarmer.OnModeChanged()
	}
}

// Prompt submits text to the session, expanding skill commands and prompt
// templates and honoring the streaming behavior.
func (s *AgentSession) Prompt(ctx context.Context, text string, options *PromptOptions) error {
	if options == nil {
		options = &PromptOptions{}
	}
	expandTemplates := options.ExpandPromptTemplates == nil || *options.ExpandPromptTemplates
	preflight := func(ok bool) {
		if options.PreflightResult != nil {
			options.PreflightResult(ok)
		}
	}

	if s.IsCompacting() {
		preflight(false)
		return fmt.Errorf("Cannot submit a prompt while compaction is in progress. Wait for compaction to finish and retry.")
	}

	expandedText := text
	if expandTemplates {
		expandedText = s.expandSkillCommand(expandedText)
		expandedText = ExpandPromptTemplate(expandedText, s.PromptTemplates())
	}

	// While streaming, the prompt must be queued with an explicit behavior.
	if s.IsStreaming() {
		if options.StreamingBehavior == "" {
			preflight(false)
			return fmt.Errorf("Agent is already processing. Specify streamingBehavior ('steer' or 'followUp') to queue the message.")
		}
		if options.StreamingBehavior == "followUp" {
			s.queueFollowUp(expandedText, options.Images)
		} else {
			s.queueSteer(expandedText, options.Images)
		}
		preflight(true)
		return nil
	}

	// Flush deferred messages before the new turn.
	s.FlushPendingBashMessages()
	s.FlushPendingCustomMessages()

	if !s.HasModel() {
		preflight(false)
		return fmt.Errorf("%s", FormatNoModelSelectedMessage())
	}
	if err := s.validateModelAuth(ctx); err != nil {
		preflight(false)
		return err
	}

	// Catch an aborted response before sending the new prompt.
	if last := s.findLastAssistantMessage(); last != nil {
		if err := s.checkCompaction(ctx, last, false); err != nil {
			preflight(false)
			return err
		}
	}

	// Build the prompt: the user message plus any pending next-turn messages.
	userContent := ai.ContentList{ai.TextContent{Text: expandedText}}
	for _, image := range options.Images {
		userContent = append(userContent, image)
	}
	messages := []ai.Message{&ai.UserMessage{Content: ai.StringOrBlocks{Blocks: userContent}, Timestamp: time.Now().UnixMilli()}}
	messages = append(messages, s.takePendingNextTurnMessages()...)

	preflight(true)
	if s.CacheWarmer != nil && options.SessionID != "" && options.SessionID == s.SessionID() {
		requestOptions := &ai.SimpleStreamOptions{SessionID: options.SessionID}
		if len(options.Images) > 0 {
			// Images ride in the user message; the warm request replays the same
			// context without them only when the model accepts it, so keep them.
		}
		s.CacheWarmer.Start(CacheWarmRequest{
			Model: s.Model(), Context: ai.Context{Messages: messages}, Options: requestOptions,
		}, s.cacheContextIsCurrent())
	}
	return s.runAgentPrompt(ctx, messages)
}

func (s *AgentSession) queueSteer(text string, images []ai.ImageContent) {
	content := ai.ContentList{ai.TextContent{Text: text}}
	for _, image := range images {
		content = append(content, image)
	}
	s.Steer(&ai.UserMessage{Content: ai.StringOrBlocks{Blocks: content}})
}

func (s *AgentSession) queueFollowUp(text string, images []ai.ImageContent) {
	content := ai.ContentList{ai.TextContent{Text: text}}
	for _, image := range images {
		content = append(content, image)
	}
	s.FollowUp(&ai.UserMessage{Content: ai.StringOrBlocks{Blocks: content}})
}

// validateModelAuth checks that the selected model's provider has usable auth.
func (s *AgentSession) validateModelAuth(ctx context.Context) error {
	if s.control == nil || s.control.ModelRuntime == nil {
		return nil
	}
	runtime := s.control.ModelRuntime
	model := s.Model()
	if runtime.HasConfiguredAuth(model.Provider) {
		return nil
	}
	if check, err := runtime.CheckAuth(model.Provider, ctx); err == nil && check != nil {
		return nil
	}
	if runtime.IsUsingOAuth(model.Provider) {
		return fmt.Errorf("Authentication failed for %q. Credentials may have expired or network is unavailable. Run '/login %s' to re-authenticate.",
			model.Provider, model.Provider)
	}
	return fmt.Errorf("%s", FormatNoAPIKeyFoundMessage(model.Provider))
}

// runAgentPrompt runs one agent prompt with the run-active flag and idle
// resolution.
func (s *AgentSession) runAgentPrompt(ctx context.Context, messages []ai.Message) error {
	state := s.prompt()
	state.mu.Lock()
	state.runActive = true
	state.mu.Unlock()

	err := s.Agent.PromptMessages(ctx, messages)
	if err == nil {
		// Post-run continuation: auto-retry, then queued continuations
		// (upstream's _runAgentPrompt loop).
		err = s.runPostRunLoop(ctx)
	}

	state.mu.Lock()
	state.runActive = false
	state.mu.Unlock()
	s.FlushPendingBashMessages()
	s.FlushPendingCustomMessages()
	if s.CacheWarmer != nil {
		s.CacheWarmer.OnAgentSettled()
	}
	s.emit(&SessionEvent{Type: SessionAgentSettled})
	s.resolveIdleWaitIfIdle()
	return err
}

// findLastAssistantMessage returns the most recent assistant message.
func (s *AgentSession) findLastAssistantMessage() *ai.AssistantMessage {
	messages := s.Messages()
	for index := len(messages) - 1; index >= 0; index-- {
		if assistant, ok := messages[index].(*ai.AssistantMessage); ok {
			return assistant
		}
	}
	return nil
}

// checkCompaction runs the automatic compaction decision (upstream
// _checkCompaction; skipAbortedCheck=false is the pre-prompt check).
func (s *AgentSession) checkCompaction(ctx context.Context, message *ai.AssistantMessage, skipAbortedCheck bool) error {
	_, err := s.CheckCompaction(ctx, message, skipAbortedCheck)
	return err
}

// SendUserMessage sends a user message, optionally delivering it as a queued
// steer/follow-up message.
func (s *AgentSession) SendUserMessage(ctx context.Context, content any, deliverAs string) error {
	var text string
	var images []ai.ImageContent
	switch typed := content.(type) {
	case string:
		text = typed
	case ai.ContentList:
		var parts []string
		for _, block := range typed {
			switch value := block.(type) {
			case ai.TextContent:
				parts = append(parts, value.Text)
			case ai.ImageContent:
				images = append(images, value)
			}
		}
		text = strings.Join(parts, "\n")
	}
	expand := false
	return s.Prompt(ctx, text, &PromptOptions{
		ExpandPromptTemplates: &expand, StreamingBehavior: deliverAs, Images: images, Source: InputSourceExtension,
	})
}

// SendMessageOptions configure SendMessage.
type SendMessageOptions struct {
	// TriggerTurn starts a turn for the message (default true).
	TriggerTurn *bool
	// DeliverAs queues as "steer" or "followUp" when a run is active.
	DeliverAs string
}

// SendMessage injects a custom message into the conversation.
func (s *AgentSession) SendMessage(ctx context.Context, message *ai.CustomMessage, options *SendMessageOptions) error {
	if options == nil {
		options = &SendMessageOptions{}
	}
	triggerTurn := options.TriggerTurn == nil || *options.TriggerTurn

	state := s.prompt()
	switch {
	case triggerTurn && !s.IsStreaming():
		return s.runAgentPrompt(ctx, []ai.Message{message})
	case s.IsStreaming():
		if !triggerTurn {
			// Appending now would sit between a tool call and its result.
			state.mu.Lock()
			state.pendingCustomMessages = append(state.pendingCustomMessages, message)
			state.mu.Unlock()
			return nil
		}
		s.AppendCustomMessage(message)
		return nil
	default:
		s.AppendCustomMessage(message)
		return nil
	}
}

// AppendCustomMessage appends a custom message to the transcript and session.
func (s *AgentSession) AppendCustomMessage(message *ai.CustomMessage) {
	messages := append(s.Agent.State().Messages, ai.Message(message))
	s.Agent.SetMessages(messages)
	s.Sessions.AppendCustomMessageEntry(message.Role, string(message.Content), false, nil)
}

// FlushPendingCustomMessages appends custom messages queued during a run.
func (s *AgentSession) FlushPendingCustomMessages() {
	state := s.prompt()
	state.mu.Lock()
	pending := state.pendingCustomMessages
	state.pendingCustomMessages = nil
	state.mu.Unlock()
	for _, message := range pending {
		s.AppendCustomMessage(message)
	}
}

// QueueNextTurnMessage queues a message injected alongside the next prompt.
func (s *AgentSession) QueueNextTurnMessage(message ai.Message) {
	state := s.prompt()
	state.mu.Lock()
	state.pendingNextTurnMessages = append(state.pendingNextTurnMessages, message)
	state.mu.Unlock()
}

func (s *AgentSession) takePendingNextTurnMessages() []ai.Message {
	state := s.prompt()
	state.mu.Lock()
	defer state.mu.Unlock()
	pending := state.pendingNextTurnMessages
	state.pendingNextTurnMessages = nil
	return pending
}

// Abort aborts the current operation and waits for idle.
func (s *AgentSession) Abort(ctx context.Context) {
	s.AbortRetry()
	s.AbortBash()
	s.Agent.Abort()
	_ = s.waitForSessionIdle(ctx)
}

// waitForSessionIdle blocks until no agent run or compaction is active,
// resolving as soon as either finishes.
func (s *AgentSession) waitForSessionIdle(ctx context.Context) error {
	if s.IsIdle() {
		return nil
	}
	state := s.prompt()
	state.mu.Lock()
	if state.idleWait == nil {
		state.idleWait = make(chan struct{})
	}
	wait := state.idleWait
	state.mu.Unlock()

	select {
	case <-wait:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// resolveIdleWaitIfIdle releases the idle waiters once nothing is active.
func (s *AgentSession) resolveIdleWaitIfIdle() {
	if !s.IsIdle() {
		return
	}
	state := s.prompt()
	state.mu.Lock()
	wait := state.idleWait
	state.idleWait = nil
	state.mu.Unlock()
	if wait != nil {
		close(wait)
	}
}

// expandSkillCommand expands a /skill:name command into its skill block.
func (s *AgentSession) expandSkillCommand(text string) string {
	if !strings.HasPrefix(text, "/skill:") {
		return text
	}
	spaceIndex := strings.Index(text, " ")
	skillName := text[len("/skill:"):]
	args := ""
	if spaceIndex != -1 {
		skillName = text[len("/skill:"):spaceIndex]
		args = strings.TrimSpace(text[spaceIndex+1:])
	}

	var skill *Skill
	if s.SystemPromptOptions != nil {
		for index := range s.SystemPromptOptions.Skills {
			if s.SystemPromptOptions.Skills[index].Name == skillName {
				skill = &s.SystemPromptOptions.Skills[index]
				break
			}
		}
	}
	if skill == nil {
		return text
	}
	content, err := os.ReadFile(skill.FilePath)
	if err != nil {
		return text
	}
	body := strings.TrimSpace(StripFrontmatter(string(content)))
	block := fmt.Sprintf("<skill name=%q location=%q>\nReferences are relative to %s.\n\n%s\n</skill>",
		skill.Name, skill.FilePath, skill.BaseDir, body)
	if args != "" {
		return block + "\n\n" + args
	}
	return block
}
