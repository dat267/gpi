package ai

import (
	ctxpkg "context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Tests ported from packages/ai/test/azure-openai-base-url.test.ts and
// azure-openai-tool-choice.test.ts (the client-construction assertions become
// request-path assertions in Go).

func TestNormalizeAzureBaseURL(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"https://marc-quicktests-resource.cognitiveservices.azure.com", "https://marc-quicktests-resource.cognitiveservices.azure.com/openai/v1"},
		{"https://marc-quicktests-resource.ai.azure.com", "https://marc-quicktests-resource.ai.azure.com/openai/v1"},
		{"https://my-resource.openai.azure.com", "https://my-resource.openai.azure.com/openai/v1"},
		{"https://my-resource.cognitiveservices.azure.com/openai", "https://my-resource.cognitiveservices.azure.com/openai/v1"},
		{"https://my-resource.cognitiveservices.azure.com/openai/v1", "https://my-resource.cognitiveservices.azure.com/openai/v1"},
		{"https://my-resource.services.ai.azure.com/openai/v1/responses", "https://my-resource.services.ai.azure.com/openai/v1"},
		{"https://my-proxy.example.com/v1", "https://my-proxy.example.com/v1"},
		{"https://my-resource.openai.azure.com/openai?api-version=2024-12-01", "https://my-resource.openai.azure.com/openai/v1"},
		// Query parameters on non-Azure hosts are preserved.
		{"https://my-proxy.example.com/v1?custom=true", "https://my-proxy.example.com/v1?custom=true"},
	}
	for _, testCase := range cases {
		got, err := NormalizeAzureBaseURL(testCase.input)
		if err != nil {
			t.Errorf("NormalizeAzureBaseURL(%q) errored: %v", testCase.input, err)
			continue
		}
		if got != testCase.want {
			t.Errorf("NormalizeAzureBaseURL(%q) = %q, want %q", testCase.input, got, testCase.want)
		}
	}
	if _, err := NormalizeAzureBaseURL("not-a-url"); err == nil {
		t.Fatal("invalid URLs must be rejected")
	}
}

func TestResolveAzureConfig(t *testing.T) {
	model := &Model{ID: "gpt-4o-mini", API: APIAzureOpenAIResponses, Provider: "azure-openai-responses"}

	// AZURE_OPENAI_RESOURCE_NAME builds the default base URL.
	t.Setenv("AZURE_OPENAI_RESOURCE_NAME", "my-resource")
	t.Setenv("AZURE_OPENAI_BASE_URL", "")
	t.Setenv("AZURE_OPENAI_API_VERSION", "")
	baseURL, apiVersion, err := ResolveAzureConfig(model, nil)
	if err != nil || baseURL != "https://my-resource.openai.azure.com/openai/v1" || apiVersion != DefaultAzureAPIVersion {
		t.Fatalf("base = %q version = %q err = %v", baseURL, apiVersion, err)
	}

	// The environment API version wins over the default.
	t.Setenv("AZURE_OPENAI_API_VERSION", "2024-12-01")
	if _, apiVersion, _ := ResolveAzureConfig(model, nil); apiVersion != "2024-12-01" {
		t.Fatalf("apiVersion = %q", apiVersion)
	}

	// Options win over the environment, and the model base URL is the fallback.
	t.Setenv("AZURE_OPENAI_RESOURCE_NAME", "")
	t.Setenv("AZURE_OPENAI_BASE_URL", "")
	t.Setenv("AZURE_OPENAI_API_VERSION", "")
	withBase := *model
	withBase.BaseURL = "https://proxy.example.com/v1"
	baseURL, _, err = ResolveAzureConfig(&withBase, nil)
	if err != nil || baseURL != "https://proxy.example.com/v1" {
		t.Fatalf("base = %q err = %v", baseURL, err)
	}
	baseURL, _, err = ResolveAzureConfig(model, &AzureOpenAIResponsesOptions{
		AzureBaseURL: "https://opt.openai.azure.com", AzureAPIVersion: "v9",
	})
	if err != nil || baseURL != "https://opt.openai.azure.com/openai/v1" {
		t.Fatalf("base = %q err = %v", baseURL, err)
	}

	// Without any configuration the upstream error is reported.
	t.Setenv("AZURE_OPENAI_BASE_URL", "")
	t.Setenv("AZURE_OPENAI_RESOURCE_NAME", "")
	if _, _, err := ResolveAzureConfig(model, nil); err == nil ||
		!strings.Contains(err.Error(), "Azure OpenAI base URL is required") {
		t.Fatalf("err = %v", err)
	}
}

func TestResolveAzureDeploymentName(t *testing.T) {
	model := &Model{ID: "gpt-4o-mini"}
	t.Setenv("AZURE_OPENAI_DEPLOYMENT_NAME_MAP", "gpt-4o-mini=my-deployment,other=second")
	if got := ResolveAzureDeploymentName(model, nil); got != "my-deployment" {
		t.Fatalf("deployment = %q", got)
	}
	// An unmapped model uses its own id; options win.
	if got := ResolveAzureDeploymentName(&Model{ID: "unmapped"}, nil); got != "unmapped" {
		t.Fatalf("deployment = %q", got)
	}
	if got := ResolveAzureDeploymentName(model, &AzureOpenAIResponsesOptions{AzureDeploymentName: "explicit"}); got != "explicit" {
		t.Fatalf("deployment = %q", got)
	}
	// Malformed entries are skipped.
	mapValue := ParseDeploymentNameMap("a=1,,=2,b=,c=3")
	if len(mapValue) != 2 || mapValue["a"] != "1" || mapValue["c"] != "3" {
		t.Fatalf("map = %#v", mapValue)
	}
}

func TestAzureResponsesRequest(t *testing.T) {
	var captured *http.Request
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		captured = request
		raw, _ := io.ReadAll(request.Body)
		_ = json.Unmarshal(raw, &body)
		writer.Header().Set("Content-Type", "text/event-stream")
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"r1\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"ok\"}]}],\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n"))
	}))
	defer server.Close()

	model := &Model{
		ID: "gpt-4o-mini", API: APIAzureOpenAIResponses, Provider: "azure-openai-responses",
		BaseURL: server.URL + "/openai/v1", ContextWindow: 128000, MaxTokens: 4096,
	}
	context := TranscriptContext{Messages: []Message{
		&UserMessage{Content: StringOrBlocks{Text: "hello"}, Timestamp: 1},
	}}
	t.Setenv("AZURE_OPENAI_BASE_URL", "")
	t.Setenv("AZURE_OPENAI_RESOURCE_NAME", "")
	t.Setenv("AZURE_OPENAI_API_VERSION", "")

	stream := StreamAzureOpenAIResponses(model, context, &AzureOpenAIResponsesOptions{
		OpenAIResponsesOptions: OpenAIResponsesOptions{
			StreamOptions: StreamOptions{APIKey: "test-api-key", SessionID: strings.Repeat("x", 67)},
		},
	})
	message, err := stream.Result(ctxpkg.Background())
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if message.StopReason != StopStop {
		t.Fatalf("message = %+v", message)
	}

	if captured == nil {
		t.Fatal("no request captured")
	}
	// Azure authenticates with the api-key header, not bearer.
	if captured.Header.Get("api-key") != "test-api-key" {
		t.Fatalf("api-key = %q", captured.Header.Get("api-key"))
	}
	if captured.Header.Get("Authorization") != "" {
		t.Fatalf("unexpected authorization: %q", captured.Header.Get("Authorization"))
	}
	// The base URL is normalized and the api-version query is added.
	if !strings.HasSuffix(captured.URL.Path, "/responses") {
		t.Fatalf("path = %s", captured.URL.Path)
	}
	if captured.URL.Query().Get("api-version") != DefaultAzureAPIVersion {
		t.Fatalf("api-version = %q", captured.URL.Query().Get("api-version"))
	}
	// The request is not rewritten to /deployments for responses.
	if strings.Contains(captured.URL.Path, "/deployments/") {
		t.Fatalf("path = %s", captured.URL.Path)
	}
	if captured.Header.Get("User-Agent") == "" {
		t.Fatal("user agent missing")
	}

	// The body keeps the OpenAI Responses shape with Azure specifics.
	if body["store"] != false {
		t.Fatalf("store = %#v", body["store"])
	}
	if body["stream"] != true {
		t.Fatalf("stream = %#v", body["stream"])
	}
	// prompt_cache_key is clamped to 64 characters.
	if key, _ := body["prompt_cache_key"].(string); len(key) != 64 {
		t.Fatalf("prompt_cache_key = %q (%d)", key, len(key))
	}
	if body["model"] != "gpt-4o-mini" {
		t.Fatalf("model = %#v", body["model"])
	}
}

func TestAzureResponsesDeploymentNameInBody(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		raw, _ := io.ReadAll(request.Body)
		_ = json.Unmarshal(raw, &body)
		writer.Header().Set("Content-Type", "text/event-stream")
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"r1\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":0,\"output_tokens\":0,\"total_tokens\":0}}}\n\n"))
	}))
	defer server.Close()

	model := &Model{
		ID: "gpt-4o-mini", API: APIAzureOpenAIResponses, Provider: "azure-openai-responses",
		BaseURL: server.URL, ContextWindow: 128000, MaxTokens: 4096,
	}
	context := TranscriptContext{Messages: []Message{
		&UserMessage{Content: StringOrBlocks{Text: "hello"}, Timestamp: 1},
	}}
	t.Setenv("AZURE_OPENAI_BASE_URL", "")
	t.Setenv("AZURE_OPENAI_RESOURCE_NAME", "")
	t.Setenv("AZURE_OPENAI_DEPLOYMENT_NAME_MAP", "gpt-4o-mini=deployment-name")

	stream := StreamAzureOpenAIResponses(model, context, &AzureOpenAIResponsesOptions{
		OpenAIResponsesOptions: OpenAIResponsesOptions{StreamOptions: StreamOptions{APIKey: "key"}},
	})
	if _, err := stream.Result(ctxpkg.Background()); err != nil {
		t.Fatalf("err = %v", err)
	}
	if body["model"] != "deployment-name" {
		t.Fatalf("model = %#v", body["model"])
	}
}

func TestAzureResponsesReasoningEffort(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		raw, _ := io.ReadAll(request.Body)
		_ = json.Unmarshal(raw, &body)
		writer.Header().Set("Content-Type", "text/event-stream")
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"r1\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":0,\"output_tokens\":0,\"total_tokens\":0}}}\n\n"))
	}))
	defer server.Close()

	model := &Model{
		ID: "o4-mini", API: APIAzureOpenAIResponses, Provider: "azure-openai-responses",
		BaseURL: server.URL, Reasoning: true, ContextWindow: 200000, MaxTokens: 100000,
	}
	context := TranscriptContext{Messages: []Message{
		&UserMessage{Content: StringOrBlocks{Text: "hello"}, Timestamp: 1},
	}}
	t.Setenv("AZURE_OPENAI_BASE_URL", "")
	t.Setenv("AZURE_OPENAI_RESOURCE_NAME", "")

	stream := StreamAzureOpenAIResponsesSimple(model, context, &SimpleStreamOptions{
		StreamOptions: StreamOptions{APIKey: "key"},
		Reasoning:     ThinkHigh,
	})
	if _, err := stream.Result(ctxpkg.Background()); err != nil {
		t.Fatalf("err = %v", err)
	}
	reasoning, ok := body["reasoning"].(map[string]any)
	if !ok {
		t.Fatalf("reasoning = %#v", body["reasoning"])
	}
	if reasoning["effort"] != "high" || reasoning["summary"] != "auto" {
		t.Fatalf("reasoning = %#v", reasoning)
	}
	include, _ := body["include"].([]any)
	if len(include) != 1 || include[0] != "reasoning.encrypted_content" {
		t.Fatalf("include = %#v", body["include"])
	}
}
