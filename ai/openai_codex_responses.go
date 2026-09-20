package ai

import (
	"bytes"
	ctxpkg "context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Port of api/openai-codex-responses.ts (the SSE transport). The WebSocket
// transport upstream prefers is not ported: `transport: "websocket"` is
// rejected explicitly, while the default "auto" uses the SSE path (upstream
// falls back to SSE on transport failures too).
//
// D28 records the WebSocket omission.

// Codex defaults.
const (
	defaultCodexBaseURL       = "https://chatgpt.com/backend-api"
	codexDefaultMaxRetries    = 0
	codexBaseDelayMS          = 1000
	codexMaxRetryDelayMS      = 60_000
	codexToolCallProvidersKey = "openai-codex"
)

// codexToolCallProviders accepts the `call_id|item_id` tool-call id form.
var codexToolCallProviders = map[string]bool{
	"openai": true, "openai-codex": true, "opencode": true,
}

// codexResponseStatuses are the statuses the Codex backend reports.
var codexResponseStatuses = map[string]bool{
	"completed": true, "incomplete": true, "failed": true,
	"cancelled": true, "queued": true, "in_progress": true,
}

// OpenAICodexResponsesOptions are the Codex Responses options.
type OpenAICodexResponsesOptions struct {
	StreamOptions
	ReasoningEffort  ThinkingLevel // "none" | "minimal" | ... | "max"
	ReasoningSummary string        // "auto" | "concise" | "detailed" | "off" | "on" | ""
	ServiceTier      string
	TextVerbosity    string // "low" | "medium" | "high"
	ToolChoice       string // "auto" | "none" | "required"
	// Transport is "auto" (default) or "sse". "websocket" is unsupported.
	Transport string
}

// ExtractCodexAccountID reads the ChatGPT account id from an access token
// (upstream extractAccountId).
func ExtractCodexAccountID(token string) (string, error) {
	payload := decodeJWT(token)
	if payload == nil {
		return "", fmt.Errorf("Failed to extract accountId from token")
	}
	auth, ok := payload[OpenAICodexJWTPath].(map[string]any)
	if !ok {
		return "", fmt.Errorf("Failed to extract accountId from token")
	}
	accountID, _ := auth["chatgpt_account_id"].(string)
	if accountID == "" {
		return "", fmt.Errorf("Failed to extract accountId from token")
	}
	return accountID, nil
}

// codexTerminalRateLimitPattern matches usage-limit errors that must not retry.
var codexTerminalRateLimitPattern = regexp.MustCompile(
	`(?i)GoUsageLimitError|FreeUsageLimitError|Monthly usage limit reached|available balance|insufficient_quota|out of budget|quota exceeded|billing`)

// codexRetryableTextPattern matches transient failures worth retrying.
var codexRetryableTextPattern = regexp.MustCompile(
	`(?i)rate.?limit|overloaded|service.?unavailable|upstream.?connect|connection.?refused`)

// IsCodexTerminalRateLimitError reports whether a 429 is a terminal quota error.
func IsCodexTerminalRateLimitError(errorText string) bool {
	return codexTerminalRateLimitPattern.MatchString(errorText)
}

// IsCodexRetryableError reports whether a failure is retryable
// (upstream isRetryableError).
func IsCodexRetryableError(status int, errorText string) bool {
	if status == http.StatusTooManyRequests && IsCodexTerminalRateLimitError(errorText) {
		return false
	}
	switch status {
	case http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusBadGateway,
		http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	}
	return codexRetryableTextPattern.MatchString(errorText)
}

// GetCodexRetryAfterDelayMS reads retry-after-ms/retry-after headers.
func GetCodexRetryAfterDelayMS(headers http.Header) (time.Duration, bool) {
	if millis := headers.Get("retry-after-ms"); millis != "" {
		if value, err := strconv.ParseFloat(millis, 64); err == nil {
			if value < 0 {
				value = 0
			}
			return time.Duration(value) * time.Millisecond, true
		}
	}
	retryAfter := headers.Get("retry-after")
	if retryAfter == "" {
		return 0, false
	}
	if seconds, err := strconv.ParseFloat(retryAfter, 64); err == nil {
		if seconds < 0 {
			seconds = 0
		}
		return time.Duration(seconds * float64(time.Second)), true
	}
	if when, err := http.ParseTime(retryAfter); err == nil {
		delay := time.Until(when)
		if delay < 0 {
			delay = 0
		}
		return delay, true
	}
	return 0, false
}

// ValidateCodexRetryDelay bounds a server-requested retry delay.
func ValidateCodexRetryDelay(delay time.Duration, options *OpenAICodexResponsesOptions) (time.Duration, error) {
	maxDelay := int64(codexMaxRetryDelayMS)
	if options != nil && options.MaxRetryDelayMs != nil {
		maxDelay = int64(*options.MaxRetryDelayMs)
	}
	if maxDelay > 0 && delay.Milliseconds() > maxDelay {
		return 0, fmt.Errorf("Server requested %ds retry delay (max: %ds)",
			int((delay.Milliseconds()+999)/1000), int((maxDelay+999)/1000))
	}
	return delay, nil
}

// ResolveCodexURL builds the Codex responses endpoint (upstream
// resolveCodexUrl).
func ResolveCodexURL(baseURL string) string {
	raw := strings.TrimSpace(baseURL)
	if raw == "" {
		raw = defaultCodexBaseURL
	}
	normalized := strings.TrimRight(raw, "/")
	switch {
	case strings.HasSuffix(normalized, "/codex/responses"):
		return normalized
	case strings.HasSuffix(normalized, "/codex"):
		return normalized + "/responses"
	default:
		return normalized + "/codex/responses"
	}
}

// ResolveCodexWebSocketURL converts the responses URL to a websocket URL
// (upstream resolveCodexWebSocketUrl).
func ResolveCodexWebSocketURL(baseURL string) string {
	parsed, err := url.Parse(ResolveCodexURL(baseURL))
	if err != nil {
		return ResolveCodexURL(baseURL)
	}
	switch parsed.Scheme {
	case "https":
		parsed.Scheme = "wss"
	case "http":
		parsed.Scheme = "ws"
	}
	return parsed.String()
}

// BuildCodexBaseHeaders applies the shared Codex auth headers
// (upstream buildBaseCodexHeaders).
func BuildCodexBaseHeaders(initHeaders map[string]string, additionalHeaders ProviderHeaders, accountID, token string) http.Header {
	headers := http.Header{}
	for name, value := range initHeaders {
		headers.Set(name, value)
	}
	for name, value := range additionalHeaders {
		if value == nil {
			headers.Del(name)
			continue
		}
		headers.Set(name, *value)
	}
	headers.Set("Authorization", "Bearer "+token)
	headers.Set("chatgpt-account-id", accountID)
	headers.Set("originator", "pi")
	headers.Set("User-Agent", GetPiUserAgent())
	return headers
}

// BuildCodexSSEHeaders builds the SSE request headers (upstream
// buildSSEHeaders).
func BuildCodexSSEHeaders(initHeaders map[string]string, additionalHeaders ProviderHeaders, accountID, token, sessionID string) http.Header {
	headers := BuildCodexBaseHeaders(initHeaders, additionalHeaders, accountID, token)
	headers.Set("OpenAI-Beta", "responses=experimental")
	headers.Set("accept", "text/event-stream")
	headers.Set("content-type", "application/json")
	if sessionID != "" {
		headers.Set("session-id", sessionID)
		headers.Set("x-client-request-id", sessionID)
	}
	return headers
}

// CodexErrorResponse is a parsed Codex failure body (upstream
// parseErrorResponse).
type CodexErrorResponse struct {
	Message         string
	FriendlyMessage string
}

// ParseCodexErrorResponse parses a failure body, producing the usage-limit
// friendly message when applicable.
func ParseCodexErrorResponse(status int, statusText, raw string) CodexErrorResponse {
	message := raw
	if message == "" {
		message = statusText
	}
	if message == "" {
		message = "Request failed"
	}
	friendly := ""
	var payload struct {
		Error *struct {
			Code     string `json:"code"`
			Type     string `json:"type"`
			Message  string `json:"message"`
			PlanType string `json:"plan_type"`
			ResetsAt *int64 `json:"resets_at"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err == nil && payload.Error != nil {
		err := payload.Error
		code := err.Code
		if code == "" {
			code = err.Type
		}
		if codexUsageLimitPattern.MatchString(code) || status == http.StatusTooManyRequests {
			plan := ""
			if err.PlanType != "" {
				plan = " (" + strings.ToLower(err.PlanType) + " plan)"
			}
			when := ""
			if err.ResetsAt != nil {
				mins := (*err.ResetsAt*1000 - time.Now().UnixMilli()) / 60000
				if mins < 0 {
					mins = 0
				}
				when = fmt.Sprintf(" Try again in ~%d min.", mins)
			}
			friendly = strings.TrimSpace(fmt.Sprintf("You have hit your ChatGPT usage limit%s.%s", plan, when))
		}
		if err.Message != "" {
			message = err.Message
		} else if friendly != "" {
			message = friendly
		}
	}
	return CodexErrorResponse{Message: message, FriendlyMessage: friendly}
}

// codexUsageLimitPattern matches the usage-limit error codes.
var codexUsageLimitPattern = regexp.MustCompile(`(?i)usage_limit_reached|usage_not_included|rate_limit_exceeded`)

// CodexServiceTierMultiplier is the cost multiplier for a service tier.
func CodexServiceTierMultiplier(model *Model, serviceTier string) float64 {
	switch serviceTier {
	case "flex":
		return 0.5
	case "priority":
		if model != nil && model.ID == "gpt-5.5" {
			return 2.5
		}
		return 2
	default:
		return 1
	}
}

// ResolveCodexServiceTier prefers an explicit flex/priority request when the
// response reports "default" (upstream resolveCodexServiceTier).
func ResolveCodexServiceTier(responseTier *string, requestTier string) string {
	if responseTier != nil && *responseTier == "default" && (requestTier == "flex" || requestTier == "priority") {
		return requestTier
	}
	if responseTier != nil {
		return *responseTier
	}
	return requestTier
}

// BuildCodexRequestBody renders the Codex request body (upstream
// buildRequestBody).
func BuildCodexRequestBody(model *Model, context TranscriptContext, options *OpenAICodexResponsesOptions, cacheSessionID string, grammarToolInputProperties map[string]string) (map[string]any, error) {
	if options == nil {
		options = &OpenAICodexResponsesOptions{}
	}
	compat := GetOpenAIResponsesCompat(model)
	supportsStrictMode := compat.SupportsStrictMode
	supportsAdditionalTools := compat.SupportsAdditionalTools
	supportsToolSearch := compat.SupportsToolSearch
	transcriptTools := ResolveTranscriptTools(context.Messages, supportsAdditionalTools || supportsToolSearch)
	if grammarToolInputProperties == nil {
		grammarToolInputProperties = CreateGrammarToolInputProperties(
			GetDeclaredTools(context.Messages), compat.SupportsOpenAIGrammarTools)
	}
	messages, err := ConvertResponsesMessages(model, context, &ConvertResponsesMessagesOptions{
		IncludeSystemPrompt:            boolPtrFalse(),
		GrammarToolInputProperties:     grammarToolInputProperties,
		SupportsMidConvoSystemMessages: compat.SupportsMidConvoSystemMessages,
		SupportsAdditionalTools:        supportsAdditionalTools,
		SupportsToolSearch:             supportsToolSearch,
		ToolCallProviders:              codexToolCallProviders,
		ToolOptions: &ConvertResponsesToolsOptions{
			Strict:                     nil,
			SupportsStrictMode:         supportsStrictMode,
			SupportsOpenAIGrammarTools: compat.SupportsOpenAIGrammarTools,
		},
	})
	if err != nil {
		return nil, err
	}

	instructions := "You are a helpful assistant."
	if initialSystemMessage := GetInitialSystemMessage(context.Messages); initialSystemMessage != nil {
		if text := GetSystemMessageText(initialSystemMessage); text != "" {
			instructions = text
		}
	}
	verbosity := options.TextVerbosity
	if verbosity == "" {
		verbosity = "low"
	}
	toolChoice := options.ToolChoice
	if toolChoice == "" {
		toolChoice = "auto"
	}
	body := map[string]any{
		"model":               model.ID,
		"store":               false,
		"stream":              true,
		"instructions":        instructions,
		"input":               messages,
		"text":                map[string]any{"verbosity": verbosity},
		"include":             []any{"reasoning.encrypted_content"},
		"tool_choice":         toolChoice,
		"parallel_tool_calls": true,
	}
	if cacheSessionID != "" {
		body["prompt_cache_key"] = cacheSessionID
	}
	if options.Temperature != nil {
		body["temperature"] = *options.Temperature
	}
	if options.ServiceTier != "" {
		body["service_tier"] = options.ServiceTier
	}
	if len(transcriptTools.RequestTools) > 0 {
		converted, err := ConvertResponsesTools(transcriptTools.RequestTools, &ConvertResponsesToolsOptions{
			Strict:                     nil,
			SupportsStrictMode:         supportsStrictMode,
			SupportsOpenAIGrammarTools: compat.SupportsOpenAIGrammarTools,
		})
		if err != nil {
			return nil, err
		}
		body["tools"] = converted
	}

	if options.ReasoningEffort != "" {
		effort := string(options.ReasoningEffort)
		if options.ReasoningEffort == ThinkOff {
			if mapped, ok := model.ThinkingLevelMap[ThinkOff]; ok {
				if mapped == nil {
					// A null mapping disables reasoning entirely.
					return body, nil
				}
				effort = *mapped
			}
		} else if mapped, ok := model.ThinkingLevelMap[options.ReasoningEffort]; ok && mapped != nil {
			effort = *mapped
		}
		summary := options.ReasoningSummary
		if summary == "" {
			summary = "auto"
		}
		body["reasoning"] = map[string]any{"effort": effort, "summary": summary}
	} else if model.Reasoning {
		if mapped, ok := model.ThinkingLevelMap[ThinkOff]; !ok || mapped != nil {
			effort := "none"
			if ok && mapped != nil {
				effort = *mapped
			}
			body["reasoning"] = map[string]any{"effort": effort}
		}
	}
	return body, nil
}

func boolPtrFalse() *bool { value := false; return &value }

// mapCodexSSEEvents translates the Codex event stream into Responses events
// (upstream mapCodexEvents). Events are patched as raw JSON so fields the typed
// subset does not model survive for the shared processor.
func mapCodexSSEEvents(body io.Reader, output *AssistantMessage) ([]byte, error) {
	var buffer bytes.Buffer
	writeEvent := func(value map[string]any) {
		encoded, err := MarshalJSON(value)
		if err != nil {
			return
		}
		buffer.WriteString("data: ")
		buffer.Write(encoded)
		buffer.WriteString("\n\n")
	}
	err := iterateResponsesEvents(ctxpkg.Background(), body, func(event *responsesStreamEvent) {
		decoded := map[string]any{}
		if len(event.Raw) > 0 {
			_ = json.Unmarshal(event.Raw, &decoded)
		}
		switch event.Type {
		case "error":
			code, message := extractCodexEventError(event.Raw)
			text := message
			if text == "" {
				text = code
			}
			if text == "" {
				text = string(event.Raw)
			}
			writeEvent(map[string]any{
				"type": "response.failed",
				"response": map[string]any{
					"status": "failed",
					"error":  map[string]any{"code": code, "message": "Codex error: " + text},
				},
			})
		case "response.failed":
			response, _ := decoded["response"].(map[string]any)
			code, message := "", ""
			if errorValue, ok := response["error"].(map[string]any); ok {
				code, _ = errorValue["code"].(string)
				message, _ = errorValue["message"].(string)
			}
			if message == "" {
				message = "Codex response failed"
			}
			writeEvent(map[string]any{
				"type": "response.failed",
				"response": map[string]any{
					"status": "failed",
					"error":  map[string]any{"code": code, "message": message},
				},
			})
		case "response.done", "response.completed", "response.incomplete":
			response, _ := decoded["response"].(map[string]any)
			if response == nil {
				response = map[string]any{}
			}
			if endTurn, ok := response["end_turn"].(bool); ok {
				value := endTurn
				output.EndTurn = &value
			}
			// The status is normalized to the Codex vocabulary and every
			// terminal variant becomes response.completed.
			status, _ := response["status"].(string)
			if !codexResponseStatuses[status] {
				status = "completed"
			}
			response["status"] = status
			decoded["type"] = "response.completed"
			decoded["response"] = response
			writeEvent(decoded)
		default:
			if len(event.Raw) > 0 {
				buffer.WriteString("data: ")
				buffer.Write(event.Raw)
				buffer.WriteString("\n\n")
			}
		}
	})
	if err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

// extractCodexEventError reads a code/message from an error event
// (upstream extractCodexEventError).
func extractCodexEventError(raw json.RawMessage) (string, string) {
	var payload struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Error   *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return "", ""
	}
	code, message := payload.Code, payload.Message
	if payload.Error != nil {
		if code == "" {
			code = payload.Error.Code
		}
		if message == "" {
			message = payload.Error.Message
		}
	}
	return code, message
}

// StreamOpenAICodexResponses streams a Codex request (upstream stream).
func StreamOpenAICodexResponses(model *Model, context TranscriptContext, options *OpenAICodexResponsesOptions) *AssistantMessageEventStream {
	stream := NewAssistantMessageEventStream()
	compat := GetOpenAIResponsesCompat(model)
	normalizedContext := ResolveTranscript(context, compat.SupportsMidConvoSystemMessages)

	go func() {
		ctx := bgCtx()
		if options != nil && options.Ctx != nil {
			ctx = options.Ctx
		}
		if options == nil {
			options = &OpenAICodexResponsesOptions{}
		}
		output := &AssistantMessage{
			API: APIOpenAICodexResponses, Provider: model.Provider, Model: model.ID,
			Usage: Usage{Cost: UsageCost{}}, StopReason: StopPending,
			Timestamp: time.Now().UnixMilli(),
		}
		fail := func(err error) {
			for index, block := range output.Content {
				if call, ok := block.(ToolCall); ok {
					call.Arguments = parseStreamingArgs(call.Arguments)
					output.Content[index] = call
				}
			}
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

		if options.Transport == "websocket" {
			fail(fmt.Errorf("The openai-codex WebSocket transport is not supported by the Go port (D28); use transport \"sse\""))
			return
		}
		if options.APIKey == "" {
			fail(fmt.Errorf("No API key for provider: %s", model.Provider))
			return
		}
		accountID, err := ExtractCodexAccountID(options.APIKey)
		if err != nil {
			fail(err)
			return
		}
		grammarToolInputProperties := CreateGrammarToolInputProperties(
			GetDeclaredTools(normalizedContext.Messages), compat.SupportsOpenAIGrammarTools)
		cacheSessionID := ""
		if options.CacheRetention != CacheRetentionNone {
			cacheSessionID = ClampOpenAIPromptCacheKey(options.SessionID)
		}
		body, err := BuildCodexRequestBody(model, normalizedContext, options, cacheSessionID, grammarToolInputProperties)
		if err != nil {
			fail(err)
			return
		}
		if options.OnPayload != nil {
			if next := options.OnPayload(mustMarshalJSON(body), model); next != nil {
				var replaced map[string]any
				if perr := json.Unmarshal(next, &replaced); perr == nil {
					body = replaced
				}
			}
		}
		bodyJSON, err := MarshalJSON(body)
		if err != nil {
			fail(err)
			return
		}
		headers := BuildCodexSSEHeaders(model.Headers, options.Headers, accountID, options.APIKey, cacheSessionID)

		var httpTimeout time.Duration
		if options.TimeoutMs != nil && *options.TimeoutMs > 0 {
			httpTimeout = time.Duration(*options.TimeoutMs) * time.Millisecond
		}
		maxRetries := codexDefaultMaxRetries
		if options.MaxRetries != nil {
			maxRetries = *options.MaxRetries
		}

		var response *http.Response
		var lastErr error
		for attempt := 0; attempt <= maxRetries; attempt++ {
			if ctxErr(ctx) != nil {
				fail(fmt.Errorf("Request was aborted"))
				return
			}
			requestCtx := ctx
			cancel := func() {}
			if httpTimeout > 0 {
				requestCtx, cancel = ctxpkg.WithTimeout(ctx, httpTimeout)
			}
			request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, ResolveCodexURL(model.BaseURL), bytes.NewReader(bodyJSON))
			if err != nil {
				cancel()
				fail(err)
				return
			}
			request.Header = headers.Clone()
			response, err = http.DefaultClient.Do(request)
			cancel()
			if err != nil {
				if httpTimeout > 0 && ctxErr(ctx) == nil {
					fail(fmt.Errorf("Codex SSE response headers timed out after %dms", httpTimeout.Milliseconds()))
					return
				}
				if ctxErr(ctx) != nil {
					fail(fmt.Errorf("Request was aborted"))
					return
				}
				lastErr = err
				if attempt < maxRetries {
					if err := AbortableSleep(ctx, time.Duration(codexBaseDelayMS*(1<<attempt))*time.Millisecond, "Request was aborted"); err != nil {
						fail(err)
						return
					}
					continue
				}
				fail(lastErr)
				return
			}
			if options.OnResponse != nil {
				responseHeaders := map[string]string{}
				for name, values := range response.Header {
					responseHeaders[strings.ToLower(name)] = strings.Join(values, ", ")
				}
				options.OnResponse(ProviderResponse{Status: response.StatusCode, Headers: responseHeaders}, model)
			}
			if response.StatusCode < 400 {
				break
			}
			raw, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
			response.Body.Close()
			errorText := string(raw)
			if attempt < maxRetries && IsCodexRetryableError(response.StatusCode, errorText) {
				delay := time.Duration(codexBaseDelayMS*(1<<attempt)) * time.Millisecond
				if retryAfter, ok := GetCodexRetryAfterDelayMS(response.Header); ok {
					bounded, berr := ValidateCodexRetryDelay(retryAfter, options)
					if berr != nil {
						fail(berr)
						return
					}
					delay = bounded
				}
				if err := AbortableSleep(ctx, delay, "Request was aborted"); err != nil {
					fail(err)
					return
				}
				continue
			}
			info := ParseCodexErrorResponse(response.StatusCode, http.StatusText(response.StatusCode), errorText)
			if info.FriendlyMessage != "" {
				fail(fmt.Errorf("%s", info.FriendlyMessage))
				return
			}
			fail(fmt.Errorf("%s", info.Message))
			return
		}
		if response == nil || response.StatusCode >= 400 {
			if lastErr != nil {
				fail(lastErr)
				return
			}
			fail(fmt.Errorf("Failed after retries"))
			return
		}
		defer response.Body.Close()

		stream.Push(AssistantMessageEvent{Type: EventStart, Partial: output})
		normalized, err := mapCodexSSEEvents(response.Body, output)
		if err != nil {
			fail(err)
			return
		}
		processingOptions := &OpenAIResponsesOptions{
			StreamOptions: options.StreamOptions,
			ServiceTier:   options.ServiceTier,
			ResolveServiceTier: func(responseTier *string, requestTier string) string {
				return ResolveCodexServiceTier(responseTier, requestTier)
			},
		}
		if err := ProcessResponsesStream(ctx, bytes.NewReader(normalized), output, stream, model, processingOptions, grammarToolInputProperties); err != nil {
			fail(err)
			return
		}
		if ctxErr(ctx) != nil {
			fail(fmt.Errorf("Request was aborted"))
			return
		}
		if output.StopReason == StopPending {
			fail(fmt.Errorf("Codex stream ended without a stop reason"))
			return
		}
		if output.StopReason == StopError || output.StopReason == StopAborted {
			message := "An unknown error occurred"
			if output.ErrorMessage != nil {
				message = *output.ErrorMessage
			}
			fail(fmt.Errorf("%s", message))
			return
		}
		stream.Push(AssistantMessageEvent{Type: EventDone, Reason: output.StopReason, Message: output})
		stream.End(&output)
	}()
	return stream
}

// StreamOpenAICodexResponsesSimple maps simple options onto Codex options
// (upstream streamSimple).
func StreamOpenAICodexResponsesSimple(model *Model, context TranscriptContext, options *SimpleStreamOptions) *AssistantMessageEventStream {
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
		codexOptions := &OpenAICodexResponsesOptions{StreamOptions: options.StreamOptions}
		if options.ToolChoice != nil {
			codexOptions.ToolChoice = string(*options.ToolChoice)
		}
		if options.Reasoning != "" {
			clamped := ClampThinkingLevel(model, options.Reasoning)
			if clamped != ThinkOff {
				codexOptions.ReasoningEffort = clamped
			}
		}
		forwardStream(stream, StreamOpenAICodexResponses(model, context, codexOptions))
	}()
	return stream
}

// CodexStreams adapts the Codex Responses implementation.
type CodexStreams struct{}

func (CodexStreams) Stream(model *Model, context TranscriptContext, options *StreamOptions) *AssistantMessageEventStream {
	return StreamOpenAICodexResponses(model, context, &OpenAICodexResponsesOptions{StreamOptions: derefStreamOptionsForCodex(options)})
}

func (CodexStreams) StreamSimple(model *Model, context TranscriptContext, options *SimpleStreamOptions) *AssistantMessageEventStream {
	return StreamOpenAICodexResponsesSimple(model, context, options)
}

func derefStreamOptionsForCodex(options *StreamOptions) StreamOptions {
	if options == nil {
		return StreamOptions{}
	}
	return *options
}

// OpenAICodexProvider builds the built-in Codex provider (upstream
// openaiCodexProvider).
func OpenAICodexProvider() *Provider {
	return CreateProvider(CreateProviderOptions{
		ID:      "openai-codex",
		Name:    "OpenAI Codex",
		BaseURL: defaultCodexBaseURL,
		Auth:    ProviderAuth{OAuth: OpenAICodexOAuth()},
		Models:  GetBuiltinModels("openai-codex"),
		Single:  CodexStreams{},
	})
}
