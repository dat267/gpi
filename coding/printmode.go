package coding

// Print mode (single-shot): send prompts, output the result, exit — port of
// src/modes/print-mode.ts with json-event.ts's wire shaping. Upstream uses it
// for `pi -p "prompt"` (text) and `pi --mode json "prompt"` (JSON event
// stream). Extensions are out of scope (D41), so the extension binding and
// session-rebind plumbing upstream carries does not apply here.

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"

	"github.com/dat267/pier/agent"
	"github.com/dat267/pier/ai"
)

// PrintModeOptions configures RunPrintMode.
type PrintModeOptions struct {
	// Mode is the output mode: CLIModeText for the final response only,
	// CLIModeJSON for the JSON event stream.
	Mode CLIMode
	// InitialMessage is the first prompt (with @file content already expanded).
	InitialMessage string
	// Messages are additional prompts sent after the initial one.
	Messages []string
}

// printStdout serializes stdout writes: session events fan out from the agent
// goroutine while prompts run on the caller's, so the JSON stream needs one
// writer. Lines flush immediately: the stream is line-delimited and consumers
// read it live.
type printStdout struct {
	mu sync.Mutex
	w  *bufio.Writer
}

func (p *printStdout) writeLine(line string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, _ = p.w.WriteString(line)
	_ = p.w.WriteByte('\n')
	_ = p.w.Flush()
}

func (p *printStdout) flush() {
	p.mu.Lock()
	defer p.mu.Unlock()
	_ = p.w.Flush()
}

// RunPrintMode runs print mode and returns the process exit code: 0 on
// success, 1 when a prompt fails or the final assistant message ended in
// error/abort (upstream reports those on stderr and exits nonzero).
func RunPrintMode(session *AgentSession, sessions *SessionManager, options PrintModeOptions) int {
	mode := options.Mode
	out := &printStdout{w: bufio.NewWriter(os.Stdout)}

	// SIGTERM (143) and SIGHUP (129) mirror upstream's signal exits. The
	// process dies with the agent goroutine; upstream's killTrackedDetached-
	// Children has no Go counterpart because the bash tool runs children in
	// their own process groups and reaps them itself.
	sigChannel := make(chan os.Signal, 1)
	signal.Notify(sigChannel, syscall.SIGTERM, syscall.SIGHUP)
	go func() {
		sig, ok := <-sigChannel
		if !ok {
			return
		}
		out.flush()
		if sig == syscall.SIGHUP {
			os.Exit(129)
		}
		os.Exit(143)
	}()

	if mode == CLIModeJSON {
		if header := sessions.GetHeader(); header != nil {
			// Marshal the header the way the session file does so the wire
			// shape carries its "type":"session" discriminator (upstream's
			// getHeader() serializes the tagged object directly).
			if encoded, err := MarshalFileEntry(FileEntry{Header: header}); err == nil {
				out.writeLine(strings.TrimSuffix(encoded, "\n"))
			}
		}
		unsubscribe := session.Subscribe(func(event *SessionEvent) {
			encoded, err := MarshalJSONSessionEvent(event)
			if err != nil {
				fmt.Fprintln(os.Stderr, "json event error: "+err.Error())
				return
			}
			out.writeLine(string(encoded))
		})
		defer unsubscribe()
	}

	prompt := func(text string) error {
		if err := session.PromptText(context.Background(), text); err != nil {
			return err
		}
		return session.WaitForIdle(context.Background())
	}
	runErr := func() error {
		if options.InitialMessage != "" {
			if err := prompt(options.InitialMessage); err != nil {
				return err
			}
		}
		for _, message := range options.Messages {
			if err := prompt(message); err != nil {
				return err
			}
		}
		return nil
	}()
	if runErr != nil {
		fmt.Fprintln(os.Stderr, runErr)
		out.flush()
		return 1
	}

	if mode == CLIModeText {
		messages := session.Agent.State().Messages
		if len(messages) > 0 {
			if assistant, ok := messages[len(messages)-1].(*ai.AssistantMessage); ok {
				if assistant.StopReason == ai.StopError || assistant.StopReason == ai.StopAborted {
					message := ""
					if assistant.ErrorMessage != nil {
						message = *assistant.ErrorMessage
					}
					if message == "" {
						message = "Request " + string(assistant.StopReason)
					}
					fmt.Fprintln(os.Stderr, message)
					out.flush()
					return 1
				}
				for _, block := range assistant.Content {
					if text, ok := block.(ai.TextContent); ok {
						out.writeLine(text.Text)
					}
				}
			}
		}
	}

	out.flush()
	return 0
}

// jsonSessionEvent is the JSON stdout wire shape for one session event. It is
// the Go counterpart of upstream's AgentSessionEvent with camelCase field
// names; the passthrough agent payload is flattened rather than nested under
// an "agent" key, matching upstream's flat event objects.
type jsonSessionEvent struct {
	Type string `json:"type"`

	// message lifecycle (message_start/update/end, turn_end)
	Message               json.RawMessage   `json:"message,omitempty"`
	Usage                 json.RawMessage   `json:"usage,omitempty"`
	AssistantMessageEvent json.RawMessage   `json:"assistantMessageEvent,omitempty"`
	Messages              []json.RawMessage `json:"messages,omitempty"`
	ToolResults           []json.RawMessage `json:"toolResults,omitempty"`

	// tool execution lifecycle
	ToolCallID    string          `json:"toolCallId,omitempty"`
	ToolName      string          `json:"toolName,omitempty"`
	Args          json.RawMessage `json:"args,omitempty"`
	PartialResult json.RawMessage `json:"partialResult,omitempty"`
	Result        json.RawMessage `json:"result,omitempty"`
	IsError       bool            `json:"isError,omitempty"`

	// session-level payloads
	WillRetry    bool     `json:"willRetry,omitempty"`
	Steering     []string `json:"steering,omitempty"`
	FollowUp     []string `json:"followUp,omitempty"`
	Reason       string   `json:"reason,omitempty"`
	ErrorMessage string   `json:"errorMessage,omitempty"`
	Aborted      bool     `json:"aborted,omitempty"`
	Level        string   `json:"level,omitempty"`
	Attempt      int      `json:"attempt,omitempty"`
	MaxAttempts  int      `json:"maxAttempts,omitempty"`
	DelayMS      int64    `json:"delayMs,omitempty"`
	Success      bool     `json:"success,omitempty"`
	ID           string   `json:"id,omitempty"`
	Delta        string   `json:"delta,omitempty"`
	Source       string   `json:"source,omitempty"`
}

// MarshalJSONSessionEvent shapes one SessionEvent for the JSON stdout stream
// (upstream toJsonEvent): cumulative assistant snapshots are stripped from
// message_update — message_start provides the initial message, deltas build
// it, and message_end carries the final authoritative message — while usage,
// tool-call ids and tool names stay because their size is constant.
func MarshalJSONSessionEvent(event *SessionEvent) ([]byte, error) {
	wire := jsonSessionEvent{
		Type:        event.Type,
		WillRetry:   event.WillRetry,
		Steering:    event.Steering,
		FollowUp:    event.FollowUp,
		Aborted:     event.Aborted,
		Level:       string(event.Level),
		Attempt:     event.Attempt,
		MaxAttempts: event.MaxAttempts,
		DelayMS:     event.DelayMS,
		Success:     event.Success,
		ID:          event.ID,
		Delta:       event.Delta,
		Source:      event.Source,
	}
	if event.Reason != "" {
		wire.Reason = string(event.Reason)
	}
	wire.ErrorMessage = event.ErrorMessage
	if event.Agent == nil {
		return json.Marshal(wire)
	}
	a := event.Agent
	wire.ToolCallID = a.ToolCallID
	wire.ToolName = a.ToolName
	wire.Args = a.Args
	wire.IsError = a.IsError
	var err error
	if a.Message != nil {
		if wire.Message, err = ai.MarshalMessage(a.Message); err != nil {
			return nil, err
		}
		if assistant, ok := a.Message.(*ai.AssistantMessage); ok {
			if wire.Usage, err = json.Marshal(assistant.Usage); err != nil {
				return nil, err
			}
		}
	}
	if len(a.Messages) > 0 {
		if wire.Messages, err = ai.MarshalMessages(a.Messages); err != nil {
			return nil, err
		}
	}
	if len(a.ToolResults) > 0 {
		if wire.ToolResults, err = ai.MarshalMessages(a.ToolResults); err != nil {
			return nil, err
		}
	}
	if len(a.PartialResult.Content) > 0 || len(a.PartialResult.Details) > 0 {
		if wire.PartialResult, err = json.Marshal(a.PartialResult); err != nil {
			return nil, err
		}
	}
	if len(a.Result.Content) > 0 || len(a.Result.Details) > 0 {
		if wire.Result, err = json.Marshal(a.Result); err != nil {
			return nil, err
		}
	}
	if a.Type == agent.MessageUpdate && a.AssistantMessageEvent != nil {
		if wire.AssistantMessageEvent, err = marshalJSONAssistantMessageEvent(a.AssistantMessageEvent); err != nil {
			return nil, err
		}
	}
	return json.Marshal(wire)
}

// jsonAssistantMessageEvent is AssistantMessageEvent plus the toolcall_start
// identity fields upstream adds; Partial is dropped before marshaling.
type jsonAssistantMessageEvent struct {
	ai.AssistantMessageEvent
	ID       string `json:"id,omitempty"`
	ToolName string `json:"toolName,omitempty"`
}

func marshalJSONAssistantMessageEvent(event *ai.AssistantMessageEvent) ([]byte, error) {
	shaped := jsonAssistantMessageEvent{AssistantMessageEvent: *event}
	shaped.Partial = nil
	if event.Type == ai.EventToolcallStart {
		toolCall, err := partialToolCall(event)
		if err != nil {
			return nil, err
		}
		shaped.ID = toolCall.ID
		shaped.ToolName = toolCall.Name
	}
	return json.Marshal(shaped)
}

// partialToolCall resolves the tool call a toolcall_start event points at via
// its content index into the cumulative partial message (upstream reads the
// same shape and throws when the block is not a tool call).
func partialToolCall(event *ai.AssistantMessageEvent) (*ai.ToolCall, error) {
	if event.Partial == nil || event.ContentIndex >= len(event.Partial.Content) {
		return nil, fmt.Errorf("toolcall_start content at index %d is missing from the partial message", event.ContentIndex)
	}
	block, ok := event.Partial.Content[event.ContentIndex].(ai.ToolCall)
	if !ok {
		return nil, fmt.Errorf("toolcall_start content at index %d is not a tool call", event.ContentIndex)
	}
	return &block, nil
}
