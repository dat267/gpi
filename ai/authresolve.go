package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Port of auth/resolve.ts: auth resolution shared by the Models collection.

// ModelsErrorCode enumerates ModelsError codes.
type ModelsErrorCode = string

const (
	ErrCodeModelSource     ModelsErrorCode = "model_source"
	ErrCodeModelValidation ModelsErrorCode = "model_validation"
	ErrCodeProvider        ModelsErrorCode = "provider"
	ErrCodeStream          ModelsErrorCode = "stream"
	ErrCodeAuth            ModelsErrorCode = "auth"
	ErrCodeOAuth           ModelsErrorCode = "oauth"
)

// ModelsError is the error type surfaced by the Models collection.
type ModelsError struct {
	Code    ModelsErrorCode
	Message string
	Cause   error
}

func (e *ModelsError) Error() string { return e.Message }
func (e *ModelsError) Unwrap() error { return e.Cause }

// NewModelsError builds a ModelsError, folding the cause's message into the
// error message (upstream withCauseDetail: callers surface error.message
// only, so the underlying reason is kept in it).
func NewModelsError(code ModelsErrorCode, message string, cause error) *ModelsError {
	if cause != nil {
		detail := strings.TrimSpace(cause.Error())
		if detail != "" && !strings.Contains(message, detail) {
			message = message + ": " + detail
		}
	}
	return &ModelsError{Code: code, Message: message, Cause: cause}
}

// AuthResolutionOverrides tunes auth resolution.
type AuthResolutionOverrides struct {
	APIKey string
	Env    ProviderEnv
	// MinOAuthValidityMS requires this much remaining OAuth-token validity;
	// defaults to five minutes.
	MinOAuthValidityMS int64
	Ctx                context.Context // upstream signal
}

const (
	oauthMinimumValidityMS = 5 * 60 * 1000
	oauthRefreshTimeoutMS  = 15 * 1000
)

// providerAuthSource narrows what resolveProviderAuth needs from a Provider.
type providerAuthSource interface {
	ProviderID() string
	ProviderAuth() ProviderAuth
}

// ResolveProviderAuth resolves auth for a provider. A stored credential owns
// the provider: ambient/env is consulted only when nothing is stored. No
// silent env fallback after a failed refresh or for a credential type
// without a matching handler.
func ResolveProviderAuth(providerID string, auth ProviderAuth, credentials CredentialStore, authContext AuthContext, overrides *AuthResolutionOverrides) (*AuthResult, error) {
	ctx := context.Background()
	if overrides != nil && overrides.Ctx != nil {
		ctx = overrides.Ctx
	}
	if err := ctxErr(ctx); err != nil {
		return nil, err
	}
	requestAuthContext := authContext
	if overrides != nil && overrides.Env != nil {
		requestAuthContext = overlayEnvAuthContext(authContext, overrides.Env)
	}

	if overrides != nil && overrides.APIKey != "" && auth.APIKey != nil {
		return resolveAPIKey(requestAuthContext, auth.APIKey, providerID, &ApiKeyCredential{
			Key: overrides.APIKey,
			Env: overrides.Env,
		}, ctx)
	}

	stored, err := readCredential(credentials, providerID, ctx)
	if err != nil {
		return nil, err
	}
	if stored != nil {
		if stored.Type == CredentialOAuth && auth.OAuth != nil {
			return resolveStoredOAuth(credentials, providerID, auth.OAuth, stored.OAuth, ctx, overrides.minValidity())
		}
		if stored.Type == CredentialAPIKey && auth.APIKey != nil {
			credential := stored.APIKey
			if overrides != nil && overrides.Env != nil {
				merged := ProviderEnv{}
				for k, v := range credential.Env {
					merged[k] = v
				}
				for k, v := range overrides.Env {
					merged[k] = v
				}
				credential = &ApiKeyCredential{Key: credential.Key, Env: merged}
			}
			return resolveAPIKey(requestAuthContext, auth.APIKey, providerID, credential, ctx)
		}
		return nil, nil
	}

	// Ambient (env vars, AWS profiles, ADC files).
	if auth.APIKey != nil {
		return resolveAPIKey(requestAuthContext, auth.APIKey, providerID, nil, ctx)
	}
	return nil, nil
}

func (o *AuthResolutionOverrides) minValidity() int64 {
	if o == nil || o.MinOAuthValidityMS == 0 {
		return 0
	}
	return o.MinOAuthValidityMS
}

func overlayEnvAuthContext(base AuthContext, env ProviderEnv) AuthContext {
	return &overlayEnvContext{base: base, env: env}
}

type overlayEnvContext struct {
	base AuthContext
	env  ProviderEnv
}

func (o *overlayEnvContext) Env(name string) (string, bool) {
	if v, ok := o.env[name]; ok && v != "" {
		return v, true
	}
	return o.base.Env(name)
}

func (o *overlayEnvContext) FileExists(path string) bool { return o.base.FileExists(path) }

// resolveStoredOAuth implements OAuth resolution with double-checked
// locking: tokens with less than five minutes remaining lock, re-check
// expiry under the lock, refresh once globally, and persist the rotated
// credential before release.
func resolveStoredOAuth(credentials CredentialStore, providerID string, oauth *OAuthAuth, stored *OAuthCredential, ctx context.Context, minOAuthValidityMS int64) (*AuthResult, error) {
	minimumValidityMS := int64(oauthMinimumValidityMS)
	if minOAuthValidityMS > minimumValidityMS {
		minimumValidityMS = minOAuthValidityMS
	}
	expiresSoon := func(credential *OAuthCredential) bool {
		return time.Now().UnixMilli()+minimumValidityMS >= credential.Expires
	}
	credential := stored

	if expiresSoon(credential) {
		// Optimistic check said expired; the authoritative check runs under the lock.
		post, err := credentials.Modify(providerID, func(current *Credential) (*Credential, error) {
			if current == nil || current.Type != CredentialOAuth {
				return nil, nil // logged out meanwhile
			}
			if !expiresSoon(current.OAuth) {
				return nil, nil // another process/request refreshed
			}
			refreshCtx, cancel := context.WithTimeout(ctx, oauthRefreshTimeoutMS*time.Millisecond)
			defer cancel()
			refreshed, err := oauth.Refresh(current.OAuth, refreshCtx)
			if err != nil {
				return nil, NewModelsError(ErrCodeOAuth, fmt.Sprintf("OAuth refresh failed for %s", providerID), err)
			}
			return &Credential{Type: CredentialOAuth, OAuth: refreshed}, nil
		}, ctx)
		if err != nil {
			var me *ModelsError
			if asModelsError(err, &me) {
				return nil, err
			}
			return nil, NewModelsError(ErrCodeAuth, fmt.Sprintf("Credential store modify failed for %s", providerID), err)
		}
		if post == nil || post.Type != CredentialOAuth {
			return nil, nil // logged out meanwhile
		}
		credential = post.OAuth
		// The normal five-minute window triggers a refresh but does not impose
		// a provider contract. Explicit callers (such as bearer-token export)
		// do require the requested minimum after the refresh.
		if minOAuthValidityMS != 0 && expiresSoon(credential) {
			return nil, NewModelsError(ErrCodeOAuth, fmt.Sprintf("OAuth refresh returned a token that expires too soon for %s", providerID), nil)
		}
	}

	auth, err := oauth.ToAuth(credential)
	if err != nil {
		return nil, NewModelsError(ErrCodeOAuth, fmt.Sprintf("OAuth auth derivation failed for %s", providerID), err)
	}
	return &AuthResult{Auth: *auth, Source: "OAuth"}, nil
}

func asModelsError(err error, target **ModelsError) bool {
	if me, ok := err.(*ModelsError); ok {
		*target = me
		return true
	}
	return false
}

func resolveAPIKey(authContext AuthContext, apiKey *ApiKeyAuth, providerID string, credential *ApiKeyCredential, ctx context.Context) (*AuthResult, error) {
	result, err := apiKey.Resolve(AuthResolveInput{Ctx: authContext, Credential: credential, Ctx2: ctx})
	if err != nil {
		return nil, NewModelsError(ErrCodeAuth, fmt.Sprintf("API key auth failed for provider %s", providerID), err)
	}
	return result, nil
}

func readCredential(credentials CredentialStore, providerID string, ctx context.Context) (*Credential, error) {
	credential, err := credentials.Read(providerID, ctx)
	if err != nil {
		return nil, NewModelsError(ErrCodeAuth, fmt.Sprintf("Credential store read failed for %s", providerID), err)
	}
	return credential, nil
}

// jsonUnmarshalStrict decodes JSON without HTML-escape concerns (decode path).
func jsonUnmarshalStrict(data []byte, v any) error {
	return json.Unmarshal(data, v)
}
