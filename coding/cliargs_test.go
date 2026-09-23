package coding

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/dat267/pier/agent"
	"github.com/dat267/pier/ai"
)

// Tests ported from packages/coding-agent/test/args.test.ts, plus the
// json-event contract from modes/json-event.ts.

func TestParseArgsVersionAndHelp(t *testing.T) {
	if result := ParseArgs([]string{"--version"}); !result.Version {
		t.Fatal("--version")
	}
	if result := ParseArgs([]string{"-v"}); !result.Version {
		t.Fatal("-v")
	}
	result := ParseArgs([]string{"--version", "--help", "some message"})
	if !result.Version || !result.Help || len(result.Messages) != 1 || result.Messages[0] != "some message" {
		t.Fatalf("result = %+v", result)
	}
	if result := ParseArgs([]string{"--help"}); !result.Help {
		t.Fatal("--help")
	}
	if result := ParseArgs([]string{"-h"}); !result.Help {
		t.Fatal("-h")
	}
}

func TestParseArgsPrint(t *testing.T) {
	if result := ParseArgs([]string{"--print"}); !result.Print {
		t.Fatal("--print")
	}
	if result := ParseArgs([]string{"-p"}); !result.Print {
		t.Fatal("-p")
	}
	// A prompt starting with YAML frontmatter is still consumed as the prompt.
	prompt := "---\ntitle: hello\n---\nSay hi."
	result := ParseArgs([]string{"-p", prompt})
	if !result.Print || len(result.Messages) != 1 || result.Messages[0] != prompt || len(result.UnknownFlags) != 0 {
		t.Fatalf("result = %+v", result)
	}
	// Options after -p are not consumed as prompts.
	result = ParseArgs([]string{"-p", "--provider", "openai", "Say hi."})
	if !result.Print || result.Provider == nil || *result.Provider != "openai" ||
		len(result.Messages) != 1 || result.Messages[0] != "Say hi." {
		t.Fatalf("result = %+v", result)
	}
}

func TestParseArgsValueFlags(t *testing.T) {
	if result := ParseArgs([]string{"--provider", "openai"}); result.Provider == nil || *result.Provider != "openai" {
		t.Fatal("--provider")
	}
	if result := ParseArgs([]string{"--model", "gpt-4o"}); result.Model == nil || *result.Model != "gpt-4o" {
		t.Fatal("--model")
	}
	if result := ParseArgs([]string{"--api-key", "sk-test-key"}); result.APIKey == nil || *result.APIKey != "sk-test-key" {
		t.Fatal("--api-key")
	}
	if result := ParseArgs([]string{"--system-prompt", "You are a helpful assistant"}); result.SystemPrompt == nil ||
		*result.SystemPrompt != "You are a helpful assistant" {
		t.Fatal("--system-prompt")
	}
	if result := ParseArgs([]string{"--append-system-prompt", "Additional context"}); len(result.AppendSystemPrompt) != 1 ||
		result.AppendSystemPrompt[0] != "Additional context" {
		t.Fatal("--append-system-prompt")
	}
	if result := ParseArgs([]string{"--append-system-prompt", "Context A", "--append-system-prompt", "Context B"}); len(result.AppendSystemPrompt) != 2 {
		t.Fatalf("append = %#v", result.AppendSystemPrompt)
	}
	if result := ParseArgs([]string{"--mode", "json"}); result.Mode != CLIModeJSON {
		t.Fatal("--mode json")
	}
	if result := ParseArgs([]string{"--mode", "rpc"}); result.Mode != CLIModeRPC {
		t.Fatal("--mode rpc")
	}
	if result := ParseArgs([]string{"--session", "/path/to/session.jsonl"}); result.Session == nil ||
		*result.Session != "/path/to/session.jsonl" {
		t.Fatal("--session")
	}
	if result := ParseArgs([]string{"--session-id", "orchestrated-session"}); result.SessionID == nil ||
		*result.SessionID != "orchestrated-session" {
		t.Fatal("--session-id")
	}
	if result := ParseArgs([]string{"--fork", "1234abcd"}); result.Fork == nil || *result.Fork != "1234abcd" ||
		len(result.Messages) != 0 {
		t.Fatalf("--fork = %+v", result)
	}
	if result := ParseArgs([]string{"--export", "session.jsonl"}); result.Export == nil || *result.Export != "session.jsonl" {
		t.Fatal("--export")
	}
	if result := ParseArgs([]string{"--thinking", "high"}); result.Thinking == nil || string(*result.Thinking) != "high" {
		t.Fatal("--thinking")
	}
	if result := ParseArgs([]string{"--models", "gpt-4o,claude-sonnet,gemini-pro"}); len(result.Models) != 3 ||
		result.Models[0] != "gpt-4o" || result.Models[2] != "gemini-pro" {
		t.Fatalf("--models = %#v", result.Models)
	}
}

func TestParseArgsNameAndSessions(t *testing.T) {
	if result := ParseArgs([]string{"--name", "my-session"}); result.Name == nil || *result.Name != "my-session" {
		t.Fatal("--name")
	}
	if result := ParseArgs([]string{"-n", "quick-session"}); result.Name == nil || *result.Name != "quick-session" {
		t.Fatal("-n")
	}
	// Empty values are preserved for main-level validation.
	if result := ParseArgs([]string{"--name", ""}); result.Name == nil || *result.Name != "" {
		t.Fatalf("name = %v", result.Name)
	}
	if name := NormalizeSessionName("  named session  "); name == nil || *name != "named session" {
		t.Fatalf("normalized = %v", name)
	}
	if name := NormalizeSessionName("   "); name != nil {
		t.Fatalf("normalized = %v", name)
	}
	result := ParseArgs([]string{"--name"})
	if len(result.Diagnostics) != 1 || result.Diagnostics[0].Type != "error" || result.Diagnostics[0].Message != "--name requires a value" {
		t.Fatalf("diagnostics = %+v", result.Diagnostics)
	}
	result = ParseArgs([]string{"--name", "named-run", "--print", "--model", "gpt-4o", "hello"})
	if result.Name == nil || *result.Name != "named-run" || !result.Print || result.Model == nil || *result.Model != "gpt-4o" ||
		len(result.Messages) != 1 || result.Messages[0] != "hello" {
		t.Fatalf("result = %+v", result)
	}
	if result := ParseArgs([]string{"--no-session"}); !result.NoSession {
		t.Fatal("--no-session")
	}
	// Custom session ids survive non-persisting commands.
	result = ParseArgs([]string{"--session-id", "ephemeral-id", "--help"})
	if result.SessionID == nil || *result.SessionID != "ephemeral-id" || !result.Help {
		t.Fatalf("result = %+v", result)
	}
	result = ParseArgs([]string{"--session-id", "ephemeral-id", "--list-models"})
	if result.SessionID == nil || !result.ListModelsAll {
		t.Fatalf("result = %+v", result)
	}
	result = ParseArgs([]string{"--session-id", "ephemeral-id", "--no-session"})
	if result.SessionID == nil || !result.NoSession {
		t.Fatalf("result = %+v", result)
	}
}

func TestParseArgsResources(t *testing.T) {
	if result := ParseArgs([]string{"--extension", "./my-extension.ts"}); len(result.Extensions) != 1 ||
		result.Extensions[0] != "./my-extension.ts" {
		t.Fatal("--extension")
	}
	if result := ParseArgs([]string{"-e", "./my-extension.ts"}); len(result.Extensions) != 1 {
		t.Fatal("-e")
	}
	if result := ParseArgs([]string{"--extension", "./ext1.ts", "-e", "./ext2.ts"}); len(result.Extensions) != 2 ||
		result.Extensions[1] != "./ext2.ts" {
		t.Fatal("multiple -e")
	}
	if result := ParseArgs([]string{"--no-extensions"}); !result.NoExtensions {
		t.Fatal("--no-extensions")
	}
	if result := ParseArgs([]string{"--no-extensions", "-e", "foo.ts", "-e", "bar.ts"}); !result.NoExtensions ||
		len(result.Extensions) != 2 {
		t.Fatal("--no-extensions with -e")
	}
	if result := ParseArgs([]string{"--skill", "./skill-dir"}); len(result.Skills) != 1 {
		t.Fatal("--skill")
	}
	if result := ParseArgs([]string{"--skill", "./skill-a", "--skill", "./skill-b"}); len(result.Skills) != 2 {
		t.Fatal("multiple --skill")
	}
	if result := ParseArgs([]string{"--prompt-template", "./prompts"}); len(result.PromptTemplates) != 1 {
		t.Fatal("--prompt-template")
	}
	if result := ParseArgs([]string{"--prompt-template", "./one", "--prompt-template", "./two"}); len(result.PromptTemplates) != 2 {
		t.Fatal("multiple --prompt-template")
	}
	if result := ParseArgs([]string{"--theme", "./theme.json"}); len(result.Themes) != 1 {
		t.Fatal("--theme")
	}
	if result := ParseArgs([]string{"--theme", "./dark.json", "--theme", "./light.json"}); len(result.Themes) != 2 {
		t.Fatal("multiple --theme")
	}
	if result := ParseArgs([]string{"--use-theme", "light"}); result.UseTheme == nil || *result.UseTheme != "light" {
		t.Fatal("--use-theme")
	}
	result := ParseArgs([]string{"--use-theme", "--print"})
	if result.UseTheme != nil || !result.Print || len(result.Diagnostics) != 1 ||
		result.Diagnostics[0].Message != "--use-theme requires a theme name" {
		t.Fatalf("result = %+v", result)
	}
	if result := ParseArgs([]string{"--no-skills"}); !result.NoSkills {
		t.Fatal("--no-skills")
	}
	if result := ParseArgs([]string{"--no-prompt-templates"}); !result.NoPromptTemplates {
		t.Fatal("--no-prompt-templates")
	}
	if result := ParseArgs([]string{"--no-themes"}); !result.NoThemes {
		t.Fatal("--no-themes")
	}
	if result := ParseArgs([]string{"--no-context-files"}); !result.NoContextFiles {
		t.Fatal("--no-context-files")
	}
	if result := ParseArgs([]string{"-nc"}); !result.NoContextFiles {
		t.Fatal("-nc")
	}
}

func TestParseArgsTrustAndModes(t *testing.T) {
	if result := ParseArgs([]string{"--approve"}); result.ProjectTrustOverride == nil || !*result.ProjectTrustOverride {
		t.Fatal("--approve")
	}
	if result := ParseArgs([]string{"-a"}); result.ProjectTrustOverride == nil || !*result.ProjectTrustOverride {
		t.Fatal("-a")
	}
	if result := ParseArgs([]string{"--no-approve"}); result.ProjectTrustOverride == nil || *result.ProjectTrustOverride {
		t.Fatal("--no-approve")
	}
	if result := ParseArgs([]string{"-na"}); result.ProjectTrustOverride == nil || *result.ProjectTrustOverride {
		t.Fatal("-na")
	}
	if result := ParseArgs([]string{"--verbose"}); !result.Verbose {
		t.Fatal("--verbose")
	}
	if result := ParseArgs([]string{"--offline"}); !result.Offline {
		t.Fatal("--offline")
	}
	for _, mode := range []string{"regular", "fullscreen"} {
		result := ParseArgs([]string{"--tui-mode", mode})
		if result.TuiMode == nil || *result.TuiMode != mode {
			t.Fatalf("--tui-mode %s = %+v", mode, result)
		}
	}
	result := ParseArgs([]string{"--tui-mode", "other"})
	if len(result.Diagnostics) != 1 ||
		result.Diagnostics[0].Message != `Invalid TUI mode "other". Valid values: regular, fullscreen` {
		t.Fatalf("diagnostics = %+v", result.Diagnostics)
	}
	result = ParseArgs([]string{"--tui-mode"})
	if len(result.Diagnostics) != 1 || result.Diagnostics[0].Message != "--tui-mode requires regular or fullscreen" {
		t.Fatalf("diagnostics = %+v", result.Diagnostics)
	}
	// The old --ui-mode flag is unknown and captured for extensions.
	result = ParseArgs([]string{"--ui-mode", "fullscreen"})
	if result.TuiMode != nil || result.UnknownFlags["ui-mode"] != "fullscreen" {
		t.Fatalf("result = %+v", result)
	}
}

func TestParseArgsToolsAndMessages(t *testing.T) {
	if result := ParseArgs([]string{"--no-tools"}); !result.NoTools {
		t.Fatal("--no-tools")
	}
	if result := ParseArgs([]string{"-nt"}); !result.NoTools {
		t.Fatal("-nt")
	}
	if result := ParseArgs([]string{"--no-builtin-tools"}); !result.NoBuiltinTools {
		t.Fatal("--no-builtin-tools")
	}
	if result := ParseArgs([]string{"-nbt"}); !result.NoBuiltinTools {
		t.Fatal("-nbt")
	}
	if result := ParseArgs([]string{"--tools", "read,bash"}); len(result.Tools) != 2 || result.Tools[1] != "bash" {
		t.Fatal("--tools")
	}
	if result := ParseArgs([]string{"-t", "read,bash"}); len(result.Tools) != 2 {
		t.Fatal("-t")
	}
	if result := ParseArgs([]string{"--exclude-tools", "read,bash"}); len(result.ExcludeTools) != 2 {
		t.Fatal("--exclude-tools")
	}
	if result := ParseArgs([]string{"-xt", "read,bash"}); len(result.ExcludeTools) != 2 {
		t.Fatal("-xt")
	}
	result := ParseArgs([]string{"--no-tools", "--tools", "read,bash"})
	if !result.NoTools || len(result.Tools) != 2 {
		t.Fatalf("result = %+v", result)
	}
	result = ParseArgs([]string{"--no-builtin-tools", "--tools", "read,bash"})
	if !result.NoBuiltinTools || len(result.Tools) != 2 {
		t.Fatalf("result = %+v", result)
	}

	// Messages, @files, and the -- separator.
	result = ParseArgs([]string{"hello", "world"})
	if len(result.Messages) != 2 || result.Messages[0] != "hello" {
		t.Fatalf("messages = %#v", result.Messages)
	}
	result = ParseArgs([]string{"@README.md", "@src/main.ts"})
	if len(result.FileArgs) != 2 || result.FileArgs[0] != "README.md" || result.FileArgs[1] != "src/main.ts" {
		t.Fatalf("fileArgs = %#v", result.FileArgs)
	}
	result = ParseArgs([]string{"@file.txt", "explain this", "@image.png"})
	if len(result.FileArgs) != 2 || len(result.Messages) != 1 || result.Messages[0] != "explain this" {
		t.Fatalf("result = %+v", result)
	}
	result = ParseArgs([]string{"-p", "--", "- Summarize these points"})
	if len(result.Messages) != 1 || result.Messages[0] != "- Summarize these points" {
		t.Fatalf("messages = %#v", result.Messages)
	}
	result = ParseArgs([]string{"--", "@prompt.md", "a message"})
	if len(result.FileArgs) != 1 || len(result.Messages) != 1 {
		t.Fatalf("result = %+v", result)
	}

	// Unknown flags are captured for extensions.
	result = ParseArgs([]string{"--unknown-flag", "message"})
	if len(result.Messages) != 0 || result.UnknownFlags["unknown-flag"] != "message" {
		t.Fatalf("result = %+v", result)
	}
	result = ParseArgs([]string{"--unknown-flag"})
	if result.UnknownFlags["unknown-flag"] != true {
		t.Fatalf("result = %+v", result)
	}
	result = ParseArgs([]string{"--unknown-flag=value"})
	if result.UnknownFlags["unknown-flag"] != "value" {
		t.Fatalf("result = %+v", result)
	}
	// Unknown short flags are errors.
	result = ParseArgs([]string{"-z"})
	if len(result.Diagnostics) != 1 || result.Diagnostics[0].Type != "error" || result.Diagnostics[0].Message != "Unknown option: -z" {
		t.Fatalf("diagnostics = %+v", result.Diagnostics)
	}

	// Complex combination.
	result = ParseArgs([]string{
		"--provider", "anthropic", "--model", "claude-sonnet", "--print", "--thinking", "high", "@prompt.md", "Do the task",
	})
	if result.Provider == nil || *result.Provider != "anthropic" || result.Model == nil || *result.Model != "claude-sonnet" ||
		!result.Print || result.Thinking == nil || string(*result.Thinking) != "high" ||
		len(result.FileArgs) != 1 || result.FileArgs[0] != "prompt.md" ||
		len(result.Messages) != 1 || result.Messages[0] != "Do the task" {
		t.Fatalf("result = %+v", result)
	}
	// An invalid thinking level is a warning naming the valid values.
	result = ParseArgs([]string{"--thinking", "sometimes"})
	if result.Thinking != nil || len(result.Diagnostics) != 1 ||
		!strings.Contains(result.Diagnostics[0].Message, `Invalid thinking level "sometimes"`) ||
		!strings.Contains(result.Diagnostics[0].Message, "off, minimal, low, medium, high, xhigh, max") {
		t.Fatalf("diagnostics = %+v", result.Diagnostics)
	}
}

func TestPrintHelpContent(t *testing.T) {
	help := PrintHelp()
	for _, needle := range []string{
		"pi - AI coding assistant",
		"Usage:",
		"pi [options] [--] [@files...] [messages...]",
		"--provider <name>",
		"--append-system-prompt <text>",
		"--use-theme <name[/name]>",
		"--tui-mode <mode>              TUI mode: regular (default) or fullscreen",
		"--list-models [search]",
		"--thinking <level>             Set thinking level: off, minimal, low, medium, high, xhigh, max",
		"read       - Read file contents",
		"powershell - Execute PowerShell commands on Windows",
		"ls         - List directory contents (read-only, off by default)",
	} {
		if !strings.Contains(help, needle) {
			t.Fatalf("help is missing %q", needle)
		}
	}
	// No ANSI color codes in the Go help text.
	if strings.Contains(help, "\x1b[") {
		t.Fatal("help must not contain ANSI escapes")
	}
}

func TestToJSONEventStripsPartials(t *testing.T) {
	partial := &ai.AssistantMessage{
		Content: ai.ContentList{
			ai.TextContent{Text: "hi"},
			ai.ToolCall{ID: "call-1", Name: "read", Arguments: json.RawMessage(`{"path":"a.txt"}`)},
		},
		StopReason: "toolUse",
	}
	usage := ai.Usage{Input: 10, Output: 20, CacheRead: 1, CacheWrite: 2, TotalTokens: 33}

	// A toolcall_start delta becomes {type, contentIndex, id, toolName}.
	event := &SessionEvent{
		Type: SessionMessageUpdate,
		Agent: &agent.AgentEvent{
			Message: &ai.AssistantMessage{Usage: usage, StopReason: "toolUse"},
			AssistantMessageEvent: &ai.AssistantMessageEvent{
				Type: ai.EventToolcallStart, ContentIndex: 1, Partial: partial,
			},
		},
	}
	value, err := ToJSONEvent(event)
	if err != nil {
		t.Fatal(err)
	}
	if value["type"] != "message_update" {
		t.Fatalf("type = %v", value["type"])
	}
	delta, ok := value["assistantMessageEvent"].(map[string]any)
	if !ok || delta["type"] != ai.EventToolcallStart || delta["contentIndex"] != 1 ||
		delta["id"] != "call-1" || delta["toolName"] != "read" {
		t.Fatalf("delta = %#v", value["assistantMessageEvent"])
	}
	if _, hasPartial := delta["partial"]; hasPartial {
		t.Fatal("partial must be stripped")
	}
	// Usage stays available because its size is constant.
	usageValue, ok := value["usage"].(map[string]any)
	if !ok || usageValue["input"] != int64(10) || usageValue["totalTokens"] != int64(33) {
		t.Fatalf("usage = %#v", value["usage"])
	}
	cost, _ := usageValue["cost"].(map[string]any)
	if cost == nil {
		t.Fatalf("cost = %#v", usageValue["cost"])
	}

	// A text_delta drops partial too.
	event = &SessionEvent{
		Type: SessionMessageUpdate,
		Agent: &agent.AgentEvent{
			Message: &ai.AssistantMessage{Usage: usage},
			AssistantMessageEvent: &ai.AssistantMessageEvent{
				Type: ai.EventTextDelta, ContentIndex: 0, Delta: "wor", Partial: partial,
			},
		},
	}
	value, err = ToJSONEvent(event)
	if err != nil {
		t.Fatal(err)
	}
	delta, _ = value["assistantMessageEvent"].(map[string]any)
	if delta["type"] != ai.EventTextDelta || delta["delta"] != "wor" {
		t.Fatalf("delta = %#v", delta)
	}
	if _, hasPartial := delta["partial"]; hasPartial {
		t.Fatal("partial must be stripped")
	}

	// toolcall_start with a non-tool-call content index fails.
	event.Agent.AssistantMessageEvent = &ai.AssistantMessageEvent{
		Type: ai.EventToolcallStart, ContentIndex: 0, Partial: partial,
	}
	if _, err := ToJSONEvent(event); err == nil ||
		!strings.Contains(err.Error(), "toolcall_start content at index 0 is not a tool call") {
		t.Fatalf("err = %v", err)
	}

	// A non-assistant message_update is rejected.
	event = &SessionEvent{
		Type:  SessionMessageUpdate,
		Agent: &agent.AgentEvent{Message: &ai.UserMessage{Content: ai.StringOrBlocks{Text: "hi"}}},
	}
	if _, err := ToJSONEvent(event); err == nil ||
		!strings.Contains(err.Error(), "message_update message is not an assistant message") {
		t.Fatalf("err = %v", err)
	}
}

func TestToJSONEventPassesThroughOtherEvents(t *testing.T) {
	// Non-streaming events keep their payload shape.
	event := &SessionEvent{
		Type: SessionToolExecutionStart,
		Agent: &agent.AgentEvent{
			ToolCallID: "call-1", ToolName: "read", Args: json.RawMessage(`{"path":"a.txt"}`),
		},
	}
	value, err := ToJSONEvent(event)
	if err != nil {
		t.Fatal(err)
	}
	if value["type"] != SessionToolExecutionStart || value["toolCallId"] != "call-1" ||
		value["toolName"] != "read" {
		t.Fatalf("value = %#v", value)
	}
	args, ok := value["args"].(map[string]any)
	if !ok || args["path"] != "a.txt" {
		t.Fatalf("args = %#v", value["args"])
	}

	event = &SessionEvent{Type: SessionQueueUpdate, Steering: []string{"a"}, FollowUp: []string{"b"}}
	value, err = ToJSONEvent(event)
	if err != nil {
		t.Fatal(err)
	}
	steering, _ := value["steering"].([]any)
	if len(steering) != 1 || steering[0] != "a" {
		t.Fatalf("value = %#v", value)
	}

	event = &SessionEvent{Type: SessionAutoRetryStart, Attempt: 2, MaxAttempts: 3, DelayMS: 4000, ErrorMessage: "boom"}
	value, err = ToJSONEvent(event)
	if err != nil {
		t.Fatal(err)
	}
	if value["attempt"] != 2 || value["maxAttempts"] != 3 || value["delayMs"] != int64(4000) || value["errorMessage"] != "boom" {
		t.Fatalf("value = %#v", value)
	}

	event = &SessionEvent{Type: SessionCompactionEnd, Reason: CompactionOverflow, Aborted: true}
	value, err = ToJSONEvent(event)
	if err != nil {
		t.Fatal(err)
	}
	if value["reason"] != CompactionOverflow || value["aborted"] != true {
		t.Fatalf("value = %#v", value)
	}

	event = &SessionEvent{Type: SessionThinkingLevelChanged, Level: ai.ThinkHigh}
	value, err = ToJSONEvent(event)
	if err != nil {
		t.Fatal(err)
	}
	if value["level"] != ai.ThinkHigh {
		t.Fatalf("value = %#v", value)
	}

	// message_start and message_end carry the message.
	event = &SessionEvent{
		Type:  SessionMessageStart,
		Agent: &agent.AgentEvent{Message: &ai.AssistantMessage{Model: "gpt-4o-mini", Provider: "openai", StopReason: "stop"}},
	}
	value, err = ToJSONEvent(event)
	if err != nil {
		t.Fatal(err)
	}
	message, ok := value["message"].(map[string]any)
	if !ok || message["role"] != "assistant" || message["model"] != "gpt-4o-mini" {
		t.Fatalf("message = %#v", value["message"])
	}

	// A nil event is rejected.
	if _, err := ToJSONEvent(nil); err == nil {
		t.Fatal("nil event must be rejected")
	}
}

// ToolSelection projects the CLI tool flags onto the session's tool options.
// The binary passed none of them, so every tool flag in --help was silently
// ignored; upstream main.ts sets noTools from --no-tools/--no-builtin-tools
// and passes the allow and deny lists straight through.
func TestArgsToolSelection(t *testing.T) {
	cases := []struct {
		name string
		argv []string
		want ToolSelection
	}{
		{"no flags", []string{}, ToolSelection{}},
		{
			"allowlist",
			[]string{"--tools", "read,grep"},
			ToolSelection{Tools: []ToolName{"read", "grep"}},
		},
		{
			"denylist",
			[]string{"--exclude-tools", "bash,write"},
			ToolSelection{ExcludeTools: []ToolName{"bash", "write"}},
		},
		{"no-tools", []string{"--no-tools"}, ToolSelection{NoTools: NoToolsAll}},
		{"no-builtin-tools", []string{"--no-builtin-tools"}, ToolSelection{NoTools: NoToolsBuiltin}},
		{
			// Upstream checks --no-tools first, so it wins.
			"both disable flags",
			[]string{"--no-tools", "--no-builtin-tools"},
			ToolSelection{NoTools: NoToolsAll},
		},
		{
			"allowlist with a disable flag",
			[]string{"--no-tools", "--tools", "read"},
			ToolSelection{Tools: []ToolName{"read"}, NoTools: NoToolsAll},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ParseArgs(c.argv).ToolSelection()
			if strings.Join(got.Tools, ",") != strings.Join(c.want.Tools, ",") {
				t.Errorf("Tools = %v, want %v", got.Tools, c.want.Tools)
			}
			if strings.Join(got.ExcludeTools, ",") != strings.Join(c.want.ExcludeTools, ",") {
				t.Errorf("ExcludeTools = %v, want %v", got.ExcludeTools, c.want.ExcludeTools)
			}
			if got.NoTools != c.want.NoTools {
				t.Errorf("NoTools = %q, want %q", got.NoTools, c.want.NoTools)
			}
		})
	}
}
