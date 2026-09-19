package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// Port of auth/oauth/xai.ts and auth/oauth/kimi-coding.ts (both RFC 8628
// device-authorization flows over the shared poller).

// xAI OAuth constants.
const (
	XAIClientID = "b1a00492-073a-47ea-816f-4c329264a828"
	XAIScope    = "openid profile email offline_access grok-cli:access api:access"
)

// The xAI endpoints are variables so tests can point them at a local server
// (upstream keeps constants and mocks fetch).
var (
	xaiDeviceCodeURL = "https://auth.x.ai/oauth2/device/code"
	xaiTokenURLValue = "https://auth.x.ai/oauth2/token"
)

// XAIDeviceCodeURL returns the device authorization endpoint.
func XAIDeviceCodeURL() string { return xaiDeviceCodeURL }

// XAITokenURL returns the token endpoint.
func XAITokenURL() string { return xaiTokenURLValue }

const (
	// xaiRefreshSkewMS refreshes slightly before the reported expiry.
	xaiRefreshSkewMS            = 5 * 60 * 1000
	xaiDefaultTokenLifetimeSecs = 3600
)

// Kimi Code OAuth constants.
const (
	KimiCodingClientID            = "17e5f671-d194-4dfb-9706-5516cb48c098"
	KimiCodingDefaultOAuthHost    = "https://auth.kimi.com"
	KimiCodingDeviceTimeoutSecs   = 15 * 60
	KimiCodingDefaultPollInterval = 5
	KimiCodingRequestTimeout      = 30 * time.Second
	KimiCodingRefreshMaxRetries   = 3
)

// oauthHTTPResponse is one JSON form-POST outcome.
type oauthHTTPResponse struct {
	OK     bool
	Status int
	Body   map[string]any
	// Text is the raw response body (used in failure messages).
	Text string
}

// oauthPostForm posts urlencoded fields and decodes a JSON object body.
// oauthPostForm posts urlencoded fields and decodes a JSON object body. When
// failOnInvalidJSON is false a non-JSON body is kept in Text for callers that
// check the status first (Kimi reports the raw text on failures).
func oauthPostForm(ctx context.Context, requestURL string, fields map[string]string, failOnInvalidJSON bool, invalidJSONMessage func(int) string) (oauthHTTPResponse, error) {
	form := url.Values{}
	for key, value := range fields {
		form.Set(key, value)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, requestURL, strings.NewReader(form.Encode()))
	if err != nil {
		return oauthHTTPResponse{}, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		if ctx != nil && ctx.Err() != nil {
			return oauthHTTPResponse{}, fmt.Errorf("Login cancelled")
		}
		return oauthHTTPResponse{}, err
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	ok := response.StatusCode < 400
	body := map[string]any{}
	var parsed any
	if err := json.Unmarshal(raw, &parsed); err == nil {
		if record, ok := parsed.(map[string]any); ok {
			body = record
		}
	} else if failOnInvalidJSON || ok {
		if ctx != nil && ctx.Err() != nil {
			return oauthHTTPResponse{}, fmt.Errorf("Login cancelled")
		}
		return oauthHTTPResponse{}, fmt.Errorf("%s", invalidJSONMessage(response.StatusCode))
	}
	return oauthHTTPResponse{OK: ok, Status: response.StatusCode, Body: body, Text: string(raw)}, nil
}

// ─── xAI ─────────────────────────────────────────────────────────────────────

// XaiDeviceCode is one device authorization.
type XaiDeviceCode struct {
	DeviceCode              string
	UserCode                string
	VerificationURI         string
	VerificationURIComplete string
	IntervalSeconds         *int
	ExpiresInSeconds        int
}

func oauthRequiredString(body map[string]any, field, prefix string) (string, error) {
	value, ok := body[field].(string)
	if !ok || value == "" {
		return "", fmt.Errorf("Invalid %s OAuth response field: %s", prefix, field)
	}
	return value, nil
}

func oauthPositiveNumber(body map[string]any, field, prefix string) (float64, error) {
	value, ok := body[field].(float64)
	if !ok || value <= 0 {
		return 0, fmt.Errorf("Invalid %s OAuth response field: %s", prefix, field)
	}
	return value, nil
}

// ValidateXaiVerificationURI only trusts https verification URIs, because the
// URI is opened in the user's browser.
func ValidateXaiVerificationURI(raw string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" {
		return "", fmt.Errorf("Untrusted verification URI in xAI OAuth response")
	}
	return parsed.String(), nil
}

func xaiRequestFailure(action string, response oauthHTTPResponse) error {
	errText, _ := response.Body["error"].(string)
	description, _ := response.Body["error_description"].(string)
	detail := ""
	if errText != "" && description != "" {
		detail = errText + ": " + description
	} else if errText != "" {
		detail = errText
	} else if description != "" {
		detail = description
	}
	message := fmt.Sprintf("xAI OAuth %s failed (HTTP %d)", action, response.Status)
	if detail != "" {
		message += ": " + detail
	}
	return fmt.Errorf("%s", message)
}

func parseXaiDeviceCode(body map[string]any) (*XaiDeviceCode, error) {
	device := &XaiDeviceCode{}
	var err error
	if device.DeviceCode, err = oauthRequiredString(body, "device_code", "xAI"); err != nil {
		return nil, err
	}
	if device.UserCode, err = oauthRequiredString(body, "user_code", "xAI"); err != nil {
		return nil, err
	}
	verificationURI, err := oauthRequiredString(body, "verification_uri", "xAI")
	if err != nil {
		return nil, err
	}
	if device.VerificationURI, err = ValidateXaiVerificationURI(verificationURI); err != nil {
		return nil, err
	}
	// RFC 8628 allows interval 0 (no minimum wait), so non-positive or malformed
	// values fall back to the poller's default.
	if interval, ok := body["interval"].(float64); ok && interval > 0 {
		seconds := int(interval)
		device.IntervalSeconds = &seconds
	}
	if complete, ok := body["verification_uri_complete"].(string); ok && complete != "" {
		if device.VerificationURIComplete, err = ValidateXaiVerificationURI(complete); err != nil {
			return nil, err
		}
	}
	expires, err := oauthPositiveNumber(body, "expires_in", "xAI")
	if err != nil {
		return nil, err
	}
	device.ExpiresInSeconds = int(expires)
	return device, nil
}

func xaiCredentialsFromTokenResponse(body map[string]any, previousRefreshToken string) (*OAuthCredential, error) {
	access, err := oauthRequiredString(body, "access_token", "xAI")
	if err != nil {
		return nil, err
	}
	// xAI may omit refresh_token on refresh when the token is not rotated.
	refresh := previousRefreshToken
	if _, present := body["refresh_token"]; present || refresh == "" {
		if refresh, err = oauthRequiredString(body, "refresh_token", "xAI"); err != nil {
			return nil, err
		}
	}
	expiresIn := float64(xaiDefaultTokenLifetimeSecs)
	if _, present := body["expires_in"]; present {
		if expiresIn, err = oauthPositiveNumber(body, "expires_in", "xAI"); err != nil {
			return nil, err
		}
	}
	return &OAuthCredential{OAuthCredentials: OAuthCredentials{
		Access:  access,
		Refresh: refresh,
		Expires: time.Now().UnixMilli() + int64(expiresIn)*1000 - xaiRefreshSkewMS,
	}}, nil
}

// RequestXaiDeviceCode starts the xAI device authorization.
func RequestXaiDeviceCode(ctx context.Context) (*XaiDeviceCode, error) {
	response, err := oauthPostForm(ctx, xaiDeviceCodeURL, map[string]string{
		"client_id": XAIClientID,
		"scope":     XAIScope,
		"referrer":  "pi",
	}, true, func(status int) string {
		return fmt.Sprintf("xAI OAuth returned invalid JSON (HTTP %d)", status)
	})
	if err != nil {
		return nil, err
	}
	if !response.OK {
		return nil, xaiRequestFailure("device authorization", response)
	}
	return parseXaiDeviceCode(response.Body)
}

// PollXaiForTokens polls the xAI token endpoint until it completes.
func PollXaiForTokens(ctx context.Context, device *XaiDeviceCode) (*OAuthCredential, error) {
	intervalSeconds := device.IntervalSeconds
	expiresIn := device.ExpiresInSeconds
	return PollOAuthDeviceCodeFlow(OAuthDeviceCodePollOptions[*OAuthCredential]{
		IntervalSeconds:     intervalSeconds,
		ExpiresInSeconds:    &expiresIn,
		WaitBeforeFirstPoll: true,
		Ctx:                 ctx,
		Poll: func() (OAuthDeviceCodePollResult[*OAuthCredential], error) {
			response, err := oauthPostForm(ctx, xaiTokenURLValue, map[string]string{
				"grant_type":  "urn:ietf:params:oauth:grant-type:device_code",
				"client_id":   XAIClientID,
				"device_code": device.DeviceCode,
			}, true, func(status int) string {
				return fmt.Sprintf("xAI OAuth returned invalid JSON (HTTP %d)", status)
			})
			if err != nil {
				return OAuthDeviceCodePollResult[*OAuthCredential]{Status: "failed", Message: err.Error()}, nil
			}
			if response.OK {
				credential, cerr := xaiCredentialsFromTokenResponse(response.Body, "")
				if cerr != nil {
					return OAuthDeviceCodePollResult[*OAuthCredential]{Status: "failed", Message: cerr.Error()}, nil
				}
				return OAuthDeviceCodePollResult[*OAuthCredential]{Status: "complete", Value: credential}, nil
			}
			errorCode, _ := response.Body["error"].(string)
			switch errorCode {
			case "authorization_pending":
				return OAuthDeviceCodePollResult[*OAuthCredential]{Status: "pending"}, nil
			case "slow_down":
				result := OAuthDeviceCodePollResult[*OAuthCredential]{Status: "slow_down"}
				if interval, ok := response.Body["interval"].(float64); ok {
					seconds := int(interval)
					result.IntervalSeconds = &seconds
				}
				return result, nil
			case "access_denied", "authorization_denied":
				return OAuthDeviceCodePollResult[*OAuthCredential]{
					Status: "failed", Message: "xAI device authorization was denied",
				}, nil
			case "expired_token":
				return OAuthDeviceCodePollResult[*OAuthCredential]{
					Status: "failed", Message: "xAI device code expired",
				}, nil
			default:
				return OAuthDeviceCodePollResult[*OAuthCredential]{
					Status: "failed", Message: xaiRequestFailure("device token polling", response).Error(),
				}, nil
			}
		},
	})
}

// RefreshXaiToken refreshes an xAI credential.
func RefreshXaiToken(ctx context.Context, refreshToken string) (*OAuthCredential, error) {
	response, err := oauthPostForm(ctx, xaiTokenURLValue, map[string]string{
		"grant_type":    "refresh_token",
		"client_id":     XAIClientID,
		"refresh_token": refreshToken,
	}, true, func(status int) string {
		return fmt.Sprintf("xAI OAuth returned invalid JSON (HTTP %d)", status)
	})
	if err != nil {
		return nil, err
	}
	if !response.OK {
		return nil, xaiRequestFailure("token refresh", response)
	}
	return xaiCredentialsFromTokenResponse(response.Body, refreshToken)
}

// LoginXai runs the xAI device-code login (upstream loginXai).
func LoginXai(interaction *AuthInteraction) (*OAuthCredential, error) {
	if interaction == nil {
		return nil, fmt.Errorf("auth interaction is required")
	}
	ctx := interaction.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	device, err := RequestXaiDeviceCode(ctx)
	if err != nil {
		return nil, err
	}
	verificationURI := device.VerificationURI
	if device.VerificationURIComplete != "" {
		verificationURI = device.VerificationURIComplete
	}
	if interaction.Notify != nil {
		event := AuthEvent{
			Type: AuthEventDeviceCode, UserCode: device.UserCode, VerificationURI: verificationURI,
			ExpiresSeconds: device.ExpiresInSeconds,
		}
		if device.IntervalSeconds != nil {
			event.IntervalSeconds = *device.IntervalSeconds
		}
		interaction.Notify(event)
	}
	return PollXaiForTokens(ctx, device)
}

// XaiOAuth is the xAI OAuth auth definition (upstream xaiOAuth).
func XaiOAuth() *OAuthAuth {
	return &OAuthAuth{
		Name:           "xAI (Grok/X subscription)",
		IsSubscription: true,
		LoginLabel:     "Sign in with SuperGrok or X Premium",
		Login:          LoginXai,
		Refresh: func(credential *OAuthCredential, ctx context.Context) (*OAuthCredential, error) {
			if credential == nil {
				return nil, fmt.Errorf("credential is required")
			}
			return RefreshXaiToken(ctx, credential.Refresh)
		},
		ToAuth: func(credential *OAuthCredential) (*ModelAuth, error) {
			if credential == nil {
				return nil, fmt.Errorf("credential is required")
			}
			return &ModelAuth{APIKey: credential.Access}, nil
		},
	}
}

// ─── Kimi Code ───────────────────────────────────────────────────────────────

// KimiCodingOAuthHost resolves the OAuth host (KIMI_CODE_OAUTH_HOST or
// KIMI_OAUTH_HOST override).
func KimiCodingOAuthHost() string {
	host := os.Getenv("KIMI_CODE_OAUTH_HOST")
	if host == "" {
		host = os.Getenv("KIMI_OAUTH_HOST")
	}
	if host == "" {
		host = KimiCodingDefaultOAuthHost
	}
	return strings.TrimRight(host, "/")
}

// KimiDeviceAuthorization is one Kimi device authorization.
type KimiDeviceAuthorization struct {
	DeviceCode              string
	UserCode                string
	VerificationURI         string
	VerificationURIComplete string
	IntervalSeconds         int
	ExpiresInSeconds        int
}

// trustedHTTPURL accepts only http(s) verification URIs (they are opened in the
// user's browser).
func trustedHTTPURL(value any) (string, bool) {
	text, ok := value.(string)
	if !ok || text == "" {
		return "", false
	}
	parsed, err := url.Parse(text)
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		return "", false
	}
	return parsed.String(), true
}

// StartKimiDeviceAuthorization begins the Kimi Code device authorization.
func StartKimiDeviceAuthorization(ctx context.Context, oauthHost string) (*KimiDeviceAuthorization, error) {
	requestCtx, cancel := context.WithTimeout(ctx, KimiCodingRequestTimeout)
	defer cancel()
	response, err := oauthPostForm(requestCtx, oauthHost+"/api/oauth/device_authorization",
		map[string]string{"client_id": KimiCodingClientID}, false, func(status int) string {
			return fmt.Sprintf("Kimi Code device authorization returned invalid JSON (status %d)", status)
		})
	if err != nil {
		return nil, err
	}
	if !response.OK {
		message := fmt.Sprintf("Kimi Code device authorization failed with status %d", response.Status)
		if text := strings.TrimSpace(response.Text); text != "" {
			message += ": " + text
		}
		return nil, fmt.Errorf("%s", message)
	}
	body := response.Body
	deviceCode, _ := body["device_code"].(string)
	userCode, _ := body["user_code"].(string)
	verificationURI, _ := body["verification_uri"].(string)
	completeRaw, _ := body["verification_uri_complete"].(string)
	complete, completeOK := trustedHTTPURL(completeRaw)
	trustedVerification, verificationOK := trustedHTTPURL(verificationURI)
	if deviceCode == "" || userCode == "" || verificationURI == "" || completeRaw == "" ||
		!completeOK || !verificationOK {
		encoded, _ := json.Marshal(body)
		return nil, fmt.Errorf("Invalid Kimi Code device authorization response: %s", encoded)
	}
	intervalSeconds := KimiCodingDefaultPollInterval
	if interval, ok := body["interval"].(float64); ok && interval > 0 {
		intervalSeconds = int(interval)
	}
	expiresInSeconds := KimiCodingDeviceTimeoutSecs
	if expiresIn, ok := body["expires_in"].(float64); ok && expiresIn > 0 {
		expiresInSeconds = int(expiresIn)
	}
	return &KimiDeviceAuthorization{
		DeviceCode: deviceCode, UserCode: userCode,
		VerificationURI: trustedVerification, VerificationURIComplete: complete,
		IntervalSeconds: intervalSeconds, ExpiresInSeconds: expiresInSeconds,
	}, nil
}

type kimiTokenResponse struct {
	Access  string
	Refresh string
	Expires int64
}

func parseKimiTokenResponse(body map[string]any, operation string) (*kimiTokenResponse, error) {
	access, _ := body["access_token"].(string)
	refresh, _ := body["refresh_token"].(string)
	expiresIn, hasExpires := body["expires_in"].(float64)
	if access == "" || refresh == "" || !hasExpires || expiresIn <= 0 {
		encoded, _ := json.Marshal(body)
		return nil, fmt.Errorf("Kimi Code token %s response missing fields: %s", operation, encoded)
	}
	return &kimiTokenResponse{
		Access: access, Refresh: refresh,
		Expires: time.Now().UnixMilli() + int64(expiresIn)*1000,
	}, nil
}

// PollKimiForToken polls the Kimi token endpoint until it completes.
func PollKimiForToken(ctx context.Context, oauthHost string, device *KimiDeviceAuthorization) (*kimiTokenResponse, error) {
	intervalSeconds := device.IntervalSeconds
	expiresInSeconds := device.ExpiresInSeconds
	return PollOAuthDeviceCodeFlow(OAuthDeviceCodePollOptions[*kimiTokenResponse]{
		IntervalSeconds:     &intervalSeconds,
		ExpiresInSeconds:    &expiresInSeconds,
		WaitBeforeFirstPoll: true,
		Ctx:                 ctx,
		Poll: func() (OAuthDeviceCodePollResult[*kimiTokenResponse], error) {
			requestCtx, cancel := context.WithTimeout(ctx, KimiCodingRequestTimeout)
			defer cancel()
			response, err := oauthPostForm(requestCtx, oauthHost+"/api/oauth/token", map[string]string{
				"client_id":   KimiCodingClientID,
				"device_code": device.DeviceCode,
				"grant_type":  "urn:ietf:params:oauth:grant-type:device_code",
			}, false, func(status int) string {
				return fmt.Sprintf("Kimi Code device token request returned invalid JSON (status %d)", status)
			})
			if err != nil {
				return OAuthDeviceCodePollResult[*kimiTokenResponse]{Status: "failed", Message: err.Error()}, nil
			}
			if response.Status >= 500 {
				message := fmt.Sprintf("Kimi Code device token request failed with status %d", response.Status)
				if text := strings.TrimSpace(response.Text); text != "" {
					message += ": " + text
				}
				return OAuthDeviceCodePollResult[*kimiTokenResponse]{Status: "failed", Message: message}, nil
			}
			if response.OK {
				if _, hasAccess := response.Body["access_token"].(string); hasAccess {
					token, terr := parseKimiTokenResponse(response.Body, "poll")
					if terr != nil {
						return OAuthDeviceCodePollResult[*kimiTokenResponse]{Status: "failed", Message: terr.Error()}, nil
					}
					return OAuthDeviceCodePollResult[*kimiTokenResponse]{Status: "complete", Value: token}, nil
				}
			}
			errorCode, _ := response.Body["error"].(string)
			description := ""
			if text, ok := response.Body["error_description"].(string); ok {
				description = ": " + text
			}
			switch errorCode {
			case "authorization_pending":
				return OAuthDeviceCodePollResult[*kimiTokenResponse]{Status: "pending"}, nil
			case "slow_down":
				result := OAuthDeviceCodePollResult[*kimiTokenResponse]{Status: "slow_down"}
				if interval, ok := response.Body["interval"].(float64); ok && interval > 0 {
					seconds := int(interval)
					result.IntervalSeconds = &seconds
				}
				return result, nil
			case "expired_token":
				return OAuthDeviceCodePollResult[*kimiTokenResponse]{
					Status: "failed", Message: "Kimi Code device authorization expired. Please restart login.",
				}, nil
			case "access_denied":
				return OAuthDeviceCodePollResult[*kimiTokenResponse]{Status: "failed", Message: "Kimi Code login was denied."}, nil
			default:
				message := fmt.Sprintf("Kimi Code device token request failed (status %d)", response.Status)
				if errorCode != "" {
					message += ": " + errorCode + description
				}
				return OAuthDeviceCodePollResult[*kimiTokenResponse]{Status: "failed", Message: message}, nil
			}
		},
	})
}

// RefreshKimiToken refreshes a Kimi credential with exponential backoff.
func RefreshKimiToken(ctx context.Context, oauthHost, refreshToken string) (*kimiTokenResponse, error) {
	var lastErr error
	for attempt := 0; attempt <= KimiCodingRefreshMaxRetries; attempt++ {
		if attempt > 0 {
			backoff := time.Duration(1000*(1<<(attempt-1))) * time.Millisecond
			if err := AbortableSleep(ctx, backoff, "Kimi Code token refresh aborted"); err != nil {
				return nil, err
			}
		}
		if ctx != nil && ctx.Err() != nil {
			return nil, fmt.Errorf("Kimi Code token refresh aborted")
		}
		requestCtx, cancel := context.WithTimeout(ctx, KimiCodingRequestTimeout)
		response, err := oauthPostForm(requestCtx, oauthHost+"/api/oauth/token", map[string]string{
			"client_id":     KimiCodingClientID,
			"grant_type":    "refresh_token",
			"refresh_token": refreshToken,
		}, false, func(status int) string {
			return fmt.Sprintf("Kimi Code token refresh returned invalid JSON (status %d)", status)
		})
		cancel()
		if err != nil {
			lastErr = err
			continue
		}
		if response.OK {
			return parseKimiTokenResponse(response.Body, "refresh")
		}
		errorCode, _ := response.Body["error"].(string)
		description := ""
		if text, ok := response.Body["error_description"].(string); ok {
			description = ": " + text
		}
		// Unauthorized: the stored credential is dead, so the caller clears it
		// and prompts for a new login.
		if response.Status == 401 || response.Status == 403 || errorCode == "invalid_grant" {
			return nil, fmt.Errorf("Kimi Code token refresh unauthorized (status %d)%s", response.Status, description)
		}
		if (response.Status == 429 || response.Status >= 500) && attempt < KimiCodingRefreshMaxRetries {
			lastErr = fmt.Errorf("Kimi Code token refresh failed with status %d", response.Status)
			continue
		}
		encoded, _ := json.Marshal(response.Body)
		return nil, fmt.Errorf("Kimi Code token refresh failed with status %d: %s", response.Status, encoded)
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("Kimi Code token refresh failed")
}

// LoginKimiCoding runs the Kimi Code device-code login.
func LoginKimiCoding(interaction *AuthInteraction) (*OAuthCredential, error) {
	if interaction == nil {
		return nil, fmt.Errorf("auth interaction is required")
	}
	ctx := interaction.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	oauthHost := KimiCodingOAuthHost()
	device, err := StartKimiDeviceAuthorization(ctx, oauthHost)
	if err != nil {
		return nil, err
	}
	if interaction.Notify != nil {
		interaction.Notify(AuthEvent{
			Type: AuthEventDeviceCode, UserCode: device.UserCode,
			VerificationURI: device.VerificationURIComplete,
			IntervalSeconds: device.IntervalSeconds, ExpiresSeconds: device.ExpiresInSeconds,
		})
	}
	token, err := PollKimiForToken(ctx, oauthHost, device)
	if err != nil {
		return nil, err
	}
	return &OAuthCredential{OAuthCredentials: OAuthCredentials{
		Access: token.Access, Refresh: token.Refresh, Expires: token.Expires,
	}}, nil
}

// KimiCodingOAuth is the Kimi Code OAuth auth definition (upstream
// kimiCodingOAuth).
func KimiCodingOAuth() *OAuthAuth {
	return &OAuthAuth{
		Name:           "Kimi Code (subscription)",
		IsSubscription: true,
		LoginLabel:     "Sign in with Kimi Code",
		Login:          LoginKimiCoding,
		Refresh: func(credential *OAuthCredential, ctx context.Context) (*OAuthCredential, error) {
			if credential == nil {
				return nil, fmt.Errorf("credential is required")
			}
			token, err := RefreshKimiToken(ctx, KimiCodingOAuthHost(), credential.Refresh)
			if err != nil {
				return nil, err
			}
			return &OAuthCredential{OAuthCredentials: OAuthCredentials{
				Access: token.Access, Refresh: token.Refresh, Expires: token.Expires,
			}}, nil
		},
		ToAuth: func(credential *OAuthCredential) (*ModelAuth, error) {
			if credential == nil {
				return nil, fmt.Errorf("credential is required")
			}
			bearer := "Bearer " + credential.Access
			return &ModelAuth{Headers: ProviderHeaders{"Authorization": &bearer}}, nil
		},
	}
}
