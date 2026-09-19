package ai

import (
	"bufio"
	"bytes"
	ctxpkg "context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// Port of api/mistral-conversations.ts: the native Mistral chat completions
// endpoint.

// mistralToolCallIDLength is the id length Mistral accepts.
const mistralToolCallIDLength = 9

// maxMistralErrorBodyChars bounds error bodies.
const maxMistralErrorBodyChars = 4000

// MistralReasoningEffort is Mistral's effort control.
type MistralReasoningEffort = string

// MistralOptions are the Mistral stream options.
type MistralOptions struct {
	StreamOptions
	// ToolChoice is "auto" | "none" | "any" | "required" or a named function.
	ToolChoice string
	// ToolChoiceFunction names the function for a forced tool choice.
	ToolChoiceFunction string
	// PromptMode is "reasoning" for prompt-mode reasoning models.
	PromptMode string
	// ReasoningEffort is "none" | "high".
	ReasoningEffort MistralReasoningEffort
}

type mistralContentChunk struct {
	Type     string             `json:"type"`
	Text     string             `json:"text,omitempty"`
	ImageURL string             `json:"imageUrl,omitempty"`
	Thinking []mistralTextChunk `json:"thinking,omitempty"`
}

type mistralTextChunk struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type mistralRequestToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
	Index int `json:"index"`
}

type mistralChatMessage struct {
	Role       string                   `json:"role"`
	Content    any                      `json:"content,omitempty"`
	ToolCalls  []mistralRequestToolCall `json:"toolCalls,omitempty"`
	ToolCallID string                   `json:"toolCallId,omitempty"`
	Name       string                   `json:"name,omitempty"`
	Prefix     *bool                    `json:"prefix,omitempty"`
}

type mistralFunctionTool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Parameters  json.RawMessage `json:"parameters"`
		Strict      bool            `json:"strict"`
	} `json:"function"`
}

type mistralChatPayload struct {
	Model           string                `json:"model"`
	Stream          bool                  `json:"stream"`
	Messages        []mistralChatMessage  `json:"messages"`
	Tools           []mistralFunctionTool `json:"tools,omitempty"`
	Temperature     *float64              `json:"temperature,omitempty"`
	MaxTokens       *int                  `json:"maxTokens,omitempty"`
	ToolChoice      json.RawMessage       `json:"toolChoice,omitempty"`
	PromptMode      string                `json:"promptMode,omitempty"`
	ReasoningEffort string                `json:"reasoningEffort,omitempty"`
	PromptCacheKey  string                `json:"promptCacheKey,omitempty"`
}

type mistralStreamToolCall struct {
	ID       string          `json:"id"`
	Index    *int            `json:"index"`
	Function mistralStreamFn `json:"function"`
}

type mistralStreamFn struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type mistralStreamContentChunk struct {
	Type     string             `json:"type"`
	Text     string             `json:"text"`
	Thinking []mistralTextChunk `json:"thinking"`
}

type mistralCompletionEvent struct {
	ID string `json:"id"`
	// Usage stays raw so the cached-token field can be read in any of the
	// shapes providers send.
	Usage   json.RawMessage `json:"usage"`
	Choices []struct {
		FinishReason string `json:"finish_reason"`
		Delta        struct {
			// Content is a string or an array of content chunks.
			Content   json.RawMessage         `json:"content"`
			ToolCalls []mistralStreamToolCall `json:"tool_calls"`
		} `json:"delta"`
	} `json:"choices"`
}

// StreamMistralConversations streams a Mistral chat completion
// (upstream stream).
func StreamMistralConversations(model *Model, context TranscriptContext, options *MistralOptions) *AssistantMessageEventStream {
	stream := NewAssistantMessageEventStream()
	normalizedContext := ResolveTranscript(context, mistralSupportsMidConvoSystemMessages(model))

	go func() {
		ctx := bgCtx()
		if options != nil && options.Ctx != nil {
			ctx = options.Ctx
		}
		if options == nil {
			options = &MistralOptions{}
		}
		output := &AssistantMessage{
			API: model.API, Provider: model.Provider, Model: model.ID,
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
			message := FormatMistralError(err)
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
		normalize := newMistralToolCallIDNormalizer()
		transformed := TransformMessages(normalizedContext.Messages, model,
			func(id string, _ *Model, _ *AssistantMessage) string { return normalize(id) })
		payload := buildMistralChatPayload(model, normalizedContext, transformed, options)
		if options.OnPayload != nil {
			if next := options.OnPayload(mustMarshalJSON(payload), model); next != nil {
				var replaced mistralChatPayload
				if perr := jsonUnmarshalStrict(next, &replaced); perr == nil {
					payload = &replaced
				}
			}
		}

		body, err := MarshalJSON(mistralWirePayload(payload))
		if err != nil {
			fail(err)
			return
		}
		resp, err := requestMistralStream(ctx, model, body, options)
		if err != nil {
			fail(err)
			return
		}
		defer resp.Body.Close()
		if options.OnResponse != nil {
			headers := map[string]string{}
			for name, values := range resp.Header {
				headers[strings.ToLower(name)] = strings.Join(values, ", ")
			}
			options.OnResponse(ProviderResponse{Status: resp.StatusCode, Headers: headers}, model)
		}
		stream.Push(AssistantMessageEvent{Type: EventStart, Partial: output})

		if err := consumeMistralChatStream(ctx, model, output, stream, resp.Body); err != nil {
			fail(err)
			return
		}
		if ctxErr(ctx) != nil {
			fail(fmt.Errorf("Request was aborted"))
			return
		}
		if output.StopReason == StopPending {
			fail(fmt.Errorf("Mistral stream ended without a finish reason"))
			return
		}
		if output.StopReason == StopAborted || output.StopReason == StopError {
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

// StreamMistralConversationsSimple maps simple options onto Mistral options
// (upstream streamSimple).
func StreamMistralConversationsSimple(model *Model, context TranscriptContext, options *SimpleStreamOptions) *AssistantMessageEventStream {
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
		mistralOptions := &MistralOptions{StreamOptions: options.StreamOptions}
		if options.ToolChoice != nil {
			mistralOptions.ToolChoice = mapToolChoiceType(*options.ToolChoice)
		}
		if options.Reasoning != "" {
			clamped := ClampThinkingLevel(model, options.Reasoning)
			if clamped != ThinkOff && model.Reasoning {
				if usesMistralReasoningEffort(model) {
					mistralOptions.ReasoningEffort = mapMistralReasoningEffort(model, clamped)
				} else if usesMistralPromptMode(model) {
					mistralOptions.PromptMode = "reasoning"
				}
			}
		}
		forwardStream(stream, StreamMistralConversations(model, context, mistralOptions))
	}()
	return stream
}

func mapToolChoiceType(choiceType string) string {
	switch choiceType {
	case "auto", "none", "any", "required":
		return choiceType
	default:
		return ""
	}
}

// FormatMistralError renders a Mistral failure (upstream formatMistralError).
func FormatMistralError(err error) string {
	if err == nil {
		return ""
	}
	if providerErr, ok := err.(*ProviderError); ok {
		bodyText := strings.TrimSpace(providerErr.Body)
		if bodyText != "" {
			return fmt.Sprintf("Mistral API error (%d): %s", providerErr.Status,
				truncateErrorText(bodyText, maxMistralErrorBodyChars))
		}
		return fmt.Sprintf("Mistral API error (%d): %s", providerErr.Status, providerErr.Error())
	}
	return err.Error()
}

func truncateErrorText(text string, maxChars int) string {
	if len(text) <= maxChars {
		return text
	}
	return fmt.Sprintf("%s... [truncated %d chars]", text[:maxChars], len(text)-maxChars)
}

func requestMistralStream(ctx ctxpkg.Context, model *Model, body []byte, options *MistralOptions) (*http.Response, error) {
	base, err := url.Parse(model.BaseURL)
	if err != nil {
		return nil, err
	}
	base.Path = strings.TrimRight(base.Path, "/") + "/"
	requestURL := base.ResolveReference(&url.URL{Path: "v1/chat/completions"}).String()

	timeout := 60 * time.Second
	if options.TimeoutMs != nil && *options.TimeoutMs > 0 {
		timeout = time.Duration(*options.TimeoutMs) * time.Millisecond
	}
	requestCtx, cancel := ctxpkg.WithTimeout(ctx, timeout)
	defer cancel()

	var resp *http.Response
	resp, err = RetryProviderRequest(requestCtx, func() (*http.Response, error) {
		req, nerr := http.NewRequestWithContext(requestCtx, http.MethodPost, requestURL, bytes.NewReader(body))
		if nerr != nil {
			return nil, nerr
		}
		req.Header.Set("User-Agent", GetPiUserAgent())
		req.Header.Set("accept", "text/event-stream")
		req.Header.Set("authorization", "Bearer "+options.APIKey)
		req.Header.Set("content-type", "application/json")
		for name, value := range model.Headers {
			req.Header.Set(name, value)
		}
		applyMistralHeaderOverrides(req, options.Headers)

		hasExplicitAffinity := hasMistralStringHeaderOverride(model.Headers, "x-affinity") ||
			hasMistralHeaderOverride(options.Headers, "x-affinity")
		if shouldUseMistralPromptCaching(options) && !hasExplicitAffinity {
			req.Header.Set("x-affinity", options.SessionID)
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
				Message: hresp.Status, Body: string(raw),
			}
		}
		return hresp, nil
	}, &ProviderRetryOptions{MaxRetries: options.MaxRetries, MaxRetryDelayMS: options.MaxRetryDelayMs})
	if err != nil {
		return nil, err
	}
	return resp, nil
}

func applyMistralHeaderOverrides(req *http.Request, overrides ProviderHeaders) {
	for name, value := range overrides {
		if value == nil {
			req.Header.Del(name)
			continue
		}
		req.Header.Set(name, *value)
	}
}

func hasMistralHeaderOverride(overrides ProviderHeaders, target string) bool {
	for name := range overrides {
		if strings.EqualFold(name, target) {
			return true
		}
	}
	return false
}

func hasMistralStringHeaderOverride(overrides map[string]string, target string) bool {
	for name := range overrides {
		if strings.EqualFold(name, target) {
			return true
		}
	}
	return false
}

// mistralSupportsMidConvoSystemMessages reads the Mistral compat flag.
func mistralSupportsMidConvoSystemMessages(model *Model) bool {
	if model == nil || model.Compat == nil || model.Compat.MistralConversations == nil ||
		model.Compat.MistralConversations.SupportsMidConvoSystemMessages == nil {
		return false
	}
	return *model.Compat.MistralConversations.SupportsMidConvoSystemMessages
}

// newMistralToolCallIDNormalizer maps tool-call ids onto Mistral's 9-character
// ids deterministically (upstream createMistralToolCallIdNormalizer).
func newMistralToolCallIDNormalizer() func(string) string {
	idMap := map[string]string{}
	reverseMap := map[string]string{}
	return func(id string) string {
		if existing, ok := idMap[id]; ok {
			return existing
		}
		for attempt := 0; ; attempt++ {
			candidate := deriveMistralToolCallID(id, attempt)
			owner, taken := reverseMap[candidate]
			if !taken || owner == id {
				idMap[id] = candidate
				reverseMap[candidate] = id
				return candidate
			}
		}
	}
}

// deriveMistralToolCallID derives one candidate id (upstream
// deriveMistralToolCallId).
func deriveMistralToolCallID(id string, attempt int) string {
	normalized := nonAlphanumeric.ReplaceAllString(id, "")
	if attempt == 0 && len(normalized) == mistralToolCallIDLength {
		return normalized
	}
	seedBase := normalized
	if seedBase == "" {
		seedBase = id
	}
	seed := seedBase
	if attempt != 0 {
		seed = fmt.Sprintf("%s:%d", seedBase, attempt)
	}
	hashed := nonAlphanumeric.ReplaceAllString(ShortHash(seed), "")
	if len(hashed) > mistralToolCallIDLength {
		hashed = hashed[:mistralToolCallIDLength]
	}
	return hashed
}

var nonAlphanumeric = regexp.MustCompile(`[^a-zA-Z0-9]`)

// mistralWirePayload renames the SDK-style keys to the wire keys
// (upstream toMistralWirePayload).
func mistralWirePayload(payload *mistralChatPayload) map[string]any {
	encoded, err := MarshalJSON(payload)
	if err != nil {
		return map[string]any{}
	}
	var wire map[string]any
	if err := json.Unmarshal(encoded, &wire); err != nil {
		return map[string]any{}
	}
	for _, rename := range [][2]string{
		{"topP", "top_p"},
		{"maxTokens", "max_tokens"},
		{"randomSeed", "random_seed"},
		{"responseFormat", "response_format"},
		{"toolChoice", "tool_choice"},
		{"presencePenalty", "presence_penalty"},
		{"frequencyPenalty", "frequency_penalty"},
		{"parallelToolCalls", "parallel_tool_calls"},
		{"reasoningEffort", "reasoning_effort"},
		{"promptMode", "prompt_mode"},
		{"promptCacheKey", "prompt_cache_key"},
		{"safePrompt", "safe_prompt"},
	} {
		remapMistralProperty(wire, rename[0], rename[1])
	}
	if messages, ok := wire["messages"].([]any); ok {
		for _, message := range messages {
			if record, ok := message.(map[string]any); ok {
				remapMistralProperty(record, "toolCalls", "tool_calls")
				remapMistralProperty(record, "toolCallId", "tool_call_id")
			}
		}
	}
	return wire
}

func remapMistralProperty(record map[string]any, source, target string) {
	value, ok := record[source]
	if !ok {
		return
	}
	record[target] = value
	delete(record, source)
}

// parseMistralEvent decodes one SSE event block: the joined data lines, or
// done=true for the [DONE] sentinel (upstream parseMistralEvent).
func parseMistralEvent(raw string) (*mistralCompletionEvent, bool, error) {
	var dataLines []string
	for _, line := range regexp.MustCompile(`\r\n|\r|\n`).Split(raw, -1) {
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		dataLines = append(dataLines, strings.TrimLeft(line[5:], " \t"))
	}
	data := strings.TrimSpace(strings.Join(dataLines, "\n"))
	if data == "" {
		return nil, false, nil
	}
	if data == "[DONE]" {
		return nil, true, nil
	}
	var event mistralCompletionEvent
	if err := json.Unmarshal([]byte(data), &event); err != nil {
		return nil, false, err
	}
	if event.Choices == nil {
		return nil, false, fmt.Errorf("Invalid Mistral streaming event")
	}
	return &event, false, nil
}

var mistralEventBoundary = regexp.MustCompile(`\r\n\r\n|\r\n\r|\r\n\n|\r\r\n|\n\r\n|\r\r|\n\r|\n\n`)

// consumeMistralChatStream assembles the streamed deltas into the message
// (upstream consumeChatStream).
func consumeMistralChatStream(ctx ctxpkg.Context, model *Model, output *AssistantMessage, stream *AssistantMessageEventStream, body io.Reader) error {
	reader := bufio.NewReader(body)
	buffer := ""
	state := &mistralStreamState{toolBlocks: map[string]int{}, partialArgs: map[int]string{}}

	handle := func(raw string) (done bool, err error) {
		event, isDone, err := parseMistralEvent(raw)
		if err != nil {
			return false, err
		}
		if isDone {
			return true, nil
		}
		if event == nil {
			return false, nil
		}
		applyMistralEvent(model, output, stream, state, event)
		return false, nil
	}

	for {
		if ctxErr(ctx) != nil {
			return ctxErr(ctx)
		}
		chunk := make([]byte, 8192)
		read, err := reader.Read(chunk)
		if read > 0 {
			buffer += string(chunk[:read])
			for {
				location := mistralEventBoundary.FindStringIndex(buffer)
				if location == nil {
					break
				}
				raw := buffer[:location[0]]
				buffer = buffer[location[1]:]
				done, herr := handle(raw)
				if herr != nil {
					return herr
				}
				if done {
					finishMistralBlock(output, stream)
					finishMistralToolCalls(output, stream, state)
					return nil
				}
			}
		}
		if err != nil {
			if err != io.EOF {
				return err
			}
			break
		}
	}
	if strings.TrimSpace(buffer) != "" {
		done, err := handle(buffer)
		if err != nil {
			return err
		}
		_ = done
	}
	finishMistralBlock(output, stream)
	finishMistralToolCalls(output, stream, state)
	return nil
}

// applyMistralEvent folds one completion event into the output message.
func applyMistralEvent(model *Model, output *AssistantMessage, stream *AssistantMessageEventStream, state *mistralStreamState, event *mistralCompletionEvent) {
	// The streamed chunk id is the stable response identifier.
	if output.ResponseID == nil && event.ID != "" {
		output.ResponseID = &event.ID
	}

	if usage, ok := parseMistralUsage(event.Usage); ok {
		promptTokens := usage.PromptTokens
		cachedPromptTokens := mistralCachedPromptTokens(event.Usage, promptTokens)
		output.Usage.Input = max64(0, promptTokens-cachedPromptTokens)
		output.Usage.Output = usage.CompletionTokens
		output.Usage.CacheRead = cachedPromptTokens
		output.Usage.CacheWrite = 0
		if usage.TotalTokens > 0 {
			output.Usage.TotalTokens = usage.TotalTokens
		} else {
			output.Usage.TotalTokens = output.Usage.Input + output.Usage.Output + output.Usage.CacheRead + output.Usage.CacheWrite
		}
		CalculateCost(model, &output.Usage)
	}

	if len(event.Choices) == 0 {
		return
	}
	choice := event.Choices[0]
	if choice.FinishReason != "" {
		output.RawStopReason = &choice.FinishReason
		stopReason, errorMessage := mapMistralStopReason(choice.FinishReason)
		output.StopReason = stopReason
		if errorMessage != "" {
			output.ErrorMessage = &errorMessage
		}
	}

	if len(choice.Delta.Content) > 0 && string(choice.Delta.Content) != "null" {
		// A string delta.
		var text string
		if err := json.Unmarshal(choice.Delta.Content, &text); err == nil {
			appendMistralText(output, stream, text)
		} else {
			var chunks []mistralStreamContentChunk
			if err := json.Unmarshal(choice.Delta.Content, &chunks); err == nil {
				for _, chunk := range chunks {
					switch chunk.Type {
					case "thinking":
						var parts []string
						for _, part := range chunk.Thinking {
							if part.Text != "" {
								parts = append(parts, part.Text)
							}
						}
						appendMistralThinking(output, stream, strings.Join(parts, ""))
					case "text":
						appendMistralText(output, stream, chunk.Text)
					}
				}
			}
		}
	}

	for _, toolCall := range choice.Delta.ToolCalls {
		appendMistralToolCall(output, stream, state, toolCall)
	}
}

// mistralStreamState tracks the tool-call blocks of one stream so partial
// arguments from several deltas land in one block.
type mistralStreamState struct {
	toolBlocks map[string]int
	// partialArgs holds the raw accumulated argument text per block index
	// (upstream's streaming scratch buffer, which is never persisted).
	partialArgs map[int]string
}

func appendMistralText(output *AssistantMessage, stream *AssistantMessageEventStream, text string) {
	delta := SanitizeSurrogates(text)
	if delta == "" {
		return
	}
	current, hasCurrent := mistralCurrentBlock(output)
	if !hasCurrent || current.kind != KindText {
		finishMistralBlock(output, stream)
		output.Content = append(output.Content, TextContent{Text: ""})
		stream.Push(AssistantMessageEvent{Type: EventTextStart, ContentIndex: len(output.Content) - 1, Partial: output})
	}
	index := len(output.Content) - 1
	text0, _ := output.Content[index].(TextContent)
	text0.Text += delta
	output.Content[index] = text0
	stream.Push(AssistantMessageEvent{Type: EventTextDelta, ContentIndex: index, Delta: delta, Partial: output})
}

func appendMistralThinking(output *AssistantMessage, stream *AssistantMessageEventStream, text string) {
	delta := SanitizeSurrogates(text)
	if delta == "" {
		return
	}
	current, hasCurrent := mistralCurrentBlock(output)
	if !hasCurrent || current.kind != KindThinking {
		finishMistralBlock(output, stream)
		output.Content = append(output.Content, ThinkingContent{Thinking: ""})
		stream.Push(AssistantMessageEvent{Type: EventThinkingStart, ContentIndex: len(output.Content) - 1, Partial: output})
	}
	index := len(output.Content) - 1
	thinking, _ := output.Content[index].(ThinkingContent)
	thinking.Thinking += delta
	output.Content[index] = thinking
	stream.Push(AssistantMessageEvent{Type: EventThinkingDelta, ContentIndex: index, Delta: delta, Partial: output})
}

// mistralCurrentBlock returns the trailing text/thinking block.
func mistralCurrentBlock(output *AssistantMessage) (blockRef, bool) {
	if len(output.Content) == 0 {
		return blockRef{}, false
	}
	switch typed := output.Content[len(output.Content)-1].(type) {
	case TextContent:
		return blockRef{kind: KindText}, true
	case ThinkingContent:
		return blockRef{kind: KindThinking}, true
	default:
		_ = typed
		return blockRef{}, false
	}
}

type blockRef struct{ kind ContentKind }

// finishMistralBlock emits the end event for the trailing block.
func finishMistralBlock(output *AssistantMessage, stream *AssistantMessageEventStream) {
	if len(output.Content) == 0 {
		return
	}
	index := len(output.Content) - 1
	switch typed := output.Content[index].(type) {
	case TextContent:
		stream.Push(AssistantMessageEvent{Type: EventTextEnd, ContentIndex: index, Content: typed.Text, Partial: output})
	case ThinkingContent:
		stream.Push(AssistantMessageEvent{Type: EventThinkingEnd, ContentIndex: index, Content: typed.Thinking, Partial: output})
	}
}

func appendMistralToolCall(output *AssistantMessage, stream *AssistantMessageEventStream, state *mistralStreamState, toolCall mistralStreamToolCall) {
	finishMistralBlock(output, stream)

	callID := toolCall.ID
	if callID == "" || callID == "null" {
		index := 0
		if toolCall.Index != nil {
			index = *toolCall.Index
		}
		callID = deriveMistralToolCallID(fmt.Sprintf("toolcall:%d", index), 0)
	}
	key := callID
	if toolCall.Index != nil {
		key = fmt.Sprintf("#%d", *toolCall.Index)
	}

	keys := state.toolBlocks
	blockIndex, existing := keys[key]
	var block ToolCall
	if existing {
		if current, ok := output.Content[blockIndex].(ToolCall); ok {
			block = current
		}
	} else {
		block = ToolCall{ID: callID, Name: toolCall.Function.Name, Arguments: json.RawMessage(`{}`)}
		output.Content = append(output.Content, block)
		blockIndex = len(output.Content) - 1
		keys[key] = blockIndex
		stream.Push(AssistantMessageEvent{Type: EventToolcallStart, ContentIndex: blockIndex, Partial: output})
	}

	argsDelta := ""
	if len(toolCall.Function.Arguments) > 0 {
		var asString string
		if err := json.Unmarshal(toolCall.Function.Arguments, &asString); err == nil {
			argsDelta = asString
		} else {
			argsDelta = string(toolCall.Function.Arguments)
		}
	}
	partial := state.partialArgs[blockIndex] + argsDelta
	state.partialArgs[blockIndex] = partial
	block.Arguments = parseStreamingArgs(json.RawMessage(partial))
	output.Content[blockIndex] = block
	stream.Push(AssistantMessageEvent{Type: EventToolcallDelta, ContentIndex: blockIndex, Delta: argsDelta, Partial: output})
}

// finishMistralToolCalls emits toolcall_end for every collected tool block.
func finishMistralToolCalls(output *AssistantMessage, stream *AssistantMessageEventStream, state *mistralStreamState) {
	for _, index := range state.toolBlocks {
		block, ok := output.Content[index].(ToolCall)
		if !ok {
			continue
		}
		if partial, ok := state.partialArgs[index]; ok {
			block.Arguments = parseStreamingArgs(json.RawMessage(partial))
			output.Content[index] = block
		}
		stream.Push(AssistantMessageEvent{Type: EventToolcallEnd, ContentIndex: index, ToolCall: &block, Partial: output})
	}
}

func mapMistralStopReason(reason string) (StopReason, string) {
	switch reason {
	case "", "stop":
		return StopStop, ""
	case "length", "model_length":
		return StopLength, ""
	case "tool_calls":
		return StopToolUse, ""
	case "error":
		return StopError, "Provider stopped with: error"
	default:
		return StopError, "Provider stopped with: " + reason
	}
}

// parseMistralUsage decodes the token counts from a usage payload.
func parseMistralUsage(raw json.RawMessage) (struct {
	PromptTokens     int64
	CompletionTokens int64
	TotalTokens      int64
}, bool) {
	result := struct {
		PromptTokens     int64
		CompletionTokens int64
		TotalTokens      int64
	}{}
	if len(raw) == 0 || string(raw) == "null" {
		return result, false
	}
	var payload struct {
		PromptTokens     *float64 `json:"prompt_tokens"`
		CompletionTokens *float64 `json:"completion_tokens"`
		TotalTokens      *float64 `json:"total_tokens"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return result, false
	}
	if payload.PromptTokens != nil {
		result.PromptTokens = int64(*payload.PromptTokens)
	}
	if payload.CompletionTokens != nil {
		result.CompletionTokens = int64(*payload.CompletionTokens)
	}
	if payload.TotalTokens != nil {
		result.TotalTokens = int64(*payload.TotalTokens)
	}
	return result, true
}

func mistralCachedPromptTokens(raw json.RawMessage, promptTokens int64) int64 {
	if len(raw) == 0 {
		return 0
	}
	var usage map[string]any
	if err := json.Unmarshal(raw, &usage); err != nil {
		return 0
	}
	pick := func(container any, key string) (int64, bool) {
		record, ok := container.(map[string]any)
		if !ok {
			return 0, false
		}
		value, ok := record[key].(float64)
		if !ok {
			return 0, false
		}
		return int64(value), true
	}
	for _, candidate := range []struct{ container, key string }{
		{"promptTokensDetails", "cachedTokens"},
		{"prompt_tokens_details", "cached_tokens"},
		{"promptTokenDetails", "cachedTokens"},
		{"prompt_token_details", "cached_tokens"},
	} {
		if value, ok := pick(usage[candidate.container], candidate.key); ok {
			return min64(promptTokens, max64(0, value))
		}
	}
	for _, key := range []string{"numCachedTokens", "num_cached_tokens"} {
		if value, ok := usage[key].(float64); ok {
			return min64(promptTokens, max64(0, int64(value)))
		}
	}
	return 0
}

func buildMistralChatPayload(model *Model, context TranscriptContext, messages []Message, options *MistralOptions) *mistralChatPayload {
	payload := &mistralChatPayload{
		Model:    model.ID,
		Stream:   true,
		Messages: toMistralChatMessages(messages, containsString(model.Input, "image")),
	}
	currentTools := GetCurrentTools(context.Messages)
	if len(currentTools) > 0 {
		payload.Tools = toMistralFunctionTools(currentTools)
	}
	if options.Temperature != nil {
		payload.Temperature = options.Temperature
	}
	if options.MaxTokens != nil {
		payload.MaxTokens = options.MaxTokens
	}
	if options.ToolChoice != "" {
		payload.ToolChoice = mapMistralToolChoice(options)
	}
	if options.PromptMode != "" {
		payload.PromptMode = options.PromptMode
	}
	if options.ReasoningEffort != "" {
		payload.ReasoningEffort = options.ReasoningEffort
	}
	if shouldUseMistralPromptCaching(options) {
		payload.PromptCacheKey = options.SessionID
	}
	return payload
}

func mapMistralToolChoice(options *MistralOptions) json.RawMessage {
	if options.ToolChoiceFunction != "" {
		return mustMarshalJSON(map[string]any{
			"type": "function", "function": map[string]any{"name": options.ToolChoiceFunction},
		})
	}
	return mustMarshalJSON(options.ToolChoice)
}

func shouldUseMistralPromptCaching(options *MistralOptions) bool {
	if options == nil {
		return false
	}
	retention := ResolveCacheRetention(options.CacheRetention, options.Env)
	return retention != CacheRetentionNone && options.SessionID != ""
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func toMistralFunctionTools(tools []Tool) []mistralFunctionTool {
	out := make([]mistralFunctionTool, 0, len(tools))
	for _, tool := range tools {
		strict, unset, err := ResolveJSONSchemaStrictSampling(tool, true)
		if err != nil || unset {
			strict = false
		}
		entry := mistralFunctionTool{Type: "function"}
		entry.Function.Name = tool.Name
		entry.Function.Description = tool.Description
		entry.Function.Parameters = stripJSONSymbolKeys(GetJSONSchemaToolParameters(tool, strict))
		entry.Function.Strict = strict
		out = append(out, entry)
	}
	return out
}

// stripJSONSymbolKeys drops non-JSON values (Go has no symbol keys, so this only
// re-encodes the schema defensively).
func stripJSONSymbolKeys(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage(`{}`)
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return json.RawMessage(`{}`)
	}
	encoded, err := MarshalJSON(value)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return encoded
}

func toMistralChatMessages(messages []Message, supportsImages bool) []mistralChatMessage {
	var result []mistralChatMessage
	for index, message := range messages {
		switch typed := message.(type) {
		case *SystemMessage:
			text := ""
			if index == 0 {
				text = GetSystemMessageText(typed)
			} else {
				text = RenderSystemMessageUpdate(typed)
			}
			if text != "" {
				result = append(result, mistralChatMessage{Role: "system", Content: SanitizeSurrogates(text)})
			}
		case *UserMessage:
			if typed.Content.String() {
				result = append(result, mistralChatMessage{Role: "user", Content: SanitizeSurrogates(typed.Content.Text)})
				continue
			}
			hadImages := false
			var content []mistralContentChunk
			for _, block := range typed.Content.Blocks {
				switch inner := block.(type) {
				case TextContent:
					content = append(content, mistralContentChunk{Type: "text", Text: SanitizeSurrogates(inner.Text)})
				case ImageContent:
					hadImages = true
					if supportsImages {
						content = append(content, mistralContentChunk{
							Type: "image_url", ImageURL: "data:" + inner.MimeType + ";base64," + inner.Data,
						})
					}
				}
			}
			if len(content) > 0 {
				result = append(result, mistralChatMessage{Role: "user", Content: content})
				continue
			}
			if hadImages && !supportsImages {
				result = append(result, mistralChatMessage{
					Role: "user", Content: "(image omitted: model does not support images)",
				})
			}
		case *AssistantMessage:
			var contentParts []mistralContentChunk
			var toolCalls []mistralRequestToolCall
			for _, block := range typed.Content {
				switch inner := block.(type) {
				case TextContent:
					if strings.TrimSpace(inner.Text) != "" {
						contentParts = append(contentParts, mistralContentChunk{Type: "text", Text: SanitizeSurrogates(inner.Text)})
					}
				case ThinkingContent:
					if strings.TrimSpace(inner.Thinking) != "" {
						contentParts = append(contentParts, mistralContentChunk{
							Type:     "thinking",
							Thinking: []mistralTextChunk{{Type: "text", Text: SanitizeSurrogates(inner.Thinking)}},
						})
					}
				case ToolCall:
					call := mistralRequestToolCall{ID: inner.ID, Type: "function"}
					call.Function.Name = inner.Name
					call.Function.Arguments = string(parseStreamingArgs(inner.Arguments))
					toolCalls = append(toolCalls, call)
				}
			}
			prefix := false
			assistant := mistralChatMessage{Role: "assistant", Prefix: &prefix}
			if len(contentParts) > 0 {
				assistant.Content = contentParts
			}
			if len(toolCalls) > 0 {
				assistant.ToolCalls = toolCalls
			}
			if len(contentParts) > 0 || len(toolCalls) > 0 {
				result = append(result, assistant)
			}
		case *ToolResultMessage:
			var textParts []string
			hasImages := false
			for _, block := range typed.Content {
				switch inner := block.(type) {
				case TextContent:
					textParts = append(textParts, SanitizeSurrogates(inner.Text))
				case ImageContent:
					hasImages = true
				}
			}
			toolText := buildMistralToolResultText(strings.Join(textParts, "\n"), hasImages, supportsImages, typed.IsError)
			content := []mistralContentChunk{{Type: "text", Text: toolText}}
			if supportsImages {
				for _, block := range typed.Content {
					if image, ok := block.(ImageContent); ok {
						content = append(content, mistralContentChunk{
							Type: "image_url", ImageURL: "data:" + image.MimeType + ";base64," + image.Data,
						})
					}
				}
			}
			result = append(result, mistralChatMessage{
				Role: "tool", ToolCallID: typed.ToolCallID, Name: typed.ToolName, Content: content,
			})
		}
	}
	return result
}

func buildMistralToolResultText(text string, hasImages, supportsImages, isError bool) string {
	trimmed := strings.TrimSpace(text)
	errorPrefix := ""
	if isError {
		errorPrefix = "[tool error] "
	}
	if trimmed != "" {
		imageSuffix := ""
		if hasImages && !supportsImages {
			imageSuffix = "\n[tool image omitted: model does not support images]"
		}
		return errorPrefix + trimmed + imageSuffix
	}
	if hasImages {
		if supportsImages {
			if isError {
				return "[tool error] (see attached image)"
			}
			return "(see attached image)"
		}
		if isError {
			return "[tool error] (image omitted: model does not support images)"
		}
		return "(image omitted: model does not support images)"
	}
	if isError {
		return "[tool error] (no tool output)"
	}
	return "(no tool output)"
}

func usesMistralReasoningEffort(model *Model) bool {
	return model.ID == "mistral-small-2603" || model.ID == "mistral-small-latest" ||
		strings.HasPrefix(model.ID, "mistral-medium-") || model.ID == "zai-glm-5-2"
}

func usesMistralPromptMode(model *Model) bool {
	return model.Reasoning && !usesMistralReasoningEffort(model)
}

func mapMistralReasoningEffort(model *Model, level ThinkingLevel) MistralReasoningEffort {
	if mapped, ok := model.ThinkingLevelMap[level]; ok && mapped != nil {
		return *mapped
	}
	return "high"
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}
