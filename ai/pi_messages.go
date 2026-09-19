package ai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Port of api/pi-messages.ts: pi's own message protocol. The request is a single
// POST of {model, context, options} to <baseUrl>/messages; the response is an
// SSE stream of serialized assistant-message events plus a terminal
// done/error event.

// PiMessagesOptions are the pi-messages stream options.
type PiMessagesOptions struct {
	StreamOptions
	Reasoning  ThinkingLevel
	ToolChoice json.RawMessage // "auto" | "none" | "required" | {function}
	// Debug asks the backend for debug metadata (e.g. routing headers).
	Debug bool
}

// PiMessagesRewriteImpact summarizes a server-side message rewrite.
type PiMessagesRewriteImpact struct {
	PolicyID            string `json:"policyId"`
	PolicyVersion       int    `json:"policyVersion"`
	Changed             bool   `json:"changed"`
	TokenCountChange    int64  `json:"tokenCountChange"`
	MessageCountChange  int64  `json:"messageCountChange"`
	SystemPromptChanged bool   `json:"systemPromptChanged"`
}

// PiMessagesEvent is one serialized event from a pi-messages backend.
type PiMessagesEvent struct {
	Type          string                   `json:"type"`
	ContentIndex  int                      `json:"contentIndex"`
	Delta         string                   `json:"delta"`
	Content       string                   `json:"content"`
	Signature     *string                  `json:"contentSignature"`
	Redacted      *bool                    `json:"redacted"`
	ID            string                   `json:"id"`
	ToolName      string                   `json:"toolName"`
	ToolCall      json.RawMessage          `json:"toolCall"`
	Reason        string                   `json:"reason"`
	Usage         json.RawMessage          `json:"usage"`
	ResponseID    *string                  `json:"responseId"`
	ThinkingLevel *string                  `json:"providerThinkingLevel"`
	ErrorMessage  *string                  `json:"errorMessage"`
	Rewrite       *PiMessagesRewriteImpact `json:"rewrite"`
}

// PiMessagesResponseError is a non-2xx response failure carrying diagnostic
// details (upstream PiMessagesResponseError).
type PiMessagesResponseError struct {
	Message           string
	Code              string
	DiagnosticDetails json.RawMessage
}

func (e *PiMessagesResponseError) Error() string { return e.Message }

// parsePiMessagesErrorBody extracts the `error` object of an error body.
func parsePiMessagesErrorBody(body string) map[string]any {
	var parsed map[string]any
	if err := json.Unmarshal([]byte(body), &parsed); err != nil {
		return nil
	}
	if _, ok := parsed["error"].(map[string]any); !ok {
		return nil
	}
	return parsed
}

func truncateDiagnosticString(value string) string {
	const maxLength = 8192
	if len(value) > maxLength {
		return value[:maxLength] + "…"
	}
	return value
}

// createPiMessagesResponseError builds the coded error with its diagnostics.
func createPiMessagesResponseError(model *Model, requestURL *url.URL, response *http.Response, body string) *PiMessagesResponseError {
	errorBody := parsePiMessagesErrorBody(body)
	code := ""
	message := ""
	var errorDetails any
	hasError := false
	if errorBody != nil {
		if errorRecord, ok := errorBody["error"].(map[string]any); ok {
			hasError = true
			errorDetails = errorRecord
			if text, ok := errorRecord["message"].(string); ok {
				message = text
			}
			code, _ = errorRecord["code"].(string)
		}
	}
	suffix := message
	if suffix == "" {
		suffix = body
	}
	codeSuffix := ""
	if code != "" {
		codeSuffix = " (" + code + ")"
	}
	formatted := fmt.Sprintf("%d %s: %s%s", response.StatusCode, http.StatusText(response.StatusCode), suffix, codeSuffix)

	details := map[string]any{
		"version":     1,
		"provider":    model.Provider,
		"model":       model.ID,
		"url":         requestURL.String(),
		"status":      response.StatusCode,
		"statusText":  http.StatusText(response.StatusCode),
		"timestampMs": time.Now().UnixMilli(),
	}
	if hasError {
		details["error"] = errorDetails
	} else {
		details["body"] = truncateDiagnosticString(body)
	}
	encoded, _ := MarshalJSON(details)
	return &PiMessagesResponseError{Message: formatted, Code: code, DiagnosticDetails: encoded}
}

// piMessagesConverter folds streamed events into one assistant message
// (upstream createEventConverter).
type piMessagesConverter struct {
	partial  *AssistantMessage
	toolJSON map[int]string
}

func newPiMessagesConverter(model *Model) *piMessagesConverter {
	return &piMessagesConverter{
		partial: &AssistantMessage{
			API: model.API, Provider: model.Provider, Model: model.ID,
			Usage: emptyPiMessagesUsage(), StopReason: StopPending,
			Timestamp: time.Now().UnixMilli(),
		},
		toolJSON: map[int]string{},
	}
}

func emptyPiMessagesUsage() Usage {
	return Usage{Cost: UsageCost{}}
}

// convert maps one wire event to the protocol event (and assembles the partial).
func (c *piMessagesConverter) convert(event *PiMessagesEvent) (AssistantMessageEvent, bool) {
	partial := c.partial
	switch event.Type {
	case "done":
		partial.StopReason = event.Reason
		if len(event.Usage) > 0 {
			_ = json.Unmarshal(event.Usage, &partial.Usage)
		}
		partial.ResponseID = event.ResponseID
		if event.ThinkingLevel != nil {
			partial.ProviderThinkingLevel = event.ThinkingLevel
		}
		appendPiMessagesRewriteDiagnostic(partial, event.Rewrite)
		return AssistantMessageEvent{Type: EventDone, Reason: event.Reason, Message: partial}, true
	case "error":
		partial.StopReason = event.Reason
		if len(event.Usage) > 0 {
			_ = json.Unmarshal(event.Usage, &partial.Usage)
		}
		partial.ErrorMessage = event.ErrorMessage
		partial.ResponseID = event.ResponseID
		if event.ThinkingLevel != nil {
			partial.ProviderThinkingLevel = event.ThinkingLevel
		}
		appendPiMessagesRewriteDiagnostic(partial, event.Rewrite)
		return AssistantMessageEvent{Type: EventError, Reason: event.Reason, Error: partial}, true
	case "start":
		return AssistantMessageEvent{Type: EventStart, Partial: partial}, false
	case "text_start":
		setPiContent(partial, event.ContentIndex, TextContent{Text: ""})
		return AssistantMessageEvent{Type: EventTextStart, ContentIndex: event.ContentIndex, Partial: partial}, false
	case "text_delta":
		if text, ok := getPiContent(partial, event.ContentIndex).(TextContent); ok {
			text.Text += event.Delta
			setPiContent(partial, event.ContentIndex, text)
		}
		return AssistantMessageEvent{Type: EventTextDelta, ContentIndex: event.ContentIndex, Delta: event.Delta, Partial: partial}, false
	case "text_end":
		if text, ok := getPiContent(partial, event.ContentIndex).(TextContent); ok {
			text.Text = event.Content
			text.TextSignature = event.Signature
			setPiContent(partial, event.ContentIndex, text)
		}
		return AssistantMessageEvent{Type: EventTextEnd, ContentIndex: event.ContentIndex, Content: event.Content, Partial: partial}, false
	case "thinking_start":
		setPiContent(partial, event.ContentIndex, ThinkingContent{Thinking: ""})
		return AssistantMessageEvent{Type: EventThinkingStart, ContentIndex: event.ContentIndex, Partial: partial}, false
	case "thinking_delta":
		if thinking, ok := getPiContent(partial, event.ContentIndex).(ThinkingContent); ok {
			thinking.Thinking += event.Delta
			setPiContent(partial, event.ContentIndex, thinking)
		}
		return AssistantMessageEvent{Type: EventThinkingDelta, ContentIndex: event.ContentIndex, Delta: event.Delta, Partial: partial}, false
	case "thinking_end":
		if thinking, ok := getPiContent(partial, event.ContentIndex).(ThinkingContent); ok {
			thinking.Thinking = event.Content
			thinking.ThinkingSignature = event.Signature
			if event.Redacted != nil {
				thinking.Redacted = *event.Redacted
			}
			setPiContent(partial, event.ContentIndex, thinking)
		}
		return AssistantMessageEvent{Type: EventThinkingEnd, ContentIndex: event.ContentIndex, Content: event.Content, Partial: partial}, false
	case "toolcall_start":
		setPiContent(partial, event.ContentIndex, ToolCall{
			ID: event.ID, Name: event.ToolName, Arguments: json.RawMessage(`{}`),
		})
		c.toolJSON[event.ContentIndex] = ""
		return AssistantMessageEvent{Type: EventToolcallStart, ContentIndex: event.ContentIndex, Partial: partial}, false
	case "toolcall_delta":
		raw := c.toolJSON[event.ContentIndex] + event.Delta
		c.toolJSON[event.ContentIndex] = raw
		if call, ok := getPiContent(partial, event.ContentIndex).(ToolCall); ok {
			call.Arguments = parseStreamingArgs(json.RawMessage(raw))
			setPiContent(partial, event.ContentIndex, call)
		}
		return AssistantMessageEvent{Type: EventToolcallDelta, ContentIndex: event.ContentIndex, Delta: event.Delta, Partial: partial}, false
	case "toolcall_end":
		if len(event.ToolCall) > 0 {
			var call ToolCall
			if err := json.Unmarshal(event.ToolCall, &call); err == nil {
				if current, ok := getPiContent(partial, event.ContentIndex).(ToolCall); ok {
					if call.ID == "" {
						call.ID = current.ID
					}
					if call.Name == "" {
						call.Name = current.Name
					}
				}
				setPiContent(partial, event.ContentIndex, call)
			}
		}
		delete(c.toolJSON, event.ContentIndex)
		call, _ := getPiContent(partial, event.ContentIndex).(ToolCall)
		return AssistantMessageEvent{
			Type: EventToolcallEnd, ContentIndex: event.ContentIndex, ToolCall: &call, Partial: partial,
		}, false
	}
	return AssistantMessageEvent{Type: event.Type, Partial: partial}, false
}

func setPiContent(message *AssistantMessage, index int, content Content) {
	for len(message.Content) <= index {
		message.Content = append(message.Content, TextContent{})
	}
	message.Content[index] = content
}

func getPiContent(message *AssistantMessage, index int) Content {
	if index < 0 || index >= len(message.Content) {
		return nil
	}
	return message.Content[index]
}

func appendPiMessagesRewriteDiagnostic(message *AssistantMessage, rewrite *PiMessagesRewriteImpact) {
	if rewrite == nil {
		return
	}
	details, err := MarshalJSON(rewrite)
	if err != nil {
		return
	}
	AppendAssistantMessageDiagnostic(message, CreateAssistantMessageDiagnostic(
		"pi_messages_rewrite", nil, details))
}

// parsePiMessagesEvent decodes one SSE block.
func parsePiMessagesEvent(raw string) (*PiMessagesEvent, error) {
	data := ""
	for _, line := range strings.Split(raw, "\n") {
		if strings.HasPrefix(line, "data:") {
			data = strings.TrimSpace(line[5:])
			break
		}
	}
	if data == "" || data == "[DONE]" {
		return nil, nil
	}
	var event PiMessagesEvent
	if err := json.Unmarshal([]byte(data), &event); err != nil {
		return nil, err
	}
	return &event, nil
}

// createPiMessagesErrorEvent builds the terminal error event.
func createPiMessagesErrorEvent(model *Model, err error, aborted bool) AssistantMessageEvent {
	reason := StopError
	if aborted {
		reason = StopAborted
	}
	message := ""
	if err != nil {
		message = err.Error()
	}
	assistant := &AssistantMessage{
		API: model.API, Provider: model.Provider, Model: model.ID,
		Usage: emptyPiMessagesUsage(), StopReason: reason,
		ErrorMessage: &message, Timestamp: time.Now().UnixMilli(),
	}
	if !aborted {
		if responseErr, ok := err.(*PiMessagesResponseError); ok {
			AppendAssistantMessageDiagnostic(assistant, CreateAssistantMessageDiagnostic(
				"pi_messages_response_failure", responseErr, responseErr.DiagnosticDetails))
		}
	}
	return AssistantMessageEvent{Type: EventError, Reason: reason, Error: assistant}
}

// resolvePiMessagesCacheRetention maps the legacy env opt-in.
func resolvePiMessagesCacheRetention(cacheRetention CacheRetention, env ProviderEnv) CacheRetention {
	if cacheRetention != "" {
		return cacheRetention
	}
	if GetProviderEnvValueOr("PI_CACHE_RETENTION", env) == "long" {
		return CacheRetentionLong
	}
	return ""
}

// StreamPiMessages streams a pi-messages request (upstream stream).
func StreamPiMessages(model *Model, context TranscriptContext, options *PiMessagesOptions) *AssistantMessageEventStream {
	stream := NewAssistantMessageEventStream()
	go func() {
		ctx := bgCtx()
		if options != nil && options.Ctx != nil {
			ctx = options.Ctx
		}
		if options == nil {
			options = &PiMessagesOptions{}
		}
		fail := func(err error) {
			stream.Push(createPiMessagesErrorEvent(model, err, ctxErr(ctx) != nil))
			stream.End(nil)
		}

		if options.APIKey == "" {
			fail(fmt.Errorf("No API key provided for provider %q", model.Provider))
			return
		}
		requestURL, err := url.Parse(strings.TrimRight(model.BaseURL, "/") + "/messages")
		if err != nil {
			fail(err)
			return
		}
		if options.Debug {
			query := requestURL.Query()
			query.Set("debug", "1")
			requestURL.RawQuery = query.Encode()
		}

		payload := map[string]any{
			"model":   model.ID,
			"context": contextJSONValue(context),
			"options": piMessagesOptionsPayload(options),
		}
		if options.OnPayload != nil {
			if next := options.OnPayload(mustMarshalJSON(payload), model); next != nil {
				var replaced map[string]any
				if perr := json.Unmarshal(next, &replaced); perr == nil {
					payload = replaced
				}
			}
		}
		body, err := MarshalJSON(payload)
		if err != nil {
			fail(err)
			return
		}

		request, err := http.NewRequestWithContext(ctx, http.MethodPost, requestURL.String(), bytes.NewReader(body))
		if err != nil {
			fail(err)
			return
		}
		request.Header.Set("authorization", "Bearer "+options.APIKey)
		request.Header.Set("accept", "text/event-stream")
		request.Header.Set("content-type", "application/json")
		for name, value := range options.Headers {
			if value == nil {
				request.Header.Del(name)
				continue
			}
			request.Header.Set(name, *value)
		}
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			fail(err)
			return
		}
		defer response.Body.Close()
		if options.OnResponse != nil {
			headers := map[string]string{}
			for name, values := range response.Header {
				headers[strings.ToLower(name)] = strings.Join(values, ", ")
			}
			options.OnResponse(ProviderResponse{Status: response.StatusCode, Headers: headers}, model)
		}
		if response.StatusCode >= 400 {
			raw, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
			fail(createPiMessagesResponseError(model, requestURL, response, string(raw)))
			return
		}

		converter := newPiMessagesConverter(model)
		terminal := false
		iterErr := readPiMessagesEvents(ctx, response.Body, func(event *PiMessagesEvent) bool {
			converted, isTerminal := converter.convert(event)
			stream.Push(converted)
			if isTerminal {
				terminal = true
				return false
			}
			return true
		})
		if iterErr != nil {
			fail(iterErr)
			return
		}
		if !terminal {
			fail(fmt.Errorf("%s stream ended without a terminal event", model.Provider))
			return
		}
		stream.End(nil)
	}()
	return stream
}

// contextJSONValue renders a transcript context for the request payload. The
// messages go through the canonical message marshaller, so the wire shape
// matches the JSON transcript format (upstream sends the context object).
func contextJSONValue(context TranscriptContext) any {
	messages := make([]any, 0, len(context.Messages))
	for _, message := range context.Messages {
		encoded, err := MarshalMessage(message)
		if err != nil {
			continue
		}
		var value any
		if err := json.Unmarshal(encoded, &value); err != nil {
			continue
		}
		messages = append(messages, value)
	}
	return map[string]any{"messages": messages}
}

// piMessagesOptionsPayload renders the request's options object.
func piMessagesOptionsPayload(options *PiMessagesOptions) map[string]any {
	payload := map[string]any{}
	if options.Temperature != nil {
		payload["temperature"] = *options.Temperature
	}
	if options.MaxTokens != nil {
		payload["maxTokens"] = *options.MaxTokens
	}
	if options.Reasoning != "" {
		payload["reasoning"] = options.Reasoning
	}
	if retention := resolvePiMessagesCacheRetention(options.CacheRetention, options.Env); retention != "" {
		payload["cacheRetention"] = retention
	}
	if options.SessionID != "" {
		payload["sessionId"] = options.SessionID
	}
	if len(options.ToolChoice) > 0 {
		var toolChoice any
		if err := json.Unmarshal(options.ToolChoice, &toolChoice); err == nil {
			payload["toolChoice"] = toolChoice
		}
	}
	return payload
}

// readPiMessagesEvents decodes the SSE body, returning false from emit to stop.
func readPiMessagesEvents(ctx context.Context, body io.Reader, emit func(event *PiMessagesEvent) bool) error {
	reader := bufio.NewReader(body)
	buffer := ""
	handleBlock := func(block string) (bool, error) {
		if strings.TrimSpace(block) == "" {
			return true, nil
		}
		event, err := parsePiMessagesEvent(block)
		if err != nil {
			return false, err
		}
		if event == nil {
			return true, nil
		}
		return emit(event), nil
	}
	for {
		if ctx != nil && ctx.Err() != nil {
			return ctx.Err()
		}
		chunk := make([]byte, 8192)
		read, err := reader.Read(chunk)
		if read > 0 {
			buffer += strings.ReplaceAll(string(chunk[:read]), "\r\n", "\n")
			for {
				split := strings.Index(buffer, "\n\n")
				if split == -1 {
					break
				}
				keepGoing, herr := handleBlock(buffer[:split])
				buffer = buffer[split+2:]
				if herr != nil {
					return herr
				}
				if !keepGoing {
					return nil
				}
			}
		}
		if err != nil {
			if err == io.EOF {
				break
			}
			return err
		}
	}
	if strings.TrimSpace(buffer) != "" {
		if _, err := handleBlock(buffer); err != nil {
			return err
		}
	}
	return nil
}

// StreamPiMessagesSimple maps simple options onto pi-messages options
// (upstream streamSimple).
func StreamPiMessagesSimple(model *Model, context TranscriptContext, options *SimpleStreamOptions) *AssistantMessageEventStream {
	if options == nil {
		options = &SimpleStreamOptions{}
	}
	piOptions := &PiMessagesOptions{StreamOptions: options.StreamOptions, Reasoning: options.Reasoning}
	if options.ToolChoice != nil {
		piOptions.ToolChoice = mustMarshalJSON(*options.ToolChoice)
	}
	return StreamPiMessages(model, context, piOptions)
}
