package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/dat267/gpi/ai"
)

// Port of packages/agent/src/agent-loop.ts.

// AgentEventSink receives loop events.
type AgentEventSink func(event AgentEvent) error

// AgentLoop starts an agent loop with new prompt messages. The prompt is
// added to the context and events are emitted for it. Returns a stream whose
// result is the new messages when agent_end fires.
func AgentLoop(
	prompts []ai.Message,
	context AgentContext,
	config *AgentLoopConfig,
	ctx context.Context,
	streamFn StreamFn,
) *ai.EventStream[AgentEvent, []ai.Message] {
	stream := ai.NewEventStream(
		func(event AgentEvent) bool { return event.Type == AgentEnd },
		func(event AgentEvent) []ai.Message {
			if event.Type == AgentEnd {
				return event.Messages
			}
			return nil
		},
	)
	go func() {
		messages := RunAgentLoop(prompts, context, config, ctx, func(event AgentEvent) error {
			stream.Push(event)
			return nil
		}, streamFn)
		stream.End(&messages)
	}()
	return stream
}

// AgentLoopContinue continues an agent loop from the current context without
// adding a new message; used for retries. The last context message must
// convert to a user or toolResult message via ConvertToLlm.
func AgentLoopContinue(
	context AgentContext,
	config *AgentLoopConfig,
	ctx context.Context,
	streamFn StreamFn,
) *ai.EventStream[AgentEvent, []ai.Message] {
	validateContinue(context)
	stream := ai.NewEventStream(
		func(event AgentEvent) bool { return event.Type == AgentEnd },
		func(event AgentEvent) []ai.Message {
			if event.Type == AgentEnd {
				return event.Messages
			}
			return nil
		},
	)
	go func() {
		messages := RunAgentLoopContinue(context, config, ctx, func(event AgentEvent) error {
			stream.Push(event)
			return nil
		}, streamFn)
		stream.End(&messages)
	}()
	return stream
}

func validateContinue(context AgentContext) {
	if len(context.Messages) == 0 {
		panic(fmt.Errorf("Cannot continue: no messages in context"))
	}
	if ai.RoleOf(context.Messages[len(context.Messages)-1]) == ai.RoleAssistant {
		panic(fmt.Errorf("Cannot continue from message role: assistant"))
	}
}

// RunAgentLoop runs the loop to completion synchronously (port of
// runAgentLoop).
func RunAgentLoop(
	prompts []ai.Message,
	context AgentContext,
	config *AgentLoopConfig,
	ctx context.Context,
	emit AgentEventSink,
	streamFn StreamFn,
) []ai.Message {
	initialMessages := declareToolChanges(context, prompts)
	newMessages := append([]ai.Message{}, initialMessages...)
	context.Messages = append(append([]ai.Message{}, context.Messages...), initialMessages...)

	mustEmit(emit, AgentEvent{Type: AgentStart})
	mustEmit(emit, AgentEvent{Type: TurnStart})
	for _, message := range initialMessages {
		mustEmit(emit, AgentEvent{Type: MessageStart, Message: message})
		mustEmit(emit, AgentEvent{Type: MessageEnd, Message: message})
	}

	runLoop(&context, &newMessages, config, ctx, emit, streamFn)
	return newMessages
}

// RunAgentLoopContinue runs a continuation to completion (port of
// runAgentLoopContinue).
func RunAgentLoopContinue(
	context AgentContext,
	config *AgentLoopConfig,
	ctx context.Context,
	emit AgentEventSink,
	streamFn StreamFn,
) []ai.Message {
	validateContinue(context)
	newMessages := []ai.Message{}
	mustEmit(emit, AgentEvent{Type: AgentStart})
	mustEmit(emit, AgentEvent{Type: TurnStart})
	runLoop(&context, &newMessages, config, ctx, emit, streamFn)
	return newMessages
}

func mustEmit(emit AgentEventSink, event AgentEvent) {
	if err := emit(event); err != nil {
		panic(err)
	}
}

// runLoop is the logic shared by AgentLoop and AgentLoopContinue (port of
// runLoop).
func runLoop(
	initialContext *AgentContext,
	newMessages *[]ai.Message,
	initialConfig *AgentLoopConfig,
	ctx context.Context,
	emit AgentEventSink,
	streamFunction StreamFn,
) {
	currentContext := *initialContext
	config := *initialConfig
	var lastCompletedTurn *ShouldStopAfterTurnContext
	// Check for steering messages at start (user may have typed while waiting).
	var pendingMessages []ai.Message
	if config.GetSteeringMessages != nil {
		pendingMessages, _ = config.GetSteeringMessages(ctx)
	}

	// Outer loop: continues when queued follow-up messages arrive after the
	// agent would stop.
	for {
		hasMoreToolCalls := true

		// Inner loop: process tool calls and steering messages.
		for hasMoreToolCalls || len(pendingMessages) > 0 {
			var preparedMessages []ai.Message
			if lastCompletedTurn != nil {
				if config.PrepareNextTurn != nil {
					if nextTurnSnapshot, err := config.PrepareNextTurn(lastCompletedTurn); err == nil && nextTurnSnapshot != nil {
						if nextTurnSnapshot.Context != nil {
							currentContext = *nextTurnSnapshot.Context
						}
						preparedMessages = nextTurnSnapshot.Messages
						if nextTurnSnapshot.Model != nil {
							config.Model = nextTurnSnapshot.Model
						}
						if nextTurnSnapshot.HasThinkingLevel {
							if nextTurnSnapshot.ThinkingLevel == ai.ThinkOff {
								config.Reasoning = ""
							} else {
								config.Reasoning = nextTurnSnapshot.ThinkingLevel
							}
						}
					}
				}
				// Preparation can be long-running (e.g. compaction). Pick up
				// steering queued while it ran — but only poll again if the
				// earlier poll returned nothing (one-at-a-time mode would
				// otherwise deliver two messages in one turn).
				if len(pendingMessages) == 0 && config.GetSteeringMessages != nil {
					pendingMessages, _ = config.GetSteeringMessages(ctx)
				}
				mustEmit(emit, AgentEvent{Type: TurnStart})
			}

			// Process prepared and queued messages before the next assistant
			// response.
			batch := append(append([]ai.Message{}, preparedMessages...), pendingMessages...)
			for _, message := range declareToolChanges(currentContext, batch) {
				mustEmit(emit, AgentEvent{Type: MessageStart, Message: message})
				mustEmit(emit, AgentEvent{Type: MessageEnd, Message: message})
				currentContext.Messages = append(currentContext.Messages, message)
				*newMessages = append(*newMessages, message)
			}
			pendingMessages = nil

			// Stream assistant response.
			message := streamAssistantResponse(&currentContext, &config, ctx, emit, streamFunction)
			*newMessages = append(*newMessages, message)

			if message.StopReason == ai.StopError || message.StopReason == ai.StopAborted {
				mustEmit(emit, AgentEvent{Type: TurnEnd, Message: message, ToolResults: []ai.Message{}})
				mustEmit(emit, AgentEvent{Type: AgentEnd, Messages: *newMessages})
				return
			}

			// Check for tool calls.
			var toolCalls []ai.ToolCall
			for _, block := range message.Content {
				if tc, ok := block.(ai.ToolCall); ok {
					toolCalls = append(toolCalls, tc)
				}
			}

			var toolResults []ai.Message
			hasMoreToolCalls = false
			if len(toolCalls) > 0 {
				// A "length" stop means output was cut off by the token
				// limit: every tool call may carry truncated arguments. Fail
				// them all instead of executing potentially borked calls.
				var executedToolBatch executedToolCallBatch
				if message.StopReason == ai.StopLength {
					executedToolBatch = failToolCallsFromTruncatedMessage(toolCalls, emit)
				} else {
					executedToolBatch = executeToolCalls(&currentContext, message, &config, ctx, emit)
				}
				toolResults = append(toolResults, executedToolBatch.messages...)
				hasMoreToolCalls = !executedToolBatch.terminate

				for _, result := range toolResults {
					currentContext.Messages = append(currentContext.Messages, result)
					*newMessages = append(*newMessages, result)
				}
			}

			mustEmit(emit, AgentEvent{Type: TurnEnd, Message: message, ToolResults: toolResults})

			lastCompletedTurn = &ShouldStopAfterTurnContext{
				Message: message, ToolResults: toolResults,
				Context: currentContext, NewMessages: *newMessages,
			}

			if config.ShouldStopAfterTurn != nil && config.ShouldStopAfterTurn(lastCompletedTurn) {
				mustEmit(emit, AgentEvent{Type: AgentEnd, Messages: *newMessages})
				return
			}

			if config.GetSteeringMessages != nil {
				pendingMessages, _ = config.GetSteeringMessages(ctx)
			}
		}

		// Agent would stop here. Check for follow-up messages.
		var followUpMessages []ai.Message
		if config.GetFollowUpMessages != nil {
			followUpMessages, _ = config.GetFollowUpMessages(ctx)
		}
		if len(followUpMessages) > 0 {
			pendingMessages = followUpMessages
			continue
		}
		break
	}

	mustEmit(emit, AgentEvent{Type: AgentEnd, Messages: *newMessages})
}

// declareToolChanges announces tool loadout changes to the model: the
// difference between the transcript's declared tools and the executable set
// becomes toolsAdded/toolsRemoved on a system message. A pending system
// message's tool fields are treated as intent and replaced with the delta;
// otherwise a new system message is inserted before the first non-system
// pending message.
func declareToolChanges(context AgentContext, pendingMessages []ai.Message) []ai.Message {
	systemIndex := -1
	for i := len(pendingMessages) - 1; i >= 0; i-- {
		if ai.RoleOf(pendingMessages[i]) == ai.RoleSystem {
			systemIndex = i
			break
		}
	}
	var pending *ai.SystemMessage
	if systemIndex >= 0 {
		pending, _ = pendingMessages[systemIndex].(*ai.SystemMessage)
	}
	baseline := pendingMessages
	if pending != nil {
		baseline = append([]ai.Message{}, pendingMessages...)
		baseline[systemIndex] = withToolChanges(pending, ai.ToolStateChanges{})
	}

	var executableTools []ai.Tool
	for _, tool := range context.Tools {
		executableTools = append(executableTools, tool.ToolDeclaration())
	}
	declaredTools := ai.GetCurrentTools(append(append([]ai.Message{}, context.Messages...), baseline...))
	changes := ai.GetToolStateChanges(declaredTools, executableTools)
	unchanged := len(changes.ToolsAdded) == 0 && len(changes.ToolsRemoved) == 0

	if pending != nil {
		// Keep the caller's message object when it already declares no tool
		// changes.
		if unchanged && len(pending.ToolsAdded) == 0 && len(pending.ToolsRemoved) == 0 {
			return pendingMessages
		}
		updated := append([]ai.Message{}, baseline...)
		updated[systemIndex] = withToolChanges(pending, changes)
		return updated
	}
	if unchanged {
		return pendingMessages
	}
	update := withToolChanges(&ai.SystemMessage{Timestamp: time.Now().UnixMilli()}, changes)
	insertIndex := -1
	for i, message := range pendingMessages {
		if ai.RoleOf(message) != ai.RoleSystem {
			insertIndex = i
			break
		}
	}
	if insertIndex == -1 {
		insertIndex = len(pendingMessages)
	}
	out := append([]ai.Message{}, pendingMessages[:insertIndex]...)
	out = append(out, update)
	return append(out, pendingMessages[insertIndex:]...)
}

// withToolChanges copies a system message with its tool fields replaced;
// empty lists omit the field.
func withToolChanges(message *ai.SystemMessage, changes ai.ToolStateChanges) *ai.SystemMessage {
	out := message.Clone()
	out.ToolsAdded = nil
	out.ToolsRemoved = nil
	if len(changes.ToolsAdded) > 0 {
		out.ToolsAdded = changes.ToolsAdded
	}
	if len(changes.ToolsRemoved) > 0 {
		out.ToolsRemoved = changes.ToolsRemoved
	}
	return out
}

// streamAssistantResponse streams one assistant response. This is where
// AgentMessages get transformed to Messages for the LLM.
func streamAssistantResponse(
	context *AgentContext,
	config *AgentLoopConfig,
	ctx context.Context,
	emit AgentEventSink,
	streamFunction StreamFn,
) *ai.AssistantMessage {
	// Apply the context transform if configured.
	messages := context.Messages
	if config.TransformContext != nil {
		messages = config.TransformContext(messages, ctx)
	}

	// Convert to LLM-compatible messages.
	var llmMessages []ai.Message
	if config.ConvertToLlm != nil {
		llmMessages = config.ConvertToLlm(messages)
	} else {
		llmMessages = messages
	}
	llmContext := ai.NormalizeContext(ai.Context{Messages: llmMessages})

	// Resolve API key (important for expiring tokens).
	resolvedAPIKey := config.APIKey
	if config.GetAPIKey != nil {
		if key, err := config.GetAPIKey(config.Model.Provider, ctx); err == nil && key != "" {
			resolvedAPIKey = key
		}
	}

	options := config.SimpleStreamOptions
	options.APIKey = resolvedAPIKey
	options.Ctx = ctx
	response := streamFunction(config.Model, llmContext, &options)

	var partialMessage *ai.AssistantMessage
	addedPartial := false

	for {
		event, ok := response.Next(ctx)
		if !ok {
			break
		}
		switch event.Type {
		case ai.EventStart:
			partialMessage = event.Partial
			context.Messages = append(context.Messages, partialMessage)
			addedPartial = true
			mustEmit(emit, AgentEvent{Type: MessageStart, Message: partialMessage})
		case ai.EventTextStart, ai.EventTextDelta, ai.EventTextEnd,
			ai.EventThinkingStart, ai.EventThinkingDelta, ai.EventThinkingEnd,
			ai.EventToolcallStart, ai.EventToolcallDelta, ai.EventToolcallEnd:
			if partialMessage != nil {
				partialMessage = event.Partial
				context.Messages[len(context.Messages)-1] = partialMessage
				inner := event
				mustEmit(emit, AgentEvent{
					Type: MessageUpdate, AssistantMessageEvent: &inner, Message: partialMessage,
				})
			}
		case ai.EventDone, ai.EventError:
			finalMessage, resultErr := response.Result(ctx)
			if resultErr != nil {
				failStreamAssistant(resultErr)
			}
			if addedPartial {
				context.Messages[len(context.Messages)-1] = finalMessage
			} else {
				context.Messages = append(context.Messages, finalMessage)
			}
			if !addedPartial {
				mustEmit(emit, AgentEvent{Type: MessageStart, Message: finalMessage})
			}
			mustEmit(emit, AgentEvent{Type: MessageEnd, Message: finalMessage})
			return finalMessage
		}
	}

	finalMessage, resultErr := response.Result(ctx)
	if resultErr != nil {
		failStreamAssistant(resultErr)
	}
	if addedPartial {
		context.Messages[len(context.Messages)-1] = finalMessage
	} else {
		context.Messages = append(context.Messages, finalMessage)
		mustEmit(emit, AgentEvent{Type: MessageStart, Message: finalMessage})
	}
	mustEmit(emit, AgentEvent{Type: MessageEnd, Message: finalMessage})
	return finalMessage
}

// failStreamAssistant propagates stream result errors through the loop's
// panic-recovery contract (upstream throws).
func failStreamAssistant(err error) {
	if err != nil {
		panic(err)
	}
}

// failToolCallsFromTruncatedMessage fails all tool calls from an assistant
// message truncated by the output token limit: streamed arguments are
// finalized best-effort and may be silently incomplete — none are safe to
// execute.
func failToolCallsFromTruncatedMessage(toolCalls []ai.ToolCall, emit AgentEventSink) executedToolCallBatch {
	var messages []ai.Message
	for _, toolCall := range toolCalls {
		mustEmit(emit, AgentEvent{
			Type: ToolExecutionStart, ToolCallID: toolCall.ID, ToolName: toolCall.Name, Args: toolCall.Arguments,
		})
		finalized := finalizedToolCallOutcome{
			toolCall: toolCall,
			result:   createErrorToolResult(fmt.Sprintf("Tool call %q was not executed: the response hit the output token limit, so its arguments may be truncated. Re-issue the tool call with complete arguments.", toolCall.Name)),
			isError:  true,
		}
		emitToolExecutionEnd(finalized, emit)
		toolResultMessage := createToolResultMessage(finalized)
		emitToolResultMessage(toolResultMessage, emit)
		messages = append(messages, toolResultMessage)
	}
	return executedToolCallBatch{messages: messages, terminate: false}
}

// executedToolCallBatch is one round of tool execution.
type executedToolCallBatch struct {
	messages  []ai.Message
	terminate bool
}

// executeToolCalls dispatches to the sequential or parallel executor.
func executeToolCalls(
	currentContext *AgentContext,
	assistantMessage *ai.AssistantMessage,
	config *AgentLoopConfig,
	ctx context.Context,
	emit AgentEventSink,
) executedToolCallBatch {
	var toolCalls []ai.ToolCall
	for _, block := range assistantMessage.Content {
		if tc, ok := block.(ai.ToolCall); ok {
			toolCalls = append(toolCalls, tc)
		}
	}
	hasSequentialToolCall := false
	for _, tc := range toolCalls {
		for _, tool := range currentContext.Tools {
			if tool.Name == tc.Name && tool.ExecutionMode == ToolExecutionSequential {
				hasSequentialToolCall = true
			}
		}
	}
	if config.ToolExecution == ToolExecutionSequential || hasSequentialToolCall {
		return executeToolCallsSequential(currentContext, assistantMessage, toolCalls, config, ctx, emit)
	}
	return executeToolCallsParallel(currentContext, assistantMessage, toolCalls, config, ctx, emit)
}

// finalizedToolCallOutcome is one finalized call.
type finalizedToolCallOutcome struct {
	toolCall ai.ToolCall
	result   AgentToolResult
	isError  bool
}

func executeToolCallsSequential(
	currentContext *AgentContext,
	assistantMessage *ai.AssistantMessage,
	toolCalls []ai.ToolCall,
	config *AgentLoopConfig,
	ctx context.Context,
	emit AgentEventSink,
) executedToolCallBatch {
	var finalizedCalls []finalizedToolCallOutcome
	var messages []ai.Message

	for _, toolCall := range toolCalls {
		mustEmit(emit, AgentEvent{
			Type: ToolExecutionStart, ToolCallID: toolCall.ID, ToolName: toolCall.Name, Args: toolCall.Arguments,
		})

		preparation := prepareToolCall(currentContext, assistantMessage, toolCall, config, ctx)
		var finalized finalizedToolCallOutcome
		if preparation.immediate != nil {
			finalized = finalizedToolCallOutcome{
				toolCall: toolCall, result: preparation.immediate.result, isError: preparation.immediate.isError,
			}
		} else {
			executed := executePreparedToolCall(preparation.prepared, ctx, emit)
			finalized = finalizeExecutedToolCall(currentContext, assistantMessage, preparation.prepared, executed, config, ctx)
		}

		emitToolExecutionEnd(finalized, emit)
		toolResultMessage := createToolResultMessage(finalized)
		emitToolResultMessage(toolResultMessage, emit)
		finalizedCalls = append(finalizedCalls, finalized)
		messages = append(messages, toolResultMessage)

		if ctxDone(ctx) {
			break
		}
	}

	return executedToolCallBatch{
		messages:  messages,
		terminate: shouldTerminateToolBatch(finalizedCalls),
	}
}

// finalizedEntry is either a settled outcome or a deferred concurrent one
// (upstream's `FinalizedToolCallEntry` function-vs-value union).
type finalizedEntry struct {
	settled *finalizedToolCallOutcome
	run     func() finalizedToolCallOutcome
}

func executeToolCallsParallel(
	currentContext *AgentContext,
	assistantMessage *ai.AssistantMessage,
	toolCalls []ai.ToolCall,
	config *AgentLoopConfig,
	ctx context.Context,
	emit AgentEventSink,
) executedToolCallBatch {
	var entries []finalizedEntry

	for _, toolCall := range toolCalls {
		mustEmit(emit, AgentEvent{
			Type: ToolExecutionStart, ToolCallID: toolCall.ID, ToolName: toolCall.Name, Args: toolCall.Arguments,
		})

		preparation := prepareToolCall(currentContext, assistantMessage, toolCall, config, ctx)
		if preparation.immediate != nil {
			finalized := finalizedToolCallOutcome{
				toolCall: toolCall, result: preparation.immediate.result, isError: preparation.immediate.isError,
			}
			emitToolExecutionEnd(finalized, emit)
			entries = append(entries, finalizedEntry{settled: &finalized})
			if ctxDone(ctx) {
				break
			}
			continue
		}

		prepared := preparation.prepared
		entries = append(entries, finalizedEntry{run: func() finalizedToolCallOutcome {
			if ctxDone(ctx) {
				finalized := finalizedToolCallOutcome{
					toolCall: prepared.toolCall,
					result:   createErrorToolResult("Operation aborted"),
					isError:  true,
				}
				emitToolExecutionEnd(finalized, emit)
				return finalized
			}
			executed := executePreparedToolCall(prepared, ctx, emit)
			finalized := finalizeExecutedToolCall(currentContext, assistantMessage, prepared, executed, config, ctx)
			emitToolExecutionEnd(finalized, emit)
			return finalized
		}})
		if ctxDone(ctx) {
			break
		}
	}

	// Concurrent execution of deferred entries; tool_execution_end is
	// emitted in completion order (inside each goroutine).
	var wg sync.WaitGroup
	results := make([]finalizedToolCallOutcome, len(entries))
	for i, entry := range entries {
		if entry.settled != nil {
			results[i] = *entry.settled
			continue
		}
		wg.Add(1)
		go func(i int, run func() finalizedToolCallOutcome) {
			defer wg.Done()
			results[i] = run()
		}(i, entry.run)
	}
	wg.Wait()

	var messages []ai.Message
	for _, finalized := range results {
		toolResultMessage := createToolResultMessage(finalized)
		emitToolResultMessage(toolResultMessage, emit)
		messages = append(messages, toolResultMessage)
	}

	return executedToolCallBatch{
		messages:  messages,
		terminate: shouldTerminateToolBatch(results),
	}
}

type preparedToolCallValue struct {
	toolCall ai.ToolCall
	tool     *AgentTool
	args     json.RawMessage
}

type immediateToolCallOutcome struct {
	result  AgentToolResult
	isError bool
}

type prepareOutcome struct {
	prepared  *preparedToolCallValue
	immediate *immediateToolCallOutcome
}

func shouldTerminateToolBatch(finalizedCalls []finalizedToolCallOutcome) bool {
	if len(finalizedCalls) == 0 {
		return false
	}
	for _, finalized := range finalizedCalls {
		if !finalized.result.Terminate {
			return false
		}
	}
	return true
}

func prepareToolCallArguments(tool *AgentTool, toolCall ai.ToolCall) ai.ToolCall {
	if tool.PrepareArguments == nil {
		return toolCall
	}
	preparedArguments := tool.PrepareArguments(toolCall.Arguments)
	if string(preparedArguments) == string(toolCall.Arguments) {
		return toolCall
	}
	toolCall.Arguments = preparedArguments
	return toolCall
}

func prepareToolCall(
	currentContext *AgentContext,
	assistantMessage *ai.AssistantMessage,
	toolCall ai.ToolCall,
	config *AgentLoopConfig,
	ctx context.Context,
) prepareOutcome {
	var tool *AgentTool
	for i := range currentContext.Tools {
		if currentContext.Tools[i].Name == toolCall.Name {
			tool = &currentContext.Tools[i]
			break
		}
	}
	if tool == nil {
		return prepareOutcome{immediate: &immediateToolCallOutcome{
			result: createErrorToolResult(fmt.Sprintf("Tool %s not found", toolCall.Name)), isError: true,
		}}
	}

	preparedCall := prepareToolCallArguments(tool, toolCall)
	validatedArgs, err := ai.ValidateToolArguments(ai.Tool{
		Name: tool.Name, Description: tool.Description, Parameters: tool.Parameters,
	}, preparedCall)
	if err != nil {
		return prepareOutcome{immediate: &immediateToolCallOutcome{
			result: createErrorToolResult(err.Error()), isError: true,
		}}
	}

	if config.BeforeToolCall != nil {
		beforeResult, berr := config.BeforeToolCall(&BeforeToolCallContext{
			AssistantMessage: assistantMessage,
			ToolCall:         toolCall,
			Args:             validatedArgs,
			Context:          *currentContext,
		}, ctx)
		if ctxDone(ctx) {
			return prepareOutcome{immediate: &immediateToolCallOutcome{
				result: createErrorToolResult("Operation aborted"), isError: true,
			}}
		}
		if berr != nil {
			return prepareOutcome{immediate: &immediateToolCallOutcome{
				result: createErrorToolResult(berr.Error()), isError: true,
			}}
		}
		if beforeResult != nil && beforeResult.Block {
			reason := beforeResult.Reason
			if reason == "" {
				reason = "Tool execution was blocked"
			}
			result := createErrorToolResult(reason)
			if beforeResult.Terminate {
				result.Terminate = true
			}
			return prepareOutcome{immediate: &immediateToolCallOutcome{result: result, isError: true}}
		}
	}
	if ctxDone(ctx) {
		return prepareOutcome{immediate: &immediateToolCallOutcome{
			result: createErrorToolResult("Operation aborted"), isError: true,
		}}
	}
	return prepareOutcome{prepared: &preparedToolCallValue{toolCall: toolCall, tool: tool, args: validatedArgs}}
}

func executePreparedToolCall(prepared *preparedToolCallValue, ctx context.Context, emit AgentEventSink) executedToolCallOutcome {
	var updateWG sync.WaitGroup
	updateEvents := 0
	acceptingUpdates := true
	defer func() { acceptingUpdates = false }()

	result, err := prepared.tool.Execute(prepared.toolCall.ID, prepared.args, ctx, func(partialResult AgentToolResult) {
		if !acceptingUpdates {
			return
		}
		event := AgentEvent{
			Type: ToolExecutionUpdate, ToolCallID: prepared.toolCall.ID,
			ToolName: prepared.toolCall.Name, Args: prepared.toolCall.Arguments,
			PartialResult: partialResult,
		}
		updateWG.Add(1)
		updateEvents++
		go func() {
			defer updateWG.Done()
			_ = emit(event)
		}()
	})
	updateWG.Wait()
	if err == nil {
		return executedToolCallOutcome{result: result, isError: false}
	}
	return executedToolCallOutcome{result: createErrorToolResult(err.Error()), isError: true}
}

type executedToolCallOutcome struct {
	result  AgentToolResult
	isError bool
}

func finalizeExecutedToolCall(
	currentContext *AgentContext,
	assistantMessage *ai.AssistantMessage,
	prepared *preparedToolCallValue,
	executed executedToolCallOutcome,
	config *AgentLoopConfig,
	ctx context.Context,
) finalizedToolCallOutcome {
	result := executed.result
	isError := executed.isError

	if config.AfterToolCall != nil {
		afterResult, err := config.AfterToolCall(&AfterToolCallContext{
			AssistantMessage: assistantMessage,
			ToolCall:         prepared.toolCall,
			Args:             prepared.args,
			Result:           executed.result,
			IsError:          executed.isError,
			Context:          *currentContext,
		}, ctx)
		if err != nil {
			return finalizedToolCallOutcome{
				toolCall: prepared.toolCall,
				result:   createErrorToolResult(err.Error()),
				isError:  true,
			}
		}
		if afterResult != nil {
			if afterResult.HasContent {
				result.Content = afterResult.Content
			}
			if afterResult.HasDetails {
				result.Details = afterResult.Details
			}
			if afterResult.Usage != nil {
				result.Usage = afterResult.Usage
			}
			if afterResult.Terminate != nil {
				result.Terminate = *afterResult.Terminate
			}
			if afterResult.IsError != nil {
				isError = *afterResult.IsError
			}
		}
	}

	return finalizedToolCallOutcome{toolCall: prepared.toolCall, result: result, isError: isError}
}

func createErrorToolResult(message string) AgentToolResult {
	return AgentToolResult{
		Content: []ai.Content{ai.TextContent{Text: message}},
		Details: json.RawMessage("{}"),
	}
}

func emitToolExecutionEnd(finalized finalizedToolCallOutcome, emit AgentEventSink) {
	mustEmit(emit, AgentEvent{
		Type: ToolExecutionEnd, ToolCallID: finalized.toolCall.ID,
		ToolName: finalized.toolCall.Name, Result: finalized.result, IsError: finalized.isError,
	})
}

func createToolResultMessage(finalized finalizedToolCallOutcome) *ai.ToolResultMessage {
	var content []ai.UserContent
	for _, block := range finalized.result.Content {
		switch b := block.(type) {
		case ai.TextContent:
			content = append(content, b)
		case ai.ImageContent:
			content = append(content, b)
		}
	}
	if content == nil {
		content = ai.UserContentList{}
	}
	return &ai.ToolResultMessage{
		ToolCallID: finalized.toolCall.ID,
		ToolName:   finalized.toolCall.Name,
		Content:    content,
		Details:    finalized.result.Details,
		Usage:      finalized.result.Usage,
		IsError:    finalized.isError,
		Timestamp:  time.Now().UnixMilli(),
	}
}

func emitToolResultMessage(toolResultMessage *ai.ToolResultMessage, emit AgentEventSink) {
	mustEmit(emit, AgentEvent{Type: MessageStart, Message: toolResultMessage})
	mustEmit(emit, AgentEvent{Type: MessageEnd, Message: toolResultMessage})
}

func ctxDone(ctx context.Context) bool {
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
