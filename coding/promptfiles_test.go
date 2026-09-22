package coding

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writePromptTestFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestResolvePromptInputReadsExistingPaths pins upstream's resolvePromptInput:
// a prompt source that names an existing file is read (BOM stripped), anything
// else is the prompt text itself.
func TestResolvePromptInputReadsExistingPaths(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "prompt.md")
	writePromptTestFile(t, file, "\ufeffBe concise.\n")

	if got := ResolvePromptInput(file); got != "Be concise.\n" {
		t.Fatalf("ResolvePromptInput(file) = %q", got)
	}
	if got := ResolvePromptInput("Be concise."); got != "Be concise." {
		t.Fatalf("ResolvePromptInput(text) = %q", got)
	}
	if got := ResolvePromptInput(""); got != "" {
		t.Fatalf("ResolvePromptInput(\"\") = %q", got)
	}
	// A missing path is prompt text (upstream falls back to the literal input).
	missing := filepath.Join(dir, "missing.md")
	if got := ResolvePromptInput(missing); got != missing {
		t.Fatalf("ResolvePromptInput(missing) = %q", got)
	}
}

// TestPromptFileDiscoveryPrefersTheTrustedProject covers upstream's
// discoverSystemPromptFile / discoverAppendSystemPromptFile: the project's
// .pi file wins while the project is trusted, the agent-dir file is the
// fallback, and neither existing means no file.
func TestPromptFileDiscoveryPrefersTheTrustedProject(t *testing.T) {
	cwd, agentDir := t.TempDir(), t.TempDir()
	projectSystem := filepath.Join(cwd, ConfigDirName, "SYSTEM.md")
	globalSystem := filepath.Join(agentDir, "SYSTEM.md")
	projectAppend := filepath.Join(cwd, ConfigDirName, "APPEND_SYSTEM.md")
	globalAppend := filepath.Join(agentDir, "APPEND_SYSTEM.md")
	writePromptTestFile(t, projectSystem, "project base")
	writePromptTestFile(t, globalSystem, "global base")
	writePromptTestFile(t, projectAppend, "project append")
	writePromptTestFile(t, globalAppend, "global append")

	// An untrusted project's prompt files are skipped.
	if got := DiscoverSystemPromptFile(cwd, agentDir, false); got != globalSystem {
		t.Fatalf("untrusted system prompt file = %q, want %q", got, globalSystem)
	}
	if got := DiscoverAppendSystemPromptFile(cwd, agentDir, false); got != globalAppend {
		t.Fatalf("untrusted append prompt file = %q, want %q", got, globalAppend)
	}
	// Trusted: the project files win.
	if got := DiscoverSystemPromptFile(cwd, agentDir, true); got != projectSystem {
		t.Fatalf("trusted system prompt file = %q, want %q", got, projectSystem)
	}
	if got := DiscoverAppendSystemPromptFile(cwd, agentDir, true); got != projectAppend {
		t.Fatalf("trusted append prompt file = %q, want %q", got, projectAppend)
	}

	// Nothing to discover.
	empty := t.TempDir()
	if got := DiscoverSystemPromptFile(empty, empty, true); got != "" {
		t.Fatalf("system prompt file = %q, want none", got)
	}
	if got := DiscoverAppendSystemPromptFile(empty, empty, true); got != "" {
		t.Fatalf("append prompt file = %q, want none", got)
	}
}

// TestLoadPromptOverridesPrefersCLIAndJoinsAppends pins the resource-loader
// resolution: an explicit CLI source wins over the discovered file, multiple
// append sources join with a blank line (upstream agent-session joins the
// loader's append prompts with "\n\n"), and an explicit empty base prompt
// suppresses discovery.
func TestLoadPromptOverridesPrefersCLIAndJoinsAppends(t *testing.T) {
	cwd, agentDir := t.TempDir(), t.TempDir()
	writePromptTestFile(t, filepath.Join(agentDir, "SYSTEM.md"), "file base")
	writePromptTestFile(t, filepath.Join(agentDir, "APPEND_SYSTEM.md"), "file append")

	overrides := LoadPromptOverrides(PromptFileSources{Cwd: cwd, AgentDir: agentDir})
	if overrides.SystemPrompt != "file base" || overrides.AppendSystemPrompt != "file append" {
		t.Fatalf("from files: base=%q append=%q", overrides.SystemPrompt, overrides.AppendSystemPrompt)
	}
	if len(overrides.SourcePaths) != 2 || overrides.SourcePaths[0] != filepath.Join(agentDir, "SYSTEM.md") ||
		overrides.SourcePaths[1] != filepath.Join(agentDir, "APPEND_SYSTEM.md") {
		t.Fatalf("source paths = %#v", overrides.SourcePaths)
	}

	cli := "cli base"
	overrides = LoadPromptOverrides(PromptFileSources{
		Cwd: cwd, AgentDir: agentDir, SystemPrompt: &cli,
		AppendSystemPrompt: []string{"one", "two"},
	})
	if overrides.SystemPrompt != "cli base" || overrides.AppendSystemPrompt != "one\n\ntwo" {
		t.Fatalf("from cli: base=%q append=%q", overrides.SystemPrompt, overrides.AppendSystemPrompt)
	}
	// Literal flag values are not files, so they add no source path.
	if len(overrides.SourcePaths) != 0 {
		t.Fatalf("literal flag sources added paths: %#v", overrides.SourcePaths)
	}

	// A CLI source may name a file too.
	appendFile := filepath.Join(t.TempDir(), "extra.md")
	writePromptTestFile(t, appendFile, "from a file")
	overrides = LoadPromptOverrides(PromptFileSources{
		Cwd: cwd, AgentDir: agentDir, AppendSystemPrompt: []string{appendFile},
	})
	if overrides.AppendSystemPrompt != "from a file" {
		t.Fatalf("append from file = %q", overrides.AppendSystemPrompt)
	}
	// The discovered base prompt is still the base source; the append came from
	// the flag's file.
	if len(overrides.SourcePaths) != 2 || !strings.HasSuffix(overrides.SourcePaths[0], "SYSTEM.md") ||
		!strings.HasSuffix(overrides.SourcePaths[1], "extra.md") {
		t.Fatalf("source paths = %#v", overrides.SourcePaths)
	}

	empty := ""
	if overrides := LoadPromptOverrides(PromptFileSources{Cwd: cwd, AgentDir: agentDir, SystemPrompt: &empty}); overrides.SystemPrompt != "" {
		t.Fatalf("an explicit empty base prompt must not discover a file, got %q", overrides.SystemPrompt)
	}
}
