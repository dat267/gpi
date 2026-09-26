package coding

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/dat267/pier/ai"
)

// Port of cli/auth-command.ts, cli/auth-check.ts, cli/credential-print.ts, and
// main.ts's runAuthCommand: `pi auth check|print-api-key|print-bearer-token`.

// AuthCommandKind is one `pi auth` subcommand.
type AuthCommandKind string

const (
	AuthCommandCheck            AuthCommandKind = "check"
	AuthCommandPrintAPIKey      AuthCommandKind = "api_key"
	AuthCommandPrintBearerToken AuthCommandKind = "bearer_token"
)

// AuthCommand is a parsed `pi auth` invocation. Args are the remaining
// arguments after the subcommand, parsed by ParseArgs.
type AuthCommand struct {
	Kind        AuthCommandKind
	Args        []string
	JSON        bool
	Credentials bool
	NoRefresh   bool
	MinExpiryMS *int64
}

// AuthCommandError is a user-facing auth-command failure.
type AuthCommandError struct{ Message string }

func (e *AuthCommandError) Error() string { return e.Message }

func authCommandName(kind AuthCommandKind) string {
	switch kind {
	case AuthCommandCheck:
		return "auth check"
	case AuthCommandPrintAPIKey:
		return "auth print-api-key"
	default:
		return "auth print-bearer-token"
	}
}

func authCommandUsage(appName string, kind AuthCommandKind) string {
	switch kind {
	case AuthCommandCheck:
		return fmt.Sprintf("%s auth check --provider <provider> [--json] [--credentials] [--no-refresh]", appName)
	case AuthCommandPrintAPIKey:
		return fmt.Sprintf("%s auth print-api-key --provider <provider> [--model <model>]", appName)
	default:
		return fmt.Sprintf("%s auth print-bearer-token --provider <provider> [--model <model>] [--min-expiry <duration>]", appName)
	}
}

// IsAuthCommandHelp reports whether the invocation asks for auth help.
func IsAuthCommandHelp(args []string) bool {
	if len(args) == 0 || args[0] != "auth" {
		return false
	}
	if len(args) == 1 || args[1] == "help" {
		return true
	}
	for _, arg := range args {
		if arg == "--help" || arg == "-h" {
			return true
		}
	}
	return false
}

// PrintAuthCommandHelp renders the `pi auth` usage text.
func PrintAuthCommandHelp(appName string) string {
	return fmt.Sprintf(`Usage:
  %[1]s auth print-api-key [--provider <provider>] [--model <model>]
  %[1]s auth print-bearer-token [--provider <provider>] [--model <model>] [--min-expiry <duration>]
  %[1]s auth check [--provider <provider>] [--model <model>] [--json] [--credentials] [--no-refresh]

Auth commands require at least one of --provider or --model. Checks refresh expired OAuth credentials by default; --no-refresh prevents this. --credentials emits the credential, or includes it in JSON output.`, appName)
}

var authMinExpiryPattern = regexp.MustCompile(`^(\d+)(ms|s|m|h)$`)

// parseAuthMinExpiry parses a duration such as 30m or 1h into milliseconds.
func parseAuthMinExpiry(value string) (int64, bool) {
	match := authMinExpiryPattern.FindStringSubmatch(strings.ToLower(strings.TrimSpace(value)))
	if match == nil {
		return 0, false
	}
	amount, err := strconv.ParseInt(match[1], 10, 64)
	if err != nil {
		return 0, false
	}
	switch match[2] {
	case "ms":
		return amount, true
	case "s":
		return amount * 1000, true
	case "m":
		return amount * 60_000, true
	default:
		return amount * 3_600_000, true
	}
}

// ParseAuthCommand parses `pi auth ...`; nil means the invocation is not an
// auth command.
func ParseAuthCommand(args []string) (*AuthCommand, error) {
	if len(args) == 0 || args[0] != "auth" {
		return nil, nil
	}
	var kind AuthCommandKind
	if len(args) > 1 {
		switch args[1] {
		case "check":
			kind = AuthCommandCheck
		case "print-api-key":
			kind = AuthCommandPrintAPIKey
		case "print-bearer-token":
			kind = AuthCommandPrintBearerToken
		}
	}
	if kind == "" {
		name := ""
		if len(args) > 1 {
			name = args[1]
		}
		return nil, &AuthCommandError{Message: fmt.Sprintf(
			`Unknown auth command %q. Use "%s auth print-api-key", "%s auth print-bearer-token", or "%s auth check".`,
			name, AppName, AppName, AppName)}
	}
	command := &AuthCommand{Kind: kind}
	for index := 2; index < len(args); index++ {
		arg := args[index]
		if arg == "--min-expiry" {
			if kind != AuthCommandPrintBearerToken {
				return nil, &AuthCommandError{Message: "--min-expiry is only supported by print-bearer-token"}
			}
			index++
			var value string
			if index < len(args) {
				value = args[index]
			}
			milliseconds, ok := parseAuthMinExpiry(value)
			if !ok {
				return nil, &AuthCommandError{Message: "--min-expiry must use a duration such as 30m or 1h"}
			}
			command.MinExpiryMS = &milliseconds
			continue
		}
		if arg == "--json" || arg == "--credentials" || arg == "--no-refresh" {
			if kind != AuthCommandCheck {
				return nil, &AuthCommandError{Message: arg + " is only supported by auth check"}
			}
			switch arg {
			case "--json":
				command.JSON = true
			case "--credentials":
				command.Credentials = true
			case "--no-refresh":
				command.NoRefresh = true
			}
			continue
		}
		command.Args = append(command.Args, arg)
	}
	return command, nil
}

// validateAuthCommandArgs extracts --provider/--model and rejects everything
// else (upstream validateAuthCommandArgs).
func validateAuthCommandArgs(args *Args, kind AuthCommandKind) (string, string, error) {
	provider := strings.TrimSpace(derefString(args.Provider))
	model := strings.TrimSpace(derefString(args.Model))
	if len(args.UnknownFlags) > 0 {
		for option := range args.UnknownFlags {
			return "", "", &AuthCommandError{Message: fmt.Sprintf("Unknown option --%s for %q.", option, authCommandName(kind))}
		}
	}
	if args.APIKey != nil || len(args.Messages) > 0 || len(args.FileArgs) > 0 {
		return "", "", &AuthCommandError{Message: "Auth commands only accept --provider and --model"}
	}
	if kind == AuthCommandCheck {
		if provider == "" && model == "" {
			return "", "", &AuthCommandError{Message: "Auth checks require --provider <provider> or --model <model>"}
		}
		return provider, model, nil
	}
	if provider == "" && model == "" {
		return "", "", &AuthCommandError{Message: "Credential printing requires --provider <provider> or --model <model>"}
	}
	return provider, model, nil
}

var bearerTokenPattern = regexp.MustCompile(`(?i)^Bearer\s+(.+)$`)

// GetAuthCredential extracts the printable credential from a resolved auth
// result: the API key, else a Bearer Authorization header (upstream
// getAuthCredential).
func GetAuthCredential(auth *ai.AuthResult) string {
	if auth == nil {
		return ""
	}
	if auth.Auth.APIKey != "" {
		return auth.Auth.APIKey
	}
	for name, value := range auth.Auth.Headers {
		if !strings.EqualFold(name, "authorization") || value == nil {
			continue
		}
		if match := bearerTokenPattern.FindStringSubmatch(*value); match != nil {
			return match[1]
		}
	}
	return ""
}

// Auth check statuses and reasons (upstream AuthCheckStatus/AuthCheckReason).
const (
	AuthCheckReady    = "ready"
	AuthCheckNotReady = "not_ready"
	AuthCheckInvalid  = "invalid"

	AuthReasonProviderNotFound         = "provider_not_found"
	AuthReasonCredentialsNotConfigured = "credentials_not_configured"
	AuthReasonCredentialNotAvailable   = "credential_not_available"
	AuthReasonInvalidState             = "invalid_state"
)

// AuthCheckResult is one `auth check` outcome.
type AuthCheckResult struct {
	Status   string `json:"status"`
	Provider string `json:"provider"`
	Reason   string `json:"reason,omitempty"`
	AuthType string `json:"authType,omitempty"`
}

// CheckProviderAuth checks a provider's auth readiness (upstream
// checkProviderAuth).
func CheckProviderAuth(args *Args, runtime *ModelRuntime, refresh bool) (AuthCheckResult, error) {
	cliProvider, cliModel, err := validateAuthCommandArgs(args, AuthCommandCheck)
	if err != nil {
		return AuthCheckResult{}, err
	}
	provider := cliProvider
	if cliModel != "" {
		resolved := ResolveCliModel(ResolveCliModelOptions{CLIProvider: cliProvider, CLIModel: cliModel, ModelRuntime: runtime})
		if resolved.Error != "" || resolved.Model == nil {
			message := resolved.Error
			if message == "" {
				message = fmt.Sprintf("Unable to resolve model %q", cliModel)
			}
			return AuthCheckResult{}, &AuthCommandError{Message: message}
		}
		provider = resolved.Model.Provider
	}
	if provider == "" {
		return AuthCheckResult{}, &AuthCommandError{Message: "Unable to resolve an auth provider"}
	}
	if runtime.GetError() != "" {
		return AuthCheckResult{Status: AuthCheckInvalid, Provider: provider, Reason: AuthReasonInvalidState}, nil
	}
	if runtime.GetProvider(provider) == nil {
		return AuthCheckResult{Status: AuthCheckNotReady, Provider: provider, Reason: AuthReasonProviderNotFound}, nil
	}
	check, err := runtime.CheckAuth(provider, context.Background())
	if err != nil {
		return AuthCheckResult{Status: AuthCheckInvalid, Provider: provider, Reason: AuthReasonInvalidState}, nil
	}
	if check == nil {
		return AuthCheckResult{Status: AuthCheckNotReady, Provider: provider, Reason: AuthReasonCredentialsNotConfigured}, nil
	}
	if refresh {
		if auth, err := runtime.GetAuth(provider, nil); err != nil || auth == nil {
			return AuthCheckResult{Status: AuthCheckNotReady, Provider: provider, Reason: AuthReasonCredentialsNotConfigured}, nil
		}
	}
	return AuthCheckResult{Status: AuthCheckReady, Provider: provider, AuthType: check.Type}, nil
}

// GetProviderCredential returns a provider's credential, preferring the stored
// OAuth access token when not refreshing (upstream getProviderCredential).
func GetProviderCredential(providerID string, runtime *ModelRuntime, credentials ai.CredentialStore, refresh bool) (string, error) {
	credential, err := credentials.Read(providerID, context.Background())
	if err != nil {
		return "", err
	}
	if !refresh && credential != nil && credential.Type == ai.CredentialOAuth && credential.OAuth != nil {
		return credential.OAuth.Access, nil
	}
	auth, err := runtime.GetAuth(providerID, nil)
	if err != nil {
		return "", err
	}
	return GetAuthCredential(auth), nil
}

const defaultBearerTokenMinExpiryMS = int64(30 * 60_000)

// ResolveCredentialForPrint resolves one configured provider credential
// (upstream resolveCredentialForPrint).
func ResolveCredentialForPrint(args *Args, runtime *ModelRuntime, kind AuthCommandKind, minExpiryMS *int64) (string, error) {
	cliProvider, cliModel, err := validateAuthCommandArgs(args, kind)
	if err != nil {
		return "", err
	}
	infos, err := runtime.ListCredentials(context.Background())
	if err != nil {
		return "", err
	}
	credentialTypes := map[string]ai.CredentialType{}
	for _, info := range infos {
		credentialTypes[info.ProviderID] = info.Type
	}
	type printTarget struct {
		id    string
		model *ai.Model
	}
	var providers []printTarget
	if cliProvider != "" {
		provider := runtime.GetProvider(cliProvider)
		if provider == nil {
			return "", &AuthCommandError{Message: fmt.Sprintf(`Unknown provider %q. Use --list-models to see available providers.`, cliProvider)}
		}
		if cliModel != "" {
			resolved := ResolveCliModel(ResolveCliModelOptions{CLIProvider: provider.ID, CLIModel: cliModel, ModelRuntime: runtime})
			if resolved.Error != "" || resolved.Model == nil {
				message := resolved.Error
				if message == "" {
					message = "Unable to resolve the requested provider/model"
				}
				return "", &AuthCommandError{Message: message}
			}
			providers = append(providers, printTarget{id: provider.ID, model: resolved.Model})
		} else {
			providers = append(providers, printTarget{id: provider.ID})
		}
	} else {
		for _, provider := range runtime.GetProviders() {
			if _, ok := credentialTypes[provider.ID]; !ok {
				continue
			}
			resolved := ResolveCliModel(ResolveCliModelOptions{CLIProvider: provider.ID, CLIModel: cliModel, ModelRuntime: runtime})
			if resolved.Model != nil && resolved.Error == "" && !strings.Contains(resolved.Warning, "Using custom model id") {
				providers = append(providers, printTarget{id: provider.ID, model: resolved.Model})
			}
		}
		if len(providers) == 0 {
			return "", &AuthCommandError{Message: fmt.Sprintf(`Model %q not found. Use --list-models to see available models.`, cliModel)}
		}
	}

	type resolvedCredential struct {
		providerID string
		value      string
	}
	var credentials []resolvedCredential
	for _, provider := range providers {
		credentialType := credentialTypes[provider.id]
		if kind == AuthCommandPrintAPIKey && credentialType == ai.CredentialOAuth {
			continue
		}
		if kind == AuthCommandPrintBearerToken && credentialType != ai.CredentialOAuth {
			continue
		}
		var overrides *ModelRuntimeAuthOverrides
		if kind == AuthCommandPrintBearerToken {
			validity := defaultBearerTokenMinExpiryMS
			if minExpiryMS != nil {
				validity = *minExpiryMS
			}
			overrides = &ModelRuntimeAuthOverrides{MinOAuthValidityMS: &validity}
		}
		var auth *ai.AuthResult
		var authErr error
		if provider.model != nil {
			auth, authErr = runtime.GetAuthForModel(provider.model, overrides)
		} else {
			auth, authErr = runtime.GetAuth(provider.id, overrides)
		}
		if authErr != nil {
			return "", authErr
		}
		if value := GetAuthCredential(auth); value != "" {
			credentials = append(credentials, resolvedCredential{providerID: provider.id, value: value})
		}
	}

	if len(credentials) == 1 {
		return credentials[0].value, nil
	}
	if len(credentials) == 0 {
		providerID := ""
		if len(providers) > 0 {
			providerID = providers[0].id
		}
		credentialType := credentialTypes[providerID]
		if cliProvider != "" && kind == AuthCommandPrintAPIKey && credentialType == ai.CredentialOAuth {
			return "", &AuthCommandError{Message: fmt.Sprintf(`Provider %q is configured with OAuth, not an API key`, providerID)}
		}
		if cliProvider != "" && kind == AuthCommandPrintBearerToken && credentialType != ai.CredentialOAuth {
			return "", &AuthCommandError{Message: fmt.Sprintf(`Provider %q is not configured with an OAuth bearer token`, providerID)}
		}
		label := "API key"
		if kind == AuthCommandPrintBearerToken {
			label = "OAuth bearer token"
		}
		return "", &AuthCommandError{Message: fmt.Sprintf("No usable %s is configured", label)}
	}
	ids := make([]string, 0, len(credentials))
	for _, credential := range credentials {
		ids = append(ids, credential.providerID)
	}
	return "", &AuthCommandError{Message: fmt.Sprintf("Multiple configured providers matched (%s). Specify --provider.", strings.Join(ids, ", "))}
}

func authCommandErrorMessage(err error, fallback string) string {
	var commandErr *AuthCommandError
	if errors.As(err, &commandErr) {
		return commandErr.Message
	}
	return fallback
}

// RunAuthCommand handles a `pi auth ...` invocation. handled is false when the
// first argument is not "auth"; otherwise the exit code is returned.
func RunAuthCommand(args []string, stdout, stderr io.Writer) (bool, int) {
	if len(args) == 0 || args[0] != "auth" {
		return false, 0
	}
	if IsAuthCommandHelp(args) {
		fmt.Fprint(stdout, PrintAuthCommandHelp(AppName))
		return true, 0
	}
	command, err := ParseAuthCommand(args)
	if err != nil {
		fmt.Fprintf(stderr, "Error: %s\n", authCommandErrorMessage(err, "Failed to parse auth command"))
		return true, 1
	}
	if command == nil {
		return false, 0
	}
	parsed := ParseArgs(command.Args)
	if len(parsed.UnknownFlags) > 0 {
		for option := range parsed.UnknownFlags {
			fmt.Fprintf(stderr, "Unknown option --%s for %q.\n", option, authCommandName(command.Kind))
			break
		}
		fmt.Fprintf(stderr, "Use \"%s --help\" or \"%s\".\n", AppName, authCommandUsage(AppName, command.Kind))
		return true, 1
	}
	if len(parsed.Diagnostics) > 0 {
		messages := make([]string, 0, len(parsed.Diagnostics))
		for _, diagnostic := range parsed.Diagnostics {
			messages = append(messages, diagnostic.Message)
		}
		fmt.Fprintf(stderr, "Error: %s\n", strings.Join(messages, "\n"))
		if command.Kind == AuthCommandCheck {
			return true, 2
		}
		return true, 1
	}

	authPath := filepath.Join(GetAgentDir(), "auth.json")
	refreshOnCreate := false

	if command.Kind != AuthCommandCheck {
		runtime, err := CreateModelRuntime(CreateModelRuntimeOptions{
			AuthPath:          authPath,
			AllowModelNetwork: false,
			RefreshOnCreate:   &refreshOnCreate,
		})
		if err != nil {
			fmt.Fprintf(stderr, "Error: %s\n", authCommandErrorMessage(err, "Failed to resolve credential"))
			return true, 1
		}
		credential, err := ResolveCredentialForPrint(parsed, runtime, command.Kind, command.MinExpiryMS)
		if err != nil {
			fmt.Fprintf(stderr, "Error: %s\n", authCommandErrorMessage(err, "Failed to resolve credential"))
			return true, 1
		}
		fmt.Fprintf(stdout, "%s\n", credential)
		return true, 0
	}

	provider, model, argErr := validateAuthCommandArgs(parsed, AuthCommandCheck)
	if argErr != nil {
		fmt.Fprintf(stderr, "Error: %s\n", authCommandErrorMessage(argErr, "Failed to resolve credential"))
		return true, 2
	}
	var credentials ai.CredentialStore
	if command.NoRefresh {
		credentials = NewReadOnlyAuthStorage(authPath)
	} else {
		credentials = NewAuthStorage(authPath)
	}
	runtime, err := CreateModelRuntime(CreateModelRuntimeOptions{
		Credentials:       credentials,
		AllowModelNetwork: false,
		RefreshOnCreate:   &refreshOnCreate,
		DisableModelsJSON: true,
	})
	invalid := func() AuthCheckResult {
		resolved := provider
		if resolved == "" {
			resolved = model
		}
		return AuthCheckResult{Status: AuthCheckInvalid, Provider: resolved, Reason: AuthReasonInvalidState}
	}
	var result AuthCheckResult
	credential := ""
	if err != nil {
		result = invalid()
	} else if result, err = CheckProviderAuth(parsed, runtime, !command.NoRefresh); err != nil {
		result = invalid()
	} else if command.Credentials && result.Status == AuthCheckReady {
		credential, err = GetProviderCredential(result.Provider, runtime, credentials, !command.NoRefresh)
		if err != nil || credential == "" {
			result = AuthCheckResult{Status: AuthCheckNotReady, Provider: result.Provider, Reason: AuthReasonCredentialNotAvailable}
		}
	}

	if command.JSON {
		output := struct {
			Status      string `json:"status"`
			Provider    string `json:"provider"`
			Reason      string `json:"reason,omitempty"`
			AuthType    string `json:"authType,omitempty"`
			Credentials string `json:"credentials,omitempty"`
		}{result.Status, result.Provider, result.Reason, result.AuthType, credential}
		encoded, marshalErr := json.Marshal(output)
		if marshalErr != nil {
			fmt.Fprintf(stderr, "Error: %s\n", marshalErr.Error())
			return true, 2
		}
		fmt.Fprintf(stdout, "%s\n", encoded)
	} else if credential != "" {
		fmt.Fprintf(stdout, "%s\n", credential)
	} else {
		fmt.Fprintf(stdout, "%s\n", result.Status)
	}
	switch result.Status {
	case AuthCheckReady:
		return true, 0
	case AuthCheckNotReady:
		return true, 1
	default:
		return true, 2
	}
}
