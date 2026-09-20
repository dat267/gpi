package coding

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"

	"github.com/dat267/gpi/agent"
	"github.com/dat267/gpi/ai"
)

// Port of core/agent-session.ts — the AgentSession facade. The extension
// runner, extension UI modes, and extension commands are out of scope by
// design (the user extends the repo directly); the retained surface is:
// session persistence from agent events, steering/follow-up queue display
// tracking, auto-compaction on threshold/overflow, auto-retry on transient
// errors, stats, and event fan-out.

// Defaults (core/defaults.ts).
const (
	DefaultThinkingLevel = ai.ThinkMedium
)

// ThinkingLevelOptions is the full level list.
var ThinkingLevelOptions = []ai.ThinkingLevel{
	ai.ThinkOff, ai.ThinkMinimal, ai.ThinkLow, ai.ThinkMedium,
	ai.ThinkHigh, ai.ThinkXHigh, ai.ThinkMax,
}

// ParsedSkillBlock is a parsed <skill> block from message text.
type ParsedSkillBlock struct {
	Name        string
	Location    string
	Content     string
	UserMessage string
}

var skillBlockPattern = regexp.MustCompile(
	`^<skill name="([^"]+)" location="([^"]+)">\n([\s\S]*?)\n</skill>(?:\n\n([\s\S]+))?$`)

// ParseSkillBlock parses a skill block from message text; nil when absent.
func ParseSkillBlock(text string) *ParsedSkillBlock {
	match := skillBlockPattern.FindStringSubmatch(text)
	if match == nil {
		return nil
	}
	return &ParsedSkillBlock{
		Name: match[1], Location: match[2], Content: match[3],
		UserMessage: strings.TrimSpace(match[4]),
	}
}

// CompactionReason discriminates compaction triggers.
type CompactionReason = string

const (
	CompactionManual    CompactionReason = "manual"
	CompactionThreshold CompactionReason = "threshold"
	CompactionOverflow  CompactionReason = "overflow"
)

// SessionEventType names session-level events.
type SessionEventType = string

const (
	SessionAgentStart           SessionEventType = "agent_start"
	SessionAgentEnd             SessionEventType = "agent_end"
	SessionTurnStart            SessionEventType = "turn_start"
	SessionTurnEnd              SessionEventType = "turn_end"
	SessionMessageStart         SessionEventType = "message_start"
	SessionMessageUpdate        SessionEventType = "message_update"
	SessionMessageEnd           SessionEventType = "message_end"
	SessionToolExecutionStart   SessionEventType = "tool_execution_start"
	SessionToolExecutionUpdate  SessionEventType = "tool_execution_update"
	SessionToolExecutionEnd     SessionEventType = "tool_execution_end"
	SessionQueueUpdate          SessionEventType = "queue_update"
	SessionCompactionStart      SessionEventType = "compaction_start"
	SessionCompactionEnd        SessionEventType = "compaction_end"
	SessionInfoChanged          SessionEventType = "session_info_changed"
	SessionThinkingLevelChanged SessionEventType = "thinking_level_changed"
	SessionAutoRetryStart       SessionEventType = "auto_retry_start"
	SessionAutoRetryEnd         SessionEventType = "auto_retry_end"
)

// SessionEvent extends the core agent events with session-level payloads.
type SessionEvent struct {
	Type SessionEventType
	// Agent carries the underlying event for passthrough types.
	Agent *agent.AgentEvent
	// agent_end
	WillRetry bool
	// queue_update
	Steering []string
	FollowUp []string
	// compaction_start/end
	Reason       CompactionReason
	Result       *CompactionResult
	Aborted      bool
	ErrorMessage string
	// thinking_level_changed
	Level ai.ThinkingLevel
	// auto_retry_*
	Attempt     int
	MaxAttempts int
	DelayMS     int64
	Success     bool
}

// SessionEventListener receives session events.
type SessionEventListener func(event *SessionEvent)

// SessionSettings bundle the tuning knobs.
type SessionSettings struct {
	Retry      *ai.RetryPolicy
	Compaction CompactionSettings
}

// AgentSession wires the stateful Agent, the SessionManager, and the
// built-in tool set into one session with persistence, auto-compaction,
// and auto-retry.
type AgentSession struct {
	Agent    *agent.Agent
	Sessions *SessionManager
	Settings SessionSettings
	Cwd      string

	// mu guards retry state and the control block.
	mu sync.Mutex

	// control holds the optional collaborators and mutation state ported from
	// the upstream session's state/control surface.
	control *AgentSessionControl

	// CompactionStreamFn is the stream function used for compaction and branch
	// summaries (upstream uses the agent's stream function).
	CompactionStreamFn StreamFnFn

	// promptState buffers custom and next-turn messages across a run.
	promptState *promptState

	// streamFn is the session's model stream function (compaction, summaries).
	streamFn agent.StreamFn

	listenerMu sync.Mutex
	listeners  []*sessionListenerKey

	steeringMessages []string
	followUpMessages []string

	lastAssistantMessage *ai.AssistantMessage
	retryAttempt         int
	retryCancel          context.CancelFunc
	retryActive          bool
	willRetry            bool

	compactionCancel context.CancelFunc
	compactionActive bool
	overflowRecovery overflowRecoveryState

	// System prompt options for section diffing on tool changes.
	SystemPromptOptions *BuildSystemPromptOptions
}

type sessionListenerKey struct {
	fn SessionEventListener
}

// SessionConfig configures NewAgentSession.
type SessionConfig struct {
	Cwd             string
	Model           *ai.Model
	StreamFn        agent.StreamFn
	APIKey          string
	SystemPrompt    string
	Tools           []agent.AgentTool
	Sessions        *SessionManager
	Settings        SessionSettings
	ThinkingLevel   ai.ThinkingLevel
	Skills          []Skill
	ContextFiles    []ContextFile
	InitialMessages []ai.Message
}

// NewAgentSession builds the wired session.
func NewAgentSession(config *SessionConfig) (*AgentSession, error) {
	if config == nil {
		return nil, fmt.Errorf("config required")
	}
	settings := config.Settings
	if settings.Compaction.ReserveTokens == 0 {
		settings.Compaction = DefaultCompactionSettings
	}

	sessionManager := config.Sessions
	if sessionManager == nil {
		sessionManager = NewSessionManager(config.Cwd, nil)
	}

	initialState := &agent.AgentInitialState{
		SystemPrompt:  config.SystemPrompt,
		Model:         config.Model,
		ThinkingLevel: config.ThinkingLevel,
		Tools:         config.Tools,
		Messages:      config.InitialMessages,
	}
	a, err := agent.NewAgent(&agent.AgentOptions{
		InitialState: initialState,
		StreamFn:     config.StreamFn,
	})
	if err != nil {
		return nil, err
	}

	s := &AgentSession{
		Agent:    a,
		Sessions: sessionManager,
		Settings: settings,
		Cwd:      config.Cwd,
		streamFn: config.StreamFn,
		SystemPromptOptions: &BuildSystemPromptOptions{
			CustomPrompt: config.SystemPrompt, Cwd: config.Cwd,
			Skills: config.Skills, ContextFiles: config.ContextFiles,
		},
	}
	// Always subscribed: session persistence, queue tracking, compaction,
	// retry logic.
	a.Subscribe(func(event agent.AgentEvent, ctx context.Context) error {
		s.handleAgentEvent(&event)
		return nil
	})

	// Auto-compaction runs before the next assistant response, mirroring
	// upstream's _compactBeforeNextAssistantResponse; the refreshed message
	// list replaces the turn context.
	a.PrepareNextTurnWithContext = func(turn *agent.ShouldStopAfterTurnContext, ctx context.Context) (*agent.AgentLoopTurnUpdate, error) {
		if err := s.maybeAutoCompact(ctx); err != nil {
			return nil, err
		}
		updated := turn.Context
		updated.Messages = s.Agent.State().Messages
		return &agent.AgentLoopTurnUpdate{Context: &updated}, nil
	}
	return s, nil
}

// Subscribe adds a session listener; the returned func unsubscribes.
func (s *AgentSession) Subscribe(listener SessionEventListener) func() {
	s.listenerMu.Lock()
	defer s.listenerMu.Unlock()
	key := &sessionListenerKey{fn: listener}
	s.listeners = append(s.listeners, key)
	return func() {
		s.listenerMu.Lock()
		defer s.listenerMu.Unlock()
		for i, k := range s.listeners {
			if k == key {
				s.listeners = append(s.listeners[:i], s.listeners[i+1:]...)
				return
			}
		}
	}
}

func (s *AgentSession) emit(event *SessionEvent) {
	s.listenerMu.Lock()
	listeners := append([]*sessionListenerKey{}, s.listeners...)
	s.listenerMu.Unlock()
	for _, key := range listeners {
		key.fn(event)
	}
}

// handleAgentEvent ports the persistence/queue/compaction event handling.
func (s *AgentSession) handleAgentEvent(event *agent.AgentEvent) {
	// Queue display tracking: user message starts remove queued entries
	// BEFORE the event fans out.
	if event.Type == agent.MessageStart && event.Message != nil && ai.RoleOf(event.Message) == ai.RoleUser {
		if user, ok := event.Message.(*ai.UserMessage); ok {
			messageText := user.Content.Text
			if messageText != "" {
				if idx := indexOf(s.steeringMessages, messageText); idx != -1 {
					s.steeringMessages = append(s.steeringMessages[:idx], s.steeringMessages[idx+1:]...)
					s.emitQueueUpdate()
				} else if idx := indexOf(s.followUpMessages, messageText); idx != -1 {
					s.followUpMessages = append(s.followUpMessages[:idx], s.followUpMessages[idx+1:]...)
					s.emitQueueUpdate()
				}
			}
		}
	}

	// Notify listeners (agent_end carries willRetry).
	if event.Type == agent.AgentEnd {
		s.willRetry = s.willRetryAfterAgentEnd(event)
		s.emit(&SessionEvent{Type: SessionAgentEnd, Agent: event, WillRetry: s.willRetry})
	} else {
		s.emit(&SessionEvent{Type: event.Type, Agent: event})
	}

	// Session persistence.
	if event.Type == agent.MessageEnd && event.Message != nil {
		switch msg := event.Message.(type) {
		case *ai.SystemMessage, *ai.UserMessage, *ai.AssistantMessage, *ai.ToolResultMessage:
			s.Sessions.AppendMessage(event.Message)
		case *ai.CustomMessage:
			if msg.Role == RoleCustom {
				var fields customMessageFields
				_ = json.Unmarshal(msg.Content, &fields)
				s.Sessions.AppendCustomMessageEntry(fields.CustomType, string(fields.Content), fields.Display, fields.Details)
			}
			// bashExecution/compactionSummary/branchSummary persist elsewhere.
		}

		// Track the assistant message for auto-compaction; a successful response
		// ends any retry sequence (upstream emits auto_retry_end success and
		// resets the counter).
		if assistant, ok := event.Message.(*ai.AssistantMessage); ok {
			s.mu.Lock()
			s.lastAssistantMessage = assistant
			attempt := s.retryAttempt
			if assistant.StopReason != ai.StopError && attempt > 0 {
				s.retryAttempt = 0
			}
			s.mu.Unlock()
			if assistant.StopReason != ai.StopError && attempt > 0 {
				s.emit(&SessionEvent{Type: SessionAutoRetryEnd, Success: true, Attempt: attempt})
			}
		}
	}
}

func indexOf(list []string, want string) int {
	for i, v := range list {
		if v == want {
			return i
		}
	}
	return -1
}

func (s *AgentSession) emitQueueUpdate() {
	s.emit(&SessionEvent{
		Type:     SessionQueueUpdate,
		Steering: append([]string{}, s.steeringMessages...),
		FollowUp: append([]string{}, s.followUpMessages...),
	})
}

// willRetryAfterAgentEnd: retry policy enabled, attempts remain, and the last
// assistant error is retryable (context overflow is not).
func (s *AgentSession) willRetryAfterAgentEnd(event *agent.AgentEvent) bool {
	settings := s.retrySettings()
	if settings == nil || !settings.Enabled {
		return false
	}
	s.mu.Lock()
	attempt := s.retryAttempt
	s.mu.Unlock()
	if attempt >= settings.MaxRetries {
		return false
	}
	for i := len(event.Messages) - 1; i >= 0; i-- {
		if assistant, ok := event.Messages[i].(*ai.AssistantMessage); ok {
			return s.IsRetryableError(assistant)
		}
	}
	return false
}

// Steer queues a steering message (drained after the current turn). The queue
// records the message's text so it can be restored to the editor.
func (s *AgentSession) Steer(message ai.Message) {
	s.Agent.Steer(message)
	if user, ok := message.(*ai.UserMessage); ok {
		if text := contentTextJoinedNoSep(user.Content); text != "" {
			s.steeringMessages = append(s.steeringMessages, text)
		}
	}
	s.emitQueueUpdate()
}

// FollowUp queues a message to run after the agent would stop.
func (s *AgentSession) FollowUp(message ai.Message) {
	s.Agent.FollowUp(message)
	if user, ok := message.(*ai.UserMessage); ok {
		if text := contentTextJoinedNoSep(user.Content); text != "" {
			s.followUpMessages = append(s.followUpMessages, text)
		}
	}
	s.emitQueueUpdate()
}

// PromptText runs a prompt from text.
func (s *AgentSession) PromptText(ctx context.Context, input string) error {
	return s.Agent.PromptText(ctx, input)
}

// PromptMessages runs a prompt from messages.
func (s *AgentSession) PromptMessages(ctx context.Context, messages []ai.Message) error {
	return s.Agent.PromptMessages(ctx, messages)
}

// WaitForIdle waits for the current run to settle.
func (s *AgentSession) WaitForIdle(ctx context.Context) error {
	if err := s.Agent.WaitForIdle(ctx); err != nil {
		return err
	}
	return s.waitForSessionIdle(ctx)
}

// ---------------------------------------------------------------------------
// Compaction
// ---------------------------------------------------------------------------

// CompactSession runs a manual compaction over the current leaf path.
func (s *AgentSession) CompactSession(ctx context.Context, streamFn agent.StreamFn) (*CompactionResult, error) {
	return s.runCompaction(ctx, CompactionManual, streamFn)
}

// runCompaction ports _runAutoCompaction: prepare → summarize → append the
// compaction entry.
func (s *AgentSession) runCompaction(ctx context.Context, reason CompactionReason, streamFn agent.StreamFn) (*CompactionResult, error) {
	pathEntries := s.Sessions.GetEntries()
	preparation := PrepareCompaction(pathEntries, s.Settings.Compaction)
	if preparation == nil {
		return nil, fmt.Errorf("Nothing to compact")
	}

	s.emit(&SessionEvent{Type: SessionCompactionStart, Reason: reason})

	result, err := Compact(preparation, CompactionOptions{
		Model: s.Agent.State().Model, Ctx: ctx, StreamFn: adaptStreamFn(streamFn),
	})
	if err != nil {
		s.emit(&SessionEvent{Type: SessionCompactionEnd, Reason: reason, Aborted: ctxErrOf(ctx) != nil, ErrorMessage: err.Error()})
		return nil, err
	}
	s.Sessions.AppendCompaction(result.Summary, result.FirstKeptEntryID, result.TokensBefore, result.Details, false, result.Usage)
	s.emit(&SessionEvent{Type: SessionCompactionEnd, Reason: reason, Result: result})
	return result, nil
}

// adaptStreamFn wraps a StreamFn into the compaction StreamFnFn shape.
func adaptStreamFn(streamFn agent.StreamFn) StreamFnFn {
	if streamFn == nil {
		return nil
	}
	return func(model *ai.Model, context ai.TranscriptContext, options *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
		return streamFn(model, context, options)
	}
}

// maybeAutoCompact checks the threshold and compacts when crossed (the
// prepareNextTurn hook upstream: _compactBeforeNextAssistantResponse).
// maybeAutoCompact runs the pre-turn compaction check over the last assistant
// message (upstream's pre-prompt _checkCompaction(lastAssistant, false)).
func (s *AgentSession) maybeAutoCompact(ctx context.Context) error {
	last := s.findLastAssistantMessage()
	if last == nil {
		return nil
	}
	if _, err := s.CheckCompaction(ctx, last, false); err != nil {
		return err
	}
	return nil
}

// ---------------------------------------------------------------------------
// Stats
// ---------------------------------------------------------------------------

// ContextUsage is the context-window fill report.
type ContextUsage struct {
	Tokens                int64
	ContextWindow         int64
	Percent               float64
	IsOverTokenSoftTarget bool
}

// SessionStats is the session counters report.
type SessionStats struct {
	SessionFile       string
	SessionID         string
	UserMessages      int
	AssistantMessages int
	ToolCalls         int
	ToolResults       int
	TotalMessages     int
	Tokens            struct {
		Input      int64
		Output     int64
		CacheRead  int64
		CacheWrite int64
		Total      int64
	}
	Cost         float64
	ContextUsage *ContextUsage
}

// GetSessionStats aggregates the session entries (port of getSessionStats).
func (s *AgentSession) GetSessionStats() *SessionStats {
	stats := &SessionStats{
		SessionFile: s.Sessions.GetSessionFile(),
		SessionID:   s.Sessions.GetSessionID(),
	}
	var totals ai.Usage
	addUsage := func(u *ai.Usage) {
		if u == nil {
			return
		}
		totals.Input += u.Input
		totals.Output += u.Output
		totals.CacheRead += u.CacheRead
		totals.CacheWrite += u.CacheWrite
		totals.Cost.Total += u.Cost.Total
	}
	for _, entry := range s.Sessions.GetEntries() {
		if (entry.Type == "branch_summary" || entry.Type == "compaction") && entry.Usage != nil {
			addUsage(entry.Usage)
		}
		if entry.Type != "message" {
			continue
		}
		stats.TotalMessages++
		message, err := ai.UnmarshalMessage(entry.Message)
		if err != nil {
			continue
		}
		switch m := message.(type) {
		case *ai.UserMessage:
			stats.UserMessages++
		case *ai.ToolResultMessage:
			stats.ToolResults++
			addUsage(m.Usage)
		case *ai.AssistantMessage:
			stats.AssistantMessages++
			for _, block := range m.Content {
				if _, ok := block.(ai.ToolCall); ok {
					stats.ToolCalls++
				}
			}
			addUsage(&m.Usage)
		}
	}
	stats.Tokens.Input = totals.Input
	stats.Tokens.Output = totals.Output
	stats.Tokens.CacheRead = totals.CacheRead
	stats.Tokens.CacheWrite = totals.CacheWrite
	stats.Tokens.Total = totals.Input + totals.Output + totals.CacheRead + totals.CacheWrite
	stats.Cost = totals.Cost.Total

	// Context usage from the current context.
	model := s.Agent.State().Model
	if model != nil && model.ContextWindow > 0 {
		tokens := EstimateContextTokens(s.Sessions.BuildSessionContext().Messages).Tokens
		stats.ContextUsage = &ContextUsage{
			Tokens:        int64(tokens),
			ContextWindow: model.ContextWindow,
			Percent:       float64(tokens) / float64(model.ContextWindow) * 100,
		}
	}
	return stats
}
