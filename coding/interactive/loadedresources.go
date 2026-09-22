package interactive

import (
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/dat267/pier/coding"
	"github.com/dat267/pier/tui"
)

// Port of the startup "loaded resources" area of interactive-mode.ts
// (showLoadedResources and the source-scope display helpers).
//
// The Skills section is ported; Context/Prompts/Themes/Extensions are not yet
// (the Go loader exposes no themes/extensions and the context display needs
// formatContextPath). Extension mechanics stay out of scope (D41/D140).

var npmPackagePathPattern = regexp.MustCompile(`node_modules/(@?[^/]+(?:/[^/]+)?)/(.*)`)

var gitPackagePathPattern = regexp.MustCompile(`git/[^/]+/[^/]+/(.*)`)

// formatDisplayPath replaces the home directory with "~" (upstream
// formatDisplayPath).
func formatDisplayPath(p string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return p
	}
	if strings.HasPrefix(p, home) {
		return "~" + p[len(home):]
	}
	return p
}

// isPackageSource reports whether the resource came from an npm/git package.
func isPackageSource(sourceInfo coding.SourceInfo) bool {
	return strings.HasPrefix(sourceInfo.Source, "npm:") || strings.HasPrefix(sourceInfo.Source, "git:")
}

// getScopeGroup maps a source info to its display group (upstream
// getScopeGroup).
func getScopeGroup(sourceInfo coding.SourceInfo) string {
	source := sourceInfo.Source
	if source == "" {
		source = "local"
	}
	scope := sourceInfo.Scope
	if scope == "" {
		scope = coding.SourceScopeProject
	}
	if source == "cli" || scope == coding.SourceScopeTemporary {
		return "path"
	}
	if scope == coding.SourceScopeUser {
		return "user"
	}
	if scope == coding.SourceScopeProject {
		return "project"
	}
	return "path"
}

// getShortPath strips the package root for package resources (upstream
// getShortPath); everything else falls back to the display path.
func getShortPath(fullPath string, sourceInfo coding.SourceInfo) string {
	normalized := strings.ReplaceAll(fullPath, "\\", "/")
	source := sourceInfo.Source
	if match := npmPackagePathPattern.FindStringSubmatch(normalized); match != nil && strings.HasPrefix(source, "npm:") {
		return match[2]
	}
	if match := gitPackagePathPattern.FindStringSubmatch(normalized); match != nil && strings.HasPrefix(source, "git:") {
		return match[1]
	}
	return formatDisplayPath(fullPath)
}

// scopeGroupItem is one path with its source info.
type scopeGroupItem struct {
	path       string
	sourceInfo coding.SourceInfo
}

// scopeGroup is a display group of non-package paths plus package paths keyed
// by source.
type scopeGroup struct {
	scope    string
	paths    []scopeGroupItem
	packages map[string][]scopeGroupItem
}

// buildScopeGroups buckets items into project/user/path groups (upstream
// buildScopeGroups).
func buildScopeGroups(items []scopeGroupItem) []scopeGroup {
	groups := map[string]*scopeGroup{
		"user":    {scope: "user", packages: map[string][]scopeGroupItem{}},
		"project": {scope: "project", packages: map[string][]scopeGroupItem{}},
		"path":    {scope: "path", packages: map[string][]scopeGroupItem{}},
	}
	for _, item := range items {
		group := groups[getScopeGroup(item.sourceInfo)]
		source := item.sourceInfo.Source
		if source == "" {
			source = "local"
		}
		if isPackageSource(item.sourceInfo) {
			group.packages[source] = append(group.packages[source], item)
		} else {
			group.paths = append(group.paths, item)
		}
	}
	result := make([]scopeGroup, 0, 3)
	for _, key := range []string{"project", "user", "path"} {
		group := groups[key]
		if len(group.paths) > 0 || len(group.packages) > 0 {
			result = append(result, *group)
		}
	}
	return result
}

// formatScopeGroups renders the expanded scope listing (upstream
// formatScopeGroups).
func formatScopeGroups(groups []scopeGroup, formatPath func(scopeGroupItem) string, formatPackagePath func(scopeGroupItem, string) string) string {
	theme := ActiveTheme()
	var lines []string
	for _, group := range groups {
		lines = append(lines, "  "+theme.Fg("accent", group.scope))

		sortedPaths := append([]scopeGroupItem{}, group.paths...)
		sort.Slice(sortedPaths, func(i, j int) bool { return sortedPaths[i].path < sortedPaths[j].path })
		for _, item := range sortedPaths {
			lines = append(lines, theme.Fg("dim", "    "+formatPath(item)))
		}

		sources := make([]string, 0, len(group.packages))
		for source := range group.packages {
			sources = append(sources, source)
		}
		sort.Strings(sources)
		for _, source := range sources {
			lines = append(lines, "    "+theme.Fg("mdLink", source))
			items := append([]scopeGroupItem{}, group.packages[source]...)
			sort.Slice(items, func(i, j int) bool { return items[i].path < items[j].path })
			for _, item := range items {
				lines = append(lines, theme.Fg("dim", "      "+formatPackagePath(item, source)))
			}
		}
	}
	return strings.Join(lines, "\n")
}

// formatCompactList renders the collapsed list of labels (upstream
// formatCompactList): dim, two-space indent, sorted, comma separated.
func formatCompactList(labels []string) string {
	return formatCompactListSorted(labels, true)
}

// formatCompactListSorted renders the collapsed list with the sort optional;
// context files keep their discovery order (upstream {sort:false}).
func formatCompactListSorted(labels []string, sorted bool) string {
	trimmed := make([]string, 0, len(labels))
	for _, label := range labels {
		if value := strings.TrimSpace(label); value != "" {
			trimmed = append(trimmed, value)
		}
	}
	if sorted {
		sort.Strings(trimmed)
	}
	return ActiveTheme().Fg("dim", "  "+strings.Join(trimmed, ", "))
}

// formatContextPath renders a context file path relative to cwd when it is
// inside cwd, else as a display path (upstream formatContextPath).
func formatContextPath(p string, cwd string) string {
	absolute := coding.ResolvePath(p, cwd, coding.PathInputOptions{})
	if relative, ok := coding.GetCwdRelativePath(absolute, cwd); ok {
		return relative
	}
	return formatDisplayPath(absolute)
}

// formatSkillDiagnostics renders the skill warnings and name collisions
// (upstream formatDiagnostics, paths shown via formatDisplayPath).
func formatSkillDiagnostics(diagnostics []coding.ResourceDiagnostic) string {
	theme := ActiveTheme()
	var lines []string

	type collisionGroup struct {
		name      string
		winner    string
		losers    []string
		hasWinner bool
	}
	groups := map[string]*collisionGroup{}
	var order []string
	var others []coding.ResourceDiagnostic
	for _, diagnostic := range diagnostics {
		if diagnostic.Type == "collision" && diagnostic.Collision != nil {
			name := diagnostic.Collision.Name
			group, ok := groups[name]
			if !ok {
				group = &collisionGroup{name: name, winner: diagnostic.Collision.WinnerPath, hasWinner: true}
				groups[name] = group
				order = append(order, name)
			}
			group.losers = append(group.losers, diagnostic.Collision.LoserPath)
			continue
		}
		others = append(others, diagnostic)
	}

	for _, name := range order {
		group := groups[name]
		lines = append(lines, theme.Fg("warning", "  \""+name+"\" collision:"))
		if group.hasWinner {
			lines = append(lines, theme.Fg("dim", "    "+theme.Fg("success", "✓")+" "+formatDisplayPath(group.winner)))
		}
		for _, loser := range group.losers {
			lines = append(lines, theme.Fg("dim", "    "+theme.Fg("warning", "✗")+" "+formatDisplayPath(loser)+" (skipped)"))
		}
	}
	for _, diagnostic := range others {
		color := "warning"
		if diagnostic.Type == "error" {
			color = "error"
		}
		if diagnostic.Path != "" {
			lines = append(lines, theme.Fg(color, "  "+formatDisplayPath(diagnostic.Path)))
			lines = append(lines, theme.Fg(color, "    "+diagnostic.Message))
			continue
		}
		lines = append(lines, theme.Fg(color, "  "+diagnostic.Message))
	}
	return strings.Join(lines, "\n")
}

// ShowLoadedResources renders the loaded-resource sections into the container
// (upstream showLoadedResources). force bypasses the quiet-startup gate.
func (a *App) ShowLoadedResources(force bool) {
	if a == nil || a.LoadedResourcesContainer == nil {
		return
	}
	a.LoadedResourcesContainer.Clear()

	showListing := force || a.options.Verbose || !a.options.QuietStartup
	if !showListing {
		return
	}

	theme := ActiveTheme()
	sectionHeader := func(name string) string { return theme.Fg("mdHeading", "["+name+"]") }
	expanded := a.options.Verbose || a.UIState.ToolOutputExpanded
	addLoadedSection := func(name string, collapsed string, expandedBody string) {
		a.LoadedResourcesContainer.AddChild(NewExpandableText(
			func() string { return sectionHeader(name) + "\n" + collapsed },
			func() string { return sectionHeader(name) + "\n" + expandedBody },
			expanded, 0, 0,
		))
		a.LoadedResourcesContainer.AddChild(tui.NewSpacer(1))
	}

	contextFiles := a.Session.ContextFiles()
	if len(contextFiles) > 0 {
		cwd := a.options.Cwd
		paths := make([]string, 0, len(contextFiles))
		for _, file := range contextFiles {
			paths = append(paths, formatContextPath(file.Path, cwd))
		}
		expandedPaths := make([]string, 0, len(paths))
		for _, path := range paths {
			expandedPaths = append(expandedPaths, theme.Fg("dim", "  "+path))
		}
		// Context is the first section and keeps discovery order (upstream
		// {sort:false} and its leading spacer).
		a.LoadedResourcesContainer.AddChild(tui.NewSpacer(1))
		addLoadedSection("Context", formatCompactListSorted(paths, false), strings.Join(expandedPaths, "\n"))
	}

	skills := a.Session.Skills()
	if len(skills) > 0 {
		items := make([]scopeGroupItem, 0, len(skills))
		names := make([]string, 0, len(skills))
		for _, skill := range skills {
			items = append(items, scopeGroupItem{path: skill.FilePath, sourceInfo: skill.SourceInfo})
			names = append(names, skill.Name)
		}
		groups := buildScopeGroups(items)
		expandedBody := formatScopeGroups(groups,
			func(item scopeGroupItem) string { return formatDisplayPath(item.path) },
			func(item scopeGroupItem, source string) string { return getShortPath(item.path, item.sourceInfo) })
		addLoadedSection("Skills", formatCompactList(names), expandedBody)
	}

	templates := a.Session.PromptTemplates()
	if len(templates) > 0 {
		byPath := map[string]coding.PromptTemplate{}
		items := make([]scopeGroupItem, 0, len(templates))
		labels := make([]string, 0, len(templates))
		for _, template := range templates {
			byPath[template.FilePath] = template
			items = append(items, scopeGroupItem{path: template.FilePath, sourceInfo: template.SourceInfo})
			labels = append(labels, "/"+template.Name)
		}
		formatTemplate := func(item scopeGroupItem) string {
			if template, ok := byPath[item.path]; ok {
				return "/" + template.Name
			}
			return formatDisplayPath(item.path)
		}
		groups := buildScopeGroups(items)
		expandedBody := formatScopeGroups(groups,
			func(item scopeGroupItem) string { return formatTemplate(item) },
			func(item scopeGroupItem, source string) string { return formatTemplate(item) })
		addLoadedSection("Prompts", formatCompactList(labels), expandedBody)
	}

	diagnostics := a.Session.SkillDiagnostics()
	if len(diagnostics) > 0 {
		a.LoadedResourcesContainer.AddChild(tui.NewText(
			theme.Fg("warning", "[Skill conflicts]")+"\n"+formatSkillDiagnostics(diagnostics), 0, 0, nil))
		a.LoadedResourcesContainer.AddChild(tui.NewSpacer(1))
	}
}
