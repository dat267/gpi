package coding

import (
	"os"
	"path/filepath"
	"strings"
)

// Port of the resource loader's prompt files (resource-loader.ts):
// discoverSystemPromptFile / discoverAppendSystemPromptFile and resolvePromptInput.
//
// The base system prompt can be replaced by SYSTEM.md and extra instructions
// appended from APPEND_SYSTEM.md, in the project's .pi dir while the project is
// trusted, falling back to the agent dir. The --system-prompt and
// --append-system-prompt flags are the same kind of source (text, or a path to
// read) and take precedence over the files.

// ResolvePromptInput resolves a prompt source: when the input names an existing
// file its contents are the prompt (BOM stripped), otherwise the input is the
// prompt text itself. An unreadable file falls back to the literal input
// (upstream logs a warning there; the port has no console in this path).
func ResolvePromptInput(input string) string {
	if input == "" {
		return ""
	}
	info, err := os.Stat(input)
	if err != nil || info.IsDir() {
		return input
	}
	raw, err := os.ReadFile(input)
	if err != nil {
		return input
	}
	_, content := SplitBom(string(raw))
	return content
}

// DiscoverSystemPromptFile returns the base system prompt file to use: the
// trusted project's <cwd>/.pi/SYSTEM.md, else <agentDir>/SYSTEM.md, else "".
func DiscoverSystemPromptFile(cwd string, agentDir string, projectTrusted bool) string {
	return discoverPromptFile(cwd, agentDir, projectTrusted, "SYSTEM.md")
}

// DiscoverAppendSystemPromptFile returns the append system prompt file to use:
// the trusted project's <cwd>/.pi/APPEND_SYSTEM.md, else
// <agentDir>/APPEND_SYSTEM.md, else "".
func DiscoverAppendSystemPromptFile(cwd string, agentDir string, projectTrusted bool) string {
	return discoverPromptFile(cwd, agentDir, projectTrusted, "APPEND_SYSTEM.md")
}

func discoverPromptFile(cwd string, agentDir string, projectTrusted bool, name string) string {
	if projectTrusted && cwd != "" {
		projectPath := filepath.Join(cwd, ConfigDirName, name)
		if fileExists(projectPath) {
			return projectPath
		}
	}
	if agentDir != "" {
		globalPath := filepath.Join(agentDir, name)
		if fileExists(globalPath) {
			return globalPath
		}
	}
	return ""
}

// PromptFileSources are the session's prompt inputs: the flag sources (nil/empty
// when the flags were not passed) and where to look for the files.
type PromptFileSources struct {
	Cwd            string
	AgentDir       string
	ProjectTrusted bool
	// SystemPrompt is the --system-prompt source (text, or a path to read).
	// A non-nil empty value suppresses file discovery (upstream distinguishes
	// an explicit source from an absent one).
	SystemPrompt *string
	// AppendSystemPrompt are the --append-system-prompt sources.
	AppendSystemPrompt []string
}

// PromptOverrides are the resolved prompt inputs: the prompt text plus the
// source paths that exist (upstream getSystemPromptSource /
// getAppendSystemPromptSources, shown in the loaded-resources Context section).
type PromptOverrides struct {
	// SystemPrompt is the base prompt ("" keeps the built-in preamble).
	SystemPrompt string
	// AppendSystemPrompt is appended to the prompt.
	AppendSystemPrompt string
	// SourcePaths are the resolved files, base prompt first.
	SourcePaths []string
}

// LoadPromptOverrides resolves the base and append prompt text: an explicit
// source wins, otherwise the discovered file is used. Multiple append sources
// join with a blank line (upstream agent-session joins the loader's append
// prompts with "\n\n").
func LoadPromptOverrides(sources PromptFileSources) PromptOverrides {
	overrides := PromptOverrides{}
	if sources.SystemPrompt != nil {
		overrides.SystemPrompt = ResolvePromptInput(*sources.SystemPrompt)
		if path := promptSourcePath(*sources.SystemPrompt); path != "" {
			overrides.SourcePaths = append(overrides.SourcePaths, path)
		}
	} else if file := DiscoverSystemPromptFile(sources.Cwd, sources.AgentDir, sources.ProjectTrusted); file != "" {
		overrides.SystemPrompt = ResolvePromptInput(file)
		overrides.SourcePaths = append(overrides.SourcePaths, file)
	}

	if len(sources.AppendSystemPrompt) > 0 {
		parts := make([]string, 0, len(sources.AppendSystemPrompt))
		for _, source := range sources.AppendSystemPrompt {
			if text := ResolvePromptInput(source); text != "" {
				parts = append(parts, text)
			}
			if path := promptSourcePath(source); path != "" {
				overrides.SourcePaths = append(overrides.SourcePaths, path)
			}
		}
		overrides.AppendSystemPrompt = strings.Join(parts, "\n\n")
		return overrides
	}
	if file := DiscoverAppendSystemPromptFile(sources.Cwd, sources.AgentDir, sources.ProjectTrusted); file != "" {
		overrides.AppendSystemPrompt = ResolvePromptInput(file)
		overrides.SourcePaths = append(overrides.SourcePaths, file)
	}
	return overrides
}

// promptSourcePath returns the absolute source path when the source names an
// existing file (upstream filters its sources on existsSync + resolvePath).
func promptSourcePath(source string) string {
	if source == "" || !fileExists(source) {
		return ""
	}
	absolute, err := filepath.Abs(source)
	if err != nil {
		return filepath.Clean(source)
	}
	return absolute
}
