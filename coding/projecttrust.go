package coding

import (
	"fmt"
	"path/filepath"
)

// Port of core/project-trust.ts, core/source-info.ts, core/auth-guidance.ts,
// and core/settings-diagnostics.ts.
//
// D39: resolveProjectTrusted's extension hook (emitProjectTrustEvent) is
// extension mechanics and omitted; the remaining decision order (override,
// no-resource fast path, stored decision, default, UI selection) is ported.

// AppMode is the active application mode.
type AppMode = string

const (
	AppModeInteractive AppMode = "interactive"
	AppModePrint       AppMode = "print"
	AppModeJSON        AppMode = "json"
	AppModeRPC         AppMode = "rpc"
)

// ResolveProjectTrustedOptions are the trust resolution inputs.
type ResolveProjectTrustedOptions struct {
	Cwd           string
	TrustStore    *ProjectTrustStore
	TrustOverride *bool
	// DefaultProjectTrust is "always", "never", or "ask" (default "ask").
	DefaultProjectTrust string
	// ProjectTrustContext selects a trust option.
	ProjectTrustContext ProjectTrustContext
}

// ProjectTrustContext is the UI surface used by the trust prompt.
type ProjectTrustContext struct {
	// HasUI reports whether an interactive UI can ask the user.
	HasUI bool
	// Select presents a prompt and returns the chosen label.
	Select func(prompt string, options []string) (string, error)
}

// FormatProjectTrustPrompt renders the trust question. Upstream's sentence also
// promises installing project packages and running project extensions; this port
// does neither (D41), so the question names only what is actually gated.
func FormatProjectTrustPrompt(cwd string) string {
	return fmt.Sprintf("Trust project folder?\n%s\n\nThis allows %s to load %s settings and resources.",
		cwd, AppName, ConfigDirName)
}

// ResolveProjectTrusted decides whether a project folder is trusted.
func ResolveProjectTrusted(options ResolveProjectTrustedOptions) (bool, error) {
	if options.TrustOverride != nil {
		return *options.TrustOverride, nil
	}
	if !HasTrustRequiringProjectResources(options.Cwd) {
		return true, nil
	}
	if decision := options.TrustStore.Get(options.Cwd); decision != nil {
		return *decision, nil
	}

	defaultTrust := options.DefaultProjectTrust
	if defaultTrust == "" {
		defaultTrust = "ask"
	}
	switch defaultTrust {
	case "always":
		return true, nil
	case "never":
		return false, nil
	}
	if !options.ProjectTrustContext.HasUI || options.ProjectTrustContext.Select == nil {
		return false, nil
	}

	trustOptions := GetProjectTrustOptions(options.Cwd, true)
	labels := make([]string, 0, len(trustOptions))
	for _, option := range trustOptions {
		labels = append(labels, option.Label)
	}
	selected, err := options.ProjectTrustContext.Select(FormatProjectTrustPrompt(options.Cwd), labels)
	if err != nil {
		return false, err
	}
	for _, option := range trustOptions {
		if option.Label != selected {
			continue
		}
		if len(option.Updates) > 0 {
			if err := options.TrustStore.SetMany(option.Updates); err != nil {
				return false, err
			}
		}
		return option.Trusted, nil
	}
	return false, nil
}

// PathMetadata is the package metadata a source info derives from
// (upstream package-manager PathMetadata). SourceScope/SourceOrigin/SourceInfo
// and CreateSyntheticSourceInfo live in prompttemplates.go.
type PathMetadata struct {
	Source  string
	Scope   SourceScope
	Origin  SourceOrigin
	BaseDir string
}

// CreateSourceInfo builds a source info from package metadata.
func CreateSourceInfo(path string, metadata PathMetadata) SourceInfo {
	return SourceInfo{
		Path: path, Source: metadata.Source, Scope: metadata.Scope,
		Origin: metadata.Origin, BaseDir: metadata.BaseDir,
	}
}

// GetProviderLoginHelp renders the login help text.
func GetProviderLoginHelp() string {
	docs := GetDocsPath()
	return fmt.Sprintf("Use /login to log into a provider via OAuth or API key. See:\n  %s\n  %s",
		filepath.Join(docs, "providers.md"), filepath.Join(docs, "models.md"))
}

// FormatNoModelsAvailableMessage reports that no models are available.
func FormatNoModelsAvailableMessage() string {
	return "No models available. " + GetProviderLoginHelp()
}

// FormatNoModelSelectedMessage reports that no model is selected.
func FormatNoModelSelectedMessage() string {
	return "No model selected.\n\n" + GetProviderLoginHelp() + "\n\nThen use /model to select a model."
}

// FormatNoAPIKeyFoundMessage reports a missing API key at request time.
func FormatNoAPIKeyFoundMessage(provider string) string {
	display := provider
	if provider == "unknown" {
		display = "the selected model"
	}
	return fmt.Sprintf("No API key found for %s.\n\n%s", display, GetProviderLoginHelp())
}

// AgentSessionRuntimeDiagnostic is one non-fatal startup issue.
type AgentSessionRuntimeDiagnostic struct {
	Type    string // "info" | "warning" | "error"
	Message string
}

// CollectSettingsDiagnostics turns recorded settings errors into diagnostics.
func CollectSettingsDiagnostics(settingsManager *SettingsManager) []AgentSessionRuntimeDiagnostic {
	errors := settingsManager.DrainErrors()
	out := make([]AgentSessionRuntimeDiagnostic, 0, len(errors))
	for _, entry := range errors {
		message := fmt.Sprintf("Invalid %s settings: %v", entry.Scope, entry.Error)
		if entry.Path != "" {
			message = fmt.Sprintf("Invalid settings file %s: %v", entry.Path, entry.Error)
		}
		out = append(out, AgentSessionRuntimeDiagnostic{Type: "warning", Message: message})
	}
	return out
}

// DeduplicateDiagnostics removes duplicate type/message diagnostics while
// preserving first occurrence.
func DeduplicateDiagnostics(diagnostics []AgentSessionRuntimeDiagnostic) []AgentSessionRuntimeDiagnostic {
	seen := map[string]bool{}
	out := make([]AgentSessionRuntimeDiagnostic, 0, len(diagnostics))
	for _, diagnostic := range diagnostics {
		key := diagnostic.Type + "\x00" + diagnostic.Message
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, diagnostic)
	}
	return out
}
