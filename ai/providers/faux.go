// Package providers holds the concrete API provider implementations,
// mirroring pi/packages/ai/src/providers. Package faux is ported from
// providers/faux.ts at the pinned upstream commit: a deterministic test
// double that is the reference spec for the streaming protocol.
package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"sync"
	"time"
	"unicode/utf16"

	"github.com/dat267/pier/ai"
)

// Port of providers/faux.ts.

const (
	fauxDefaultAPI       = "faux"
	fauxDefaultProvider  = "faux"
	fauxDefaultModelID   = "faux-1"
	fauxDefaultModelName = "Faux Model"
	fauxDefaultBaseURL   = "http://localhost:0"
	fauxDefaultMinToken  = 3
	fauxDefaultMaxToken  = 5
)

var fauxDefaultUsage = ai.Usage{
	Input: 0, Output: 0, CacheRead: 0, CacheWrite: 0, TotalTokens: 0,
	Cost: ai.UsageCost{},
}

// FauxModelDefinition declares one faux model.
type FauxModelDefinition struct {
	ID            string
	Name          string // defaults to ID
	Reasoning     bool
	Input         []string // defaults to ["text","image"]
	Cost          *ai.ModelCostRates
	ContextWindow int64 // defaults to 128000
	MaxTokens     int64 // defaults to 16384
}

// FauxProviderState is the faux provider's observable state.
type FauxProviderState struct {
	CallCount          int
	DeferredFetchCount int
	CancelledDeferred  []*ai.DeferredHandle
}

// FauxResponseFactory is a scripted dynamic response. It receives the
// normalized transcript context, the stream options, the provider state, and
// the requested model.
type FauxResponseFactory func(
	context ai.TranscriptContext,
	options *ai.SimpleStreamOptions,
	state *FauxProviderState,
	model *ai.Model,
) (*ai.AssistantMessage, error)

// FauxResponseStep is one queued response: a fixed message or a factory.
type FauxResponseStep struct {
	Message *ai.AssistantMessage
	Factory FauxResponseFactory
}

// FauxOptions configures a faux provider core.
type FauxOptions struct {
	API      string
	Provider string
	Models   []FauxModelDefinition
	Deferred struct {
		// PendingFetches is the number of fetches that return the original
		// handle before the scripted response becomes ready.
		PendingFetches int
		PollAfterMs    int
		HasPollAfterMs bool
	}
	// TokensPerSecond paces delta delivery; <= 0 streams as fast as possible.
	TokensPerSecond float64
	TokenSizeMin    int
	TokenSizeMax    int
}

// FauxCore is the scripted provider core (upstream createFauxCore).
type FauxCore struct {
	mu sync.Mutex

	api          string
	provider     string
	minTokenSize int
	maxTokenSize int
	tps          float64
	responses    []FauxResponseStep
	state        FauxProviderState
	promptCache  map[string]string
	cacheMu      sync.Mutex
	deferral     struct {
		pendingFetches int
		pollAfterMs    *int64
	}

	models []*ai.Model

	deferredResponses map[string]*fauxDeferredEntry
}

type fauxDeferredEntry struct {
	handle         ai.DeferredHandle
	step           FauxResponseStep
	context        ai.TranscriptContext
	options        *ai.SimpleStreamOptions
	model          *ai.Model
	pendingFetches int
	cancelled      bool
	final          *ai.AssistantMessage
}

// NewFauxCore builds a faux provider core.
func NewFauxCore(options FauxOptions) *FauxCore {
	api := options.API
	if api == "" {
		api = fauxRandomID(fauxDefaultAPI)
	}
	provider := options.Provider
	if provider == "" {
		provider = fauxDefaultProvider
	}
	minToken := options.TokenSizeMin
	if minToken == 0 {
		minToken = fauxDefaultMinToken
	}
	maxToken := options.TokenSizeMax
	if maxToken == 0 {
		maxToken = fauxDefaultMaxToken
	}
	minToken = max(1, min(minToken, maxToken))
	maxToken = max(minToken, maxToken)

	defs := options.Models
	if len(defs) == 0 {
		defs = []FauxModelDefinition{{
			ID: fauxDefaultModelID, Name: fauxDefaultModelName,
			Input: []string{"text", "image"}, ContextWindow: 128000, MaxTokens: 16384,
		}}
	}
	models := make([]*ai.Model, 0, len(defs))
	for _, def := range defs {
		input := def.Input
		if input == nil {
			input = []string{"text", "image"}
		}
		cost := def.Cost
		if cost == nil {
			cost = &ai.ModelCostRates{}
		}
		contextWindow := def.ContextWindow
		if contextWindow == 0 {
			contextWindow = 128000
		}
		maxTokens := def.MaxTokens
		if maxTokens == 0 {
			maxTokens = 16384
		}
		name := def.Name
		if name == "" {
			name = def.ID
		}
		models = append(models, &ai.Model{
			ID: def.ID, Name: name, API: api, Provider: provider,
			BaseURL: fauxDefaultBaseURL, Reasoning: def.Reasoning,
			Input: input, Cost: ai.ModelCost{ModelCostRates: *cost},
			ContextWindow: contextWindow, MaxTokens: maxTokens,
		})
	}

	core := &FauxCore{
		api:               api,
		provider:          provider,
		minTokenSize:      minToken,
		maxTokenSize:      maxToken,
		tps:               options.TokensPerSecond,
		promptCache:       map[string]string{},
		models:            models,
		deferredResponses: map[string]*fauxDeferredEntry{},
	}
	core.deferral.pendingFetches = options.Deferred.PendingFetches
	if options.Deferred.HasPollAfterMs {
		ms := int64(options.Deferred.PollAfterMs)
		core.deferral.pollAfterMs = &ms
	}
	return core
}

// API returns the faux API id.
func (c *FauxCore) API() string { return c.api }

// Provider returns the faux provider id.
func (c *FauxCore) Provider() string { return c.provider }

// Models returns the faux model list.
func (c *FauxCore) Models() []*ai.Model { return c.models }

// GetModel returns the first model, or the model with the given id.
func (c *FauxCore) GetModel(modelID string) *ai.Model {
	c.mu.Lock()
	defer c.mu.Unlock()
	if modelID == "" {
		return c.models[0]
	}
	for _, m := range c.models {
		if m.ID == modelID {
			return m
		}
	}
	return nil
}

// State returns the provider state.
func (c *FauxCore) State() *FauxProviderState { return &c.state }

// SetResponses replaces the queued responses.
func (c *FauxCore) SetResponses(responses []FauxResponseStep) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.responses = append([]FauxResponseStep{}, responses...)
}

// AppendResponses appends to the queued responses.
func (c *FauxCore) AppendResponses(responses []FauxResponseStep) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.responses = append(c.responses, responses...)
}

// GetPendingResponseCount returns the number of queued responses.
func (c *FauxCore) GetPendingResponseCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.responses)
}

func (c *FauxCore) shiftResponse() (FauxResponseStep, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.responses) == 0 {
		return FauxResponseStep{}, false
	}
	step := c.responses[0]
	c.responses = c.responses[1:]
	return step, true
}

// Faux helpers (upstream module-level functions).

// FauxText builds a text block.
func FauxText(text string) ai.TextContent { return ai.TextContent{Text: text} }

// FauxThinking builds a thinking block.
func FauxThinking(thinking string) ai.ThinkingContent { return ai.ThinkingContent{Thinking: thinking} }

// FauxToolCall builds a tool call block with a random id unless given.
func FauxToolCall(name string, args json.RawMessage, id string) ai.ToolCall {
	if id == "" {
		id = fauxRandomID("tool")
	}
	if args == nil {
		args = json.RawMessage("{}")
	}
	return ai.ToolCall{ID: id, Name: name, Arguments: args}
}

// FauxAssistantMessage builds an assistant message attributed to the default
// faux api/provider/model.
type FauxMessageOptions struct {
	StopReason   ai.StopReason
	Deferred     *ai.DeferredHandle
	ErrorMessage string
	ResponseID   string
	Timestamp    int64 // 0 → time.Now().UnixMilli()
}

func FauxAssistantMessage(content any, options FauxMessageOptions) *ai.AssistantMessage {
	blocks := normalizeFauxAssistantContent(content)
	msg := &ai.AssistantMessage{
		Content:    blocks,
		API:        fauxDefaultAPI,
		Provider:   fauxDefaultProvider,
		Model:      fauxDefaultModelID,
		Usage:      fauxDefaultUsage,
		StopReason: options.StopReason,
		Timestamp:  options.Timestamp,
	}
	if msg.StopReason == "" {
		msg.StopReason = ai.StopStop
	}
	if options.Deferred != nil {
		msg.Deferred = options.Deferred
	}
	if options.ErrorMessage != "" {
		msg.ErrorMessage = &options.ErrorMessage
	}
	if options.ResponseID != "" {
		msg.ResponseID = &options.ResponseID
	}
	if msg.Timestamp == 0 {
		msg.Timestamp = time.Now().UnixMilli()
	}
	return msg
}

func normalizeFauxAssistantContent(content any) ai.ContentList {
	switch v := content.(type) {
	case string:
		return ai.ContentList{FauxText(v)}
	case ai.TextContent:
		return ai.ContentList{v}
	case ai.ThinkingContent:
		return ai.ContentList{v}
	case ai.ToolCall:
		return ai.ContentList{v}
	case ai.ContentList:
		return v
	case []ai.Content:
		return ai.ContentList(v)
	case nil:
		return nil
	default:
		panic(fmt.Sprintf("providers: unsupported faux content %T", content))
	}
}

func fauxEstimateTokens(text string) int {
	return (ai.JSLength(text) + 3) / 4 // Math.ceil(length / 4)
}

func fauxRandomID(prefix string) string {
	return fmt.Sprintf("%s:%d:%s", prefix, time.Now().UnixMilli(), fauxRandomSuffix())
}

func fauxRandomSuffix() string {
	const charset = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, 8)
	for i := range b {
		b[i] = charset[rand.Intn(len(charset))]
	}
	return string(b)
}

// fauxContentToText renders user/tool-result content.
func fauxContentToText(content ai.StringOrBlocks) string {
	if content.Blocks == nil {
		return content.Text
	}
	var parts []string
	for _, block := range content.Blocks {
		switch b := block.(type) {
		case ai.TextContent:
			parts = append(parts, b.Text)
		case ai.ImageContent:
			parts = append(parts, fmt.Sprintf("[image:%s:%d]", b.MimeType, ai.JSLength(b.Data)))
		}
	}
	return joinLines(parts)
}

// fauxAssistantContentToText renders assistant content.
func fauxAssistantContentToText(content ai.ContentList) string {
	var parts []string
	for _, block := range content {
		switch b := block.(type) {
		case ai.TextContent:
			parts = append(parts, b.Text)
		case ai.ThinkingContent:
			parts = append(parts, b.Thinking)
		case ai.ToolCall:
			args, err := ai.MarshalJSON(json.RawMessage(b.Arguments))
			if err != nil {
				args = []byte("null")
			}
			parts = append(parts, fmt.Sprintf("%s:%s", b.Name, args))
		}
	}
	return joinLines(parts)
}

func joinLines(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += "\n"
		}
		out += p
	}
	return out
}

func fauxToolResultToText(message *ai.ToolResultMessage) string {
	parts := []string{message.ToolName}
	for _, block := range message.Content {
		parts = append(parts, fauxContentToText(ai.StringOrBlocks{Blocks: ai.ContentList{block}}))
	}
	return joinLines(parts)
}

func fauxMessageToText(message ai.Message) string {
	switch m := message.(type) {
	case *ai.SystemMessage:
		parts := []string{ai.GetSystemMessageText(m)}
		for _, tool := range m.ToolsRemoved {
			enc, _ := ai.MarshalJSON(tool)
			parts = append(parts, "tool-:"+string(enc))
		}
		for _, tool := range m.ToolsAdded {
			enc, _ := ai.MarshalJSON(tool)
			parts = append(parts, "tool+:"+string(enc))
		}
		var nonEmpty []string
		for _, p := range parts {
			if len(p) > 0 {
				nonEmpty = append(nonEmpty, p)
			}
		}
		return joinLines(nonEmpty)
	case *ai.UserMessage:
		return fauxContentToText(m.Content)
	case *ai.AssistantMessage:
		return fauxAssistantContentToText(m.Content)
	case *ai.ToolResultMessage:
		return fauxToolResultToText(m)
	default:
		return ""
	}
}

func fauxSerializeContext(context ai.TranscriptContext) string {
	var parts []string
	for _, message := range context.Messages {
		parts = append(parts, fmt.Sprintf("%s:%s", ai.RoleOf(message), fauxMessageToText(message)))
	}
	return joinWithDoubleNewline(parts)
}

func joinWithDoubleNewline(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += "\n\n"
		}
		out += p
	}
	return out
}

func fauxPrefixLength(a, b string) int {
	ua := utf16.Encode([]rune(a))
	ub := utf16.Encode([]rune(b))
	length := min(len(ua), len(ub))
	index := 0
	for index < length && ua[index] == ub[index] {
		index++
	}
	return index
}

func fauxWithUsageEstimate(
	message *ai.AssistantMessage,
	context ai.TranscriptContext,
	options *ai.StreamOptions,
	promptCache map[string]string,
	cacheMu *sync.Mutex,
) *ai.AssistantMessage {
	promptText := fauxSerializeContext(context)
	promptTokens := fauxEstimateTokens(promptText)
	outputTokens := fauxEstimateTokens(fauxAssistantContentToText(message.Content))
	input := promptTokens
	var cacheRead, cacheWrite int64
	if options != nil && options.SessionID != "" && options.CacheRetention != ai.CacheRetentionNone {
		cacheMu.Lock()
		previousPrompt, ok := promptCache[options.SessionID]
		if ok {
			cachedChars := fauxPrefixLength(previousPrompt, promptText)
			cacheRead = int64(fauxEstimateTokens(ai.JSSlice(previousPrompt, 0, cachedChars)))
			cacheWrite = int64(fauxEstimateTokens(ai.JSSlice(promptText, cachedChars, ai.JSLength(promptText))))
			input = max(0, promptTokens-int(cacheRead))
		} else {
			cacheWrite = int64(promptTokens)
		}
		promptCache[options.SessionID] = promptText
		cacheMu.Unlock()
	}

	usage := ai.Usage{
		Input:       int64(input),
		Output:      int64(outputTokens),
		CacheRead:   cacheRead,
		CacheWrite:  cacheWrite,
		TotalTokens: int64(input) + int64(outputTokens) + cacheRead + cacheWrite,
		Cost:        ai.UsageCost{},
	}
	cloned := fauxCloneMessage(message)
	cloned.Usage = usage
	return cloned
}

// fauxCloneMessage deep-clones a message (upstream structuredClone).
func fauxCloneMessage(message *ai.AssistantMessage) *ai.AssistantMessage {
	enc, err := ai.MarshalMessage(message)
	if err != nil {
		panic(err)
	}
	decoded, err := ai.UnmarshalMessage(enc)
	if err != nil {
		panic(err)
	}
	return decoded.(*ai.AssistantMessage)
}

// fauxReattribute clones a message and rewrites api, provider, and model
// (upstream cloneMessage: `{...cloned, api, provider, model: modelId}`).
func fauxReattribute(message *ai.AssistantMessage, api, provider, modelID string) *ai.AssistantMessage {
	cloned := fauxCloneMessage(message)
	cloned.API = api
	cloned.Provider = provider
	cloned.Model = modelID
	if cloned.Timestamp == 0 {
		cloned.Timestamp = time.Now().UnixMilli()
	}
	return cloned
}

func fauxSplitStringByTokenSize(text string, minTokenSize, maxTokenSize int) []string {
	var chunks []string
	index := 0
	length := ai.JSLength(text)
	for index < length {
		tokenSize := minTokenSize + rand.Intn(maxTokenSize-minTokenSize+1)
		charSize := max(1, tokenSize*4)
		chunks = append(chunks, ai.JSSlice(text, index, index+charSize))
		index += charSize
	}
	if len(chunks) == 0 {
		return []string{""}
	}
	return chunks
}

func fauxCloneForEvent(message *ai.AssistantMessage) *ai.AssistantMessage {
	// Events carry a shallow copy of the shared partial (upstream spreads
	// `{ ...partial }`); the partial itself is mutated in place between
	// events, so each event's snapshot must be independent.
	enc, err := ai.MarshalMessage(message)
	if err != nil {
		panic(err)
	}
	decoded, err := ai.UnmarshalMessage(enc)
	if err != nil {
		panic(err)
	}
	return decoded.(*ai.AssistantMessage)
}

func fauxCreateDeferredMessage(model *ai.Model, handle *ai.DeferredHandle) *ai.AssistantMessage {
	return &ai.AssistantMessage{
		Content:    nil,
		API:        model.API,
		Provider:   model.Provider,
		Model:      model.ID,
		Usage:      fauxDefaultUsage,
		StopReason: ai.StopDeferred,
		Deferred:   handle,
		Timestamp:  time.Now().UnixMilli(),
	}
}

func fauxCreateErrorMessage(err error, api, provider, modelID string) *ai.AssistantMessage {
	msg := err.Error()
	return &ai.AssistantMessage{
		Content:      nil,
		API:          api,
		Provider:     provider,
		Model:        modelID,
		Usage:        fauxDefaultUsage,
		StopReason:   ai.StopError,
		ErrorMessage: &msg,
		Timestamp:    time.Now().UnixMilli(),
	}
}

func fauxCreateAbortedMessage(partial *ai.AssistantMessage) *ai.AssistantMessage {
	cloned := fauxCloneForEvent(partial)
	cloned.StopReason = ai.StopAborted
	msg := "Request was aborted"
	cloned.ErrorMessage = &msg
	cloned.Timestamp = time.Now().UnixMilli()
	return cloned
}

func fauxAborted(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	select {
	case <-ctx.Done():
		return true
	default:
		return false
	}
}

func fauxScheduleChunk(chunk string, tokensPerSecond float64) {
	if tokensPerSecond <= 0 {
		return
	}
	delay := (float64(fauxEstimateTokens(chunk)) / tokensPerSecond) * 1000
	time.Sleep(time.Duration(delay) * time.Millisecond)
}

func (c *FauxCore) streamWithDeltas(
	stream *ai.AssistantMessageEventStream,
	message *ai.AssistantMessage,
	ctx context.Context,
) {
	partial := fauxCloneForEvent(message)
	partial.Content = nil
	partial.StopReason = ai.StopPending
	if fauxAborted(ctx) {
		aborted := fauxCreateAbortedMessage(partial)
		stream.Push(ai.AssistantMessageEvent{Type: ai.EventError, Reason: ai.StopAborted, Error: aborted})
		stream.End(&aborted)
		return
	}

	stream.Push(ai.AssistantMessageEvent{Type: ai.EventStart, Partial: partial})

	for index := 0; index < len(message.Content); index++ {
		if fauxAborted(ctx) {
			aborted := fauxCreateAbortedMessage(partial)
			stream.Push(ai.AssistantMessageEvent{Type: ai.EventError, Reason: ai.StopAborted, Error: aborted})
			stream.End(&aborted)
			return
		}

		switch block := message.Content[index].(type) {
		case ai.ThinkingContent:
			partial.Content = append(partial.Content, ai.ThinkingContent{})
			stream.Push(ai.AssistantMessageEvent{Type: ai.EventThinkingStart, ContentIndex: index, Partial: partial})
			for _, chunk := range fauxSplitStringByTokenSize(block.Thinking, c.minTokenSize, c.maxTokenSize) {
				fauxScheduleChunk(chunk, c.tokensPerSecond())
				if fauxAborted(ctx) {
					aborted := fauxCreateAbortedMessage(partial)
					stream.Push(ai.AssistantMessageEvent{Type: ai.EventError, Reason: ai.StopAborted, Error: aborted})
					stream.End(&aborted)
					return
				}
				partial.Content[index] = ai.ThinkingContent{Thinking: partial.Content[index].(ai.ThinkingContent).Thinking + chunk}
				stream.Push(ai.AssistantMessageEvent{Type: ai.EventThinkingDelta, ContentIndex: index, Delta: chunk, Partial: partial})
			}
			stream.Push(ai.AssistantMessageEvent{Type: ai.EventThinkingEnd, ContentIndex: index, Content: block.Thinking, Partial: partial})

		case ai.TextContent:
			partial.Content = append(partial.Content, ai.TextContent{})
			stream.Push(ai.AssistantMessageEvent{Type: ai.EventTextStart, ContentIndex: index, Partial: partial})
			for _, chunk := range fauxSplitStringByTokenSize(block.Text, c.minTokenSize, c.maxTokenSize) {
				fauxScheduleChunk(chunk, c.tokensPerSecond())
				if fauxAborted(ctx) {
					aborted := fauxCreateAbortedMessage(partial)
					stream.Push(ai.AssistantMessageEvent{Type: ai.EventError, Reason: ai.StopAborted, Error: aborted})
					stream.End(&aborted)
					return
				}
				partial.Content[index] = ai.TextContent{Text: partial.Content[index].(ai.TextContent).Text + chunk}
				stream.Push(ai.AssistantMessageEvent{Type: ai.EventTextDelta, ContentIndex: index, Delta: chunk, Partial: partial})
			}
			stream.Push(ai.AssistantMessageEvent{Type: ai.EventTextEnd, ContentIndex: index, Content: block.Text, Partial: partial})

		case ai.ToolCall:
			partial.Content = append(partial.Content, ai.ToolCall{ID: block.ID, Name: block.Name, Arguments: json.RawMessage("{}")})
			stream.Push(ai.AssistantMessageEvent{Type: ai.EventToolcallStart, ContentIndex: index, Partial: partial})
			argsJSON, err := ai.MarshalJSON(json.RawMessage(block.Arguments))
			if err != nil {
				argsJSON = []byte("null")
			}
			for _, chunk := range fauxSplitStringByTokenSize(string(argsJSON), c.minTokenSize, c.maxTokenSize) {
				fauxScheduleChunk(chunk, c.tokensPerSecond())
				if fauxAborted(ctx) {
					aborted := fauxCreateAbortedMessage(partial)
					stream.Push(ai.AssistantMessageEvent{Type: ai.EventError, Reason: ai.StopAborted, Error: aborted})
					stream.End(&aborted)
					return
				}
				stream.Push(ai.AssistantMessageEvent{Type: ai.EventToolcallDelta, ContentIndex: index, Delta: chunk, Partial: partial})
			}
			partialBlock := partial.Content[index].(ai.ToolCall)
			partialBlock.Arguments = block.Arguments
			partial.Content[index] = partialBlock
			stream.Push(ai.AssistantMessageEvent{Type: ai.EventToolcallEnd, ContentIndex: index, ToolCall: &block, Partial: partial})

		default:
			panic(fmt.Sprintf("providers: unsupported faux content block %T", message.Content[index]))
		}
	}

	if message.StopReason == ai.StopPending || message.StopReason == "" {
		err := fmt.Errorf("Faux response ended without a stop reason")
		msg := fauxCreateErrorMessage(err, c.api, c.provider, message.Model)
		stream.Push(ai.AssistantMessageEvent{Type: ai.EventError, Reason: ai.StopError, Error: msg})
		stream.End(&msg)
		return
	}
	if message.StopReason == ai.StopError || message.StopReason == ai.StopAborted {
		stream.Push(ai.AssistantMessageEvent{Type: ai.EventError, Reason: message.StopReason, Error: message})
		stream.End(&message)
		return
	}

	stream.Push(ai.AssistantMessageEvent{Type: ai.EventDone, Reason: message.StopReason, Message: message})
	stream.End(&message)
}

func (c *FauxCore) tokensPerSecond() float64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.tps
}

func (c *FauxCore) resolveResponse(
	step FauxResponseStep,
	context ai.TranscriptContext,
	options *ai.SimpleStreamOptions,
	requestModel *ai.Model,
) (*ai.AssistantMessage, error) {
	resolved := step.Message
	if step.Factory != nil {
		msg, err := step.Factory(context, options, &c.state, requestModel)
		if err != nil {
			return nil, err
		}
		resolved = msg
	}
	return fauxWithUsageEstimate(fauxReattribute(resolved, c.api, c.provider, requestModel.ID), context, streamOptionsOf(options), c.promptCache, &c.cacheMu), nil
}

func streamOptionsOf(options *ai.SimpleStreamOptions) *ai.StreamOptions {
	if options == nil {
		return nil
	}
	return &options.StreamOptions
}

func ctxOf(options *ai.StreamOptions) context.Context {
	if options != nil && options.Ctx != nil {
		return options.Ctx
	}
	return context.Background()
}

func ctxOfSimple(options *ai.SimpleStreamOptions) context.Context {
	return ctxOf(streamOptionsOf(options))
}

// Stream implements the faux stream function.
func (c *FauxCore) Stream(requestModel *ai.Model, context ai.TranscriptContext, options *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
	outer := ai.NewAssistantMessageEventStream()
	step, hasStep := c.shiftResponse()
	c.state.CallCount++

	go func() {
		ctx := ctxOf(streamOptionsOf(options))
		streamOpts := streamOptionsOf(options)
		if streamOpts != nil && streamOpts.OnResponse != nil {
			streamOpts.OnResponse(ai.ProviderResponse{Status: 200, Headers: map[string]string{}}, requestModel)
		}
		if !hasStep {
			message := fauxWithUsageEstimate(
				fauxCreateErrorMessage(fmt.Errorf("No more faux responses queued"), c.api, c.provider, requestModel.ID),
				context, streamOpts, c.promptCache, &c.cacheMu)
			outer.Push(ai.AssistantMessageEvent{Type: ai.EventError, Reason: ai.StopError, Error: message})
			outer.End(&message)
			return
		}

		if streamOpts != nil && streamOpts.Deferred != nil {
			handle := &ai.DeferredHandle{
				Provider: requestModel.Provider,
				ModelID:  requestModel.ID,
				API:      requestModel.API,
				ID:       fauxRandomID("deferred"),
			}
			c.mu.Lock()
			if c.deferral.pollAfterMs != nil {
				ms := *c.deferral.pollAfterMs
				handle.PollAfterMs = &ms
			}
			pending := max(0, c.deferral.pendingFetches)
			c.deferredResponses[handle.ID] = &fauxDeferredEntry{
				handle:         *handle,
				step:           step,
				context:        context,
				options:        options,
				model:          requestModel,
				pendingFetches: pending,
			}
			c.mu.Unlock()
			c.streamWithDeltas(outer, fauxCreateDeferredMessage(requestModel, handle), ctx)
			return
		}

		message, err := c.resolveResponse(step, context, options, requestModel)
		if err != nil {
			msg := fauxCreateErrorMessage(err, c.api, c.provider, requestModel.ID)
			outer.Push(ai.AssistantMessageEvent{Type: ai.EventError, Reason: ai.StopError, Error: msg})
			outer.End(&msg)
			return
		}
		c.streamWithDeltas(outer, message, ctx)
	}()

	return outer
}

// StreamSimple implements the faux streamSimple function (same behavior).
func (c *FauxCore) StreamSimple(model *ai.Model, context ai.TranscriptContext, options *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
	return c.Stream(model, context, options)
}

// FetchDeferred implements the faux fetchDeferred function.
func (c *FauxCore) FetchDeferred(requestModel *ai.Model, handle *ai.DeferredHandle, fetchOptions *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
	outer := ai.NewAssistantMessageEventStream()
	c.state.DeferredFetchCount++

	go func() {
		ctx := ctxOfSimple(fetchOptions)
		if fetchOptions != nil && fetchOptions.OnResponse != nil {
			fetchOptions.OnResponse(ai.ProviderResponse{Status: 200, Headers: map[string]string{}}, requestModel)
		}
		c.mu.Lock()
		entry, ok := c.deferredResponses[handle.ID]
		c.mu.Unlock()
		if !ok ||
			entry.handle.Provider != handle.Provider ||
			entry.handle.ModelID != handle.ModelID ||
			entry.handle.API != handle.API {
			msg := fauxCreateErrorMessage(fmt.Errorf("Unknown faux deferred response: %s", handle.ID), c.api, c.provider, requestModel.ID)
			outer.Push(ai.AssistantMessageEvent{Type: ai.EventError, Reason: ai.StopError, Error: msg})
			outer.End(&msg)
			return
		}
		if entry.cancelled {
			msg := fauxCreateErrorMessage(fmt.Errorf("Faux deferred response was cancelled: %s", handle.ID), c.api, c.provider, requestModel.ID)
			outer.Push(ai.AssistantMessageEvent{Type: ai.EventError, Reason: ai.StopError, Error: msg})
			outer.End(&msg)
			return
		}

		c.mu.Lock()
		if entry.pendingFetches > 0 {
			entry.pendingFetches--
			c.mu.Unlock()
			c.streamWithDeltas(outer, fauxCreateDeferredMessage(requestModel, &entry.handle), ctx)
			return
		}
		c.mu.Unlock()

		if entry.final == nil {
			// Submission options minus deferred/signal/onResponse (upstream
			// destructures them away).
			submission := *entry.options
			submission.Deferred = nil
			submission.Ctx = nil
			submission.OnResponse = nil
			msg, err := c.resolveResponse(entry.step, entry.context, &submission, entry.model)
			if err != nil {
				msg = fauxCreateErrorMessage(err, c.api, c.provider, entry.model.ID)
			}
			c.mu.Lock()
			entry.final = msg
			c.mu.Unlock()
		}
		c.streamWithDeltas(outer, entry.final, ctx)
	}()

	return outer
}

// CancelDeferred implements the faux cancelDeferred function.
func (c *FauxCore) CancelDeferred(requestModel *ai.Model, handle *ai.DeferredHandle, cancelOptions *ai.SimpleStreamOptions) error {
	cloned := *handle
	c.mu.Lock()
	c.state.CancelledDeferred = append(c.state.CancelledDeferred, &cloned)
	if entry, ok := c.deferredResponses[handle.ID]; ok {
		entry.cancelled = true
	}
	c.mu.Unlock()
	if cancelOptions != nil && cancelOptions.OnResponse != nil {
		cancelOptions.OnResponse(ai.ProviderResponse{Status: 200, Headers: map[string]string{}}, requestModel)
	}
	return nil
}

var _ = math.Ceil // retained for parity notes

// FauxProviderHandle is the faux provider registration surface (upstream
// FauxProviderHandle minus the global-registry unregister, which arrives
// with the API registry port).
type FauxProviderHandle struct {
	Provider *ai.Provider
	Core     *FauxCore
}

// FauxProvider builds a faux provider through CreateProvider for tests built
// on explicit Models collections (port of fauxProvider()).
//
//	var faux = providers.FauxProvider(providers.FauxOptions{})
//	models := ai.CreateModels(nil)
//	models.SetProvider(faux.Provider)
//	faux.Core.SetResponses([]providers.FauxResponseStep{{Message: ...}})
func FauxProvider(options FauxOptions) *FauxProviderHandle {
	core := NewFauxCore(options)
	provider := ai.CreateProvider(ai.CreateProviderOptions{
		ID:     core.Provider(),
		Auth:   ai.ProviderAuth{APIKey: &ai.ApiKeyAuth{Name: "Faux", Resolve: func(ai.AuthResolveInput) (*ai.AuthResult, error) { return &ai.AuthResult{Auth: ai.ModelAuth{}}, nil }}},
		Models: core.Models(),
		Single: fauxStreams{core},
	})
	return &FauxProviderHandle{Provider: provider, Core: core}
}

// fauxStreams adapts FauxCore to the ProviderStreams contract.
type fauxStreams struct{ core *FauxCore }

func (f fauxStreams) Stream(model *ai.Model, context ai.TranscriptContext, options *ai.StreamOptions) *ai.AssistantMessageEventStream {
	simple := &ai.SimpleStreamOptions{}
	if options != nil {
		simple.StreamOptions = *options
	}
	return f.core.Stream(model, context, simple)
}

func (f fauxStreams) StreamSimple(model *ai.Model, context ai.TranscriptContext, options *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
	return f.core.Stream(model, context, options)
}

func (f fauxStreams) FetchDeferred(model *ai.Model, handle *ai.DeferredHandle, options *ai.StreamOptions) *ai.AssistantMessageEventStream {
	simple := &ai.SimpleStreamOptions{}
	if options != nil {
		simple.StreamOptions = *options
	}
	return f.core.FetchDeferred(model, handle, simple)
}

func (f fauxStreams) CancelDeferred(model *ai.Model, handle *ai.DeferredHandle, options *ai.StreamOptions) error {
	simple := &ai.SimpleStreamOptions{}
	if options != nil {
		simple.StreamOptions = *options
	}
	return f.core.CancelDeferred(model, handle, simple)
}
