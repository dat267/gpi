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
	"sync"
	"time"
)

// Port of api/anthropic-messages.ts streaming half: SSE decoding, HTTP
// client construction, the stream event loop, and streamSimple.

// ServerSentEvent is one decoded SSE message.
type ServerSentEvent struct {
	Event string
	Data  string
	Raw   []string
}

// SSEDecoder ports upstream's SSE decoder: `event:`/`data:` fields, comment
// lines (leading `:`), CRLF/LF line breaks, multi-line data joined with \n.
type SSEDecoder struct {
	event string
	data  []string
	raw   []string
}

func (d *SSEDecoder) flush() *ServerSentEvent {
	if d.event == "" && len(d.data) == 0 {
		return nil
	}
	event := &ServerSentEvent{
		Event: d.event,
		Data:  strings.Join(d.data, "\n"),
		Raw:   append([]string{}, d.raw...),
	}
	d.event = ""
	d.data = nil
	d.raw = nil
	return event
}

func (d *SSEDecoder) decodeLine(line string) *ServerSentEvent {
	if line == "" {
		return d.flush()
	}
	d.raw = append(d.raw, line)
	if strings.HasPrefix(line, ":") {
		return nil
	}
	delimiterIndex := strings.Index(line, ":")
	fieldName := line
	value := ""
	if delimiterIndex == -1 {
		fieldName = line
	} else {
		fieldName = line[:delimiterIndex]
		value = line[delimiterIndex+1:]
		if strings.HasPrefix(value, " ") {
			value = value[1:]
		}
	}
	switch fieldName {
	case "event":
		d.event = value
	case "data":
		d.data = append(d.data, value)
	}
	return nil
}

// anthropicMessageEvents is the set of wire events the stream consumes.
var anthropicMessageEvents = map[string]bool{
	"message_start": true, "message_delta": true, "message_stop": true,
	"content_block_start": true, "content_block_delta": true, "content_block_stop": true,
}

// anthropicStreamEvent is one parsed wire event (subset of fields pi uses).
type anthropicStreamEvent struct {
	Type string `json:"type"`

	// message_start
	Message *anthropicStreamMessageStart `json:"message,omitempty"`
	// content_block_start
	Index        int                          `json:"index"`
	ContentBlock *anthropicStreamContentBlock `json:"content_block,omitempty"`
	// content_block_delta
	Delta *anthropicStreamDelta `json:"delta,omitempty"`
	// message_delta
	InputTransformations []anthropicInputTransformation `json:"input_transformations,omitempty"`
	DeltaStop            *anthropicStreamStopDelta      `json:"-"`
	Usage                *anthropicStreamUsage          `json:"usage,omitempty"`
}

// UnmarshalJSON handles upstream's overlapping "delta" shapes: on
// content_block_delta it is a content delta; on message_delta it carries
// stop_reason/stop_details.
func (e *anthropicStreamEvent) UnmarshalJSON(data []byte) error {
	type wireProbe struct {
		Type                 string                         `json:"type"`
		Message              *anthropicStreamMessageStart   `json:"message,omitempty"`
		Index                int                            `json:"index"`
		ContentBlock         *anthropicStreamContentBlock   `json:"content_block,omitempty"`
		InputTransformations []anthropicInputTransformation `json:"input_transformations,omitempty"`
		Usage                *anthropicStreamUsage          `json:"usage,omitempty"`
	}
	var probe wireProbe
	if err := jsonUnmarshalStrict(data, &probe); err != nil {
		return err
	}
	e.Type = probe.Type
	e.Message = probe.Message
	e.Index = probe.Index
	e.ContentBlock = probe.ContentBlock
	e.InputTransformations = probe.InputTransformations
	e.Usage = probe.Usage
	switch probe.Type {
	case "content_block_delta":
		var d struct {
			Delta *anthropicStreamDelta `json:"delta"`
		}
		if err := jsonUnmarshalStrict(data, &d); err != nil {
			return err
		}
		e.Delta = d.Delta
	case "message_delta":
		var d struct {
			Delta *anthropicStreamStopDelta `json:"delta"`
		}
		if err := jsonUnmarshalStrict(data, &d); err != nil {
			return err
		}
		e.DeltaStop = d.Delta
	}
	return nil
}

type anthropicStreamMessageStart struct {
	ID                   string                         `json:"id"`
	Model                string                         `json:"model"`
	InputTransformations []anthropicInputTransformation `json:"input_transformations,omitempty"`
	Usage                anthropicStreamUsage           `json:"usage"`
}

type anthropicInputTransformation struct {
	Type   string `json:"type,omitempty"`
	Path   string `json:"path,omitempty"`
	Reason string `json:"reason,omitempty"`
}

type anthropicStreamContentBlock struct {
	Type     string `json:"type"` // text | thinking | redacted_thinking | tool_use | fallback
	Text     string `json:"text,omitempty"`
	Thinking string `json:"thinking,omitempty"`
	// Signature: thinking signature or redacted payload.
	Signature string          `json:"signature,omitempty"`
	Data      string          `json:"data,omitempty"` // redacted_thinking payload
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
}

type anthropicStreamDelta struct {
	Type string `json:"type"` // text_delta | thinking_delta | input_json_delta | signature_delta
	Text string `json:"text,omitempty"`
	// Thinking: thinking_delta text.
	Thinking    string `json:"thinking,omitempty"`
	PartialJSON string `json:"partial_json,omitempty"`
	Signature   string `json:"signature,omitempty"`
}

type anthropicStreamStopDelta struct {
	StopReason  string `json:"stop_reason,omitempty"`
	StopDetails *struct {
		Explanation string `json:"explanation,omitempty"`
	} `json:"stop_details,omitempty"`
}

type anthropicStreamUsage struct {
	InputTokens              *int64 `json:"input_tokens"`
	OutputTokens             *int64 `json:"output_tokens"`
	CacheReadInputTokens     *int64 `json:"cache_read_input_tokens"`
	CacheCreationInputTokens *int64 `json:"cache_creation_input_tokens"`
	CacheCreation            *struct {
		Ephemeral1hInputTokens *int64 `json:"ephemeral_1h_input_tokens"`
	} `json:"cache_creation,omitempty"`
	OutputTokensDetails *struct {
		ThinkingTokens *int64 `json:"thinking_tokens"`
	} `json:"output_tokens_details,omitempty"`
}

// IterateAnthropicEvents decodes the response body into wire events, throwing
// on SSE `error` events and unparsable data, and on a stream that ends before
// message_stop (port of iterateAnthropicEvents). Events are delivered to the
// returned channel; the error is non-nil when iteration terminated early.
func IterateAnthropicEvents(ctx context.Context, body io.Reader, emit func(anthropicStreamEvent)) error {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	decoder := &SSEDecoder{}
	sawMessageStart := false
	sawMessageEnd := false

	send := func(raw string, rawLines []string) error {
		var event anthropicStreamEvent
		if err := ParseJSONWithRepair(raw, &event); err != nil {
			return fmt.Errorf("Could not parse Anthropic SSE event %s: %s; data=%s; raw=%s",
				decoder.event, err.Error(), raw, strings.Join(rawLines, "\\n"))
		}
		switch event.Type {
		case "message_start":
			sawMessageStart = true
		case "message_stop":
			sawMessageEnd = true
		}
		emit(event)
		return nil
	}

	for scanner.Scan() {
		if ctxErr(ctx) != nil {
			return ctxErr(ctx)
		}
		if event := decoder.decodeLine(scanner.Text()); event != nil {
			if event.Event == "error" {
				return fmt.Errorf("%s", event.Data)
			}
			if !anthropicMessageEvents[event.Event] {
				continue
			}
			if err := send(event.Data, event.Raw); err != nil {
				return err
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	// Flush a trailing event without a final blank line.
	if event := decoder.flush(); event != nil {
		if event.Event == "error" {
			return fmt.Errorf("%s", event.Data)
		}
		if anthropicMessageEvents[event.Event] {
			if err := send(event.Data, event.Raw); err != nil {
				return err
			}
		}
	}
	if sawMessageStart && !sawMessageEnd {
		return fmt.Errorf("Anthropic stream ended before message_stop")
	}
	return nil
}

// IsOAuthAnthropicToken detects Claude OAuth tokens (port of isOAuthToken).
func IsOAuthAnthropicToken(apiKey string) bool {
	return strings.Contains(apiKey, "sk-ant-oat")
}

// anthropicClientOptions carry the client construction inputs (port of
// createClient's header assembly).
type anthropicClientOptions struct {
	APIKey         string
	Headers        ProviderHeaders
	SessionID      string
	DynamicHeaders map[string]string
}

// BuildAnthropicRequest builds the HTTP request for the messages API,
// mirroring the pinned @anthropic-ai/sdk: POST {baseURL}/v1/messages?beta=true
// with x-api-key (or Bearer for OAuth tokens), anthropic-version, and the
// merged default headers.
func BuildAnthropicRequest(ctx context.Context, model *Model, params *AnthropicMessageCreateParams, options anthropicClientOptions) (*http.Request, error) {
	compat := GetAnthropicCompat(model)
	isOAuth := IsOAuthAnthropicToken(options.APIKey)

	url := strings.TrimSuffix(model.BaseURL, "/") + "/v1/messages?beta=true"
	body, err := MarshalJSON(params)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("anthropic-dangerous-direct-browser-access", "true")
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("User-Agent", GetPiUserAgent())

	if model.Provider == "github-copilot" {
		// Copilot: Bearer auth.
		req.Header.Set("Authorization", "Bearer "+options.APIKey)
	} else if isOAuth {
		// OAuth: Bearer auth + Claude Code identity headers.
		req.Header.Set("Authorization", "Bearer "+options.APIKey)
		req.Header.Set("user-agent", "claude-cli/"+claudeCodeVersion)
		req.Header.Set("x-app", "cli")
		req.Header.Del("x-api-key")
	} else if options.APIKey != "" {
		req.Header.Set("X-Api-Key", options.APIKey)
	}

	// Session affinity headers for cache routing.
	if options.SessionID != "" && compat.SendSessionAffinityHeaders {
		header := "x-session-affinity"
		if compat.SessionAffinityFormat == "openrouter" {
			header = "x-session-id"
		}
		req.Header.Set(header, options.SessionID)
	}

	applyHeaders := func(headers ProviderHeaders) {
		for name, value := range headers {
			if value == nil {
				req.Header.Del(name)
				continue
			}
			req.Header.Set(name, *value)
		}
	}
	for _, hv := range model.Headers {
		_ = hv
	}
	// Model headers, then dynamic headers, then caller headers (later wins).
	for name, value := range model.Headers {
		req.Header.Set(name, value)
	}
	for name, value := range options.DynamicHeaders {
		req.Header.Set(name, value)
	}
	applyHeaders(options.Headers)
	return req, nil
}

// anthropicStreamBlock is the live per-block accumulator (upstream's Block
// with the partialJson scratch buffer).
type anthropicStreamBlock struct {
	block   Content
	index   int
	partial []byte
}

// StreamAnthropic implements the anthropic-messages stream function
// (port of stream).
func StreamAnthropic(model *Model, context TranscriptContext, options *AnthropicOptions) *AssistantMessageEventStream {
	stream := NewAssistantMessageEventStream()
	normalizedContext := ResolveTranscript(context, GetAnthropicCompat(model).SupportsMidConvoSystemMessages)
	currentTools := GetCurrentTools(normalizedContext.Messages)

	go func() {
		ctx := bgCtx()
		if options != nil && options.Ctx != nil {
			ctx = options.Ctx
		}
		providerThinkingLevel := ""
		if model.Compat != nil && model.Compat.AnthropicMessages != nil &&
			model.Compat.AnthropicMessages.SupportsMidConvoEffort != nil && *model.Compat.AnthropicMessages.SupportsMidConvoEffort {
			providerThinkingLevel = options.Effort
			if providerThinkingLevel == "" {
				providerThinkingLevel = AnthropicEffortHigh
			}
		}
		output := &AssistantMessage{
			API:        model.API,
			Provider:   model.Provider,
			Model:      model.ID,
			Usage:      Usage{Cost: UsageCost{}},
			StopReason: StopPending,
			Timestamp:  time.Now().UnixMilli(),
		}
		if providerThinkingLevel != "" {
			output.ProviderThinkingLevel = &providerThinkingLevel
		}

		finalizeBlocks := func() {
			for i, block := range output.Content {
				switch b := block.(type) {
				case ToolCall:
					// partialJson is a streaming scratch buffer; never persist it.
					b.Arguments = parseStreamingArgs(b.Arguments)
					output.Content[i] = b
				}
			}
		}

		fail := func(err error) {
			finalizeBlocks()
			if ctxErr(ctx) != nil {
				output.StopReason = StopAborted
			} else {
				output.StopReason = StopError
			}
			msg := err.Error()
			output.ErrorMessage = &msg
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

		if options == nil {
			options = &AnthropicOptions{}
		}
		hasAuth := options.APIKey != ""
		if !hasAuth && options.Headers != nil {
			for name, value := range options.Headers {
				lower := strings.ToLower(name)
				if (lower == "authorization" || lower == "x-api-key" || lower == "cf-aig-authorization") &&
					value != nil && strings.TrimSpace(*value) != "" {
					hasAuth = true
				}
			}
		}
		if !hasAuth {
			fail(fmt.Errorf("No API key for provider: %s", model.Provider))
			return
		}
		isOAuth := IsOAuthAnthropicToken(options.APIKey)

		params, err := BuildAnthropicParams(model, normalizedContext, isOAuth, options)
		if err != nil {
			fail(err)
			return
		}
		if options.OnPayload != nil {
			if next := options.OnPayload(mustMarshalJSON(params), model); next != nil {
				var replaced AnthropicMessageCreateParams
				if perr := jsonUnmarshalStrict(next, &replaced); perr == nil {
					replaced.Stream = true
					params = &replaced
				}
			}
		}

		clientOpts := anthropicClientOptions{
			APIKey:    options.APIKey,
			Headers:   options.Headers,
			SessionID: options.SessionID,
		}
		if clientOpts.SessionID != "" && ResolveCacheRetention(options.CacheRetention, options.Env) == CacheRetentionNone {
			clientOpts.SessionID = ""
		}

		// Retry loop with per-attempt fresh requests.
		var resp *http.Response
		requestErr := func() error {
			var attemptErr error
			resp, attemptErr = RetryProviderRequest(ctx, func() (*http.Response, error) {
				req, berr := BuildAnthropicRequest(ctx, model, params, clientOpts)
				if berr != nil {
					return nil, berr
				}
				hresp, rerr := http.DefaultClient.Do(req)
				if rerr != nil {
					return nil, rerr
				}
				if hresp.StatusCode >= 400 {
					body, _ := io.ReadAll(io.LimitReader(hresp.Body, 1<<20))
					hresp.Body.Close()
					return nil, &ProviderError{
						Status: hresp.StatusCode, Headers: hresp.Header,
						Message: fmt.Sprintf("%d %s: %s", hresp.StatusCode, http.StatusText(hresp.StatusCode), string(body)),
						Body:    string(body),
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

		var blocks []anthropicStreamBlock
		usageModel := model
		var inputTransformations []anthropicInputTransformation

		applyUsage := func(u anthropicStreamUsage, overwrite bool) {
			if overwrite || u.InputTokens != nil {
				if u.InputTokens != nil {
					output.Usage.Input = *u.InputTokens
				}
			}
			if overwrite || u.OutputTokens != nil {
				if u.OutputTokens != nil {
					output.Usage.Output = *u.OutputTokens
				}
			}
			if u.CacheReadInputTokens != nil {
				output.Usage.CacheRead = *u.CacheReadInputTokens
			}
			if u.CacheCreationInputTokens != nil {
				output.Usage.CacheWrite = *u.CacheCreationInputTokens
			}
			if u.CacheCreation != nil && u.CacheCreation.Ephemeral1hInputTokens != nil {
				v := *u.CacheCreation.Ephemeral1hInputTokens
				output.Usage.CacheWrite1h = &v
			}
			if u.OutputTokensDetails != nil && u.OutputTokensDetails.ThinkingTokens != nil {
				reasoning := *u.OutputTokensDetails.ThinkingTokens
				output.Usage.Reasoning = &reasoning
			}
			output.Usage.TotalTokens = output.Usage.Input + output.Usage.Output + output.Usage.CacheRead + output.Usage.CacheWrite
			CalculateCost(usageModel, &output.Usage)
		}

		iterErr := IterateAnthropicEvents(ctx, resp.Body, func(event anthropicStreamEvent) {
			switch event.Type {
			case "message_start":
				output.ResponseID = &event.Message.ID
				if len(event.Message.InputTransformations) > 0 {
					inputTransformations = event.Message.InputTransformations
				}
				responseModel := event.Message.Model
				if responseModel != model.ID {
					output.ResponseModel = &responseModel
					// Server-side fallback pricing when configured.
					if model.Compat != nil && model.Compat.AnthropicMessages != nil {
						for _, fallback := range model.Compat.AnthropicMessages.AllowedFallbackModels {
							if fallback.Provider == model.Provider && fallback.Model == responseModel {
								fallbackModel := *model
								fallbackModel.ID = responseModel
								fallbackModel.Cost = fallback.Cost
								usageModel = &fallbackModel
							}
						}
					}
				}
				applyUsage(event.Message.Usage, true)
			case "content_block_start":
				cb := event.ContentBlock
				if cb.Type == "fallback" {
					if len(output.Content) > 0 {
						panic(fmt.Errorf("Anthropic performed an unsupported mid-output model fallback"))
					}
					return
				}
				switch cb.Type {
				case "text":
					blocks = append(blocks, anthropicStreamBlock{block: TextContent{Text: cb.Text}, index: event.Index})
					output.Content = append(output.Content, TextContent{Text: cb.Text})
					stream.Push(AssistantMessageEvent{Type: EventTextStart, ContentIndex: len(output.Content) - 1, Partial: output})
				case "thinking":
					sig := cb.Signature
					blocks = append(blocks, anthropicStreamBlock{block: ThinkingContent{Thinking: cb.Thinking, ThinkingSignature: &sig}, index: event.Index})
					output.Content = append(output.Content, ThinkingContent{Thinking: cb.Thinking, ThinkingSignature: &sig})
					stream.Push(AssistantMessageEvent{Type: EventThinkingStart, ContentIndex: len(output.Content) - 1, Partial: output})
				case "redacted_thinking":
					sig := cb.Data
					redacted := ThinkingContent{Thinking: "[Reasoning redacted]", ThinkingSignature: &sig, Redacted: true}
					blocks = append(blocks, anthropicStreamBlock{block: redacted, index: event.Index})
					output.Content = append(output.Content, redacted)
					stream.Push(AssistantMessageEvent{Type: EventThinkingStart, ContentIndex: len(output.Content) - 1, Partial: output})
				case "tool_use":
					name := cb.Name
					if isOAuth {
						name = fromClaudeCodeName(name, currentTools)
					}
					input := cb.Input
					if len(input) == 0 {
						input = json.RawMessage("{}")
					}
					call := ToolCall{ID: cb.ID, Name: name, Arguments: input}
					blocks = append(blocks, anthropicStreamBlock{block: call, index: event.Index})
					output.Content = append(output.Content, call)
					stream.Push(AssistantMessageEvent{Type: EventToolcallStart, ContentIndex: len(output.Content) - 1, Partial: output})
				}
			case "content_block_delta":
				idx := findBlock(blocks, event.Index)
				if idx < 0 {
					return
				}
				switch event.Delta.Type {
				case "text_delta":
					if tc, ok := blocks[idx].block.(TextContent); ok {
						tc.Text += event.Delta.Text
						blocks[idx].block = tc
						output.Content[idx] = tc
						stream.Push(AssistantMessageEvent{Type: EventTextDelta, ContentIndex: idx, Delta: event.Delta.Text, Partial: output})
					}
				case "thinking_delta":
					if tc, ok := blocks[idx].block.(ThinkingContent); ok {
						tc.Thinking += event.Delta.Thinking
						blocks[idx].block = tc
						output.Content[idx] = tc
						stream.Push(AssistantMessageEvent{Type: EventThinkingDelta, ContentIndex: idx, Delta: event.Delta.Thinking, Partial: output})
					}
				case "input_json_delta":
					if tc, ok := blocks[idx].block.(ToolCall); ok {
						blocks[idx].partial = append(blocks[idx].partial, event.Delta.PartialJSON...)
						var parsed map[string]any
						parseStreamingJSONInto(string(blocks[idx].partial), &parsed)
						enc, _ := MarshalJSON(parsed)
						tc.Arguments = enc
						output.Content[idx] = tc
						stream.Push(AssistantMessageEvent{Type: EventToolcallDelta, ContentIndex: idx, Delta: event.Delta.PartialJSON, Partial: output})
					}
				case "signature_delta":
					if tc, ok := blocks[idx].block.(ThinkingContent); ok {
						sig := deref(tc.ThinkingSignature)
						if sig == "" {
							sig = event.Delta.Signature
						} else {
							sig += event.Delta.Signature
						}
						tc.ThinkingSignature = &sig
						blocks[idx].block = tc
						output.Content[idx] = tc
					}
				}
			case "content_block_stop":
				idx := findBlock(blocks, event.Index)
				if idx < 0 {
					return
				}
				switch b := blocks[idx].block.(type) {
				case TextContent:
					stream.Push(AssistantMessageEvent{Type: EventTextEnd, ContentIndex: idx, Content: b.Text, Partial: output})
				case ThinkingContent:
					stream.Push(AssistantMessageEvent{Type: EventThinkingEnd, ContentIndex: idx, Content: b.Thinking, Partial: output})
				case ToolCall:
					var parsed map[string]any
					parseStreamingJSONInto(string(blocks[idx].partial), &parsed)
					enc, _ := MarshalJSON(parsed)
					b.Arguments = enc
					blocks[idx].block = b
					output.Content[idx] = b
					stream.Push(AssistantMessageEvent{Type: EventToolcallEnd, ContentIndex: idx, ToolCall: &b, Partial: output})
				}
			case "message_delta":
				if len(event.InputTransformations) > 0 {
					inputTransformations = event.InputTransformations
				}
				if event.DeltaStop != nil && event.DeltaStop.StopReason != "" {
					raw := event.DeltaStop.StopReason
					output.RawStopReason = &raw
					mapped, errMsg, merr := MapAnthropicStopReason(raw, stopDetailsExplanation(event.DeltaStop))
					if merr != nil {
						panic(merr)
					}
					output.StopReason = mapped
					if errMsg != "" {
						output.ErrorMessage = &errMsg
					}
				}
				if event.Usage != nil {
					applyUsage(*event.Usage, false)
				} else {
					output.Usage.TotalTokens = output.Usage.Input + output.Usage.Output + output.Usage.CacheRead + output.Usage.CacheWrite
					CalculateCost(usageModel, &output.Usage)
				}
			}
		})
		if iterErr != nil {
			fail(iterErr)
			return
		}

		if ctxErr(ctx) != nil {
			fail(fmt.Errorf("Request was aborted"))
			return
		}
		if output.StopReason == StopPending {
			fail(fmt.Errorf("Anthropic stream ended without a stop reason"))
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
		if len(inputTransformations) > 0 {
			details, _ := MarshalJSON(map[string]any{"transformations": inputTransformations})
			AppendAssistantMessageDiagnostic(output, CreateAssistantMessageDiagnostic("anthropic_input_transformations", errorNil(), details))
		}

		finalizeBlocks()
		stream.Push(AssistantMessageEvent{Type: EventDone, Reason: output.StopReason, Message: output})
		stream.End(&output)
	}()

	return stream
}

func errorNil() error { return nil }

func stopDetailsExplanation(d *anthropicStreamStopDelta) string {
	if d == nil || d.StopDetails == nil {
		return ""
	}
	return d.StopDetails.Explanation
}

func findBlock(blocks []anthropicStreamBlock, index int) int {
	for i, b := range blocks {
		if b.index == index {
			return i
		}
	}
	return -1
}

// parseStreamingArgs finalizes tool-call arguments from the scratch buffer.
func parseStreamingArgs(args json.RawMessage) json.RawMessage {
	var parsed map[string]any
	parseStreamingJSONInto(string(args), &parsed)
	enc, _ := MarshalJSON(parsed)
	return enc
}

// StreamAnthropicSimple maps simple reasoning levels onto the anthropic
// options (port of streamSimple).
func StreamAnthropicSimple(model *Model, context TranscriptContext, options *SimpleStreamOptions) *AssistantMessageEventStream {
	if options == nil {
		options = &SimpleStreamOptions{}
	}
	// streamSimple throws synchronously when request auth is missing.
	hasAuth := options.APIKey != ""
	if !hasAuth && options.Headers != nil {
		for name, value := range options.Headers {
			lower := strings.ToLower(name)
			if (lower == "authorization" || lower == "x-api-key" || lower == "cf-aig-authorization") &&
				value != nil && strings.TrimSpace(*value) != "" {
				hasAuth = true
			}
		}
	}
	if !hasAuth {
		stream := NewAssistantMessageEventStream()
		go func() {
			err := fmt.Errorf("No API key for provider: %s", model.Provider)
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

	base := AnthropicOptions{StreamOptions: options.StreamOptions}
	if options.ToolChoice != nil {
		base.ToolChoice = mustMarshalJSON(*options.ToolChoice)
	}
	if options.Reasoning == "" {
		off := false
		base.ThinkingEnabled = &off
		return StreamAnthropic(model, context, &base)
	}

	// Adaptive thinking models take an effort level; older models take a
	// budget.
	forcedAdaptive := model.Compat != nil && model.Compat.AnthropicMessages != nil &&
		model.Compat.AnthropicMessages.ForceAdaptiveThinking != nil && *model.Compat.AnthropicMessages.ForceAdaptiveThinking
	if forcedAdaptive {
		effort := mapThinkingLevelToEffort(model, options.Reasoning)
		base.ThinkingEnabled = boolPtr(true)
		base.Effort = effort
		return StreamAnthropic(model, context, &base)
	}

	baseMaxTokens := options.MaxTokens
	maxTokens, thinkingBudget := AdjustMaxTokensForThinking(baseMaxTokens, int(model.MaxTokens), options.Reasoning, options.ThinkingBudgets)
	clamped := ClampMaxTokensToContext(model, context, maxTokens)
	base.MaxTokens = &clamped
	base.ThinkingEnabled = boolPtr(true)
	budget := min(thinkingBudget, max(0, clamped-MinAnswerTokens))
	base.ThinkingBudgetTokens = &budget
	return StreamAnthropic(model, context, &base)
}

// mapThinkingLevelToEffort maps a pi thinking level to an Anthropic effort.
func mapThinkingLevelToEffort(model *Model, level ThinkingLevel) AnthropicEffort {
	if level != "" {
		if mapped, ok := model.ThinkingLevelMap[level]; ok && mapped != nil && *mapped != "" {
			return AnthropicEffort(*mapped)
		}
	}
	switch level {
	case ThinkMinimal, ThinkLow:
		return AnthropicEffortLow
	case ThinkMedium:
		return AnthropicEffortMedium
	case ThinkHigh:
		return AnthropicEffortHigh
	default:
		return AnthropicEffortHigh
	}
}

var _ = sync.Mutex{}
