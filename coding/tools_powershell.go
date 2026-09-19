package coding

import (
	"github.com/dat267/gpi/agent"
)

// Port of core/tools/powershell.ts.

// utf8OutputPrefix forces UTF-8 console output inside PowerShell.
const utf8OutputPrefix = "try { [Console]::OutputEncoding=[System.Text.Encoding]::UTF8 } catch {}\n"

// PowerShellToolSystemPromptContribution is the powershell prompt
// contribution (upstream powershellToolSystemPromptContribution).
var PowerShellToolSystemPromptContribution = struct {
	Snippet    string
	Guidelines []string
}{
	Snippet:    "Execute PowerShell commands",
	Guidelines: []string{"You can inspect PI_* environment variables for current model and session details."},
}

// PowerSShellToolConfig is the PowerShell variant of the shell tool.
//
// PowerShellOptions is the Go analogue of upstream's
// createLocalPowerShellOperations: the resolved shell config plus the UTF-8
// command prefix.
var PowerShellShellToolConfig = ShellToolConfig{
	Name:             "powershell",
	Label:            "powershell",
	ShellName:        "PowerShell",
	Prompt:           "PS>",
	PromptSnippet:    PowerShellToolSystemPromptContribution.Snippet,
	PromptGuidelines: PowerShellToolSystemPromptContribution.Guidelines,
	TempFilePrefix:   "pi-powershell",
	ResolveShell: func(shellPath string) (ShellConfig, error) {
		return GetPowerShellConfig()
	},
	TransformCommand: func(command string) string {
		return utf8OutputPrefix + command
	},
}

// CreatePowerShellTool builds the PowerShell tool (upstream
// createPowerShellTool). Shell resolution happens at exec time, so creating the
// tool off Windows succeeds and the execution fails with the Windows-only
// error, matching upstream.
func CreatePowerShellTool(cwd string, options *BashToolOptions) agent.AgentTool {
	// The powershell variant ignores the bash shellPath override: it always
	// resolves PowerShell (upstream createLocalPowerShellOperations).
	localized := BashToolOptions{}
	if options != nil {
		localized = *options
		localized.ShellPath = ""
	}
	return CreateShellTool(cwd, PowerShellShellToolConfig, &localized)
}
