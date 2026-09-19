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

// Port of api/openai-completions.ts streaming half: SSE chunk consumption,
// the block-building loop, streamSimple, and the HTTP client.

// iterateOpenAIChunks reads the SSE stream of ChatCompletionChunk payloads,
// stopping at `data: [DONE]`. Chunk decoding is lenient (JSON per event).
func iterateOpenAIChunks(ctx context.Context, body io.Reader, emit func(json.RawMessage)) error {
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
			emit(json.RawMessage(data))
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if event := decoder.flush(); event != nil {
		data := strings.TrimSpace(event.Data)
		if data != "" && data != "[DONE]" {
			emit(json.RawMessage(data))
		}
	}
	return nil
}

// openaiChunk is the parsed ChatCompletionChunk subset pi consumes.
type openaiChunk struct {
	ID      string          `json:"id"`
	Model   string          `json:"model"`
	Usage   json.RawMessage `json:"usage,omitempty"`
	Choices []struct {
		FinishReason string           `json:"finish_reason"`
		Delta        openaiChunkDelta `json:"delta"`
		// Fallback usage on the choice (e.g. Moonshot).
		Usage json.RawMessage `json:"usage,omitempty"`
	} `json:"choices"`
}

type openaiChunkDelta struct {
	Content   *string `json:"content"`
	ToolCalls []struct {
		Index    *int   `json:"index"`
		ID       string `json:"id"`
		Function *struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"function"`
		Custom *struct {
			Name  string `json:"name"`
			Input string `json:"input"`
		} `json:"custom"`
	} `json:"tool_calls"`
	// reasoning_content / reasoning / reasoning_text live here.
	Extra openaiChunkDeltaExtra `json:"-"`
	// reasoning_details (OpenRouter).
	ReasoningDetails []json.RawMessage `json:"reasoning_details,omitempty"`
}

// UnmarshalJSON captures unknown delta fields for the reasoning-field scan.
func (d *openaiChunkDelta) UnmarshalJSON(data []byte) error {
	type wire struct {
		Content   *string `json:"content"`
		ToolCalls []struct {
			Index    *int   `json:"index"`
			ID       string `json:"id"`
			Function *struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			} `json:"function"`
			Custom *struct {
				Name  string `json:"name"`
				Input string `json:"input"`
			} `json:"custom"`
		} `json:"tool_calls"`
		ReasoningDetails []json.RawMessage `json:"reasoning_details,omitempty"`
		ReasoningContent *string           `json:"reasoning_content"`
		Reasoning        *string           `json:"reasoning"`
		ReasoningText    *string           `json:"reasoning_text"`
	}
	var w wire
	if err := jsonUnmarshalStrict(data, &w); err != nil {
		return err
	}
	d.Content = w.Content
	d.ToolCalls = w.ToolCalls
	d.ReasoningDetails = w.ReasoningDetails
	d.Extra = openaiChunkDeltaExtra{
		ReasoningContent: w.ReasoningContent,
		Reasoning:        w.Reasoning,
		ReasoningText:    w.ReasoningText,
	}
	return nil
}

type openaiChunkDeltaExtra struct {
	ReasoningContent *string
	Reasoning        *string
	ReasoningText    *string
}

// openaiStreamingToolCall is the live tool-call accumulator.
type openaiStreamingToolCall struct {
	call        ToolCall
	partialArgs string
	customInput *struct {
		property   string
		jsonBuffer GrammarToolInputJSONBuffer
	}
	streamIndex *int
}

// StreamOpenAICompletions implements the openai-completions stream function.
func StreamOpenAICompletions(model *Model, context TranscriptContext, options *OpenAICompletionsOptions) *AssistantMessageEventStream {
	stream := NewAssistantMessageEventStream()
	compat := GetOpenAICompletionsCompat(model)
	normalizedContext := ResolveTranscript(context, compat.SupportsMidConvoSystemMessages)

	go func() {
		ctx := bgCtx()
		if options != nil && options.Ctx != nil {
			ctx = options.Ctx
		}
		if options == nil {
			options = &OpenAICompletionsOptions{}
		}
		output := &AssistantMessage{
			API: model.API, Provider: model.Provider, Model: model.ID,
			Usage: Usage{Cost: UsageCost{}}, StopReason: StopPending,
			Timestamp: time.Now().UnixMilli(),
		}

		// reasoning_details are replay metadata, not user-visible stream
		// deltas; serialize into the thinking signature when finalized.
		var streamedReasoningDetails []json.RawMessage
		applyStreamedReasoningDetails := func(block *ThinkingContent) {
			if streamedReasoningDetails != nil {
				enc, _ := MarshalJSON(streamedReasoningDetails)
				sig := string(enc)
				block.ThinkingSignature = &sig
			}
		}

		fail := func(err error) {
			for i, block := range output.Content {
				if t, ok := block.(ThinkingContent); ok {
					applyStreamedReasoningDetails(&t)
					output.Content[i] = t
				}
				if tc, ok := block.(ToolCall); ok {
					tc.Arguments = parseStreamingArgs(tc.Arguments)
					output.Content[i] = tc
				}
			}
			if ctxErr(ctx) != nil {
				output.StopReason = StopAborted
			} else {
				output.StopReason = StopError
			}
			norm := NormalizeProviderError(err)
			message := FormatProviderError(norm, "")
			// OpenRouter-style raw metadata deduplication.
			if pe, ok := err.(*ProviderError); ok {
				var probe struct {
					Error *struct {
						Metadata *struct {
							Raw string `json:"raw"`
						} `json:"metadata"`
					} `json:"error"`
				}
				if jsonUnmarshalStrict(json.RawMessage(pe.Body), &probe) == nil && probe.Error != nil &&
					probe.Error.Metadata != nil && probe.Error.Metadata.Raw != "" &&
					!strings.Contains(message, probe.Error.Metadata.Raw) {
					message += "\n" + probe.Error.Metadata.Raw
				}
			}
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
		grammarToolInputProperties := CreateGrammarToolInputProperties(
			GetDeclaredTools(normalizedContext.Messages), compat.SupportsOpenAIGrammarTools)
		cacheRetention := ResolveCacheRetention(options.CacheRetention, options.Env)
		cacheSessionID := options.SessionID
		if cacheRetention == CacheRetentionNone {
			cacheSessionID = ""
		}

		params, err := BuildOpenAICompletionsParams(model, normalizedContext, options, &compat, cacheRetention, grammarToolInputProperties)
		if err != nil {
			fail(err)
			return
		}
		if options.OnPayload != nil {
			if next := options.OnPayload(mustMarshalJSON(params), model); next != nil {
				var replaced OpenAICompletionsParams
				if perr := jsonUnmarshalStrict(next, &replaced); perr == nil {
					params = &replaced
				}
			}
		}

		// HTTP request mirroring the pinned openai SDK: POST
		// {baseURL}/chat/completions, Authorization: Bearer, merged headers.
		var resp *http.Response
		requestErr := func() error {
			var attemptErr error
			resp, attemptErr = RetryProviderRequest(ctx, func() (*http.Response, error) {
				body, berr := MarshalJSON(params)
				if berr != nil {
					return nil, berr
				}
				req, nerr := http.NewRequestWithContext(ctx, http.MethodPost,
					strings.TrimSuffix(model.BaseURL, "/")+"/chat/completions", bytes.NewReader(body))
				if nerr != nil {
					return nil, nerr
				}
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("User-Agent", GetPiUserAgent())
				req.Header.Set("Authorization", "Bearer "+apiKey)
				for name, value := range model.Headers {
					req.Header.Set(name, value)
				}
				// Session affinity headers.
				if cacheSessionID != "" && compat.SendSessionAffinityHeaders {
					switch compat.SessionAffinityFormat {
					case SessionAffinityOpenRouter:
						req.Header.Set("x-session-id", cacheSessionID)
					default:
						if compat.SessionAffinityFormat == SessionAffinityOpenAI {
							req.Header.Set("session_id", cacheSessionID)
						}
						req.Header.Set("x-client-request-id", cacheSessionID)
						req.Header.Set("x-session-affinity", cacheSessionID)
					}
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

		var textBlock *int     // content index when non-nil
		var thinkingBlock *int // content index when non-nil
		hasFinishReason := false
		toolCallBlocksByIndex := map[int]*openaiStreamingToolCall{}
		toolCallBlocksByID := map[string]*openaiStreamingToolCall{}

		finishBlock := func(index int) {
			if index < 0 || index >= len(output.Content) {
				return
			}
			switch block := output.Content[index].(type) {
			case TextContent:
				stream.Push(AssistantMessageEvent{Type: EventTextEnd, ContentIndex: index, Content: block.Text, Partial: output})
			case ThinkingContent:
				applyStreamedReasoningDetails(&block)
				output.Content[index] = block
				stream.Push(AssistantMessageEvent{Type: EventThinkingEnd, ContentIndex: index, Content: block.Thinking, Partial: output})
			case ToolCall:
				streaming := findStreaming(toolCallBlocksByIndex, toolCallBlocksByID, block)
				if streaming != nil && streaming.customInput != nil {
					nextInput := customToolInputValue(streaming)
					delta, ok, derr := AppendGrammarToolInputJSONDelta(&streaming.customInput.jsonBuffer, streaming.customInput.property, nextInput, true)
					if derr != nil {
						panic(derr)
					}
					if ok && delta != "" {
						stream.Push(AssistantMessageEvent{Type: EventToolcallDelta, ContentIndex: index, Delta: delta, Partial: output})
					}
				} else if streaming != nil {
					var parsed map[string]any
					parseStreamingJSONInto(streaming.partialArgs, &parsed)
					enc, _ := MarshalJSON(parsed)
					block.Arguments = enc
				}
				if streaming != nil {
					streaming.partialArgs = ""
				}
				output.Content[index] = block
				stream.Push(AssistantMessageEvent{Type: EventToolcallEnd, ContentIndex: index, ToolCall: &block, Partial: output})
			}
		}

		ensureTextBlock := func() int {
			if textBlock == nil {
				output.Content = append(output.Content, TextContent{Text: ""})
				index := len(output.Content) - 1
				textBlock = &index
				stream.Push(AssistantMessageEvent{Type: EventTextStart, ContentIndex: index, Partial: output})
			}
			return *textBlock
		}
		ensureThinkingBlock := func(thinkingSignature string) int {
			if thinkingBlock == nil {
				sig := thinkingSignature
				output.Content = append(output.Content, ThinkingContent{Thinking: "", ThinkingSignature: &sig})
				index := len(output.Content) - 1
				thinkingBlock = &index
				stream.Push(AssistantMessageEvent{Type: EventThinkingStart, ContentIndex: index, Partial: output})
			}
			return *thinkingBlock
		}
		ensureToolCallBlock := func(delta struct {
			Index    *int   `json:"index"`
			ID       string `json:"id"`
			Function *struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			} `json:"function"`
			Custom *struct {
				Name  string `json:"name"`
				Input string `json:"input"`
			} `json:"custom"`
		}) *openaiStreamingToolCall {
			name := ""
			if delta.Function != nil {
				name = delta.Function.Name
			} else if delta.Custom != nil {
				name = delta.Custom.Name
			}
			var block *openaiStreamingToolCall
			if delta.Index != nil {
				block = toolCallBlocksByIndex[*delta.Index]
			}
			if block == nil && delta.ID != "" {
				block = toolCallBlocksByID[delta.ID]
			}
			if block == nil {
				// Grammar custom tools get their input property from the
				// tool's schema (or "input").
				customInputProperty := ""
				if delta.Custom != nil && delta.Function == nil {
					if prop, ok := grammarToolInputProperties[name]; ok {
						customInputProperty = prop
					} else {
						customInputProperty = "input"
					}
				}
				hasCustomInput := customInputProperty != ""
				block = &openaiStreamingToolCall{
					call: ToolCall{ID: delta.ID, Name: name, Arguments: json.RawMessage(`{"` + customInputProperty + `":""}`)},
				}
				if hasCustomInput {
					block.customInput = &struct {
						property   string
						jsonBuffer GrammarToolInputJSONBuffer
					}{property: customInputProperty}
					block.call.Arguments = mustMarshalJSON(map[string]string{customInputProperty: ""})
				} else {
					block.partialArgs = ""
					block.call.Arguments = json.RawMessage("{}")
				}
				if delta.Index != nil {
					idx := *delta.Index
					block.streamIndex = &idx
					toolCallBlocksByIndex[idx] = block
				}
				if delta.ID != "" {
					toolCallBlocksByID[delta.ID] = block
				}
				output.Content = append(output.Content, block.call)
				stream.Push(AssistantMessageEvent{Type: EventToolcallStart, ContentIndex: len(output.Content) - 1, Partial: output})
			}
			if delta.Index != nil && block.streamIndex == nil {
				idx := *delta.Index
				block.streamIndex = &idx
				toolCallBlocksByIndex[idx] = block
			}
			if delta.ID != "" {
				block.call.ID = delta.ID
				toolCallBlocksByID[delta.ID] = block
			}
			if block.call.Name == "" && name != "" {
				block.call.Name = name
				output.Content[outputIndexForCall(output, block)] = block.call
			}
			if delta.Custom != nil && delta.Function == nil && block.customInput == nil {
				customInputProperty := "input"
				if prop, ok := grammarToolInputProperties[block.call.Name]; ok {
					customInputProperty = prop
				}
				block.call.Arguments = mustMarshalJSON(map[string]string{customInputProperty: ""})
				block.customInput = &struct {
					property   string
					jsonBuffer GrammarToolInputJSONBuffer
				}{property: customInputProperty}
				block.partialArgs = ""
			}
			return block
		}

		iterErr := iterateOpenAIChunks(ctx, resp.Body, func(raw json.RawMessage) {
			var chunk openaiChunk
			if err := jsonUnmarshalStrict(raw, &chunk); err != nil {
				return // upstream skips non-object chunks
			}
			if chunk.ID != "" && output.ResponseID == nil {
				id := chunk.ID
				output.ResponseID = &id
			}
			if chunk.Model != "" && chunk.Model != model.ID && output.ResponseModel == nil {
				rm := chunk.Model
				output.ResponseModel = &rm
			}
			if len(chunk.Usage) > 0 && string(chunk.Usage) != "null" {
				output.Usage = ParseChunkUsage(chunk.Usage, model)
			}

			if len(chunk.Choices) == 0 {
				return
			}
			choice := chunk.Choices[0]
			// Fallback: some providers return usage on the choice.
			if len(chunk.Usage) == 0 && len(choice.Usage) > 0 && string(choice.Usage) != "null" {
				output.Usage = ParseChunkUsage(choice.Usage, model)
			}

			if choice.FinishReason != "" && choice.FinishReason != "null" {
				raw := choice.FinishReason
				output.RawStopReason = &raw
				mapped, errMsg, merr := MapOpenAICompletionsStopReason(raw)
				if merr != nil {
					panic(merr)
				}
				output.StopReason = mapped
				if errMsg != "" {
					output.ErrorMessage = &errMsg
				}
				hasFinishReason = true
			}

			delta := choice.Delta
			if delta.Content != nil && len(*delta.Content) > 0 {
				index := ensureTextBlock()
				if tc, ok := output.Content[index].(TextContent); ok {
					tc.Text += *delta.Content
					output.Content[index] = tc
				}
				stream.Push(AssistantMessageEvent{Type: EventTextDelta, ContentIndex: index, Delta: *delta.Content, Partial: output})
			}

			// Some endpoints return reasoning in reasoning_content
			// (llama.cpp), reasoning (other OpenAI-compatible endpoints), or
			// reasoning_text. Use the first non-empty field to avoid
			// duplication.
			foundReasoningField := ""
			var reasoningDelta string
			for _, field := range []struct {
				name  string
				value *string
			}{{"reasoning_content", delta.Extra.ReasoningContent}, {"reasoning", delta.Extra.Reasoning}, {"reasoning_text", delta.Extra.ReasoningText}} {
				if field.value != nil && len(*field.value) > 0 {
					foundReasoningField = field.name
					reasoningDelta = *field.value
					break
				}
			}
			if foundReasoningField != "" && len(reasoningDelta) > 0 {
				signature := foundReasoningField
				if model.Provider == "opencode-go" && foundReasoningField == "reasoning" {
					signature = "reasoning_content"
				}
				index := ensureThinkingBlock(signature)
				if tc, ok := output.Content[index].(ThinkingContent); ok {
					tc.Thinking += reasoningDelta
					output.Content[index] = tc
				}
				stream.Push(AssistantMessageEvent{Type: EventThinkingDelta, ContentIndex: index, Delta: reasoningDelta, Partial: output})
			}

			for _, toolCall := range delta.ToolCalls {
				block := ensureToolCallBlock(toolCall)
				outputIdx := outputIndexForCall(output, block)
				if block.call.ID == "" && toolCall.ID != "" {
					block.call.ID = toolCall.ID
					toolCallBlocksByID[toolCall.ID] = block
				}
				name := ""
				if toolCall.Function != nil {
					name = toolCall.Function.Name
				} else if toolCall.Custom != nil {
					name = toolCall.Custom.Name
				}
				if block.call.Name == "" && name != "" {
					block.call.Name = name
				}
				if toolCall.Function != nil && toolCall.Function.Arguments != "" {
					deltaText := toolCall.Function.Arguments
					block.partialArgs += deltaText
					var parsed map[string]any
					parseStreamingJSONInto(block.partialArgs, &parsed)
					enc, _ := MarshalJSON(parsed)
					block.call.Arguments = enc
				} else if toolCall.Custom != nil && toolCall.Custom.Input != "" {
					nextInput := customToolInputValue(block) + toolCall.Custom.Input
					deltaText, ok, derr := AppendGrammarToolInputJSONDelta(&block.customInput.jsonBuffer, block.customInput.property, nextInput, false)
					if derr != nil {
						panic(derr)
					}
					if !ok {
						deltaText = ""
					}
					block.call.Arguments = mustMarshalJSON(map[string]string{block.customInput.property: nextInput})
					_ = deltaText
				}
				output.Content[outputIdx] = block.call
				stream.Push(AssistantMessageEvent{Type: EventToolcallDelta, ContentIndex: outputIdx, Delta: toolCallDeltaText(toolCall), Partial: output})
			}

			if len(delta.ReasoningDetails) > 0 {
				for _, detail := range delta.ReasoningDetails {
					if !isOpenAIReasoningDetail(detail) {
						continue
					}
					ensureThinkingBlock("")
					streamedReasoningDetails = appendOpenAIReasoningDetail(streamedReasoningDetails, detail)
				}
			}
		})
		if iterErr != nil {
			fail(iterErr)
			return
		}

		for index := range output.Content {
			finishBlock(index)
		}
		if ctxErr(ctx) != nil {
			fail(fmt.Errorf("Request was aborted"))
			return
		}
		if output.StopReason == StopAborted {
			fail(fmt.Errorf("Request was aborted"))
			return
		}
		if !hasFinishReason && !compat.SupportsFinishReason {
			hasToolCalls := false
			for _, block := range output.Content {
				if _, ok := block.(ToolCall); ok {
					hasToolCalls = true
				}
			}
			if hasToolCalls {
				output.StopReason = StopToolUse
			} else {
				output.StopReason = StopStop
			}
		}
		if output.StopReason == StopError {
			msg := "Provider returned an error stop reason"
			if output.ErrorMessage != nil {
				msg = *output.ErrorMessage
			}
			fail(fmt.Errorf("%s", msg))
			return
		}
		if (compat.SupportsFinishReason && !hasFinishReason) || output.StopReason == StopPending {
			fail(fmt.Errorf("Stream ended without finish_reason"))
			return
		}

		stream.Push(AssistantMessageEvent{Type: EventDone, Reason: output.StopReason, Message: output})
		stream.End(&output)
	}()

	return stream
}

func toolCallDeltaText(toolCall struct {
	Index    *int   `json:"index"`
	ID       string `json:"id"`
	Function *struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
	Custom *struct {
		Name  string `json:"name"`
		Input string `json:"input"`
	} `json:"custom"`
}) string {
	if toolCall.Function != nil {
		return toolCall.Function.Arguments
	}
	if toolCall.Custom != nil {
		return toolCall.Custom.Input
	}
	return ""
}

func customToolInputValue(block *openaiStreamingToolCall) string {
	if block.customInput == nil {
		return ""
	}
	var args map[string]json.RawMessage
	if err := jsonUnmarshalStrict(block.call.Arguments, &args); err == nil {
		if raw, ok := args[block.customInput.property]; ok {
			var s string
			if jsonUnmarshalStrict(raw, &s) == nil {
				return s
			}
		}
	}
	return ""
}

func outputIndexForCall(output *AssistantMessage, block *openaiStreamingToolCall) int {
	for i, content := range output.Content {
		if tc, ok := content.(ToolCall); ok && tc.ID == block.call.ID && tc.Name == block.call.Name {
			return i
		}
	}
	// Match by reference from the last known output push: fall back to the
	// last toolcall entry.
	for i := len(output.Content) - 1; i >= 0; i-- {
		if _, ok := output.Content[i].(ToolCall); ok {
			return i
		}
	}
	return -1
}

func findStreaming(byIndex map[int]*openaiStreamingToolCall, byID map[string]*openaiStreamingToolCall, call ToolCall) *openaiStreamingToolCall {
	for _, block := range byIndex {
		if block.call.ID == call.ID {
			return block
		}
	}
	return byID[call.ID]
}

// appendOpenAIReasoningDetail merges consecutive reasoning deltas
// (port of appendOpenAIReasoningDetail).
func appendOpenAIReasoningDetail(details []json.RawMessage, detail json.RawMessage) []json.RawMessage {
	if len(details) == 0 {
		return append(details, detail)
	}
	last := details[len(details)-1]
	var lastObj, newObj map[string]json.RawMessage
	_ = jsonUnmarshalStrict(last, &lastObj)
	_ = jsonUnmarshalStrict(detail, &newObj)
	lastType, newType := rawString(lastObj["type"]), rawString(newObj["type"])
	if newType == "reasoning.text" && lastType == "reasoning.text" {
		var lastText, newText string
		_ = jsonUnmarshalStrict(lastObj["text"], &lastText)
		_ = jsonUnmarshalStrict(newObj["text"], &newText)
		merged := lastText + newText
		lastObj["text"] = mustMarshalJSON(merged)
		if rawString(lastObj["signature"]) == "" {
			if sig, ok := newObj["signature"]; ok {
				lastObj["signature"] = sig
			}
		}
		fillMissingCommonReasoningDetailFields(lastObj, newObj)
		details[len(details)-1] = mustMarshalJSON(lastObj)
		return details
	}
	if newType == "reasoning.summary" && lastType == "reasoning.summary" {
		var lastSummary, newSummary string
		_ = jsonUnmarshalStrict(lastObj["summary"], &lastSummary)
		_ = jsonUnmarshalStrict(newObj["summary"], &newSummary)
		lastObj["summary"] = mustMarshalJSON(lastSummary + newSummary)
		fillMissingCommonReasoningDetailFields(lastObj, newObj)
		details[len(details)-1] = mustMarshalJSON(lastObj)
		return details
	}
	return append(details, detail)
}

func fillMissingCommonReasoningDetailFields(target, source map[string]json.RawMessage) {
	if _, ok := target["id"]; !ok {
		if v, ok := source["id"]; ok {
			target["id"] = v
		}
	}
	if rawString(target["format"]) == "" {
		if v, ok := source["format"]; ok {
			target["format"] = v
		}
	}
	if _, ok := target["index"]; !ok {
		if v, ok := source["index"]; ok {
			target["index"] = v
		}
	}
}

func rawString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if jsonUnmarshalStrict(raw, &s) == nil {
		return s
	}
	return ""
}

// getClientAPIKey resolves the request key (port of getClientApiKey):
// header-owned auth substitutes "unused".
func getClientAPIKey(provider, apiKey string, headers ProviderHeaders) (string, error) {
	if apiKey != "" {
		return apiKey, nil
	}
	for name, value := range headers {
		lower := strings.ToLower(name)
		if (lower == "authorization" || lower == "cf-aig-authorization") && value != nil && strings.TrimSpace(*value) != "" {
			return "unused", nil
		}
	}
	return "", fmt.Errorf("No API key for provider: %s", provider)
}

// StreamOpenAICompletionsSimple maps reasoning through the model's thinking
// level clamp (port of openai-completions streamSimple).
func StreamOpenAICompletionsSimple(model *Model, context TranscriptContext, options *SimpleStreamOptions) *AssistantMessageEventStream {
	if options == nil {
		options = &SimpleStreamOptions{}
	}
	stream := NewAssistantMessageEventStream()
	if _, err := getClientAPIKey(model.Provider, options.APIKey, options.Headers); err != nil {
		go func() {
			msgText := err.Error()
			msg := &AssistantMessage{
				API: model.API, Provider: model.Provider, Model: model.ID,
				Usage: Usage{Cost: UsageCost{}}, StopReason: StopError,
				ErrorMessage: &msgText, Timestamp: time.Now().UnixMilli(),
			}
			stream.Push(AssistantMessageEvent{Type: EventError, Reason: StopError, Error: msg})
			stream.End(&msg)
		}()
		return stream
	}

	go func() {
		completionsOptions := &OpenAICompletionsOptions{
			StreamOptions:   options.StreamOptions,
			ReasoningEffort: options.Reasoning,
			ThinkingBudgets: options.ThinkingBudgets,
		}
		if options.ToolChoice != nil {
			completionsOptions.ToolChoice = mustMarshalJSON(*options.ToolChoice)
		}
		if options.Reasoning != "" {
			clamped := ClampThinkingLevel(model, options.Reasoning)
			if clamped == ThinkOff {
				completionsOptions.ReasoningEffort = ""
			} else {
				completionsOptions.ReasoningEffort = clamped
			}
		}
		inner := StreamOpenAICompletions(model, context, completionsOptions)
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
