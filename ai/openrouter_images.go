package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// Port of api/openrouter-images.ts: OpenRouter's image generation through the
// chat completions endpoint (modalities: ["image"]).

// openRouterImagesEndpoint is overridable for tests.
var openRouterImagesEndpoint = "/chat/completions"

// OpenRouterImages implements ProviderImages.
type OpenRouterImages struct{}

// GenerateImages runs one image generation (upstream generateImages).
func (OpenRouterImages) GenerateImages(model *ImagesModel, context ImagesContext, options *ImagesOptions) *AssistantImages {
	output := &AssistantImages{
		API: model.API, Provider: model.Provider, Model: model.ID,
		Output: []ImagesOutputContent{}, StopReason: ImagesStopStop,
		Timestamp: time.Now().UnixMilli(),
	}
	if options == nil {
		options = &ImagesOptions{}
	}
	if options.APIKey == "" {
		message := fmt.Sprintf("No API key for provider: %s", model.Provider)
		output.StopReason = ImagesStopError
		output.ErrorMessage = &message
		return output
	}

	params := buildOpenRouterImagesParams(model, context)
	if options.OnPayload != nil {
		payload := mustMarshalJSON(params)
		if next := options.OnPayload(payload, &Model{
			ID: model.ID, Name: model.Name, API: model.API, Provider: model.Provider, BaseURL: model.BaseURL,
		}); next != nil {
			var replaced map[string]any
			if err := json.Unmarshal(next, &replaced); err == nil {
				params = replaced
			}
		}
	}
	body, err := MarshalJSON(params)
	if err != nil {
		message := err.Error()
		output.StopReason = ImagesStopError
		output.ErrorMessage = &message
		return output
	}

	ctx := bgCtx()
	if options.Ctx != nil {
		ctx = options.Ctx
	}
	requestURL := strings.TrimRight(model.BaseURL, "/") + openRouterImagesEndpoint
	response, err := RetryProviderRequest(ctx, func() (*http.Response, error) {
		request, nerr := http.NewRequestWithContext(ctx, http.MethodPost, requestURL, bytes.NewReader(body))
		if nerr != nil {
			return nil, nerr
		}
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("User-Agent", GetPiUserAgent())
		request.Header.Set("Authorization", "Bearer "+options.APIKey)
		for name, value := range model.Headers {
			request.Header.Set(name, value)
		}
		for name, value := range options.Headers {
			if value == nil {
				request.Header.Del(name)
				continue
			}
			request.Header.Set(name, *value)
		}
		hresp, rerr := http.DefaultClient.Do(request)
		if rerr != nil {
			return nil, rerr
		}
		if hresp.StatusCode >= 400 {
			raw, _ := io.ReadAll(io.LimitReader(hresp.Body, 1<<20))
			hresp.Body.Close()
			return nil, &ProviderError{
				Status: hresp.StatusCode, Headers: hresp.Header,
				Message: fmt.Sprintf("%d %s: %s", hresp.StatusCode, http.StatusText(hresp.StatusCode), string(raw)),
				Body:    string(raw),
			}
		}
		return hresp, nil
	}, &ProviderRetryOptions{MaxRetries: options.MaxRetries, MaxRetryDelayMS: options.MaxRetryDelayMs})
	if err != nil {
		if options.Ctx != nil && options.Ctx.Err() != nil {
			output.StopReason = ImagesStopAborted
		} else {
			output.StopReason = ImagesStopError
		}
		message := FormatProviderError(NormalizeProviderError(err), "")
		output.ErrorMessage = &message
		return output
	}
	defer response.Body.Close()
	if options.OnResponse != nil {
		headers := map[string]string{}
		for name, values := range response.Header {
			headers[strings.ToLower(name)] = strings.Join(values, ", ")
		}
		options.OnResponse(ProviderResponse{Status: response.StatusCode, Headers: headers},
			&Model{ID: model.ID, Provider: model.Provider, API: model.API})
	}

	raw, _ := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	var payload struct {
		ID      string `json:"id"`
		Choices []struct {
			Message struct {
				Content json.RawMessage `json:"content"`
				Images  []struct {
					ImageURL json.RawMessage `json:"image_url"`
				} `json:"images"`
			} `json:"message"`
		} `json:"choices"`
		Usage json.RawMessage `json:"usage"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		message := err.Error()
		output.StopReason = ImagesStopError
		output.ErrorMessage = &message
		return output
	}
	output.ResponseID = &payload.ID
	if len(payload.Usage) > 0 {
		var usage struct {
			PromptTokens        float64 `json:"prompt_tokens"`
			CompletionTokens    float64 `json:"completion_tokens"`
			PromptTokensDetails *struct {
				CachedTokens     *float64 `json:"cached_tokens"`
				CacheWriteTokens *float64 `json:"cache_write_tokens"`
			} `json:"prompt_tokens_details"`
		}
		if err := json.Unmarshal(payload.Usage, &usage); err == nil {
			computed := parseOpenRouterImagesUsage(usage, model)
			output.Usage = &computed
		}
	}
	if len(payload.Choices) > 0 {
		choice := payload.Choices[0]
		var text string
		if err := json.Unmarshal(choice.Message.Content, &text); err == nil && text != "" {
			output.Output = append(output.Output, TextContent{Text: text})
		}
		for _, image := range choice.Message.Images {
			imageURL := ""
			if err := json.Unmarshal(image.ImageURL, &imageURL); err != nil {
				var record struct {
					URL string `json:"url"`
				}
				if err := json.Unmarshal(image.ImageURL, &record); err == nil {
					imageURL = record.URL
				}
			}
			if !strings.HasPrefix(imageURL, "data:") {
				continue
			}
			matches := openRouterDataURLPattern.FindStringSubmatch(imageURL)
			if matches == nil {
				continue
			}
			output.Output = append(output.Output, ImageContent{MimeType: matches[1], Data: matches[2]})
		}
	}
	return output
}

var openRouterDataURLPattern = regexp.MustCompile(`^data:([^;]+);base64,(.+)$`)

// buildOpenRouterImagesParams renders the chat completions payload.
func buildOpenRouterImagesParams(model *ImagesModel, context ImagesContext) map[string]any {
	content := make([]any, 0, len(context.Input))
	for _, item := range context.Input {
		switch typed := item.(type) {
		case TextContent:
			content = append(content, map[string]any{"type": "text", "text": SanitizeSurrogates(typed.Text)})
		case ImageContent:
			content = append(content, map[string]any{
				"type": "image_url",
				"image_url": map[string]any{
					"url": "data:" + typed.MimeType + ";base64," + typed.Data,
				},
			})
		}
	}
	modalities := []any{"image"}
	if containsString(model.Output, "text") {
		modalities = []any{"image", "text"}
	}
	return map[string]any{
		"model":      model.ID,
		"messages":   []any{map[string]any{"role": "user", "content": content}},
		"stream":     false,
		"modalities": modalities,
	}
}

// parseOpenRouterImagesUsage derives usage and cost (upstream parseUsage).
func parseOpenRouterImagesUsage(raw struct {
	PromptTokens        float64 `json:"prompt_tokens"`
	CompletionTokens    float64 `json:"completion_tokens"`
	PromptTokensDetails *struct {
		CachedTokens     *float64 `json:"cached_tokens"`
		CacheWriteTokens *float64 `json:"cache_write_tokens"`
	} `json:"prompt_tokens_details"`
}, model *ImagesModel) Usage {
	promptTokens := int64(raw.PromptTokens)
	reportedCached := int64(0)
	cacheWrite := int64(0)
	if raw.PromptTokensDetails != nil {
		if raw.PromptTokensDetails.CachedTokens != nil {
			reportedCached = int64(*raw.PromptTokensDetails.CachedTokens)
		}
		if raw.PromptTokensDetails.CacheWriteTokens != nil {
			cacheWrite = int64(*raw.PromptTokensDetails.CacheWriteTokens)
		}
	}
	cacheRead := reportedCached
	if cacheWrite > 0 {
		cacheRead = max(0, reportedCached-cacheWrite)
	}
	inputTokens := max(0, promptTokens-cacheRead-cacheWrite)
	outputTokens := int64(raw.CompletionTokens)
	usage := Usage{
		Input: inputTokens, Output: outputTokens,
		CacheRead: cacheRead, CacheWrite: cacheWrite,
		TotalTokens: inputTokens + outputTokens + cacheRead + cacheWrite,
		Cost: UsageCost{
			Input:      model.Cost.Input / 1000000 * float64(inputTokens),
			Output:     model.Cost.Output / 1000000 * float64(outputTokens),
			CacheRead:  model.Cost.CacheRead / 1000000 * float64(cacheRead),
			CacheWrite: model.Cost.CacheWrite / 1000000 * float64(cacheWrite),
		},
	}
	usage.Cost.Total = usage.Cost.Input + usage.Cost.Output + usage.Cost.CacheRead + usage.Cost.CacheWrite
	return usage
}

// OpenRouterImagesProvider builds the built-in OpenRouter image provider
// (upstream openrouterImagesProvider).
func OpenRouterImagesProvider() *ImagesProvider {
	return CreateImagesProvider(ImageProviderOptions{
		ID:   "openrouter",
		Name: "OpenRouter",
		Auth: ProviderAuth{
			APIKey: EnvApiKeyAuth("OpenRouter API key", []string{"OPENROUTER_API_KEY"}),
			OAuth:  OpenRouterOAuth(),
		},
		Models: GetBuiltinImageModels("openrouter"),
		API:    OpenRouterImages{},
	})
}

// BuiltinImagesProviders lists every built-in image provider (upstream
// builtinImagesProviders).
func BuiltinImagesProviders() []*ImagesProvider {
	return []*ImagesProvider{OpenRouterImagesProvider()}
}

// BuiltinImagesModels registers every built-in image provider (upstream
// builtinImagesModels).
func BuiltinImagesModels(models *ImagesModels) *ImagesModels {
	for _, provider := range BuiltinImagesProviders() {
		models.SetProvider(provider)
	}
	return models
}

var _ = context.Background
