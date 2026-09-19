package ai

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Port of utils/error-body.ts: shared normalization for provider HTTP error
// objects. In Go, provider errors are ProviderError values carrying the
// status and raw body, so the SDK-shape probing collapses to reading those.

const MaxProviderErrorBodyChars = 4000

// NormalizedProviderError is the shared error surface providers compose into
// display strings.
type NormalizedProviderError struct {
	// Status is the HTTP status code, when one could be extracted.
	Status int
	// Body is the raw HTTP body reason, trimmed and truncated to the cap.
	Body string
	// Message is error.Error().
	Message string
	// MessageCarriesBody is true when Message already contains the body.
	MessageCarriesBody bool
}

// NormalizeProviderError probes the error for status/body. In Go the only
// error shape carrying HTTP data is *ProviderError.
func NormalizeProviderError(err error) NormalizedProviderError {
	if err == nil {
		return NormalizedProviderError{Message: "undefined"}
	}
	pe, ok := err.(*ProviderError)
	if !ok {
		return NormalizedProviderError{Message: err.Error(), MessageCarriesBody: false}
	}
	body := strings.TrimSpace(pe.Body)
	if body == "" {
		body = ""
	} else {
		body = TruncateErrorText(body, MaxProviderErrorBodyChars)
	}
	messageCarriesBody := body == "" || strings.Contains(pe.Message, body)
	return NormalizedProviderError{
		Status: pe.Status, Body: body, Message: pe.Message, MessageCarriesBody: messageCarriesBody,
	}
}

// FormatProviderError composes a display string from a normalized error.
// When the message already carries the body or no body/status was extracted,
// the message is returned unchanged; otherwise status and body are surfaced
// with an optional provider prefix.
func FormatProviderError(norm NormalizedProviderError, prefix string) string {
	if norm.MessageCarriesBody || norm.Status == 0 || norm.Body == "" {
		if prefix != "" && norm.Status != 0 {
			return fmt.Sprintf("%s (%d): %s", prefix, norm.Status, norm.Message)
		}
		return norm.Message
	}
	if prefix != "" {
		return fmt.Sprintf("%s (%d): %s", prefix, norm.Status, norm.Body)
	}
	return fmt.Sprintf("%d: %s", norm.Status, norm.Body)
}

// TruncateErrorText appends an ellipsis marker beyond the cap.
func TruncateErrorText(text string, maxChars int) string {
	if JSLength(text) <= maxChars {
		return text
	}
	return JSSlice(text, 0, maxChars) + fmt.Sprintf("... [truncated %d chars]", JSLength(text)-maxChars)
}

// SafeJSONStringify serializes or falls back to String(value).
func SafeJSONStringify(v any) string {
	enc, err := MarshalJSON(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(enc)
}

// ShortHash is a fast deterministic hash to shorten long strings
// (port of utils/hash.ts shortHash with JS 32-bit integer semantics).
func ShortHash(str string) string {
	const (
		h1Seed = uint32(0xdeadbeef)
		h2Seed = uint32(0x41c6ce57)
	)
	h1, h2 := h1Seed, h2Seed
	for _, ch := range []rune(str) {
		// JS charCodeAt is UTF-16; use code units.
		_ = ch
		break
	}
	units := utf16Units(str)
	for _, ch := range units {
		h1 = imul(h1^uint32(ch), 2654435761)
		h2 = imul(h2^uint32(ch), 1597334677)
	}
	h1 = imul(h1^(h1>>16), 2246822507) ^ imul(h2^(h2>>13), 3266489909)
	h2 = imul(h2^(h2>>16), 2246822507) ^ imul(h1^(h1>>13), 3266489909)
	return jsUintToString(h2, 36) + jsUintToString(h1, 36)
}

func utf16Units(s string) []uint16 {
	out := make([]uint16, 0, len(s))
	for _, r := range s {
		if r <= 0xFFFF {
			out = append(out, uint16(r))
		} else {
			r -= 0x10000
			out = append(out, uint16(0xD800+(r>>10)), uint16(0xDC00+(r&0x3FF)))
		}
	}
	return out
}

// imul is JS Math.imul: 32-bit integer multiplication.
func imul(a, b uint32) uint32 { return a * b }

// jsUintToString formats like JS (x >>> 0).toString(radix).
func jsUintToString(v uint32, radix int) string {
	if v == 0 {
		return "0"
	}
	digits := "0123456789abcdefghijklmnopqrstuvwxyz"
	var out []byte
	for v > 0 {
		out = append([]byte{digits[v%uint32(radix)]}, out...)
		v /= uint32(radix)
	}
	return string(out)
}

// ClampOpenAIPromptCacheKey clamps a prompt cache key to OpenAI's 64-char
// limit by code points (port of openai-prompt-cache.ts).
const OpenAIPromptCacheKeyMaxLength = 64

func ClampOpenAIPromptCacheKey(key string) string {
	runes := []rune(key)
	if len(runes) <= OpenAIPromptCacheKeyMaxLength {
		return key
	}
	return string(runes[:OpenAIPromptCacheKeyMaxLength])
}

// --- api/github-copilot-headers.ts ---

// InferCopilotInitiator reports whether the request is user-initiated or
// agent-initiated.
func InferCopilotInitiator(messages []Message) string {
	if last := len(messages) - 1; last >= 0 {
		if RoleOf(messages[last]) != RoleUser {
			return "agent"
		}
	}
	return "user"
}

// HasCopilotVisionInput reports whether the transcript carries images
// (Copilot requires the Copilot-Vision-Request header then).
func HasCopilotVisionInput(messages []Message) bool {
	for _, msg := range messages {
		switch m := msg.(type) {
		case *UserMessage:
			for _, block := range m.Content.Blocks {
				if _, ok := block.(ImageContent); ok {
					return true
				}
			}
		case *ToolResultMessage:
			for _, block := range m.Content {
				if _, ok := block.(ImageContent); ok {
					return true
				}
			}
		}
	}
	return false
}

// BuildCopilotDynamicHeaders builds Copilot's per-request headers.
func BuildCopilotDynamicHeaders(messages []Message, hasImages bool) map[string]string {
	headers := map[string]string{
		"X-Initiator":   InferCopilotInitiator(messages),
		"Openai-Intent": "conversation-edits",
	}
	if hasImages {
		headers["Copilot-Vision-Request"] = "true"
	}
	return headers
}

// --- grammar constrained sampling (api/constrained-sampling.ts remainder) ---

// GrammarConstrainedSampling resolves a tool's grammar variant.
type GrammarConstrainedSampling struct {
	Format        string // "lark" | "regex"
	Definition    string
	InputProperty string
}

// InferGrammarInputProperty requires exactly one required string property.
func inferGrammarInputProperty(tool Tool) (string, error) {
	var schema struct {
		Type       string                     `json:"type"`
		Required   []string                   `json:"required"`
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := jsonUnmarshalStrict(tool.Parameters, &schema); err != nil {
		return "", fmt.Errorf("grammar constrained sampling requires an object parameter schema")
	}
	if schema.Type != "object" {
		return "", fmt.Errorf("grammar constrained sampling requires an object parameter schema")
	}
	if len(schema.Required) != 1 {
		return "", fmt.Errorf("grammar constrained sampling requires exactly one required string property")
	}
	inputProperty := schema.Required[0]
	prop, ok := schema.Properties[inputProperty]
	if !ok {
		return "", fmt.Errorf("grammar constrained sampling requires a properties entry for %s", inputProperty)
	}
	var propSchema struct {
		Type string `json:"type"`
	}
	if err := jsonUnmarshalStrict(prop, &propSchema); err != nil || propSchema.Type != "string" {
		return "", fmt.Errorf("grammar constrained sampling property %s must have type string", inputProperty)
	}
	return inputProperty, nil
}

// ResolveGrammarConstrainedSampling resolves the tool's grammar variant
// (port of resolveGrammarConstrainedSampling).
func ResolveGrammarConstrainedSampling(tool Tool, supportsOpenAIGrammarTools bool) (*GrammarConstrainedSampling, error) {
	config := tool.ConstrainedSampling
	if !config.Set || config.False || config.Config == nil || config.Config.Type != "grammar" {
		return nil, nil
	}
	if !supportsOpenAIGrammarTools {
		return nil, nil
	}
	larkDefinition, hasLark := config.Config.Variants["openai_lark"]
	regexDefinition, hasRegex := config.Config.Variants["openai_regex"]
	hasLarkDefinition := hasLark && strings.TrimSpace(larkDefinition) != ""
	hasRegexDefinition := hasRegex && strings.TrimSpace(regexDefinition) != ""
	if !hasLarkDefinition && !hasRegexDefinition {
		return nil, fmt.Errorf("Tool %q cannot use grammar constrained sampling: no supported grammar variant was provided.", tool.Name)
	}
	inputProperty, err := inferGrammarInputProperty(tool)
	if err != nil {
		return nil, fmt.Errorf("Tool %q cannot use grammar constrained sampling: %s", tool.Name, err.Error())
	}
	if hasLarkDefinition {
		return &GrammarConstrainedSampling{Format: "lark", Definition: larkDefinition, InputProperty: inputProperty}, nil
	}
	return &GrammarConstrainedSampling{Format: "regex", Definition: regexDefinition, InputProperty: inputProperty}, nil
}

// GetGrammarToolInput extracts the grammar input property value
// (port of getGrammarToolInput).
func GetGrammarToolInput(toolName string, arguments json.RawMessage, inputProperty string) (string, error) {
	var args map[string]json.RawMessage
	if err := jsonUnmarshalStrict(arguments, &args); err == nil {
		if raw, ok := args[inputProperty]; ok {
			var s string
			if err := jsonUnmarshalStrict(raw, &s); err == nil {
				return s, nil
			}
		}
	}
	return "", fmt.Errorf("Grammar tool call %q requires argument %q to be a string.", toolName, inputProperty)
}

// GrammarToolInputJSONBuffer tracks the streaming custom-tool input.
type GrammarToolInputJSONBuffer struct {
	Input   string
	Started bool
	Closed  bool
}

// AppendGrammarToolInputJSONDelta wraps input deltas into a JSON object
// stream (port of appendGrammarToolInputJsonDelta). Returns "" when no
// delta should be emitted; ok is false on errors.
func AppendGrammarToolInputJSONDelta(buffer *GrammarToolInputJSONBuffer, inputProperty, nextInput string, close bool) (delta string, ok bool, err error) {
	if buffer.Closed {
		if close && nextInput == buffer.Input {
			return "", true, nil
		}
		return "", false, fmt.Errorf("grammar tool input for property %q changed after it was closed", inputProperty)
	}
	if !strings.HasPrefix(nextInput, buffer.Input) {
		return "", false, fmt.Errorf("grammar tool input for property %q changed non-monotonically", inputProperty)
	}
	inputDelta := nextInput[JSLength(buffer.Input):]
	if !close && len(inputDelta) == 0 {
		return "", true, nil
	}
	var out strings.Builder
	if !buffer.Started {
		out.WriteString("{")
		keyEnc, _ := MarshalJSON(inputProperty)
		out.Write(keyEnc)
		out.WriteString(":\"")
		buffer.Started = true
	}
	enc, _ := MarshalJSON(inputDelta)
	out.Write(enc[1 : len(enc)-1])
	buffer.Input = nextInput
	if close {
		out.WriteString("\"}")
		buffer.Closed = true
	}
	return out.String(), true, nil
}

// CreateGrammarToolInputProperties maps tool names to their grammar input
// properties (port of createGrammarToolInputProperties).
func CreateGrammarToolInputProperties(tools []Tool, supportsOpenAIGrammarTools bool) map[string]string {
	properties := map[string]string{}
	for _, tool := range tools {
		grammar, err := ResolveGrammarConstrainedSampling(tool, supportsOpenAIGrammarTools)
		if err == nil && grammar != nil {
			properties[tool.Name] = grammar.InputProperty
		}
	}
	return properties
}

var _ = sort.Strings
