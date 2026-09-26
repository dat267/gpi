package coding

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/dat267/pier/internal/offloop"
)

// Tests ported from packages/coding-agent/test/settings-manager.test.ts (and
// the escaping/migration cases from the -bug/-compaction suites).

func settingsDirs(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	agentDir := filepath.Join(root, "agent")
	projectDir := filepath.Join(root, "project")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(projectDir, ConfigDirName), 0o755); err != nil {
		t.Fatal(err)
	}
	return agentDir, projectDir
}

func writeSettingsFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readSettingsFile(t *testing.T, path string) map[string]any {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(content, &raw); err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestSettingsPreservesExternallyAddedSettings(t *testing.T) {
	agentDir, projectDir := settingsDirs(t)
	settingsPath := filepath.Join(agentDir, "settings.json")
	writeSettingsFile(t, settingsPath, `{"theme":"dark","defaultModel":"claude-sonnet"}`)

	manager := NewSettingsManagerFromFiles(projectDir, agentDir, SettingsManagerCreateOptions{})

	// The user edits settings.json externally to add enabledModels.
	raw := readSettingsFile(t, settingsPath)
	raw["enabledModels"] = []any{"claude-opus-4-5", "gpt-5.2-codex"}
	encoded, _ := json.Marshal(raw)
	writeSettingsFile(t, settingsPath, string(encoded))

	manager.SetDefaultThinkingLevel("high")
	saved := readSettingsFile(t, settingsPath)
	if saved["defaultThinkingLevel"] != "high" || saved["theme"] != "dark" || saved["defaultModel"] != "claude-sonnet" {
		t.Fatalf("saved = %#v", saved)
	}
	models, _ := saved["enabledModels"].([]any)
	if len(models) != 2 || models[0] != "claude-opus-4-5" || models[1] != "gpt-5.2-codex" {
		t.Fatalf("enabledModels = %#v", saved["enabledModels"])
	}

	// Unrelated custom settings survive a theme write too.
	raw = readSettingsFile(t, settingsPath)
	raw["shellPath"] = "/bin/zsh"
	raw["extensions"] = []any{"/path/to/extension.ts"}
	encoded, _ = json.Marshal(raw)
	writeSettingsFile(t, settingsPath, string(encoded))
	manager.SetTheme("light")
	saved = readSettingsFile(t, settingsPath)
	if saved["theme"] != "light" || saved["shellPath"] != "/bin/zsh" {
		t.Fatalf("saved = %#v", saved)
	}
	extensions, _ := saved["extensions"].([]any)
	if len(extensions) != 1 || extensions[0] != "/path/to/extension.ts" {
		t.Fatalf("extensions = %#v", saved["extensions"])
	}
}

func TestSettingsPackagesMigration(t *testing.T) {
	agentDir, projectDir := settingsDirs(t)
	settingsPath := filepath.Join(agentDir, "settings.json")

	// Local-only extensions stay in the extensions array.
	writeSettingsFile(t, settingsPath, `{"extensions":["/local/ext.ts","./relative/ext.ts"]}`)
	manager := NewSettingsManagerFromFiles(projectDir, agentDir, SettingsManagerCreateOptions{})
	if packages := manager.GetPackages(); len(packages) != 0 {
		t.Fatalf("packages = %#v", packages)
	}
	if paths := manager.GetExtensionPaths(); len(paths) != 2 || paths[0] != "/local/ext.ts" || paths[1] != "./relative/ext.ts" {
		t.Fatalf("extensions = %#v", paths)
	}

	// Package entries keep their filtering objects.
	writeSettingsFile(t, settingsPath, `{"packages":["npm:simple-pkg",{"source":"npm:shitty-extensions","extensions":["extensions/oracle.ts"],"skills":[]}]}`)
	manager = NewSettingsManagerFromFiles(projectDir, agentDir, SettingsManagerCreateOptions{})
	packages := manager.GetPackages()
	if len(packages) != 2 || packages[0] != "npm:simple-pkg" {
		t.Fatalf("packages = %#v", packages)
	}
	filter, ok := packages[1].(map[string]any)
	if !ok || filter["source"] != "npm:shitty-extensions" {
		t.Fatalf("filter = %#v", packages[1])
	}
	extensions, _ := filter["extensions"].([]any)
	if len(extensions) != 1 || extensions[0] != "extensions/oracle.ts" {
		t.Fatalf("filter extensions = %#v", filter["extensions"])
	}
	if skills, ok := filter["skills"].([]any); !ok || len(skills) != 0 {
		t.Fatalf("filter skills = %#v", filter["skills"])
	}
}

func TestSettingsReload(t *testing.T) {
	agentDir, projectDir := settingsDirs(t)
	settingsPath := filepath.Join(agentDir, "settings.json")
	writeSettingsFile(t, settingsPath, `{"theme":"dark","extensions":["/before.ts"]}`)

	manager := NewSettingsManagerFromFiles(projectDir, agentDir, SettingsManagerCreateOptions{})
	writeSettingsFile(t, settingsPath, `{"theme":"light","extensions":["/after.ts"],"defaultModel":"claude-sonnet"}`)
	manager.Reload()

	if theme := manager.GetTheme(); theme == nil || *theme != "light" {
		t.Fatalf("theme = %v", theme)
	}
	if paths := manager.GetExtensionPaths(); len(paths) != 1 || paths[0] != "/after.ts" {
		t.Fatalf("extensions = %#v", paths)
	}
	if model := manager.GetDefaultModel(); model == nil || *model != "claude-sonnet" {
		t.Fatalf("defaultModel = %v", model)
	}

	// An invalid file keeps the previous settings and reports the path.
	writeSettingsFile(t, settingsPath, "{ invalid json")
	manager.Reload()
	if theme := manager.GetTheme(); theme == nil || *theme != "light" {
		t.Fatalf("theme after invalid reload = %v", theme)
	}
	errors := manager.DrainErrors()
	if len(errors) != 1 || errors[0].Scope != SettingsScopeGlobal || errors[0].Path != settingsPath {
		t.Fatalf("errors = %+v", errors)
	}
}

func TestSettingsThemeSetting(t *testing.T) {
	agentDir, projectDir := settingsDirs(t)
	settingsPath := filepath.Join(agentDir, "settings.json")
	writeSettingsFile(t, settingsPath, `{"theme":"light/dark"}`)

	manager := NewSettingsManagerFromFiles(projectDir, agentDir, SettingsManagerCreateOptions{})
	if theme := manager.GetTheme(); theme != nil {
		t.Fatalf("getTheme() = %v", theme)
	}
	if theme := manager.GetThemeSetting(); theme == nil || *theme != "light/dark" {
		t.Fatalf("getThemeSetting() = %v", theme)
	}
	manager.SetTheme("solarized-light/tokyo-night")
	if saved := readSettingsFile(t, settingsPath); saved["theme"] != "solarized-light/tokyo-night" {
		t.Fatalf("theme = %#v", saved["theme"])
	}
}

func TestSettingsErrorTracking(t *testing.T) {
	agentDir, projectDir := settingsDirs(t)
	globalPath := filepath.Join(agentDir, "settings.json")
	projectPath := filepath.Join(projectDir, ConfigDirName, "settings.json")
	writeSettingsFile(t, globalPath, "{ invalid global json")
	writeSettingsFile(t, projectPath, "{ invalid project json")

	manager := NewSettingsManagerFromFiles(projectDir, agentDir, SettingsManagerCreateOptions{})
	errors := manager.DrainErrors()
	if len(errors) != 2 {
		t.Fatalf("errors = %+v", errors)
	}
	if errors[0].Scope != SettingsScopeGlobal || errors[0].Path != globalPath {
		t.Fatalf("error 0 = %+v", errors[0])
	}
	if errors[1].Scope != SettingsScopeProject || errors[1].Path != projectPath {
		t.Fatalf("error 1 = %+v", errors[1])
	}
	if drained := manager.DrainErrors(); len(drained) != 0 {
		t.Fatalf("drained = %+v", drained)
	}
}

func TestSettingsProjectTrust(t *testing.T) {
	agentDir, projectDir := settingsDirs(t)
	writeSettingsFile(t, filepath.Join(agentDir, "settings.json"), `{"theme":"global"}`)
	projectPath := filepath.Join(projectDir, ConfigDirName, "settings.json")
	writeSettingsFile(t, projectPath, `{"theme":"project"}`)

	untrusted := false
	manager := NewSettingsManagerFromFiles(projectDir, agentDir, SettingsManagerCreateOptions{ProjectTrusted: &untrusted})
	if manager.IsProjectTrusted() {
		t.Fatal("project must be untrusted")
	}
	if theme := manager.GetTheme(); theme == nil || *theme != "global" {
		t.Fatalf("theme = %v", theme)
	}
	if project := manager.GetProjectSettings(); settingsToMap(project)["theme"] != nil {
		t.Fatalf("project settings = %#v", project)
	}

	// Trusting reloads the project scope.
	manager.SetProjectTrusted(true)
	if !manager.IsProjectTrusted() {
		t.Fatal("project must be trusted")
	}
	if theme := manager.GetTheme(); theme == nil || *theme != "project" {
		t.Fatalf("theme = %v", theme)
	}

	// Writes to an untrusted project are refused and recorded (D23: the Go port
	// records the error where upstream throws).
	writeSettingsFile(t, projectPath, `{"packages":["npm:existing"]}`)
	untrustedManager := NewSettingsManagerFromFiles(projectDir, agentDir, SettingsManagerCreateOptions{ProjectTrusted: &untrusted})
	untrustedManager.SetProjectPackages([]any{"npm:new"})
	errors := untrustedManager.DrainErrors()
	if len(errors) != 1 || !strings.Contains(errors[0].Error.Error(), "Project is not trusted; refusing to write project settings") {
		t.Fatalf("errors = %+v", errors)
	}
	if saved := readSettingsFile(t, projectPath); len(saved) != 1 || saved["packages"] == nil {
		t.Fatalf("project settings file = %#v", saved)
	}
}

func TestSettingsDefaultProjectTrust(t *testing.T) {
	agentDir, projectDir := settingsDirs(t)
	writeSettingsFile(t, filepath.Join(agentDir, "settings.json"), `{"defaultProjectTrust":"always"}`)
	writeSettingsFile(t, filepath.Join(projectDir, ConfigDirName, "settings.json"), `{"defaultProjectTrust":"never"}`)
	manager := NewSettingsManagerFromFiles(projectDir, agentDir, SettingsManagerCreateOptions{})
	if got := manager.GetDefaultProjectTrust(); got != "always" {
		t.Fatalf("defaultProjectTrust = %s", got)
	}

	writeSettingsFile(t, filepath.Join(agentDir, "settings.json"), `{"defaultProjectTrust":"sometimes"}`)
	manager = NewSettingsManagerFromFiles(projectDir, agentDir, SettingsManagerCreateOptions{})
	if got := manager.GetDefaultProjectTrust(); got != "ask" {
		t.Fatalf("defaultProjectTrust = %s", got)
	}
}

func TestSettingsProjectDirectoryCreation(t *testing.T) {
	agentDir, projectDir := settingsDirs(t)
	writeSettingsFile(t, filepath.Join(agentDir, "settings.json"), `{"theme":"dark"}`)
	projectConfigDir := filepath.Join(projectDir, ConfigDirName)
	if err := os.RemoveAll(projectConfigDir); err != nil {
		t.Fatal(err)
	}

	// Reading must not create the project config directory.
	manager := NewSettingsManagerFromFiles(projectDir, agentDir, SettingsManagerCreateOptions{})
	if fileExists(projectConfigDir) {
		t.Fatal("reading project settings must not create the config directory")
	}
	if theme := manager.GetTheme(); theme == nil || *theme != "dark" {
		t.Fatalf("theme = %v", theme)
	}

	// Writing does create it.
	manager.SetProjectPackages([]any{map[string]any{"source": "npm:test-pkg"}})
	if !fileExists(projectConfigDir) {
		t.Fatal("writing project settings must create the config directory")
	}
	if !fileExists(filepath.Join(projectConfigDir, "settings.json")) {
		t.Fatal("writing project settings must create the settings file")
	}
}

func TestSettingsRetryDefaultsAndOverrides(t *testing.T) {
	defaults := NewInMemorySettingsManager(nil, SettingsManagerCreateOptions{}).GetRetrySettings()
	if defaults != (RetrySettingsResult{Enabled: true, MaxRetries: 3, BaseDelayMS: 2000, MaxAgentDelayMS: 60000}) {
		t.Fatalf("defaults = %+v", defaults)
	}
	enabled := true
	maxRetries := 10
	baseDelay := int64(500)
	maxAgent := int64(5000)
	custom := NewInMemorySettingsManager(&Settings{Retry: &SettingsRetry{
		Enabled: &enabled, MaxRetries: &maxRetries, BaseDelayMS: &baseDelay, MaxAgentDelayMS: &maxAgent,
	}}, SettingsManagerCreateOptions{}).GetRetrySettings()
	if custom != (RetrySettingsResult{Enabled: true, MaxRetries: 10, BaseDelayMS: 500, MaxAgentDelayMS: 5000}) {
		t.Fatalf("custom = %+v", custom)
	}
}

func TestSettingsHTTPIdleTimeout(t *testing.T) {
	agentDir, projectDir := settingsDirs(t)
	manager := NewSettingsManagerFromFiles(projectDir, agentDir, SettingsManagerCreateOptions{})
	timeout, err := manager.GetHTTPIdleTimeoutMS()
	if err != nil || timeout != DefaultHTTPIdleTimeoutMS {
		t.Fatalf("timeout = %d err = %v", timeout, err)
	}

	// Project settings override global ones.
	writeSettingsFile(t, filepath.Join(agentDir, "settings.json"), `{"httpIdleTimeoutMs":300000}`)
	writeSettingsFile(t, filepath.Join(projectDir, ConfigDirName, "settings.json"), `{"httpIdleTimeoutMs":0}`)
	manager = NewSettingsManagerFromFiles(projectDir, agentDir, SettingsManagerCreateOptions{})
	if timeout, err := manager.GetHTTPIdleTimeoutMS(); err != nil || timeout != 0 {
		t.Fatalf("timeout = %d err = %v", timeout, err)
	}

	// Invalid values are rejected (fresh dirs: the project scope from the
	// previous case would otherwise override the global value).
	agentDir, projectDir = settingsDirs(t)
	writeSettingsFile(t, filepath.Join(agentDir, "settings.json"), `{"httpIdleTimeoutMs":-1}`)
	manager = NewSettingsManagerFromFiles(projectDir, agentDir, SettingsManagerCreateOptions{})
	if _, err := manager.GetHTTPIdleTimeoutMS(); err == nil ||
		!strings.Contains(err.Error(), "Invalid httpIdleTimeoutMs setting") {
		t.Fatalf("err = %v", err)
	}
}

func TestSettingsExternalEditorAndTUISettings(t *testing.T) {
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "")
	manager := NewInMemorySettingsManager(nil, SettingsManagerCreateOptions{})
	// settings-manager.ts:995 is process.platform === "win32" ? "notepad" : "nano".
	wantDefault := "nano"
	if runtime.GOOS == "windows" {
		wantDefault = "notepad"
	}
	if got := manager.GetExternalEditorCommand(); got != wantDefault {
		t.Fatalf("editor = %s", got)
	}
	editor := "  custom-editor  "
	configured := NewInMemorySettingsManager(&Settings{ExternalEditor: &editor}, SettingsManagerCreateOptions{})
	if got := configured.GetExternalEditorCommand(); got != "  custom-editor  " {
		t.Fatalf("editor = %q", got)
	}
	t.Setenv("VISUAL", "visual-editor")
	if got := NewInMemorySettingsManager(nil, SettingsManagerCreateOptions{}).GetExternalEditorCommand(); got != "visual-editor" {
		t.Fatalf("editor = %q", got)
	}
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "env-editor")
	if got := NewInMemorySettingsManager(nil, SettingsManagerCreateOptions{}).GetExternalEditorCommand(); got != "env-editor" {
		t.Fatalf("editor = %q", got)
	}

	// TUI mode, output padding, mermaid, and shell command prefix.
	if got := manager.GetTuiMode(); got != "regular" {
		t.Fatalf("tui mode = %s", got)
	}
	manager.SetTuiMode("fullscreen")
	if got := manager.GetTuiMode(); got != "fullscreen" {
		t.Fatalf("tui mode = %s", got)
	}
	if got := manager.GetOutputPad(); got != 1 {
		t.Fatalf("outputPad = %d", got)
	}
	manager.SetOutputPad(0)
	if got := manager.GetOutputPad(); got != 0 {
		t.Fatalf("outputPad = %d", got)
	}
	if got := manager.GetMermaidRenderingMode(); got != "streaming" {
		t.Fatalf("mermaid = %s", got)
	}
	manager.SetMermaidRenderingMode("final")
	if got := manager.GetMermaidRenderingMode(); got != "final" {
		t.Fatalf("mermaid = %s", got)
	}
	weird := 2
	unsupported := NewInMemorySettingsManager(&Settings{OutputPad: &weird}, SettingsManagerCreateOptions{})
	if got := unsupported.GetOutputPad(); got != 1 {
		t.Fatalf("unsupported outputPad = %d", got)
	}
	sometimes := "sometimes"
	badMermaid := NewInMemorySettingsManager(&Settings{Markdown: &SettingsMarkdown{Mermaid: &sometimes}}, SettingsManagerCreateOptions{})
	if got := badMermaid.GetMermaidRenderingMode(); got != "streaming" {
		t.Fatalf("unsupported mermaid = %s", got)
	}

	prefix := "shopt -s expand_aliases"
	withPrefix := NewInMemorySettingsManager(&Settings{ShellCommandPrefix: &prefix}, SettingsManagerCreateOptions{})
	if got := withPrefix.GetShellCommandPrefix(); got == nil || *got != "shopt -s expand_aliases" {
		t.Fatalf("prefix = %v", got)
	}
	if got := manager.GetShellCommandPrefix(); got != nil {
		t.Fatalf("prefix = %v", got)
	}
}

func TestSettingsDefaultToolsAndPaths(t *testing.T) {
	agentDir, projectDir := settingsDirs(t)
	writeSettingsFile(t, filepath.Join(agentDir, "settings.json"), `{"defaultTools":["read","bash"],"sessionDir":"/tmp/sessions","shellPath":"~"}`)
	manager := NewSettingsManagerFromFiles(projectDir, agentDir, SettingsManagerCreateOptions{})
	tools := manager.GetDefaultTools()
	if len(tools) != 2 || tools[0] != "read" || tools[1] != "bash" {
		t.Fatalf("defaultTools = %#v", tools)
	}
	// Project settings replace the global array.
	writeSettingsFile(t, filepath.Join(projectDir, ConfigDirName, "settings.json"), `{"defaultTools":["grep"]}`)
	manager = NewSettingsManagerFromFiles(projectDir, agentDir, SettingsManagerCreateOptions{})
	if tools := manager.GetDefaultTools(); len(tools) != 1 || tools[0] != "grep" {
		t.Fatalf("defaultTools = %#v", tools)
	}
	// An explicitly empty list survives.
	if tools := NewInMemorySettingsManager(&Settings{DefaultTools: []string{}}, SettingsManagerCreateOptions{}).GetDefaultTools(); tools == nil || len(tools) != 0 {
		t.Fatalf("empty defaultTools = %#v", tools)
	}
	if tools := NewInMemorySettingsManager(nil, SettingsManagerCreateOptions{}).GetDefaultTools(); tools != nil {
		t.Fatalf("unset defaultTools = %#v", tools)
	}

	// Shell path expands a bare tilde; sessionDir expands too.
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	if got := manager.GetShellPath(); got == nil || *got != home {
		t.Fatalf("shellPath = %v (want %s)", got, home)
	}
	if got := manager.GetSessionDir(); got == nil || *got != "/tmp/sessions" {
		t.Fatalf("sessionDir = %v", got)
	}
	relative := "./sessions"
	if got := NewInMemorySettingsManager(&Settings{SessionDir: &relative}, SettingsManagerCreateOptions{}).GetSessionDir(); got == nil || *got != "./sessions" {
		t.Fatalf("relative sessionDir = %v", got)
	}
	if got := NewInMemorySettingsManager(nil, SettingsManagerCreateOptions{}).GetSessionDir(); got != nil {
		t.Fatalf("unset sessionDir = %v", got)
	}
}

func TestSettingsMigrations(t *testing.T) {
	// queueMode -> steeringMode
	migrated := MigrateSettingsRaw(map[string]any{"queueMode": "all"})
	if migrated.SteeringMode == nil || *migrated.SteeringMode != "all" {
		t.Fatalf("steeringMode = %v", migrated.SteeringMode)
	}
	// websockets -> transport
	migrated = MigrateSettingsRaw(map[string]any{"websockets": true})
	if migrated.Transport == nil || *migrated.Transport != "websocket" {
		t.Fatalf("transport = %v", migrated.Transport)
	}
	migrated = MigrateSettingsRaw(map[string]any{"websockets": false})
	if migrated.Transport == nil || *migrated.Transport != "sse" {
		t.Fatalf("transport = %v", migrated.Transport)
	}
	// skills object -> array (+ enableSkillCommands)
	migrated = MigrateSettingsRaw(map[string]any{
		"skills": map[string]any{"enableSkillCommands": false, "customDirectories": []any{"/skills/a"}},
	})
	if migrated.EnableSkillCommands == nil || *migrated.EnableSkillCommands {
		t.Fatalf("enableSkillCommands = %v", migrated.EnableSkillCommands)
	}
	if len(migrated.Skills) != 1 || migrated.Skills[0] != "/skills/a" {
		t.Fatalf("skills = %#v", migrated.Skills)
	}
	migrated = MigrateSettingsRaw(map[string]any{"skills": map[string]any{"customDirectories": []any{}}})
	if migrated.Skills != nil {
		t.Fatalf("skills = %#v", migrated.Skills)
	}
	// retry.maxDelayMs -> retry.provider.maxRetryDelayMs
	migrated = MigrateSettingsRaw(map[string]any{"retry": map[string]any{"maxDelayMs": float64(1234)}})
	if migrated.Retry == nil || migrated.Retry.Provider == nil || migrated.Retry.Provider.MaxRetryDelayMS == nil ||
		*migrated.Retry.Provider.MaxRetryDelayMS != 1234 {
		t.Fatalf("retry = %+v", migrated.Retry)
	}
	if raw := settingsToMap(migrated); raw["retry"].(map[string]any)["maxDelayMs"] != nil {
		t.Fatalf("legacy key survived: %#v", raw["retry"])
	}
}

func TestSettingsDeepMerge(t *testing.T) {
	enabled := false
	base := &Settings{
		Compaction: &SettingsCompaction{Enabled: &enabled, ReserveTokens: int64Ptr(100)},
		Extensions: []string{"/a"},
	}
	keepRecent := int64(50)
	overrides := &Settings{
		Compaction: &SettingsCompaction{KeepRecentTokens: &keepRecent},
		Extensions: []string{"/b", "/c"},
	}
	merged := DeepMergeSettings(base, overrides)
	if merged.Compaction == nil || merged.Compaction.Enabled == nil || *merged.Compaction.Enabled {
		t.Fatalf("merged compaction = %+v", merged.Compaction)
	}
	if merged.Compaction.ReserveTokens == nil || *merged.Compaction.ReserveTokens != 100 {
		t.Fatalf("reserveTokens = %v", merged.Compaction.ReserveTokens)
	}
	if merged.Compaction.KeepRecentTokens == nil || *merged.Compaction.KeepRecentTokens != 50 {
		t.Fatalf("keepRecentTokens = %v", merged.Compaction.KeepRecentTokens)
	}
	// Arrays replace rather than merge.
	if len(merged.Extensions) != 2 || merged.Extensions[0] != "/b" {
		t.Fatalf("extensions = %#v", merged.Extensions)
	}
}

func int64Ptr(value int64) *int64 { return &value }

func TestSettingsCompactionTokenResolution(t *testing.T) {
	manager := NewInMemorySettingsManager(nil, SettingsManagerCreateOptions{})
	reserve, err := manager.GetCompactionReserveTokens(nil)
	if err != nil || reserve != DefaultCompactionReserveTokens {
		t.Fatalf("reserve = %d err = %v", reserve, err)
	}
	// Model overrides win over ordinary settings, which win over defaults.
	ordinary := int64(4096)
	override := int64(1024)
	manager = NewInMemorySettingsManager(&Settings{Compaction: &SettingsCompaction{
		ReserveTokens:  &ordinary,
		ModelOverrides: map[string]SettingsCompactionOverride{"anthropic/claude-sonnet-4-5": {ReserveTokens: &override}},
	}}, SettingsManagerCreateOptions{})
	model := testModel("claude-sonnet-4-5", "Claude Sonnet 4.5", "anthropic", true)
	value, err := manager.GetCompactionReserveTokens(model)
	if err != nil || value != 1024 {
		t.Fatalf("override = %d err = %v", value, err)
	}
	other := testModel("gpt-4o", "GPT-4o", "openai", false)
	value, err = manager.GetCompactionReserveTokens(other)
	if err != nil || value != 4096 {
		t.Fatalf("ordinary = %d err = %v", value, err)
	}
	// Negative values are rejected.
	negative := int64(-1)
	invalid := NewInMemorySettingsManager(&Settings{Compaction: &SettingsCompaction{ReserveTokens: &negative}}, SettingsManagerCreateOptions{})
	if _, err := invalid.GetCompactionReserveTokens(nil); err == nil ||
		!strings.Contains(err.Error(), "Invalid compaction.reserveTokens setting") {
		t.Fatalf("err = %v", err)
	}
}

func TestSettingsManagerInMemoryRoundTrip(t *testing.T) {
	manager := NewInMemorySettingsManager(&Settings{Theme: strPtr("dark")}, SettingsManagerCreateOptions{})
	manager.SetDefaultModelAndProvider("anthropic", "claude-opus-4-8")
	manager.SetModelThinkingLevel("anthropic", "claude-opus-4-8", "high")
	if level := manager.GetModelThinkingLevel("anthropic", "claude-opus-4-8"); level == nil || *level != "high" {
		t.Fatalf("level = %v", level)
	}
	if all := manager.GetAllModelThinkingLevels(); all["anthropic/claude-opus-4-8"] != "high" {
		t.Fatalf("all = %#v", all)
	}
	manager.RemoveModelThinkingLevel("anthropic", "claude-opus-4-8")
	if level := manager.GetModelThinkingLevel("anthropic", "claude-opus-4-8"); level != nil {
		t.Fatalf("level = %v", level)
	}
	if provider := manager.GetDefaultProvider(); provider == nil || *provider != "anthropic" {
		t.Fatalf("provider = %v", provider)
	}
	// Global scope changes are visible through the merged view.
	if model := manager.GetDefaultModel(); model == nil || *model != "claude-opus-4-8" {
		t.Fatalf("model = %v", model)
	}
	// Overrides apply to the merged view only.
	override := "override"
	manager.ApplyOverrides(&Settings{Theme: &override})
	if theme := manager.GetThemeSetting(); theme == nil || *theme != "override" {
		t.Fatalf("theme = %v", theme)
	}
	if global := manager.GetGlobalSettings(); global.Theme == nil || *global.Theme != "dark" {
		t.Fatalf("global theme = %v", global.Theme)
	}
}

func strPtr(value string) *string { return &value }

// SetX hands persistence to the off-loop queue: the calling goroutine (the UI
// loop in interactive mode) must not wait on the storage lock, whose retries
// can cost up to 10 x 20 ms, or on the file read and write.
func TestSettingsSetDoesNotBlockOnTheStorageLock(t *testing.T) {
	agentDir, projectDir := settingsDirs(t)
	settingsPath := filepath.Join(agentDir, "settings.json")
	writeSettingsFile(t, settingsPath, `{"theme":"light"}`)
	// Hold the storage lock the way a concurrent writer would.
	lock, err := acquireLockWithRetry(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	defer lock()

	manager := NewSettingsManagerFromFiles(projectDir, agentDir, SettingsManagerCreateOptions{
		PersistQueue: offloop.New(),
	})
	done := make(chan struct{})
	go func() {
		manager.SetTheme("dark")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("SetTheme blocked on the storage lock")
	}
	// The queued persist fails after its retry window and the error is
	// recorded; nothing is lost or silently swallowed.
	manager.FlushPersists()
	if saved := readSettingsFile(t, settingsPath); saved["theme"] != "light" {
		t.Fatalf("theme = %#v, want the unchanged light", saved["theme"])
	}
	errors := manager.DrainErrors()
	if len(errors) != 1 || errors[0].Scope != SettingsScopeGlobal {
		t.Fatalf("errors = %+v", errors)
	}
}

// Persistence still lands: mutators keep the in-memory view synchronous and
// the queue persists every submission in order.
func TestSettingsPersistLandsOffLoop(t *testing.T) {
	agentDir, projectDir := settingsDirs(t)
	settingsPath := filepath.Join(agentDir, "settings.json")
	writeSettingsFile(t, settingsPath, `{"theme":"light"}`)

	manager := NewSettingsManagerFromFiles(projectDir, agentDir, SettingsManagerCreateOptions{
		PersistQueue: offloop.New(),
	})
	manager.SetTheme("dark")
	manager.SetDefaultThinkingLevel("high")
	manager.FlushPersists()
	saved := readSettingsFile(t, settingsPath)
	if saved["theme"] != "dark" || saved["defaultThinkingLevel"] != "high" {
		t.Fatalf("saved = %#v", saved)
	}
	if manager.GetTheme() == nil || *manager.GetTheme() != "dark" {
		t.Fatalf("in-memory theme = %v", manager.GetTheme())
	}
}
