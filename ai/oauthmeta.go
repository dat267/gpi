package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Port of auth/oauth/meta.ts.
//
// Meta splits identity from API access: the RFC 8628 device flow yields an
// identity token that inference does not accept, so it is exchanged for a
// Model API key via the Muse Code key-mint endpoint (minted keys live about a
// day). The identity token is stored as `refresh` and the minted key as
// `access`, so the standard OAuth scheduler re-mints the key when it expires
// with no bespoke renewal machinery. The identity token is not renewable
// (auth.meta.com issues no refresh_token), so a 401/403 from mint means the
// session is dead and the user must sign in again.

const (
	// metaOAuthClientID is the Muse Code CLI client id.
	metaOAuthClientID    = "1031625952748946"
	metaOAuthHost        = "https://auth.meta.com"
	metaAPIKeyLifetimeMS = int64(24 * 60 * 60 * 1000)
	metaRequestTimeout   = 30 * time.Second
)

var (
	metaDeviceAuthorizationURL = metaOAuthHost + "/oidc/device/authorization/"
	metaDeviceTokenURL         = metaOAuthHost + "/oidc/device/token/"
	metaAPIKeyMintURL          = "https://api.meta.ai/muse-code/key"
)

// metaDeviceAuthorization is one device authorization.
type metaDeviceAuthorization struct {
	DeviceCode       string
	UserCode         string
	VerificationURI  string
	IntervalSeconds  *int
	ExpiresInSeconds *int
}

// metaTrustedHTTPURL only trusts http(s) URLs, because the verification URI is
// opened in the user's browser (upstream trustedHttpUrl).
func metaTrustedHTTPURL(value any) string {
	raw, ok := value.(string)
	if !ok || raw == "" {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		return ""
	}
	return parsed.String()
}

// metaErrorDetail renders the first present error detail field (upstream
// errorDetail).
func metaErrorDetail(body map[string]any) string {
	for _, key := range []string{"error_description", "detail", "message", "error"} {
		if value, ok := body[key].(string); ok && strings.TrimSpace(value) != "" {
			return ": " + strings.TrimSpace(value)
		}
	}
	return ""
}

func metaRequestContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithTimeout(ctx, metaRequestTimeout)
}

// metaDo runs one request and decodes a JSON object body. The status is
// returned even on an error response so callers can branch on it.
func metaDo(request *http.Request, loginCtx context.Context) (int, map[string]any, error) {
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		if loginCtx != nil && loginCtx.Err() != nil {
			return 0, nil, fmt.Errorf("%s", DeviceCodeCancelMessage)
		}
		return 0, nil, err
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	body := map[string]any{}
	var parsed any
	if err := json.Unmarshal(raw, &parsed); err == nil {
		if record, ok := parsed.(map[string]any); ok {
			body = record
		}
	}
	return response.StatusCode, body, nil
}

func metaPostForm(ctx context.Context, requestURL string, fields map[string]string) (int, map[string]any, error) {
	requestCtx, cancel := metaRequestContext(ctx)
	defer cancel()
	form := url.Values{}
	for key, value := range fields {
		form.Set(key, value)
	}
	request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, requestURL, strings.NewReader(form.Encode()))
	if err != nil {
		return 0, nil, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return metaDo(request, ctx)
}

func metaStartDeviceAuthorization(ctx context.Context) (*metaDeviceAuthorization, error) {
	status, body, err := metaPostForm(ctx, metaDeviceAuthorizationURL, map[string]string{"client_id": metaOAuthClientID})
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("Meta device authorization failed with status %d%s", status, metaErrorDetail(body))
	}
	deviceCode, _ := body["device_code"].(string)
	userCode, _ := body["user_code"].(string)
	verificationURI := metaTrustedHTTPURL(body["verification_uri_complete"])
	if verificationURI == "" {
		verificationURI = metaTrustedHTTPURL(body["verification_uri"])
	}
	if deviceCode == "" || userCode == "" || verificationURI == "" {
		encoded, _ := json.Marshal(body)
		return nil, fmt.Errorf("Invalid Meta device authorization response: %s", encoded)
	}
	device := &metaDeviceAuthorization{DeviceCode: deviceCode, UserCode: userCode, VerificationURI: verificationURI}
	if interval, ok := body["interval"].(float64); ok && interval > 0 {
		seconds := int(interval)
		device.IntervalSeconds = &seconds
	}
	if expires, ok := body["expires_in"].(float64); ok && expires > 0 {
		seconds := int(expires)
		device.ExpiresInSeconds = &seconds
	}
	return device, nil
}

func metaPollForIdentityToken(ctx context.Context, device *metaDeviceAuthorization) (string, error) {
	return PollOAuthDeviceCodeFlow(OAuthDeviceCodePollOptions[string]{
		IntervalSeconds:     device.IntervalSeconds,
		ExpiresInSeconds:    device.ExpiresInSeconds,
		WaitBeforeFirstPoll: true,
		Ctx:                 ctx,
		Poll: func() (OAuthDeviceCodePollResult[string], error) {
			status, body, err := metaPostForm(ctx, metaDeviceTokenURL, map[string]string{
				"grant_type":  "urn:ietf:params:oauth:grant-type:device_code",
				"device_code": device.DeviceCode,
				"client_id":   metaOAuthClientID,
			})
			if err != nil {
				return OAuthDeviceCodePollResult[string]{}, err
			}
			if status >= 200 && status < 300 {
				if token, ok := body["access_token"].(string); ok && token != "" {
					return OAuthDeviceCodePollResult[string]{Status: "complete", Value: token}, nil
				}
			}
			switch errCode, _ := body["error"].(string); errCode {
			case "authorization_pending":
				return OAuthDeviceCodePollResult[string]{Status: "pending"}, nil
			case "slow_down":
				result := OAuthDeviceCodePollResult[string]{Status: "slow_down"}
				if interval, ok := body["interval"].(float64); ok && interval > 0 {
					seconds := int(interval)
					result.IntervalSeconds = &seconds
				}
				return result, nil
			case "access_denied":
				return OAuthDeviceCodePollResult[string]{Status: "failed", Message: "Meta login was denied."}, nil
			case "expired_token":
				return OAuthDeviceCodePollResult[string]{Status: "failed", Message: "Meta device authorization expired. Please restart login."}, nil
			default:
				return OAuthDeviceCodePollResult[string]{
					Status:  "failed",
					Message: fmt.Sprintf("Meta device token request failed with status %d%s", status, metaErrorDetail(body)),
				}, nil
			}
		},
	})
}

// metaMintAPIKey exchanges an identity token for a Model API key. Keys are
// valid for about a day.
func metaMintAPIKey(identityToken string, ctx context.Context) (*OAuthCredential, error) {
	requestCtx, cancel := metaRequestContext(ctx)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, metaAPIKeyMintURL, strings.NewReader("{}"))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "Bearer "+identityToken)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("x-api-version", "1.0.0")
	status, body, err := metaDo(request, ctx)
	if err != nil {
		return nil, err
	}
	if status == 401 || status == 403 {
		// The identity token is not renewable (see the file header); only a
		// fresh device flow helps.
		return nil, fmt.Errorf("Meta session expired (status %d). Run `/login meta` to sign in again.%s", status, metaErrorDetail(body))
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("Meta API key mint failed with status %d%s", status, metaErrorDetail(body))
	}
	apiKey, _ := body["api_key"].(string)
	if apiKey == "" {
		if actionURL := metaTrustedHTTPURL(body["action_url"]); actionURL != "" {
			return nil, fmt.Errorf("Meta did not issue an API key. Complete setup at %s", actionURL)
		}
		return nil, fmt.Errorf("Meta did not issue an API key.")
	}
	return &OAuthCredential{OAuthCredentials: OAuthCredentials{
		Refresh: identityToken,
		Access:  apiKey,
		Expires: time.Now().UnixMilli() + metaAPIKeyLifetimeMS,
	}}, nil
}

func metaIntValue(value *int) int {
	if value == nil {
		return 0
	}
	return *value
}

func loginMeta(interaction *AuthInteraction) (*OAuthCredential, error) {
	device, err := metaStartDeviceAuthorization(interaction.Ctx)
	if err != nil {
		return nil, err
	}
	interaction.Notify(AuthEvent{
		Type:            AuthEventDeviceCode,
		UserCode:        device.UserCode,
		VerificationURI: device.VerificationURI,
		IntervalSeconds: metaIntValue(device.IntervalSeconds),
		ExpiresSeconds:  metaIntValue(device.ExpiresInSeconds),
	})
	identityToken, err := metaPollForIdentityToken(interaction.Ctx, device)
	if err != nil {
		return nil, err
	}
	interaction.Notify(AuthEvent{Type: AuthEventProgress, Message: "Enabling Meta Model API access..."})
	return metaMintAPIKey(identityToken, interaction.Ctx)
}

// MetaOAuth is the Meta Muse subscription OAuth auth (upstream metaOAuth).
func MetaOAuth() *OAuthAuth {
	return &OAuthAuth{
		Name:           "Meta (Muse subscription)",
		IsSubscription: true,
		LoginLabel:     "Sign in with Meta",
		Login: func(interaction *AuthInteraction) (*OAuthCredential, error) {
			if interaction == nil {
				return nil, fmt.Errorf("auth interaction is required")
			}
			credential, err := loginMeta(interaction)
			if err != nil {
				// An in-flight fetch rejects with a DOMException on abort; the
				// login UI matches on this message (upstream loginMeta's catch).
				if interaction.Ctx != nil && interaction.Ctx.Err() != nil {
					return nil, fmt.Errorf("Login cancelled")
				}
				return nil, err
			}
			return credential, nil
		},
		Refresh: func(credential *OAuthCredential, ctx context.Context) (*OAuthCredential, error) {
			if credential == nil {
				return nil, fmt.Errorf("credential is required")
			}
			return metaMintAPIKey(credential.Refresh, ctx)
		},
		ToAuth: func(credential *OAuthCredential) (*ModelAuth, error) {
			if credential == nil {
				return nil, fmt.Errorf("credential is required")
			}
			return &ModelAuth{APIKey: credential.Access}, nil
		},
	}
}
