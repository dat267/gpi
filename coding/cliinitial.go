package coding

import (
	"strings"

	"github.com/dat267/pier/ai"
)

// InitialPrompt is upstream buildInitialMessage's result (cli/initial-message.ts):
// the composed first user message, the images belonging to it, and the
// positional messages still queued behind it.
type InitialPrompt struct {
	Message string
	Images  []ai.ImageContent
	// Rest holds the positional messages after the one folded into Message —
	// upstream shifts the first message off parsed.messages and passes the rest
	// as initialMessages.
	Rest []string
}

// BuildInitialPrompt composes the session's first user message the way upstream
// buildInitialMessage does: piped stdin (headless runs) first, then the text of
// any @file arguments, then the first positional message, concatenated with no
// separator so a file and the question about it arrive as a single prompt.
// Everything after the first message stays queued as separate follow-ups.
func BuildInitialPrompt(messages []string, fileText string, fileImages []ai.ImageContent, stdinContent string) InitialPrompt {
	parts := make([]string, 0, 3)
	if stdinContent != "" {
		parts = append(parts, stdinContent)
	}
	if fileText != "" {
		parts = append(parts, fileText)
	}
	rest := messages
	if len(messages) > 0 {
		parts = append(parts, messages[0])
		rest = messages[1:]
	}
	message := ""
	if len(parts) > 0 {
		message = strings.Join(parts, "")
	}
	if len(rest) == 0 {
		rest = nil
	}
	return InitialPrompt{Message: message, Images: fileImages, Rest: rest}
}
