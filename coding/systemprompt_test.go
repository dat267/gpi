package coding

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// System prompt + skills tests keyed to upstream (system-prompt.ts,
// skills.ts).

func TestBuildSystemPromptSectionsDefault(t *testing.T) {
	sections, err := BuildSystemPromptSections(BuildSystemPromptOptions{Cwd: "/proj"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(sections["preamble"], "You are an expert coding assistant operating inside pi") {
		t.Fatalf("preamble = %q", sections["preamble"])
	}
	// Non-preamble sections are XML-wrapped.
	for _, name := range []string{"tools", "rules", "docs", "cwd"} {
		value, ok := sections[name]
		if !ok || !strings.HasPrefix(value, "<"+name+">\n") || !strings.HasSuffix(value, "\n</"+name+">") {
			t.Fatalf("section %s = %q", name, value)
		}
	}
	if !strings.Contains(sections["cwd"], "/proj") {
		t.Fatalf("cwd section = %q", sections["cwd"])
	}
	// Default rules present.
	if !strings.Contains(sections["rules"], "- Be concise in your responses") {
		t.Fatalf("rules = %q", sections["rules"])
	}
}

func TestBuildSystemPromptCustomPromptReplacesPreamble(t *testing.T) {
	sections, err := BuildSystemPromptSections(BuildSystemPromptOptions{Cwd: "/p", CustomPrompt: "My custom persona."})
	if err != nil {
		t.Fatal(err)
	}
	if sections["preamble"] != "My custom persona." {
		t.Fatalf("preamble = %q", sections["preamble"])
	}
	// No tools/rules/docs sections with a custom prompt.
	for _, name := range []string{"tools", "rules", "docs"} {
		if _, ok := sections[name]; ok {
			t.Fatalf("section %s should not exist with a custom prompt", name)
		}
	}
}

func TestBuildSystemPromptRulesFromToolGuidelines(t *testing.T) {
	sections, err := BuildSystemPromptSections(BuildSystemPromptOptions{
		Cwd:           "/p",
		SelectedTools: []string{"read", "bash"},
		ToolSnippets:  map[string]string{"read": "Read files", "bash": "Run commands"},
		ToolGuidelines: map[string][]string{
			"read": {"Use read to examine files instead of cat"},
			"bash": {"Use bash for file operations like ls, rg, find"},
		},
		PromptGuidelines: []string{"Extra guideline"},
	})
	if err != nil {
		t.Fatal(err)
	}
	rules := sections["rules"]
	for _, want := range []string{
		"- Use bash for file operations like ls, rg, find",
		"- Use read to examine files instead of cat",
		"- Extra guideline",
	} {
		if !strings.Contains(rules, want) {
			t.Fatalf("rules missing %q: %q", want, rules)
		}
	}
	// Deduplication: the same rule from two sources appears once.
	if strings.Count(rules, "Extra guideline") != 1 {
		t.Fatalf("dedup failed: %q", rules)
	}
}

func TestBuildSystemPromptSectionNameValidation(t *testing.T) {
	for _, bad := range []string{"Preamble", "1bad", "has space", "preamble"} {
		if _, err := BuildSystemPromptSections(BuildSystemPromptOptions{
			Cwd: "/p", Sections: map[string]string{bad: "x"},
		}); err == nil {
			t.Fatalf("section %q should be rejected", bad)
		}
	}
	if _, err := BuildSystemPromptSections(BuildSystemPromptOptions{
		Cwd: "/p", Sections: map[string]string{"custom_section": "x"},
	}); err != nil {
		t.Fatal(err)
	}
}

func TestBuildSystemPromptForced(t *testing.T) {
	content, sections, err := BuildSystemPromptState(BuildSystemPromptOptions{
		Cwd: "/p", ForceSystemPrompt: "Opaque prompt.",
	})
	if err != nil {
		t.Fatal(err)
	}
	if content != "Opaque prompt." || sections != nil {
		t.Fatalf("forced state = %q, %v", content, sections)
	}
}

func TestBuildSystemPromptFullRender(t *testing.T) {
	prompt, err := BuildSystemPrompt(BuildSystemPromptOptions{
		Cwd:          "/p",
		ContextFiles: []ContextFile{{Path: "AGENTS.md", Content: "Project rules here."}},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Section order: preamble, addendum?, project_context, cwd...
	parts := strings.Split(prompt, "\n\n")
	if !strings.HasPrefix(parts[0], "You are an expert coding assistant") {
		t.Fatalf("prompt head = %q", parts[0][:40])
	}
	if !strings.Contains(prompt, "<project_instructions path=\"AGENTS.md\">\nProject rules here.\n</project_instructions>") {
		t.Fatalf("project context missing: %q", prompt[len(prompt)-200:])
	}
	if !strings.HasSuffix(prompt, "</cwd>") {
		t.Fatal("cwd section must be last")
	}
}

func TestDiffSystemPromptSections(t *testing.T) {
	current := SystemPromptSections{"a": "1", "b": "2"}
	// No change → nil.
	if DiffSystemPromptSections(map[string]string{"a": "1", "b": "2"}, current) != nil {
		t.Fatal("no-change diff should be nil")
	}
	// Change + removal.
	patch := DiffSystemPromptSections(map[string]string{"a": "1", "b": "old", "c": "3"}, current)
	if patch == nil || patch["b"] == nil || *patch["b"] != "2" || patch["c"] != nil {
		t.Fatalf("patch = %v", patch)
	}
}

func TestSkillsDiscoveryAndFrontmatter(t *testing.T) {
	dir := t.TempDir()
	skillDir := filepath.Join(dir, "skills", "my-skill")
	os.MkdirAll(skillDir, 0o755)
	os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(`---
name: my-skill
description: Does a thing
disable-model-invocation: false
---

Skill body here.`), 0o644)

	result := LoadSkillsFromDir(filepath.Join(dir, "skills"))
	if len(result.Skills) != 1 {
		t.Fatalf("skills = %d (%v)", len(result.Skills), result.Diagnostics)
	}
	skill := result.Skills[0]
	if skill.Name != "my-skill" || skill.Description != "Does a thing" || skill.BaseDir != skillDir {
		t.Fatalf("skill = %+v", skill)
	}
	if !strings.HasSuffix(skill.FilePath, "SKILL.md") {
		t.Fatal("filePath should point at SKILL.md")
	}

	// disable-model-invocation hides from prompt.
	hidden := filepath.Join(dir, "skills", "hidden")
	os.MkdirAll(hidden, 0o755)
	os.WriteFile(filepath.Join(hidden, "SKILL.md"), []byte("---\ndescription: Hidden skill\ndisable-model-invocation: true\n---\n"), 0o644)
	result = LoadSkillsFromDir(filepath.Join(dir, "skills"))
	if len(result.Skills) != 2 {
		t.Fatalf("skills = %d", len(result.Skills))
	}
	prompt := FormatSkillsForPrompt(result.Skills, "read")
	if strings.Contains(prompt, "hidden") {
		t.Fatalf("hidden skill in prompt: %q", prompt)
	}

	// Name validation warnings (uppercase name).
	bad := filepath.Join(dir, "skills", "BadName")
	os.MkdirAll(bad, 0o755)
	os.WriteFile(filepath.Join(bad, "SKILL.md"), []byte("---\nname: BadName\ndescription: x\n---\n"), 0o644)
	result = LoadSkillsFromDir(filepath.Join(dir, "skills"))
	foundWarning := false
	for _, diag := range result.Diagnostics {
		if strings.Contains(diag.Message, "invalid characters") {
			foundWarning = true
		}
	}
	if !foundWarning {
		t.Fatalf("diagnostics = %+v", result.Diagnostics)
	}
	// The skill still loads (warnings, not errors).
	if len(result.Skills) != 3 {
		t.Fatalf("skills with warnings should still load: %d", len(result.Skills))
	}
}

func TestSkillsProjectTrustGate(t *testing.T) {
	cwd := t.TempDir()
	projectSkill := filepath.Join(cwd, ".pi", "skills", "proj")
	os.MkdirAll(projectSkill, 0o755)
	os.WriteFile(filepath.Join(projectSkill, "SKILL.md"), []byte("---\nname: proj\ndescription: project skill\n---\n"), 0o644)

	// Untrusted (default): project skills are NOT scanned.
	result := LoadSkills(LoadSkillsOptions{Cwd: cwd, IncludeDefaults: false}, false)
	if len(result.Skills) != 0 {
		t.Fatal("untrusted project skills must not load")
	}
	// Trusted: scanned.
	result = LoadSkills(LoadSkillsOptions{Cwd: cwd, IncludeDefaults: false}, true)
	if len(result.Skills) != 1 || result.Skills[0].Name != "proj" {
		t.Fatalf("trusted skills = %+v", result.Skills)
	}
}

func TestFormatSkillsForPromptXMLEscaping(t *testing.T) {
	prompt := FormatSkillsForPrompt([]Skill{{
		Name: "escape", Description: "Uses <tags> & \"quotes\" — 'apostrophes'", FilePath: "/x/SKILL.md",
	}}, "read")
	if !strings.Contains(prompt, "&lt;tags&gt; &amp; &quot;quotes&quot;") {
		t.Fatalf("xml escaping missing: %q", prompt)
	}
	if !strings.Contains(prompt, "&apos;apostrophes&apos;") {
		t.Fatalf("apos escaping missing: %q", prompt)
	}
}

func TestParseFrontmatterSubset(t *testing.T) {
	parsed := ParseFrontmatter("---\r\nname: 'quoted name'\r\ndescription: \"dq\"\r\nplain: bare value\r\nflag: true\r\n---\r\n\r\nBody text.")
	fm := parsed.Frontmatter
	if fm["name"] != "quoted name" || fm["description"] != "dq" || fm["plain"] != "bare value" {
		t.Fatalf("frontmatter = %v", fm)
	}
	if !parsed.Booleans["flag"] {
		t.Fatal("boolean parse failed")
	}
	if parsed.Body != "Body text." {
		t.Fatalf("body = %q", parsed.Body)
	}
	// No frontmatter.
	parsed = ParseFrontmatter("just body")
	if parsed.Body != "just body" || len(parsed.Frontmatter) != 0 {
		t.Fatalf("plain = %+v", parsed)
	}
}
