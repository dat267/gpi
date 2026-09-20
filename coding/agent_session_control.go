package coding

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/dat267/gpi/agent"
	"github.com/dat267/gpi/ai"
)

// Port of the state/control surface of core/agent-session.ts: read-only state
// access, tool management, queue modes, model and thinking-level management,
// auto toggles, bash state, and context usage.
//
// D41: upstream's session owns an ExtensionRunner and a resource loader; both
// are extension mechanics and out of scope, so the corresponding accessors
// (extensionRunner, hasExtensionHandlers, bindExtensions, promptTemplates via
// the resource loader) are omitted or take their data from the session config.

// QueueMode controls how queued messages drain.
type QueueMode = string

const (
	QueueModeAll        QueueMode = "all"
	QueueModeOneAtATime QueueMode = "one-at-a-time"
)

// ToolInfo describes one configured tool.
type ToolInfo struct {
	Name             string
	Description      string
	Parameters       json.RawMessage
	PromptGuidelines []string
	SourceInfo       *SourceInfo
}

// ToolPromptGuidelines supplies prompt guidelines for tools that carry them
// outside the agent tool definition (upstream reads them from the tool).
type ToolPromptGuidelines = map[string][]string

// ModelMutationOptions control model/thinking mutations.
type ModelMutationOptions struct {
	// Persist writes the change to global settings.
	Persist bool
}

// ModelCycleResult is the outcome of cycling a model.
type ModelCycleResult struct {
	Model         *ai.Model
	ThinkingLevel ai.ThinkingLevel
	IsScoped      bool
}

// agentTool aliases the agent tool type for the control surface.
type agentTool = agent.AgentTool

// AgentSessionControl holds the optional runtime collaborators and mutation
// state ported here.
type AgentSessionControl struct {
	// ModelRuntime resolves availability and auth for model switches.
	ModelRuntime *ModelRuntime
	// Settings supplies defaults and persists changes.
	Settings *SettingsManager
	// Tools is the registry of selectable tools keyed by name.
	Tools map[string]AgentToolDefinition
	// PromptTemplates are the file-based prompt templates (resource loader).
	PromptTemplates []PromptTemplate

	stateMu           sync.Mutex
	scopedModels      []ScopedModel
	autoCompaction    bool
	autoRetry         bool
	retryAborted      bool
	compactionActive  bool
	branchSummaryOpen bool
	bashActive        bool
	pendingBash       int
	bashAborted       bool
	lastAssistant     *ai.AssistantMessage
	disposed          bool
}

// AgentToolDefinition is a registered tool plus its source metadata.
type AgentToolDefinition struct {
	Tool       agent.AgentTool
	SourceInfo *SourceInfo
	// PromptGuidelines are the tool's prompt bullets (upstream reads these
	// from the tool definition).
	PromptGuidelines []string
}

// State returns the underlying agent state.
func (s *AgentSession) State() agent.AgentState { return s.Agent.State() }

// Model returns the current model.
func (s *AgentSession) Model() *ai.Model { return s.Agent.State().Model }

// HasModel reports whether a real model is selected. The agent keeps a
// placeholder model when none was configured, which upstream represents as an
// undefined model.
func (s *AgentSession) HasModel() bool {
	model := s.Model()
	return model != nil && model.ID != "" && model.ID != "unknown"
}

// ThinkingLevel returns the current thinking level.
func (s *AgentSession) ThinkingLevel() ai.ThinkingLevel { return s.Agent.State().ThinkingLevel }

// IsStreaming reports whether an agent run is active (upstream's
// _isAgentRunActive, which spans prompt, compaction continuation, and retry).
func (s *AgentSession) IsStreaming() bool {
	if s.Agent.State().IsStreaming {
		return true
	}
	if s.promptState == nil {
		return false
	}
	s.promptState.mu.Lock()
	defer s.promptState.mu.Unlock()
	return s.promptState.runActive
}

// IsCompacting reports whether compaction or branch summarization is running.
func (s *AgentSession) IsCompacting() bool {
	if s.control == nil {
		return false
	}
	s.control.stateMu.Lock()
	defer s.control.stateMu.Unlock()
	return s.control.compactionActive || s.control.branchSummaryOpen
}

// IsIdle reports whether no agent run or compaction is active.
func (s *AgentSession) IsIdle() bool { return !s.IsStreaming() && !s.IsCompacting() }

// SystemPrompt returns the effective system prompt.
func (s *AgentSession) SystemPrompt() string {
	if s.SystemPromptOptions != nil {
		prompt, err := BuildSystemPrompt(*s.SystemPromptOptions)
		if err == nil {
			return prompt
		}
	}
	return s.Agent.State().SystemPrompt
}

// RetryAttempt returns the current retry attempt (0 when not retrying).
func (s *AgentSession) RetryAttempt() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.retryAttempt
}

// Messages returns the transcript including custom message types.
func (s *AgentSession) Messages() []ai.Message { return s.Agent.State().Messages }

// SteeringMode returns the current steering drain mode.
func (s *AgentSession) SteeringMode() QueueMode { return s.Agent.SteeringMode() }

// FollowUpMode returns the current follow-up drain mode.
func (s *AgentSession) FollowUpMode() QueueMode { return s.Agent.FollowUpMode() }

// SetSteeringMode sets the steering drain mode.
func (s *AgentSession) SetSteeringMode(mode QueueMode) { s.Agent.SetSteeringMode(mode) }

// SetFollowUpMode sets the follow-up drain mode.
func (s *AgentSession) SetFollowUpMode(mode QueueMode) { s.Agent.SetFollowUpMode(mode) }

// SyncQueueModesFromSettings applies the settings-backed queue modes.
func (s *AgentSession) SyncQueueModesFromSettings() {
	if s.control == nil || s.control.Settings == nil {
		return
	}
	s.Agent.SetSteeringMode(s.control.Settings.GetSteeringMode())
	s.Agent.SetFollowUpMode(s.control.Settings.GetFollowUpMode())
}

// ClearQueue drains both queues and reports what was removed.
func (s *AgentSession) ClearQueue() (steering []string, followUp []string) {
	steering = s.GetSteeringMessages()
	followUp = s.GetFollowUpMessages()
	s.Agent.ClearAllQueues()
	s.steeringMessages = nil
	s.followUpMessages = nil
	s.emitQueueUpdate()
	return steering, followUp
}

// PendingMessageCount is the number of queued steering and follow-up messages.
func (s *AgentSession) PendingMessageCount() int {
	return len(s.GetSteeringMessages()) + len(s.GetFollowUpMessages())
}

// GetSteeringMessages lists the queued steering message texts.
func (s *AgentSession) GetSteeringMessages() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string{}, s.steeringMessages...)
}

// GetFollowUpMessages lists the queued follow-up message texts.
func (s *AgentSession) GetFollowUpMessages() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string{}, s.followUpMessages...)
}

// SessionFile returns the session file path ("" when disabled).
func (s *AgentSession) SessionFile() string { return s.Sessions.GetSessionFile() }

// SessionID returns the session id.
func (s *AgentSession) SessionID() string { return s.Sessions.GetSessionID() }

// SessionName returns the current display name.
func (s *AgentSession) SessionName() string { return s.Sessions.GetSessionName() }

// SetSessionName records a display name for the session.
func (s *AgentSession) SetSessionName(name string) {
	s.Sessions.AppendSessionInfo(name)
	s.emit(&SessionEvent{Type: SessionInfoChanged})
}

// ScopedModels returns the model cycle scope.
func (s *AgentSession) ScopedModels() []ScopedModel {
	if s.control == nil {
		return nil
	}
	s.control.stateMu.Lock()
	defer s.control.stateMu.Unlock()
	return append([]ScopedModel{}, s.control.scopedModels...)
}

// SetScopedModels replaces the model cycle scope.
func (s *AgentSession) SetScopedModels(models []ScopedModel) {
	if s.control == nil {
		return
	}
	s.control.stateMu.Lock()
	s.control.scopedModels = append([]ScopedModel{}, models...)
	s.control.stateMu.Unlock()
}

// PromptTemplates returns the file-based prompt templates.
func (s *AgentSession) PromptTemplates() []PromptTemplate {
	if s.control == nil {
		return nil
	}
	return append([]PromptTemplate{}, s.control.PromptTemplates...)
}

// AutoCompactionEnabled reports the auto-compaction toggle.
func (s *AgentSession) AutoCompactionEnabled() bool {
	if s.control == nil {
		return false
	}
	s.control.stateMu.Lock()
	defer s.control.stateMu.Unlock()
	return s.control.autoCompaction
}

// SetAutoCompactionEnabled toggles auto-compaction.
func (s *AgentSession) SetAutoCompactionEnabled(enabled bool) {
	if s.control == nil {
		return
	}
	s.control.stateMu.Lock()
	s.control.autoCompaction = enabled
	s.control.stateMu.Unlock()
}

// AutoRetryEnabled reports the auto-retry toggle.
func (s *AgentSession) AutoRetryEnabled() bool {
	if s.control == nil {
		return false
	}
	s.control.stateMu.Lock()
	defer s.control.stateMu.Unlock()
	return s.control.autoRetry
}

// SetAutoRetryEnabled toggles auto-retry.
func (s *AgentSession) SetAutoRetryEnabled(enabled bool) {
	if s.control == nil {
		return
	}
	s.control.stateMu.Lock()
	s.control.autoRetry = enabled
	s.control.stateMu.Unlock()
}

// GetActiveToolNames lists the tools currently set on the agent.
func (s *AgentSession) GetActiveToolNames() []string {
	tools := s.Agent.State().Tools
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		names = append(names, tool.Name)
	}
	return names
}

// GetAllTools lists every configured tool with its metadata.
func (s *AgentSession) GetAllTools() []ToolInfo {
	if s.control == nil {
		return nil
	}
	names := make([]string, 0, len(s.control.Tools))
	for name := range s.control.Tools {
		names = append(names, name)
	}
	sortStrings(names)
	out := make([]ToolInfo, 0, len(names))
	for _, name := range names {
		entry := s.control.Tools[name]
		out = append(out, ToolInfo{
			Name:             entry.Tool.Name,
			Description:      entry.Tool.Description,
			Parameters:       entry.Tool.Parameters,
			PromptGuidelines: entry.PromptGuidelines,
			SourceInfo:       entry.SourceInfo,
		})
	}
	return out
}

// GetToolDefinition returns a registered tool by name.
func (s *AgentSession) GetToolDefinition(name string) *agent.AgentTool {
	if s.control == nil {
		return nil
	}
	entry, ok := s.control.Tools[name]
	if !ok {
		return nil
	}
	return &entry.Tool
}

// SetActiveToolsByName enables the named registry tools (unknown names are
// ignored) and rebuilds the system prompt.
func (s *AgentSession) SetActiveToolsByName(names []string) {
	if s.control == nil {
		return
	}
	tools := make([]agent.AgentTool, 0, len(names))
	validNames := make([]string, 0, len(names))
	for _, name := range names {
		entry, ok := s.control.Tools[name]
		if !ok {
			continue
		}
		tools = append(tools, entry.Tool)
		validNames = append(validNames, name)
	}
	s.Agent.SetTools(tools)
	s.RebuildSystemPrompt(validNames)
}

// RebuildSystemPrompt refreshes the session prompt for a tool set.
func (s *AgentSession) RebuildSystemPrompt(toolNames []string) {
	if s.SystemPromptOptions == nil {
		return
	}
	options := *s.SystemPromptOptions
	options.SelectedTools = toolNames
	s.SystemPromptOptions = &options
}

// SetModel switches the session model (auth-checked) and applies the thinking
// level for the new model.
func (s *AgentSession) SetModel(ctx context.Context, model *ai.Model, options ModelMutationOptions) error {
	if s.control != nil && s.control.ModelRuntime != nil && !s.control.ModelRuntime.HasConfiguredAuth(model.Provider) {
		return fmt.Errorf("No API key for %s/%s", model.Provider, model.ID)
	}
	previous := s.Model()
	s.Agent.SetModel(model)
	s.Sessions.AppendModelChange(model.Provider, model.ID)
	if options.Persist && s.control != nil && s.control.Settings != nil {
		s.control.Settings.SetDefaultModelAndProvider(model.Provider, model.ID)
		s.addPersistedDefaultToNonEmptyScope(model)
	}
	s.SetThinkingLevel(s.getThinkingLevelForModelSwitch(model, ""), ModelMutationOptions{Persist: options.Persist})
	s.emitModelSelect(model, previous, "set")
	return nil
}

// CycleModel advances to the next (or previous) model in scope or availability.
func (s *AgentSession) CycleModel(ctx context.Context, direction string, options ModelMutationOptions) (*ModelCycleResult, error) {
	if len(s.ScopedModels()) > 0 {
		return s.cycleScopedModel(ctx, direction, options)
	}
	return s.cycleAvailableModel(ctx, direction, options)
}

func (s *AgentSession) cycleScopedModel(ctx context.Context, direction string, options ModelMutationOptions) (*ModelCycleResult, error) {
	available := s.availableModelKeys()
	scoped := make([]ScopedModel, 0, len(s.ScopedModels()))
	for _, candidate := range s.ScopedModels() {
		if available[candidate.Model.Provider+"\x00"+candidate.Model.ID] {
			scoped = append(scoped, candidate)
		}
	}
	if len(scoped) <= 1 {
		return nil, nil
	}
	current := s.Model()
	index := 0
	for position, candidate := range scoped {
		if ai.ModelsAreEqual(candidate.Model, current) {
			index = position
			break
		}
	}
	nextIndex := index + 1
	if direction == "backward" {
		nextIndex = index - 1 + len(scoped)
	}
	next := scoped[nextIndex%len(scoped)]
	return s.applyModelCycle(ctx, next.Model, next.ThinkingLevel, options, true)
}

func (s *AgentSession) cycleAvailableModel(ctx context.Context, direction string, options ModelMutationOptions) (*ModelCycleResult, error) {
	var models []*ai.Model
	if s.control != nil && s.control.ModelRuntime != nil {
		models = s.control.ModelRuntime.GetAvailableSnapshot()
	}
	if len(models) <= 1 {
		return nil, nil
	}
	current := s.Model()
	index := 0
	for position, candidate := range models {
		if ai.ModelsAreEqual(candidate, current) {
			index = position
			break
		}
	}
	nextIndex := index + 1
	if direction == "backward" {
		nextIndex = index - 1 + len(models)
	}
	next := models[nextIndex%len(models)]
	return s.applyModelCycle(ctx, next, "", options, false)
}

func (s *AgentSession) applyModelCycle(ctx context.Context, model *ai.Model, explicitLevel ai.ThinkingLevel, options ModelMutationOptions, isScoped bool) (*ModelCycleResult, error) {
	if s.control != nil && s.control.ModelRuntime != nil && !s.control.ModelRuntime.HasConfiguredAuth(model.Provider) {
		return nil, fmt.Errorf("No API key for %s/%s", model.Provider, model.ID)
	}
	previous := s.Model()
	s.Agent.SetModel(model)
	s.Sessions.AppendModelChange(model.Provider, model.ID)
	if options.Persist && s.control != nil && s.control.Settings != nil {
		s.control.Settings.SetDefaultModelAndProvider(model.Provider, model.ID)
		s.addPersistedDefaultToNonEmptyScope(model)
	}
	s.SetThinkingLevel(s.getThinkingLevelForModelSwitch(model, explicitLevel), ModelMutationOptions{Persist: options.Persist})
	s.emitModelSelect(model, previous, "cycle")
	return &ModelCycleResult{Model: model, ThinkingLevel: s.ThinkingLevel(), IsScoped: isScoped}, nil
}

func (s *AgentSession) availableModelKeys() map[string]bool {
	keys := map[string]bool{}
	if s.control != nil && s.control.ModelRuntime != nil {
		for _, model := range s.control.ModelRuntime.GetAvailableSnapshot() {
			keys[model.Provider+"\x00"+model.ID] = true
		}
	}
	return keys
}

// addPersistedDefaultToNonEmptyScope keeps a persisted default inside a
// non-empty model scope.
func (s *AgentSession) addPersistedDefaultToNonEmptyScope(model *ai.Model) {
	if s.control == nil || s.control.Settings == nil {
		return
	}
	s.control.stateMu.Lock()
	scoped := append([]ScopedModel{}, s.control.scopedModels...)
	s.control.stateMu.Unlock()
	if len(scoped) == 0 {
		return
	}
	for _, candidate := range scoped {
		if ai.ModelsAreEqual(candidate.Model, model) {
			return
		}
	}
	scoped = append(scoped, ScopedModel{Model: model})
	s.SetScopedModels(scoped)

	enabledModels := s.control.Settings.GetEnabledModels()
	if len(enabledModels) == 0 {
		return
	}
	reference := fmt.Sprintf("%s/%s", model.Provider, model.ID)
	for _, pattern := range enabledModels {
		if strings.EqualFold(pattern, reference) {
			return
		}
	}
	s.control.Settings.SetEnabledModels(append(enabledModels, reference))
}

// SetThinkingLevel clamps the level to the model's capabilities, appends a
// transcript entry when it changes, and optionally persists the requested level.
func (s *AgentSession) SetThinkingLevel(level ai.ThinkingLevel, optionList ...ModelMutationOptions) {
	options := ModelMutationOptions{}
	if len(optionList) > 0 {
		options = optionList[0]
	}
	available := s.GetAvailableThinkingLevels()
	effective := level
	found := false
	for _, candidate := range available {
		if candidate == level {
			found = true
			break
		}
	}
	if !found {
		effective = s.clampThinkingLevel(level)
	}

	previous := s.ThinkingLevel()
	changed := effective != previous
	s.Agent.SetThinkingLevel(effective)

	if options.Persist && s.control != nil && s.control.Settings != nil {
		s.control.Settings.SetDefaultThinkingLevel(level)
	}
	if changed {
		s.Sessions.AppendThinkingLevelChange(effective)
		s.emit(&SessionEvent{Type: SessionThinkingLevelChanged, Level: effective})
	}
}

// CycleThinkingLevel advances to the next available level.
func (s *AgentSession) CycleThinkingLevel(options ModelMutationOptions) (ai.ThinkingLevel, bool) {
	if !s.SupportsThinking() {
		return "", false
	}
	levels := s.GetAvailableThinkingLevels()
	if len(levels) == 0 {
		return "", false
	}
	current := s.ThinkingLevel()
	index := 0
	for position, level := range levels {
		if level == current {
			index = position
			break
		}
	}
	next := levels[(index+1)%len(levels)]
	s.SetThinkingLevel(next, options)
	return next, true
}

// GetAvailableThinkingLevels lists the levels the current model supports.
func (s *AgentSession) GetAvailableThinkingLevels() []ai.ThinkingLevel {
	model := s.Model()
	if model == nil {
		return append([]ai.ThinkingLevel{}, ThinkingLevelOptions...)
	}
	supported := ai.GetSupportedThinkingLevels(model)
	out := make([]ai.ThinkingLevel, 0, len(supported))
	for _, level := range supported {
		out = append(out, level)
	}
	return out
}

// SupportsThinking reports whether the current model supports reasoning.
func (s *AgentSession) SupportsThinking() bool {
	model := s.Model()
	return model != nil && model.Reasoning
}

// getThinkingLevelForModelSwitch resolves the level to apply on a model change:
// an explicit level wins, then the per-model default, then the global default,
// then the current level, then the built-in default.
func (s *AgentSession) getThinkingLevelForModelSwitch(target *ai.Model, explicit ai.ThinkingLevel) ai.ThinkingLevel {
	if explicit != "" {
		return explicit
	}
	if target != nil && s.control != nil && s.control.Settings != nil {
		if perModel := s.control.Settings.GetModelThinkingLevel(target.Provider, target.ID); perModel != nil {
			return *perModel
		}
	}
	if s.control != nil && s.control.Settings != nil {
		if global := s.control.Settings.GetDefaultThinkingLevel(); global != nil {
			return *global
		}
	}
	if current := s.ThinkingLevel(); current != "" {
		return current
	}
	return DefaultThinkingLevel
}

func (s *AgentSession) clampThinkingLevel(level ai.ThinkingLevel) ai.ThinkingLevel {
	model := s.Model()
	if model == nil {
		return ai.ThinkOff
	}
	return ai.ClampThinkingLevel(model, level)
}

func (s *AgentSession) emitModelSelect(model, previous *ai.Model, source string) {
	// Upstream emits a model_select event; the Go session carries it on the
	// model cycle result and the message stream, so only the transcript entry
	// and thinking-level event are emitted here.
	_ = model
	_ = previous
	_ = source
}

// GetLastAssistantText returns the last assistant message's trimmed text, or ""
// when there is none (port of getLastAssistantText).
func (s *AgentSession) GetLastAssistantText() string {
	messages := s.Messages()
	for index := len(messages) - 1; index >= 0; index-- {
		assistant, ok := messages[index].(*ai.AssistantMessage)
		if !ok {
			continue
		}
		// Skip aborted messages with no content.
		if assistant.StopReason == ai.StopAborted && len(assistant.Content) == 0 {
			continue
		}
		var builder strings.Builder
		for _, block := range assistant.Content {
			if text, ok := block.(ai.TextContent); ok {
				builder.WriteString(text.Text)
			}
		}
		if trimmed := strings.TrimSpace(builder.String()); trimmed != "" {
			return trimmed
		}
		return ""
	}
	return ""
}

// UserMessageFork is one user message available for forking.
type UserMessageFork struct {
	EntryID string
	Text    string
}

// GetUserMessagesForForking lists the user messages with their entry ids.
func (s *AgentSession) GetUserMessagesForForking() []UserMessageFork {
	var out []UserMessageFork
	for _, entry := range s.Sessions.GetEntries() {
		if entry.Type != "message" {
			continue
		}
		message, err := ai.UnmarshalMessage(entry.Message)
		if err != nil {
			continue
		}
		user, ok := message.(*ai.UserMessage)
		if !ok {
			continue
		}
		text := user.Content.Text
		if text != "" {
			out = append(out, UserMessageFork{EntryID: entry.ID, Text: text})
		}
	}
	return out
}

// ContextUsageReport is the context-window usage; Tokens and Percent are nil
// when the count is unknown (upstream's null).
type ContextUsageReport struct {
	Tokens        *int64
	ContextWindow int64
	Percent       *float64
}

// GetContextUsage computes context usage, or nil when no model/window is known.
func (s *AgentSession) GetContextUsage() *ContextUsageReport {
	model := s.Model()
	if model == nil {
		return nil
	}
	contextWindow := model.ContextWindow
	if contextWindow <= 0 {
		return nil
	}

	// After compaction the last assistant usage reflects the pre-compaction
	// context, so usage is only trusted from an assistant that responded after
	// the latest compaction. Without one the count is unknown.
	branch := s.Sessions.GetBranch("")
	latestCompaction := GetLatestCompactionEntry(branch)
	if latestCompaction != nil {
		compactionIndex := -1
		for index, entry := range branch {
			if entry.ID == latestCompaction.ID {
				compactionIndex = index
			}
		}
		hasPostCompactionUsage := false
		for index := len(branch) - 1; index > compactionIndex; index-- {
			entry := branch[index]
			if entry.Type != "message" {
				continue
			}
			message, err := ai.UnmarshalMessage(entry.Message)
			if err != nil {
				continue
			}
			assistant, ok := message.(*ai.AssistantMessage)
			if !ok {
				continue
			}
			if assistant.StopReason == ai.StopAborted || assistant.StopReason == ai.StopError {
				continue
			}
			if ai.CalculateContextTokens(assistant.Usage) > 0 {
				hasPostCompactionUsage = true
				break
			}
		}
		if !hasPostCompactionUsage {
			return &ContextUsageReport{ContextWindow: contextWindow}
		}
	}

	estimate := EstimateContextTokens(s.Messages())
	tokens := int64(estimate.Tokens)
	percent := float64(estimate.Tokens) / float64(contextWindow) * 100
	return &ContextUsageReport{Tokens: &tokens, ContextWindow: contextWindow, Percent: &percent}
}

// Dispose aborts every active operation and drops listeners.
func (s *AgentSession) Dispose() {
	defer func() { _ = recover() }()
	s.AbortRetry()
	s.AbortBash()
	s.Agent.Abort()
	// The session's resources (temp files, watchers) are released with the
	// session id, like upstream's cleanupSessionResources(this.sessionId).
	if err := CleanupSessionResources(s.SessionID()); err != nil {
		// Cleanup is best-effort; the remaining dispose steps still run.
		_ = err
	}
	if s.control != nil {
		s.control.stateMu.Lock()
		s.control.disposed = true
		s.control.stateMu.Unlock()
	}
	s.listenerMu.Lock()
	s.listeners = nil
	s.listenerMu.Unlock()
}
