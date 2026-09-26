package coding

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"

	"github.com/dat267/pier/agent"
	"github.com/dat267/pier/ai"
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
	SessionAgentStart                     SessionEventType = "agent_start"
	SessionAgentEnd                       SessionEventType = "agent_end"
	SessionTurnStart                      SessionEventType = "turn_start"
	SessionTurnEnd                        SessionEventType = "turn_end"
	SessionMessageStart                   SessionEventType = "message_start"
	SessionMessageUpdate                  SessionEventType = "message_update"
	SessionMessageEnd                     SessionEventType = "message_end"
	SessionToolExecutionStart             SessionEventType = "tool_execution_start"
	SessionToolExecutionUpdate            SessionEventType = "tool_execution_update"
	SessionToolExecutionEnd               SessionEventType = "tool_execution_end"
	SessionQueueUpdate                    SessionEventType = "queue_update"
	SessionCompactionStart                SessionEventType = "compaction_start"
	SessionCompactionEnd                  SessionEventType = "compaction_end"
	SessionInfoChanged                    SessionEventType = "session_info_changed"
	SessionThinkingLevelChanged           SessionEventType = "thinking_level_changed"
	SessionAutoRetryStart                 SessionEventType = "auto_retry_start"
	SessionAutoRetryEnd                   SessionEventType = "auto_retry_end"
	SessionBashExecutionUpdate            SessionEventType = "bash_execution_update"
	SessionEntryAppended                  SessionEventType = "entry_appended"
	SessionSummarizationRetryScheduled    SessionEventType = "summarization_retry_scheduled"
	SessionSummarizationRetryAttemptStart SessionEventType = "summarization_retry_attempt_start"
	SessionSummarizationRetryFinished     SessionEventType = "summarization_retry_finished"
	SessionAgentSettled                   SessionEventType = "agent_settled"
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
	// bash_execution_update
	ID    string
	Delta string
	// entry_appended
	Entry *SessionEntry
	// summarization_retry_attempt_start
	Source string // "branchSummary" | "compaction"
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

	// CacheWarmer keeps the prompt cache entry of session requests warm.
	CacheWarmer *CacheWarmer

	// streamFn is the session's model stream function (compaction, summaries).
	streamFn agent.StreamFn

	// summarizationRetrySource is the source label for the in-flight
	// summarization retry callbacks ("" when idle).
	summarizationRetrySource string

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
	overflowRecovery overflowRecoveryState

	bashMu              sync.Mutex
	bashNextID          int
	bashCancels         map[int]context.CancelFunc
	pendingBashMessages []*ai.CustomMessage

	// System prompt options for section diffing on tool changes.
	SystemPromptOptions *BuildSystemPromptOptions

	// skillDiagnostics are the skill loader's warnings/collisions.
	skillDiagnostics []ResourceDiagnostic

	// resources are the CLI-derived resource switches (--skill and friends),
	// kept so a reload re-reads with the same options instead of resetting them.
	resources resourceOptions

	// agentDir and promptSources are the inputs Reload re-reads the resource
	// files from (upstream ResourceLoader's config; its extension side is out
	// of scope, D41/D140).
	agentDir      string
	promptSources PromptFileSources
}

type sessionListenerKey struct {
	fn SessionEventListener
}

// SessionConfig configures NewAgentSession.
type SessionConfig struct {
	Cwd          string
	Model        *ai.Model
	StreamFn     agent.StreamFn
	APIKey       string
	SystemPrompt string
	// AppendSystemPrompt is appended to the prompt (before project context).
	AppendSystemPrompt string
	// PromptSourcePaths are the loaded system/append prompt files.
	PromptSourcePaths []string
	// AgentDir is the global agent directory: the resource files are re-read
	// from it on Reload.
	AgentDir string
	// PromptSources are the explicit system/append prompt inputs (CLI text or
	// paths); Reload re-resolves them against the current trust state and
	// re-reads the discovered files when they are nil.
	PromptSources *PromptFileSources
	// Control holds the session's collaborators (model runtime, settings
	// manager, tool registry, toggles). NewAgentSession always installs one:
	// the config's block when present, otherwise an empty block. A session is
	// never half-built, so its methods do not guard against a missing block;
	// the fields inside it stay optional.
	Control       *AgentSessionControl
	Tools         []agent.AgentTool
	Sessions      *SessionManager
	Settings      SessionSettings
	ThinkingLevel ai.ThinkingLevel
	Skills        []Skill
	ContextFiles  []ContextFile
	// SkillDiagnostics are the skill loader's warnings/collisions, surfaced in
	// the interactive loaded-resources area.
	SkillDiagnostics []ResourceDiagnostic
	InitialMessages  []ai.Message
	// ConvertToLlm transforms messages before provider calls (the block-images
	// filter).
	ConvertToLlm func(messages []ai.Message) []ai.Message
	// SessionID is forwarded to providers for cache-aware backends.
	SessionID string
	// Agent-level request wiring from settings.
	SteeringMode    QueueMode
	FollowUpMode    QueueMode
	Transport       ai.Transport
	ThinkingBudgets *ai.ThinkingBudgets
	MaxRetryDelayMS *int
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
		InitialState:    initialState,
		StreamFn:        config.StreamFn,
		ConvertToLlm:    config.ConvertToLlm,
		SessionID:       config.SessionID,
		SteeringMode:    config.SteeringMode,
		FollowUpMode:    config.FollowUpMode,
		Transport:       config.Transport,
		ThinkingBudgets: config.ThinkingBudgets,
		MaxRetryDelayMS: config.MaxRetryDelayMS,
	})
	if err != nil {
		return nil, err
	}

	// The block is taken by pointer (it holds a mutex); a session without one
	// gets an empty block so it is never half-built.
	control := config.Control
	if control == nil {
		control = &AgentSessionControl{}
	}
	if control.Tools == nil {
		control.Tools = map[string]AgentToolDefinition{}
	}

	s := &AgentSession{
		Agent:    a,
		Sessions: sessionManager,
		Settings: settings,
		Cwd:      config.Cwd,
		control:  control,
		streamFn: config.StreamFn,
		SystemPromptOptions: &BuildSystemPromptOptions{
			CustomPrompt: config.SystemPrompt, AppendSystemPrompt: config.AppendSystemPrompt, Cwd: config.Cwd,
			Skills: config.Skills, ContextFiles: config.ContextFiles,
			PromptSourcePaths: append([]string{}, config.PromptSourcePaths...),
			SelectedTools:     agentToolNames(config.Tools),
		},
		skillDiagnostics: config.SkillDiagnostics,
		agentDir:         config.AgentDir,
	}
	if config.PromptSources != nil {
		s.promptSources = *config.PromptSources
	}
	// Always subscribed: session persistence, queue tracking, compaction,
	// retry logic.
	a.Subscribe(func(event agent.AgentEvent, ctx context.Context) error {
		s.handleAgentEvent(&event)
		return nil
	})

	// Auto-compaction runs before the next assistant response, mirroring
	// upstream's _compactBeforeNextAssistantResponse; the refreshed message
	// list replaces the turn context. The agent state is also re-read here so a
	// model or thinking-level switch made while the turn is running reaches the
	// next assistant request in that same turn, not only the next prompt
	// (upstream agent-session.ts prepareNextTurnWithContext, applied by the loop
	// as nextTurnSnapshot); the tool set is refreshed with it.
	a.PrepareNextTurnWithContext = func(turn *agent.ShouldStopAfterTurnContext, ctx context.Context) (*agent.AgentLoopTurnUpdate, error) {
		if err := s.maybeAutoCompact(ctx); err != nil {
			return nil, err
		}
		state := s.Agent.State()
		updated := turn.Context
		updated.Messages = state.Messages
		updated.Tools = append([]agent.AgentTool{}, state.Tools...)
		return &agent.AgentLoopTurnUpdate{
			Context:          &updated,
			Model:            state.Model,
			ThinkingLevel:    state.ThinkingLevel,
			HasThinkingLevel: true,
		}, nil
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
	// BEFORE the event fans out. Text is extracted Blocks-aware: queued
	// messages are built with block content (queueSteer), whose Text field is
	// empty — matching on Content.Text alone left the dock banner showing the
	// steered message after it was pushed into the transcript.
	if event.Type == agent.MessageStart && event.Message != nil && ai.RoleOf(event.Message) == ai.RoleUser {
		if user, ok := event.Message.(*ai.UserMessage); ok {
			messageText := contentTextJoinedNoSep(user.Content)
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

// CompactSession runs a manual compaction over the current leaf path
// (upstream compact(customInstructions?)): aborts any active run, emits the
// compaction events, resolves summarization auth, and distinguishes
// "Already compacted" from "Nothing to compact (session too small)".
func (s *AgentSession) CompactSession(ctx context.Context, customInstructions string) (*CompactionResult, error) {
	s.Abort(ctx)

	compactionCtx, cancel := context.WithCancel(ctx)
	s.mu.Lock()
	s.compactionCancel = cancel
	s.mu.Unlock()

	s.emit(&SessionEvent{Type: SessionCompactionStart, Reason: CompactionManual})

	// Upstream routes every compaction error through the catch that emits
	// compaction_end; skipping the end event left the compaction status
	// indicator mounted forever. Clear the state before notifying so end
	// listeners observe an idle session and may submit queued prompts.
	cleaned := false
	cleanup := func() {
		if cleaned {
			return
		}
		cleaned = true
		s.mu.Lock()
		s.compactionCancel = nil
		s.mu.Unlock()
		cancel()
	}
	defer cleanup()

	result, err := s.runManualCompaction(compactionCtx, customInstructions)
	// Aborted means the compaction context was cancelled (ESC/user abort);
	// compute it before cleanup's own cancel() runs.
	aborted := compactionCtx.Err() != nil
	cleanup()
	if err != nil {
		errorMessage := ""
		if !aborted {
			errorMessage = "Compaction failed: " + err.Error()
		}
		s.emit(&SessionEvent{
			Type: SessionCompactionEnd, Reason: CompactionManual, Aborted: aborted,
			ErrorMessage: errorMessage,
		})
		return nil, err
	}
	s.emit(&SessionEvent{Type: SessionCompactionEnd, Reason: CompactionManual, Result: result})
	return result, nil
}

// runManualCompaction runs the manual compaction after compaction_start was
// emitted. All errors are reported through compaction_end by the caller.
func (s *AgentSession) runManualCompaction(ctx context.Context, customInstructions string) (*CompactionResult, error) {
	model := s.Model()
	if !s.HasModel() || model == nil {
		return nil, fmt.Errorf("%s", FormatNoModelSelectedMessage())
	}
	settings, _ := s.compactionSettings()

	pathEntries := s.Sessions.GetBranch("")
	preparation := PrepareCompaction(pathEntries, settings)
	if preparation == nil {
		// Distinguish why compaction is impossible (upstream uses optional
		// chaining: an empty branch reports Nothing-to-compact, not a panic).
		if len(pathEntries) > 0 && pathEntries[len(pathEntries)-1].Type == "compaction" {
			return nil, fmt.Errorf("Already compacted")
		}
		return nil, fmt.Errorf("Nothing to compact (session too small)")
	}

	options := CompactionOptions{
		Model: model, Ctx: ctx, CustomInstructions: customInstructions,
		StreamFn: s.compactionStreamFn(), Retry: s.retrySettings(),
		SessionID: s.Sessions.GetSessionID(),
	}
	if s.control.ModelRuntime != nil {
		resolution, err := s.control.ModelRuntime.GetAuthForModel(model, nil)
		if err == nil && resolution != nil {
			options.APIKey = resolution.Auth.APIKey
			options.Headers = headersToStrings(resolution.Auth.Headers)
			options.Env = resolution.Env
			if resolution.Auth.BaseURL != "" {
				copied := *model
				copied.BaseURL = resolution.Auth.BaseURL
				options.Model = &copied
			}
		}
	}

	s.applyCompactModelOverride(&options)
	result, err := Compact(preparation, options)
	if err != nil {
		return nil, err
	}
	if ctx.Err() != nil {
		return nil, fmt.Errorf("Compaction cancelled")
	}

	s.Sessions.AppendCompaction(result.Summary, result.FirstKeptEntryID, result.TokensBefore, result.Details, false, result.Usage)
	sessionContext := s.Sessions.Projection()
	s.Agent.SetMessages(sessionContext.Messages)
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

// GetSessionStats aggregates the session entries (port of getSessionStats). The
// counts, tool calls and token totals come folded from the session manager; only
// the context usage needs the live agent state, which the port reads here.
func (s *AgentSession) GetSessionStats() *SessionStats {
	stats := s.Sessions.SessionStats()

	// Context usage from the current context.
	model := s.Agent.State().Model
	if model != nil && model.ContextWindow > 0 {
		tokens := EstimateContextTokens(s.Sessions.Projection().Messages).Tokens
		stats.ContextUsage = &ContextUsage{
			Tokens:        int64(tokens),
			ContextWindow: model.ContextWindow,
			Percent:       float64(tokens) / float64(model.ContextWindow) * 100,
		}
	}
	return stats
}
