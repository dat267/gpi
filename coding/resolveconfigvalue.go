package coding

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"
)

// Port of core/resolve-config-value.ts: resolve configuration values that may
// be shell commands, environment variables, or literals. Used by auth-storage
// and model-registry.

var (
	configEnvVarNamePattern       = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	configEnvVarNamePrefixPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*`)
)

// templatePart is either literal text or an environment reference.
type templatePart struct {
	isEnv bool
	value string
}

// configValueReference is a command (leading "!") or a template.
type configValueReference struct {
	isCommand bool
	config    string
	parts     []templatePart
}

// commandConfig returns the command text (with the leading "!").
func (r configValueReference) commandConfig() string { return r.config }

func appendConfigLiteral(parts []templatePart, value string) []templatePart {
	if value == "" {
		return parts
	}
	if len(parts) > 0 && !parts[len(parts)-1].isEnv {
		parts[len(parts)-1].value += value
		return parts
	}
	return append(parts, templatePart{value: value})
}

func parseConfigValueTemplate(config string) []templatePart {
	var parts []templatePart
	index := 0

	for index < len(config) {
		dollarIndex := strings.Index(config[index:], "$")
		if dollarIndex < 0 {
			parts = appendConfigLiteral(parts, config[index:])
			break
		}
		dollarIndex += index

		parts = appendConfigLiteral(parts, config[index:dollarIndex])
		var nextChar byte
		if dollarIndex+1 < len(config) {
			nextChar = config[dollarIndex+1]
		}

		if nextChar == '$' || nextChar == '!' {
			parts = appendConfigLiteral(parts, string(nextChar))
			index = dollarIndex + 2
			continue
		}

		if nextChar == '{' {
			endIndex := strings.Index(config[dollarIndex+2:], "}")
			if endIndex < 0 {
				parts = appendConfigLiteral(parts, "$")
				index = dollarIndex + 1
				continue
			}
			endIndex += dollarIndex + 2
			name := config[dollarIndex+2 : endIndex]
			if configEnvVarNamePattern.MatchString(name) {
				parts = append(parts, templatePart{isEnv: true, value: name})
			} else {
				parts = appendConfigLiteral(parts, config[dollarIndex:endIndex+1])
			}
			index = endIndex + 1
			continue
		}

		match := configEnvVarNamePrefixPattern.FindString(config[dollarIndex+1:])
		if match != "" {
			parts = append(parts, templatePart{isEnv: true, value: match})
			index = dollarIndex + 1 + len(match)
			continue
		}

		parts = appendConfigLiteral(parts, "$")
		index = dollarIndex + 1
	}

	return parts
}

func parseConfigValueReference(config string) configValueReference {
	if strings.HasPrefix(config, "!") {
		return configValueReference{isCommand: true, config: config}
	}
	return configValueReference{parts: parseConfigValueTemplate(config)}
}

func resolveEnvConfigValue(name string, env map[string]string) string {
	if env != nil {
		if value := env[name]; value != "" {
			return value
		}
	}
	return os.Getenv(name)
}

func getTemplateEnvVarNames(parts []templatePart) []string {
	var names []string
	for _, part := range parts {
		if !part.isEnv {
			continue
		}
		found := false
		for _, name := range names {
			if name == part.value {
				found = true
				break
			}
		}
		if !found {
			names = append(names, part.value)
		}
	}
	return names
}

func resolveConfigTemplate(parts []templatePart, env map[string]string) (string, bool) {
	var resolved strings.Builder
	for _, part := range parts {
		if !part.isEnv {
			resolved.WriteString(part.value)
			continue
		}
		envValue := resolveEnvConfigValue(part.value, env)
		if envValue == "" {
			return "", false
		}
		resolved.WriteString(envValue)
	}
	return resolved.String(), true
}

// GetConfigValueEnvVarName returns the single env var a config value references,
// when it is exactly one reference.
func GetConfigValueEnvVarName(config string) string {
	reference := parseConfigValueReference(config)
	if reference.isCommand {
		return ""
	}
	if len(reference.parts) == 1 && reference.parts[0].isEnv {
		return reference.parts[0].value
	}
	return ""
}

// GetConfigValueEnvVarNames lists every env var a config value references.
func GetConfigValueEnvVarNames(config string) []string {
	reference := parseConfigValueReference(config)
	if reference.isCommand {
		return nil
	}
	return getTemplateEnvVarNames(reference.parts)
}

// GetMissingConfigValueEnvVarNames lists referenced env vars that are unset.
func GetMissingConfigValueEnvVarNames(config string, env map[string]string) []string {
	var missing []string
	for _, name := range GetConfigValueEnvVarNames(config) {
		if resolveEnvConfigValue(name, env) == "" {
			missing = append(missing, name)
		}
	}
	return missing
}

// IsCommandConfigValue reports whether the value is a shell command.
func IsCommandConfigValue(config string) bool {
	return parseConfigValueReference(config).isCommand
}

// IsConfigValueConfigured reports whether every referenced env var is set.
func IsConfigValueConfigured(config string, env map[string]string) bool {
	return len(GetMissingConfigValueEnvVarNames(config, env)) == 0
}

// configCommandCache caches shell command results for the process lifetime.
var (
	configCommandCacheMu sync.Mutex
	configCommandCache   = map[string]*string{}
)

// ResolveConfigValue resolves a config value (API key, header value, ...) to an
// actual value. A leading "!" executes the rest as a shell command and uses
// stdout (cached); "$ENV" and "${ENV}" interpolate environment variables; "$$"
// and "$!" escape a literal "$" and "!"; everything else is a literal.
func ResolveConfigValue(config string, env map[string]string) (string, bool) {
	return resolveConfigValue(config, env, true)
}

// ResolveConfigValueUncached is ResolveConfigValue without the command cache.
func ResolveConfigValueUncached(config string, env map[string]string) (string, bool) {
	return resolveConfigValue(config, env, false)
}

func resolveConfigValue(config string, env map[string]string, cached bool) (string, bool) {
	reference := parseConfigValueReference(config)
	if reference.isCommand {
		return executeConfigCommand(reference.config, cached)
	}
	return resolveConfigTemplate(reference.parts, env)
}

// ResolveConfigValueOrThrow resolves a value or reports the specific failure.
func ResolveConfigValueOrThrow(config, description string, env map[string]string) (string, error) {
	resolved, ok := ResolveConfigValueUncached(config, env)
	if ok {
		return resolved, nil
	}
	reference := parseConfigValueReference(config)
	if reference.isCommand {
		return "", fmt.Errorf("Failed to resolve %s from shell command: %s", description, reference.config[1:])
	}
	missing := GetMissingConfigValueEnvVarNames(config, env)
	if len(missing) == 1 {
		return "", fmt.Errorf("Failed to resolve %s from environment variable: %s", description, missing[0])
	}
	if len(missing) > 1 {
		return "", fmt.Errorf("Failed to resolve %s from environment variables: %s", description, strings.Join(missing, ", "))
	}
	return "", fmt.Errorf("Failed to resolve %s", description)
}

// ResolveHeaders resolves header values with the same logic as API keys;
// unresolvable headers are omitted.
func ResolveHeaders(headers map[string]string, env map[string]string) map[string]string {
	if headers == nil {
		return nil
	}
	resolved := map[string]string{}
	for key, value := range headers {
		if resolvedValue, ok := ResolveConfigValue(value, env); ok && resolvedValue != "" {
			resolved[key] = resolvedValue
		}
	}
	if len(resolved) == 0 {
		return nil
	}
	return resolved
}

// ResolveHeadersOrThrow resolves header values, failing on the first
// unresolvable one.
func ResolveHeadersOrThrow(headers map[string]string, description string, env map[string]string) (map[string]string, error) {
	if headers == nil {
		return nil, nil
	}
	resolved := map[string]string{}
	for key, value := range headers {
		resolvedValue, err := ResolveConfigValueOrThrow(value, fmt.Sprintf("%s header %q", description, key), env)
		if err != nil {
			return nil, err
		}
		resolved[key] = resolvedValue
	}
	if len(resolved) == 0 {
		return nil, nil
	}
	return resolved, nil
}

// ClearConfigValueCache drops the cached shell command results (for tests).
func ClearConfigValueCache() {
	configCommandCacheMu.Lock()
	configCommandCache = map[string]*string{}
	configCommandCacheMu.Unlock()
}

// configCommandTimeout is upstream's 10s spawnSync/execSync timeout.
const configCommandTimeout = 10 * time.Second

// executeWithConfiguredShell runs the command through the resolved shell.
// executed reports whether the shell could be spawned at all (upstream
// distinguishes ENOENT, which falls back to the default shell).
func executeWithConfiguredShell(command string, env map[string]string) (value string, executed bool) {
	config, err := GetShellConfig("")
	if err != nil {
		return "", false
	}
	commandFromStdin := config.CommandTransport == "stdin"
	args := append([]string{}, config.Args...)
	if !commandFromStdin {
		args = append(args, command)
	}
	ctx, cancel := context.WithTimeout(context.Background(), configCommandTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, config.Shell, args...)
	if commandFromStdin {
		cmd.Stdin = strings.NewReader(command)
	}
	if env != nil {
		cmd.Env = mergeEnvMap(os.Environ(), env)
	}
	output, err := cmd.Output()
	if err != nil {
		var exitError *exec.ExitError
		if errors.As(err, &exitError) || errors.Is(err, context.DeadlineExceeded) {
			return "", true
		}
		// The shell could not be spawned (upstream ENOENT).
		return "", false
	}
	return strings.TrimSpace(string(output)), true
}

// executeWithDefaultShell runs the command through the platform default shell
// (upstream execSync).
func executeWithDefaultShell(command string, env map[string]string) (string, bool) {
	shell := "/bin/sh"
	args := []string{"-c", command}
	if runtime.GOOS == "windows" {
		shell = "cmd.exe"
		args = []string{"/d", "/s", "/c", command}
	}
	ctx, cancel := context.WithTimeout(context.Background(), configCommandTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, shell, args...)
	if env != nil {
		cmd.Env = mergeEnvMap(os.Environ(), env)
	}
	output, err := cmd.Output()
	if err != nil {
		return "", false
	}
	value := strings.TrimSpace(string(output))
	return value, value != ""
}

func executeConfigCommandUncached(commandConfig string) (string, bool) {
	command := commandConfig[1:]
	if runtime.GOOS == "windows" {
		// Only Windows falls back to the default shell when the configured
		// shell cannot be spawned.
		if value, executed := executeWithConfiguredShell(command, nil); executed {
			return value, value != ""
		}
		return executeWithDefaultShell(command, nil)
	}
	return executeWithDefaultShell(command, nil)
}

func executeConfigCommand(commandConfig string, cached bool) (string, bool) {
	if !cached {
		return executeConfigCommandUncached(commandConfig)
	}

	configCommandCacheMu.Lock()
	if value, ok := configCommandCache[commandConfig]; ok {
		configCommandCacheMu.Unlock()
		if value == nil {
			return "", false
		}
		return *value, true
	}
	configCommandCacheMu.Unlock()

	value, ok := executeConfigCommandUncached(commandConfig)
	configCommandCacheMu.Lock()
	if ok {
		copied := value
		configCommandCache[commandConfig] = &copied
	} else {
		configCommandCache[commandConfig] = nil
	}
	configCommandCacheMu.Unlock()
	return value, ok
}

// mergeEnvMap overlays extra variables on a base environment.
func mergeEnvMap(base []string, extra map[string]string) []string {
	out := make([]string, 0, len(base)+len(extra))
	for _, entry := range base {
		name, _, _ := strings.Cut(entry, "=")
		if _, overridden := extra[name]; overridden {
			continue
		}
		out = append(out, entry)
	}
	for name, value := range extra {
		out = append(out, name+"="+value)
	}
	return out
}
