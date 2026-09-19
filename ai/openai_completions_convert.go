package ai

import (
	"encoding/json"
	"strings"
)

// Port of api/openai-completions.ts conversion half: convertMessages,
// convertTools, parseChunkUsage, mapStopReason, BuildOpenAICompletionsParams.

// ConvertOpenAICompletionsMessages converts a transcript to Chat Completions
// wire messages (port of convertMessages).
func ConvertOpenAICompletionsMessages(
	model *Model,
	context TranscriptContext,
	compat ResolvedOpenAICompletionsCompat,
	options *ConvertCompletionsMessagesOptions,
) []OpenAIMessage {
	normalizedContext := ResolveTranscript(context, compat.SupportsMidConvoSystemMessages)
	var params []OpenAIMessage

	normalizeToolCallID := func(id string) string {
		// Pipe-separated ids from the Responses API: {call_id}|{item_id}.
		if strings.Contains(id, "|") {
			separatorIndex := strings.Index(id, "|")
			callID := sanitizeID(id[:separatorIndex])
			itemID := sanitizeID(id[separatorIndex+1:])
			combinedID := callID
			if len(itemID) > 0 {
				combinedID = callID + "_" + itemID
			}
			if JSLength(combinedID) <= 40 {
				return combinedID
			}
			hash := ShortHash(id)[:8]
			prefix := JSSlice(callID, 0, max(1, 40-JSLength(hash)-1))
			return prefix + "_" + hash
		}
		if model.Provider == "openai" {
			if JSLength(id) > 40 {
				return JSSlice(id, 0, 40)
			}
			return id
		}
		return id
	}

	transformedMessages := TransformMessages(normalizedContext.Messages, model, func(id string, _ *Model, _ *AssistantMessage) string {
		return normalizeToolCallID(id)
	})
	transcriptTools := ResolveTranscriptTools(normalizedContext.Messages,
		compat.SupportsMidConvoSystemMessages && compat.SupportsMidConvoToolAdditions)
	instructionRole := "system"
	if model.Reasoning && compat.SupportsDeveloperRole {
		instructionRole = "developer"
	}

	lastRole := ""
	for i := 0; i < len(transformedMessages); i++ {
		msg := transformedMessages[i]
		// Some providers don't allow user messages directly after tool
		// results; bridge with a synthetic assistant message.
		if compat.RequiresAssistantAfterToolResult && lastRole == RoleToolResult && RoleOf(msg) == RoleUser {
			params = append(params, OpenAIMessage{
				Role:    "assistant",
				Content: mustMarshalJSON("I have processed the tool results."),
			})
		}

		switch m := msg.(type) {
		case *SystemMessage:
			addedTools := []Tool{}
			if i > 0 && transcriptTools.AnchorsAdditions {
				addedTools = m.ToolsAdded
			}
			if len(addedTools) > 0 {
				tools, _ := ConvertOpenAICompletionsTools(addedTools, compat)
				// Kimi tool system message: role + tools, no content key.
				params = append(params, OpenAIMessage{Role: "system", Tools: tools})
			}
			text := ""
			if i == 0 {
				text = GetSystemMessageText(m)
			} else {
				text = RenderSystemMessageUpdate(m)
			}
			if len(text) > 0 {
				params = append(params, OpenAIMessage{
					Role:    instructionRole,
					Content: mustMarshalJSON(SanitizeSurrogates(text)),
				})
			}
		case *UserMessage:
			if m.Content.Blocks == nil {
				params = append(params, OpenAIMessage{
					Role:    "user",
					Content: mustMarshalJSON(SanitizeSurrogates(m.Content.Text)),
				})
			} else {
				var content []OpenAIContentPart
				for _, item := range m.Content.Blocks {
					switch b := item.(type) {
					case TextContent:
						content = append(content, OpenAIContentPart{Type: "text", Text: SanitizeSurrogates(b.Text)})
					case ImageContent:
						content = append(content, OpenAIContentPart{Type: "image_url",
							ImageURL: &OpenAIImageURL{URL: "data:" + b.MimeType + ";base64," + b.Data}})
					}
				}
				if len(content) == 0 {
					continue
				}
				params = append(params, OpenAIMessage{Role: "user", Content: mustMarshalJSON(content)})
			}
		case *AssistantMessage:
			// Some providers don't accept null content, use empty string instead.
			content := openaiNull
			if compat.RequiresAssistantAfterToolResult {
				content = mustMarshalJSON("")
			}
			assistantMsg := OpenAIMessage{Role: "assistant", Content: content}

			var assistantTextParts []OpenAIContentPart
			for _, block := range m.Content {
				if tc, ok := block.(TextContent); ok && !JSTrimIsEmpty(tc.Text) {
					assistantTextParts = append(assistantTextParts, OpenAIContentPart{Type: "text", Text: SanitizeSurrogates(tc.Text)})
				}
			}
			var assistantText strings.Builder
			for _, part := range assistantTextParts {
				assistantText.WriteString(part.Text)
			}

			var thinkingBlocks []ThinkingContent
			var toolCalls []ToolCall
			for _, block := range m.Content {
				switch b := block.(type) {
				case ThinkingContent:
					thinkingBlocks = append(thinkingBlocks, b)
				case ToolCall:
					toolCalls = append(toolCalls, b)
				}
			}
			var signedReasoningDetails []json.RawMessage
			for _, block := range thinkingBlocks {
				if details := parseOpenAIReasoningDetailsJSON(deref(block.ThinkingSignature)); details != nil {
					signedReasoningDetails = details
					break
				}
			}
			var legacyReasoningDetails []json.RawMessage
			for _, tc := range toolCalls {
				if detail := parseLegacyEncryptedReasoningDetailJSON(deref(tc.ThoughtSignature)); detail != nil {
					legacyReasoningDetails = append(legacyReasoningDetails, detail)
				}
			}
			preservedReasoningDetails := signedReasoningDetails
			if preservedReasoningDetails == nil && len(legacyReasoningDetails) > 0 {
				preservedReasoningDetails = legacyReasoningDetails
			}

			var nonEmptyThinkingBlocks []ThinkingContent
			for _, block := range thinkingBlocks {
				if !JSTrimIsEmpty(block.Thinking) {
					nonEmptyThinkingBlocks = append(nonEmptyThinkingBlocks, block)
				}
			}
			if len(nonEmptyThinkingBlocks) > 0 {
				if compat.RequiresThinkingAsText {
					// Convert thinking blocks to plain text (no tags to avoid
					// model mimicking them).
					var texts []string
					for _, block := range nonEmptyThinkingBlocks {
						texts = append(texts, SanitizeSurrogates(block.Thinking))
					}
					joined := strings.Join(texts, "\n\n")
					parts := append([]OpenAIContentPart{{Type: "text", Text: joined}}, assistantTextParts...)
					assistantMsg.Content = mustMarshalJSON(parts)
				} else {
					// Always send assistant content as a plain string.
					if assistantText.Len() > 0 {
						assistantMsg.Content = mustMarshalJSON(assistantText.String())
					}
					// reasoning_details is the structured alternative to a raw
					// reasoning field.
					if preservedReasoningDetails == nil {
						signature := deref(nonEmptyThinkingBlocks[0].ThinkingSignature)
						if model.Provider == "opencode-go" && signature == "reasoning" {
							signature = "reasoning_content"
						}
						if isOpenAICompletionsReasoningField(signature) {
							var joined []string
							for _, block := range nonEmptyThinkingBlocks {
								joined = append(joined, block.Thinking)
							}
							value := strings.Join(joined, "\n")
							switch signature {
							case "reasoning_content":
								assistantMsg.ReasoningContent = &value
							case "reasoning":
								assistantMsg.Reasoning = &value
							case "reasoning_text":
								assistantMsg.ReasoningText = &value
							}
						}
					}
				}
			} else if assistantText.Len() > 0 {
				assistantMsg.Content = mustMarshalJSON(assistantText.String())
			}

			if len(toolCalls) > 0 {
				for _, tc := range toolCalls {
					var customInputProperty string
					if options != nil && options.GrammarToolInputProperties != nil {
						customInputProperty = options.GrammarToolInputProperties[tc.Name]
					}
					if customInputProperty != "" {
						input, err := GetGrammarToolInput(tc.Name, tc.Arguments, customInputProperty)
						if err != nil {
							panic(err)
						}
						assistantMsg.ToolCalls = append(assistantMsg.ToolCalls, OpenAIToolCall{
							ID:     tc.ID,
							Type:   "custom",
							Custom: &OpenAICustom{Name: tc.Name, Input: SanitizeSurrogates(input)},
						})
						continue
					}
					assistantMsg.ToolCalls = append(assistantMsg.ToolCalls, OpenAIToolCall{
						ID:   tc.ID,
						Type: "function",
						Function: &OpenAIFunction{
							Name:      tc.Name,
							Arguments: string(mustMarshalJSON(json.RawMessage(tc.Arguments))),
						},
					})
				}
			}
			if preservedReasoningDetails != nil {
				assistantMsg.ReasoningDetails = preservedReasoningDetails
			}
			if compat.RequiresReasoningContentOnAssistantMessages && model.Reasoning && assistantMsg.ReasoningContent == nil {
				empty := ""
				assistantMsg.ReasoningContent = &empty
			}
			// Skip assistant messages with no content and no tool calls
			// (aborted responses that got no content).
			hasContent := false
			if len(assistantMsg.Content) > 0 && string(assistantMsg.Content) != "null" {
				var s string
				if err := jsonUnmarshalStrict(assistantMsg.Content, &s); err == nil {
					hasContent = len(s) > 0
				} else {
					hasContent = len(assistantMsg.Content) > 0
				}
			}
			if !hasContent && len(assistantMsg.ToolCalls) == 0 {
				continue
			}
			params = append(params, assistantMsg)
		case *ToolResultMessage:
			var imageBlocks []OpenAIContentPart
			j := i
			for ; j < len(transformedMessages); j++ {
				toolMsg, ok := transformedMessages[j].(*ToolResultMessage)
				if !ok {
					break
				}
				var texts []string
				for _, block := range toolMsg.Content {
					if tc, ok := block.(TextContent); ok {
						texts = append(texts, tc.Text)
					}
				}
				textResult := strings.Join(texts, "\n")
				hasImages := false
				for _, block := range toolMsg.Content {
					if _, ok := block.(ImageContent); ok {
						hasImages = true
					}
				}
				// Always send tool result with text (or placeholder).
				toolResultText := textResult
				if len(toolResultText) == 0 {
					if hasImages {
						toolResultText = "(see attached image)"
					} else {
						toolResultText = "(no tool output)"
					}
				}
				toolResultMsg := OpenAIMessage{
					Role:       "tool",
					Content:    mustMarshalJSON(SanitizeSurrogates(toolResultText)),
					ToolCallID: toolMsg.ToolCallID,
				}
				if compat.RequiresToolResultName && toolMsg.ToolName != "" {
					toolResultMsg.Name = toolMsg.ToolName
				}
				params = append(params, toolResultMsg)

				if hasImages {
					hasImageInput := false
					for _, input := range model.Input {
						if input == "image" {
							hasImageInput = true
						}
					}
					if hasImageInput {
						for _, block := range toolMsg.Content {
							if img, ok := block.(ImageContent); ok {
								imageBlocks = append(imageBlocks, OpenAIContentPart{Type: "image_url",
									ImageURL: &OpenAIImageURL{URL: "data:" + img.MimeType + ";base64," + img.Data}})
							}
						}
					}
				}
			}
			i = j - 1
			if len(imageBlocks) > 0 {
				if compat.RequiresAssistantAfterToolResult {
					params = append(params, OpenAIMessage{
						Role:    "assistant",
						Content: mustMarshalJSON("I have processed the tool results."),
					})
				}
				parts := append([]OpenAIContentPart{{Type: "text", Text: "Attached image(s) from tool result:"}}, imageBlocks...)
				params = append(params, OpenAIMessage{Role: "user", Content: mustMarshalJSON(parts)})
				lastRole = "user"
			} else {
				lastRole = RoleToolResult
			}
			continue
		}

		lastRole = RoleOf(msg)
	}
	return params
}

func sanitizeID(id string) string {
	re := func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' {
			return r
		}
		return '_'
	}
	return strings.Map(re, id)
}

func isOpenAICompletionsReasoningField(field string) bool {
	return field == "reasoning" || field == "reasoning_content" || field == "reasoning_text"
}

// ConvertCompletionsMessagesOptions carry per-call conversion inputs.
type ConvertCompletionsMessagesOptions struct {
	GrammarToolInputProperties map[string]string
}

// ConvertOpenAICompletionsTools converts tool declarations (port of
// convertTools).
func ConvertOpenAICompletionsTools(tools []Tool, compat ResolvedOpenAICompletionsCompat) ([]OpenAITool, error) {
	var out []OpenAITool
	for _, tool := range tools {
		grammar, err := ResolveGrammarConstrainedSampling(tool, compat.SupportsOpenAIGrammarTools)
		if err != nil {
			return nil, err
		}
		if grammar != nil {
			out = append(out, OpenAITool{
				Type: "custom",
				Custom: &OpenAICustomDef{
					Name:        tool.Name,
					Description: tool.Description,
					Format: &OpenAIGrammarFormat{
						Type: "grammar",
						Grammar: OpenAIGrammarDef{
							Syntax:     grammar.Format,
							Definition: grammar.Definition,
						},
					},
				},
			})
			continue
		}
		supportsStrictMode := compat.SupportsStrictMode
		strict, unset, err := ResolveJSONSchemaStrictSampling(tool, supportsStrictMode)
		if err != nil {
			return nil, err
		}
		var strictField *bool
		if supportsStrictMode && !unset {
			strictField = boolPtr(strict)
		} else if supportsStrictMode {
			strictField = boolPtr(false)
		}
		out = append(out, OpenAITool{
			Type: "function",
			Function: &OpenAIFunctionDef{
				Name:        tool.Name,
				Description: tool.Description,
				Parameters:  GetJSONSchemaToolParameters(tool, strict && !unset),
				Strict:      strictField,
			},
		})
	}
	return out, nil
}

// ParseChunkUsage maps a ChatCompletionChunk usage object to pi's Usage
// (port of parseChunkUsage).
func ParseChunkUsage(raw []byte, model *Model) Usage {
	var u struct {
		PromptTokens         *int64 `json:"prompt_tokens"`
		CompletionTokens     *int64 `json:"completion_tokens"`
		CachedTokens         *int64 `json:"cached_tokens"`
		PromptCacheHitTokens *int64 `json:"prompt_cache_hit_tokens"`
		PromptTokensDetails  *struct {
			CachedTokens     *int64 `json:"cached_tokens"`
			CacheWriteTokens *int64 `json:"cache_write_tokens"`
		} `json:"prompt_tokens_details"`
		CompletionTokensDetails *struct {
			ReasoningTokens *int64 `json:"reasoning_tokens"`
		} `json:"completion_tokens_details"`
	}
	_ = jsonUnmarshalStrict(raw, &u)

	promptTokens := derefI64(u.PromptTokens)
	cacheReadTokens := int64(0)
	if u.PromptTokensDetails != nil && u.PromptTokensDetails.CachedTokens != nil {
		cacheReadTokens = *u.PromptTokensDetails.CachedTokens
	} else if u.PromptCacheHitTokens != nil {
		cacheReadTokens = *u.PromptCacheHitTokens
	} else if u.CachedTokens != nil {
		cacheReadTokens = *u.CachedTokens
	}
	cacheWriteTokens := int64(0)
	if u.PromptTokensDetails != nil && u.PromptTokensDetails.CacheWriteTokens != nil {
		cacheWriteTokens = *u.PromptTokensDetails.CacheWriteTokens
	}
	input := max(0, promptTokens-cacheReadTokens-cacheWriteTokens)
	outputTokens := derefI64(u.CompletionTokens)
	usage := Usage{
		Input:       input,
		Output:      outputTokens,
		CacheRead:   cacheReadTokens,
		CacheWrite:  cacheWriteTokens,
		TotalTokens: input + outputTokens + cacheReadTokens + cacheWriteTokens,
		Cost:        UsageCost{},
	}
	if u.CompletionTokensDetails != nil && u.CompletionTokensDetails.ReasoningTokens != nil {
		reasoning := *u.CompletionTokensDetails.ReasoningTokens
		usage.Reasoning = &reasoning
	}
	CalculateCost(model, &usage)
	return usage
}

func derefI64(v *int64) int64 {
	if v == nil {
		return 0
	}
	return *v
}

// MapOpenAICompletionsStopReason maps a finish_reason (port of mapStopReason).
func MapOpenAICompletionsStopReason(reason string) (stopReason StopReason, errorMessage string, err error) {
	switch reason {
	case "stop", "end":
		return StopStop, "", nil
	case "length":
		return StopLength, "", nil
	case "function_call", "tool_calls":
		return StopToolUse, "", nil
	case "content_filter":
		return StopError, "Provider finish_reason: content_filter", nil
	case "network_error":
		return StopError, "Provider finish_reason: network_error", nil
	default:
		return StopError, "Provider finish_reason: " + reason, nil
	}
}

// ResolveThinkingTokenBudgetField resolves the budget field name
// (port of resolveThinkingTokenBudgetField).
func ResolveThinkingTokenBudgetField(compat ResolvedOpenAICompletionsCompat) ThinkingTokenBudgetField {
	if compat.ThinkingTokenBudgetField != "" {
		return compat.ThinkingTokenBudgetField
	}
	if compat.SupportsThinkingTokenBudget {
		return ThinkingTokenBudgetVLLM
	}
	return ""
}

// BuildOpenAICompletionsParams builds the streaming request body
// (port of buildParams).
func BuildOpenAICompletionsParams(model *Model, context TranscriptContext, options *OpenAICompletionsOptions, compat *ResolvedOpenAICompletionsCompat, cacheRetention CacheRetention, grammarToolInputProperties map[string]string) (*OpenAICompletionsParams, error) {
	if compat == nil {
		resolved := GetOpenAICompletionsCompat(model)
		compat = &resolved
	}
	if cacheRetention == "" {
		cacheRetention = ResolveCacheRetention(options.CacheRetention, options.Env)
	}
	if grammarToolInputProperties == nil {
		grammarToolInputProperties = CreateGrammarToolInputProperties(GetDeclaredTools(context.Messages), compat.SupportsOpenAIGrammarTools)
	}
	transcriptTools := ResolveTranscriptTools(context.Messages,
		compat.SupportsMidConvoSystemMessages && compat.SupportsMidConvoToolAdditions)
	messages := ConvertOpenAICompletionsMessages(model, context, *compat, &ConvertCompletionsMessagesOptions{
		GrammarToolInputProperties: grammarToolInputProperties,
	})
	cacheControl := getOpenAICompatCacheControl(*compat, cacheRetention)

	params := &OpenAICompletionsParams{
		Model:    model.ID,
		Messages: messages,
		Stream:   true,
	}
	if (strings.Contains(model.BaseURL, "api.openai.com") && cacheRetention != CacheRetentionNone) ||
		(cacheRetention == CacheRetentionLong && compat.SupportsLongCacheRetention) {
		if options.SessionID != "" {
			clamped := ClampOpenAIPromptCacheKey(options.SessionID)
			params.PromptCacheKey = &clamped
		} else {
			empty := ""
			params.PromptCacheKey = &empty
		}
	}
	if cacheRetention == CacheRetentionLong && compat.SupportsLongCacheRetention {
		retention := "24h"
		params.PromptCacheRetention = &retention
	}
	if compat.SupportsUsageInStreaming {
		params.StreamOptions = &OpenAIStreamOptions{IncludeUsage: true}
	}
	if compat.SupportsStore {
		params.Store = boolPtr(false)
	}
	if options.MaxTokens != nil {
		if compat.MaxTokensField == "max_tokens" {
			params.MaxTokens = int64Ptr(int64(*options.MaxTokens))
		} else {
			params.MaxCompletionTokens = int64Ptr(int64(*options.MaxTokens))
		}
	}
	if options.Temperature != nil {
		params.Temperature = options.Temperature
	}
	if len(transcriptTools.RequestTools) > 0 {
		tools, err := ConvertOpenAICompletionsTools(transcriptTools.RequestTools, *compat)
		if err != nil {
			return nil, err
		}
		params.Tools = tools
		if compat.ZaiToolStream {
			params.ToolStream = boolPtr(true)
		}
	} else if hasOpenAIToolHistory(context.Messages) {
		// Anthropic (via LiteLLM/proxy) requires the tools param when the
		// conversation has tool_calls/tool_results.
		params.Tools = []OpenAITool{}
	}
	if cacheControl != nil {
		applyOpenAIAnthropicCacheControl(messages, params.Tools, cacheControl)
	}
	if options.ToolChoice != nil && len(options.ToolChoice) > 0 {
		params.ToolChoice = options.ToolChoice
	}
	if compat.VllmPriority != nil {
		params.Priority = compat.VllmPriority
	}

	// Thinking format dispatch.
	if compat.ThinkingFormat == "zai" && model.Reasoning {
		if options.ReasoningEffort != "" {
			clear := false
			params.Thinking = &OpenAIThinkingParam{Type: "enabled", ClearThinking: &clear}
		} else {
			params.Thinking = &OpenAIThinkingParam{Type: "disabled"}
		}
		if options.ReasoningEffort != "" && compat.SupportsReasoningEffort {
			effort := resolveLevelMapValue(model, options.ReasoningEffort, true)
			if effort != nil {
				params.ReasoningEffort = effort
			}
		}
	} else if compat.ThinkingFormat == "qwen" && model.Reasoning {
		params.EnableThinking = boolPtr(options.ReasoningEffort != "")
		if options.ReasoningEffort != "" && compat.SupportsReasoningEffort {
			if effort := resolveLevelMapValue(model, options.ReasoningEffort, true); effort != nil {
				params.ReasoningEffort = effort
			}
		}
	} else if compat.ThinkingFormat == "qwen-chat-template" && model.Reasoning {
		params.ChatTemplateKwargs = map[string]any{
			"enable_thinking":   options.ReasoningEffort != "",
			"preserve_thinking": true,
		}
	} else if compat.ThinkingFormat == "chat-template" && model.Reasoning {
		kwargs := buildChatTemplateValues(model, options, compat.ChatTemplateKwargs, resolveClampedThinkingBudget(model, options, params))
		if kwargs != nil {
			params.ChatTemplateKwargs = kwargs
		}
	} else if compat.ThinkingFormat == "baseten" && model.Reasoning {
		args := buildChatTemplateValues(model, options, compat.ChatTemplateArgs, resolveClampedThinkingBudget(model, options, params))
		if args != nil {
			params.ChatTemplateArgs = args
		}
		if compat.SupportsReasoningEffort {
			requested := options.ReasoningEffort
			var effort *string
			if requested != "" {
				effort = resolveLevelMapValue(model, requested, true)
			} else {
				effort = resolveLevelMapValue(model, ThinkOff, true)
			}
			if effort != nil {
				params.ReasoningEffort = effort
			}
		}
	} else if compat.ThinkingFormat == "deepseek" && model.Reasoning {
		if options.ReasoningEffort != "" {
			params.Thinking = &OpenAIThinkingParam{Type: "enabled"}
		} else if !thinkingLevelMapOffIsNull(model) {
			params.Thinking = &OpenAIThinkingParam{Type: "disabled"}
		}
		if options.ReasoningEffort != "" && compat.SupportsReasoningEffort {
			if effort := resolveLevelMapValue(model, options.ReasoningEffort, true); effort != nil {
				params.ReasoningEffort = effort
			}
		}
	} else if compat.ThinkingFormat == "openrouter" && model.Reasoning {
		if options.ReasoningEffort != "" {
			params.ReasoningObj = &OpenAIReasoningParam{Effort: resolveLevelMapValue(model, options.ReasoningEffort, true)}
		} else if !thinkingLevelMapOffIsNull(model) {
			if mapped := resolveLevelMapValue(model, ThinkOff, true); mapped != nil {
				params.ReasoningObj = &OpenAIReasoningParam{Effort: mapped}
			} else {
				none := "none"
				params.ReasoningObj = &OpenAIReasoningParam{Effort: &none}
			}
		}
	} else if compat.ThinkingFormat == "ant-ling" && model.Reasoning && options.ReasoningEffort != "" {
		effort := resolveLevelMapValue(model, options.ReasoningEffort, false)
		if effort != nil {
			params.ReasoningObj = &OpenAIReasoningParam{Effort: effort}
		}
	} else if compat.ThinkingFormat == "together" && model.Reasoning {
		params.ReasoningObj = &OpenAIReasoningParam{Enabled: boolPtr(options.ReasoningEffort != "")}
		if options.ReasoningEffort != "" && compat.SupportsReasoningEffort {
			params.ReasoningEffort = resolveLevelMapValue(model, options.ReasoningEffort, true)
		}
	} else if compat.ThinkingFormat == "string-thinking" && model.Reasoning {
		if options.ReasoningEffort != "" {
			params.Thinking = &OpenAIThinkingParam{Type: resolveLevelMapValueOrNone(model, options.ReasoningEffort)}
		} else if !thinkingLevelMapOffIsNull(model) {
			params.Thinking = &OpenAIThinkingParam{Type: resolveLevelMapValueOrNone(model, ThinkOff)}
		}
	} else if options.ReasoningEffort != "" && model.Reasoning && compat.SupportsReasoningEffort {
		params.ReasoningEffort = resolveLevelMapValue(model, options.ReasoningEffort, true)
	} else if options.ReasoningEffort == "" && model.Reasoning && compat.SupportsReasoningEffort {
		if mapped, ok := model.ThinkingLevelMap[ThinkOff]; ok && mapped != nil {
			params.ReasoningEffort = mapped
		}
	}

	// Cap reasoning with a top-level budget field, independent of the
	// thinking format.
	if field := ResolveThinkingTokenBudgetField(*compat); field != "" {
		if budget := resolveClampedThinkingBudget(model, options, params); budget != nil {
			params.ThinkingTokenBudget = int64Ptr(int64(*budget))
			params.ThinkingTokenBudgetField = field
		}
	}

	// OpenRouter provider routing preferences.
	if model.Compat != nil && model.Compat.OpenAICompletions != nil && model.Compat.OpenAICompletions.OpenRouterRouting != nil {
		params.ProviderRouting = mustMarshalJSON(model.Compat.OpenAICompletions.OpenRouterRouting)
	}
	// Vercel AI Gateway routing preferences.
	if model.Compat != nil && model.Compat.OpenAICompletions != nil && model.Compat.OpenAICompletions.VercelGatewayRouting != nil {
		routing := model.Compat.OpenAICompletions.VercelGatewayRouting
		if len(routing.Only) > 0 || len(routing.Order) > 0 {
			gateway := map[string][]string{}
			if len(routing.Only) > 0 {
				gateway["only"] = routing.Only
			}
			if len(routing.Order) > 0 {
				gateway["order"] = routing.Order
			}
			params.ProviderOptions = mustMarshalJSON(map[string]any{"gateway": gateway})
		}
	}

	// Sampling params merged last so custom keys override the named fields.
	if len(options.SamplingParams) > 0 {
		params.SamplingExtras = options.SamplingParams
	}

	return params, nil
}

func int64Ptr(v int64) *int64 { return &v }

// resolveLevelMapValue maps a thinking level through thinkingLevelMap.
// fallbackToRequested: when nothing is mapped, return the requested level
// itself (upstream `?? options.reasoningEffort`).
func resolveLevelMapValue(model *Model, level ThinkingLevel, fallbackToRequested bool) *string {
	if mapped, ok := model.ThinkingLevelMap[level]; ok {
		if mapped != nil {
			return mapped
		}
		return nil // explicit null: unsupported
	}
	if fallbackToRequested && level != "" {
		return ptrString(level)
	}
	return nil
}

func resolveLevelMapValueOrNone(model *Model, level ThinkingLevel) string {
	if mapped, ok := model.ThinkingLevelMap[level]; ok && mapped != nil {
		return *mapped
	}
	return "none"
}

// thinkingLevelMapOffIsNull reports whether thinkingLevelMap.off === null
// (meaning disabled-thinking must NOT be sent).
func thinkingLevelMapOffIsNull(model *Model) bool {
	mapped, ok := model.ThinkingLevelMap[ThinkOff]
	return ok && mapped == nil
}

func hasOpenAIToolHistory(messages []Message) bool {
	for _, msg := range messages {
		switch m := msg.(type) {
		case *ToolResultMessage:
			return true
		case *AssistantMessage:
			for _, block := range m.Content {
				if _, ok := block.(ToolCall); ok {
					return true
				}
			}
		}
	}
	return false
}

func getOpenAICompatCacheControl(compat ResolvedOpenAICompletionsCompat, cacheRetention CacheRetention) *AnthropicCacheControl {
	if compat.CacheControlFormat != "anthropic" || cacheRetention == CacheRetentionNone {
		return nil
	}
	cc := &AnthropicCacheControl{Type: "ephemeral"}
	if cacheRetention == CacheRetentionLong && compat.SupportsLongCacheRetention {
		ttl := "1h"
		cc.TTL = &ttl
	}
	return cc
}

func applyOpenAIAnthropicCacheControl(messages []OpenAIMessage, tools []OpenAITool, cacheControl *AnthropicCacheControl) {
	// System prompt (first system/developer message).
	for i := range messages {
		if messages[i].Role == "system" || messages[i].Role == "developer" {
			addCacheControlToInstruction(&messages[i], cacheControl)
			break
		}
	}
	// Last tool.
	if len(tools) > 0 {
		tools[len(tools)-1].CacheControl = cacheControl
	}
	// Last user/assistant/tool message.
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "user" || messages[i].Role == "assistant" || messages[i].Role == "tool" {
			if addCacheControlToOpenAIMessage(&messages[i], cacheControl) {
				return
			}
		}
	}
}

func addCacheControlToInstruction(message *OpenAIMessage, cacheControl *AnthropicCacheControl) bool {
	return addCacheControlToOpenAIMessage(message, cacheControl)
}

func addCacheControlToOpenAIMessage(message *OpenAIMessage, cacheControl *AnthropicCacheControl) bool {
	// String content: convert to a parts array with the marker.
	var text string
	if err := jsonUnmarshalStrict(message.Content, &text); err == nil {
		if len(text) == 0 {
			return false
		}
		message.Content = mustMarshalJSON([]OpenAIContentPart{{
			Type: "text", Text: text, CacheControl: cacheControl,
		}})
		return true
	}
	// Parts array: mark the last text part.
	var parts []OpenAIContentPart
	if err := jsonUnmarshalStrict(message.Content, &parts); err != nil {
		return false
	}
	for i := len(parts) - 1; i >= 0; i-- {
		if parts[i].Type == "text" {
			parts[i].CacheControl = cacheControl
			message.Content = mustMarshalJSON(parts)
			return true
		}
	}
	return false
}

// parseOpenAIReasoningDetailsJSON parses a reasoning_details payload from a
// thinking signature.
func parseOpenAIReasoningDetailsJSON(signature string) []json.RawMessage {
	if signature == "" {
		return nil
	}
	var parsed json.RawMessage
	if err := jsonUnmarshalStrict(json.RawMessage(signature), &parsed); err != nil {
		return nil
	}
	var list []json.RawMessage
	if err := jsonUnmarshalStrict(parsed, &list); err != nil || len(list) == 0 {
		return nil
	}
	for _, detail := range list {
		if !isOpenAIReasoningDetail(detail) {
			return nil
		}
	}
	return list
}

// parseLegacyEncryptedReasoningDetailJSON parses a legacy encrypted reasoning
// detail from a tool-call thought signature.
func parseLegacyEncryptedReasoningDetailJSON(signature string) json.RawMessage {
	if signature == "" {
		return nil
	}
	var detail map[string]json.RawMessage
	if err := jsonUnmarshalStrict(json.RawMessage(signature), &detail); err != nil {
		return nil
	}
	if !isOpenAIReasoningDetail(json.RawMessage(signature)) {
		return nil
	}
	if typeRaw, ok := detail["type"]; ok && string(typeRaw) == `"reasoning.encrypted"` {
		if idRaw, ok := detail["id"]; ok {
			var id string
			if jsonUnmarshalStrict(idRaw, &id) == nil && len(id) > 0 {
				dataRaw, ok := detail["data"]
				if ok {
					var data string
					if jsonUnmarshalStrict(dataRaw, &data) == nil && len(data) > 0 {
						return mustMarshalJSON(detail)
					}
				}
			}
		}
	}
	return nil
}

// isOpenAIReasoningDetail validates one reasoning detail object.
func isOpenAIReasoningDetail(raw json.RawMessage) bool {
	var candidate map[string]json.RawMessage
	if err := jsonUnmarshalStrict(raw, &candidate); err != nil {
		return false
	}
	hasValidCommon := func() bool {
		if idRaw, ok := candidate["id"]; ok && string(idRaw) != "null" {
			var s string
			if jsonUnmarshalStrict(idRaw, &s) != nil {
				return false
			}
		}
		if formatRaw, ok := candidate["format"]; ok && string(formatRaw) != "null" {
			var s string
			if jsonUnmarshalStrict(formatRaw, &s) != nil {
				return false
			}
		}
		if indexRaw, ok := candidate["index"]; ok && string(indexRaw) != "null" {
			var f float64
			if jsonUnmarshalStrict(indexRaw, &f) != nil {
				return false
			}
		}
		return true
	}
	typeRaw, hasType := candidate["type"]
	if !hasType {
		return false
	}
	var typ string
	if jsonUnmarshalStrict(typeRaw, &typ) != nil {
		return false
	}
	switch typ {
	case "reasoning.summary":
		if summaryRaw, ok := candidate["summary"]; ok {
			var s string
			return jsonUnmarshalStrict(summaryRaw, &s) == nil && hasValidCommon()
		}
		return false
	case "reasoning.encrypted":
		if dataRaw, ok := candidate["data"]; ok {
			var s string
			return jsonUnmarshalStrict(dataRaw, &s) == nil && hasValidCommon()
		}
		return false
	case "reasoning.text":
		if textRaw, ok := candidate["text"]; ok {
			var s string
			if jsonUnmarshalStrict(textRaw, &s) != nil {
				return false
			}
			if sigRaw, ok := candidate["signature"]; ok && string(sigRaw) != "null" {
				var sig string
				if jsonUnmarshalStrict(sigRaw, &sig) != nil {
					return false
				}
			}
			return hasValidCommon()
		}
		return false
	default:
		return false
	}
}

// resolveClampedThinkingBudget resolves the thinking budget under the
// response ceiling (port of resolveClampedThinkingBudget).
func resolveClampedThinkingBudget(model *Model, options *OpenAICompletionsOptions, params *OpenAICompletionsParams) *int {
	if options.ReasoningEffort == "" || !model.Reasoning {
		return nil
	}
	ceiling := int64(0)
	if params.MaxTokens != nil {
		ceiling = *params.MaxTokens
	} else if params.MaxCompletionTokens != nil {
		ceiling = *params.MaxCompletionTokens
	} else {
		ceiling = model.MaxTokens
	}
	budget := ClampThinkingBudgetToAnswerRoom(
		ThinkingBudgetForLevel(options.ReasoningEffort, options.ThinkingBudgets), int(ceiling))
	if budget > 0 {
		return &budget
	}
	return nil
}

// buildChatTemplateValues resolves $var placeholders in chat template values
// (port of buildChatTemplateValues).
func buildChatTemplateValues(model *Model, options *OpenAICompletionsOptions, values map[string]json.RawMessage, thinkingBudget *int) map[string]any {
	resolved := map[string]any{}
	for key, raw := range values {
		var value struct {
			Var         string `json:"$var"`
			OmitWhenOff bool   `json:"omitWhenOff"`
		}
		isObject := jsonUnmarshalStrict(raw, &value) == nil && value.Var != ""
		if !isObject {
			var literal any
			if err := jsonUnmarshalStrict(raw, &literal); err == nil {
				resolved[key] = literal
			}
			continue
		}
		reasoningEffort := options.ReasoningEffort
		if reasoningEffort == "" && value.OmitWhenOff {
			continue
		}
		switch value.Var {
		case "thinking.enabled":
			resolved[key] = reasoningEffort != ""
		case "thinking.budget":
			// undefined budget omits the key (upstream returns undefined).
			if thinkingBudget != nil {
				resolved[key] = *thinkingBudget
			}
		default:
			var effort string
			if reasoningEffort != "" {
				if mapped := resolveLevelMapValue(model, reasoningEffort, true); mapped != nil {
					effort = *mapped
				}
			} else if mapped := resolveLevelMapValue(model, ThinkOff, true); mapped != nil {
				effort = *mapped
			}
			if effort != "" {
				resolved[key] = effort
			}
		}
	}
	if len(resolved) == 0 {
		return nil
	}
	return resolved
}
