package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Port of packages/ai/src/auth/types.ts, helpers.ts, credential-store.ts,
// context.ts, and resolve.ts. Upstream's async surface (Promise-based, for
// browser bundling) maps to synchronous Go with context.Context for abort.

// ModelAuth is request auth for a single model request. If a value cannot be
// expressed as APIKey, Headers, or BaseURL, it is provider config, not auth.
type ModelAuth struct {
	APIKey  string          `json:"apiKey,omitempty"`
	Headers ProviderHeaders `json:"headers,omitempty"`
	BaseURL string          `json:"baseUrl,omitempty"`
}

// CredentialType discriminates credentials.
type CredentialType = string

const (
	CredentialAPIKey CredentialType = "api_key"
	CredentialOAuth  CredentialType = "oauth"
)

// ApiKeyCredential is a stored api-key credential. Env holds provider-scoped
// environment/config values such as Cloudflare account/gateway ids.
type ApiKeyCredential struct {
	Key string      `json:"key,omitempty"`
	Env ProviderEnv `json:"env,omitempty"`
}

// OAuthCredentials is OAuth token data returned by extension compatibility flows.
type OAuthCredentials struct {
	Refresh string                     `json:"refresh"`
	Access  string                     `json:"access"`
	Expires int64                      `json:"expires"` // Unix ms
	Extra   map[string]json.RawMessage `json:"-"`
}

// OAuthCredential is a stored canonical OAuth credential.
type OAuthCredential struct {
	OAuthCredentials
}

// Credential is one type-tagged credential per provider — the shape of
// today's auth.json.
type Credential struct {
	Type CredentialType `json:"type"`
	// Exactly one of the following arms is populated per Type.
	APIKey *ApiKeyCredential
	OAuth  *OAuthCredential
}

func (c *Credential) MarshalJSON() ([]byte, error) {
	switch c.Type {
	case CredentialAPIKey:
		return MarshalJSON(struct {
			Type string      `json:"type"`
			Key  string      `json:"key,omitempty"`
			Env  ProviderEnv `json:"env,omitempty"`
		}{c.Type, c.APIKey.Key, c.APIKey.Env})
	case CredentialOAuth:
		base, err := MarshalJSON(c.OAuth.OAuthCredentials)
		if err != nil {
			return nil, err
		}
		var probe map[string]json.RawMessage
		if err := json.Unmarshal(base, &probe); err != nil {
			return nil, err
		}
		probe["type"], err = MarshalJSON(CredentialOAuth)
		if err != nil {
			return nil, err
		}
		// Splice extra keys.
		for k, v := range c.OAuth.Extra {
			if _, exists := probe[k]; !exists {
				probe[k] = v
			}
		}
		// Preserve upstream key order: type first.
		out := []byte{'{'}
		out = append(out, []byte(`"type":"oauth"`)...)
		first := len(probe) == len(c.OAuth.Extra)+1
		// Emit remaining keys in encoded order (sorted by map marshal).
		rest, err := MarshalJSON(probe)
		if err != nil {
			return nil, err
		}
		var ordered map[string]json.RawMessage
		_ = json.Unmarshal(rest, &ordered)
		_ = first
		_ = ordered
		_ = out
		// Simplest faithful encoding: rebuild in field order.
		var buf []byte
		buf = append(buf, '{')
		buf = append(buf, `"type":"oauth","refresh":`...)
		enc, err := MarshalJSON(c.OAuth.Refresh)
		if err != nil {
			return nil, err
		}
		buf = append(buf, enc...)
		buf = append(buf, `,"access":`...)
		enc, err = MarshalJSON(c.OAuth.Access)
		if err != nil {
			return nil, err
		}
		buf = append(buf, enc...)
		buf = append(buf, `,"expires":`...)
		enc, err = MarshalJSON(c.OAuth.Expires)
		if err != nil {
			return nil, err
		}
		buf = append(buf, enc...)
		// Extra keys in sorted order for determinism (upstream preserves
		// insertion order; Go callers should not depend on extra ordering).
		var keys []string
		for k := range c.OAuth.Extra {
			keys = append(keys, k)
		}
		sortStrings(keys)
		for _, k := range keys {
			if k == "type" || k == "refresh" || k == "access" || k == "expires" {
				continue
			}
			enc, err := MarshalJSON(k)
			if err != nil {
				return nil, err
			}
			buf = append(buf, ',')
			buf = append(buf, enc...)
			buf = append(buf, ':')
			buf = append(buf, c.OAuth.Extra[k]...)
		}
		buf = append(buf, '}')
		return buf, nil
	default:
		return nil, fmt.Errorf("ai: unknown credential type %q", c.Type)
	}
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

func (c *Credential) UnmarshalJSON(data []byte) error {
	var probe struct {
		Type CredentialType `json:"type"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return err
	}
	switch probe.Type {
	case CredentialAPIKey:
		var v struct {
			Key string            `json:"key"`
			Env map[string]string `json:"env"`
		}
		if err := json.Unmarshal(data, &v); err != nil {
			return err
		}
		c.Type = CredentialAPIKey
		c.APIKey = &ApiKeyCredential{Key: v.Key, Env: v.Env}
	case CredentialOAuth:
		var v struct {
			Refresh string                     `json:"refresh"`
			Access  string                     `json:"access"`
			Expires int64                      `json:"expires"`
			Extra   map[string]json.RawMessage `json:"-"`
		}
		if err := json.Unmarshal(data, &v); err != nil {
			return err
		}
		// Capture unknown keys for the index signature.
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(data, &raw); err != nil {
			return err
		}
		extra := map[string]json.RawMessage{}
		for k, val := range raw {
			switch k {
			case "type", "refresh", "access", "expires":
			default:
				extra[k] = val
			}
		}
		c.Type = CredentialOAuth
		c.OAuth = &OAuthCredential{OAuthCredentials{Refresh: v.Refresh, Access: v.Access, Expires: v.Expires, Extra: extra}}
	default:
		return fmt.Errorf("ai: unknown credential type %q", probe.Type)
	}
	return nil
}

// CredentialInfo is non-secret credential metadata for status enumeration.
type CredentialInfo struct {
	ProviderID string         `json:"providerId"`
	Type       CredentialType `json:"type"`
}

// CredentialStore is app-owned credential storage, keyed by Provider.ID, one
// credential per provider. Modify is the only write path, so every mutation
// is a serialized read-modify-write; Models.GetAuth runs OAuth refresh inside
// Modify so concurrent requests cannot double-refresh a rotated token.
//
// Error semantics: Read returns (nil, nil) for missing entries. Methods
// return errors only on storage failure; Models wraps them in ModelsError
// with code "auth".
type CredentialStore interface {
	Read(providerID string, ctx context.Context) (*Credential, error)
	// List returns stored credential metadata without resolving or exposing
	// secrets. Implementations must not execute configured API-key commands
	// while listing.
	List(ctx context.Context) ([]CredentialInfo, error)
	// Modify is the serialized write path. Fn sees the current credential
	// because correct writes (refresh, login-during-refresh) depend on it;
	// return the new credential, or nil to leave the entry unchanged.
	// Mutual exclusion per provider id, cross-process too where the backing
	// store supports it. Returns the post-write credential. Errors from fn
	// propagate.
	Modify(providerID string, fn func(current *Credential) (*Credential, error), ctx context.Context) (*Credential, error)
	// Delete removes a credential (logout). Serialized against Modify.
	Delete(providerID string, ctx context.Context) error
}

// AuthContext is environment access for auth resolution. Injectable for tests.
// (Upstream methods are async for browser bundling; Go is synchronous.)
type AuthContext interface {
	// Env resolves an environment variable; ok is false when unset or blank.
	Env(name string) (value string, ok bool)
	// FileExists checks whether a file exists. Supports a leading ~.
	FileExists(path string) bool
}

// DefaultAuthContext is the default auth context: process environment and
// filesystem existence with ~ expansion (port of defaultProviderAuthContext).
type DefaultAuthContext struct{}

func (DefaultAuthContext) Env(name string) (string, bool) {
	value, ok := os.LookupEnv(name)
	if !ok || strings.TrimSpace(value) == "" {
		return "", false
	}
	return value, true
}

func (DefaultAuthContext) FileExists(path string) bool {
	if strings.HasPrefix(path, "~") {
		home, err := os.UserHomeDir()
		if err != nil {
			return false
		}
		path = home + path[1:]
	}
	_, err := os.Stat(filepath.Clean(path))
	return err == nil
}

// AuthResult is the result of resolving auth for a model.
type AuthResult struct {
	Auth ModelAuth
	// Env is provider-scoped environment/config resolved from credentials
	// and ambient context.
	Env ProviderEnv
	// Source is a human-readable label for status UI: "ANTHROPIC_API_KEY",
	// "OAuth", "~/.aws/credentials".
	Source string
}

// AuthCheck reports whether a provider has complete auth configuration.
type AuthCheck struct {
	Source string
	Type   AuthType
}

// AuthType distinguishes credential kinds for login.
type AuthType = string

const (
	AuthTypeAPIKey AuthType = "api_key"
	AuthTypeOAuth  AuthType = "oauth"
)

// AuthPromptKind discriminates login prompts.
type AuthPromptKind = string

const (
	AuthPromptText       AuthPromptKind = "text"
	AuthPromptSecret     AuthPromptKind = "secret"
	AuthPromptSelect     AuthPromptKind = "select"
	AuthPromptManualCode AuthPromptKind = "manual_code"
)

// AuthPrompt is a prompt shown to the user during login.
type AuthPrompt struct {
	Type        AuthPromptKind
	Message     string
	Placeholder string
	// Select options (Type == "select").
	SelectOptions []AuthSelectOption
}

// AuthSelectOption is one option of a select prompt.
type AuthSelectOption struct {
	ID          string
	Label       string
	Description string
}

// AuthInfoLink is a link attached to an info event.
type AuthInfoLink struct {
	URL   string
	Label string
}

// AuthEventKind discriminates login flow events.
type AuthEventKind = string

const (
	AuthEventInfo       AuthEventKind = "info"
	AuthEventAuthURL    AuthEventKind = "auth_url"
	AuthEventDeviceCode AuthEventKind = "device_code"
	AuthEventProgress   AuthEventKind = "progress"
)

// AuthEvent is one login flow notification.
type AuthEvent struct {
	Type AuthEventKind
	// Info events.
	Message string
	Links   []AuthInfoLink
	// Auth URL events.
	URL          string
	Instructions string
	// Device code events.
	UserCode        string
	VerificationURI string
	IntervalSeconds int
	ExpiresSeconds  int
}

// AuthInteraction serves both api-key and OAuth login flows. Prompt returns
// the entered/selected string (select returns the option id) and an error on
// cancel/abort. Ctx aborts the whole login flow.
type AuthInteraction struct {
	Ctx    context.Context
	Prompt func(prompt AuthPrompt) (string, error)
	Notify func(event AuthEvent)
}

// ApiKeyAuth is api-key auth: stored key/provider env plus ambient sources
// (env vars, AWS profiles, ADC files). Ambient-only providers omit Login.
type ApiKeyAuth struct {
	// Name is a display name, e.g. "Anthropic API key".
	Name string
	// Login is interactive setup (prompt for key/provider env). Nil =
	// ambient-only.
	Login func(interaction *AuthInteraction) (*ApiKeyCredential, error)
	// Check is an optional side-effect-free availability check. Use it when
	// Resolve may execute commands or other request-time work. Nil means
	// Models checks availability by resolving auth.
	Check func(input AuthResolveInput) (*AuthCheck, error)
	// Resolve resolves auth from the stored credential and/or ambient
	// sources, merging per field (credential.Key ?? env("..."),
	// credential.Env?.NAME ?? env("...")). Nil = not configured.
	Resolve func(input AuthResolveInput) (*AuthResult, error)
}

// AuthResolveInput is the argument to ApiKeyAuth.Check/Resolve.
type AuthResolveInput struct {
	Ctx        AuthContext
	Credential *ApiKeyCredential
	Ctx2       context.Context // upstream `signal`
}

// OAuthAuth is OAuth auth. The Refresh/ToAuth split lets Models own the
// locked refresh pattern: Refresh produces a credential, ToAuth derives
// request auth from whatever credential ends up stored.
type OAuthAuth struct {
	// Name is a display name, e.g. "Anthropic (Claude Pro/Max)".
	Name string
	// IsSubscription reports whether access through this auth method is
	// backed by a provider subscription.
	IsSubscription bool
	// LoginLabel is the selector label for the OAuth login option.
	LoginLabel string
	// Login runs the interactive flow.
	Login func(interaction *AuthInteraction) (*OAuthCredential, error)
	// Refresh exchanges the refresh token. Network call; returns an error on
	// failure (invalid_grant etc.). Models runs this under the store lock.
	Refresh func(credential *OAuthCredential, ctx context.Context) (*OAuthCredential, error)
	// ToAuth is side-effect-free derivation of request auth from a valid
	// credential. Covers per-credential baseUrl (GitHub Copilot).
	ToAuth func(credential *OAuthCredential) (*ModelAuth, error)
}

// ProviderAuth is provider auth. At least one of APIKey/OAuth must be
// present: even ambient-credential providers and keyless local servers
// provide APIKey auth whose Resolve reports whether the provider is
// configured.
type ProviderAuth struct {
	APIKey *ApiKeyAuth
	OAuth  *OAuthAuth
}

// LazyOAuth wraps a lazily-loaded OAuthAuth so provider definitions can
// advertise OAuth without importing the implementation (port of lazyOAuth).
type LazyOAuth struct {
	Name           string
	IsSubscription bool
	LoginLabel     string
	Load           func() (*OAuthAuth, error)

	once   sync.Once
	loaded *OAuthAuth
	err    error
}

func (l *LazyOAuth) impl() (*OAuthAuth, error) {
	l.once.Do(func() {
		l.loaded, l.err = l.Load()
	})
	return l.loaded, l.err
}

func (l *LazyOAuth) Login(interaction *AuthInteraction) (*OAuthCredential, error) {
	impl, err := l.impl()
	if err != nil {
		return nil, err
	}
	return impl.Login(interaction)
}

func (l *LazyOAuth) Refresh(credential *OAuthCredential, ctx context.Context) (*OAuthCredential, error) {
	impl, err := l.impl()
	if err != nil {
		return nil, err
	}
	return impl.Refresh(credential, ctx)
}

func (l *LazyOAuth) ToAuth(credential *OAuthCredential) (*ModelAuth, error) {
	impl, err := l.impl()
	if err != nil {
		return nil, err
	}
	return impl.ToAuth(credential)
}

// AsOAuthAuth adapts a LazyOAuth to the OAuthAuth interface shape used by
// ProviderAuth.
func (l *LazyOAuth) AsOAuthAuth() *OAuthAuth {
	return &OAuthAuth{
		Name:           l.Name,
		IsSubscription: l.IsSubscription,
		LoginLabel:     l.LoginLabel,
		Login:          l.Login,
		Refresh:        l.Refresh,
		ToAuth:         l.ToAuth,
	}
}

// EnvApiKeyAuth is standard api-key auth: a stored credential key wins,
// otherwise the first set env var resolves. Includes a Login that prompts
// for the key (port of envApiKeyAuth).
func EnvApiKeyAuth(name string, envVars []string) *ApiKeyAuth {
	return &ApiKeyAuth{
		Name: name,
		Login: func(interaction *AuthInteraction) (*ApiKeyCredential, error) {
			if err := ctxErr(interaction.Ctx); err != nil {
				return nil, err
			}
			key, err := interaction.Prompt(AuthPrompt{Type: AuthPromptSecret, Message: "Enter " + name})
			if err != nil {
				return nil, err
			}
			if err := ctxErr(interaction.Ctx); err != nil {
				return nil, err
			}
			return &ApiKeyCredential{Key: key}, nil
		},
		Resolve: func(input AuthResolveInput) (*AuthResult, error) {
			if err := ctxErr(input.Ctx2); err != nil {
				return nil, err
			}
			if input.Credential != nil && input.Credential.Key != "" {
				return &AuthResult{
					Auth:   ModelAuth{APIKey: input.Credential.Key},
					Env:    input.Credential.Env,
					Source: "stored credential",
				}, nil
			}
			for _, envVar := range envVars {
				value, ok := input.Ctx.Env(envVar)
				if err := ctxErr(input.Ctx2); err != nil {
					return nil, err
				}
				// JS truthiness: a blank string is falsy.
				if ok && value != "" {
					return &AuthResult{Auth: ModelAuth{APIKey: value}, Source: envVar}, nil
				}
			}
			return nil, nil
		},
	}
}

func ctxErr(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}
