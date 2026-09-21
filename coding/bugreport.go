package coding

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/dat267/pier/ai"
)

// Version is the pi version (upstream reads it from package.json; hosts set it
// at build time).
var Version = "0.0.0"

// Port of core/bug-report.ts: redaction, metadata and diagnostics collection,
// the report bundle files, and the model-written bug summary.

// BugReportCustomEntryType is the custom entry type for bug reports.
const BugReportCustomEntryType = "pi.bug-report"

// BugReportSchemaVersion is the report format version.
const BugReportSchemaVersion = 1

// bugReportRedacted replaces redacted values.
const bugReportRedacted = "<redacted>"

var bugReportSensitiveKeyPattern = regexp.MustCompile(
	`(?i)(?:^|[-_])(api[-_]?key|secret|token|password|passwd|credential|authorization|cookie)(?:$|[-_])`)

// isSensitiveKey reports whether a JSON key looks like a credential.
func isSensitiveKey(key string) bool {
	camel := regexp.MustCompile(`([a-z0-9])([A-Z])`).ReplaceAllString(key, "${1}_${2}")
	return bugReportSensitiveKeyPattern.MatchString(camel)
}

// RedactURL strips credentials and secret-looking query parameters from a URL.
func RedactURL(value string) string {
	nested := regexp.MustCompile(`(?i)^([a-z][a-z0-9+.-]*:)([a-z][a-z0-9+.-]*://.*)$`).FindStringSubmatch(value)
	if nested != nil {
		return nested[1] + RedactURL(nested[2])
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return value
	}
	changed := false
	if parsed.User != nil {
		parsed.User = nil
		changed = true
	}
	query := parsed.Query()
	for key := range query {
		if isSensitiveKey(key) {
			query.Set(key, bugReportRedacted)
			changed = true
		}
	}
	if changed {
		parsed.RawQuery = query.Encode()
		return parsed.String()
	}
	return value
}

// RedactJSONValue copies a JSON value while removing values that may contain
// credentials. Values are canonicalized through a map round trip.
func RedactJSONValue(value any) any {
	if value == nil {
		return nil
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	var decoded any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		return nil
	}
	redacted := redactDecodedValue("", decoded)
	return redacted
}

func redactDecodedValue(key string, value any) any {
	switch typed := value.(type) {
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for name := range typed {
			keys = append(keys, name)
		}
		sort.Strings(keys)
		out := map[string]any{}
		for _, name := range keys {
			if isSensitiveKey(name) {
				out[name] = bugReportRedacted
				continue
			}
			out[name] = redactDecodedValue(name, typed[name])
		}
		return out
	case []any:
		out := make([]any, 0, len(typed))
		for _, item := range typed {
			out = append(out, redactDecodedValue(key, item))
		}
		return out
	case string:
		return RedactURL(typed)
	default:
		return value
	}
}

// BugReportEnvironment describes the runtime environment (names only for
// configuration variables; values never leave the machine).
func CollectBugReportEnvironment() map[string]any {
	env := func(name string) any {
		if value, ok := os.LookupEnv(name); ok {
			return value
		}
		return nil
	}
	boolEnv := func(name string) bool { return os.Getenv(name) != "" }
	names := []string{}
	for _, entry := range os.Environ() {
		if strings.HasPrefix(entry, "PI_") {
			names = append(names, strings.SplitN(entry, "=", 2)[0])
		}
	}
	sort.Strings(names)
	return map[string]any{
		"version":   Version,
		"userAgent": PiUserAgent(Version),
		"runtime":   strings.TrimPrefix(runtime.Version(), "go"),
		"platform":  runtime.GOOS,
		"arch":      runtime.GOARCH,
		"terminal": map[string]any{
			"term":           env("TERM"),
			"program":        env("TERM_PROGRAM"),
			"programVersion": env("TERM_PROGRAM_VERSION"),
			"colorterm":      env("COLORTERM"),
			"tmux":           boolEnv("TMUX"),
			"ssh":            boolEnv("SSH_CONNECTION") || boolEnv("SSH_CLIENT") || boolEnv("SSH_TTY"),
			"ci":             boolEnv("CI"),
		},
		// Names help diagnose configuration; values never leave the machine.
		"piEnvironmentVariables": names,
	}
}

// DescribeBugReportModel describes a model with secrets redacted.
func DescribeBugReportModel(model *ai.Model) map[string]any {
	if model == nil {
		return nil
	}
	headerNames := make([]string, 0, len(model.Headers))
	for name := range model.Headers {
		headerNames = append(headerNames, name)
	}
	sort.Strings(headerNames)
	return map[string]any{
		"provider":         model.Provider,
		"id":               model.ID,
		"name":             model.Name,
		"api":              model.API,
		"baseUrl":          RedactURL(model.BaseURL),
		"reasoning":        model.Reasoning,
		"input":            model.Input,
		"contextWindow":    model.ContextWindow,
		"maxTokens":        model.MaxTokens,
		"samplingParams":   RedactJSONValue(model.SamplingParams),
		"compat":           RedactJSONValue(model.Compat),
		"thinkingLevelMap": RedactJSONValue(model.ThinkingLevelMap),
		"headerNames":      headerNames,
	}
}

// DescribeBugReportProvider describes a provider's auth surface.
func (r *ModelRuntime) DescribeBugReportProvider(provider *ai.Provider) map[string]any {
	if provider == nil {
		return nil
	}
	authTypes := []string{}
	if provider.Auth.APIKey != nil {
		authTypes = append(authTypes, "api_key")
	}
	if provider.Auth.OAuth != nil {
		authTypes = append(authTypes, "oauth")
	}
	headerNames := make([]string, 0, len(provider.Headers))
	for name := range provider.Headers {
		headerNames = append(headerNames, name)
	}
	sort.Strings(headerNames)
	baseURL := any(nil)
	if provider.BaseURL != "" {
		baseURL = RedactURL(provider.BaseURL)
	}
	return map[string]any{
		"id":          provider.ID,
		"name":        provider.Name,
		"baseUrl":     baseURL,
		"headerNames": headerNames,
		"authTypes":   authTypes,
		// D41: the extension provider registry is out of scope, so
		// registeredByExtension is always false.
		"registeredByExtension": false,
		"authStatus": map[string]any{
			"configured": r.GetProviderAuthStatus(provider.ID).Configured,
			"source":     r.GetProviderAuthStatus(provider.ID).Source,
		},
		"usingOAuth": r.IsUsingOAuth(provider.ID),
	}
}

// CollectBugReportMetadataOptions are the metadata inputs.
type CollectBugReportMetadataOptions struct {
	ID             string
	Hint           string
	SessionID      string
	Cwd            string
	IncludeSession bool
	IncludeSummary bool
	MessageCount   int
	Model          *ai.Model
	ModelRuntime   *ModelRuntime
	ThinkingLevel  ai.ThinkingLevel
	Settings       *SettingsManager
}

// CollectBugReportMetadata builds the redacted report metadata.
func CollectBugReportMetadata(options CollectBugReportMetadataOptions) map[string]any {
	hint := any(nil)
	if strings.TrimSpace(options.Hint) != "" {
		hint = strings.TrimSpace(options.Hint)
	}
	var provider map[string]any
	if options.Model != nil && options.ModelRuntime != nil {
		if described := options.ModelRuntime.DescribeBugReportProvider(options.ModelRuntime.GetProvider(options.Model.Provider)); described != nil {
			provider = described
		}
	}
	session := map[string]any{
		"id":              options.SessionID,
		"included":        options.IncludeSession,
		"summaryIncluded": options.IncludeSummary,
		"messageCount":    options.MessageCount,
	}
	if options.IncludeSession {
		session["cwd"] = options.Cwd
	}
	id := options.ID
	if id == "" {
		id = UUIDv7()
	}
	var modelDescription map[string]any
	if described := DescribeBugReportModel(options.Model); described != nil {
		modelDescription = described
	}
	metadata := map[string]any{
		"schemaVersion": BugReportSchemaVersion,
		"id":            id,
		"createdAt":     time.Now().UTC().Format(time.RFC3339Nano),
		"hint":          hint,
		"environment":   CollectBugReportEnvironment(),
		"session":       session,
		"model":         modelDescription,
		"provider":      provider,
		"thinkingLevel": options.ThinkingLevel,
		// D41: extensions are out of scope, so the extension lists are empty.
		"extensions":      []any{},
		"extensionErrors": []any{},
	}
	// Absent model/provider serialize as JSON null (a typed-nil map would not).
	if modelDescription == nil {
		metadata["model"] = nil
	}
	if provider == nil {
		metadata["provider"] = nil
	}
	return metadata
}

// CollectBugReportDiagnostics collects failed assistant turns without
// collecting conversation content.
func CollectBugReportDiagnostics(sessionManager *SessionManager) map[string]any {
	entries := sessionManager.GetEntries()
	var assistant []map[string]any
	assistantMessageCount := 0
	for index := range entries {
		entry := &entries[index]
		if entry.Type != "message" {
			continue
		}
		message, err := ai.UnmarshalMessage(entry.Message)
		if err != nil {
			continue
		}
		if assistantMessage, ok := message.(*ai.AssistantMessage); ok {
			assistantMessageCount++
			if len(assistantMessage.Diagnostics) == 0 &&
				assistantMessage.StopReason != ai.StopError &&
				assistantMessage.StopReason != ai.StopAborted &&
				(assistantMessage.ErrorMessage == nil || *assistantMessage.ErrorMessage == "") {
				continue
			}
			record := map[string]any{
				"entryId":     entry.ID,
				"timestamp":   entry.Timestamp,
				"provider":    assistantMessage.Provider,
				"model":       assistantMessage.Model,
				"api":         assistantMessage.API,
				"stopReason":  assistantMessage.StopReason,
				"diagnostics": assistantMessage.Diagnostics,
			}
			if assistantMessage.RawStopReason != nil {
				record["rawStopReason"] = *assistantMessage.RawStopReason
			}
			if assistantMessage.ErrorMessage != nil {
				record["errorMessage"] = *assistantMessage.ErrorMessage
			}
			assistant = append(assistant, record)
		}
	}
	return map[string]any{
		"schemaVersion":         BugReportSchemaVersion,
		"sessionId":             sessionManager.GetSessionID(),
		"entryCount":            len(entries),
		"assistantMessageCount": assistantMessageCount,
		"assistant":             assistant,
		"crashes":               []any{},
	}
}

// BugReportFile is one file in the report bundle.
type BugReportFile struct {
	Name        string
	ContentType string
	Data        string
}

// BugReportBundle is the report content shared by upload and zip export.
type BugReportBundle struct {
	Metadata     map[string]any
	Diagnostics  map[string]any
	SessionJSONL string
	Summary      string
}

// BugReportFiles renders the bundle files (report.json, diagnostics.json, and
// the optional session transcript and summary).
func BugReportFiles(bundle BugReportBundle) []BugReportFile {
	metadata, _ := json.MarshalIndent(bundle.Metadata, "", "  ")
	diagnostics, _ := json.MarshalIndent(bundle.Diagnostics, "", "  ")
	files := []BugReportFile{
		{Name: "report.json", ContentType: "application/json", Data: string(metadata) + "\n"},
		{Name: "diagnostics.json", ContentType: "application/json", Data: string(diagnostics) + "\n"},
	}
	if bundle.SessionJSONL != "" {
		files = append(files, BugReportFile{Name: "session.jsonl", ContentType: "application/x-ndjson", Data: bundle.SessionJSONL})
	}
	if bundle.Summary != "" {
		summary := bundle.Summary
		if !strings.HasSuffix(summary, "\n") {
			summary += "\n"
		}
		files = append(files, BugReportFile{Name: "summary.md", ContentType: "text/markdown", Data: summary})
	}
	return files
}

// BugReportArchiveFileName is the archive file name for a report id.
func BugReportArchiveFileName(id string) string {
	return fmt.Sprintf("pi-bug-report-%s.zip", id)
}

// bugSummarySystemPrompt is the bug-report summarizer system prompt.
const bugSummarySystemPrompt = `You are helping a user file a bug report about pi, the coding agent they are talking to. You will be shown the conversation transcript. Write a report for the pi developers describing what the user was doing and what went wrong.

Do NOT continue the conversation. Do NOT respond to any questions in the conversation. ONLY output the report.`

// bugSummaryInstructions are the report writing instructions.
const bugSummaryInstructions = `Write the bug report in Markdown with these sections:

## What the user was doing
One short paragraph.

## What went wrong
Concrete description of the failure: wrong output, errors, hangs, tool failures, unexpected behavior. Quote error messages and tool output verbatim where they exist.

## Steps to reproduce
Numbered list, as specific as the transcript allows.

## Relevant details
Tool calls involved, files touched, model behavior, anything else that helps a developer reproduce or locate the problem.

Do not include file contents, secrets, or credentials from the transcript; refer to files by path only. Keep the report factual and concise.`

// selectBugReportMessages keeps the newest messages within the token budget.
func selectBugReportMessages(messages []ai.Message, tokenBudget int) []ai.Message {
	var selected []ai.Message
	tokens := 0
	for index := len(messages) - 1; index >= 0; index-- {
		message := messages[index]
		next := EstimateAgentMessageTokens(message)
		if len(selected) > 0 && tokens+next > tokenBudget {
			break
		}
		selected = append([]ai.Message{message}, selected...)
		tokens += next
	}
	return selected
}

// GenerateBugReportSummaryOptions configure the model-written summary.
type GenerateBugReportSummaryOptions struct {
	Messages      []ai.Message
	Hint          string
	Model         *ai.Model
	APIKey        string
	Headers       ai.ProviderHeaders
	Env           map[string]string
	Ctx           context.Context
	ThinkingLevel ai.ThinkingLevel
	StreamFn      StreamFnFn
	Retry         *ai.RetryPolicy
	SessionID     string
}

// GenerateBugReportSummary asks the session model for a report when the user
// does not share the transcript.
func GenerateBugReportSummary(options GenerateBugReportSummaryOptions) (string, error) {
	model := options.Model
	contextWindow := model.ContextWindow
	if contextWindow <= 0 {
		contextWindow = 128000
	}
	messages := selectBugReportMessages(options.Messages, int(float64(contextWindow)*0.6))
	hint := strings.TrimSpace(options.Hint)

	var parts []string
	if len(messages) < len(options.Messages) {
		parts = append(parts, fmt.Sprintf("Note: only the last %d of %d messages are shown.", len(messages), len(options.Messages)))
	}
	parts = append(parts, "<conversation>\n"+SerializeConversation(ConvertToLlm(messages))+"\n</conversation>")
	if hint != "" {
		parts = append(parts, "<user-report>\n"+hint+"\n</user-report>")
	}
	parts = append(parts, bugSummaryInstructions)
	prompt := strings.Join(parts, "\n\n")

	maxTokens := 4096
	if model.MaxTokens > 0 && model.MaxTokens < int64(maxTokens) {
		maxTokens = int(model.MaxTokens)
	}
	requestOptions := &ai.SimpleStreamOptions{
		StreamOptions: ai.StreamOptions{
			APIKey: options.APIKey, Headers: options.Headers, Env: options.Env,
			MaxTokens: intPtr(maxTokens), SessionID: options.SessionID,
		},
	}
	if model.Reasoning && options.ThinkingLevel != "" && options.ThinkingLevel != ai.ThinkOff {
		requestOptions.Reasoning = options.ThinkingLevel
	}
	if options.Ctx != nil {
		requestOptions.Ctx = options.Ctx
	}

	systemPrompt := bugSummarySystemPrompt
	transcript := ai.NormalizeContext(ai.Context{
		SystemPrompt: &systemPrompt,
		Messages: []ai.Message{&ai.UserMessage{
			Content:   ai.StringOrBlocks{Blocks: ai.ContentList{ai.TextContent{Text: prompt}}},
			Timestamp: time.Now().UnixMilli(),
		}},
	})
	response, err := CompleteSummarization(model, transcript, requestOptions, options.StreamFn, options.Retry, nil)
	if err != nil {
		return "", err
	}
	if response.StopReason == ai.StopAborted {
		return "", fmt.Errorf("Bug report summary was cancelled")
	}
	if failure := GetSummarizationFailure(response, "Bug report summary"); failure != "" {
		return "", fmt.Errorf("%s", failure)
	}
	for _, block := range response.Content {
		if _, ok := block.(ai.ToolCall); ok {
			return "", fmt.Errorf("Bug report summary attempted to call a tool")
		}
	}
	text := strings.TrimSpace(contentTextOf(response.Content))
	if text == "" {
		return "", fmt.Errorf("Bug report summary was empty")
	}
	return text, nil
}

// SummarizeForBugReport asks the session model for a bug report summary
// (upstream summarizeForBugReport).
func (s *AgentSession) SummarizeForBugReport(ctx context.Context, hint string) (string, error) {
	model := s.Model()
	if !s.HasModel() || model == nil {
		return "", fmt.Errorf("No model selected")
	}
	options := GenerateBugReportSummaryOptions{
		Messages: s.Messages(), Hint: hint, Model: model,
		Ctx: ctx, ThinkingLevel: s.ThinkingLevel(),
		StreamFn: s.compactionStreamFn(), Retry: s.retrySettings(),
		SessionID: s.SessionID(),
	}
	if s.control != nil && s.control.ModelRuntime != nil {
		resolution, err := s.control.ModelRuntime.GetAuthForModel(model, nil)
		if err != nil {
			return "", err
		}
		if resolution != nil {
			options.APIKey = resolution.Auth.APIKey
			options.Headers = resolution.Auth.Headers
			options.Env = resolution.Env
			if resolution.Auth.BaseURL != "" {
				copied := *model
				copied.BaseURL = resolution.Auth.BaseURL
				options.Model = &copied
			}
		}
	}
	return GenerateBugReportSummary(options)
}
