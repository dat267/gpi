package coding

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

// Port of core/system-prompt.ts: system prompt construction and project
// context loading.

// SystemPromptSectionName validates section names.
var systemPromptSectionName = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)

// BuildSystemPromptOptions are the prompt inputs.
type BuildSystemPromptOptions struct {
	// CustomPrompt replaces the default prefix.
	CustomPrompt string
	// ForceSystemPrompt is an exact full prompt replacement.
	ForceSystemPrompt string
	// SelectedTools to include in the prompt (default: read, bash, edit, write).
	SelectedTools []string
	// ToolSnippets: one-line tool descriptions keyed by tool name.
	ToolSnippets map[string]string
	// ToolGuidelines: guideline bullets per tool.
	ToolGuidelines map[string][]string
	// PromptGuidelines appended to the default rules.
	PromptGuidelines []string
	// AppendSystemPrompt is appended before project context/skills/cwd.
	AppendSystemPrompt string
	// PromptSourcePaths are the loaded system/append prompt files (the
	// loaded-resources Context section lists them ahead of the context files).
	PromptSourcePaths []string
	// Sections: additional XML-wrapped prompt sections keyed by tag name.
	Sections map[string]string
	// Cwd is the working directory.
	Cwd string
	// ContextFiles are pre-loaded context files.
	ContextFiles []ContextFile
	// Skills are pre-loaded skills.
	Skills []Skill
}

// ContextFile is one project context file.
type ContextFile struct {
	Path    string
	Content string
}

// SystemPromptSections are the ordered, independently replaceable prompt
// sections keyed by name (preamble untagged, the rest XML-wrapped).
type SystemPromptSections map[string]string

// GetPackageDir resolves the pi package directory (docs/README paths). The
// Go port uses PI_PACKAGE_DIR when set, else the module root discovered at
// build time is unavailable — docs paths resolve relative to PI_PACKAGE_DIR
// only (upstream resolves its npm layout).
var (
	packageDirMu sync.Mutex
	packageDir   string
)

// SetPackageDir sets the docs root (host wiring).
func SetPackageDir(dir string) {
	packageDirMu.Lock()
	defer packageDirMu.Unlock()
	packageDir = dir
}

func currentPackageDir() string {
	packageDirMu.Lock()
	defer packageDirMu.Unlock()
	return packageDir
}

func GetReadmePath() string   { return resolveIn(currentPackageDir(), "README.md") }
func GetDocsPath() string     { return resolveIn(currentPackageDir(), "docs") }
func GetExamplesPath() string { return resolveIn(currentPackageDir(), "examples") }

// GetChangelogPath is the CHANGELOG.md path.
func GetChangelogPath() string { return resolveIn(currentPackageDir(), "CHANGELOG.md") }

// GetDebugLogPath is the interactive debug log path.
func GetDebugLogPath() string { return filepath.Join(GetAgentDir(), AppName+"-debug.log") }

func resolveIn(base, name string) string {
	if base == "" {
		return name
	}
	return fmt.Sprintf("%s/%s", strings.TrimRight(base, "/"), name)
}

func renderProjectContext(contextFiles []ContextFile) string {
	parts := []string{"Project-specific instructions and guidelines:"}
	for _, file := range contextFiles {
		parts = append(parts, fmt.Sprintf("<project_instructions path=%q>\n%s\n</project_instructions>", file.Path, file.Content))
	}
	return strings.Join(parts, "\n\n")
}

func buildRules(selectedTools []string, toolGuidelines map[string][]string, promptGuidelines []string) string {
	var rules []string
	seen := map[string]bool{}
	addRule := func(rule string) {
		normalized := strings.TrimSpace(rule)
		if normalized == "" || seen[normalized] {
			return
		}
		seen[normalized] = true
		rules = append(rules, normalized)
	}

	hasBash := contains(selectedTools, "bash")
	hasPowerShell := contains(selectedTools, "powershell")
	hasGrep := contains(selectedTools, "grep")
	hasFind := contains(selectedTools, "find")
	hasLs := contains(selectedTools, "ls")

	if (hasBash || hasPowerShell) && !hasGrep && !hasFind && !hasLs {
		switch {
		case hasBash && hasPowerShell:
			addRule("Use bash or PowerShell for file operations like listing, searching, and finding files")
		case hasPowerShell:
			addRule("Use PowerShell for file operations like listing, searching, and finding files")
		default:
			addRule("Use bash for file operations like ls, rg, find")
		}
	}

	for _, name := range selectedTools {
		for _, rule := range toolGuidelines[name] {
			addRule(rule)
		}
	}
	for _, rule := range promptGuidelines {
		addRule(rule)
	}
	addRule("Be concise in your responses")
	addRule("Show file paths clearly when working with files")
	var lines []string
	for _, rule := range rules {
		lines = append(lines, "- "+rule)
	}
	return strings.Join(lines, "\n")
}

func contains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

// BuildSystemPromptSections builds the ordered prompt sections
// (port of buildSystemPromptSections).
func BuildSystemPromptSections(input BuildSystemPromptOptions) (SystemPromptSections, error) {
	selectedTools := input.SelectedTools
	if len(selectedTools) == 0 {
		selectedTools = []string{"read", "bash", "edit", "write"}
	}
	toolSnippets := input.ToolSnippets
	toolGuidelines := input.ToolGuidelines
	promptGuidelines := input.PromptGuidelines
	appendSystemPrompt := input.AppendSystemPrompt
	customSections := input.Sections
	cwd := input.Cwd
	contextFiles := input.ContextFiles
	skills := input.Skills

	for name := range customSections {
		if !systemPromptSectionName.MatchString(name) || name == "preamble" {
			return nil, fmt.Errorf("Invalid system prompt section name: %s", name)
		}
	}

	promptSections := SystemPromptSections{}
	if input.CustomPrompt != "" {
		promptSections["preamble"] = input.CustomPrompt
	} else {
		promptSections["preamble"] = "You are an expert coding assistant operating inside pi, a coding agent harness. You help users by reading files, executing commands, editing code, and writing new files."
		var visibleTools []string
		for _, name := range selectedTools {
			if toolSnippets[name] != "" {
				visibleTools = append(visibleTools, name)
			}
		}
		var tools string
		if len(visibleTools) > 0 {
			var lines []string
			for _, name := range visibleTools {
				lines = append(lines, fmt.Sprintf("- %s: %s", name, toolSnippets[name]))
			}
			tools = strings.Join(lines, "\n")
		} else {
			tools = "(none)"
		}
		promptSections["tools"] = tools + "\n\nIn addition to the tools above, you may have access to other custom tools depending on the project."
		promptSections["rules"] = buildRules(selectedTools, toolGuidelines, promptGuidelines)
		promptSections["docs"] = fmt.Sprintf(`Pi documentation (read only when the user asks about pi itself, its SDK, extensions, themes, skills, or TUI):
- Main documentation: %s
- Additional docs: %s
- Examples: %s (extensions, custom tools, SDK)
- When reading pi docs or examples, resolve docs/... under Additional docs and examples/... under Examples, not the current working directory
- When asked about: extensions (docs/extensions.md, examples/extensions/), themes (docs/themes.md), skills (docs/skills.md), prompt templates (docs/prompt-templates.md), TUI components (docs/tui.md), keybindings (docs/keybindings.md), SDK integrations (docs/sdk.md), custom providers (docs/custom-provider.md), adding models (docs/models.md), pi packages (docs/packages.md), environment variables (docs/environment-variables.md)
- When working on pi topics, read the docs and examples, and follow .md cross-references before implementing
- Always read pi .md files completely and follow links to related docs (e.g., tui.md for TUI API details)`,
			GetReadmePath(), GetDocsPath(), GetExamplesPath())
	}

	if appendSystemPrompt != "" {
		promptSections["addendum"] = appendSystemPrompt
	}
	if len(contextFiles) > 0 {
		promptSections["project_context"] = renderProjectContext(contextFiles)
	}
	skillFileReadTool := ""
	for _, tool := range []string{"read", "bash"} {
		if contains(selectedTools, tool) {
			skillFileReadTool = tool
			break
		}
	}
	if skillFileReadTool != "" && len(skills) > 0 {
		skillsPrompt := strings.TrimSpace(FormatSkillsForPrompt(skills, skillFileReadTool))
		if skillsPrompt != "" {
			promptSections["skills"] = skillsPrompt
		}
	}
	promptSections["cwd"] = strings.ReplaceAll(cwd, "\\", "/")
	for name, content := range customSections {
		if content != "" {
			promptSections[name] = content
		}
	}

	sections := SystemPromptSections{"preamble": promptSections["preamble"]}
	// Upstream renders sections in INSERTION order: preamble, tools, rules,
	// docs, addendum, project_context, skills, cwd, then custom sections.
	knownOrder := []string{"tools", "rules", "docs", "addendum", "project_context", "skills", "cwd"}
	rendered := map[string]bool{"preamble": true}
	for _, name := range knownOrder {
		if content, ok := promptSections[name]; ok {
			sections[name] = fmt.Sprintf("<%s>\n%s\n</%s>", name, content, name)
			rendered[name] = true
		}
	}
	var customNames []string
	for name := range customSections {
		if !rendered[name] {
			customNames = append(customNames, name)
		}
	}
	sortNames(customNames)
	for _, name := range customNames {
		sections[name] = fmt.Sprintf("<%s>\n%s\n</%s>", name, promptSections[name], name)
	}
	return sections, nil
}

// sectionInsertionOrder recovers the render order: known sections in their
// fixed sequence, then custom sections sorted.
func sectionInsertionOrder(sections SystemPromptSections) []string {
	knownOrder := []string{"tools", "rules", "docs", "addendum", "project_context", "skills", "cwd"}
	var out []string
	for _, name := range knownOrder {
		if _, ok := sections[name]; ok {
			out = append(out, name)
		}
	}
	var custom []string
	for name := range sections {
		if !contains(knownOrder, name) && name != "preamble" {
			custom = append(custom, name)
		}
	}
	sortNames(custom)
	return append(out, custom...)
}

func sortNames(names []string) {
	for i := 1; i < len(names); i++ {
		for j := i; j > 0 && names[j] < names[j-1]; j-- {
			names[j], names[j-1] = names[j-1], names[j]
		}
	}
}

// BuildSystemPromptState returns the complete prompt state: a forced prompt
// is opaque (content only, no sections); otherwise the structured sections
// carry the prompt.
func BuildSystemPromptState(input BuildSystemPromptOptions) (content string, sections SystemPromptSections, err error) {
	if input.ForceSystemPrompt != "" {
		return input.ForceSystemPrompt, nil, nil
	}
	sections, err = BuildSystemPromptSections(input)
	if err != nil {
		return "", nil, err
	}
	return "", sections, nil
}

// BuildSystemPrompt renders the prompt text exactly as the transcript's
// system message replays it.
func BuildSystemPrompt(input BuildSystemPromptOptions) (string, error) {
	content, sections, err := BuildSystemPromptState(input)
	if err != nil {
		return "", err
	}
	// getSystemMessageText: content followed by section values in
	// insertion order — preamble first, then the section order fixed above.
	parts := []string{content}
	if sections != nil {
		orderedNames := append([]string{"preamble"}, sectionInsertionOrder(sections)...)
		for _, name := range orderedNames {
			if value, ok := sections[name]; ok {
				parts = append(parts, value)
			}
		}
	}
	var nonEmpty []string
	for _, part := range parts {
		if len(part) > 0 {
			nonEmpty = append(nonEmpty, part)
		}
	}
	return strings.Join(nonEmpty, "\n\n"), nil
}

// DiffSystemPromptSections diffs the model's current sections against the
// desired ones, returning a SystemMessage.sections patch (nil = no change).
func DiffSystemPromptSections(previous map[string]string, current SystemPromptSections) map[string]*string {
	patch := map[string]*string{}
	for name, text := range current {
		if previous[name] != text {
			value := text
			patch[name] = &value
		}
	}
	for name := range previous {
		if _, ok := current[name]; !ok {
			patch[name] = nil
		}
	}
	if len(patch) == 0 {
		return nil
	}
	return patch
}
