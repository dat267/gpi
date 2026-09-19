package ai

import "strings"

// ContentText extracts and joins text from message content.
// Port of utils/text.ts contentText.
func ContentText(content StringOrBlocks, separator string) string {
	if content.Blocks == nil {
		return content.Text
	}
	var parts []string
	for _, block := range content.Blocks {
		if t, ok := block.(TextContent); ok {
			parts = append(parts, t.Text)
		}
	}
	return strings.Join(parts, separator)
}

// GetSystemMessageText renders a system message as a complete prompt: its
// content followed by its sections (in declaration order).
func GetSystemMessageText(message *SystemMessage) string {
	parts := []string{ContentText(message.Content, "\n")}
	for _, name := range message.sectionNames() {
		if text, ok := message.Sections[name]; ok && text != nil {
			parts = append(parts, *text)
		}
	}
	var nonEmpty []string
	for _, part := range parts {
		if len(part) > 0 {
			nonEmpty = append(nonEmpty, part)
		}
	}
	return strings.Join(nonEmpty, "\n\n")
}

// RenderSystemMessageUpdate renders a later system message for APIs that
// accept system messages mid-conversation. Section changes are framed by
// name so the model can relate them to the leading prompt. This framing is
// request-time only and may change between versions.
func RenderSystemMessageUpdate(message *SystemMessage) string {
	var parts []string
	if text := ContentText(message.Content, "\n"); text != "" {
		parts = append(parts, text)
	}
	for _, name := range message.sectionNames() {
		value, ok := message.Sections[name]
		if !ok {
			continue
		}
		if value == nil {
			parts = append(parts, "Removed system prompt section \""+name+"\".")
		} else {
			parts = append(parts, "Updated system prompt section \""+name+"\":\n\n"+*value)
		}
	}
	return strings.Join(parts, "\n\n")
}
