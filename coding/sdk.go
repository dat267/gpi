package coding

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/dat267/pier/agent"
	"github.com/dat267/pier/ai"
)

// Port of the extension-free core of core/sdk.ts createAgentSession: the
// programmatic session assembly (model restore and fallback, thinking-level
// restore and clamp, tool selection, the block-images transcript filter, the
// settings-backed request options, cache warming, and the agent/session
// construction).
//
// D41: the resource loader, extension runner, session start events, and custom
// tools are extension mechanics and omitted; the tool set comes from the
// built-in registry filtered by name.

// defaultActiveToolNames is the default tool selection.
var defaultActiveToolNames = []ToolName{ToolNameRead, ToolNameBash, ToolNameEdit, ToolNameWrite}

// NoTools values, matching upstream's main.ts.
const (
	// NoToolsAll disables every tool, built-in or otherwise.
	NoToolsAll = "all"
	// NoToolsBuiltin disables the built-in tools while leaving the registry
	// populated, so caller-supplied tools stay available.
	NoToolsBuiltin = "builtin"
)

// CreateAgentSessionOptions are the session assembly inputs.
type CreateAgentSessionOptions struct {
	Cwd      string
	AgentDir string
	// Model pins the model, skipping restore and resolution.
	Model *ai.Model
	// ThinkingLevel pins the thinking level.
	ThinkingLevel ai.ThinkingLevel
	// Tools names the allowed tools; nil means the configured defaults.
	Tools []ToolName
	// ExcludeTools removes tools from the selection.
	ExcludeTools []ToolName
	// NoTools disables the default set. NoToolsAll disables every tool;
	// NoToolsBuiltin disables the built-ins while leaving the registry in
	// place for caller-supplied tools. Any other non-empty value behaves like
	// NoToolsAll (upstream tests the option for truthiness).
	NoTools string
	// ScopedModels seeds the model cycle scope.
	ScopedModels    []ScopedModel
	SessionManager  *SessionManager
	ModelRuntime    *ModelRuntime
	SettingsManager *SettingsManager
	// StreamFn overrides the model stream function (compaction/summaries keep
	// the session's).
	StreamFn agent.StreamFn
	// SystemPrompt replaces the default prefix: a prompt source (text, or a
	// path to read). Nil discovers SYSTEM.md; a non-nil empty value means no
	// custom prompt (upstream systemPromptSource).
	SystemPrompt *string
	// AppendSystemPrompt appends to the system prompt. Each entry is a prompt
	// source; nil discovers APPEND_SYSTEM.md. Multiple entries join with a
	// blank line.
	AppendSystemPrompt []string
}

// CreateAgentSessionResult is the assembled session.
type CreateAgentSessionResult struct {
	Session *AgentSession
	// ModelFallbackMessage explains a model restore or resolution fallback.
	ModelFallbackMessage string
	// ModelDefaultMessage is the modeldefault sync notice (D151); empty when
	// the session was already on the default or settings name no default.
	ModelDefaultMessage string
	// ModelDefaultWarning marks ModelDefaultMessage as a warning rather than
	// information (the default could not be applied).
	ModelDefaultWarning bool
}

// CreateAgentSession assembles a configured coding-agent session.
func CreateAgentSession(ctx context.Context, options *CreateAgentSessionOptions) (*CreateAgentSessionResult, error) {
	if options == nil {
		options = &CreateAgentSessionOptions{}
	}
	cwd := options.Cwd
	if cwd == "" && options.SessionManager != nil {
		cwd = options.SessionManager.GetCwd()
	}
	if cwd == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return nil, err
		}
		_ = cwd
	}
	agentDir := options.AgentDir
	if agentDir == "" {
		agentDir = GetAgentDir()
	}
	modelRuntime := options.ModelRuntime
	if modelRuntime == nil {
		created, err := CreateModelRuntime(CreateModelRuntimeOptions{
			AuthPath:   filepath.Join(agentDir, "auth.json"),
			ModelsPath: filepath.Join(agentDir, "models.json"),
		})
		if err != nil {
			return nil, err
		}
		modelRuntime = created
	}
	settingsManager := options.SettingsManager
	if settingsManager == nil {
		settingsManager = NewSettingsManagerFromFiles(cwd, agentDir, SettingsManagerCreateOptions{})
	}
	sessionManager := options.SessionManager
	if sessionManager == nil {
		sessionManager = NewSessionManager(cwd, &SessionManagerOptions{
			SessionDir: DefaultSessionDir(cwd, agentDir),
		})
	}

	// Restore the model and thinking level from an existing session.
	existingSession := sessionManager.Projection()
	hasExistingSession := len(existingSession.Messages) > 0
	hasThinkingEntry := false
	for _, entry := range sessionManager.GetBranch("") {
		if entry.Type == "thinking_level_change" {
			hasThinkingEntry = true
			break
		}
	}

	model := options.Model
	var modelFallbackMessage string
	if model == nil && hasExistingSession && existingSession.Model != nil {
		ref := existingSession.Model
		restored := modelRuntime.GetModel(ref.Provider, ref.ModelID)
		if restored != nil && modelRuntime.HasConfiguredAuth(restored.Provider) {
			model = restored
		}
		if model == nil {
			modelFallbackMessage = fmt.Sprintf("Could not restore model %s/%s", ref.Provider, ref.ModelID)
		}
	}

	// Resolve the model from the settings defaults when still unset.
	if model == nil {
		defaultThinking := settingsManager.GetDefaultThinkingLevel()
		result := FindInitialModel(FindInitialModelOptions{
			IsContinuing:         hasExistingSession,
			DefaultProvider:      derefString(settingsManager.GetDefaultProvider()),
			DefaultModelID:       derefString(settingsManager.GetDefaultModel()),
			DefaultThinkingLevel: derefString(defaultThinking),
			HasDefaultThinking:   defaultThinking != nil,
			ModelThinkingLevels:  settingsManager.GetAllModelThinkingLevels(),
			ModelRuntime:         modelRuntime,
		})
		model = result.Model
		if model == nil {
			modelFallbackMessage = FormatNoModelsAvailableMessage()
		} else if modelFallbackMessage != "" {
			modelFallbackMessage += fmt.Sprintf(". Using %s/%s", model.Provider, model.ID)
		}
	}

	// Thinking level: the pinned level, then the session entry, then per-model,
	// then the global default.
	thinkingLevel := options.ThinkingLevel
	if thinkingLevel == "" && hasExistingSession {
		if hasThinkingEntry && existingSession.ThinkingLevel != "" {
			thinkingLevel = existingSession.ThinkingLevel
		} else if global := settingsManager.GetDefaultThinkingLevel(); global != nil {
			thinkingLevel = *global
		} else {
			thinkingLevel = DefaultThinkingLevel
		}
	}
	if thinkingLevel == "" && model != nil {
		if perModel := settingsManager.GetModelThinkingLevel(model.Provider, model.ID); perModel != nil {
			thinkingLevel = *perModel
		}
	}
	if thinkingLevel == "" {
		if global := settingsManager.GetDefaultThinkingLevel(); global != nil {
			thinkingLevel = *global
		} else {
			thinkingLevel = DefaultThinkingLevel
		}
	}
	if model == nil {
		thinkingLevel = ai.ThinkOff
	} else {
		thinkingLevel = ai.ClampThinkingLevel(model, thinkingLevel)
	}

	// Tool selection: the allowed names, then noTools, then the configured
	// defaults, then the built-in defaults; exclusions apply last.
	var initialActiveToolNames []ToolName
	if options.Tools != nil {
		initialActiveToolNames = append([]ToolName{}, options.Tools...)
	} else if options.NoTools != "" {
		initialActiveToolNames = []ToolName{}
	} else if configured := settingsManager.GetDefaultTools(); configured != nil {
		// An explicitly empty defaultTools is a value, not an absent one:
		// upstream reads it with nullish coalescing, so `[]` selects no tools
		// and only a missing key falls through to the built-in default.
		initialActiveToolNames = append([]ToolName{}, configured...)
	} else {
		initialActiveToolNames = append([]ToolName{}, defaultActiveToolNames...)
	}
	if len(options.ExcludeTools) > 0 {
		excluded := map[ToolName]bool{}
		for _, name := range options.ExcludeTools {
			excluded[name] = true
		}
		filtered := make([]ToolName, 0, len(initialActiveToolNames))
		for _, name := range initialActiveToolNames {
			if !excluded[name] {
				filtered = append(filtered, name)
			}
		}
		initialActiveToolNames = filtered
	}

	// The registry holds every built-in tool (upstream createAllToolDefinitions);
	// the active selection is filtered by name.
	toolByName := CreateAllTools(cwd, nil)
	activeTools := make([]agent.AgentTool, 0, len(initialActiveToolNames))
	for _, name := range initialActiveToolNames {
		if tool, ok := toolByName[name]; ok {
			activeTools = append(activeTools, tool)
		}
	}

	// The transcript filter drops images when block-images is enabled
	// (defense-in-depth; the setting is checked per conversion so mid-session
	// changes take effect).
	convertToLlmWithBlockImages := func(messages []ai.Message) []ai.Message {
		converted := ConvertToLlm(messages)
		if !settingsManager.GetBlockImages() {
			return converted
		}
		const disabledText = "Image reading is disabled."
		out := make([]ai.Message, 0, len(converted))
		for _, message := range converted {
			switch typed := message.(type) {
			case *ai.UserMessage:
				if !contentListHasImages(typed.Content.Blocks) {
					out = append(out, message)
					continue
				}
				filtered := filterContentListBlocks(typed.Content.Blocks, disabledText)
				out = append(out, &ai.UserMessage{Content: ai.StringOrBlocks{Blocks: filtered}, Timestamp: typed.Timestamp})
			case *ai.ToolResultMessage:
				if !userContentHasImages(typed.Content) {
					out = append(out, message)
					continue
				}
				filtered := filterUserContentBlocks(typed.Content, disabledText)
				out = append(out, &ai.ToolResultMessage{
					ToolCallID: typed.ToolCallID, ToolName: typed.ToolName, Content: filtered,
					Details: typed.Details, Usage: typed.Usage, IsError: typed.IsError, Timestamp: typed.Timestamp,
				})
			default:
				out = append(out, message)
			}
		}
		return out
	}

	// The session stream function applies the settings-backed request options
	// and restarts cache warming from session requests.
	cacheWarmer := NewCacheWarmer(nil, sessionManager, func() CacheWarmingMode {
		return settingsManager.GetCacheWarmingMode()
	})
	buildRequestOptions := func(requestModel *ai.Model, requestOptions *ai.SimpleStreamOptions) *ai.SimpleStreamOptions {
		merged := ai.SimpleStreamOptions{}
		if requestOptions != nil {
			merged = *requestOptions
		}
		providerRetry := settingsManager.GetProviderRetrySettings()
		idleTimeout, err := settingsManager.GetHTTPIdleTimeoutMS()
		if err != nil {
			idleTimeout = DefaultHTTPIdleTimeoutMS
		}
		effectiveTimeout := idleTimeout
		if effectiveTimeout == 0 {
			effectiveTimeout = 2147483647
		}
		if merged.TimeoutMs == nil {
			timeout := int(effectiveTimeout)
			if providerRetry.TimeoutMS != nil {
				timeout = int(*providerRetry.TimeoutMS)
			}
			merged.TimeoutMs = &timeout
		}
		if merged.MaxRetries == nil && providerRetry.MaxRetries != nil {
			merged.MaxRetries = providerRetry.MaxRetries
		}
		if merged.MaxRetryDelayMs == nil {
			maxDelay := int(providerRetry.MaxRetryDelayMS)
			merged.MaxRetryDelayMs = &maxDelay
		}
		if connectTimeout, ok := settingsManager.GetWebSocketConnectTimeoutMS(); ok && merged.WebsocketConnectTimeoutMs == nil {
			connect := int(connectTimeout)
			merged.WebsocketConnectTimeoutMs = &connect
		}
		previousTransform := merged.TransformHeaders
		merged.TransformHeaders = func(requestHeaders ai.ProviderHeaders) ai.ProviderHeaders {
			mergedHeaders := MergeProviderAttributionHeaders(requestModel, settingsManager, merged.SessionID, headersToStrings(requestHeaders))
			transformed := stringHeaders(mergedHeaders)
			if previousTransform != nil {
				return previousTransform(transformed)
			}
			return transformed
		}
		return &merged
	}

	providerRetry := settingsManager.GetProviderRetrySettings()
	sessionID := sessionManager.GetSessionID()
	retryResolved := settingsManager.GetRetrySettings()
	retryPolicy := &ai.RetryPolicy{
		Enabled: retryResolved.Enabled, MaxRetries: retryResolved.MaxRetries,
		BaseDelayMS: int(retryResolved.BaseDelayMS), MaxAgentDelayMS: retryResolved.MaxAgentDelayMS,
	}
	maxRetryDelay := int(providerRetry.MaxRetryDelayMS)
	// The request-options wiring always wraps the transport: the settings-backed
	// timeouts/retries and cache warming apply to any stream function.
	innerStreamFn := options.StreamFn
	if innerStreamFn == nil {
		innerStreamFn = func(requestModel *ai.Model, requestContext ai.TranscriptContext, requestOptions *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
			return modelRuntime.StreamSimple(requestModel, ai.Context{Messages: requestContext.Messages}, &ai.ModelsSimpleStreamOptions{SimpleStreamOptions: *requestOptions})
		}
	}
	streamFn := func(requestModel *ai.Model, requestContext ai.TranscriptContext, requestOptions *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
		merged := buildRequestOptions(requestModel, requestOptions)
		// Compaction and summaries use their own routing ids; only session
		// requests replace the cache entry, so warming restarts from them.
		if merged.SessionID == sessionManager.GetSessionID() {
			cacheWarmer.Start(CacheWarmRequest{
				Model: requestModel, Context: ai.Context{Messages: requestContext.Messages}, Options: merged,
			}, cacheContextIsCurrent(sessionManager, requestModel))
		}
		return innerStreamFn(requestModel, requestContext, merged)
	}

	// Context files and skills feed the system prompt (upstream
	// agent-session._buildRuntime reads them from the resource loader):
	// the global agent-dir context file plus every workspace ancestor's,
	// and skills from the settings paths plus a trusted project's
	// .pi/skills.
	contextFiles := LoadProjectContextFiles(cwd, agentDir)
	projectTrusted := settingsManager.IsProjectTrusted()
	skillsResult := LoadSkills(LoadSkillsOptions{
		Cwd: cwd, AgentDir: agentDir, SkillPaths: settingsManager.GetSkillPaths(),
		IncludeDefaults: true,
	}, projectTrusted)
	// The base/append system prompt follows the same rule as upstream's
	// resource loader: an explicit --system-prompt / --append-system-prompt
	// source wins, otherwise the project's (trusted) or agent-dir file.
	promptSources := PromptFileSources{
		Cwd: cwd, AgentDir: agentDir, ProjectTrusted: projectTrusted,
		SystemPrompt: options.SystemPrompt, AppendSystemPrompt: options.AppendSystemPrompt,
	}
	promptOverrides := LoadPromptOverrides(promptSources)

	session, err := NewAgentSession(&SessionConfig{
		Cwd:                cwd,
		Model:              model,
		StreamFn:           streamFn,
		Tools:              activeTools,
		Sessions:           sessionManager,
		Settings:           SessionSettings{Retry: retryPolicy, Compaction: compactionSettingsOf(settingsManager, model)},
		ThinkingLevel:      thinkingLevel,
		Skills:             skillsResult.Skills,
		ContextFiles:       contextFiles,
		SkillDiagnostics:   skillsResult.Diagnostics,
		SystemPrompt:       promptOverrides.SystemPrompt,
		AppendSystemPrompt: promptOverrides.AppendSystemPrompt,
		PromptSourcePaths:  promptOverrides.SourcePaths,
		AgentDir:           agentDir,
		PromptSources:      &promptSources,
		Control: &AgentSessionControl{
			ModelRuntime: modelRuntime, Settings: settingsManager,
			Tools: map[string]AgentToolDefinition{}, autoCompaction: true, autoRetry: true,
		},
		ConvertToLlm:    convertToLlmWithBlockImages,
		SessionID:       sessionID,
		SteeringMode:    settingsManager.GetSteeringMode(),
		FollowUpMode:    settingsManager.GetFollowUpMode(),
		Transport:       ai.Transport(settingsManager.GetTransport()),
		ThinkingBudgets: thinkingBudgetsOf(settingsManager.GetThinkingBudgets()),
		MaxRetryDelayMS: &maxRetryDelay,
	})
	if err != nil {
		return nil, err
	}

	// Restore messages or record the initial model/thinking state so resume can
	// restore it.
	if hasExistingSession {
		session.Agent.SetMessages(existingSession.Messages)
		if !hasThinkingEntry {
			sessionManager.AppendThinkingLevelChange(thinkingLevel)
		}
	} else {
		if model != nil {
			sessionManager.AppendModelChange(model.Provider, model.ID)
		}
		sessionManager.AppendThinkingLevelChange(thinkingLevel)
	}

	session.streamFn = streamFn
	for name, tool := range toolByName {
		session.control.Tools[name] = AgentToolDefinition{Tool: tool}
	}
	session.SetScopedModels(options.ScopedModels)
	// modeldefault (D151): a restored session would otherwise keep the model it
	// chose in an earlier run, leaving the settings default unreachable for it.
	// An explicit --model/--provider choice is the caller saying "this session,
	// this model", so the sync defers to it.
	var modelDefault ModelDefaultSyncResult
	if options.Model == nil {
		modelDefault = session.SyncSessionModelToDefault(ctx, "session start")
		session.recordModelDefaultSync(modelDefault)
	} else {
		session.suspendModelDefaultSync()
	}
	session.CacheWarmer = cacheWarmer
	if cacheWarmer != nil {
		// Upstream wires the warmed usage entry into an entry_appended event.
		cacheWarmer.OnWarmed = func(entry *SessionEntry) {
			session.emit(&SessionEvent{Type: SessionEntryAppended, Entry: entry})
		}
	}

	return &CreateAgentSessionResult{
		Session:              session,
		ModelFallbackMessage: modelFallbackMessage,
		ModelDefaultMessage:  modelDefault.Message,
		ModelDefaultWarning:  modelDefault.Warning,
	}, nil
}

// cacheContextIsCurrent reports whether the session's context still extends the
// warmed request: the model is the request's model and no earlier message was
// dropped. The currency check is re-evaluated while the warmer runs (and by the
// status display), so it compares cheap context signatures instead of
// projecting the session.
func cacheContextIsCurrent(sessionManager *SessionManager, requestModel *ai.Model) func() bool {
	warm := sessionManager.ContextSignature()
	return func() bool {
		current := sessionManager.ContextSignature()
		if current.Provider != string(requestModel.Provider) || current.ModelID != requestModel.ID {
			return false
		}
		return warm.MessageCount <= current.MessageCount
	}
}

func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func thinkingBudgetsOf(settings *SettingsThinkingBudgets) *ai.ThinkingBudgets {
	if settings == nil {
		return nil
	}
	budgets := &ai.ThinkingBudgets{}
	if settings.Minimal != nil {
		minimal := int(*settings.Minimal)
		budgets.Minimal = &minimal
	}
	if settings.Low != nil {
		low := int(*settings.Low)
		budgets.Low = &low
	}
	if settings.Medium != nil {
		medium := int(*settings.Medium)
		budgets.Medium = &medium
	}
	if settings.High != nil {
		high := int(*settings.High)
		budgets.High = &high
	}
	return budgets
}

func compactionSettingsOf(settingsManager *SettingsManager, model *ai.Model) CompactionSettings {
	resolved, err := settingsManager.GetCompactionSettings(model)
	if err != nil {
		return DefaultCompactionSettings
	}
	return CompactionSettings{
		Enabled: resolved.Enabled, ReserveTokens: resolved.ReserveTokens,
		KeepRecentTokens: resolved.KeepRecentTokens,
	}
}

var _ = strings.TrimSpace

// contentListHasImages reports whether any block is an image.
func contentListHasImages(blocks ai.ContentList) bool {
	for _, block := range blocks {
		if _, ok := block.(ai.ImageContent); ok {
			return true
		}
	}
	return false
}

// userContentHasImages is the tool-result variant.
func userContentHasImages(blocks ai.UserContentList) bool {
	for _, block := range blocks {
		if _, ok := block.(ai.ImageContent); ok {
			return true
		}
	}
	return false
}

// filterContentListBlocks replaces images with the disabled text, deduplicating
// consecutive disabled texts.
func filterContentListBlocks(blocks ai.ContentList, disabledText string) ai.ContentList {
	filtered := make(ai.ContentList, 0, len(blocks))
	appendDisabled := func() {
		if previous, ok := lastBlock(filtered).(ai.TextContent); ok && previous.Text == disabledText {
			return
		}
		filtered = append(filtered, ai.TextContent{Text: disabledText})
	}
	for _, block := range blocks {
		if _, ok := block.(ai.ImageContent); ok {
			appendDisabled()
			continue
		}
		if text, ok := block.(ai.TextContent); ok && text.Text == disabledText && len(filtered) > 0 {
			if previous, ok := filtered[len(filtered)-1].(ai.TextContent); ok && previous.Text == disabledText {
				continue
			}
		}
		filtered = append(filtered, block)
	}
	return filtered
}

// lastBlock returns the final block of a content list.
func lastBlock(blocks ai.ContentList) ai.Content {
	if len(blocks) == 0 {
		return nil
	}
	return blocks[len(blocks)-1]
}

// lastUserBlock is the tool-result variant.
func lastUserBlock(blocks ai.UserContentList) ai.UserContent {
	if len(blocks) == 0 {
		return nil
	}
	return blocks[len(blocks)-1]
}

// filterUserContentBlocks is the tool-result variant.
func filterUserContentBlocks(blocks ai.UserContentList, disabledText string) ai.UserContentList {
	filtered := make(ai.UserContentList, 0, len(blocks))
	appendDisabled := func() {
		if previous, ok := lastUserBlock(filtered).(ai.TextContent); ok && previous.Text == disabledText {
			return
		}
		filtered = append(filtered, ai.TextContent{Text: disabledText})
	}
	for _, block := range blocks {
		if _, ok := block.(ai.ImageContent); ok {
			appendDisabled()
			continue
		}
		if text, ok := block.(ai.TextContent); ok && text.Text == disabledText && len(filtered) > 0 {
			if previous, ok := filtered[len(filtered)-1].(ai.TextContent); ok && previous.Text == disabledText {
				continue
			}
		}
		filtered = append(filtered, block)
	}
	return filtered
}
