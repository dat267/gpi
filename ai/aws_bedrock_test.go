package ai

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Tests for the AWS plumbing behind the Bedrock adapter (SigV4, credentials,
// region/endpoint resolution, and the event-stream decoder).

func TestSigV4SigningKeyAndCanonicalRequest(t *testing.T) {
	// The AWS documentation's canonical "get-vanilla" vector.
	request, err := http.NewRequest(http.MethodGet, "https://example.amazonaws.com/", nil)
	if err != nil {
		t.Fatal(err)
	}
	credentials := AWSCredentials{
		AccessKeyID:     "AKIDEXAMPLE",
		SecretAccessKey: "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY",
	}
	now := time.Date(2015, 8, 30, 12, 36, 0, 0, time.UTC)
	result, err := SignAWSRequest(request, nil, credentials, "service", "us-east-1", now)
	if err != nil {
		t.Fatal(err)
	}
	const wantSignature = "5fa00fa31553b73ebf1942676e86291e8372ff2a2260956d9b8aae1d763fbf31"
	if result.Signature != wantSignature {
		t.Fatalf("signature = %s, want %s\ncanonical:\n%s\nstringToSign:\n%s",
			result.Signature, wantSignature, result.Canonical, result.StringToSign)
	}
	if result.SignedHeaders != "host;x-amz-date" {
		t.Fatalf("signed headers = %s", result.SignedHeaders)
	}
	wantAuthorization := "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20150830/us-east-1/service/aws4_request, " +
		"SignedHeaders=host;x-amz-date, Signature=" + wantSignature
	if result.Authorization != wantAuthorization {
		t.Fatalf("authorization = %s", result.Authorization)
	}
	if request.Header.Get("x-amz-date") != "20150830T123600Z" {
		t.Fatalf("x-amz-date = %s", request.Header.Get("x-amz-date"))
	}
}

func TestSigV4IncludesBodyHashSessionTokenAndContentType(t *testing.T) {
	body := []byte(`{"modelId":"m"}`)
	request, err := http.NewRequest(http.MethodPost, "https://bedrock-runtime.us-west-2.amazonaws.com/model/m/converse-stream", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	credentials := AWSCredentials{AccessKeyID: "AKID", SecretAccessKey: "SECRET", SessionToken: "TOKEN"}
	now := time.Date(2024, 5, 1, 10, 0, 0, 0, time.UTC)
	result, err := SignAWSRequest(request, body, credentials, "bedrock", "us-west-2", now)
	if err != nil {
		t.Fatal(err)
	}
	// content-type, host, x-amz-date and the session token are all signed.
	for _, header := range []string{"content-type", "host", "x-amz-date", "x-amz-security-token"} {
		if !strings.Contains(result.SignedHeaders, header) {
			t.Fatalf("signed headers %q missing %q", result.SignedHeaders, header)
		}
	}
	if !strings.Contains(result.Canonical, sha256Hex(body)) {
		t.Fatalf("canonical request does not carry the body hash:\n%s", result.Canonical)
	}
	if request.Header.Get("x-amz-security-token") != "TOKEN" {
		t.Fatalf("session token header = %q", request.Header.Get("x-amz-security-token"))
	}
	if !strings.Contains(result.StringToSign, "20240501/us-west-2/bedrock/aws4_request") {
		t.Fatalf("string to sign = %s", result.StringToSign)
	}
	// Signing without credentials is an error.
	if _, err := SignAWSRequest(request, body, AWSCredentials{}, "bedrock", "us-west-2", now); err == nil {
		t.Fatal("missing credentials must be rejected")
	}
}

func TestCanonicalizeAWSQuery(t *testing.T) {
	request, err := http.NewRequest(http.MethodGet, "https://example.com/path?b=2&a=1&a=0&c=x%20y", nil)
	if err != nil {
		t.Fatal(err)
	}
	got := canonicalizeAWSQuery(request.URL)
	if got != "a=0&a=1&b=2&c=x%20y" {
		t.Fatalf("canonical query = %q", got)
	}
}

func TestGetStandardBedrockEndpointRegion(t *testing.T) {
	cases := map[string]string{
		"https://bedrock-runtime.us-west-2.amazonaws.com":          "us-west-2",
		"https://bedrock-runtime-fips.us-gov-west-1.amazonaws.com": "us-gov-west-1",
		"https://bedrock-runtime.cn-north-1.amazonaws.com.cn":      "cn-north-1",
		"https://bedrock-runtime.us-east-1.amazonaws.com/model":    "us-east-1",
		"https://bedrock-runtime.us-east-1.amazonaws.com/path?x=1": "us-east-1",
		"https://custom.example.com":                               "",
		"https://bedrock.invalid.example.com":                      "",
		"":                                                         "",
	}
	for input, want := range cases {
		if got := GetStandardBedrockEndpointRegion(input); got != want {
			t.Errorf("GetStandardBedrockEndpointRegion(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestShouldUseExplicitBedrockEndpoint(t *testing.T) {
	// A custom endpoint is always explicit.
	if !ShouldUseExplicitBedrockEndpoint("https://custom.example.com", "", false) {
		t.Fatal("custom endpoints must be used explicitly")
	}
	// A standard endpoint is only pinned with no configured region/profile.
	if !ShouldUseExplicitBedrockEndpoint("https://bedrock-runtime.us-west-2.amazonaws.com", "", false) {
		t.Fatal("standard endpoints are explicit without a region")
	}
	if ShouldUseExplicitBedrockEndpoint("https://bedrock-runtime.us-west-2.amazonaws.com", "us-east-1", false) {
		t.Fatal("a configured region must win over the catalog endpoint")
	}
	if ShouldUseExplicitBedrockEndpoint("https://bedrock-runtime.us-west-2.amazonaws.com", "", true) {
		t.Fatal("an ambient AWS_PROFILE must win over the catalog endpoint")
	}
}

func TestResolveBedrockRegion(t *testing.T) {
	model := &Model{ID: "anthropic.claude-sonnet-4-5-v1:0"}
	// The ARN-embedded region wins.
	arnModel := &Model{ID: "arn:aws:bedrock:eu-central-1:123:inference-profile/x"}
	if region := ResolveBedrockRegion(arnModel, nil, "us-east-1", "", true, false); region != "eu-central-1" {
		t.Fatalf("region = %s", region)
	}
	if region := ResolveBedrockRegion(model, nil, "eu-west-1", "", true, false); region != "eu-west-1" {
		t.Fatalf("region = %s", region)
	}
	if region := ResolveBedrockRegion(model, nil, "", "us-west-2", true, false); region != "us-west-2" {
		t.Fatalf("region = %s", region)
	}
	if region := ResolveBedrockRegion(model, nil, "", "us-west-2", false, false); region != "us-east-1" {
		t.Fatalf("region = %s", region)
	}
	// With an ambient profile and no explicit region the SDK chain decides.
	if region := ResolveBedrockRegion(model, nil, "", "", true, true); region != "" {
		t.Fatalf("region = %s", region)
	}
	// GovCloud targets.
	options := &BedrockOptions{Region: "us-gov-west-1"}
	if !IsGovCloudBedrockTarget(model, options) {
		t.Fatal("us-gov regions must be detected")
	}
	if !IsGovCloudBedrockTarget(&Model{ID: "us-gov.anthropic.claude"}, nil) {
		t.Fatal("us-gov model prefixes must be detected")
	}
	if !IsGovCloudBedrockTarget(&Model{ID: "arn:aws-us-gov:bedrock:us-gov-west-1:1:x"}, nil) {
		t.Fatal("gov ARNs must be detected")
	}
	if IsGovCloudBedrockTarget(&Model{ID: "anthropic.claude"}, nil) {
		t.Fatal("ordinary models are not GovCloud")
	}
}

func TestBedrockCredentialsResolution(t *testing.T) {
	env := ProviderEnv{
		"AWS_ACCESS_KEY_ID":     "AKID",
		"AWS_SECRET_ACCESS_KEY": "SECRET",
		"AWS_SESSION_TOKEN":     "TOKEN",
	}
	credentials, ok := GetConfiguredBedrockCredentials(env)
	if !ok || credentials.AccessKeyID != "AKID" || credentials.SessionToken != "TOKEN" {
		t.Fatalf("credentials = %+v ok = %v", credentials, ok)
	}
	// Both halves are required.
	if _, ok := GetConfiguredBedrockCredentials(ProviderEnv{"AWS_ACCESS_KEY_ID": "AKID"}); ok {
		t.Fatal("a partial credential set must not resolve")
	}

	// Profile files: credentials and config sections (the latter with the
	// "profile " prefix).
	dir := t.TempDir()
	credentialsPath := filepath.Join(dir, "credentials")
	if err := os.WriteFile(credentialsPath, []byte(
		"[default]\naws_access_key_id = DEFAULT\naws_secret_access_key = DSECRET\n\n"+
			"[work]\naws_access_key_id=WKEY\naws_secret_access_key=WSECRET\naws_session_token=WTOKEN\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(dir, "config")
	if err := os.WriteFile(configPath, []byte("[profile other]\naws_access_key_id = OKEY\naws_secret_access_key = OSECRET\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	profileEnv := ProviderEnv{"AWS_SHARED_CREDENTIALS_FILE": credentialsPath}
	credentials, ok = ResolveAWSProfileCredentials("work", profileEnv)
	if !ok || credentials.AccessKeyID != "WKEY" || credentials.SessionToken != "WTOKEN" {
		t.Fatalf("credentials = %+v ok = %v", credentials, ok)
	}
	if _, ok := ResolveAWSProfileCredentials("missing", profileEnv); ok {
		t.Fatal("unknown profiles must not resolve")
	}
	configEnv := ProviderEnv{"AWS_CONFIG_FILE": configPath}
	if credentials, ok := ResolveAWSProfileCredentials("other", configEnv); !ok || credentials.AccessKeyID != "OKEY" {
		t.Fatalf("credentials = %+v ok = %v", credentials, ok)
	}
	// A profile without both keys does not resolve.
	partialPath := filepath.Join(dir, "partial")
	if err := os.WriteFile(partialPath, []byte("[p]\naws_access_key_id = ONLY\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := ResolveAWSProfileCredentials("p", ProviderEnv{"AWS_SHARED_CREDENTIALS_FILE": partialPath}); ok {
		t.Fatal("partial profiles must not resolve")
	}
}

func TestBedrockBearerTokenResolution(t *testing.T) {
	// An explicit option wins over the environment.
	token, use, skip := ResolveBedrockBearerToken(&BedrockOptions{
		StreamOptions: StreamOptions{APIKey: "option-token", Env: ProviderEnv{"AWS_BEARER_TOKEN_BEDROCK": "env-token"}},
	})
	if token != "option-token" || !use || skip {
		t.Fatalf("token = %q use = %v skip = %v", token, use, skip)
	}
	// The environment fallback.
	token, use, _ = ResolveBedrockBearerToken(&BedrockOptions{
		StreamOptions: StreamOptions{Env: ProviderEnv{"AWS_BEARER_TOKEN_BEDROCK": "env-token"}},
	})
	if token != "env-token" || !use {
		t.Fatalf("token = %q use = %v", token, use)
	}
	// AWS_BEDROCK_SKIP_AUTH=1 disables bearer auth.
	token, use, skip = ResolveBedrockBearerToken(&BedrockOptions{
		StreamOptions: StreamOptions{APIKey: "option-token", Env: ProviderEnv{"AWS_BEDROCK_SKIP_AUTH": "1"}},
	})
	if token != "option-token" || use || !skip {
		t.Fatalf("token = %q use = %v skip = %v", token, use, skip)
	}
	if token, use, _ := ResolveBedrockBearerToken(nil); token != "" || use {
		t.Fatalf("token = %q use = %v", token, use)
	}
}

func TestBedrockErrorFormatting(t *testing.T) {
	// A modeled exception name maps to the legacy prefix.
	err := &ProviderError{Status: 429, Message: "rate exceeded", Body: "rate exceeded", Code: "ThrottlingException"}
	if got := FormatBedrockError(err); got != "Throttling error: rate exceeded" {
		t.Fatalf("formatted = %q", got)
	}
	// An unmapped exception name is used verbatim.
	err = &ProviderError{Status: 400, Message: "bad", Body: "bad", Code: "SomethingNewException"}
	if got := FormatBedrockError(err); got != "SomethingNewException: bad" {
		t.Fatalf("formatted = %q", got)
	}
	// A non-exception code falls back to the plain message.
	err = &ProviderError{Status: 500, Message: "boom", Body: "boom", Code: "TimeoutError"}
	if got := FormatBedrockError(err); got != "boom" {
		t.Fatalf("formatted = %q", got)
	}
	// A status with a body that the message does not carry is surfaced.
	err = &ProviderError{Status: 403, Message: "Forbidden", Body: `{"message":"nope"}`}
	if got := FormatBedrockError(err); !strings.Contains(got, "403:") || !strings.Contains(got, "nope") {
		t.Fatalf("formatted = %q", got)
	}
	// The data-retention hint is appended when the surfaced text mentions it.
	// (The message must carry the body, otherwise the status/body form is used,
	// exactly as upstream does.)
	retention := "data retention mode 'default' is not available"
	err = &ProviderError{Status: 400, Message: retention, Body: retention}
	if got := FormatBedrockError(err); !strings.Contains(got, BedrockDataRetentionDocsURL) {
		t.Fatalf("formatted = %q", got)
	}
	if got := FormatBedrockError(nil); got != "" {
		t.Fatalf("formatted = %q", got)
	}
}

func TestBedrockFailureDiagnostic(t *testing.T) {
	message := &AssistantMessage{}
	err := &ProviderError{Status: 503, Body: "x", Code: "ServiceUnavailableException"}
	AppendBedrockFailureDiagnostic(message, err, "request-1")
	if len(message.Diagnostics) != 1 || message.Diagnostics[0].Type != "bedrock_response_failure" {
		t.Fatalf("diagnostics = %+v", message.Diagnostics)
	}
	details := string(message.Diagnostics[0].Details)
	for _, needle := range []string{`"status":503`, `"errorCode":"ServiceUnavailableException"`, `"requestId":"request-1"`} {
		if !strings.Contains(details, needle) {
			t.Fatalf("details missing %s: %s", needle, details)
		}
	}
	// Over-long request ids are dropped, and empty details produce no
	// diagnostic.
	other := &AssistantMessage{}
	AppendBedrockFailureDiagnostic(other, &ProviderError{Status: 1}, strings.Repeat("x", 201))
	details = string(other.Diagnostics[0].Details)
	if strings.Contains(details, "requestId") {
		t.Fatalf("over-long request id must be dropped: %s", details)
	}
	empty := &AssistantMessage{}
	AppendBedrockFailureDiagnostic(empty, nil, "")
	if len(empty.Diagnostics) != 0 {
		t.Fatalf("diagnostics = %+v", empty.Diagnostics)
	}
}

func TestReservedBedrockHeaders(t *testing.T) {
	for _, key := range []string{"authorization", "Host", "X-Amz-Date", "x-amz-security-token"} {
		if !IsReservedBedrockHeader(key) {
			t.Errorf("%s must be reserved", key)
		}
	}
	for _, key := range []string{"x-custom", "user-agent", "content-type"} {
		if IsReservedBedrockHeader(key) {
			t.Errorf("%s must not be reserved", key)
		}
	}
}

func TestMapBedrockStopReason(t *testing.T) {
	cases := []struct {
		reason string
		want   StopReason
		error  string
	}{
		{"end_turn", StopStop, ""},
		{"stop_sequence", StopStop, ""},
		{"max_tokens", StopLength, ""},
		{"model_context_window_exceeded", StopLength, ""},
		{"tool_use", StopToolUse, ""},
		{"", StopError, ""},
		{"mystery", StopError, "Provider stopped with: mystery"},
	}
	for _, testCase := range cases {
		reason, message := MapBedrockStopReason(testCase.reason)
		if reason != testCase.want || message != testCase.error {
			t.Errorf("MapBedrockStopReason(%q) = %s/%q, want %s/%q",
				testCase.reason, reason, message, testCase.want, testCase.error)
		}
	}
}

// buildAWSEventStreamMessage encodes a test frame.
func buildAWSEventStreamMessage(t *testing.T, headers map[string]any, payload []byte) []byte {
	t.Helper()
	var headerBlock []byte
	for name, value := range headers {
		headerBlock = append(headerBlock, awsVarint(uint64(len(name)))...)
		headerBlock = append(headerBlock, name...)
		switch typed := value.(type) {
		case string:
			headerBlock = append(headerBlock, awsHeaderString)
			headerBlock = append(headerBlock, awsVarint(uint64(len(typed)))...)
			headerBlock = append(headerBlock, typed...)
		case int:
			headerBlock = append(headerBlock, awsHeaderInt)
			encoded := make([]byte, 4)
			binary.BigEndian.PutUint32(encoded, uint32(typed))
			headerBlock = append(headerBlock, encoded...)
		case bool:
			if typed {
				headerBlock = append(headerBlock, awsHeaderBoolTrue)
			} else {
				headerBlock = append(headerBlock, awsHeaderBoolFalse)
			}
		default:
			t.Fatalf("unsupported header value %T", value)
		}
	}
	totalLength := awsEventStreamPreludeLength + len(headerBlock) + len(payload) + 4
	prelude := make([]byte, awsEventStreamPreludeLength)
	binary.BigEndian.PutUint32(prelude[0:4], uint32(totalLength))
	binary.BigEndian.PutUint32(prelude[4:8], uint32(len(headerBlock)))
	binary.BigEndian.PutUint32(prelude[8:12], crc32.ChecksumIEEE(prelude[:8]))
	message := append([]byte{}, prelude...)
	message = append(message, headerBlock...)
	message = append(message, payload...)
	crc := make([]byte, 4)
	binary.BigEndian.PutUint32(crc, crc32.ChecksumIEEE(message))
	return append(message, crc...)
}

// awsVarint encodes the event-stream variable-length integer.
func awsVarint(value uint64) []byte {
	var buffer []byte
	for {
		buffer = append([]byte{byte(value & 0x7f)}, buffer...)
		value >>= 7
		if value == 0 {
			break
		}
	}
	for index := 0; index < len(buffer)-1; index++ {
		buffer[index] &= 0x7f
	}
	buffer[len(buffer)-1] |= 0x80
	return buffer
}

func TestAWSEventStreamDecoding(t *testing.T) {
	first := buildAWSEventStreamMessage(t, map[string]any{
		":message-type": "event",
		":event-type":   "contentBlockDelta",
		":content-type": "application/json",
		"customNumber":  42,
		"customBool":    true,
	}, []byte(`{"delta":{"text":"hi"}}`))
	second := buildAWSEventStreamMessage(t, map[string]any{
		":message-type":   "exception",
		":exception-type": "ThrottlingException",
		":content-type":   "application/json",
	}, []byte(`{"message":"slow down"}`))

	reader := bytes.NewReader(append(first, second...))
	decoded, err := ReadAWSEventStreamMessage(reader)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Headers[":message-type"] != "event" || decoded.Headers[":event-type"] != "contentBlockDelta" ||
		decoded.Headers[":content-type"] != "application/json" {
		t.Fatalf("headers = %#v", decoded.Headers)
	}
	if number, ok := decoded.Headers["customNumber"].(int32); !ok || number != 42 {
		t.Fatalf("customNumber = %#v", decoded.Headers["customNumber"])
	}
	if flag, ok := decoded.Headers["customBool"].(bool); !ok || !flag {
		t.Fatalf("customBool = %#v", decoded.Headers["customBool"])
	}
	if string(decoded.Payload) != `{"delta":{"text":"hi"}}` {
		t.Fatalf("payload = %s", decoded.Payload)
	}

	decoded, err = ReadAWSEventStreamMessage(reader)
	if err != nil || decoded.Headers[":exception-type"] != "ThrottlingException" {
		t.Fatalf("decoded = %+v err = %v", decoded, err)
	}
	// A clean end reports io.EOF.
	if _, err := ReadAWSEventStreamMessage(reader); err != io.EOF {
		t.Fatalf("err = %v", err)
	}

	// A corrupt prelude CRC is detected.
	corrupt := buildAWSEventStreamMessage(t, map[string]any{":message-type": "event"}, []byte("{}"))
	corrupt[9] ^= 0xff
	if _, err := ReadAWSEventStreamMessage(bytes.NewReader(corrupt)); err == nil ||
		!strings.Contains(err.Error(), "prelude checksum mismatch") {
		t.Fatalf("err = %v", err)
	}
	// A corrupt message CRC is detected.
	corrupt = buildAWSEventStreamMessage(t, map[string]any{":message-type": "event"}, []byte("{}"))
	corrupt[len(corrupt)-1] ^= 0xff
	if _, err := ReadAWSEventStreamMessage(bytes.NewReader(corrupt)); err == nil ||
		!strings.Contains(err.Error(), "message checksum mismatch") {
		t.Fatalf("err = %v", err)
	}
	// A short read is an error.
	if _, err := ReadAWSEventStreamMessage(bytes.NewReader([]byte{0, 0, 0})); err == nil {
		t.Fatal("truncated frames must error")
	}
}

var _ = fmt.Sprintf
var _ = time.Now
