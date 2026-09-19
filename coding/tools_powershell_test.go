package coding

import (
	"context"
	"encoding/json"
	"runtime"
	"strings"
	"testing"

	"github.com/dat267/gpi/ai"
)

// Tests keyed to upstream utils/shell.ts, core/tools/powershell.ts, and
// core/tools/index.ts.

func TestGetShellConfigResolution(t *testing.T) {
	config, err := GetShellConfig("")
	if err != nil {
		t.Fatalf("default shell config: %v", err)
	}
	if runtime.GOOS == "windows" {
		if !strings.Contains(strings.ToLower(config.Shell), "bash") {
			t.Fatalf("shell = %s", config.Shell)
		}
	} else if config.Shell != "/bin/bash" && !strings.HasSuffix(config.Shell, "bash") {
		t.Fatalf("shell = %s", config.Shell)
	}
	if len(config.Args) != 1 || config.Args[0] != "-c" {
		t.Fatalf("args = %#v", config.Args)
	}
	if config.CommandTransport != "" {
		t.Fatalf("transport = %s", config.CommandTransport)
	}

	// A missing custom shell path keeps upstream's message.
	if _, err := GetShellConfig("/definitely/missing/bash"); err == nil ||
		!strings.Contains(err.Error(), "Custom shell path not found: /definitely/missing/bash") {
		t.Fatalf("err = %v", err)
	}

	// A legacy WSL bash path switches to stdin transport.
	legacy := `C:\Windows\System32\bash.exe`
	if !isLegacyWslBashPath(legacy) {
		t.Fatalf("legacy WSL path not detected: %s", legacy)
	}
	if isLegacyWslBashPath(`C:\Program Files\Git\bin\bash.exe`) {
		t.Fatal("Git Bash must not use stdin transport")
	}
	config, err = GetShellConfig("/bin/sh")
	if err != nil {
		t.Fatalf("custom shell: %v", err)
	}
	if config.Shell != "/bin/sh" || config.Args[0] != "-c" {
		t.Fatalf("config = %#v", config)
	}
}

func TestGetPowerShellConfigWindowsOnly(t *testing.T) {
	config, err := GetPowerShellConfig()
	if runtime.GOOS != "windows" {
		if err == nil || err.Error() != "The powershell tool is only available on Windows." {
			t.Fatalf("err = %v", err)
		}
		return
	}
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(config.Args) != len(PowerShellArgs) {
		t.Fatalf("args = %#v", config.Args)
	}
	for index, arg := range PowerShellArgs {
		if config.Args[index] != arg {
			t.Fatalf("args = %#v", config.Args)
		}
	}
}

func TestSanitizeBinaryOutput(t *testing.T) {
	input := "ok\tline\nnext\rnull\x00bell\x07format\uFFF9tail"
	got := SanitizeBinaryOutput(input)
	want := "ok\tline\nnext\rnullbellformattail"
	if got != want {
		t.Fatalf("sanitized = %q, want %q", got, want)
	}
	// Escapes are stripped; DEL (0x7f) is above the control-character range
	// upstream filters, so it survives.
	if got := SanitizeBinaryOutput("\x1b[31mred\x1b[0m\x7f"); got != "[31mred[0m\x7f" {
		t.Fatalf("sanitized = %q", got)
	}
}

func TestPowerShellToolMetadataAndExecFailure(t *testing.T) {
	tool := CreatePowerShellTool(t.TempDir(), nil)
	if tool.Name != "powershell" || tool.Label != "powershell" {
		t.Fatalf("tool = %s/%s", tool.Name, tool.Label)
	}
	if !strings.Contains(tool.Description, "PowerShell") {
		t.Fatalf("description = %s", tool.Description)
	}
	if PowerShellShellToolConfig.TempFilePrefix != "pi-powershell" {
		t.Fatalf("prefix = %s", PowerShellShellToolConfig.TempFilePrefix)
	}
	if PowerShellShellToolConfig.Prompt != "PS>" {
		t.Fatalf("prompt = %s", PowerShellShellToolConfig.Prompt)
	}
	if PowerShellToolSystemPromptContribution.Snippet != "Execute PowerShell commands" {
		t.Fatalf("snippet = %s", PowerShellToolSystemPromptContribution.Snippet)
	}
	if len(PowerShellToolSystemPromptContribution.Guidelines) != 1 ||
		PowerShellToolSystemPromptContribution.Guidelines[0] != "You can inspect PI_* environment variables for current model and session details." {
		t.Fatalf("guidelines = %#v", PowerShellToolSystemPromptContribution.Guidelines)
	}
	// The UTF-8 prefix is prepended to every command.
	if got := PowerShellShellToolConfig.TransformCommand("Get-Date"); got != utf8OutputPrefix+"Get-Date" {
		t.Fatalf("transformed = %q", got)
	}
	// The schema is the shared shell schema.
	var schema map[string]any
	if err := json.Unmarshal(tool.Parameters, &schema); err != nil {
		t.Fatalf("schema: %v", err)
	}
	properties, _ := schema["properties"].(map[string]any)
	if _, ok := properties["command"]; !ok {
		t.Fatalf("schema = %s", tool.Parameters)
	}

	// Off Windows, execution fails with the Windows-only error (the tool never
	// falls back to bash).
	if runtime.GOOS != "windows" {
		_, err := tool.Execute("call-1", json.RawMessage(`{"command":"Get-Date"}`), context.Background(), nil)
		if err == nil || err.Error() != "The powershell tool is only available on Windows." {
			t.Fatalf("err = %v", err)
		}
	}
}

func TestBashToolUsesSharedShellConfig(t *testing.T) {
	tool := CreateBashTool(t.TempDir(), nil)
	if tool.Name != "bash" || tool.Label != "bash" {
		t.Fatalf("tool = %s/%s", tool.Name, tool.Label)
	}
	if BashShellToolConfig.TempFilePrefix != "pi-bash" {
		t.Fatalf("prefix = %s", BashShellToolConfig.TempFilePrefix)
	}
	if BashToolSystemPromptContribution.Snippet != "Execute bash commands (ls, grep, find, etc.)" {
		t.Fatalf("snippet = %s", BashShellToolConfig.PromptSnippet)
	}
	// The shared shell tool still runs commands end to end.
	result, err := tool.Execute("call-1", json.RawMessage(`{"command":"echo shared-config"}`), context.Background(), nil)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(result.Content) == 0 {
		t.Fatal("no content")
	}
	if text := result.Content[0].(ai.TextContent).Text; !strings.Contains(text, "shared-config") {
		t.Fatalf("output = %q", text)
	}
}

func TestToolRegistry(t *testing.T) {
	if len(AllToolNames) != 8 {
		t.Fatalf("tool names = %#v", AllToolNames)
	}
	expected := []string{"read", "bash", "powershell", "edit", "write", "grep", "find", "ls"}
	for index, name := range expected {
		if AllToolNames[index] != name {
			t.Fatalf("tool names = %#v", AllToolNames)
		}
		if !IsToolName(name) {
			t.Fatalf("IsToolName(%q) = false", name)
		}
	}
	if IsToolName("unknown") {
		t.Fatal("unknown names must not be tools")
	}

	cwd := t.TempDir()
	for _, name := range AllToolNames {
		tool, err := CreateTool(name, cwd, nil)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if tool.Name != name {
			t.Fatalf("%s: tool name = %s", name, tool.Name)
		}
	}
	if _, err := CreateTool("unknown", cwd, nil); err == nil ||
		!strings.Contains(err.Error(), "Unknown tool name: unknown") {
		t.Fatalf("err = %v", err)
	}

	coding := CreateCodingTools(cwd, nil)
	if len(coding) != 4 {
		t.Fatalf("coding tools = %d", len(coding))
	}
	for index, name := range []string{"read", "bash", "edit", "write"} {
		if coding[index].Name != name {
			t.Fatalf("coding tools = %s at %d", coding[index].Name, index)
		}
	}
	readOnly := CreateReadOnlyTools(cwd, nil)
	if len(readOnly) != 4 {
		t.Fatalf("read-only tools = %d", len(readOnly))
	}
	for index, name := range []string{"read", "grep", "find", "ls"} {
		if readOnly[index].Name != name {
			t.Fatalf("read-only tools = %s at %d", readOnly[index].Name, index)
		}
	}
	all := CreateAllTools(cwd, nil)
	if len(all) != 8 {
		t.Fatalf("all tools = %d", len(all))
	}
	for _, name := range AllToolNames {
		if all[name].Name != name {
			t.Fatalf("all tools[%s] = %s", name, all[name].Name)
		}
	}
}
