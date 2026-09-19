package ai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Port of the streaming half of api/openai-responses.ts and
// openai-responses-shared.ts: the params builder, the SSE event loop, and
// streamSimple.

// BuildOpenAIResponsesParams builds the streaming request body
// (port of buildParams).
func BuildOpenAIResponsesParams(model *Model, context TranscriptContext, options *OpenAIResponsesOptions, compat *ResolvedOpenAIResponsesCompat, grammarToolInputProperties map[string]string) (*OpenAIResponsesParams, error) {
	return BuildOpenAIResponsesParamsWithProviders(model, context, options, compat, grammarToolInputProperties, nil)
}

// BuildOpenAIResponsesParamsWithProviders is BuildOpenAIResponsesParams with an
// explicit tool-call provider set.
func BuildOpenAIResponsesParamsWithProviders(
	model *Model,
	context TranscriptContext,
	options *OpenAIResponsesOptions,
	compat *ResolvedOpenAIResponsesCompat,
	grammarToolInputProperties map[string]string,
	toolCallProviders map[string]bool,
) (*OpenAIResponsesParams, error) {
	if options == nil {
		options = &OpenAIResponsesOptions{}
	}
	if compat == nil {
		resolved := GetOpenAIResponsesCompat(model)
		compat = &resolved
	}
	if grammarToolInputProperties == nil {
		grammarToolInputProperties = CreateGrammarToolInputProperties(
			GetDeclaredTools(context.Messages), compat.SupportsOpenAIGrammarTools)
	}
	transcriptTools := ResolveTranscriptTools(context.Messages,
		compat.SupportsAdditionalTools || compat.SupportsToolSearch)
	toolOptions := &ConvertResponsesToolsOptions{
		SupportsStrictMode:         compat.SupportsStrictMode,
		SupportsOpenAIGrammarTools: compat.SupportsOpenAIGrammarTools,
	}
	input, err := ConvertResponsesMessages(model, context, &ConvertResponsesMessagesOptions{
		ToolCallProviders:              toolCallProviders,
		GrammarToolInputProperties:     grammarToolInputProperties,
		SupportsMidConvoSystemMessages: compat.SupportsMidConvoSystemMessages,
		SupportsAdditionalTools:        compat.SupportsAdditionalTools,
		SupportsToolSearch:             compat.SupportsToolSearch,
		ToolOptions:                    toolOptions,
	})
	if err != nil {
		return nil, err
	}

	cacheRetention := ResolveCacheRetention(options.CacheRetention, options.Env)
	params := &OpenAIResponsesParams{
		Model:  model.ID,
		Input:  input,
		Stream: true,
		Store:  boolPtr(false),
	}
	if cacheRetention != CacheRetentionNone && options.SessionID != "" {
		clamped := ClampOpenAIPromptCacheKey(options.SessionID)
		params.PromptCacheKey = &clamped
	}
	if retention := GetPromptCacheRetention(*compat, cacheRetention); retention != "" {
		params.PromptCacheRetention = &retention
	}
	params.PromptCacheOptions = GetPromptCacheOptions(*compat, cacheRetention)

	if options.MaxTokens != nil && compat.SupportsMaxOutputTokens {
		tokens := int64(max(*options.MaxTokens, openAIResponsesMinOutputTokens))
		params.MaxOutputTokens = &tokens
	}
	if options.Temperature != nil {
		params.Temperature = options.Temperature
	}
	if options.ServiceTier != "" {
		tier := options.ServiceTier
		params.ServiceTier = &tier
	}
	if len(transcriptTools.RequestTools) > 0 {
		converted, err := ConvertResponsesTools(transcriptTools.RequestTools, toolOptions)
		if err != nil {
			return nil, err
		}
		params.Tools = converted
	}
	if len(options.ToolChoice) > 0 {
		params.ToolChoice = options.ToolChoice
	}

	if model.Reasoning {
		if options.ReasoningEffort != "" || options.ReasoningSummary != "" {
			effort := "medium"
			if options.ReasoningEffort != "" {
				if mapped, ok := model.ThinkingLevelMap[options.ReasoningEffort]; ok && mapped != nil {
					effort = *mapped
				} else {
					effort = options.ReasoningEffort
				}
			}
			summary := options.ReasoningSummary
			if summary == "" {
				summary = "auto"
			}
			params.Reasoning = &OpenAIResponsesReasoning{Effort: effort, Summary: summary}
			params.Include = []string{"reasoning.encrypted_content"}
		} else if model.Provider != "github-copilot" {
			offMapped, hasOff := model.ThinkingLevelMap[ThinkOff]
			if !hasOff || offMapped != nil {
				effort := "none"
				if hasOff && offMapped != nil {
					effort = *offMapped
				}
				params.Reasoning = &OpenAIResponsesReasoning{Effort: effort}
			}
		}
		if model.Provider == "xai" {
			params.Include = []string{"reasoning.encrypted_content"}
		}
	}

	if len(options.SamplingParams) > 0 {
		params.SamplingExtras = options.SamplingParams
	}
	return params, nil
}

// MarshalJSON emits upstream's key order plus sampling extras merged last.
func (p *OpenAIResponsesParams) MarshalJSON() ([]byte, error) {
	root := map[string]json.RawMessage{}
	put := func(key string, value any) error {
		enc, err := MarshalJSON(value)
		if err != nil {
			return err
		}
		root[key] = enc
		return nil
	}
	if err := put("model", p.Model); err != nil {
		return nil, err
	}
	root["input"] = p.Input
	if err := put("stream", p.Stream); err != nil {
		return nil, err
	}
	if p.PromptCacheKey != nil {
		if err := put("prompt_cache_key", *p.PromptCacheKey); err != nil {
			return nil, err
		}
	}
	if p.PromptCacheRetention != nil {
		if err := put("prompt_cache_retention", *p.PromptCacheRetention); err != nil {
			return nil, err
		}
	}
	if p.PromptCacheOptions != nil {
		if err := put("prompt_cache_options", p.PromptCacheOptions); err != nil {
			return nil, err
		}
	}
	if p.Store != nil {
		if err := put("store", *p.Store); err != nil {
			return nil, err
		}
	}
	if p.MaxOutputTokens != nil {
		if err := put("max_output_tokens", *p.MaxOutputTokens); err != nil {
			return nil, err
		}
	}
	if p.Temperature != nil {
		if err := put("temperature", *p.Temperature); err != nil {
			return nil, err
		}
	}
	if p.ServiceTier != nil {
		if err := put("service_tier", *p.ServiceTier); err != nil {
			return nil, err
		}
	}
	if p.Tools != nil {
		if err := put("tools", p.Tools); err != nil {
			return nil, err
		}
	}
	if len(p.ToolChoice) > 0 {
		root["tool_choice"] = p.ToolChoice
	}
	if p.Reasoning != nil {
		if err := put("reasoning", p.Reasoning); err != nil {
			return nil, err
		}
	}
	if p.Include != nil {
		if err := put("include", p.Include); err != nil {
			return nil, err
		}
	}
	for key, value := range p.SamplingExtras {
		root[key] = value
	}
	keys := make([]string, 0, len(root))
	for key := range root {
		keys = append(keys, key)
	}
	sortStrings(keys)
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, key := range keys {
		if i > 0 {
			buf.WriteByte(',')
		}
		keyEnc, _ := MarshalJSON(key)
		buf.Write(keyEnc)
		buf.WriteByte(':')
		buf.Write(root[key])
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// responsesStreamEvent is one Responses SSE event (subset pi consumes).
type responsesStreamEvent struct {
	Type        string          `json:"type"`
	Delta       string          `json:"delta,omitempty"`
	Arguments   string          `json:"arguments,omitempty"`
	Input       string          `json:"input,omitempty"`
	OutputIndex int             `json:"output_index"`
	Item        json.RawMessage `json:"item,omitempty"`
	Response    json.RawMessage `json:"response,omitempty"`
	Code        string          `json:"code,omitempty"`
	Message     string          `json:"message,omitempty"`
}

// responsesItem is the parsed subset of a Responses output item.
type responsesItem struct {
	Type      string  `json:"type"`
	ID        string  `json:"id"`
	CallID    string  `json:"call_id"`
	Name      string  `json:"name"`
	Input     string  `json:"input,omitempty"`
	Arguments string  `json:"arguments,omitempty"`
	Namespace *string `json:"namespace,omitempty"`
	Phase     string  `json:"phase,omitempty"`
	Summary   []struct {
		Text string `json:"text"`
	} `json:"summary,omitempty"`
	Content []struct {
		Type    string `json:"type"`
		Text    string `json:"text,omitempty"`
		Refusal string `json:"refusal,omitempty"`
	} `json:"content,omitempty"`
	EncryptedContent *string `json:"encrypted_content,omitempty"`
}

// responsesTerminalResponse is the parsed terminal response payload.
type responsesTerminalResponse struct {
	ID          string  `json:"id"`
	Status      string  `json:"status"`
	ServiceTier *string `json:"service_tier,omitempty"`
	Usage       *struct {
		InputTokens        *int64 `json:"input_tokens"`
		OutputTokens       *int64 `json:"output_tokens"`
		TotalTokens        *int64 `json:"total_tokens"`
		InputTokensDetails *struct {
			CachedTokens     *int64 `json:"cached_tokens"`
			CacheWriteTokens *int64 `json:"cache_write_tokens"`
		} `json:"input_tokens_details"`
		OutputTokensDetails *struct {
			ReasoningTokens *int64 `json:"reasoning_tokens"`
		} `json:"output_tokens_details"`
	} `json:"usage"`
	IncompleteDetails *struct {
		Reason string `json:"reason"`
	} `json:"incomplete_details"`
	Error *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
	Output []json.RawMessage `json:"output"`
}

// responsesOutputSlot tracks one in-flight output block.
type responsesOutputSlot struct {
	kind         string // "thinking" | "text" | "toolCall"
	contentIndex int
	thinking     *ThinkingContent
	text         *TextContent
	toolCall     *responsesStreamingToolCall
}

// responsesStreamingToolCall is the live tool-call accumulator.
type responsesStreamingToolCall struct {
	call        ToolCall
	partialJSON *string
	customInput *struct {
		property   string
		jsonBuffer GrammarToolInputJSONBuffer
	}
}

// ProcessResponsesStream consumes the Responses SSE stream
// (port of processResponsesStream).
func ProcessResponsesStream(
	ctx context.Context,
	body io.Reader,
	output *AssistantMessage,
	stream *AssistantMessageEventStream,
	model *Model,
	options *OpenAIResponsesOptions,
	grammarToolInputProperties map[string]string,
) error {
	sawTerminalResponseEvent := false
	outputSlots := map[int]*responsesOutputSlot{}
	reasoningBlocksByID := map[string]*ThinkingContent{}

	applyMessagePhaseStopReason := func(item *responsesItem) {
		if item.Type == "message" && item.Phase == "final_answer" {
			output.StopReason = StopStop
		}
	}
	getSlot := func(outputIndex int, kind string) *responsesOutputSlot {
		slot := outputSlots[outputIndex]
		if slot == nil || slot.kind != kind {
			return nil
		}
		return slot
	}
	createSlot := func(outputIndex int, item *responsesItem) *responsesOutputSlot {
		switch item.Type {
		case "reasoning":
			block := ThinkingContent{Thinking: ""}
			output.Content = append(output.Content, block)
			slot := &responsesOutputSlot{kind: "thinking", thinking: &block, contentIndex: len(output.Content) - 1}
			outputSlots[outputIndex] = slot
			stream.Push(AssistantMessageEvent{Type: EventThinkingStart, ContentIndex: slot.contentIndex, Partial: output})
			return slot
		case "message":
			applyMessagePhaseStopReason(item)
			block := TextContent{Text: ""}
			output.Content = append(output.Content, block)
			slot := &responsesOutputSlot{kind: "text", text: &block, contentIndex: len(output.Content) - 1}
			outputSlots[outputIndex] = slot
			stream.Push(AssistantMessageEvent{Type: EventTextStart, ContentIndex: slot.contentIndex, Partial: output})
			return slot
		case "function_call":
			partial := item.Arguments
			call := ToolCall{ID: item.CallID + "|" + item.ID, Name: item.Name, Arguments: json.RawMessage("{}"), Namespace: item.Namespace}
			output.Content = append(output.Content, call)
			slot := &responsesOutputSlot{
				kind: "toolCall", contentIndex: len(output.Content) - 1,
				toolCall: &responsesStreamingToolCall{call: call, partialJSON: &partial},
			}
			outputSlots[outputIndex] = slot
			stream.Push(AssistantMessageEvent{Type: EventToolcallStart, ContentIndex: slot.contentIndex, Partial: output})
			return slot
		case "custom_tool_call":
			inputProperty := "input"
			if grammarToolInputProperties != nil {
				if prop, ok := grammarToolInputProperties[item.Name]; ok {
					inputProperty = prop
				}
			}
			call := ToolCall{
				ID: item.CallID + "|" + item.ID, Name: item.Name,
				Arguments: mustMarshalJSON(map[string]string{inputProperty: item.Input}),
				Namespace: item.Namespace,
			}
			output.Content = append(output.Content, call)
			slot := &responsesOutputSlot{
				kind: "toolCall", contentIndex: len(output.Content) - 1,
				toolCall: &responsesStreamingToolCall{
					call: call,
					customInput: &struct {
						property   string
						jsonBuffer GrammarToolInputJSONBuffer
					}{property: inputProperty},
				},
			}
			outputSlots[outputIndex] = slot
			stream.Push(AssistantMessageEvent{Type: EventToolcallStart, ContentIndex: slot.contentIndex, Partial: output})
			return slot
		default:
			return nil
		}
	}
	getOrCreateSlot := func(outputIndex int, item *responsesItem) *responsesOutputSlot {
		if slot, ok := outputSlots[outputIndex]; ok {
			return slot
		}
		return createSlot(outputIndex, item)
	}
	updateContent := func(slot *responsesOutputSlot) {
		switch slot.kind {
		case "thinking":
			output.Content[slot.contentIndex] = *slot.thinking
		case "text":
			output.Content[slot.contentIndex] = *slot.text
		case "toolCall":
			output.Content[slot.contentIndex] = slot.toolCall.call
		}
	}
	pushToolCallDelta := func(slot *responsesOutputSlot, delta string) {
		if slot == nil || delta == "" {
			return
		}
		stream.Push(AssistantMessageEvent{Type: EventToolcallDelta, ContentIndex: slot.contentIndex, Delta: delta, Partial: output})
	}
	customToolCallInput := func(slot *responsesOutputSlot) string {
		if slot.toolCall.customInput == nil {
			return ""
		}
		var args map[string]json.RawMessage
		if jsonUnmarshalStrict(slot.toolCall.call.Arguments, &args) == nil {
			if raw, ok := args[slot.toolCall.customInput.property]; ok {
				var s string
				if jsonUnmarshalStrict(raw, &s) == nil {
					return s
				}
			}
		}
		return ""
	}
	appendCustomToolCallInput := func(slot *responsesOutputSlot, nextInput string, close bool) string {
		if slot.toolCall.customInput == nil {
			return ""
		}
		delta, ok, err := AppendGrammarToolInputJSONDelta(&slot.toolCall.customInput.jsonBuffer, slot.toolCall.customInput.property, nextInput, close)
		if err != nil {
			panic(err)
		}
		if !ok {
			return ""
		}
		slot.toolCall.call.Arguments = mustMarshalJSON(map[string]string{slot.toolCall.customInput.property: nextInput})
		updateContent(slot)
		return delta
	}

	// Azure OpenAI can omit reasoning.encrypted_content from
	// response.output_item.done and provide it only in the terminal
	// response; backfill for stateless multi-turn replay (#6409).
	backfillReasoningSignatures := func(responseOutput []json.RawMessage) {
		for _, raw := range responseOutput {
			var item responsesItem
			if jsonUnmarshalStrict(raw, &item) != nil || item.Type != "reasoning" || item.EncryptedContent == nil {
				continue
			}
			block := reasoningBlocksByID[item.ID]
			if block == nil || block.ThinkingSignature == nil || *block.ThinkingSignature == "" {
				continue
			}
			if strings.Contains(*block.ThinkingSignature, "encrypted_content") {
				continue
			}
			var stored map[string]json.RawMessage
			if jsonUnmarshalStrict(json.RawMessage(*block.ThinkingSignature), &stored) != nil {
				continue
			}
			enc, _ := MarshalJSON(*item.EncryptedContent)
			stored["encrypted_content"] = enc
			merged, _ := MarshalJSON(stored)
			signature := string(merged)
			block.ThinkingSignature = &signature
		}
	}
	finalizeResponse := func(response *responsesTerminalResponse) {
		sawTerminalResponseEvent = true
		backfillReasoningSignatures(response.Output)
		if response.ID != "" {
			id := response.ID
			output.ResponseID = &id
		}
		if response.Usage != nil {
			cachedTokens := int64(0)
			cacheWriteTokens := int64(0)
			if response.Usage.InputTokensDetails != nil {
				if response.Usage.InputTokensDetails.CachedTokens != nil {
					cachedTokens = *response.Usage.InputTokensDetails.CachedTokens
				}
				if response.Usage.InputTokensDetails.CacheWriteTokens != nil {
					cacheWriteTokens = *response.Usage.InputTokensDetails.CacheWriteTokens
				}
			}
			inputTokens := int64(0)
			if response.Usage.InputTokens != nil {
				inputTokens = *response.Usage.InputTokens
			}
			outputTokens := int64(0)
			if response.Usage.OutputTokens != nil {
				outputTokens = *response.Usage.OutputTokens
			}
			totalTokens := int64(0)
			if response.Usage.TotalTokens != nil {
				totalTokens = *response.Usage.TotalTokens
			}
			output.Usage = Usage{
				// OpenAI includes cached and cache-write tokens in
				// input_tokens, so subtract both.
				Input:       max(0, inputTokens-cachedTokens-cacheWriteTokens),
				Output:      outputTokens,
				CacheRead:   cachedTokens,
				CacheWrite:  cacheWriteTokens,
				TotalTokens: totalTokens,
				Cost:        UsageCost{},
			}
			if response.Usage.OutputTokensDetails != nil && response.Usage.OutputTokensDetails.ReasoningTokens != nil {
				reasoning := *response.Usage.OutputTokensDetails.ReasoningTokens
				output.Usage.Reasoning = &reasoning
			}
		}
		CalculateCost(model, &output.Usage)
		if options != nil && options.ServiceTier != "" {
			serviceTier := options.ServiceTier
			if response.ServiceTier != nil {
				serviceTier = *response.ServiceTier
			}
			applyServiceTierPricing(&output.Usage, serviceTier, model)
		}
		status := response.Status
		incompleteReason := ""
		if response.IncompleteDetails != nil {
			incompleteReason = response.IncompleteDetails.Reason
		}
		rawStop := status
		if incompleteReason != "" {
			rawStop = status + "." + incompleteReason
		}
		output.RawStopReason = &rawStop
		mapped, errMsg, err := mapResponsesStopReason(status, incompleteReason)
		if err != nil {
			panic(err)
		}
		output.StopReason = mapped
		if errMsg == "" {
			output.ErrorMessage = nil
		} else {
			output.ErrorMessage = &errMsg
		}
		hasToolCall := false
		for _, block := range output.Content {
			if _, ok := block.(ToolCall); ok {
				hasToolCall = true
			}
		}
		if hasToolCall && output.StopReason == StopStop {
			output.StopReason = StopToolUse
		}
	}

	iterErr := iterateResponsesEvents(ctx, body, func(event *responsesStreamEvent) {
		switch event.Type {
		case "response.created":
			var response responsesTerminalResponse
			if jsonUnmarshalStrict(event.Response, &response) == nil && response.ID != "" {
				id := response.ID
				output.ResponseID = &id
			}
		case "response.output_item.added":
			var item responsesItem
			if jsonUnmarshalStrict(event.Item, &item) == nil {
				createSlot(event.OutputIndex, &item)
			}
		case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
			slot := getSlot(event.OutputIndex, "thinking")
			if slot == nil {
				return
			}
			slot.thinking.Thinking += event.Delta
			updateContent(slot)
			stream.Push(AssistantMessageEvent{Type: EventThinkingDelta, ContentIndex: slot.contentIndex, Delta: event.Delta, Partial: output})
		case "response.reasoning_summary_part.done":
			slot := getSlot(event.OutputIndex, "thinking")
			if slot == nil {
				return
			}
			slot.thinking.Thinking += "\n\n"
			updateContent(slot)
			stream.Push(AssistantMessageEvent{Type: EventThinkingDelta, ContentIndex: slot.contentIndex, Delta: "\n\n", Partial: output})
		case "response.output_text.delta", "response.refusal.delta":
			slot := getSlot(event.OutputIndex, "text")
			if slot == nil {
				return
			}
			slot.text.Text += event.Delta
			updateContent(slot)
			stream.Push(AssistantMessageEvent{Type: EventTextDelta, ContentIndex: slot.contentIndex, Delta: event.Delta, Partial: output})
		case "response.function_call_arguments.delta":
			slot := getSlot(event.OutputIndex, "toolCall")
			if slot == nil || slot.toolCall.partialJSON == nil {
				return
			}
			*slot.toolCall.partialJSON += event.Delta
			var parsed map[string]any
			parseStreamingJSONInto(*slot.toolCall.partialJSON, &parsed)
			slot.toolCall.call.Arguments = mustMarshalJSON(parsed)
			updateContent(slot)
			pushToolCallDelta(slot, event.Delta)
		case "response.function_call_arguments.done":
			slot := getSlot(event.OutputIndex, "toolCall")
			if slot == nil || slot.toolCall.partialJSON == nil {
				return
			}
			previous := *slot.toolCall.partialJSON
			*slot.toolCall.partialJSON = event.Arguments
			var parsed map[string]any
			parseStreamingJSONInto(event.Arguments, &parsed)
			slot.toolCall.call.Arguments = mustMarshalJSON(parsed)
			updateContent(slot)
			if strings.HasPrefix(event.Arguments, previous) {
				delta := event.Arguments[len(previous):]
				pushToolCallDelta(slot, delta)
			}
		case "response.custom_tool_call_input.delta":
			slot := getSlot(event.OutputIndex, "toolCall")
			if slot == nil || slot.toolCall.customInput == nil {
				return
			}
			pushToolCallDelta(slot, appendCustomToolCallInput(slot, customToolCallInput(slot)+event.Delta, false))
		case "response.custom_tool_call_input.done":
			slot := getSlot(event.OutputIndex, "toolCall")
			if slot == nil || slot.toolCall.customInput == nil {
				return
			}
			pushToolCallDelta(slot, appendCustomToolCallInput(slot, event.Input, true))
		case "response.output_item.done":
			var item responsesItem
			if jsonUnmarshalStrict(event.Item, &item) != nil {
				return
			}
			applyMessagePhaseStopReason(&item)
			slot := getOrCreateSlot(event.OutputIndex, &item)

			switch {
			case item.Type == "reasoning" && slot != nil && slot.kind == "thinking":
				var summaryTexts, contentTexts []string
				for _, s := range item.Summary {
					summaryTexts = append(summaryTexts, s.Text)
				}
				for _, c := range item.Content {
					contentTexts = append(contentTexts, c.Text)
				}
				if joined := strings.Join(summaryTexts, "\n\n"); joined != "" {
					slot.thinking.Thinking = joined
				} else if joined := strings.Join(contentTexts, "\n\n"); joined != "" {
					slot.thinking.Thinking = joined
				}
				signature := string(event.Item)
				slot.thinking.ThinkingSignature = &signature
				reasoningBlocksByID[item.ID] = slot.thinking
				updateContent(slot)
				stream.Push(AssistantMessageEvent{Type: EventThinkingEnd, ContentIndex: slot.contentIndex, Content: slot.thinking.Thinking, Partial: output})
				delete(outputSlots, event.OutputIndex)
			case item.Type == "message" && slot != nil && slot.kind == "text":
				var builder strings.Builder
				for _, c := range item.Content {
					if c.Type == "output_text" {
						builder.WriteString(c.Text)
					} else {
						builder.WriteString(c.Refusal)
					}
				}
				slot.text.Text = builder.String()
				signature := EncodeTextSignatureV1(item.ID, item.Phase)
				slot.text.TextSignature = &signature
				updateContent(slot)
				stream.Push(AssistantMessageEvent{Type: EventTextEnd, ContentIndex: slot.contentIndex, Content: slot.text.Text, Partial: output})
				delete(outputSlots, event.OutputIndex)
			case item.Type == "function_call" && slot != nil && slot.kind == "toolCall" && slot.toolCall.partialJSON != nil:
				arguments := item.Arguments
				if arguments == "" {
					arguments = *slot.toolCall.partialJSON
				}
				if arguments == "" {
					arguments = "{}"
				}
				var parsed map[string]any
				parseStreamingJSONInto(arguments, &parsed)
				slot.toolCall.call.Arguments = mustMarshalJSON(parsed)
				if item.Namespace != nil {
					slot.toolCall.call.Namespace = item.Namespace
				}
				slot.toolCall.partialJSON = nil
				updateContent(slot)
				call := slot.toolCall.call
				stream.Push(AssistantMessageEvent{Type: EventToolcallEnd, ContentIndex: slot.contentIndex, ToolCall: &call, Partial: output})
				delete(outputSlots, event.OutputIndex)
			case item.Type == "custom_tool_call" && slot != nil && slot.kind == "toolCall" && slot.toolCall.customInput != nil:
				nextInput := item.Input
				if nextInput == "" {
					nextInput = customToolCallInput(slot)
				}
				pushToolCallDelta(slot, appendCustomToolCallInput(slot, nextInput, true))
				if item.Namespace != nil {
					slot.toolCall.call.Namespace = item.Namespace
				}
				slot.toolCall.customInput = nil
				updateContent(slot)
				call := slot.toolCall.call
				stream.Push(AssistantMessageEvent{Type: EventToolcallEnd, ContentIndex: slot.contentIndex, ToolCall: &call, Partial: output})
				delete(outputSlots, event.OutputIndex)
			}
		case "response.completed", "response.incomplete":
			var response responsesTerminalResponse
			if jsonUnmarshalStrict(event.Response, &response) == nil {
				finalizeResponse(&response)
			}
		case "error":
			panic(fmt.Errorf("Error Code %s: %s", event.Code, event.Message))
		case "response.failed":
			sawTerminalResponseEvent = true
			var response responsesTerminalResponse
			_ = jsonUnmarshalStrict(event.Response, &response)
			if response.Status != "" {
				status := response.Status
				output.RawStopReason = &status
			}
			var msg string
			switch {
			case response.Error != nil:
				code := response.Error.Code
				if code == "" {
					code = "unknown"
				}
				message := response.Error.Message
				if message == "" {
					message = "no message"
				}
				msg = code + ": " + message
			case response.IncompleteDetails != nil && response.IncompleteDetails.Reason != "":
				msg = "incomplete: " + response.IncompleteDetails.Reason
			default:
				msg = "Unknown error (no error details in response)"
			}
			panic(fmt.Errorf("%s", msg))
		}
	})
	if iterErr != nil {
		return iterErr
	}
	if !sawTerminalResponseEvent {
		return fmt.Errorf("OpenAI Responses stream ended before a terminal response event")
	}
	return nil
}

// iterateResponsesEvents decodes the Responses SSE stream, stopping at
// [DONE].
func iterateResponsesEvents(ctx context.Context, body io.Reader, emit func(event *responsesStreamEvent)) error {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	decoder := &SSEDecoder{}
	for scanner.Scan() {
		if ctxErr(ctx) != nil {
			return ctxErr(ctx)
		}
		if event := decoder.decodeLine(scanner.Text()); event != nil {
			data := strings.TrimSpace(event.Data)
			if data == "[DONE]" {
				return nil
			}
			if data == "" {
				continue
			}
			var parsed responsesStreamEvent
			if jsonUnmarshalStrict(json.RawMessage(data), &parsed) != nil {
				continue
			}
			emit(&parsed)
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if event := decoder.flush(); event != nil {
		data := strings.TrimSpace(event.Data)
		if data != "" && data != "[DONE]" {
			var parsed responsesStreamEvent
			if jsonUnmarshalStrict(json.RawMessage(data), &parsed) == nil {
				emit(&parsed)
			}
		}
	}
	return nil
}

// mapResponsesStopReason maps a Responses status (plus incomplete reason).
func mapResponsesStopReason(status string, incompleteReason string) (StopReason, string, error) {
	switch status {
	case "":
		return StopStop, "", nil
	case "completed":
		return StopStop, "", nil
	case "incomplete":
		if incompleteReason == "max_output_tokens" {
			return StopLength, "", nil
		}
		if incompleteReason != "" {
			return StopError, "Response incomplete: " + incompleteReason, nil
		}
		return StopError, "Response incomplete without a provider reason", nil
	case "failed", "cancelled":
		return StopError, "", nil
	case "in_progress", "queued":
		return StopStop, "", nil
	default:
		return "", "", fmt.Errorf("Unhandled stop reason: %s", status)
	}
}

// getServiceTierCostMultiplier: flex halves cost; priority doubles (2.5x on
// gpt-5.5).
func getServiceTierCostMultiplier(model *Model, serviceTier string) float64 {
	switch serviceTier {
	case "flex":
		return 0.5
	case "priority":
		if model.ID == "gpt-5.5" {
			return 2.5
		}
		return 2
	default:
		return 1
	}
}

// applyServiceTierPricing scales the computed cost and recomputes the total.
func applyServiceTierPricing(usage *Usage, serviceTier string, model *Model) {
	multiplier := getServiceTierCostMultiplier(model, serviceTier)
	if multiplier == 1 {
		return
	}
	usage.Cost.Input *= multiplier
	usage.Cost.Output *= multiplier
	usage.Cost.CacheRead *= multiplier
	usage.Cost.CacheWrite *= multiplier
	usage.Cost.Total = usage.Cost.Input + usage.Cost.Output + usage.Cost.CacheRead + usage.Cost.CacheWrite
}

// StreamOpenAIResponses implements the openai-responses stream function.
func StreamOpenAIResponses(model *Model, context TranscriptContext, options *OpenAIResponsesOptions) *AssistantMessageEventStream {
	return streamOpenAIResponses(model, context, options, nil)
}

// openAIResponsesStreamConfig parameterizes the shared Responses streaming loop
// for the OpenAI and Azure dialects.
type openAIResponsesStreamConfig struct {
	// errorPrefix labels provider failures.
	errorPrefix string
	// noStopReasonMessage is reported when the stream ends without a stop reason.
	noStopReasonMessage string
	// toolCallProviders selects the tool-call id form the dialect expects.
	toolCallProviders map[string]bool
	// modelName resolves the request's model field (Azure sends the deployment
	// name).
	modelName func(model *Model, options *OpenAIResponsesOptions) string
	// buildRequest issues the streaming call.
	buildRequest func(
		ctx context.Context, model *Model, body []byte, apiKey string,
		options *OpenAIResponsesOptions, compat ResolvedOpenAIResponsesCompat,
		cacheSessionID string, messages []Message,
	) (*http.Request, error)
}

var defaultOpenAIResponsesStreamConfig = &openAIResponsesStreamConfig{
	errorPrefix:         defaultOpenAIErrorPrefix(nil),
	noStopReasonMessage: "OpenAI Responses stream ended without a stop reason",
	toolCallProviders:   openAIToolCallProviders,
	buildRequest:        buildOpenAIResponsesRequest,
}

func defaultOpenAIErrorPrefix(model *Model) string {
	prefix := "OpenAI"
	if model != nil && model.Provider != "openai" {
		prefix = model.Provider
	}
	return prefix + " API error"
}

// buildOpenAIResponsesRequest posts the params to the provider's /responses
// endpoint with bearer auth.
func buildOpenAIResponsesRequest(
	ctx context.Context, model *Model, body []byte, apiKey string,
	options *OpenAIResponsesOptions, compat ResolvedOpenAIResponsesCompat,
	cacheSessionID string, messages []Message,
) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimSuffix(model.BaseURL, "/")+"/responses", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", GetPiUserAgent())
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	if model.Provider == "github-copilot" {
		hasImages := HasCopilotVisionInput(messages)
		for name, value := range BuildCopilotDynamicHeaders(messages, hasImages) {
			req.Header.Set(name, value)
		}
	}
	if cacheSessionID != "" {
		if compat.SessionAffinityFormat == SessionAffinityOpenRouter {
			req.Header.Set("x-session-id", cacheSessionID)
		} else {
			if compat.SessionAffinityFormat == SessionAffinityOpenAI {
				req.Header.Set("session_id", cacheSessionID)
			}
			req.Header.Set("x-client-request-id", cacheSessionID)
		}
	}
	for name, value := range model.Headers {
		req.Header.Set(name, value)
	}
	for name, value := range options.Headers {
		if value == nil {
			req.Header.Del(name)
			continue
		}
		req.Header.Set(name, *value)
	}
	return req, nil
}

// streamOpenAIResponses runs one Responses streaming request through the shared
// loop.
func streamOpenAIResponses(model *Model, context TranscriptContext, options *OpenAIResponsesOptions, config *openAIResponsesStreamConfig) *AssistantMessageEventStream {
	if config == nil {
		config = &openAIResponsesStreamConfig{
			errorPrefix:         defaultOpenAIErrorPrefix(model),
			noStopReasonMessage: "OpenAI Responses stream ended without a stop reason",
			toolCallProviders:   openAIToolCallProviders,
			buildRequest:        buildOpenAIResponsesRequest,
		}
	}
	if config.toolCallProviders == nil {
		config.toolCallProviders = openAIToolCallProviders
	}
	stream := NewAssistantMessageEventStream()
	compat := GetOpenAIResponsesCompat(model)
	normalizedContext := ResolveTranscript(context, compat.SupportsMidConvoSystemMessages)

	go func() {
		ctx := bgCtx()
		if options != nil && options.Ctx != nil {
			ctx = options.Ctx
		}
		if options == nil {
			options = &OpenAIResponsesOptions{}
		}
		output := &AssistantMessage{
			API: model.API, Provider: model.Provider, Model: model.ID,
			Usage: Usage{Cost: UsageCost{}}, StopReason: StopPending,
			Timestamp: time.Now().UnixMilli(),
		}
		fail := func(err error) {
			for i, block := range output.Content {
				if call, ok := block.(ToolCall); ok {
					call.Arguments = parseStreamingArgs(call.Arguments)
					output.Content[i] = call
				}
			}
			if ctxErr(ctx) != nil {
				output.StopReason = StopAborted
			} else {
				output.StopReason = StopError
			}
			message := FormatProviderError(NormalizeProviderError(err), config.errorPrefix)
			output.ErrorMessage = &message
			stream.Push(AssistantMessageEvent{Type: EventError, Reason: output.StopReason, Error: output})
			stream.End(&output)
		}
		defer func() {
			if r := recover(); r != nil {
				if err, ok := r.(error); ok {
					fail(err)
				} else {
					fail(fmt.Errorf("%v", r))
				}
			}
		}()

		apiKey, keyErr := getClientAPIKey(model.Provider, options.APIKey, options.Headers)
		if keyErr != nil {
			fail(keyErr)
			return
		}
		cacheRetention := ResolveCacheRetention(options.CacheRetention, options.Env)
		cacheSessionID := options.SessionID
		if cacheRetention == CacheRetentionNone {
			cacheSessionID = ""
		}
		grammarToolInputProperties := CreateGrammarToolInputProperties(
			GetDeclaredTools(normalizedContext.Messages), compat.SupportsOpenAIGrammarTools)

		params, err := BuildOpenAIResponsesParamsWithProviders(model, normalizedContext, options, &compat,
			grammarToolInputProperties, config.toolCallProviders)
		if err == nil && config.modelName != nil {
			params.Model = config.modelName(model, options)
		}
		if err != nil {
			fail(err)
			return
		}
		if options.OnPayload != nil {
			if next := options.OnPayload(mustMarshalJSON(params), model); next != nil {
				var replaced OpenAIResponsesParams
				if perr := jsonUnmarshalStrict(next, &replaced); perr == nil {
					params = &replaced
				}
			}
		}

		var resp *http.Response
		requestErr := func() error {
			var attemptErr error
			resp, attemptErr = RetryProviderRequest(ctx, func() (*http.Response, error) {
				body, berr := aiMarshalNoHTMLEscaping(params)
				if berr != nil {
					return nil, berr
				}
				req, nerr := config.buildRequest(ctx, model, body, apiKey, options, compat, cacheSessionID, normalizedContext.Messages)
				if nerr != nil {
					return nil, nerr
				}
				hresp, rerr := http.DefaultClient.Do(req)
				if rerr != nil {
					return nil, rerr
				}
				if hresp.StatusCode >= 400 {
					raw, _ := io.ReadAll(io.LimitReader(hresp.Body, 1<<20))
					hresp.Body.Close()
					return nil, &ProviderError{
						Status: hresp.StatusCode, Headers: hresp.Header,
						Message: fmt.Sprintf("%d %s: %s", hresp.StatusCode, http.StatusText(hresp.StatusCode), string(raw)),
						Body:    string(raw),
					}
				}
				return hresp, nil
			}, &ProviderRetryOptions{MaxRetries: options.MaxRetries, MaxRetryDelayMS: options.MaxRetryDelayMs})
			return attemptErr
		}()
		if requestErr != nil {
			fail(requestErr)
			return
		}
		defer resp.Body.Close()
		if options.OnResponse != nil {
			headers := map[string]string{}
			for k, v := range resp.Header {
				headers[strings.ToLower(k)] = strings.Join(v, ", ")
			}
			options.OnResponse(ProviderResponse{Status: resp.StatusCode, Headers: headers}, model)
		}
		stream.Push(AssistantMessageEvent{Type: EventStart, Partial: output})

		if err := ProcessResponsesStream(ctx, resp.Body, output, stream, model, options, grammarToolInputProperties); err != nil {
			fail(err)
			return
		}
		if ctxErr(ctx) != nil {
			fail(fmt.Errorf("Request was aborted"))
			return
		}
		if output.StopReason == StopPending {
			fail(fmt.Errorf("%s", config.noStopReasonMessage))
			return
		}
		if output.StopReason == StopAborted || output.StopReason == StopError {
			msg := "An unknown error occurred"
			if output.ErrorMessage != nil {
				msg = *output.ErrorMessage
			}
			fail(fmt.Errorf("%s", msg))
			return
		}
		stream.Push(AssistantMessageEvent{Type: EventDone, Reason: output.StopReason, Message: output})
		stream.End(&output)
	}()
	return stream
}

// aiMarshalNoHTMLEscaping marshals a request params value.
func aiMarshalNoHTMLEscaping(v any) ([]byte, error) { return MarshalJSON(v) }

// StreamOpenAIResponsesSimple maps reasoning levels (port of streamSimple).
func StreamOpenAIResponsesSimple(model *Model, context TranscriptContext, options *SimpleStreamOptions) *AssistantMessageEventStream {
	if options == nil {
		options = &SimpleStreamOptions{}
	}
	stream := NewAssistantMessageEventStream()
	if _, err := getClientAPIKey(model.Provider, options.APIKey, options.Headers); err != nil {
		go func() {
			message := err.Error()
			msg := &AssistantMessage{
				API: model.API, Provider: model.Provider, Model: model.ID,
				Usage: Usage{Cost: UsageCost{}}, StopReason: StopError,
				ErrorMessage: &message, Timestamp: time.Now().UnixMilli(),
			}
			stream.Push(AssistantMessageEvent{Type: EventError, Reason: StopError, Error: msg})
			stream.End(&msg)
		}()
		return stream
	}
	go func() {
		responsesOptions := &OpenAIResponsesOptions{StreamOptions: options.StreamOptions}
		if options.ToolChoice != nil {
			responsesOptions.ToolChoice = mustMarshalJSON(*options.ToolChoice)
		}
		if options.Reasoning != "" {
			clamped := ClampThinkingLevel(model, options.Reasoning)
			if clamped != ThinkOff {
				responsesOptions.ReasoningEffort = clamped
			}
		}
		inner := StreamOpenAIResponses(model, context, responsesOptions)
		for {
			event, ok := inner.Next(bgCtx())
			if !ok {
				break
			}
			stream.Push(event)
		}
		if result, err := inner.Result(bgCtx()); err == nil {
			stream.End(&result)
		} else {
			stream.End(nil)
		}
	}()
	return stream
}
