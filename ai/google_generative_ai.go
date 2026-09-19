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
	"sync/atomic"
	"time"
)

// Port of api/google-generative-ai.ts: the Gemini API stream adapter.

// GoogleOptions extends StreamOptions for google-generative-ai.
type GoogleOptions struct {
	StreamOptions
	// ToolChoice: "auto" | "none" | "any".
	ToolChoice string
	// Thinking configures extended thinking.
	Thinking *GoogleThinkingOption
}

// GoogleThinkingOption is the thinking request input.
type GoogleThinkingOption struct {
	Enabled bool
	// BudgetTokens: -1 for dynamic, 0 to disable (token-based models only).
	BudgetTokens *int
	// Level selects the discrete Gemini 3 control.
	Level GoogleApiThinkingLevel
}

// GoogleGenerateContentParams is the streaming request body.
type GoogleGenerateContentParams struct {
	Contents          []GoogleContent          `json:"contents"`
	Tools             []GoogleToolGroup        `json:"tools,omitempty"`
	ToolConfig        *GoogleToolConfig        `json:"toolConfig,omitempty"`
	SystemInstruction *GoogleSystemInstruction `json:"systemInstruction,omitempty"`
	GenerationConfig  *GoogleGenerationConfig  `json:"generationConfig,omitempty"`
}

// GoogleToolConfig is the tool configuration.
type GoogleToolConfig struct {
	FunctionCallingConfig *GoogleFunctionCallingConfig `json:"functionCallingConfig,omitempty"`
}

// GoogleFunctionCallingConfig selects the calling mode.
type GoogleFunctionCallingConfig struct {
	Mode string `json:"mode"`
}

// GoogleSystemInstruction carries the system prompt.
type GoogleSystemInstruction struct {
	Parts []GooglePart `json:"parts"`
}

// GoogleGenerationConfig is the generation configuration.
type GoogleGenerationConfig struct {
	Temperature     *float64              `json:"temperature,omitempty"`
	MaxOutputTokens *int64                `json:"maxOutputTokens,omitempty"`
	ThinkingConfig  *GoogleThinkingConfig `json:"thinkingConfig,omitempty"`
}

// BuildGoogleParams builds the streaming request body
// (port of buildParams).
func BuildGoogleParams(model *Model, context TranscriptContext, options *GoogleOptions) (*GoogleGenerateContentParams, error) {
	if options == nil {
		options = &GoogleOptions{}
	}
	contents, err := ConvertGoogleMessages(model, context)
	if err != nil {
		return nil, err
	}
	initialSystemMessage := GetInitialSystemMessage(context.Messages)
	currentTools := GetCurrentTools(context.Messages)

	if options.Ctx != nil && ctxErr(options.Ctx) != nil {
		return nil, fmt.Errorf("Request aborted")
	}

	supportsStrictMode := SupportsGoogleStrictToolSampling(model.ID)
	functionCallingMode := ""
	if len(currentTools) > 0 {
		functionCallingMode = ResolveGoogleFunctionCallingMode(currentTools, options.ToolChoice, supportsStrictMode)
	}
	systemInstruction := ""
	if initialSystemMessage != nil {
		systemInstruction = GetSystemMessageText(initialSystemMessage)
	}

	params := &GoogleGenerateContentParams{Contents: contents}
	if systemInstruction != "" {
		params.SystemInstruction = &GoogleSystemInstruction{
			Parts: []GooglePart{{Text: SanitizeSurrogates(systemInstruction)}},
		}
	}
	if len(currentTools) > 0 {
		tools, err := ConvertGoogleTools(currentTools, false, supportsStrictMode)
		if err != nil {
			return nil, err
		}
		params.Tools = tools
	}
	if functionCallingMode != "" {
		params.ToolConfig = &GoogleToolConfig{
			FunctionCallingConfig: &GoogleFunctionCallingConfig{Mode: functionCallingMode},
		}
	}

	generationConfig := &GoogleGenerationConfig{}
	hasGenerationConfig := false
	if options.Temperature != nil {
		generationConfig.Temperature = options.Temperature
		hasGenerationConfig = true
	}
	if options.MaxTokens != nil {
		tokens := int64(*options.MaxTokens)
		generationConfig.MaxOutputTokens = &tokens
		hasGenerationConfig = true
	}

	if options.Thinking != nil && options.Thinking.Enabled && model.Reasoning {
		includeThoughts := true
		thinkingConfig := &GoogleThinkingConfig{IncludeThoughts: &includeThoughts}
		if options.Thinking.Level != "" {
			thinkingConfig.ThinkingLevel = options.Thinking.Level
		} else if options.Thinking.BudgetTokens != nil {
			thinkingConfig.ThinkingBudget = options.Thinking.BudgetTokens
		}
		generationConfig.ThinkingConfig = thinkingConfig
		hasGenerationConfig = true
	} else if model.Reasoning && options.Thinking != nil && !options.Thinking.Enabled {
		generationConfig.ThinkingConfig = GetDisabledGoogleThinkingConfig(model)
		hasGenerationConfig = true
	}
	if hasGenerationConfig {
		params.GenerationConfig = generationConfig
	}
	return params, nil
}

// GetGoogleBudget resolves the token budget for a level
// (port of getGoogleBudget).
func GetGoogleBudget(model *Model, level ResolvedGoogleThinkingLevel, customBudgets *ThinkingBudgets) int {
	budgetFor := func(level ThinkingLevel) (int, bool) {
		if customBudgets == nil {
			return 0, false
		}
		switch level {
		case ThinkMinimal:
			if customBudgets.Minimal != nil {
				return *customBudgets.Minimal, true
			}
		case ThinkLow:
			if customBudgets.Low != nil {
				return *customBudgets.Low, true
			}
		case ThinkMedium:
			if customBudgets.Medium != nil {
				return *customBudgets.Medium, true
			}
		case ThinkHigh:
			if customBudgets.High != nil {
				return *customBudgets.High, true
			}
		}
		return 0, false
	}
	if value, ok := budgetFor(level); ok {
		return value
	}
	budgetTable := func(minimal, low, medium, high int) int {
		switch level {
		case ThinkMinimal:
			return minimal
		case ThinkLow:
			return low
		case ThinkMedium:
			return medium
		case ThinkHigh:
			return high
		default:
			return -1
		}
	}
	switch {
	case strings.Contains(model.ID, "2.5-pro"):
		return budgetTable(128, 2048, 8192, 32768)
	case strings.Contains(model.ID, "2.5-flash-lite"):
		return budgetTable(512, 2048, 8192, 24576)
	case strings.Contains(model.ID, "2.5-flash"):
		return budgetTable(128, 2048, 8192, 24576)
	default:
		return -1
	}
}

// googleToolCallCounter generates unique tool-call ids when the provider
// omits or duplicates them.
var googleToolCallCounter atomic.Int64

// googleChunk is one streamed GenerateContentResponse.
type googleChunk struct {
	ResponseID string `json:"responseId"`
	Candidates []struct {
		Content *struct {
			Roles string       `json:"role"`
			Parts []GooglePart `json:"parts"`
		} `json:"content"`
		FinishReason string `json:"finishReason"`
	} `json:"candidates"`
	UsageMetadata *struct {
		PromptTokenCount        *int64 `json:"promptTokenCount"`
		CandidatesTokenCount    *int64 `json:"candidatesTokenCount"`
		ThoughtsTokenCount      *int64 `json:"thoughtsTokenCount"`
		CachedContentTokenCount *int64 `json:"cachedContentTokenCount"`
		TotalTokenCount         *int64 `json:"totalTokenCount"`
	} `json:"usageMetadata"`
}

// StreamGoogleGenerativeAI implements the google-generative-ai stream
// function.
func StreamGoogleGenerativeAI(model *Model, context TranscriptContext, options *GoogleOptions) *AssistantMessageEventStream {
	stream := NewAssistantMessageEventStream()
	normalizedContext := CollapseSystemMessages(context)

	go func() {
		ctx := bgCtx()
		if options != nil && options.Ctx != nil {
			ctx = options.Ctx
		}
		if options == nil {
			options = &GoogleOptions{}
		}
		output := &AssistantMessage{
			API: APIGoogleGenerativeAI, Provider: model.Provider, Model: model.ID,
			Usage: Usage{Cost: UsageCost{}}, StopReason: StopPending,
			Timestamp: time.Now().UnixMilli(),
		}
		fail := func(err error) {
			if ctxErr(ctx) != nil {
				output.StopReason = StopAborted
			} else {
				output.StopReason = StopError
			}
			message := FormatProviderError(NormalizeProviderError(err), "")
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

		if options.APIKey == "" {
			fail(fmt.Errorf("No API key for provider: %s", model.Provider))
			return
		}
		params, err := BuildGoogleParams(model, normalizedContext, options)
		if err != nil {
			fail(err)
			return
		}
		if options.OnPayload != nil {
			if next := options.OnPayload(mustMarshalJSON(params), model); next != nil {
				var replaced GoogleGenerateContentParams
				if perr := jsonUnmarshalStrict(next, &replaced); perr == nil {
					params = &replaced
				}
			}
		}

		resp, err := requestGoogleStream(ctx, model, params, options)
		if err != nil {
			fail(err)
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

		var currentBlock Content
		blockIndex := func() int { return len(output.Content) - 1 }

		iterErr := iterateGoogleChunks(ctx, resp.Body, func(chunk *googleChunk) {
			if chunk.ResponseID != "" && output.ResponseID == nil {
				id := chunk.ResponseID
				output.ResponseID = &id
			}
			if len(chunk.Candidates) > 0 {
				candidate := chunk.Candidates[0]
				if candidate.Content != nil {
					for i := range candidate.Content.Parts {
						part := &candidate.Content.Parts[i]
						if part.Text != "" || part.Thought != nil {
							isThinking := IsThinkingPart(part)
							if currentBlock == nil ||
								(isThinking && currentBlock.contentKind() != KindThinking) ||
								(!isThinking && currentBlock.contentKind() != KindText) {
								if currentBlock != nil {
									if text, ok := currentBlock.(TextContent); ok {
										stream.Push(AssistantMessageEvent{Type: EventTextEnd, ContentIndex: blockIndex(), Content: text.Text, Partial: output})
									} else if thinking, ok := currentBlock.(ThinkingContent); ok {
										stream.Push(AssistantMessageEvent{Type: EventThinkingEnd, ContentIndex: blockIndex(), Content: thinking.Thinking, Partial: output})
									}
								}
								if isThinking {
									currentBlock = ThinkingContent{}
									output.Content = append(output.Content, currentBlock)
									stream.Push(AssistantMessageEvent{Type: EventThinkingStart, ContentIndex: blockIndex(), Partial: output})
								} else {
									currentBlock = TextContent{}
									output.Content = append(output.Content, currentBlock)
									stream.Push(AssistantMessageEvent{Type: EventTextStart, ContentIndex: blockIndex(), Partial: output})
								}
							}
							if thinking, ok := currentBlock.(ThinkingContent); ok {
								thinking.Thinking += part.Text
								if signature := RetainThoughtSignature(deref(thinking.ThinkingSignature), part.ThoughtSignature); signature != "" {
									thinking.ThinkingSignature = &signature
								}
								currentBlock = thinking
								output.Content[blockIndex()] = thinking
								stream.Push(AssistantMessageEvent{Type: EventThinkingDelta, ContentIndex: blockIndex(), Delta: part.Text, Partial: output})
							} else if text, ok := currentBlock.(TextContent); ok {
								text.Text += part.Text
								if signature := RetainThoughtSignature(deref(text.TextSignature), part.ThoughtSignature); signature != "" {
									text.TextSignature = &signature
								}
								currentBlock = text
								output.Content[blockIndex()] = text
								stream.Push(AssistantMessageEvent{Type: EventTextDelta, ContentIndex: blockIndex(), Delta: part.Text, Partial: output})
							}
						}

						if part.FunctionCall != nil {
							if currentBlock != nil {
								if text, ok := currentBlock.(TextContent); ok {
									stream.Push(AssistantMessageEvent{Type: EventTextEnd, ContentIndex: blockIndex(), Content: text.Text, Partial: output})
								} else if thinking, ok := currentBlock.(ThinkingContent); ok {
									stream.Push(AssistantMessageEvent{Type: EventThinkingEnd, ContentIndex: blockIndex(), Content: thinking.Thinking, Partial: output})
								}
								currentBlock = nil
							}

							// Generate a unique id when absent or duplicated.
							providedID := part.FunctionCall.ID
							needsNewID := providedID == ""
							if !needsNewID {
								for _, block := range output.Content {
									if call, ok := block.(ToolCall); ok && call.ID == providedID {
										needsNewID = true
									}
								}
							}
							toolCallID := providedID
							if needsNewID {
								toolCallID = fmt.Sprintf("%s_%d_%d", part.FunctionCall.Name,
									time.Now().UnixMilli(), googleToolCallCounter.Add(1))
							}
							args := part.FunctionCall.Args
							if len(args) == 0 {
								args = json.RawMessage("{}")
							}
							call := ToolCall{ID: toolCallID, Name: part.FunctionCall.Name, Arguments: args}
							if part.ThoughtSignature != nil && *part.ThoughtSignature != "" {
								signature := *part.ThoughtSignature
								call.ThoughtSignature = &signature
							}
							output.Content = append(output.Content, call)
							stream.Push(AssistantMessageEvent{Type: EventToolcallStart, ContentIndex: blockIndex(), Partial: output})
							stream.Push(AssistantMessageEvent{Type: EventToolcallDelta, ContentIndex: blockIndex(),
								Delta: string(mustMarshalJSON(json.RawMessage(call.Arguments))), Partial: output})
							finalCall := call
							stream.Push(AssistantMessageEvent{Type: EventToolcallEnd, ContentIndex: blockIndex(), ToolCall: &finalCall, Partial: output})
						}
					}
				}
				if candidate.FinishReason != "" {
					raw := candidate.FinishReason
					output.RawStopReason = &raw
					output.StopReason = MapGoogleStopReason(candidate.FinishReason)
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
			}
			if chunk.UsageMetadata != nil {
				usage := chunk.UsageMetadata
				inputTokens := derefI64(usage.PromptTokenCount) - derefI64(usage.CachedContentTokenCount)
				outputTokens := derefI64(usage.CandidatesTokenCount) + derefI64(usage.ThoughtsTokenCount)
				output.Usage = Usage{
					Input:       inputTokens,
					Output:      outputTokens,
					CacheRead:   derefI64(usage.CachedContentTokenCount),
					CacheWrite:  0,
					TotalTokens: derefI64(usage.TotalTokenCount),
					Cost:        UsageCost{},
				}
				reasoning := derefI64(usage.ThoughtsTokenCount)
				output.Usage.Reasoning = &reasoning
				CalculateCost(model, &output.Usage)
			}
		})
		if iterErr != nil {
			fail(iterErr)
			return
		}

		if currentBlock != nil {
			if text, ok := currentBlock.(TextContent); ok {
				stream.Push(AssistantMessageEvent{Type: EventTextEnd, ContentIndex: blockIndex(), Content: text.Text, Partial: output})
			} else if thinking, ok := currentBlock.(ThinkingContent); ok {
				stream.Push(AssistantMessageEvent{Type: EventThinkingEnd, ContentIndex: blockIndex(), Content: thinking.Thinking, Partial: output})
			}
		}
		if ctxErr(ctx) != nil {
			fail(fmt.Errorf("Request was aborted"))
			return
		}
		if output.StopReason == StopPending {
			fail(fmt.Errorf("Google stream ended without a finish reason"))
			return
		}
		if output.StopReason == StopAborted || output.StopReason == StopError {
			message := "An unknown error occurred"
			if output.RawStopReason != nil {
				message = "Provider stopped with: " + *output.RawStopReason
			}
			fail(fmt.Errorf("%s", message))
			return
		}
		stream.Push(AssistantMessageEvent{Type: EventDone, Reason: output.StopReason, Message: output})
		stream.End(&output)
	}()
	return stream
}

// requestGoogleStream issues the streamGenerateContent call with the shared
// provider retry policy.
func requestGoogleStream(ctx context.Context, model *Model, params *GoogleGenerateContentParams, options *GoogleOptions) (*http.Response, error) {
	body, err := MarshalJSON(params)
	if err != nil {
		return nil, err
	}
	url := strings.TrimSuffix(model.BaseURL, "/") + "/models/" + model.ID + ":streamGenerateContent?alt=sse"
	var resp *http.Response
	resp, err = RetryProviderRequest(ctx, func() (*http.Response, error) {
		req, nerr := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if nerr != nil {
			return nil, nerr
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", GetPiUserAgent())
		req.Header.Set("x-goog-api-key", options.APIKey)
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
	if err != nil {
		return nil, err
	}
	return resp, nil
}

// iterateGoogleChunks decodes the Gemini SSE stream.
func iterateGoogleChunks(ctx context.Context, body io.Reader, emit func(chunk *googleChunk)) error {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	decoder := &SSEDecoder{}
	process := func(data string) {
		data = strings.TrimSpace(data)
		if data == "" || data == "[DONE]" {
			return
		}
		var chunk googleChunk
		if jsonUnmarshalStrict(json.RawMessage(data), &chunk) == nil {
			emit(&chunk)
		}
	}
	for scanner.Scan() {
		if ctxErr(ctx) != nil {
			return ctxErr(ctx)
		}
		if event := decoder.decodeLine(scanner.Text()); event != nil {
			process(event.Data)
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if event := decoder.flush(); event != nil {
		process(event.Data)
	}
	return nil
}

// StreamGoogleGenerativeAISimple maps reasoning levels onto the Google
// thinking controls (port of streamSimple).
func StreamGoogleGenerativeAISimple(model *Model, context TranscriptContext, options *SimpleStreamOptions) *AssistantMessageEventStream {
	if options == nil {
		options = &SimpleStreamOptions{}
	}
	stream := NewAssistantMessageEventStream()
	if options.APIKey == "" {
		go func() {
			message := fmt.Sprintf("No API key for provider: %s", model.Provider)
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
		googleOptions := &GoogleOptions{StreamOptions: options.StreamOptions, ToolChoice: deref(options.ToolChoice)}
		if options.Reasoning == "" {
			googleOptions.Thinking = &GoogleThinkingOption{Enabled: false}
			forwardStream(stream, StreamGoogleGenerativeAI(model, context, googleOptions))
			return
		}
		clamped := ClampThinkingLevel(model, options.Reasoning)
		if clamped == ThinkOff {
			googleOptions.Thinking = &GoogleThinkingOption{Enabled: false}
			forwardStream(stream, StreamGoogleGenerativeAI(model, context, googleOptions))
			return
		}
		resolved, err := ResolveGoogleThinkingLevel(model, clamped)
		if err != nil {
			message := err.Error()
			msg := &AssistantMessage{
				API: model.API, Provider: model.Provider, Model: model.ID,
				Usage: Usage{Cost: UsageCost{}}, StopReason: StopError,
				ErrorMessage: &message, Timestamp: time.Now().UnixMilli(),
			}
			stream.Push(AssistantMessageEvent{Type: EventError, Reason: StopError, Error: msg})
			stream.End(&msg)
			return
		}
		if UsesGoogleThinkingLevel(model) {
			googleOptions.Thinking = &GoogleThinkingOption{Enabled: true, Level: ToGoogleThinkingLevel(resolved)}
		} else {
			budget := GetGoogleBudget(model, resolved, options.ThinkingBudgets)
			googleOptions.Thinking = &GoogleThinkingOption{Enabled: true, BudgetTokens: &budget}
		}
		forwardStream(stream, StreamGoogleGenerativeAI(model, context, googleOptions))
	}()
	return stream
}

// forwardStream pumps an inner stream into an outer one.
func forwardStream(outer *AssistantMessageEventStream, inner *AssistantMessageEventStream) {
	for {
		event, ok := inner.Next(bgCtx())
		if !ok {
			break
		}
		outer.Push(event)
	}
	if result, err := inner.Result(bgCtx()); err == nil {
		outer.End(&result)
	} else {
		outer.End(nil)
	}
}
