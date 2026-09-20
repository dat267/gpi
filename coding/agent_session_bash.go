package coding

import (
	"context"

	"github.com/dat267/gpi/ai"
)

// Port of the bash-execution surface of core/agent-session.ts: executeBash,
// recordBashResult, abortBash, isBashRunning, hasPendingBashMessages, and
// _flushPendingBashMessages.

// ExecuteBashOptions configure one bash execution.
type ExecuteBashOptions struct {
	// ExcludeFromContext keeps the result out of LLM context (!! prefix).
	ExcludeFromContext bool
	// ID labels the execution for bash_execution_update events.
	ID string
	// Operations overrides the execution backend (remote systems).
	Operations BashOperations
}

// exitCodeValue flattens an optional exit code.
func exitCodeValue(exitCode *int) int {
	if exitCode == nil {
		return 0
	}
	return *exitCode
}

// bashRunSet guards the running bash commands and deferred messages.

// ExecuteBash runs a bash command in the session's working directory, streams
// output chunks, and records the result in session history.
func (s *AgentSession) ExecuteBash(ctx context.Context, command string, onChunk func(chunk string), options *ExecuteBashOptions) (*BashResult, error) { //nolint:revive,
	if options == nil {
		options = &ExecuteBashOptions{}
	}
	cancel := s.trackBashRun()
	defer s.untrackBashRun(cancel)

	// Apply the configured command prefix (for example alias support).
	resolvedCommand := command
	if s.control != nil && s.control.Settings != nil {
		if prefix := s.control.Settings.GetShellCommandPrefix(); prefix != nil && *prefix != "" {
			resolvedCommand = *prefix + "\n" + command
		}
	}
	shellPath := ""
	if s.control != nil && s.control.Settings != nil {
		if path := s.control.Settings.GetShellPath(); path != nil {
			shellPath = *path
		}
	}

	operations := options.Operations
	if operations == nil {
		operations = CreateLocalBashOperations(shellPath, nil)
	}

	result, err := ExecuteBashWithOperations(ctx, resolvedCommand, s.Sessions.GetCwd(), operations, &BashExecutorOptions{
		OnChunk: func(delta string) {
			if onChunk != nil {
				onChunk(delta)
			}
			s.emit(&SessionEvent{Type: SessionBashExecutionUpdate, ID: options.ID, Delta: delta})
		},
		Signal: ctx.Done(),
	})
	if err != nil {
		return nil, err
	}
	s.RecordBashResult(command, result, options.ExcludeFromContext)
	return &result, nil
}

// trackBashRun registers a cancellable bash run.
func (s *AgentSession) trackBashRun() context.CancelFunc {
	s.bashMu.Lock()
	defer s.bashMu.Unlock()
	if s.bashCancels == nil {
		s.bashCancels = map[int]context.CancelFunc{}
	}
	s.bashNextID++
	id := s.bashNextID
	_, cancel := context.WithCancel(context.Background())
	s.bashCancels[id] = cancel
	return func() {
		s.bashMu.Lock()
		delete(s.bashCancels, id)
		s.bashMu.Unlock()
		cancel()
	}
}

func (s *AgentSession) untrackBashRun(cancel context.CancelFunc) {
	if cancel != nil {
		cancel()
	}
}

// RecordBashResult records a bash execution result in session history. While
// the agent is streaming the message is deferred to keep tool-call ordering.
func (s *AgentSession) RecordBashResult(command string, result BashResult, excludeFromContext bool) {
	message := CreateBashExecutionMessage(command, result.Output, exitCodeValue(result.ExitCode),
		result.Cancelled, result.Truncated, result.FullOutputPath, excludeFromContext, 0)

	if s.IsStreaming() {
		s.bashMu.Lock()
		s.pendingBashMessages = append(s.pendingBashMessages, message)
		s.bashMu.Unlock()
		return
	}
	s.appendBashMessage(message)
}

// appendBashMessage adds a bash message to agent state and session history.
func (s *AgentSession) appendBashMessage(message *ai.CustomMessage) {
	messages := append(s.Agent.State().Messages, ai.Message(message))
	s.Agent.SetMessages(messages)
	s.Sessions.AppendMessage(message)
}

// FlushPendingBashMessages appends bash messages queued during a run (upstream
// _flushPendingBashMessages, called after the agent turn completes).
func (s *AgentSession) FlushPendingBashMessages() {
	s.bashMu.Lock()
	pending := s.pendingBashMessages
	s.pendingBashMessages = nil
	s.bashMu.Unlock()
	for _, message := range pending {
		s.appendBashMessage(message)
	}
}

// HasPendingBashMessages reports whether deferred bash messages await a flush.
func (s *AgentSession) HasPendingBashMessages() bool {
	s.bashMu.Lock()
	defer s.bashMu.Unlock()
	return len(s.pendingBashMessages) > 0
}

// IsBashRunning reports whether any bash command is executing.
func (s *AgentSession) IsBashRunning() bool {
	s.bashMu.Lock()
	defer s.bashMu.Unlock()
	return len(s.bashCancels) > 0
}

// AbortBash cancels every running bash command.
func (s *AgentSession) AbortBash() {
	s.bashMu.Lock()
	cancels := make([]context.CancelFunc, 0, len(s.bashCancels))
	for _, cancel := range s.bashCancels {
		cancels = append(cancels, cancel)
	}
	s.bashMu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
}
