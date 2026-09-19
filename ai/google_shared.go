package ai

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// Port of api/google-shared.ts: message/tool conversion and thinking-level
// resolution shared by google-generative-ai and google-vertex.

// GoogleApiThinkingLevel is the wire thinking level for Gemini 3 models.
type GoogleApiThinkingLevel = string

const (
	GoogleThinkingUnspecified GoogleApiThinkingLevel = "THINKING_LEVEL_UNSPECIFIED"
	GoogleThinkingMinimal     GoogleApiThinkingLevel = "MINIMAL"
	GoogleThinkingLow         GoogleApiThinkingLevel = "LOW"
	GoogleThinkingMedium      GoogleApiThinkingLevel = "MEDIUM"
	GoogleThinkingHigh        GoogleApiThinkingLevel = "HIGH"
)

// ResolvedGoogleThinkingLevel is a thinking level supported by Google's
// token-budget control.
type ResolvedGoogleThinkingLevel = ThinkingLevel

// ResolveGoogleThinkingLevel resolves a pi level (or the model's mapping) to
// a standard Google level; unsupported mappings are an error.
func ResolveGoogleThinkingLevel(model *Model, level ThinkingLevel) (ResolvedGoogleThinkingLevel, error) {
	mapped, has := model.ThinkingLevelMap[level]
	resolved := level
	if has && mapped != nil {
		resolved = strings.ToLower(*mapped)
	}
	switch resolved {
	case "minimal", "low", "medium", "high":
		return resolved, nil
	default:
		return "", fmt.Errorf("Unsupported Google thinking level mapping for %s/%s: %s -> %v",
			model.Provider, model.ID, level, deref(mapped))
	}
}

var (
	googleThinkingLevelPattern = regexp.MustCompile(`gemini-3(?:\.\d+)?-(?:pro|flash)`)
	gemmaThinkingPattern       = regexp.MustCompile(`gemma-?4`)
	geminiMajorVersionPattern  = regexp.MustCompile(`^gemini(?:-live)?-(\d+)`)
)

// UsesGoogleThinkingLevel reports whether the model uses Gemini's discrete
// thinkingLevel control instead of the token-based thinkingBudget.
func UsesGoogleThinkingLevel(model *Model) bool {
	id := strings.ToLower(model.ID)
	return googleThinkingLevelPattern.MatchString(id) ||
		id == "gemini-flash-latest" ||
		id == "gemini-flash-lite-latest" ||
		gemmaThinkingPattern.MatchString(id)
}

// ToGoogleThinkingLevel maps a resolved level to the API enum.
func ToGoogleThinkingLevel(level ResolvedGoogleThinkingLevel) GoogleApiThinkingLevel {
	switch level {
	case "minimal":
		return GoogleThinkingMinimal
	case "low":
		return GoogleThinkingLow
	case "medium":
		return GoogleThinkingMedium
	case "high":
		return GoogleThinkingHigh
	default:
		return GoogleThinkingUnspecified
	}
}

// GetDisabledGoogleThinkingConfig disables thinking for the model's control
// style.
func GetDisabledGoogleThinkingConfig(model *Model) *GoogleThinkingConfig {
	if !UsesGoogleThinkingLevel(model) {
		return &GoogleThinkingConfig{ThinkingBudget: intPtr(0)}
	}
	fallback := ClampThinkingLevel(model, ThinkOff)
	if fallback == ThinkOff {
		return &GoogleThinkingConfig{ThinkingBudget: intPtr(0)}
	}
	resolved, err := ResolveGoogleThinkingLevel(model, fallback)
	if err != nil {
		return &GoogleThinkingConfig{ThinkingBudget: intPtr(0)}
	}
	return &GoogleThinkingConfig{ThinkingLevel: ToGoogleThinkingLevel(resolved)}
}

// IsThinkingPart: `thought: true` is the definitive thinking marker.
func IsThinkingPart(part *GooglePart) bool {
	return part.Thought != nil && *part.Thought
}

// RetainThoughtSignature keeps the last non-empty signature for the current
// streamed block (backends may only send it on the first delta).
func RetainThoughtSignature(existing string, incoming *string) string {
	if incoming != nil && *incoming != "" {
		return *incoming
	}
	return existing
}

var base64SignaturePattern = regexp.MustCompile(`^[A-Za-z0-9+/]+={0,2}$`)

func isValidThoughtSignature(signature string) bool {
	if signature == "" {
		return false
	}
	if len(signature)%4 != 0 {
		return false
	}
	return base64SignaturePattern.MatchString(signature)
}

// resolveThoughtSignature keeps signatures only for the same provider/model
// and when they are valid base64.
func resolveThoughtSignature(isSameProviderAndModel bool, signature string) string {
	if isSameProviderAndModel && isValidThoughtSignature(signature) {
		return signature
	}
	return ""
}

// RequiresToolCallID reports whether the model needs explicit tool call ids.
func RequiresToolCallID(modelID string) bool {
	major := getGeminiMajorVersion(modelID)
	return strings.HasPrefix(modelID, "claude-") ||
		strings.HasPrefix(modelID, "gpt-oss-") ||
		(major != 0 && major >= 3)
}

func getGeminiMajorVersion(modelID string) int {
	match := geminiMajorVersionPattern.FindStringSubmatch(strings.ToLower(modelID))
	if match == nil {
		return 0
	}
	var major int
	fmt.Sscanf(match[1], "%d", &major)
	return major
}

func supportsMultimodalFunctionResponse(modelID string) bool {
	major := getGeminiMajorVersion(modelID)
	if major != 0 {
		return major >= 3
	}
	return true
}

// GooglePart is one Gemini content part.
type GooglePart struct {
	Text             string                  `json:"text,omitempty"`
	Thought          *bool                   `json:"thought,omitempty"`
	ThoughtSignature *string                 `json:"thoughtSignature,omitempty"`
	InlineData       *GoogleInlineData       `json:"inlineData,omitempty"`
	FunctionCall     *GoogleFunctionCall     `json:"functionCall,omitempty"`
	FunctionResponse *GoogleFunctionResponse `json:"functionResponse,omitempty"`
}

// GoogleInlineData is base64 media.
type GoogleInlineData struct {
	MimeType string `json:"mimeType"`
	Data     string `json:"data"`
}

// GoogleFunctionCall is a model tool call.
type GoogleFunctionCall struct {
	Name string          `json:"name"`
	Args json.RawMessage `json:"args,omitempty"`
	ID   string          `json:"id,omitempty"`
}

// GoogleFunctionResponse is a tool result.
type GoogleFunctionResponse struct {
	Name     string          `json:"name"`
	Response json.RawMessage `json:"response,omitempty"`
	Parts    []GooglePart    `json:"parts,omitempty"`
	ID       string          `json:"id,omitempty"`
}

// GoogleContent is one conversation turn.
type GoogleContent struct {
	Role  string       `json:"role"`
	Parts []GooglePart `json:"parts"`
}

// GoogleThinkingConfig is the thinking control.
type GoogleThinkingConfig struct {
	IncludeThoughts *bool                  `json:"includeThoughts,omitempty"`
	ThinkingLevel   GoogleApiThinkingLevel `json:"thinkingLevel,omitempty"`
	ThinkingBudget  *int                   `json:"thinkingBudget,omitempty"`
}

// ConvertGoogleMessages converts transcript messages to Gemini contents
// (port of google-shared convertMessages). The leading system prompt travels
// as systemInstruction and is removed here.
func ConvertGoogleMessages(model *Model, context TranscriptContext) ([]GoogleContent, error) {
	// Gemini has no mid-conversation system messages; the leading prompt is
	// sent as systemInstruction.
	collapsed := CollapseSystemMessages(context)
	conversation := WithoutInitialSystemMessage(collapsed.Messages)
	var contents []GoogleContent

	normalizeToolCallID := func(id string, _ *Model, _ *AssistantMessage) string {
		if !RequiresToolCallID(model.ID) {
			return id
		}
		return JSSlice(sanitizeID(id), 0, 64)
	}
	transformedMessages := TransformMessages(conversation, model, normalizeToolCallID)

	for _, msg := range transformedMessages {
		switch m := msg.(type) {
		case *UserMessage:
			if m.Content.Blocks == nil {
				contents = append(contents, GoogleContent{Role: "user", Parts: []GooglePart{{Text: SanitizeSurrogates(m.Content.Text)}}})
			} else {
				var parts []GooglePart
				for _, item := range m.Content.Blocks {
					switch b := item.(type) {
					case TextContent:
						parts = append(parts, GooglePart{Text: SanitizeSurrogates(b.Text)})
					case ImageContent:
						parts = append(parts, GooglePart{InlineData: &GoogleInlineData{MimeType: b.MimeType, Data: b.Data}})
					}
				}
				if len(parts) == 0 {
					continue
				}
				contents = append(contents, GoogleContent{Role: "user", Parts: parts})
			}
		case *AssistantMessage:
			var parts []GooglePart
			isSameProviderAndModel := m.Provider == model.Provider && m.Model == model.ID

			for _, block := range m.Content {
				switch b := block.(type) {
				case TextContent:
					thoughtSignature := resolveThoughtSignature(isSameProviderAndModel, deref(b.TextSignature))
					// Empty text blocks are skipped unless they carry a
					// thought signature (Gemini attaches signatures to empty
					// parts and requires them echoed back).
					if (b.Text == "" || JSTrim(b.Text) == "") && thoughtSignature == "" {
						continue
					}
					part := GooglePart{Text: SanitizeSurrogates(b.Text)}
					if thoughtSignature != "" {
						sig := thoughtSignature
						part.ThoughtSignature = &sig
					}
					parts = append(parts, part)
				case ThinkingContent:
					if isSameProviderAndModel {
						thoughtSignature := resolveThoughtSignature(isSameProviderAndModel, deref(b.ThinkingSignature))
						if (b.Thinking == "" || JSTrim(b.Thinking) == "") && thoughtSignature == "" {
							continue
						}
						thought := true
						part := GooglePart{Text: SanitizeSurrogates(b.Thinking), Thought: &thought}
						if thoughtSignature != "" {
							sig := thoughtSignature
							part.ThoughtSignature = &sig
						}
						parts = append(parts, part)
					} else {
						// Cross-provider/model: the signature is unusable,
						// empty blocks stay dropped.
						if b.Thinking == "" || JSTrim(b.Thinking) == "" {
							continue
						}
						parts = append(parts, GooglePart{Text: SanitizeSurrogates(b.Thinking)})
					}
				case ToolCall:
					thoughtSignature := resolveThoughtSignature(isSameProviderAndModel, deref(b.ThoughtSignature))
					call := &GoogleFunctionCall{Name: b.Name, Args: b.Arguments}
					if RequiresToolCallID(model.ID) {
						call.ID = b.ID
					}
					part := GooglePart{FunctionCall: call}
					if thoughtSignature != "" {
						sig := thoughtSignature
						part.ThoughtSignature = &sig
					}
					parts = append(parts, part)
				}
			}
			if len(parts) == 0 {
				continue
			}
			contents = append(contents, GoogleContent{Role: "model", Parts: parts})
		case *ToolResultMessage:
			var textParts []string
			var images []ImageContent
			hasImageInput := false
			for _, input := range model.Input {
				if input == "image" {
					hasImageInput = true
				}
			}
			for _, block := range m.Content {
				switch b := block.(type) {
				case TextContent:
					textParts = append(textParts, b.Text)
				case ImageContent:
					if hasImageInput {
						images = append(images, b)
					}
				}
			}
			textResult := strings.Join(textParts, "\n")
			hasText := len(textResult) > 0
			hasImages := len(images) > 0

			modelSupportsMultimodal := supportsMultimodalFunctionResponse(model.ID)
			responseValue := ""
			switch {
			case hasText:
				responseValue = SanitizeSurrogates(textResult)
			case hasImages:
				responseValue = "(see attached image)"
			}
			var imageParts []GooglePart
			for _, image := range images {
				imageParts = append(imageParts, GooglePart{InlineData: &GoogleInlineData{MimeType: image.MimeType, Data: image.Data}})
			}

			// "output" key for success, "error" for errors (SDK docs).
			var response json.RawMessage
			if m.IsError {
				response = mustMarshalJSON(map[string]any{"error": responseValue})
			} else {
				response = mustMarshalJSON(map[string]any{"output": responseValue})
			}
			functionResponse := &GoogleFunctionResponse{Name: m.ToolName, Response: response}
			if hasImages && modelSupportsMultimodal {
				functionResponse.Parts = imageParts
			}
			if RequiresToolCallID(model.ID) {
				functionResponse.ID = m.ToolCallID
			}
			part := GooglePart{FunctionResponse: functionResponse}

			// Cloud Code Assist requires all function responses in a single
			// user turn; merge when the last content already is one.
			if len(contents) > 0 {
				last := &contents[len(contents)-1]
				mergeable := last.Role == "user"
				if mergeable {
					hasFunctionResponse := false
					for _, p := range last.Parts {
						if p.FunctionResponse != nil {
							hasFunctionResponse = true
						}
					}
					if hasFunctionResponse {
						last.Parts = append(last.Parts, part)
						if hasImages && !modelSupportsMultimodal {
							contents = append(contents, GoogleContent{
								Role: "user", Parts: append([]GooglePart{{Text: "Tool result image:"}}, imageParts...),
							})
						}
						continue
					}
				}
			}
			contents = append(contents, GoogleContent{Role: "user", Parts: []GooglePart{part}})
			if hasImages && !modelSupportsMultimodal {
				contents = append(contents, GoogleContent{
					Role: "user", Parts: append([]GooglePart{{Text: "Tool result image:"}}, imageParts...),
				})
			}
		default:
			continue
		}
	}
	return contents, nil
}

var googleJSONSchemaMetaDeclarations = map[string]bool{
	"$schema": true, "$id": true, "$anchor": true, "$dynamicAnchor": true,
	"$vocabulary": true, "$comment": true, "$defs": true, "definitions": true,
}

// sanitizeForOpenAPI strips JSON-Schema meta declarations (needed when the
// legacy `parameters` field is used, e.g. Claude behind Cloud Code Assist).
func sanitizeForOpenAPI(schema any) any {
	switch typed := schema.(type) {
	case map[string]any:
		result := map[string]any{}
		for key, value := range typed {
			if googleJSONSchemaMetaDeclarations[key] {
				continue
			}
			result[key] = sanitizeForOpenAPI(value)
		}
		return result
	case []any:
		out := make([]any, 0, len(typed))
		for _, item := range typed {
			out = append(out, sanitizeForOpenAPI(item))
		}
		return out
	default:
		return schema
	}
}

// ConvertGoogleTools converts tools to Gemini functionDeclarations
// (port of convertTools).
func ConvertGoogleTools(tools []Tool, useParameters bool, supportsStrictMode bool) ([]GoogleToolGroup, error) {
	if len(tools) == 0 {
		return nil, nil
	}
	var declarations []GoogleFunctionDeclaration
	for _, tool := range tools {
		strict, unset, err := ResolveJSONSchemaStrictSampling(tool, supportsStrictMode)
		if err != nil {
			return nil, err
		}
		parameters := GetJSONSchemaToolParameters(tool, strict && !unset)
		declaration := GoogleFunctionDeclaration{Name: tool.Name, Description: tool.Description}
		if useParameters {
			var decoded any
			_ = jsonUnmarshalStrict(parameters, &decoded)
			declaration.Parameters = mustMarshalJSON(sanitizeForOpenAPI(decoded))
		} else {
			declaration.ParametersJSONSchema = parameters
		}
		declarations = append(declarations, declaration)
	}
	return []GoogleToolGroup{{FunctionDeclarations: declarations}}, nil
}

// GoogleToolGroup is one `tools` entry.
type GoogleToolGroup struct {
	FunctionDeclarations []GoogleFunctionDeclaration `json:"functionDeclarations"`
}

// GoogleFunctionDeclaration is one function declaration.
type GoogleFunctionDeclaration struct {
	Name                 string          `json:"name"`
	Description          string          `json:"description"`
	Parameters           json.RawMessage `json:"parameters,omitempty"`
	ParametersJSONSchema json.RawMessage `json:"parametersJsonSchema,omitempty"`
}

// SupportsGoogleStrictToolSampling: Gemini 3+ enforces required parameters in
// validated tool-calling modes.
func SupportsGoogleStrictToolSampling(modelID string) bool {
	major := getGeminiMajorVersion(modelID)
	return major != 0 && major >= 3
}

// MapGoogleToolChoice maps a tool choice string to the Gemini mode.
func MapGoogleToolChoice(choice string) string {
	switch choice {
	case "auto":
		return "AUTO"
	case "none":
		return "NONE"
	case "any":
		return "ANY"
	default:
		return "AUTO"
	}
}

// ResolveGoogleFunctionCallingMode resolves the function calling mode.
func ResolveGoogleFunctionCallingMode(tools []Tool, toolChoice string, supportsStrictMode bool) string {
	useStrictMode := false
	for _, tool := range tools {
		strict, unset, err := ResolveJSONSchemaStrictSampling(tool, supportsStrictMode)
		if err == nil && !unset && strict {
			useStrictMode = true
		}
	}
	if toolChoice == "none" || toolChoice == "any" {
		return MapGoogleToolChoice(toolChoice)
	}
	if useStrictMode {
		return "VALIDATED"
	}
	if toolChoice != "" {
		return MapGoogleToolChoice(toolChoice)
	}
	return ""
}

// MapGoogleStopReason maps a Gemini finish reason to pi's StopReason; any
// safety/other reason is an error (port of mapStopReason).
func MapGoogleStopReason(reason string) StopReason {
	switch reason {
	case "STOP":
		return StopStop
	case "MAX_TOKENS":
		return StopLength
	default:
		return StopError
	}
}

// GoogleErrorPatterns are the finish reasons that map to errors (exhaustive
// upstream list, kept for documentation value).
var googleErrorFinishReasons = []string{
	"BLOCKLIST", "PROHIBITED_CONTENT", "SPII", "SAFETY", "IMAGE_SAFETY",
	"IMAGE_PROHIBITED_CONTENT", "IMAGE_RECITATION", "IMAGE_OTHER", "RECITATION",
	"FINISH_REASON_UNSPECIFIED", "OTHER", "LANGUAGE", "MALFORMED_FUNCTION_CALL",
	"UNEXPECTED_TOOL_CALL", "TOO_MANY_TOOL_CALLS", "NO_IMAGE",
}
