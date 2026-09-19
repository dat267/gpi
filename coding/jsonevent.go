package coding

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/dat267/gpi/agent"
	"github.com/dat267/gpi/ai"
)

// Port of modes/json-event.ts.

// ToJSONEvent maps one session event to the JSON shape the JSON and RPC stdout
// protocols emit (upstream toJsonEvent).
//
// Streaming `message_update` events drop their cumulative `partial` snapshot:
// message_start carries the initial message, deltas build it, and message_end
// carries the final authoritative message. Cumulative usage, tool-call ids, and
// tool names stay available because their size is constant.
func ToJSONEvent(event *SessionEvent) (map[string]any, error) {
	if event == nil {
		return nil, fmt.Errorf("session event is required")
	}
	if event.Type != SessionMessageUpdate {
		return sessionEventValue(event), nil
	}
	if event.Agent == nil {
		return nil, fmt.Errorf("message_update event has no agent payload")
	}
	message := event.Agent.Message
	assistant, ok := message.(*ai.AssistantMessage)
	if !ok {
		return nil, fmt.Errorf("message_update message is not an assistant message")
	}
	assistantEvent, err := toJSONAssistantMessageEvent(event.Agent.AssistantMessageEvent)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"type":                  "message_update",
		"usage":                 usageValue(assistant.Usage),
		"assistantMessageEvent": assistantEvent,
	}, nil
}

func toJSONAssistantMessageEvent(event *ai.AssistantMessageEvent) (map[string]any, error) {
	if event == nil {
		return nil, fmt.Errorf("message_update event has no assistant message event")
	}
	if event.Type == ai.EventToolcallStart {
		if event.Partial == nil {
			return nil, fmt.Errorf("toolcall_start event has no partial message")
		}
		if event.ContentIndex < 0 || event.ContentIndex >= len(event.Partial.Content) {
			return nil, fmt.Errorf("toolcall_start content at index %d is not a tool call", event.ContentIndex)
		}
		toolCall, ok := event.Partial.Content[event.ContentIndex].(ai.ToolCall)
		if !ok {
			return nil, fmt.Errorf("toolcall_start content at index %d is not a tool call", event.ContentIndex)
		}
		delta := assistantEventValue(event)
		delta["id"] = toolCall.ID
		delta["toolName"] = toolCall.Name
		return delta, nil
	}
	return assistantEventValue(event), nil
}

// assistantEventValue renders an assistant streaming event without its
// cumulative partial snapshot (upstream strips `partial` from the delta event).
func assistantEventValue(event *ai.AssistantMessageEvent) map[string]any {
	value := map[string]any{"type": event.Type}
	if event.ContentIndex != 0 {
		value["contentIndex"] = event.ContentIndex
	}
	if event.Delta != "" {
		value["delta"] = event.Delta
	}
	if event.Content != "" {
		value["content"] = event.Content
	}
	if event.ToolCall != nil {
		value["toolCall"] = toolCallValue(event.ToolCall)
	}
	if event.Message != nil {
		value["message"] = assistantMessageValue(event.Message)
	}
	if event.Reason != "" {
		value["reason"] = event.Reason
	}
	if event.Error != nil {
		value["error"] = assistantMessageValue(event.Error)
	}
	return value
}

func toolCallValue(call *ai.ToolCall) map[string]any {
	value := map[string]any{
		"type":      "toolCall",
		"id":        call.ID,
		"name":      call.Name,
		"arguments": rawJSONValue(call.Arguments),
	}
	if call.ThoughtSignature != nil {
		value["thoughtSignature"] = *call.ThoughtSignature
	}
	if call.Namespace != nil {
		value["namespace"] = *call.Namespace
	}
	return value
}

func assistantMessageValue(message *ai.AssistantMessage) map[string]any {
	if message == nil {
		return nil
	}
	value := map[string]any{
		"role":       "assistant",
		"content":    contentValues(message.Content),
		"api":        message.API,
		"provider":   message.Provider,
		"model":      message.Model,
		"usage":      usageValue(message.Usage),
		"stopReason": message.StopReason,
		"timestamp":  message.Timestamp,
	}
	if message.ErrorMessage != nil {
		value["errorMessage"] = *message.ErrorMessage
	}
	return value
}

func contentValues(content ai.ContentList) []any {
	out := make([]any, 0, len(content))
	for _, entry := range content {
		switch typed := entry.(type) {
		case ai.TextContent:
			value := map[string]any{"type": "text", "text": typed.Text}
			if typed.TextSignature != nil {
				value["textSignature"] = *typed.TextSignature
			}
			out = append(out, value)
		case ai.ThinkingContent:
			value := map[string]any{"type": "thinking", "thinking": typed.Thinking}
			if typed.ThinkingSignature != nil {
				value["thinkingSignature"] = *typed.ThinkingSignature
			}
			if typed.Redacted {
				value["redacted"] = true
			}
			out = append(out, value)
		case ai.ToolCall:
			call := typed
			out = append(out, toolCallValue(&call))
		default:
			out = append(out, map[string]any{"type": fmt.Sprintf("%T", entry)})
		}
	}
	return out
}

// userContentValues renders user/tool-result content blocks (text and images).
func userContentValues(content ai.UserContentList) []any {
	out := make([]any, 0, len(content))
	for _, entry := range content {
		switch typed := entry.(type) {
		case ai.TextContent:
			out = append(out, map[string]any{"type": "text", "text": typed.Text})
		case ai.ImageContent:
			out = append(out, map[string]any{"type": "image", "data": typed.Data, "mimeType": typed.MimeType})
		default:
			out = append(out, map[string]any{"type": fmt.Sprintf("%T", entry)})
		}
	}
	return out
}

func usageValue(usage ai.Usage) map[string]any {
	return map[string]any{
		"input":       usage.Input,
		"output":      usage.Output,
		"cacheRead":   usage.CacheRead,
		"cacheWrite":  usage.CacheWrite,
		"totalTokens": usage.TotalTokens,
		"cost": map[string]any{
			"input":      usage.Cost.Input,
			"output":     usage.Cost.Output,
			"cacheRead":  usage.Cost.CacheRead,
			"cacheWrite": usage.Cost.CacheWrite,
			"total":      usage.Cost.Total,
		},
	}
}

// sessionEventValue renders a non-streaming session event as its JSON payload,
// mirroring the event objects upstream serializes directly.
func sessionEventValue(event *SessionEvent) map[string]any {
	value := map[string]any{"type": event.Type}
	agentEvent := event.Agent
	switch event.Type {
	case SessionAgentEnd:
		if agentEvent != nil {
			value["messages"] = messageValues(agentEvent.Messages)
		}
	case SessionTurnEnd:
		if agentEvent != nil {
			if agentEvent.Message != nil {
				value["message"] = messageValue(agentEvent.Message)
			}
			value["toolResults"] = messageValues(agentEvent.ToolResults)
		}
	case SessionMessageStart, SessionMessageEnd:
		if agentEvent != nil && agentEvent.Message != nil {
			value["message"] = messageValue(agentEvent.Message)
		}
	case SessionToolExecutionStart:
		if agentEvent != nil {
			value["toolCallId"] = agentEvent.ToolCallID
			value["toolName"] = agentEvent.ToolName
			value["args"] = rawJSONValue(agentEvent.Args)
		}
	case SessionToolExecutionUpdate:
		if agentEvent != nil {
			value["toolCallId"] = agentEvent.ToolCallID
			value["toolName"] = agentEvent.ToolName
			value["partialResult"] = toolResultValue(agentEvent.PartialResult)
		}
	case SessionToolExecutionEnd:
		if agentEvent != nil {
			value["toolCallId"] = agentEvent.ToolCallID
			value["toolName"] = agentEvent.ToolName
			value["result"] = toolResultValue(agentEvent.Result)
			value["isError"] = agentEvent.IsError
		}
	case SessionQueueUpdate:
		value["steering"] = stringSliceValue(event.Steering)
		value["followUp"] = stringSliceValue(event.FollowUp)
	case SessionCompactionStart:
		value["reason"] = event.Reason
	case SessionCompactionEnd:
		value["reason"] = event.Reason
		value["aborted"] = event.Aborted
		if event.ErrorMessage != "" {
			value["errorMessage"] = event.ErrorMessage
		}
	case SessionThinkingLevelChanged:
		value["level"] = event.Level
	case SessionAutoRetryStart:
		value["attempt"] = event.Attempt
		value["maxAttempts"] = event.MaxAttempts
		value["delayMs"] = event.DelayMS
		if event.ErrorMessage != "" {
			value["errorMessage"] = event.ErrorMessage
		}
	case SessionAutoRetryEnd:
		value["success"] = event.Success
		value["attempt"] = event.Attempt
	case SessionAgentStart:
		// No payload beyond the type.
	}
	if event.Type == SessionAgentEnd && event.WillRetry {
		value["willRetry"] = true
	}
	return value
}

func messageValues(messages []ai.Message) []any {
	out := make([]any, 0, len(messages))
	for _, message := range messages {
		out = append(out, messageValue(message))
	}
	return out
}

func messageValue(message ai.Message) any {
	switch typed := message.(type) {
	case *ai.AssistantMessage:
		return assistantMessageValue(typed)
	case *ai.UserMessage:
		return map[string]any{
			"role":      "user",
			"content":   userMessageContent(typed.Content),
			"timestamp": typed.Timestamp,
		}
	case *ai.ToolResultMessage:
		value := map[string]any{
			"role":       "toolResult",
			"toolCallId": typed.ToolCallID,
			"toolName":   typed.ToolName,
			"content":    userContentValues(typed.Content),
			"isError":    typed.IsError,
			"timestamp":  typed.Timestamp,
		}
		if len(typed.Details) > 0 {
			value["details"] = rawJSONValue(typed.Details)
		}
		return value
	case *ai.SystemMessage:
		return systemMessageValue(typed)
	default:
		return map[string]any{"role": fmt.Sprintf("%T", message)}
	}
}

// userMessageContent renders StringOrBlocks user content the way JSON.stringify
// serializes upstream's `string | Content[]`.
func userMessageContent(content ai.StringOrBlocks) any {
	if content.String() {
		return content.Text
	}
	blocks := make(ai.UserContentList, 0, len(content.Blocks))
	for _, block := range content.Blocks {
		if userBlock, ok := block.(ai.UserContent); ok {
			blocks = append(blocks, userBlock)
		}
	}
	return userContentValues(blocks)
}

func systemMessageValue(message *ai.SystemMessage) map[string]any {
	value := map[string]any{"role": "system", "content": message.Content}
	if message.Timestamp != 0 {
		value["timestamp"] = message.Timestamp
	}
	return value
}

func toolResultValue(result agent.AgentToolResult) map[string]any {
	value := map[string]any{"content": contentValues(ai.ContentList(result.Content))}
	if len(result.Details) > 0 {
		value["details"] = rawJSONValue(result.Details)
	}
	return value
}

func stringSliceValue(values []string) []any {
	out := make([]any, 0, len(values))
	for _, value := range values {
		out = append(out, value)
	}
	return out
}

func rawJSONValue(raw any) any {
	switch typed := raw.(type) {
	case nil:
		return nil
	case json.RawMessage:
		if len(typed) == 0 {
			return nil
		}
		var parsed any
		decoder := json.NewDecoder(bytes.NewReader(typed))
		decoder.UseNumber()
		if err := decoder.Decode(&parsed); err != nil {
			return string(typed)
		}
		return parsed
	case []byte:
		if len(typed) == 0 {
			return nil
		}
		var parsed any
		decoder := json.NewDecoder(bytes.NewReader(typed))
		decoder.UseNumber()
		if err := decoder.Decode(&parsed); err != nil {
			return string(typed)
		}
		return parsed
	case string:
		if typed == "" {
			return nil
		}
		return typed
	default:
		return typed
	}
}
