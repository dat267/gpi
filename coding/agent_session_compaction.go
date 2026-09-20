package coding

import (
	"context"

	"github.com/dat267/gpi/ai"
)

// Port of the automatic-compaction decision (core/agent-session.ts
// _checkCompaction / _runAutoCompaction): the overflow, recoverable-length, and
// threshold cases with their guard rails.

// overflowRecoveryState is the one-attempt overflow recovery bookkeeping.
type overflowRecoveryState struct {
	attempted bool
}

// CheckCompaction decides whether automatic compaction should run for the given
// assistant message and runs it. It reports whether the caller should continue
// the agent (overflow recovery retries the turn).
func (s *AgentSession) CheckCompaction(ctx context.Context, assistant *ai.AssistantMessage, skipAbortedCheck bool) (bool, error) {
	if assistant == nil {
		return false, nil
	}
	settings, enabled := s.compactionSettings()
	if !enabled {
		return false, nil
	}

	// An aborted message (user cancelled) is skipped unless the caller wants it
	// considered (the pre-prompt check).
	if skipAbortedCheck && assistant.StopReason == ai.StopAborted {
		return false, nil
	}

	model := s.Model()
	contextWindow := int64(0)
	if model != nil {
		contextWindow = model.ContextWindow
	}

	// Overflow from a different model must not trigger compaction for the
	// current one (for example after switching to a larger-context model).
	sameModel := model != nil && string(assistant.Provider) == string(model.Provider) && assistant.Model == model.ID

	// Stale pre-compaction usage or errors must not retrigger compaction right
	// after one finished.
	branch := s.Sessions.GetBranch("")
	compactionEntry := GetLatestCompactionEntry(branch)
	if compactionEntry != nil && assistant.Timestamp > 0 {
		if assistant.Timestamp <= entryTimestampMS(compactionEntry) {
			return false, nil
		}
	}

	// Cases 1 and 2: context overflow (or a recoverable length stop).
	recoverableLength := sameModel && ai.IsRecoverableLength(assistant, maxTokensOf(model))
	contextOverflow := sameModel && ai.IsContextOverflow(assistant, contextWindow)
	if contextOverflow || recoverableLength {
		willRetry := assistant.StopReason != ai.StopStop

		// Case 2: the response completed successfully; compact without retrying
		// (agent.continue cannot continue a completed response).
		if !willRetry {
			return s.RunAutoCompaction(ctx, CompactionOverflow, false)
		}

		s.mu.Lock()
		attempted := s.overflowRecovery.attempted
		s.mu.Unlock()
		if attempted {
			errorMessage := "Context overflow recovery failed after one compact-and-retry attempt. Try reducing context or switching to a larger-context model."
			if !contextOverflow {
				errorMessage = "Truncated response recovery failed after one compact-and-retry attempt."
			}
			s.emit(&SessionEvent{
				Type: SessionCompactionEnd, Reason: CompactionOverflow,
				Aborted: false, ErrorMessage: errorMessage,
			})
			return false, nil
		}

		// Case 1: drop the failed/truncated message from agent state, compact,
		// and retry once. It stays in session history.
		s.mu.Lock()
		s.overflowRecovery.attempted = true
		s.mu.Unlock()
		messages := s.Agent.State().Messages
		if len(messages) > 0 {
			if _, ok := messages[len(messages)-1].(*ai.AssistantMessage); ok {
				s.Agent.SetMessages(messages[:len(messages)-1])
			}
		}
		return s.RunAutoCompaction(ctx, CompactionOverflow, willRetry)
	}

	// Case 3: threshold compaction without retry. Error or zero-usage messages
	// estimate from the last valid response so persistent API errors can still
	// compact and do not reset context accounting.
	contextTokens := int64(0)
	directContextTokens := int64(ai.CalculateContextTokens(assistant.Usage))
	if assistant.StopReason == ai.StopError || directContextTokens == 0 {
		messages := s.Agent.State().Messages
		estimate := EstimateContextTokens(messages)
		if estimate.LastUsageIndex >= 0 && compactionEntry != nil && estimate.LastUsageIndex < len(messages) {
			// Only usage-backed estimates need the stale pre-compaction check:
			// kept pre-compaction usage reflects the old, larger context.
			if usageAssistant, ok := messages[estimate.LastUsageIndex].(*ai.AssistantMessage); ok {
				if usageAssistant.Timestamp > 0 && usageAssistant.Timestamp <= entryTimestampMS(compactionEntry) {
					return false, nil
				}
			}
		}
		contextTokens = int64(estimate.Tokens)
	} else {
		contextTokens = directContextTokens
	}

	if ShouldCompact(contextTokens, contextWindow, settings) {
		return s.RunAutoCompaction(ctx, CompactionThreshold, false)
	}
	return false, nil
}

func maxTokensOf(model *ai.Model) int64 {
	if model == nil {
		return 0
	}
	return model.MaxTokens
}

// compactionSettings resolves the compaction settings and whether automatic
// compaction is enabled: the settings manager decides when one is wired
// (upstream's setAutoCompactionEnabled writes there), otherwise the session
// toggle gates the session-level settings.
func (s *AgentSession) compactionSettings() (CompactionSettings, bool) {
	if s.control != nil && s.control.Settings != nil {
		resolved, err := s.control.Settings.GetCompactionSettings(s.Model())
		if err == nil {
			return CompactionSettings{
				Enabled: resolved.Enabled, ReserveTokens: resolved.ReserveTokens,
				KeepRecentTokens: resolved.KeepRecentTokens,
			}, resolved.Enabled
		}
	}
	settings := s.Settings.Compaction
	if settings.ReserveTokens == 0 && settings.KeepRecentTokens == 0 {
		settings = DefaultCompactionSettings
	}
	if s.control != nil {
		return settings, settings.Enabled && s.AutoCompactionEnabled()
	}
	return settings, settings.Enabled
}

// RunAutoCompaction executes threshold or overflow compaction. It reports
// whether the post-run loop should continue the agent.
func (s *AgentSession) RunAutoCompaction(ctx context.Context, reason CompactionReason, willRetry bool) (bool, error) {
	model := s.Model()
	if !s.HasModel() || model == nil {
		return false, nil
	}
	settings, _ := s.compactionSettings()
	pathEntries := s.Sessions.GetBranch("")
	preparation := PrepareCompaction(pathEntries, settings)
	if preparation == nil {
		return false, nil
	}

	s.emit(&SessionEvent{Type: SessionCompactionStart, Reason: reason})
	compactionCtx, cancel := context.WithCancel(ctx)
	s.mu.Lock()
	s.compactionCancel = cancel
	s.compactionActive = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.compactionActive = false
		s.compactionCancel = nil
		s.mu.Unlock()
		cancel()
	}()

	options := CompactionOptions{
		Model: model, Ctx: compactionCtx, StreamFn: s.compactionStreamFn(),
		Retry: s.retrySettings(), SessionID: s.Sessions.GetSessionID(),
		Callbacks: s.summarizationRetryCallbacksForCompaction(reason),
	}
	if s.control != nil && s.control.ModelRuntime != nil {
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

	result, err := Compact(preparation, options)
	if err != nil {
		s.emit(&SessionEvent{
			Type: SessionCompactionEnd, Reason: reason, Aborted: compactionCtx.Err() != nil,
			ErrorMessage: err.Error(),
		})
		return false, err
	}
	if compactionCtx.Err() != nil {
		s.emit(&SessionEvent{Type: SessionCompactionEnd, Reason: reason, Aborted: true})
		return false, compactionCtx.Err()
	}

	s.Sessions.AppendCompaction(result.Summary, result.FirstKeptEntryID, result.TokensBefore, result.Details, false, result.Usage)
	sessionContext := s.Sessions.BuildSessionContext()
	s.Agent.SetMessages(sessionContext.Messages)

	s.emit(&SessionEvent{Type: SessionCompactionEnd, Reason: reason, Result: result})
	// An overflow recovery that consumed the failed message retries the turn.
	return willRetry, nil
}

func headersToStrings(headers ai.ProviderHeaders) map[string]string {
	if len(headers) == 0 {
		return nil
	}
	out := map[string]string{}
	for key, value := range headers {
		if value != nil {
			out[key] = *value
		}
	}
	return out
}

// AbortCompaction cancels an in-flight automatic or manual compaction.
func (s *AgentSession) AbortCompaction() {
	s.mu.Lock()
	cancel := s.compactionCancel
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// summarizationRetryCallbacksForCompaction builds the compaction callbacks
// (the attempt-start event carries the compaction reason).
func (s *AgentSession) summarizationRetryCallbacksForCompaction(reason CompactionReason) *SummarizationCallbacks {
	callbacks := s.summarizationRetryCallbacks("compaction")
	callbacks.OnRetryAttemptStart = func() error {
		s.emit(&SessionEvent{Type: SessionSummarizationRetryAttemptStart, Source: "compaction", Reason: reason})
		return nil
	}
	return callbacks
}

// summarizationRetryCallbacks builds the retry callbacks shared by compaction
// and branch-summary summarization (upstream _summarizationRetryCallbacks).
func (s *AgentSession) summarizationRetryCallbacks(source string) *SummarizationCallbacks {
	return &SummarizationCallbacks{
		OnRetryScheduled: func(attempt, maxAttempts int, delayMS int64, errorMessage string) error {
			s.emit(&SessionEvent{
				Type: SessionSummarizationRetryScheduled, Source: source,
				Attempt: attempt, MaxAttempts: maxAttempts, DelayMS: delayMS, ErrorMessage: errorMessage,
			})
			return nil
		},
		OnRetryAttemptStart: func() error {
			s.emit(&SessionEvent{Type: SessionSummarizationRetryAttemptStart, Source: source, Reason: source})
			return nil
		},
		OnRetryFinished: func(success bool, attempt int, finalError string) error {
			s.emit(&SessionEvent{Type: SessionSummarizationRetryFinished, Source: source})
			return nil
		},
	}
}
