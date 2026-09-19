// Package agent is a faithful Go port of @earendil-works/pi-agent-core
// (pi/packages/agent): the low-level agent loop and its event protocol.
//
// Ground truth: pi/packages/agent/src/{agent-loop.ts, types.ts} at the
// pinned upstream commit.
package agent

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/dat267/gpi/ai"
)

// Port of packages/agent/src/types.ts.

// StreamFn is the stream function used by the agent loop; Models.StreamSimple
// satisfies this shape. The loop passes a normalized transcript: the system
// prompt and tool declarations are carried by the transcript's system
// messages. It must not throw for request/model/runtime failures — failures
// are encoded in the returned stream.
type StreamFn func(model *ai.Model, context ai.TranscriptContext, options *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream

var defaultStreamFn StreamFn

// SetDefaultStreamFn configures the fallback used by Agent and low-level
// loops when callers omit streamFn (port of stream-fn.ts).
func SetDefaultStreamFn(streamFn StreamFn) { defaultStreamFn = streamFn }

// GetDefaultStreamFn returns the configured fallback.
func GetDefaultStreamFn() (StreamFn, error) {
	if defaultStreamFn == nil {
		return nil, fmt.Errorf("No default stream function configured. Pass streamFn explicitly or call SetDefaultStreamFn().")
	}
	return defaultStreamFn, nil
}

// ToolExecutionMode configures how tool calls from one assistant message run.
type ToolExecutionMode = string

const (
	// ToolExecutionSequential prepares, executes, and finalizes each tool
	// call before the next one starts.
	ToolExecutionSequential ToolExecutionMode = "sequential"
	// ToolExecutionParallel prepares sequentially, executes concurrently,
	// emits tool_execution_end in completion order, then emits tool-result
	// messages in assistant source order.
	ToolExecutionParallel ToolExecutionMode = "parallel"
)

// QueueMode controls how many queued user messages are injected at a drain
// point. (Unused by the low-level loop; part of the surface for Agent.)
type QueueMode = string

const (
	QueueModeAll        QueueMode = "all"
	QueueModeOneAtATime QueueMode = "one-at-a-time"
)

// AgentToolResult is the final or partial result produced by a tool.
type AgentToolResult struct {
	// Content is the text/image content returned to the model.
	Content []ai.Content
	// Details is arbitrary structured details for logs or UI rendering.
	Details json.RawMessage
	// Usage from the final tool execution itself, if available. Not used for
	// main LLM context accounting.
	Usage *ai.Usage
	// Terminate hints that the agent should stop after the current tool
	// batch. Early termination only happens when every finalized tool
	// result in the batch sets this to true.
	Terminate bool
}

// AgentTool is a tool definition used by the agent runtime. Execute throws
// (returns an error) on failure instead of encoding errors in content.
type AgentTool struct {
	Name        string
	Description string
	// Parameters is the tool's JSON Schema.
	Parameters json.RawMessage
	// Label is a human-readable label for UI display.
	Label string
	// PrepareArguments is an optional compatibility shim for raw tool-call
	// arguments before schema validation.
	PrepareArguments func(args json.RawMessage) json.RawMessage
	// Execute runs the tool call with ctx as the abort signal; onUpdate
	// streams partial results (calls after settle are ignored by the loop).
	Execute func(toolCallID string, params json.RawMessage, ctx context.Context, onUpdate func(AgentToolResult)) (AgentToolResult, error)
	// Replay is the recovery policy for an effect whose durable intent
	// exists but whose outcome is unknown: "never" or "safe".
	Replay string
	// ExecutionMode overrides the per-run default.
	ExecutionMode ToolExecutionMode
}

// ToolDeclaration projects the tool onto its ai.Tool declaration.
func (t AgentTool) ToolDeclaration() ai.Tool {
	return ai.Tool{Name: t.Name, Description: t.Description, Parameters: t.Parameters}
}

// AgentContext is the context snapshot passed into the low-level loop.
type AgentContext struct {
	// Messages is the transcript visible to the model.
	Messages []ai.Message
	// Tools are the tools available for execution in this run.
	Tools []AgentTool
}

// BeforeToolCallResult returned from BeforeToolCall: block:true prevents
// execution and emits an error tool result instead.
type BeforeToolCallResult struct {
	Block bool
	// Reason becomes the blocked result's text; a default is used when empty.
	Reason string
	// Terminate participates in the batch early-termination rule.
	Terminate bool
}

// AfterToolCallResult is the partial override returned from AfterToolCall.
// Merge is field-by-field; omitted fields keep executed values.
type AfterToolCallResult struct {
	Content    []ai.Content
	HasContent bool
	Details    json.RawMessage
	HasDetails bool
	IsError    *bool
	Usage      *ai.Usage
	Terminate  *bool
}

// BeforeToolCallContext is passed to BeforeToolCall.
type BeforeToolCallContext struct {
	AssistantMessage *ai.AssistantMessage
	ToolCall         ai.ToolCall
	// Args are the validated arguments for the tool schema.
	Args    json.RawMessage
	Context AgentContext
}

// AfterToolCallContext is passed to AfterToolCall.
type AfterToolCallContext struct {
	AssistantMessage *ai.AssistantMessage
	ToolCall         ai.ToolCall
	Args             json.RawMessage
	Result           AgentToolResult
	IsError          bool
	Context          AgentContext
}

// ShouldStopAfterTurnContext is passed to ShouldStopAfterTurn.
type ShouldStopAfterTurnContext struct {
	Message     *ai.AssistantMessage
	ToolResults []ai.Message
	Context     AgentContext
	// NewMessages are the messages this loop invocation returns if it exits
	// here (prompt runs include the initial prompts; continuation runs do
	// not include pre-existing context).
	NewMessages []ai.Message
}

// AgentLoopTurnUpdate is replacement runtime state before the next request.
type AgentLoopTurnUpdate struct {
	Context          *AgentContext
	Messages         []ai.Message
	Model            *ai.Model
	ThinkingLevel    ai.ThinkingLevel
	HasThinkingLevel bool
}

// AgentLoopConfig configures the loop. It embeds the SimpleStreamOptions
// passed to the stream function.
type AgentLoopConfig struct {
	ai.SimpleStreamOptions
	Model *ai.Model

	// ConvertToLlm maps AgentMessages to LLM messages before each call.
	// Must not return an error; filter or map unconvertible messages instead.
	ConvertToLlm func(messages []ai.Message) []ai.Message
	// TransformContext applies a context transform before ConvertToLlm.
	TransformContext func(messages []ai.Message, ctx context.Context) []ai.Message
	// GetAPIKey resolves an API key per call (expiring tokens).
	GetAPIKey func(provider string, ctx context.Context) (string, error)
	// ShouldStopAfterTurn runs after turn_end; true exits before polling
	// queues.
	ShouldStopAfterTurn func(context *ShouldStopAfterTurnContext) bool
	// PrepareNextTurn runs before the next turn when continuing.
	PrepareNextTurn func(context *ShouldStopAfterTurnContext) (*AgentLoopTurnUpdate, error)
	// GetSteeringMessages returns mid-run steering messages.
	GetSteeringMessages func(ctx context.Context) ([]ai.Message, error)
	// GetFollowUpMessages returns post-run follow-up messages.
	GetFollowUpMessages func(ctx context.Context) ([]ai.Message, error)
	// ToolExecution mode; default "parallel".
	ToolExecution ToolExecutionMode
	// BeforeToolCall runs before execution, after validation.
	BeforeToolCall func(context *BeforeToolCallContext, ctx context.Context) (*BeforeToolCallResult, error)
	// AfterToolCall runs after execution, before the end events.
	AfterToolCall func(context *AfterToolCallContext, ctx context.Context) (*AfterToolCallResult, error)
}

// AgentEventType discriminates agent events.
type AgentEventType = string

const (
	AgentStart          AgentEventType = "agent_start"
	AgentEnd            AgentEventType = "agent_end"
	TurnStart           AgentEventType = "turn_start"
	TurnEnd             AgentEventType = "turn_end"
	MessageStart        AgentEventType = "message_start"
	MessageUpdate       AgentEventType = "message_update"
	MessageEnd          AgentEventType = "message_end"
	ToolExecutionStart  AgentEventType = "tool_execution_start"
	ToolExecutionUpdate AgentEventType = "tool_execution_update"
	ToolExecutionEnd    AgentEventType = "tool_execution_end"
)

// AgentEvent is one event of the agent run protocol.
type AgentEvent struct {
	Type AgentEventType

	// Messages on agent_end.
	Messages []ai.Message

	// TurnEnd/message lifecycle payload (assistant message for turn_end;
	// any ai.Message for message_start/update/end).
	Message     ai.Message
	ToolResults []ai.Message
	// AssistantMessageEvent is message_update's inner assistant event.
	AssistantMessageEvent *ai.AssistantMessageEvent

	// tool execution lifecycle
	ToolCallID    string
	ToolName      string
	Args          json.RawMessage
	PartialResult AgentToolResult
	Result        AgentToolResult
	IsError       bool
}
