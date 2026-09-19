package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/dat267/gpi/ai"
)

// Port of packages/agent/src/agent.ts: the stateful wrapper around the
// low-level loop.

// AgentState is the public agent state.
type AgentState struct {
	// SystemPrompt is the current prompt, replayed from the transcript's
	// system messages (read-only).
	SystemPrompt string
	// Model is the active model for future turns.
	Model *ai.Model
	// ThinkingLevel is the requested reasoning level for future turns.
	ThinkingLevel ai.ThinkingLevel
	// Tools are the executable tools; differences from the transcript's
	// declarations are announced to the model before the next request.
	Tools []AgentTool
	// Messages is the conversation transcript.
	Messages []ai.Message
	// IsStreaming is true while the agent is processing a prompt or
	// continuation (until awaited agent_end listeners settle).
	IsStreaming bool
	// StreamingMessage is the partial assistant message for the current
	// streamed response, if any.
	StreamingMessage ai.Message
	// PendingToolCalls are the tool call ids currently executing.
	PendingToolCalls map[string]bool
	// ErrorMessage is from the most recent failed or aborted turn.
	ErrorMessage string
}

// AgentInitialState seeds a new Agent.
type AgentInitialState struct {
	SystemPrompt  string
	Model         *ai.Model
	ThinkingLevel ai.ThinkingLevel
	Tools         []AgentTool
	Messages      []ai.Message
}

// AgentOptions are Agent construction options.
type AgentOptions struct {
	InitialState     *AgentInitialState
	ConvertToLlm     func(messages []ai.Message) []ai.Message
	TransformContext func(messages []ai.Message, ctx context.Context) []ai.Message
	// StreamFn is required.
	StreamFn                   StreamFn
	GetAPIKey                  func(provider string, ctx context.Context) (string, error)
	OnPayload                  func(payload json.RawMessage, model *ai.Model) json.RawMessage
	OnResponse                 func(response ai.ProviderResponse, model *ai.Model)
	BeforeToolCall             func(context *BeforeToolCallContext, ctx context.Context) (*BeforeToolCallResult, error)
	AfterToolCall              func(context *AfterToolCallContext, ctx context.Context) (*AfterToolCallResult, error)
	ShouldStopAfterTurn        func(context *ShouldStopAfterTurnContext, ctx context.Context) bool
	PrepareNextTurn            func(ctx context.Context) (*AgentLoopTurnUpdate, error)
	PrepareNextTurnWithContext func(context *ShouldStopAfterTurnContext, ctx context.Context) (*AgentLoopTurnUpdate, error)
	SteeringMode               QueueMode
	FollowUpMode               QueueMode
	// SessionID is forwarded to providers for cache-aware backends.
	SessionID string
	// ThinkingBudgets are forwarded to the stream function.
	ThinkingBudgets *ai.ThinkingBudgets
	// Transport is forwarded to the stream function.
	Transport ai.Transport
	// MaxRetryDelayMS caps provider-requested retry delays.
	MaxRetryDelayMS *int
	// ToolExecution is the strategy for multi-tool-call messages.
	ToolExecution ToolExecutionMode
}

// pendingMessageQueue is a mode-aware message queue.
type pendingMessageQueue struct {
	mu       sync.Mutex
	messages []ai.Message
	mode     QueueMode
}

func (q *pendingMessageQueue) enqueue(message ai.Message) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.messages = append(q.messages, message)
}

func (q *pendingMessageQueue) hasItems() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.messages) > 0
}

func (q *pendingMessageQueue) drain() []ai.Message {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.mode == QueueModeAll {
		drained := q.messages
		q.messages = nil
		return drained
	}
	if len(q.messages) == 0 {
		return nil
	}
	first := q.messages[0]
	q.messages = q.messages[1:]
	return []ai.Message{first}
}

func (q *pendingMessageQueue) clear() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.messages = nil
}

func (q *pendingMessageQueue) setMode(mode QueueMode) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.mode = mode
}

func (q *pendingMessageQueue) getMode() QueueMode {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.mode
}

type activeRun struct {
	cancel context.CancelFunc
	done   chan struct{}
}

// Listener receives agent events. It runs in subscription order during the
// run settlement; the second argument is the active run's abort context.
type Listener func(event AgentEvent, ctx context.Context) error

// Agent owns the current transcript, emits lifecycle events, executes tools,
// and exposes queueing APIs for steering and follow-up messages.
type Agent struct {
	mu    sync.Mutex
	state *AgentState

	listeners    map[*int]Listener
	listenerKeys []*int
	listenerMu   sync.Mutex

	steeringQueue *pendingMessageQueue
	followUpQueue *pendingMessageQueue

	// Options surface (upstream public fields).
	ConvertToLlm               func(messages []ai.Message) []ai.Message
	TransformContext           func(messages []ai.Message, ctx context.Context) []ai.Message
	StreamFunction             StreamFn
	GetAPIKey                  func(provider string, ctx context.Context) (string, error)
	OnPayload                  func(payload json.RawMessage, model *ai.Model) json.RawMessage
	OnResponse                 func(response ai.ProviderResponse, model *ai.Model)
	BeforeToolCall             func(context *BeforeToolCallContext, ctx context.Context) (*BeforeToolCallResult, error)
	AfterToolCall              func(context *AfterToolCallContext, ctx context.Context) (*AfterToolCallResult, error)
	ShouldStopAfterTurn        func(context *ShouldStopAfterTurnContext, ctx context.Context) bool
	PrepareNextTurn            func(ctx context.Context) (*AgentLoopTurnUpdate, error)
	PrepareNextTurnWithContext func(context *ShouldStopAfterTurnContext, ctx context.Context) (*AgentLoopTurnUpdate, error)
	SessionID                  string
	ThinkingBudgets            *ai.ThinkingBudgets
	Transport                  ai.Transport
	MaxRetryDelayMS            *int
	ToolExecution              ToolExecutionMode

	activeRun *activeRun
}

// NewAgent builds an Agent.
func NewAgent(options *AgentOptions) (*Agent, error) {
	if options == nil {
		options = &AgentOptions{}
	}
	initial := options.InitialState
	tools := []AgentTool{}
	var initialMessages []ai.Message
	if initial != nil {
		tools = append([]AgentTool{}, initial.Tools...)
		initialMessages = append([]ai.Message{}, initial.Messages...)
	}
	var declarations []ai.Tool
	for _, tool := range tools {
		declarations = append(declarations, tool.ToolDeclaration())
	}
	prompt := ""
	if initial != nil {
		prompt = initial.SystemPrompt
	}
	initialSystemMessage := ai.CreateInitialSystemMessage(&prompt, declarations)
	if len(initialMessages) > 0 && ai.RoleOf(initialMessages[0]) != ai.RoleSystem {
		initialMessages = append([]ai.Message{initialSystemMessage}, initialMessages...)
	} else if len(initialMessages) == 0 && initialSystemMessage != nil {
		initialMessages = []ai.Message{initialSystemMessage}
	}

	model := (*ai.Model)(nil)
	thinkingLevel := ai.ThinkingLevel(ai.ThinkOff)
	if initial != nil {
		model = initial.Model
		thinkingLevel = initial.ThinkingLevel
	}
	if model == nil {
		model = defaultModel()
	}

	state := &AgentState{
		Model:            model,
		ThinkingLevel:    thinkingLevel,
		Tools:            tools,
		Messages:         initialMessages,
		PendingToolCalls: map[string]bool{},
	}
	state.SystemPrompt = ai.GetCurrentSystemPrompt(state.Messages)

	streamFn := options.StreamFn
	if streamFn == nil {
		var err error
		streamFn, err = GetDefaultStreamFn()
		if err != nil {
			return nil, err
		}
	}

	steeringMode := options.SteeringMode
	if steeringMode == "" {
		steeringMode = QueueModeOneAtATime
	}
	followUpMode := options.FollowUpMode
	if followUpMode == "" {
		followUpMode = QueueModeOneAtATime
	}
	toolExecution := options.ToolExecution
	if toolExecution == "" {
		toolExecution = ToolExecutionParallel
	}
	transport := options.Transport
	if transport == "" {
		transport = ai.TransportAuto
	}

	return &Agent{
		state:                      state,
		listeners:                  map[*int]Listener{},
		steeringQueue:              &pendingMessageQueue{mode: steeringMode},
		followUpQueue:              &pendingMessageQueue{mode: followUpMode},
		ConvertToLlm:               options.ConvertToLlm,
		TransformContext:           options.TransformContext,
		StreamFunction:             streamFn,
		GetAPIKey:                  options.GetAPIKey,
		OnPayload:                  options.OnPayload,
		OnResponse:                 options.OnResponse,
		BeforeToolCall:             options.BeforeToolCall,
		AfterToolCall:              options.AfterToolCall,
		ShouldStopAfterTurn:        options.ShouldStopAfterTurn,
		PrepareNextTurn:            options.PrepareNextTurn,
		PrepareNextTurnWithContext: options.PrepareNextTurnWithContext,
		SessionID:                  options.SessionID,
		ThinkingBudgets:            options.ThinkingBudgets,
		Transport:                  transport,
		MaxRetryDelayMS:            options.MaxRetryDelayMS,
		ToolExecution:              toolExecution,
	}, nil
}

func defaultModel() *ai.Model {
	return &ai.Model{
		ID: "unknown", Name: "unknown", API: "unknown", Provider: "unknown",
		Reasoning: false, ContextWindow: 0, MaxTokens: 0,
	}
}

func defaultConvertToLlm(messages []ai.Message) []ai.Message {
	var out []ai.Message
	for _, message := range messages {
		switch ai.RoleOf(message) {
		case ai.RoleSystem, ai.RoleUser, ai.RoleAssistant, ai.RoleToolResult:
			out = append(out, message)
		}
	}
	return out
}

// Subscribe adds a listener; the returned func unsubscribes.
func (a *Agent) Subscribe(listener Listener) func() {
	a.listenerMu.Lock()
	defer a.listenerMu.Unlock()
	key := new(int)
	a.listeners[key] = listener
	a.listenerKeys = append(a.listenerKeys, key)
	return func() {
		a.listenerMu.Lock()
		defer a.listenerMu.Unlock()
		delete(a.listeners, key)
		for i, k := range a.listenerKeys {
			if k == key {
				a.listenerKeys = append(a.listenerKeys[:i], a.listenerKeys[i+1:]...)
				break
			}
		}
	}
}

// State reads the current state snapshot.
func (a *Agent) State() AgentState {
	a.mu.Lock()
	defer a.mu.Unlock()
	return *a.state
}

// SetTools replaces the executable tools (copied).
func (a *Agent) SetTools(tools []AgentTool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.state.Tools = append([]AgentTool{}, tools...)
}

// SetMessages replaces the transcript (copied).
func (a *Agent) SetMessages(messages []ai.Message) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.state.Messages = append([]ai.Message{}, messages...)
}

// SetModel updates the active model.
func (a *Agent) SetModel(model *ai.Model) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.state.Model = model
}

// SetThinkingLevel updates the requested reasoning level.
func (a *Agent) SetThinkingLevel(level ai.ThinkingLevel) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.state.ThinkingLevel = level
}

// SteeringMode controls how queued steering messages are drained.
func (a *Agent) SteeringMode() QueueMode { return a.steeringQueue.getMode() }

// SetSteeringMode sets the steering drain mode.
func (a *Agent) SetSteeringMode(mode QueueMode) { a.steeringQueue.setMode(mode) }

// FollowUpMode controls how queued follow-up messages are drained.
func (a *Agent) FollowUpMode() QueueMode { return a.followUpQueue.getMode() }

// SetFollowUpMode sets the follow-up drain mode.
func (a *Agent) SetFollowUpMode(mode QueueMode) { a.followUpQueue.setMode(mode) }

// Steer queues a message to be injected after the current assistant turn.
func (a *Agent) Steer(message ai.Message) { a.steeringQueue.enqueue(message) }

// FollowUp queues a message to run only after the agent would otherwise stop.
func (a *Agent) FollowUp(message ai.Message) { a.followUpQueue.enqueue(message) }

// ClearSteeringQueue removes all queued steering messages.
func (a *Agent) ClearSteeringQueue() { a.steeringQueue.clear() }

// ClearFollowUpQueue removes all queued follow-up messages.
func (a *Agent) ClearFollowUpQueue() { a.followUpQueue.clear() }

// ClearAllQueues removes all queued steering and follow-up messages.
func (a *Agent) ClearAllQueues() {
	a.ClearSteeringQueue()
	a.ClearFollowUpQueue()
}

// HasQueuedMessages reports whether either queue still holds messages.
func (a *Agent) HasQueuedMessages() bool {
	return a.steeringQueue.hasItems() || a.followUpQueue.hasItems()
}

// Abort cancels the current run, if one is active.
func (a *Agent) Abort() {
	a.mu.Lock()
	run := a.activeRun
	a.mu.Unlock()
	if run != nil {
		run.cancel()
	}
}

// WaitForIdle resolves when the current run and all awaited listeners have
// finished.
func (a *Agent) WaitForIdle(ctx context.Context) error {
	a.mu.Lock()
	run := a.activeRun
	a.mu.Unlock()
	if run == nil {
		return nil
	}
	select {
	case <-run.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Reset clears conversation state and queues while retaining the replayed
// prompt/tool baseline.
func (a *Agent) Reset() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.activeRun != nil {
		return fmt.Errorf("Agent is already processing. Wait for completion before resetting.")
	}
	if baseline := ai.GetCurrentSystemMessage(a.state.Messages); baseline != nil {
		a.state.Messages = []ai.Message{baseline}
	} else {
		a.state.Messages = nil
	}
	a.state.IsStreaming = false
	a.state.StreamingMessage = nil
	a.state.PendingToolCalls = map[string]bool{}
	a.state.ErrorMessage = ""
	a.followUpQueue.clear()
	a.steeringQueue.clear()
	return nil
}

// PromptText starts a new prompt from text (plus optional images).
func (a *Agent) PromptText(ctx context.Context, input string, images ...ai.ImageContent) error {
	content := []ai.Content{ai.TextContent{Text: input}}
	for _, img := range images {
		content = append(content, img)
	}
	message := &ai.UserMessage{Content: ai.StringOrBlocks{Blocks: content}, Timestamp: time.Now().UnixMilli()}
	return a.runPromptMessages(ctx, []ai.Message{message}, false)
}

// PromptMessages starts a new prompt from a message batch.
func (a *Agent) PromptMessages(ctx context.Context, messages []ai.Message) error {
	return a.runPromptMessages(ctx, messages, false)
}

// Continue continues from the current transcript. The last message must be a
// user or tool-result message (an assistant tail drains steering/follow-up
// queues first).
func (a *Agent) Continue(ctx context.Context) error {
	a.mu.Lock()
	if a.activeRun != nil {
		a.mu.Unlock()
		return fmt.Errorf("Agent is already processing. Wait for completion before continuing.")
	}
	messages := a.state.Messages
	if len(messages) == 0 {
		a.mu.Unlock()
		return fmt.Errorf("No messages to continue from")
	}
	allSystem := true
	for _, message := range messages {
		if ai.RoleOf(message) != ai.RoleSystem {
			allSystem = false
		}
	}
	if allSystem {
		a.mu.Unlock()
		return fmt.Errorf("No messages to continue from")
	}
	lastMessage := messages[len(messages)-1]
	a.mu.Unlock()

	if ai.RoleOf(lastMessage) == ai.RoleAssistant {
		queuedSteering := a.steeringQueue.drain()
		if len(queuedSteering) > 0 {
			return a.runPromptMessages(ctx, queuedSteering, true)
		}
		queuedFollowUps := a.followUpQueue.drain()
		if len(queuedFollowUps) > 0 {
			return a.runPromptMessages(ctx, queuedFollowUps, false)
		}
		return fmt.Errorf("Cannot continue from message role: assistant")
	}

	a.mu.Lock()
	if a.activeRun != nil {
		a.mu.Unlock()
		return fmt.Errorf("Agent is already processing.")
	}
	a.mu.Unlock()
	return a.runWithLifecycle(ctx, func(ctx context.Context) error {
		RunAgentLoopContinue(a.createContextSnapshot(), a.createLoopConfig(false), ctx,
			sinkOf(a), a.StreamFunction)
		return nil
	})
}

func (a *Agent) runPromptMessages(ctx context.Context, messages []ai.Message, skipInitialSteeringPoll bool) error {
	a.mu.Lock()
	if a.activeRun != nil {
		a.mu.Unlock()
		return fmt.Errorf("Agent is already processing a prompt. Use Steer() or FollowUp() to queue messages, or wait for completion.")
	}
	a.mu.Unlock()
	return a.runWithLifecycle(ctx, func(ctx context.Context) error {
		RunAgentLoop(messages, a.createContextSnapshot(), a.createLoopConfig(skipInitialSteeringPoll), ctx,
			sinkOf(a), a.StreamFunction)
		return nil
	})
}

func (a *Agent) createContextSnapshot() AgentContext {
	a.mu.Lock()
	defer a.mu.Unlock()
	return AgentContext{
		Messages: append([]ai.Message{}, a.state.Messages...),
		Tools:    append([]AgentTool{}, a.state.Tools...),
	}
}

func (a *Agent) createLoopConfig(skipInitialSteeringPoll bool) *AgentLoopConfig {
	a.mu.Lock()
	state := *a.state
	a.mu.Unlock()
	reasoning := state.ThinkingLevel
	if reasoning == ai.ThinkOff {
		reasoning = ""
	}
	config := &AgentLoopConfig{
		SimpleStreamOptions: ai.SimpleStreamOptions{
			StreamOptions: ai.StreamOptions{
				SessionID:       a.SessionID,
				Transport:       a.Transport,
				MaxRetryDelayMs: a.MaxRetryDelayMS,
				OnPayload:       a.OnPayload,
				OnResponse:      a.OnResponse,
			},
			Reasoning: reasoning,
		},
		Model:            state.Model,
		ConvertToLlm:     a.ConvertToLlm,
		TransformContext: a.TransformContext,
		GetAPIKey:        a.GetAPIKey,
		ToolExecution:    a.ToolExecution,
	}
	if a.ThinkingBudgets != nil {
		config.ThinkingBudgets = a.ThinkingBudgets
	}
	if a.ShouldStopAfterTurn != nil {
		config.ShouldStopAfterTurn = func(context *ShouldStopAfterTurnContext) bool {
			return a.ShouldStopAfterTurn(context, a.runContext())
		}
	}
	if a.PrepareNextTurnWithContext != nil || a.PrepareNextTurn != nil {
		config.PrepareNextTurn = func(context *ShouldStopAfterTurnContext) (*AgentLoopTurnUpdate, error) {
			if a.PrepareNextTurnWithContext != nil {
				return a.PrepareNextTurnWithContext(context, a.runContext())
			}
			return a.PrepareNextTurn(a.runContext())
		}
	}
	skipPoll := skipInitialSteeringPoll
	config.GetSteeringMessages = func(ctx context.Context) ([]ai.Message, error) {
		// The first poll after a queued-steering continuation is suppressed
		// (upstream's skipInitialSteeringPoll local flag).
		if skipPoll {
			skipPoll = false
			return nil, nil
		}
		return a.steeringQueue.drain(), nil
	}
	config.GetFollowUpMessages = func(ctx context.Context) ([]ai.Message, error) {
		return a.followUpQueue.drain(), nil
	}
	if a.ConvertToLlm == nil {
		config.ConvertToLlm = defaultConvertToLlm
	}
	return config
}

func (a *Agent) runContext() context.Context {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.activeRun == nil {
		return nil
	}
	// The cancel func doubles as the run's context source.
	return runContextStore(a)
}

type runKey struct{ a *Agent }

var runContexts sync.Map

func runContextStore(a *Agent) context.Context {
	if v, ok := runContexts.Load(a); ok {
		return v.(context.Context)
	}
	return nil
}

func (a *Agent) runWithLifecycle(ctx context.Context, executor func(ctx context.Context) error) error {
	a.mu.Lock()
	if a.activeRun != nil {
		a.mu.Unlock()
		return fmt.Errorf("Agent is already processing.")
	}
	runCtx, cancel := context.WithCancel(contextOrBackground(ctx))
	run := &activeRun{cancel: cancel, done: make(chan struct{})}
	a.activeRun = run
	runContexts.Store(a, runCtx)
	a.state.IsStreaming = true
	a.state.StreamingMessage = nil
	a.state.ErrorMessage = ""
	a.mu.Unlock()

	defer func() {
		a.mu.Lock()
		a.state.IsStreaming = false
		a.state.StreamingMessage = nil
		a.state.PendingToolCalls = map[string]bool{}
		a.activeRun = nil
		a.mu.Unlock()
		runContexts.Delete(a)
		close(run.done)
	}()

	// Upstream: executor throws become rejected promises caught here, folded
	// into failure lifecycle events.
	func() {
		defer func() {
			if r := recover(); r != nil {
				if err, ok := r.(error); ok {
					a.handleRunFailure(runCtx, err, ctxDone(runCtx))
				} else {
					a.handleRunFailure(runCtx, fmt.Errorf("%v", r), ctxDone(runCtx))
				}
			}
		}()
		if err := executor(runCtx); err != nil {
			a.handleRunFailure(runCtx, err, ctxDone(runCtx))
		}
	}()
	return nil
}

func contextOrBackground(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

func (a *Agent) handleRunFailure(ctx context.Context, err error, aborted bool) {
	stopReason := ai.StopError
	if aborted {
		stopReason = ai.StopAborted
	}
	message := err.Error()
	failureMessage := &ai.AssistantMessage{
		Content:      []ai.Content{ai.TextContent{Text: ""}},
		API:          a.State().Model.API,
		Provider:     a.State().Model.Provider,
		Model:        a.State().Model.ID,
		StopReason:   stopReason,
		ErrorMessage: &message,
		Timestamp:    time.Now().UnixMilli(),
	}
	_ = a.processEvents(AgentEvent{Type: MessageStart, Message: failureMessage}, ctx)
	_ = a.processEvents(AgentEvent{Type: MessageEnd, Message: failureMessage}, ctx)
	_ = a.processEvents(AgentEvent{Type: TurnEnd, Message: failureMessage, ToolResults: []ai.Message{}}, ctx)
	_ = a.processEvents(AgentEvent{Type: AgentEnd, Messages: []ai.Message{failureMessage}}, ctx)
}

// processEvents reduces internal state for a loop event, then runs listeners
// in subscription order. agent_end only means no further loop events will be
// emitted; the run is idle after listeners settle and finishRun clears state.
func (a *Agent) processEvents(event AgentEvent, ctx context.Context) error {
	a.mu.Lock()
	switch event.Type {
	case MessageStart:
		a.state.StreamingMessage = event.Message
	case MessageUpdate:
		a.state.StreamingMessage = event.Message
	case MessageEnd:
		a.state.StreamingMessage = nil
		a.state.Messages = append(a.state.Messages, event.Message)
		a.state.SystemPrompt = ai.GetCurrentSystemPrompt(a.state.Messages)
	case ToolExecutionStart:
		a.state.PendingToolCalls[event.ToolCallID] = true
	case ToolExecutionEnd:
		delete(a.state.PendingToolCalls, event.ToolCallID)
	case TurnEnd:
		if msg, ok := event.Message.(*ai.AssistantMessage); ok && msg.ErrorMessage != nil {
			a.state.ErrorMessage = *msg.ErrorMessage
		}
	case AgentEnd:
		a.state.StreamingMessage = nil
	}
	a.mu.Unlock()

	if ctx == nil {
		a.mu.Lock()
		run := a.activeRun
		a.mu.Unlock()
		if run == nil {
			return fmt.Errorf("Agent listener invoked outside active run")
		}
	}

	a.listenerMu.Lock()
	keys := append([]*int{}, a.listenerKeys...)
	listeners := make([]Listener, 0, len(keys))
	for _, key := range keys {
		listeners = append(listeners, a.listeners[key])
	}
	a.listenerMu.Unlock()
	for _, listener := range listeners {
		if err := listener(event, ctx); err != nil {
			return err
		}
	}
	return nil
}

// sinkOf adapts processEvents to the AgentEventSink signature.
func sinkOf(a *Agent) AgentEventSink {
	return func(event AgentEvent) error {
		return a.processEvents(event, runContextStore(a))
	}
}
