package ai

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// Port of the AWS resolution and error handling in
// api/bedrock-converse-stream.ts (region/credential/bearer resolution, the
// endpoint decision, error formatting, reserved headers, and stop reasons).

// BedrockOptions are the Bedrock Converse stream options (upstream
// BedrockOptions).
type BedrockOptions struct {
	StreamOptions
	// Region overrides the resolved AWS region.
	Region string
	// Profile selects an AWS profile for credentials.
	Profile string
	// ToolChoice is "auto" | "any" | "none" or a named tool.
	ToolChoice string
	// ToolChoiceName names the tool for a forced tool choice.
	ToolChoiceName string
	// Reasoning is the thinking level.
	Reasoning ThinkingLevel
	// ThinkingBudgets are custom token budgets per thinking level.
	ThinkingBudgets *ThinkingBudgets
	// InterleavedThinking enables Claude 4.x interleaved thinking.
	InterleavedThinking *bool
	// ThinkingDisplay is "summarized" (default) or "omitted".
	ThinkingDisplay string
	// RequestMetadata attaches cost-allocation tags to the request.
	RequestMetadata map[string]string
	// BearerToken uses Bedrock API-key authentication instead of SigV4.
	BearerToken string
}

// BEDROCK_ERROR_PREFIXES maps AWS SDK exception names to their human-readable
// prefixes (upstream keeps the legacy prefix format for downstream matching).
var bedrockErrorPrefixes = map[string]string{
	"InternalServerException":     "Internal server error",
	"ModelStreamErrorException":   "Model stream error",
	"ValidationException":         "Validation error",
	"ThrottlingException":         "Throttling error",
	"ServiceUnavailableException": "Service unavailable",
}

// BedrockDataRetentionDocsURL is the data-retention troubleshooting link.
const BedrockDataRetentionDocsURL = "https://docs.aws.amazon.com/bedrock/latest/userguide/data-retention.html"

// FormatBedrockError renders a Bedrock failure with its stable prefix
// (upstream formatBedrockError).
func FormatBedrockError(err error) string {
	if err == nil {
		return ""
	}
	norm := NormalizeProviderError(err)
	core := norm.Message
	if !norm.MessageCarriesBody && norm.Status != 0 && norm.Body != "" {
		core = fmt.Sprintf("%d: %s", norm.Status, norm.Body)
	}
	hint := ""
	if strings.Contains(strings.ToLower(core), "data retention mode") {
		hint = " See " + BedrockDataRetentionDocsURL + " for supported data retention modes."
	}
	if providerErr, ok := errAsProviderError(err); ok {
		// Only modeled Bedrock service exceptions get the prefix treatment; a
		// transport name such as TimeoutError falls through to the plain
		// message (upstream checks the SDK exception base class).
		if strings.HasSuffix(providerErr.Code, "Exception") {
			prefix := providerErr.Code
			if mapped, ok := bedrockErrorPrefixes[providerErr.Code]; ok {
				prefix = mapped
			}
			return prefix + ": " + core + hint
		}
	}
	return core + hint
}

// errAsProviderError extracts a provider error carrying an AWS exception code.
func errAsProviderError(err error) (*ProviderError, bool) {
	for err != nil {
		if providerErr, ok := err.(*ProviderError); ok {
			return providerErr, true
		}
		unwrapper, ok := err.(interface{ Unwrap() error })
		if !ok {
			break
		}
		err = unwrapper.Unwrap()
	}
	return nil, false
}

// maxBedrockDiagnosticValueChars bounds diagnostic values; over-long values are
// dropped rather than truncated.
const maxBedrockDiagnosticValueChars = 200

// NormalizeBedrockDiagnosticValue trims a diagnostic string, dropping empty and
// over-long values.
func NormalizeBedrockDiagnosticValue(value string) (string, bool) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" || len(trimmed) > maxBedrockDiagnosticValueChars {
		return "", false
	}
	return trimmed, true
}

// BedrockErrorCode extracts the modeled exception name from an error (upstream
// extractBedrockErrorCode: only names ending in "Exception" qualify).
func BedrockErrorCode(err error) (string, bool) {
	if err == nil {
		return "", false
	}
	if providerErr, ok := errAsProviderError(err); ok && providerErr.Code != "" {
		if strings.HasSuffix(providerErr.Code, "Exception") {
			return NormalizeBedrockDiagnosticValue(providerErr.Code)
		}
		return "", false
	}
	return "", false
}

// BedrockFailureDetails is the structured diagnostic payload for a failure.
type BedrockFailureDetails struct {
	Status    *int   `json:"status,omitempty"`
	ErrorCode string `json:"errorCode,omitempty"`
	RequestID string `json:"requestId,omitempty"`
}

// AppendBedrockFailureDiagnostic records a bedrock_response_failure diagnostic
// when any detail is known (upstream appendBedrockFailureDiagnostic).
func AppendBedrockFailureDiagnostic(output *AssistantMessage, err error, fallbackRequestID string) {
	details := BedrockFailureDetails{}
	if providerErr, ok := errAsProviderError(err); ok && providerErr.Status != 0 {
		status := providerErr.Status
		details.Status = &status
	}
	if code, ok := BedrockErrorCode(err); ok {
		details.ErrorCode = code
	}
	requestID := fallbackRequestID
	if normalized, ok := NormalizeBedrockDiagnosticValue(requestID); ok {
		details.RequestID = normalized
	}
	if details.Status == nil && details.ErrorCode == "" && details.RequestID == "" {
		return
	}
	encoded, marshalErr := MarshalJSON(details)
	if marshalErr != nil {
		return
	}
	AppendAssistantMessageDiagnostic(output, CreateAssistantMessageDiagnostic(
		"bedrock_response_failure", nil, encoded))
}

// BedrockReservedHeadersExact are the header names SigV4/auth own.
var bedrockReservedHeadersExact = map[string]bool{"authorization": true, "host": true}

// IsReservedBedrockHeader reports whether caller headers may not override a
// header (upstream isReservedHeader).
func IsReservedBedrockHeader(key string) bool {
	lower := strings.ToLower(key)
	return strings.HasPrefix(lower, "x-amz-") || bedrockReservedHeadersExact[lower]
}

// GetConfiguredBedrockRegion resolves the region from options and env
// (upstream getConfiguredBedrockRegion).
func GetConfiguredBedrockRegion(options *BedrockOptions) string {
	if options != nil && options.Region != "" {
		return options.Region
	}
	var env ProviderEnv
	if options != nil {
		env = options.Env
	}
	if value, ok := GetProviderEnvValue("AWS_REGION", env); ok {
		return value
	}
	if value, ok := GetProviderEnvValue("AWS_DEFAULT_REGION", env); ok {
		return value
	}
	return ""
}

// GetConfiguredBedrockCredentials reads explicit env credentials (upstream
// getConfiguredBedrockCredentials).
func GetConfiguredBedrockCredentials(env ProviderEnv) (AWSCredentials, bool) {
	accessKeyID, ok := GetProviderEnvValue("AWS_ACCESS_KEY_ID", env)
	if !ok {
		return AWSCredentials{}, false
	}
	secret, ok := GetProviderEnvValue("AWS_SECRET_ACCESS_KEY", env)
	if !ok {
		return AWSCredentials{}, false
	}
	sessionToken, _ := GetProviderEnvValue("AWS_SESSION_TOKEN", env)
	return AWSCredentials{AccessKeyID: accessKeyID, SecretAccessKey: secret, SessionToken: sessionToken}, true
}

// GetStandardBedrockEndpointRegion extracts the region from a standard Bedrock
// runtime hostname (upstream getStandardBedrockEndpointRegion).
func GetStandardBedrockEndpointRegion(baseURL string) string {
	if baseURL == "" {
		return ""
	}
	parsed, err := parseURLHost(baseURL)
	if err != nil {
		return ""
	}
	host := strings.ToLower(parsed)
	prefix := "bedrock-runtime"
	if !strings.HasPrefix(host, prefix) {
		return ""
	}
	rest := strings.TrimPrefix(host, prefix)
	rest = strings.TrimPrefix(rest, "-fips")
	if !strings.HasPrefix(rest, ".") {
		return ""
	}
	rest = strings.TrimPrefix(rest, ".")
	// Strip the trailing .amazonaws.com[.cn]
	const suffix = ".amazonaws.com"
	index := strings.Index(rest, suffix)
	if index <= 0 {
		return ""
	}
	region := rest[:index]
	if !isLowerAlnumDash(region) {
		return ""
	}
	return region
}

func parseURLHost(baseURL string) (string, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return "", err
	}
	return parsed.Hostname(), nil
}

func isLowerAlnumDash(value string) bool {
	for _, char := range value {
		switch {
		case char >= 'a' && char <= 'z', char >= '0' && char <= '9', char == '-':
		default:
			return false
		}
	}
	return value != ""
}

// ShouldUseExplicitBedrockEndpoint decides whether the model base URL is a
// custom endpoint that must be pinned (upstream
// shouldUseExplicitBedrockEndpoint).
func ShouldUseExplicitBedrockEndpoint(baseURL, configuredRegion string, hasAmbientConfiguredProfile bool) bool {
	if GetStandardBedrockEndpointRegion(baseURL) == "" {
		return true
	}
	return configuredRegion == "" && !hasAmbientConfiguredProfile
}

// IsGovCloudBedrockTarget reports whether a region or model id targets GovCloud
// (upstream isGovCloudBedrockTarget).
func IsGovCloudBedrockTarget(model *Model, options *BedrockOptions) bool {
	region := GetConfiguredBedrockRegion(options)
	if strings.HasPrefix(strings.ToLower(region), "us-gov-") {
		return true
	}
	if model == nil {
		return false
	}
	modelID := strings.ToLower(model.ID)
	return strings.HasPrefix(modelID, "us-gov.") || strings.HasPrefix(modelID, "arn:aws-us-gov:")
}

// ResolveAWSProfileCredentials loads credentials for a named profile from
// ~/.aws/credentials and ~/.aws/config (upstream delegates to the SDK's default
// chain; the Go port implements the file-based subset).
func ResolveAWSProfileCredentials(profile string, env ProviderEnv) (AWSCredentials, bool) {
	if profile == "" {
		return AWSCredentials{}, false
	}
	for _, path := range awsConfigPaths(env) {
		credentials, ok := readAWSProfileFile(path, profile)
		if ok {
			return credentials, true
		}
	}
	return AWSCredentials{}, false
}

func awsConfigPaths(env ProviderEnv) []string {
	if shared := GetProviderEnvValueOr("AWS_SHARED_CREDENTIALS_FILE", env); shared != "" {
		return []string{shared}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	dir := GetProviderEnvValueOr("AWS_CONFIG_FILE", env)
	if dir != "" {
		return []string{dir, filepath.Join(home, ".aws", "credentials")}
	}
	return []string{
		filepath.Join(home, ".aws", "credentials"),
		filepath.Join(home, ".aws", "config"),
	}
}

// readAWSProfileFile reads one INI file for a profile's credentials. The config
// file prefixes profile sections with "profile ".
func readAWSProfileFile(path, profile string) (AWSCredentials, bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return AWSCredentials{}, false
	}
	sections := parseAWSINI(string(raw))
	for _, name := range []string{profile, "profile " + profile} {
		values, ok := sections[name]
		if !ok {
			continue
		}
		accessKeyID := firstNonEmpty(values["aws_access_key_id"], values["aws_access_key"])
		secret := firstNonEmpty(values["aws_secret_access_key"], values["aws_secret_key"])
		if accessKeyID == "" || secret == "" {
			return AWSCredentials{}, false
		}
		return AWSCredentials{
			AccessKeyID: accessKeyID, SecretAccessKey: secret,
			SessionToken: values["aws_session_token"],
		}, true
	}
	return AWSCredentials{}, false
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

// parseAWSINI parses the simple INI subset AWS config files use.
func parseAWSINI(content string) map[string]map[string]string {
	sections := map[string]map[string]string{}
	current := ""
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, ";") {
			continue
		}
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			current = strings.TrimSpace(trimmed[1 : len(trimmed)-1])
			if sections[current] == nil {
				sections[current] = map[string]string{}
			}
			continue
		}
		if current == "" {
			continue
		}
		index := strings.Index(trimmed, "=")
		if index <= 0 {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(trimmed[:index]))
		value := strings.TrimSpace(trimmed[index+1:])
		sections[current][key] = value
	}
	return sections
}

// ResolveBedrockBearerToken resolves the bearer token for Bedrock API-key auth
// (upstream bearerToken resolution).
func ResolveBedrockBearerToken(options *BedrockOptions) (string, bool, bool) {
	var env ProviderEnv
	if options != nil {
		env = options.Env
	}
	skipAuth, _ := GetProviderEnvValue("AWS_BEDROCK_SKIP_AUTH", env)
	if options != nil && (options.BearerToken != "" || options.APIKey != "") {
		token := options.BearerToken
		if token == "" {
			token = options.APIKey
		}
		return token, token != "" && skipAuth != "1", skipAuth == "1"
	}
	envToken, _ := GetProviderEnvValue("AWS_BEARER_TOKEN_BEDROCK", env)
	return envToken, envToken != "" && skipAuth != "1", skipAuth == "1"
}

// MapBedrockStopReason maps a Bedrock stop reason (upstream mapStopReason).
func MapBedrockStopReason(reason string) (StopReason, string) {
	switch reason {
	case "end_turn", "stop_sequence":
		return StopStop, ""
	case "max_tokens", "model_context_window_exceeded":
		return StopLength, ""
	case "tool_use":
		return StopToolUse, ""
	default:
		if reason != "" {
			return StopError, "Provider stopped with: " + reason
		}
		return StopError, ""
	}
}

// ResolveBedrockRegion implements the region resolution order: an
// ARN-embedded region wins, then the configured region, then the endpoint
// region when the endpoint is explicit, then us-east-1.
func ResolveBedrockRegion(model *Model, options *BedrockOptions, configuredRegion, endpointRegion string, useExplicitEndpoint, hasAmbientConfiguredProfile bool) string {
	if model != nil {
		if arnRegion := bedrockARNRegion(model.ID); arnRegion != "" {
			return arnRegion
		}
	}
	if configuredRegion != "" {
		return configuredRegion
	}
	if endpointRegion != "" && useExplicitEndpoint {
		return endpointRegion
	}
	if !hasAmbientConfiguredProfile {
		return "us-east-1"
	}
	return ""
}

// bedrockARNRegion extracts the region from an inference-profile ARN.
func bedrockARNRegion(modelID string) string {
	if !strings.HasPrefix(modelID, "arn:aws") {
		return ""
	}
	parts := strings.Split(modelID, ":")
	// arn:aws[-partition]:bedrock:REGION:...
	if len(parts) < 4 || parts[2] != "bedrock" {
		return ""
	}
	return parts[3]
}

var _ = json.Marshal
