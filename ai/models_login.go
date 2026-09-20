package ai

import (
	"context"
	"fmt"
)

// Port of the login/logout half of packages/ai/src/models.ts. Login runs a
// provider-owned flow and persists its credential; logout deletes it. Both wrap
// credential-store failures in an auth ModelsError.

// Login runs a provider-owned login flow and persists the returned credential.
func (m *Models) Login(providerID string, authType AuthType, interaction *AuthInteraction) (*Credential, error) {
	if interaction == nil {
		interaction = &AuthInteraction{}
	}
	ctx := interaction.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	m.mu.Lock()
	provider := m.providers[providerID]
	m.mu.Unlock()
	if provider == nil {
		return nil, NewModelsError(ErrCodeProvider, fmt.Sprintf("Unknown provider: %s", providerID), nil)
	}

	var login func(*AuthInteraction) (*Credential, error)
	var methodName string
	if authType == AuthTypeOAuth {
		methodName = "oauth"
		if provider.Auth.OAuth != nil && provider.Auth.OAuth.Login != nil {
			oauthLogin := provider.Auth.OAuth.Login
			login = func(interaction *AuthInteraction) (*Credential, error) {
				credential, err := oauthLogin(interaction)
				if err != nil {
					return nil, err
				}
				return &Credential{Type: CredentialOAuth, OAuth: credential}, nil
			}
		}
	} else {
		methodName = "api_key"
		if provider.Auth.APIKey != nil && provider.Auth.APIKey.Login != nil {
			apiKeyLogin := provider.Auth.APIKey.Login
			login = func(interaction *AuthInteraction) (*Credential, error) {
				credential, err := apiKeyLogin(interaction)
				if err != nil {
					return nil, err
				}
				return &Credential{Type: CredentialAPIKey, APIKey: credential}, nil
			}
		}
	}
	if login == nil {
		return nil, NewModelsError(ErrCodeAuth, fmt.Sprintf("%s does not support %s login", provider.Name, methodName), nil)
	}

	credential, err := raceWithContext(ctx, func() (*Credential, error) {
		return login(&AuthInteraction{Ctx: ctx, Prompt: interaction.Prompt, Notify: interaction.Notify})
	})
	if err != nil {
		return nil, err
	}

	// Persist the credential. If cancellation arrives before the store mutation
	// begins the caller sees the abort; once the mutation started it is allowed
	// to settle (upstream's mutationStarted handshake).
	started := make(chan struct{})
	mutationDone := make(chan error, 1)
	go func() {
		_, modifyErr := m.credentials.Modify(providerID, func(current *Credential) (*Credential, error) {
			close(started)
			return credential, nil
		}, ctx)
		mutationDone <- modifyErr
	}()

	select {
	case <-started:
		// The write is in flight; wait for it (or for the store's own
		// cancellation) but never report a plain abort from here.
		if err := <-mutationDone; err != nil {
			return nil, NewModelsError(ErrCodeAuth, fmt.Sprintf("Credential store modify failed for %s", providerID), err)
		}
		return credential, nil
	case err := <-mutationDone:
		if err != nil {
			return nil, NewModelsError(ErrCodeAuth, fmt.Sprintf("Credential store modify failed for %s", providerID), err)
		}
		return credential, nil
	case <-ctx.Done():
		// Cancellation before the mutation began: abandon the write.
		go func() {
			<-mutationDone
		}()
		return nil, ctx.Err()
	}
}

// Logout deletes the provider's stored credential.
func (m *Models) Logout(providerID string, ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := m.credentials.Delete(providerID, ctx); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return NewModelsError(ErrCodeAuth, fmt.Sprintf("Credential store delete failed for %s", providerID), err)
	}
	return nil
}

// raceWithContext runs fn in a goroutine and stops waiting when ctx is done
// while observing the abandoned operation through settlement (port of
// raceWithAbortSignal).
func raceWithContext[T any](ctx context.Context, fn func() (T, error)) (T, error) {
	type outcome struct {
		value T
		err   error
	}
	finished := make(chan outcome, 1)
	go func() {
		value, err := fn()
		finished <- outcome{value: value, err: err}
	}()
	select {
	case result := <-finished:
		return result.value, result.err
	case <-ctx.Done():
		go func() { <-finished }()
		var zero T
		return zero, ctx.Err()
	}
}
