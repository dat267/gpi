package ai

import "time"

// Port of api/transform-messages.ts.

const (
	nonVisionUserImagePlaceholder = "(image omitted: model does not support images)"
	nonVisionToolImagePlaceholder = "(tool image omitted: model does not support images)"
)

// replaceImagesWithPlaceholder collapses image blocks to text placeholders,
// deduplicating consecutive placeholders (upstream semantics: a placeholder
// is emitted once per run of images).
func replaceImagesWithPlaceholder(content ContentList, placeholder string) ContentList {
	var result ContentList
	previousWasPlaceholder := false
	for _, block := range content {
		if img, ok := block.(ImageContent); ok {
			_ = img
			if !previousWasPlaceholder {
				result = append(result, TextContent{Text: placeholder})
			}
			previousWasPlaceholder = true
			continue
		}
		text, _ := block.(TextContent)
		result = append(result, text)
		previousWasPlaceholder = text.Text == placeholder
	}
	return result
}

func downgradeUnsupportedImages(messages []Message, model *Model) []Message {
	hasImageInput := false
	for _, input := range model.Input {
		if input == "image" {
			hasImageInput = true
		}
	}
	if hasImageInput {
		return messages
	}
	out := make([]Message, 0, len(messages))
	for _, msg := range messages {
		switch m := msg.(type) {
		case *UserMessage:
			if m.Content.Blocks != nil {
				cloned := *m
				cloned.Content = StringOrBlocks{Blocks: replaceImagesWithPlaceholder(m.Content.Blocks, nonVisionUserImagePlaceholder)}
				out = append(out, &cloned)
				continue
			}
		case *ToolResultMessage:
			cloned := *m
			placeholderBlocks := replaceImagesWithPlaceholder(userToContentList(m.Content), nonVisionToolImagePlaceholder)
			userContent := make(UserContentList, 0, len(placeholderBlocks))
			for _, b := range placeholderBlocks {
				if uc, ok := b.(UserContent); ok {
					userContent = append(userContent, uc)
				}
			}
			cloned.Content = userContent
			out = append(out, &cloned)
			continue
		}
		out = append(out, msg)
	}
	return out
}

// ToolCallIDNormalizer normalizes tool call IDs for cross-provider
// compatibility (upstream's normalizeToolCallId argument).
type ToolCallIDNormalizer func(id string, model *Model, source *AssistantMessage) string

// TransformMessages normalizes a transcript for a model: downgrades images
// for non-vision models, adjusts thinking blocks for cross-model replay,
// normalizes tool call IDs, and synthesizes tool results for orphaned calls.
func TransformMessages(messages []Message, model *Model, normalizeToolCallID ToolCallIDNormalizer) []Message {
	toolCallIDMap := map[string]string{}

	// First pass: per-message transformation.
	imageAware := downgradeUnsupportedImages(messages, model)
	transformed := make([]Message, 0, len(imageAware))
	for _, msg := range imageAware {
		switch m := msg.(type) {
		case *SystemMessage, *UserMessage:
			transformed = append(transformed, msg)
		case *ToolResultMessage:
			cloned := *m
			if normalized, ok := toolCallIDMap[m.ToolCallID]; ok && normalized != m.ToolCallID {
				cloned.ToolCallID = normalized
			}
			transformed = append(transformed, &cloned)
		case *AssistantMessage:
			cloned := *m
			isSameModel := m.Provider == model.Provider && m.API == model.API && m.Model == model.ID
			var transformedContent ContentList
			for _, block := range m.Content {
				switch b := block.(type) {
				case ThinkingContent:
					if b.Redacted {
						if isSameModel {
							transformedContent = append(transformedContent, b)
						}
						continue
					}
					if isSameModel && b.ThinkingSignature != nil {
						transformedContent = append(transformedContent, b)
						continue
					}
					if b.Thinking == "" || JSTrimIsEmpty(b.Thinking) {
						continue
					}
					if isSameModel {
						transformedContent = append(transformedContent, b)
					} else {
						transformedContent = append(transformedContent, TextContent{Text: b.Thinking})
					}
				case TextContent:
					if isSameModel {
						transformedContent = append(transformedContent, b)
					} else {
						transformedContent = append(transformedContent, TextContent{Text: b.Text})
					}
				case ToolCall:
					normalized := b
					if !isSameModel && b.ThoughtSignature != nil {
						normalized.ThoughtSignature = nil
					}
					if !isSameModel && normalizeToolCallID != nil {
						normalizedID := normalizeToolCallID(b.ID, model, m)
						if normalizedID != b.ID {
							toolCallIDMap[b.ID] = normalizedID
							normalized.ID = normalizedID
						}
					}
					transformedContent = append(transformedContent, normalized)
				default:
					transformedContent = append(transformedContent, block)
				}
			}
			cloned.Content = transformedContent
			transformed = append(transformed, &cloned)
		default:
			transformed = append(transformed, msg)
		}
	}

	// Second pass: insert synthetic empty tool results for orphaned tool
	// calls. System messages are transparent to tool-call accounting: one
	// landing between a tool call and its results is held back and emitted
	// after the results.
	var result []Message
	var pendingToolCalls []ToolCall
	existingToolResultIDs := map[string]bool{}
	var heldSystemMessages []Message
	closePendingToolCalls := func() {
		if len(pendingToolCalls) > 0 {
			for _, tc := range pendingToolCalls {
				if !existingToolResultIDs[tc.ID] {
					text := "No result provided"
					result = append(result, &ToolResultMessage{
						ToolCallID: tc.ID,
						ToolName:   tc.Name,
						Content:    UserContentList{TextContent{Text: text}},
						IsError:    true,
						Timestamp:  time.Now().UnixMilli(),
					})
				}
			}
			pendingToolCalls = nil
			existingToolResultIDs = map[string]bool{}
		}
		result = append(result, heldSystemMessages...)
		heldSystemMessages = nil
	}

	for _, msg := range transformed {
		switch m := msg.(type) {
		case *AssistantMessage:
			closePendingToolCalls()
			// Skip errored/aborted assistant messages entirely: incomplete
			// turns that shouldn't be replayed.
			if m.StopReason == StopError || m.StopReason == StopAborted {
				continue
			}
			var toolCalls []ToolCall
			for _, block := range m.Content {
				if tc, ok := block.(ToolCall); ok {
					toolCalls = append(toolCalls, tc)
				}
			}
			if len(toolCalls) > 0 {
				pendingToolCalls = toolCalls
				existingToolResultIDs = map[string]bool{}
			}
			result = append(result, msg)
		case *ToolResultMessage:
			existingToolResultIDs[m.ToolCallID] = true
			result = append(result, msg)
		case *SystemMessage:
			if len(pendingToolCalls) > 0 {
				heldSystemMessages = append(heldSystemMessages, msg)
			} else {
				result = append(result, msg)
			}
		case *UserMessage:
			closePendingToolCalls()
			result = append(result, msg)
		default:
			result = append(result, msg)
		}
	}
	closePendingToolCalls()
	return result
}
