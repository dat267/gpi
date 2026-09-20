package ai

import (
	"fmt"
	"sync"
	"time"
)

// derefStreamOptionsValue flattens optional stream options for the API
// implementations that take a typed options struct.
func derefStreamOptionsValue(options *StreamOptions) StreamOptions {
	if options == nil {
		return StreamOptions{}
	}
	return *options
}

// Port of the api-provider registry in packages/ai/src/compat.ts
// (registerApiProvider / getApiProvider / registerBuiltInApiProviders).

// funcStreams adapts two functions to the ProviderStreams interface.
type funcStreams struct {
	stream       func(model *Model, context TranscriptContext, options *StreamOptions) *AssistantMessageEventStream
	streamSimple func(model *Model, context TranscriptContext, options *SimpleStreamOptions) *AssistantMessageEventStream
}

func (f funcStreams) Stream(model *Model, context TranscriptContext, options *StreamOptions) *AssistantMessageEventStream {
	return f.stream(model, context, options)
}

func (f funcStreams) StreamSimple(model *Model, context TranscriptContext, options *SimpleStreamOptions) *AssistantMessageEventStream {
	return f.streamSimple(model, context, options)
}

// registeredAPIProvider is one API implementation plus its optional source id.
type registeredAPIProvider struct {
	streams  ProviderStreams
	sourceID string
}

var (
	apiProviderMu       sync.RWMutex
	apiProviderRegistry = map[Api]registeredAPIProvider{}
)

// apiMismatchStream builds the error stream upstream throws when a stream
// function is called with a model of the wrong api.
func apiMismatchStream(model *Model, api Api) *AssistantMessageEventStream {
	stream := NewAssistantMessageEventStream()
	go func() {
		message := &AssistantMessage{
			API: model.API, Provider: model.Provider, Model: model.ID,
			Timestamp: time.Now().UnixMilli(),
		}
		text := fmt.Sprintf("Mismatched api: %s expected %s", model.API, api)
		message.StopReason = StopError
		message.ErrorMessage = &text
		stream.Push(AssistantMessageEvent{Type: "error", Reason: StopError, Error: message})
	}()
	return stream
}

// registerAPIStreams wraps an implementation so it rejects foreign api ids.
func registerAPIStreams(api Api, streams ProviderStreams) ProviderStreams {
	return funcStreams{
		stream: func(model *Model, context TranscriptContext, options *StreamOptions) *AssistantMessageEventStream {
			if model.API != api {
				return apiMismatchStream(model, api)
			}
			return streams.Stream(model, context, options)
		},
		streamSimple: func(model *Model, context TranscriptContext, options *SimpleStreamOptions) *AssistantMessageEventStream {
			if model.API != api {
				return apiMismatchStream(model, api)
			}
			return streams.StreamSimple(model, context, options)
		},
	}
}

// RegisterAPIProvider registers an API implementation (upstream
// registerApiProvider).
func RegisterAPIProvider(api Api, streams ProviderStreams, sourceID string) {
	apiProviderMu.Lock()
	apiProviderRegistry[api] = registeredAPIProvider{streams: registerAPIStreams(api, streams), sourceID: sourceID}
	apiProviderMu.Unlock()
}

// GetAPIProvider returns the registered implementation for an api.
func GetAPIProvider(api Api) ProviderStreams {
	apiProviderMu.RLock()
	defer apiProviderMu.RUnlock()
	entry, ok := apiProviderRegistry[api]
	if !ok {
		return nil
	}
	return entry.streams
}

// GetAPIProviders lists every registered implementation.
func GetAPIProviders() []ProviderStreams {
	apiProviderMu.RLock()
	defer apiProviderMu.RUnlock()
	out := make([]ProviderStreams, 0, len(apiProviderRegistry))
	for _, entry := range apiProviderRegistry {
		out = append(out, entry.streams)
	}
	return out
}

// UnregisterAPIProviders removes every implementation registered with a source
// id.
func UnregisterAPIProviders(sourceID string) {
	apiProviderMu.Lock()
	for api, entry := range apiProviderRegistry {
		if entry.sourceID == sourceID {
			delete(apiProviderRegistry, api)
		}
	}
	apiProviderMu.Unlock()
}

// builtinAPIStreams maps every built-in api id to its implementation.
func builtinAPIStreams() map[Api]ProviderStreams {
	return map[Api]ProviderStreams{
		APIAnthropicMessages: funcStreams{
			stream: func(model *Model, context TranscriptContext, options *StreamOptions) *AssistantMessageEventStream {
				return StreamAnthropic(model, context, &AnthropicOptions{StreamOptions: derefStreamOptionsValue(options)})
			},
			streamSimple: StreamAnthropicSimple,
		},
		APIOpenAICompletions: funcStreams{
			stream: func(model *Model, context TranscriptContext, options *StreamOptions) *AssistantMessageEventStream {
				return StreamOpenAICompletions(model, context, &OpenAICompletionsOptions{StreamOptions: derefStreamOptionsValue(options)})
			},
			streamSimple: StreamOpenAICompletionsSimple,
		},
		APIOpenAIResponses: funcStreams{
			stream: func(model *Model, context TranscriptContext, options *StreamOptions) *AssistantMessageEventStream {
				return StreamOpenAIResponses(model, context, &OpenAIResponsesOptions{StreamOptions: derefStreamOptionsValue(options)})
			},
			streamSimple: StreamOpenAIResponsesSimple,
		},
		APIOpenAICodexResponses: funcStreams{
			stream: func(model *Model, context TranscriptContext, options *StreamOptions) *AssistantMessageEventStream {
				return StreamOpenAICodexResponses(model, context, &OpenAICodexResponsesOptions{StreamOptions: derefStreamOptionsValue(options)})
			},
			streamSimple: StreamOpenAICodexResponsesSimple,
		},
		APIAzureOpenAIResponses: funcStreams{
			stream: func(model *Model, context TranscriptContext, options *StreamOptions) *AssistantMessageEventStream {
				return StreamAzureOpenAIResponses(model, context, &AzureOpenAIResponsesOptions{StreamOptions: derefStreamOptionsValue(options)})
			},
			streamSimple: StreamAzureOpenAIResponsesSimple,
		},
		APIGoogleGenerativeAI: funcStreams{
			stream: func(model *Model, context TranscriptContext, options *StreamOptions) *AssistantMessageEventStream {
				return StreamGoogleGenerativeAI(model, context, &GoogleOptions{StreamOptions: derefStreamOptionsValue(options)})
			},
			streamSimple: StreamGoogleGenerativeAISimple,
		},
		APIGoogleVertex: funcStreams{
			stream: func(model *Model, context TranscriptContext, options *StreamOptions) *AssistantMessageEventStream {
				return StreamGoogleVertex(model, context, &GoogleVertexOptions{StreamOptions: derefStreamOptionsValue(options)})
			},
			streamSimple: StreamGoogleVertexSimple,
		},
		APIMistralConversations: funcStreams{
			stream: func(model *Model, context TranscriptContext, options *StreamOptions) *AssistantMessageEventStream {
				return StreamMistralConversations(model, context, &MistralOptions{StreamOptions: derefStreamOptionsValue(options)})
			},
			streamSimple: StreamMistralConversationsSimple,
		},
		APIBedrockConverse: funcStreams{
			stream: func(model *Model, context TranscriptContext, options *StreamOptions) *AssistantMessageEventStream {
				return StreamBedrockConverse(model, context, &BedrockOptions{StreamOptions: derefStreamOptionsValue(options)})
			},
			streamSimple: StreamBedrockConverseSimple,
		},
		APIPiMessages: funcStreams{
			stream: func(model *Model, context TranscriptContext, options *StreamOptions) *AssistantMessageEventStream {
				return StreamPiMessages(model, context, &PiMessagesOptions{StreamOptions: derefStreamOptionsValue(options)})
			},
			streamSimple: StreamPiMessagesSimple,
		},
	}
}

// RegisterBuiltInAPIProviders registers the built-in implementations without
// clobbering entries an earlier registration installed (upstream
// registerBuiltInApiProviders).
func RegisterBuiltInAPIProviders() {
	for api, streams := range builtinAPIStreams() {
		apiProviderMu.RLock()
		_, exists := apiProviderRegistry[api]
		apiProviderMu.RUnlock()
		if !exists {
			RegisterAPIProvider(api, streams, "")
		}
	}
}

// ResetAPIProviders clears the registry and re-registers the built-ins.
func ResetAPIProviders() {
	apiProviderMu.Lock()
	apiProviderRegistry = map[Api]registeredAPIProvider{}
	apiProviderMu.Unlock()
	RegisterBuiltInAPIProviders()
}

func init() {
	RegisterBuiltInAPIProviders()
}
