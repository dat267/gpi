package cmd

import (
	"github.com/dat267/pier/coding"
	"github.com/dat267/pier/coding/interactive"
)

// Project trust at startup (upstream main.ts around the runtime factory).
//
// Upstream reads global settings through a bootstrap manager whose project
// settings are gated off, then resolves trust per cwd — the CLI override first,
// then the store's decision, then the defaultProjectTrust setting, and finally
// the startup prompt — and builds the runtime settings manager with the answer.
// Trust decided nothing at all here before: SettingsManagerCreateOptions
// defaults ProjectTrusted to true, so every project counted as trusted and its
// .pi settings, skills, prompts, themes and prompt files were loaded without a
// decision.

// startupTrustOptions are the inputs of resolveStartupProjectTrust.
type startupTrustOptions struct {
	cwd      string
	agentDir string
	// override is --approve / --no-approve: it settles trust for the run without
	// consulting or updating the store (upstream projectTrustOverride).
	override *bool
	// bootstrap reads the global settings, which is where defaultProjectTrust and
	// the terminal flags come from before any project resource is readable.
	bootstrap *coding.SettingsManager
	// hasUI reports whether the startup prompt can ask a question.
	hasUI bool
	// prompt is the startup prompt (nil when there is no UI).
	prompt func(prompt string, options []string) (string, error)
}

// resolveStartupProjectTrust decides whether the project's .pi settings and
// resources may be loaded. It returns an error only when the trust store cannot
// be read; a cancelled prompt is simply an untrusted project.
func resolveStartupProjectTrust(options startupTrustOptions) (bool, error) {
	context := coding.ProjectTrustContext{HasUI: options.hasUI, Select: options.prompt}
	return coding.ResolveProjectTrusted(coding.ResolveProjectTrustedOptions{
		Cwd:                 options.cwd,
		TrustStore:          coding.NewProjectTrustStore(options.agentDir),
		TrustOverride:       options.override,
		DefaultProjectTrust: options.bootstrap.GetDefaultProjectTrust(),
		ProjectTrustContext: context,
	})
}

// startupTrustPrompt asks the trust question on the startup screen, before the
// app's TUI owns the terminal.
func startupTrustPrompt(bootstrap *coding.SettingsManager) func(string, []string) (string, error) {
	return func(prompt string, options []string) (string, error) {
		label, ok := interactive.ShowStartupSelector(
			interactive.StartupSelectorOptions{Settings: bootstrap}, prompt, options)
		if !ok {
			// Cancelled: no decision, which the resolver reads as untrusted.
			return "", nil
		}
		return label, nil
	}
}
