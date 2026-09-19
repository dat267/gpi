package ai

// AssistantMessageEventType discriminates stream protocol events.
type AssistantMessageEventType = string

const (
	EventStart         AssistantMessageEventType = "start"
	EventTextStart     AssistantMessageEventType = "text_start"
	EventTextDelta     AssistantMessageEventType = "text_delta"
	EventTextEnd       AssistantMessageEventType = "text_end"
	EventThinkingStart AssistantMessageEventType = "thinking_start"
	EventThinkingDelta AssistantMessageEventType = "thinking_delta"
	EventThinkingEnd   AssistantMessageEventType = "thinking_end"
	EventToolcallStart AssistantMessageEventType = "toolcall_start"
	EventToolcallDelta AssistantMessageEventType = "toolcall_delta"
	EventToolcallEnd   AssistantMessageEventType = "toolcall_end"
	EventDone          AssistantMessageEventType = "done"
	EventError         AssistantMessageEventType = "error"
)

// AssistantMessageEvent is one event of the stream protocol.
//
// Successful streams emit start before partial updates and terminate with
// done. A stream may terminate directly with error when request setup fails
// before generation starts; after start, failures also terminate with error.
// Updates and done must never appear before start.
//
// Partial is the shared live response-so-far helper, not an event-time
// snapshot. Text and thinking blocks are empty when their *_start event is
// emitted and grow only through their corresponding *_delta events until the
// authoritative *_end. Redacted thinking may be complete at start and emit
// no deltas. Tool-call arguments at toolcall_start are provider-specific;
// toolcall_delta carries subsequent JSON updates.
type AssistantMessageEvent struct {
	Type AssistantMessageEventType `json:"type"`

	// Partial is the live response-so-far. Set on every event except done
	// and error.
	Partial *AssistantMessage `json:"partial,omitempty"`

	// ContentIndex is set on the *_start/*_delta/*_end events.
	ContentIndex int `json:"contentIndex,omitempty"`

	// Delta is set on text_delta and toolcall_delta (and thinking_delta).
	Delta string `json:"delta,omitempty"`

	// Content is the authoritative final text on text_end and thinking_end.
	Content string `json:"content,omitempty"`

	// ToolCall is the authoritative final call on toolcall_end.
	ToolCall *ToolCall `json:"toolCall,omitempty"`

	// Message is set on done: the final assistant message.
	Message *AssistantMessage `json:"message,omitempty"`

	// Reason is the terminal stop reason: "stop"|"length"|"toolUse"|"deferred"
	// on done, "aborted"|"error" on error.
	Reason StopReason `json:"reason,omitempty"`

	// Error is set on error: the assistant message carrying the failure.
	Error *AssistantMessage `json:"error,omitempty"`
}

// IsTerminalEvent reports whether an event terminates a stream.
func IsTerminalEvent(e AssistantMessageEvent) bool {
	return e.Type == EventDone || e.Type == EventError
}
