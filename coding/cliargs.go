package coding

import (
	"fmt"
	"strings"

	"github.com/dat267/pier/ai"
)

// Port of cli/args.ts: CLI argument parsing and help display.

// CLIMode is one output mode.
type CLIMode = string

const (
	CLIModeText CLIMode = "text"
	CLIModeJSON CLIMode = "json"
	CLIModeRPC  CLIMode = "rpc"
)

// AppName is the executable name used by the help text.
const AppName = "pi"

// Args are the parsed CLI arguments (upstream Args).
type Args struct {
	Provider           *string
	Model              *string
	APIKey             *string
	SystemPrompt       *string
	AppendSystemPrompt []string
	Thinking           *ai.ThinkingLevel
	Continue           bool
	Resume             bool
	Help               bool
	Version            bool
	Mode               CLIMode
	Name               *string
	NoSession          bool
	Session            *string
	SessionID          *string
	Fork               *string
	SessionDir         *string
	Models             []string
	Tools              []string
	ExcludeTools       []string
	NoTools            bool
	NoBuiltinTools     bool
	Extensions         []string
	NoExtensions       bool
	Print              bool
	Export             *string
	NoSkills           bool
	Skills             []string
	PromptTemplates    []string
	NoPromptTemplates  bool
	Themes             []string
	UseTheme           *string
	NoThemes           bool
	NoContextFiles     bool
	ListModels         *string
	ListModelsAll      bool
	Offline            bool
	TuiMode            *string
	Verbose            bool
	// ProjectTrustOverride is nil when unset (upstream's optional boolean).
	ProjectTrustOverride *bool
	Messages             []string
	FileArgs             []string
	// UnknownFlags are flags that may belong to extensions.
	UnknownFlags map[string]any
	Diagnostics  []CLIDiagnostic
}

// CLIDiagnostic is one parse diagnostic.
type CLIDiagnostic struct {
	Type    string // "warning" | "error"
	Message string
}

// NormalizeSessionName trims a display name, returning nil for blank input.
func NormalizeSessionName(value string) *string {
	name := strings.TrimSpace(value)
	if name == "" {
		return nil
	}
	return &name
}

// ParseArgs parses CLI arguments (upstream parseArgs).
func ParseArgs(args []string) *Args {
	result := &Args{UnknownFlags: map[string]any{}}

	for i := 0; i < len(args); i++ {
		arg := args[i]
		// next consumes the following argument when present. It never moves the
		// cursor on failure, so callers need no compensation.
		next := func() (string, bool) {
			if i+1 < len(args) {
				i++
				return args[i], true
			}
			return "", false
		}

		switch {
		case arg == "--":
			for _, positional := range args[i+1:] {
				if strings.HasPrefix(positional, "@") {
					result.FileArgs = append(result.FileArgs, strings.TrimPrefix(positional, "@"))
				} else {
					result.Messages = append(result.Messages, positional)
				}
			}
			i = len(args)
		case arg == "--help" || arg == "-h":
			result.Help = true
		case arg == "--version" || arg == "-v":
			result.Version = true
		case arg == "--mode":
			if value, ok := next(); ok {
				if value == CLIModeText || value == CLIModeJSON || value == CLIModeRPC {
					result.Mode = value
				}
			}
		case arg == "--continue" || arg == "-c":
			result.Continue = true
		case arg == "--resume" || arg == "-r":
			result.Resume = true
		case arg == "--provider":
			if value, ok := next(); ok {
				result.Provider = &value
			}
		case arg == "--model":
			if value, ok := next(); ok {
				result.Model = &value
			}
		case arg == "--api-key":
			if value, ok := next(); ok {
				result.APIKey = &value
			}
		case arg == "--system-prompt":
			if value, ok := next(); ok {
				result.SystemPrompt = &value
			}
		case arg == "--append-system-prompt":
			if value, ok := next(); ok {
				result.AppendSystemPrompt = append(result.AppendSystemPrompt, value)
			}
		case arg == "--name" || arg == "-n":
			if value, ok := next(); ok {
				result.Name = &value
			} else {
				result.Diagnostics = append(result.Diagnostics, CLIDiagnostic{Type: "error", Message: "--name requires a value"})
			}
		case arg == "--no-session":
			result.NoSession = true
		case arg == "--session":
			if value, ok := next(); ok {
				result.Session = &value
			}
		case arg == "--session-id":
			if value, ok := next(); ok {
				result.SessionID = &value
			}
		case arg == "--fork":
			if value, ok := next(); ok {
				result.Fork = &value
			}
		case arg == "--session-dir":
			if value, ok := next(); ok {
				result.SessionDir = &value
			}
		case arg == "--models":
			if value, ok := next(); ok {
				result.Models = splitTrimmed(value)
			}
		case arg == "--no-tools" || arg == "-nt":
			result.NoTools = true
		case arg == "--no-builtin-tools" || arg == "-nbt":
			result.NoBuiltinTools = true
		case arg == "--tools" || arg == "-t":
			if value, ok := next(); ok {
				result.Tools = splitNonEmpty(value)
			}
		case arg == "--exclude-tools" || arg == "-xt":
			if value, ok := next(); ok {
				result.ExcludeTools = splitNonEmpty(value)
			}
		case arg == "--thinking":
			if value, ok := next(); ok {
				if IsValidThinkingLevel(value) {
					level := ai.ThinkingLevel(value)
					result.Thinking = &level
				} else {
					result.Diagnostics = append(result.Diagnostics, CLIDiagnostic{
						Type:    "warning",
						Message: fmt.Sprintf("Invalid thinking level %q. Valid values: %s", value, strings.Join(validThinkingLevelStrings(), ", ")),
					})
				}
			}
		case arg == "--print" || arg == "-p":
			result.Print = true
			if i+1 < len(args) {
				candidate := args[i+1]
				if !strings.HasPrefix(candidate, "@") && (!strings.HasPrefix(candidate, "-") || strings.HasPrefix(candidate, "---")) {
					result.Messages = append(result.Messages, candidate)
					i++
				}
			}
		case arg == "--export":
			if value, ok := next(); ok {
				result.Export = &value
			}
		case arg == "--extension" || arg == "-e":
			if value, ok := next(); ok {
				result.Extensions = append(result.Extensions, value)
				result.Diagnostics = append(result.Diagnostics, CLIDiagnostic{
					Type:    "warning",
					Message: "Ignoring " + arg + " " + value + ": this build loads no extensions.",
				})
			} else {
				result.Diagnostics = append(result.Diagnostics, CLIDiagnostic{Type: "error", Message: "--extension requires a value"})
			}
		case arg == "--no-extensions" || arg == "-ne":
			// Accepted and ignored: it asks for fewer extensions, and this build
			// loads none. The flag stays known so it is never mistaken for a
			// message.
			result.NoExtensions = true
		case arg == "--skill":
			if value, ok := next(); ok {
				result.Skills = append(result.Skills, value)
			}
		case arg == "--prompt-template":
			if value, ok := next(); ok {
				result.PromptTemplates = append(result.PromptTemplates, value)
			}
		case arg == "--theme":
			if value, ok := next(); ok {
				result.Themes = append(result.Themes, value)
			}
		case arg == "--use-theme":
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				result.UseTheme = &args[i+1]
				i++
			} else {
				result.Diagnostics = append(result.Diagnostics, CLIDiagnostic{Type: "error", Message: "--use-theme requires a theme name"})
			}
		case arg == "--no-skills" || arg == "-ns":
			result.NoSkills = true
		case arg == "--no-prompt-templates" || arg == "-np":
			result.NoPromptTemplates = true
		case arg == "--no-themes":
			result.NoThemes = true
		case arg == "--no-context-files" || arg == "-nc":
			result.NoContextFiles = true
		case arg == "--list-models":
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") && !strings.HasPrefix(args[i+1], "@") {
				result.ListModels = &args[i+1]
				i++
			} else {
				result.ListModelsAll = true
			}
		case arg == "--tui-mode":
			if i+1 < len(args) {
				value := args[i+1]
				if value == "regular" || value == "fullscreen" {
					result.TuiMode = &value
					i++
				} else if strings.HasPrefix(value, "-") {
					result.Diagnostics = append(result.Diagnostics, CLIDiagnostic{Type: "error", Message: "--tui-mode requires regular or fullscreen"})
				} else {
					i++
					result.Diagnostics = append(result.Diagnostics, CLIDiagnostic{
						Type:    "error",
						Message: fmt.Sprintf("Invalid TUI mode %q. Valid values: regular, fullscreen", value),
					})
				}
			} else {
				result.Diagnostics = append(result.Diagnostics, CLIDiagnostic{Type: "error", Message: "--tui-mode requires regular or fullscreen"})
			}
		case arg == "--verbose":
			result.Verbose = true
		case arg == "--approve" || arg == "-a":
			value := true
			result.ProjectTrustOverride = &value
		case arg == "--no-approve" || arg == "-na":
			value := false
			result.ProjectTrustOverride = &value
		case arg == "--offline":
			result.Offline = true
		case strings.HasPrefix(arg, "@"):
			result.FileArgs = append(result.FileArgs, strings.TrimPrefix(arg, "@"))
		case strings.HasPrefix(arg, "--"):
			if eqIndex := strings.Index(arg, "="); eqIndex != -1 {
				result.UnknownFlags[arg[2:eqIndex]] = arg[eqIndex+1:]
			} else {
				flagName := arg[2:]
				if i+1 < len(args) {
					candidate := args[i+1]
					if !strings.HasPrefix(candidate, "-") && !strings.HasPrefix(candidate, "@") {
						result.UnknownFlags[flagName] = candidate
						i++
						continue
					}
				}
				result.UnknownFlags[flagName] = true
			}
		case strings.HasPrefix(arg, "-"):
			result.Diagnostics = append(result.Diagnostics, CLIDiagnostic{Type: "error", Message: "Unknown option: " + arg})
		default:
			result.Messages = append(result.Messages, arg)
		}
	}

	return result
}

func splitTrimmed(value string) []string {
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		out = append(out, strings.TrimSpace(part))
	}
	return out
}

func splitNonEmpty(value string) []string {
	parts := strings.Split(value, ",")
	out := []string{}
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func validThinkingLevelStrings() []string {
	out := make([]string, 0, len(ThinkingLevelOptions))
	for _, level := range ThinkingLevelOptions {
		out = append(out, string(level))
	}
	return out
}

// PrintHelp returns the CLI help text (upstream printHelp without ANSI color).
//
// Upstream's Commands table lists install/remove/uninstall/update/list/config/
// auth. Only `auth` is implemented here: the package manager and the
// resource-manager TUI are out of scope (D41), so those lines are gone rather
// than advertising commands that silently turn into the first message to the
// model.
//
// The extension flag lines are gone for the same reason: -e/--extension loads
// nothing here, and --no-extensions asks for less of something that does not
// exist. Both stay parsed (an -e path must not become a message) and -e reports
// that it is ignored. The remaining option lines are upstream's, minus the
// extension claims in the tool descriptions.
func PrintHelp() string { return PrintHelpNamed(AppName) }

// PrintHelpNamed renders the help text with the invoked binary name.
func PrintHelpNamed(appName string) string {
	if appName == "" {
		appName = AppName
	}
	var builder strings.Builder
	builder.WriteString(appName + " - AI coding assistant with read, bash, edit, write tools\n\n")
	builder.WriteString("Usage:\n  " + appName + " [options] [--] [@files...] [messages...]\n\n")
	builder.WriteString("Commands:\n")
	builder.WriteString("  " + appName + " auth <command>            Print credentials or check provider readiness\n")
	builder.WriteString("  " + appName + " auth --help              Show help for auth commands\n\n")
	builder.WriteString("Options:\n")
	for _, line := range helpOptionLines() {
		builder.WriteString(line + "\n")
	}
	builder.WriteString("\nBuilt-in Tool Names:\n")
	for _, line := range helpToolLines() {
		builder.WriteString(line + "\n")
	}
	return builder.String()
}

func helpOptionLines() []string {
	lines := []string{
		"  --provider <name>              Provider name (default: google)",
		"  --model <pattern>              Model pattern or ID (supports \"provider/id\" and optional \":<thinking>\")",
		"  --api-key <key>                API key (defaults to env vars)",
		"  --system-prompt <text>         System prompt (default: coding assistant prompt)",
		"  --append-system-prompt <text>  Append text or file contents to the system prompt (can be used multiple times)",
		"  --mode <mode>                  Output mode: text (default), json, or rpc",
		"  --print, -p                    Non-interactive mode: process prompt and exit",
		"  --continue, -c                 Continue previous session",
		"  --resume, -r                   Select a session to resume",
		"  --session <path|id>            Use specific session file or partial UUID",
		"  --session-id <id>              Use exact project session ID, creating it if missing",
		"  --fork <path|id>               Fork specific session file or partial UUID into a new session",
		"  --session-dir <dir>            Directory for session storage and lookup",
		"  --no-session                   Don't save session (ephemeral)",
		"  --name, -n <name>              Set session display name",
		"  --models <patterns>            Comma-separated model patterns for Ctrl+P cycling",
		"                                 Supports globs (anthropic/*, *sonnet*) and fuzzy matching",
		"  --no-tools, -nt                Disable all tools by default",
		"  --no-builtin-tools, -nbt       Disable built-in tools by default but keep custom tools enabled",
		"  --tools, -t <tools>            Comma-separated allowlist of tool names to enable",
		"                                 Applies to built-in and custom tools",
		"  --exclude-tools, -xt <tools>   Comma-separated denylist of tool names to disable",
		"                                 Applies to built-in and custom tools",
		"  --thinking <level>             Set thinking level: " + strings.Join(validThinkingLevelStrings(), ", "),
		"  --skill <path>                 Load a skill file or directory (can be used multiple times)",
		"  --no-skills, -ns               Disable skills discovery and loading",
		"  --prompt-template <path>       Load a prompt template file or directory (can be used multiple times)",
		"  --no-prompt-templates, -np     Disable prompt template discovery and loading",
		"  --theme <path>                 Load a theme file or directory (can be used multiple times)",
		"  --use-theme <name[/name]>      Set the initial interactive theme for this run",
		"  --no-themes                    Disable theme discovery and loading",
		"  --no-context-files, -nc        Disable AGENTS.md and CLAUDE.md discovery and loading",
		"  --export <file>                Export session file to HTML and exit",
		"  --list-models [search]         List available models (with optional fuzzy search)",
		"  --verbose                      Force verbose startup (overrides quietStartup setting)",
		"  --tui-mode <mode>              TUI mode: regular (default) or fullscreen",
		"  --approve, -a                  Trust project-local files for this run",
		"  --no-approve, -na              Ignore project-local files for this run",
		"  --offline                      Disable startup network operations (same as PI_OFFLINE=1)",
		"  --                             End option parsing; treat remaining arguments as messages/files",
		"  --help, -h                     Show this help",
		"  --version, -v                  Show version number",
	}
	return lines
}

func helpToolLines() []string {
	return []string{
		"  read       - Read file contents",
		"  bash       - Execute bash commands",
		"  powershell - Execute PowerShell commands on Windows",
		"  edit       - Edit files with find/replace",
		"  write      - Write files (creates/overwrites)",
		"  grep       - Search file contents (read-only, off by default)",
		"  find       - Find files by glob pattern (read-only, off by default)",
		"  ls         - List directory contents (read-only, off by default)",
	}
}

// ToolSelection is the session tool configuration a CLI invocation implies.
type ToolSelection struct {
	// Tools is the allowlist (nil when the flag was absent).
	Tools []ToolName
	// ExcludeTools is the denylist (nil when the flag was absent).
	ExcludeTools []ToolName
	// NoTools is NoToolsAll, NoToolsBuiltin, or "" when neither flag was given.
	NoTools string
}

// ToolSelection projects the tool flags onto the session's options. Upstream's
// main.ts checks --no-tools before --no-builtin-tools, so the former wins when
// both are passed, and the allow and deny lists pass through unchanged.
func (a *Args) ToolSelection() ToolSelection {
	var selection ToolSelection
	if a == nil {
		return selection
	}
	switch {
	case a.NoTools:
		selection.NoTools = NoToolsAll
	case a.NoBuiltinTools:
		selection.NoTools = NoToolsBuiltin
	}
	if a.Tools != nil {
		selection.Tools = append([]ToolName{}, a.Tools...)
	}
	if a.ExcludeTools != nil {
		selection.ExcludeTools = append([]ToolName{}, a.ExcludeTools...)
	}
	return selection
}
