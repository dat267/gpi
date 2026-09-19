package coding

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/dat267/gpi/ai"
)

// Port of modes/print-mode.ts (the extension-free core).
//
// The extension binding, session runtime host, signal handlers, and raw-stdout
// backpressure machinery are out of scope by design; the Go runner keeps the
// observable contract: the JSON mode emits the session header followed by one
// JSON event per line, the text mode prints the final assistant text (or reports
// the failure), and both return the process exit code.

// PrintModeOptions configure RunPrintMode.
type PrintModeOptions struct {
	// Mode is "text" (final response only) or "json" (every event).
	Mode string
	// Messages are additional prompts sent after InitialMessage.
	Messages []string
	// InitialMessage is sent first.
	InitialMessage string
	// InitialImages attach to the initial message.
	InitialImages []ai.ImageContent
	// Stdout receives output (defaults to no writer).
	Stdout io.Writer
	// Stderr receives failure text.
	Stderr io.Writer
	// Context cancels the run.
	Context context.Context
}

// RunPrintMode sends the prompts and writes the result, returning the exit code.
func RunPrintMode(session *AgentSession, options PrintModeOptions) int {
	if session == nil {
		return 1
	}
	stdout := options.Stdout
	if stdout == nil {
		stdout = io.Discard
	}
	stderr := options.Stderr
	if stderr == nil {
		stderr = io.Discard
	}
	ctx := options.Context
	if ctx == nil {
		ctx = context.Background()
	}

	if options.Mode == CLIModeJSON {
		if header := session.Sessions.GetHeader(); header != nil {
			if encoded, err := ai.MarshalJSON(header); err == nil {
				fmt.Fprintf(stdout, "%s\n", encoded)
			}
		}
	}
	unsubscribe := session.Subscribe(func(event *SessionEvent) {
		if options.Mode != CLIModeJSON {
			return
		}
		value, err := ToJSONEvent(event)
		if err != nil {
			fmt.Fprintf(stderr, "%v\n", err)
			return
		}
		encoded, err := ai.MarshalJSON(value)
		if err != nil {
			fmt.Fprintf(stderr, "%v\n", err)
			return
		}
		fmt.Fprintf(stdout, "%s\n", encoded)
	})
	defer unsubscribe()

	if options.InitialMessage != "" {
		if err := session.Agent.PromptText(ctx, options.InitialMessage, options.InitialImages...); err != nil {
			fmt.Fprintf(stderr, "%v\n", err)
			return 1
		}
	}
	for _, message := range options.Messages {
		if err := session.PromptText(ctx, message); err != nil {
			fmt.Fprintf(stderr, "%v\n", err)
			return 1
		}
	}

	if options.Mode != CLIModeJSON {
		return writeFinalAssistantText(session, stdout, stderr)
	}
	return 0
}

// writeFinalAssistantText prints the last assistant message's text blocks, or
// reports the stop reason and returns the failure exit code (upstream's text
// mode).
func writeFinalAssistantText(session *AgentSession, stdout, stderr io.Writer) int {
	messages := session.Agent.State().Messages
	if len(messages) == 0 {
		return 0
	}
	assistant, ok := messages[len(messages)-1].(*ai.AssistantMessage)
	if !ok {
		return 0
	}
	if assistant.StopReason == ai.StopError || assistant.StopReason == ai.StopAborted {
		message := assistant.ErrorMessage
		if message != nil && *message != "" {
			fmt.Fprintf(stderr, "%s\n", *message)
		} else {
			fmt.Fprintf(stderr, "Request %s\n", assistant.StopReason)
		}
		return 1
	}
	for _, content := range assistant.Content {
		if text, ok := content.(ai.TextContent); ok {
			fmt.Fprintf(stdout, "%s\n", text.Text)
		}
	}
	return 0
}

// FormatJSONEventLine renders one event as a single JSON line (used by the JSON
// and RPC stdout protocols).
func FormatJSONEventLine(event *SessionEvent) (string, error) {
	value, err := ToJSONEvent(event)
	if err != nil {
		return "", err
	}
	encoded, err := ai.MarshalJSON(value)
	if err != nil {
		return "", err
	}
	return strings.TrimRight(string(encoded), "\n"), nil
}
