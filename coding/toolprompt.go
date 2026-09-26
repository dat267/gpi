package coding

// Tool prompt contributions: the one-line snippet and guideline bullets each
// built-in tool contributes to the system prompt (upstream
// `*ToolSystemPromptContribution`, read by `_rebuildSystemPrompt` into
// `toolSnippets`/`toolGuidelines`). The shell and edit contributions already
// live beside their tool definitions; the rest are declared here.

// builtinToolPromptSnippets are the one-line prompt snippets keyed by tool name.
var builtinToolPromptSnippets = map[string]string{
	ToolNameRead:       "Read file contents",
	ToolNameBash:       BashToolSystemPromptContribution.Snippet,
	ToolNamePowerShell: PowerShellToolSystemPromptContribution.Snippet,
	ToolNameEdit:       EditToolSystemPromptContribution,
	ToolNameWrite:      "Create or overwrite files",
	ToolNameGrep:       "Search file contents for patterns (respects .gitignore)",
	ToolNameFind:       "Find files by glob pattern (respects .gitignore)",
	ToolNameLS:         "List directory contents",
}

// builtinToolPromptGuidelines are the prompt guideline bullets keyed by tool
// name.
var builtinToolPromptGuidelines = map[string][]string{
	ToolNameRead:       {"Use read to examine files instead of cat or sed."},
	ToolNameBash:       BashToolSystemPromptContribution.Guidelines,
	ToolNamePowerShell: PowerShellToolSystemPromptContribution.Guidelines,
	ToolNameEdit:       EditToolSystemPromptGuidelines,
	ToolNameWrite:      {"Use write only for new files or complete rewrites."},
}

// toolPromptContributions returns the snippet and guideline maps for every
// registered tool (upstream `_rebuildSystemPrompt`'s toolSnippets and
// toolGuidelines).
func (s *AgentSession) toolPromptContributions() (map[string]string, map[string][]string) {
	snippets := map[string]string{}
	guidelines := map[string][]string{}
	for name := range s.control.Tools {
		if snippet, ok := builtinToolPromptSnippets[name]; ok && snippet != "" {
			snippets[name] = snippet
		}
		if bullets, ok := builtinToolPromptGuidelines[name]; ok && len(bullets) > 0 {
			guidelines[name] = append([]string{}, bullets...)
		}
	}
	return snippets, guidelines
}

// selectedRegistryTools deduplicates the requested names (first wins) and drops
// names that are not in the registry, preserving order (upstream
// `[...new Set(selectedTools)].filter((name) => registry.has(name))`).
func selectedRegistryTools(names []string, registry map[string]AgentToolDefinition) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(names))
	for _, name := range names {
		if seen[name] {
			continue
		}
		if _, ok := registry[name]; !ok {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	return out
}
