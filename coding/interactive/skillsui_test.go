package interactive

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dat267/pier/coding"
	"github.com/dat267/pier/tui"
)

// TestAutocompleteSkillCommandsWired covers the skill slash commands: the
// session loads skills into the system prompt (systemPromptOptions.Skills),
// but AutocompleteWiring.Skills was never assigned, so /skill:<name> commands
// never appeared (upstream builds them from the resource loader's skills).
func TestAutocompleteSkillCommandsWired(t *testing.T) {
	app, cleanup := newTestApp(t)
	defer cleanup()
	app.Init(context.Background())

	app.session.SystemPromptOptions.Skills = []coding.Skill{
		{Name: "tdd", Description: "Test-driven development", FilePath: "/skills/tdd/SKILL.md"},
	}

	commands := app.autocomplete.Skills()
	if len(commands) != 1 || commands[0].Name != "tdd" || commands[0].FilePath != "/skills/tdd/SKILL.md" {
		t.Fatalf("skill commands = %+v", commands)
	}

	provider, ok := app.autocomplete.CreateBaseAutocompleteProvider().(*tui.CombinedAutocompleteProvider)
	if !ok {
		t.Fatal("provider is not combined")
	}
	suggestions := provider.GetSuggestions(context.Background(), []string{"/skill:td"}, 0, len("/skill:td"), false)
	if suggestions == nil {
		t.Fatal("no suggestions for /skill:td")
	}
	found := false
	for _, item := range suggestions.Items {
		if item.Value == "skill:tdd" {
			found = true
		}
	}
	if !found {
		t.Fatalf("/skill:td suggestions missing skill:tdd: %+v", suggestions.Items)
	}
}

// TestLoadedResourcesShowsSkills covers the startup loaded-resources area: the
// container existed and was cleared but nothing ever populated it, so ctrl+o
// showed no skills (upstream showLoadedResources renders a [Skills] section).
func TestLoadedResourcesShowsSkills(t *testing.T) {
	app, cleanup := newTestApp(t)
	defer cleanup()
	app.Init(context.Background())

	app.session.SystemPromptOptions.Skills = []coding.Skill{
		{Name: "tdd", Description: "Test-driven development", FilePath: "/skills/tdd/SKILL.md"},
		{Name: "karpathy-guidelines", Description: "Guidelines", FilePath: "/skills/karpathy-guidelines/SKILL.md"},
	}
	app.session.SystemPromptOptions.ContextFiles = []coding.ContextFile{{Path: "/tmp/project/AGENTS.md"}}
	app.showLoadedResources(true)

	rendered := coding.StripAnsi(strings.Join(app.loadedResourcesContainer.Render(80), "\n"))
	if !strings.Contains(rendered, "Skills") {
		t.Fatalf("loaded resources missing Skills section:\n%s", rendered)
	}
	if !strings.Contains(rendered, "tdd") {
		t.Fatalf("loaded resources missing skill name:\n%s", rendered)
	}
	if !strings.Contains(rendered, "Context") {
		t.Fatalf("loaded resources missing Context section:\n%s", rendered)
	}
}

// TestLoadedResourcesListsPromptSources pins the Context section's leading
// entries: upstream spreads the system prompt source and the append prompt
// sources ahead of the agents files (getSystemPromptSource /
// getAppendSystemPromptSources).
func TestLoadedResourcesListsPromptSources(t *testing.T) {
	app, cleanup := newTestApp(t)
	defer cleanup()
	app.Init(context.Background())

	app.session.SystemPromptOptions.PromptSourcePaths = []string{
		filepath.Join(app.options.Cwd, ".pi", "SYSTEM.md"),
		"/home/user/.pi/agent/APPEND_SYSTEM.md",
	}
	app.session.SystemPromptOptions.ContextFiles = []coding.ContextFile{{Path: "/tmp/project/AGENTS.md"}}
	app.showLoadedResources(true)

	rendered := coding.StripAnsi(strings.Join(app.loadedResourcesContainer.Render(100), "\n"))
	contextIndex := strings.Index(rendered, "Context")
	systemIndex := strings.Index(rendered, "SYSTEM.md")
	appendIndex := strings.Index(rendered, "APPEND_SYSTEM.md")
	agentsIndex := strings.Index(rendered, "AGENTS.md")
	if contextIndex == -1 || systemIndex == -1 || appendIndex == -1 || agentsIndex == -1 {
		t.Fatalf("Context section is missing a prompt source:\n%s", rendered)
	}
	if !(contextIndex < systemIndex && systemIndex < appendIndex && appendIndex < agentsIndex) {
		t.Fatalf("prompt sources must precede the context files:\n%s", rendered)
	}
}

// TestReloadNowRereadsResources pins /reload's resource half: the wiring's
// reload re-reads the ported resource files (and the settings), so a prompt
// file or skill added after startup takes effect without restarting pier.
func TestReloadNowRereadsResources(t *testing.T) {
	app, cleanup := newTestApp(t)
	defer cleanup()
	app.Init(context.Background())

	if strings.Contains(app.session.SystemPrompt(), "reloaded append") {
		t.Fatal("the append file must not be loaded before it exists")
	}
	agentDir := app.options.AgentDir
	if err := os.MkdirAll(filepath.Join(agentDir, "skills", "tdd"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentDir, "APPEND_SYSTEM.md"), []byte("reloaded append"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentDir, "skills", "tdd", "SKILL.md"),
		[]byte("---\nname: tdd\ndescription: Test-driven development\n---\n\nWrite the test first.\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, _, err := app.commands.ReloadNow(); err != nil {
		t.Fatalf("reload failed: %v", err)
	}

	prompt := app.session.SystemPrompt()
	if !strings.Contains(prompt, "reloaded append") {
		t.Fatalf("/reload did not re-read the prompt files:\n%s", prompt)
	}
	if !strings.Contains(prompt, "tdd") {
		t.Fatalf("/reload did not re-read the skills:\n%s", prompt)
	}

	// The loaded-resources list follows the reloaded options.
	app.showLoadedResources(true)
	rendered := coding.StripAnsi(strings.Join(app.loadedResourcesContainer.Render(100), "\n"))
	if !strings.Contains(rendered, "APPEND_SYSTEM.md") || !strings.Contains(rendered, "tdd") {
		t.Fatalf("loaded resources are stale after reload:\n%s", rendered)
	}
}

// TestToolsExpandTogglesLoadedResources covers ctrl+o: OnToolsExpand only
// flipped the UIState bool, never calling SetToolsExpanded, so the header and
// loaded-resources expandables stayed collapsed.
func TestToolsExpandTogglesLoadedResources(t *testing.T) {
	app, cleanup := newTestApp(t)
	defer cleanup()
	app.Init(context.Background())

	app.session.SystemPromptOptions.Skills = []coding.Skill{{
		Name: "tdd", Description: "Test-driven development", FilePath: "/skills/tdd/SKILL.md",
		SourceInfo: coding.CreateSyntheticSourceInfo("/skills/tdd/SKILL.md", "local", coding.SourceScopeUser, coding.SourceOriginTopLevel, "/skills"),
	}}
	app.showLoadedResources(true)

	renderLoaded := func() string {
		return coding.StripAnsi(strings.Join(app.loadedResourcesContainer.Render(80), "\n"))
	}
	if strings.Contains(renderLoaded(), "/skills/tdd/SKILL.md") {
		t.Fatalf("collapsed resources already show the expanded path:\n%s", renderLoaded())
	}

	app.key.OnToolsExpand()

	if !app.display.ToolOutputExpanded {
		t.Fatal("ToolOutputExpanded not set")
	}
	if !strings.Contains(renderLoaded(), "/skills/tdd/SKILL.md") {
		t.Fatalf("expanded resources missing the skill path:\n%s", renderLoaded())
	}
}

// TestFormatSkillDiagnostics covers the [Skill conflicts] body: collision
// groups keep the winner and list the skipped losers, like upstream
// formatDiagnostics.
func TestFormatSkillDiagnostics(t *testing.T) {
	rendered := coding.StripAnsi(formatSkillDiagnostics([]coding.ResourceDiagnostic{
		{Type: "collision", Message: "name \"dup\" collision", Collision: &coding.ResourceCollision{
			ResourceType: "skill", Name: "dup", WinnerPath: "/a/dup/SKILL.md", LoserPath: "/b/dup/SKILL.md",
		}},
		{Type: "warning", Message: "description is required", Path: "/c/SKILL.md"},
	}))
	for _, want := range []string{"\"dup\" collision:", "/a/dup/SKILL.md", "/b/dup/SKILL.md (skipped)", "/c/SKILL.md", "description is required"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("diagnostics missing %q:\n%s", want, rendered)
		}
	}
}
