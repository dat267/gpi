package interactive

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/dat267/pier/coding"
	"github.com/dat267/pier/tui"
)

// Port of src/modes/interactive/components/config-selector.ts: the package
// resource manager (/config) with the global/project write scopes.
//
// D99: the package manager (resource resolution) is out of scope (D41), so the
// resolved paths are injected as plain data instead of being produced by a
// resolver.

// ResourceType identifies a resource kind.
type ResourceType = string

// Resource types.
const (
	ResourceTypeExtensions ResourceType = "extensions"
	ResourceTypeSkills     ResourceType = "skills"
	ResourceTypePrompts    ResourceType = "prompts"
	ResourceTypeThemes     ResourceType = "themes"
)

// ConfigResourceTypes is the resource type order.
var ConfigResourceTypes = []ResourceType{
	ResourceTypeExtensions, ResourceTypeSkills, ResourceTypePrompts, ResourceTypeThemes,
}

var resourceTypeLabels = map[ResourceType]string{
	ResourceTypeExtensions: "Extensions",
	ResourceTypeSkills:     "Skills",
	ResourceTypePrompts:    "Prompts",
	ResourceTypeThemes:     "Themes",
}

// ConfigWriteScope is the scope the selector writes to.
type ConfigWriteScope = string

// Write scopes.
const (
	ConfigWriteScopeGlobal  ConfigWriteScope = "global"
	ConfigWriteScopeProject ConfigWriteScope = "project"
)

// ProjectOverrideState is a project override state.
type ProjectOverrideState = string

// Override states.
const (
	ProjectOverrideInherit ProjectOverrideState = "inherit"
	ProjectOverrideLoad    ProjectOverrideState = "load"
	ProjectOverrideUnload  ProjectOverrideState = "unload"
)

// ResolvedResource is one resolved resource (upstream package-manager).
type ResolvedResource struct {
	Path     string
	Enabled  bool
	Metadata coding.PathMetadata
}

// ResolvedPaths groups the resolved resources by type.
type ResolvedPaths struct {
	Extensions []ResolvedResource
	Skills     []ResolvedResource
	Prompts    []ResolvedResource
	Themes     []ResolvedResource
}

// ScopedResolvedPaths holds the global and project resolutions.
type ScopedResolvedPaths struct {
	Global  ResolvedPaths
	Project ResolvedPaths
}

// resourceItem is one toggleable resource.
type resourceItem struct {
	Path         string
	Enabled      bool
	Metadata     coding.PathMetadata
	ResourceType ResourceType
	DisplayName  string
	GroupKey     string
	SubgroupKey  string
}

// resourceSubgroup groups items of one type.
type resourceSubgroup struct {
	Type  ResourceType
	Label string
	Items []*resourceItem
}

// resourceGroup groups the subgroups of one source.
type resourceGroup struct {
	Key       string
	Label     string
	Scope     string
	Origin    string
	Source    string
	Subgroups []*resourceSubgroup
}

// formatBaseDir renders a base directory with a home prefix.
func formatBaseDir(baseDir string) string {
	homeDir, _ := os.UserHomeDir()
	var displayPath string
	switch {
	case baseDir == homeDir:
		displayPath = "~"
	case homeDir != "" && strings.HasPrefix(baseDir, homeDir):
		rest := baseDir[len(homeDir):]
		displayPath = "~" + strings.ReplaceAll(rest, "\\", "/")
	default:
		displayPath = strings.ReplaceAll(baseDir, "\\", "/")
	}
	if strings.HasSuffix(displayPath, "/") {
		return displayPath
	}
	return displayPath + "/"
}

func getGroupLabel(metadata coding.PathMetadata, agentDir string) string {
	if metadata.Origin == coding.SourceOriginPackage {
		return metadata.Source + " (" + metadata.Scope + ")"
	}
	if metadata.Source == "auto" {
		if metadata.BaseDir != "" {
			if metadata.Scope == coding.SourceScopeUser {
				return "User (" + formatBaseDir(metadata.BaseDir) + ")"
			}
			return "Project (" + formatBaseDir(metadata.BaseDir) + ")"
		}
		if metadata.Scope == coding.SourceScopeUser {
			return "User (" + formatBaseDir(agentDir) + ")"
		}
		return "Project (" + coding.ConfigDirName + "/)"
	}
	if metadata.Scope == coding.SourceScopeUser {
		return "User settings"
	}
	return "Project settings"
}

func resolvedResourcesByType(resolved ResolvedPaths, resourceType ResourceType) []ResolvedResource {
	switch resourceType {
	case ResourceTypeExtensions:
		return resolved.Extensions
	case ResourceTypeSkills:
		return resolved.Skills
	case ResourceTypePrompts:
		return resolved.Prompts
	default:
		return resolved.Themes
	}
}

// buildGroups groups the resolved resources by source (upstream buildGroups).
func buildGroups(resolved ResolvedPaths, agentDir string) []*resourceGroup {
	var groups []*resourceGroup
	groupMap := map[string]*resourceGroup{}

	addToGroup := func(resources []ResolvedResource, resourceType ResourceType) {
		for _, res := range resources {
			metadata := res.Metadata
			groupKey := metadata.Origin + ":" + metadata.Scope + ":" + metadata.Source + ":" + metadata.BaseDir

			group, ok := groupMap[groupKey]
			if !ok {
				group = &resourceGroup{
					Key:    groupKey,
					Label:  getGroupLabel(metadata, agentDir),
					Scope:  metadata.Scope,
					Origin: metadata.Origin,
					Source: metadata.Source,
				}
				groupMap[groupKey] = group
				groups = append(groups, group)
			}

			var subgroup *resourceSubgroup
			for _, candidate := range group.Subgroups {
				if candidate.Type == resourceType {
					subgroup = candidate
					break
				}
			}
			if subgroup == nil {
				subgroup = &resourceSubgroup{Type: resourceType, Label: resourceTypeLabels[resourceType]}
				group.Subgroups = append(group.Subgroups, subgroup)
			}

			fileName := filepath.Base(res.Path)
			parentFolder := filepath.Base(filepath.Dir(res.Path))
			var displayName string
			switch {
			case resourceType == ResourceTypeExtensions && parentFolder != "extensions":
				displayName = parentFolder + "/" + fileName
			case resourceType == ResourceTypeSkills && fileName == "SKILL.md":
				displayName = parentFolder
			default:
				displayName = fileName
			}
			subgroup.Items = append(subgroup.Items, &resourceItem{
				Path:         res.Path,
				Enabled:      res.Enabled,
				Metadata:     metadata,
				ResourceType: resourceType,
				DisplayName:  displayName,
				GroupKey:     groupKey,
				SubgroupKey:  groupKey + ":" + resourceType,
			})
		}
	}

	for _, resourceType := range ConfigResourceTypes {
		addToGroup(resolvedResourcesByType(resolved, resourceType), resourceType)
	}

	sort.SliceStable(groups, func(i int, j int) bool {
		a, b := groups[i], groups[j]
		if a.Origin != b.Origin {
			return a.Origin == coding.SourceOriginPackage
		}
		if a.Scope != b.Scope {
			return a.Scope == coding.SourceScopeUser
		}
		return a.Source < b.Source
	})

	typeOrder := map[ResourceType]int{
		ResourceTypeExtensions: 0, ResourceTypeSkills: 1, ResourceTypePrompts: 2, ResourceTypeThemes: 3,
	}
	for _, group := range groups {
		sort.SliceStable(group.Subgroups, func(i int, j int) bool {
			return typeOrder[group.Subgroups[i].Type] < typeOrder[group.Subgroups[j].Type]
		})
		for _, subgroup := range group.Subgroups {
			sort.SliceStable(subgroup.Items, func(i int, j int) bool {
				return subgroup.Items[i].DisplayName < subgroup.Items[j].DisplayName
			})
		}
	}
	return groups
}

type flatEntryKind int

const (
	flatGroup flatEntryKind = iota
	flatSubgroup
	flatItem
)

type flatEntry struct {
	Kind     flatEntryKind
	Group    *resourceGroup
	Subgroup *resourceSubgroup
	Item     *resourceItem
}

// ConfigSelectorHeader renders the scope title and hints.
type ConfigSelectorHeader struct {
	writeScope           ConfigWriteScope
	projectModeAvailable bool
}

// NewConfigSelectorHeader creates the header.
func NewConfigSelectorHeader(writeScope ConfigWriteScope, projectModeAvailable bool) *ConfigSelectorHeader {
	return &ConfigSelectorHeader{writeScope: writeScope, projectModeAvailable: projectModeAvailable}
}

// SetWriteScope updates the scope.
func (h *ConfigSelectorHeader) SetWriteScope(writeScope ConfigWriteScope) { h.writeScope = writeScope }

// Invalidate is a no-op.
func (h *ConfigSelectorHeader) Invalidate() {}

// Render renders the header.
func (h *ConfigSelectorHeader) Render(width int) []string {
	theme := ActiveTheme()
	titleText := "Global Resources"
	if h.writeScope == ConfigWriteScopeProject {
		titleText = "Project Local Resources"
	}
	title := theme.Bold(titleText)
	sep := theme.Fg("muted", " · ")
	switchHint := ""
	if h.projectModeAvailable {
		switchHint = KeyHint("tui.input.tab", "switch mode") + sep
	}
	actionHint := RawKeyHint("space", "toggle")
	if h.writeScope == ConfigWriteScopeProject {
		actionHint = RawKeyHint("space", "cycle inherit/+/-")
	}
	hint := switchHint + actionHint + sep + RawKeyHint("esc", "close")
	spacing := maxIntLocal(1, width-tui.VisibleWidth(title)-tui.VisibleWidth(hint))
	scopeHint := theme.Fg("muted", "~/"+coding.ConfigDirName+"/agent/settings.json")
	if h.writeScope == ConfigWriteScopeProject {
		scopeHint = theme.Fg("muted", coding.ConfigDirName+"/settings.json · inherited global resources are dimmed")
	}
	return []string{
		tui.TruncateToWidth(title+strings.Repeat(" ", spacing)+hint, width, "", false),
		tui.TruncateToWidth(scopeHint, width, "", false),
	}
}

// ResourceList is the focusable resource list.
type ResourceList struct {
	groupsByScope map[ConfigWriteScope][]*resourceGroup
	flatItems     []flatEntry
	filteredItems []flatEntry
	selectedIndex int
	searchInput   *tui.Input
	maxVisible    int
	settings      *coding.SettingsManager
	cwd           string
	agentDir      string
	writeScope    ConfigWriteScope
	inherited     map[string]bool

	OnCancel     func()
	OnExit       func()
	OnToggle     func(item *resourceItem, newEnabled bool)
	OnSwitchMode func()

	focused bool
}

// NewResourceList creates the resource list.
func NewResourceList(groupsByScope map[ConfigWriteScope][]*resourceGroup, settings *coding.SettingsManager, cwd string, agentDir string, terminalHeight int, writeScope ConfigWriteScope) *ResourceList {
	list := &ResourceList{
		groupsByScope: groupsByScope,
		settings:      settings,
		cwd:           cwd,
		agentDir:      agentDir,
		writeScope:    writeScope,
		searchInput:   tui.NewInput(tui.InputOptions{}),
	}
	list.inherited = list.buildInheritedEnabledMap(groupsByScope[ConfigWriteScopeGlobal])
	// 8 lines of chrome: top spacer + top border + spacer + header (2 lines) +
	// spacer + bottom spacer + bottom border.
	const chrome = 8
	if terminalHeight == 0 {
		terminalHeight = 24
	}
	list.maxVisible = maxIntLocal(5, terminalHeight-chrome)
	list.buildFlatList()
	list.filteredItems = append([]flatEntry{}, list.flatItems...)
	return list
}

// SetWriteScope switches the scope.
func (r *ResourceList) SetWriteScope(writeScope ConfigWriteScope) {
	r.writeScope = writeScope
	r.buildFlatList()
	r.filterItems(r.searchInput.Value())
}

// SetFocused focuses the list and its search input.
func (r *ResourceList) SetFocused(focused bool) {
	r.focused = focused
	r.searchInput.SetFocused(focused)
}

// Focused reports the focus state.
func (r *ResourceList) Focused() bool { return r.focused }

// Invalidate is a no-op.
func (r *ResourceList) Invalidate() {}

func (r *ResourceList) groups() []*resourceGroup { return r.groupsByScope[r.writeScope] }

func (r *ResourceList) buildInheritedEnabledMap(groups []*resourceGroup) map[string]bool {
	result := map[string]bool{}
	for _, group := range groups {
		for _, subgroup := range group.Subgroups {
			for _, item := range subgroup.Items {
				result[r.getResourceItemKey(item)] = item.Enabled
			}
		}
	}
	return result
}

func (r *ResourceList) buildFlatList() {
	r.flatItems = nil
	for _, group := range r.groups() {
		r.flatItems = append(r.flatItems, flatEntry{Kind: flatGroup, Group: group})
		for _, subgroup := range group.Subgroups {
			r.flatItems = append(r.flatItems, flatEntry{Kind: flatSubgroup, Subgroup: subgroup, Group: group})
			for _, item := range subgroup.Items {
				r.flatItems = append(r.flatItems, flatEntry{Kind: flatItem, Item: item})
			}
		}
	}
	r.selectedIndex = 0
	for index, entry := range r.flatItems {
		if entry.Kind == flatItem {
			r.selectedIndex = index
			return
		}
	}
}

func (r *ResourceList) findNextItem(fromIndex int, direction int) int {
	index := fromIndex + direction
	for index >= 0 && index < len(r.filteredItems) {
		if r.filteredItems[index].Kind == flatItem {
			return index
		}
		index += direction
	}
	return fromIndex
}

func (r *ResourceList) filterItems(query string) {
	if strings.TrimSpace(query) == "" {
		r.filteredItems = append([]flatEntry{}, r.flatItems...)
		r.selectFirstItem()
		return
	}

	lowerQuery := strings.ToLower(query)
	matchingItems := map[*resourceItem]bool{}
	matchingSubgroups := map[*resourceSubgroup]bool{}
	matchingGroups := map[*resourceGroup]bool{}

	for _, entry := range r.flatItems {
		if entry.Kind != flatItem {
			continue
		}
		item := entry.Item
		if strings.Contains(strings.ToLower(item.DisplayName), lowerQuery) ||
			strings.Contains(strings.ToLower(item.ResourceType), lowerQuery) ||
			strings.Contains(strings.ToLower(item.Path), lowerQuery) {
			matchingItems[item] = true
		}
	}

	for _, group := range r.groups() {
		for _, subgroup := range group.Subgroups {
			for _, item := range subgroup.Items {
				if matchingItems[item] {
					matchingSubgroups[subgroup] = true
					matchingGroups[group] = true
				}
			}
		}
	}

	r.filteredItems = nil
	for _, entry := range r.flatItems {
		switch entry.Kind {
		case flatGroup:
			if matchingGroups[entry.Group] {
				r.filteredItems = append(r.filteredItems, entry)
			}
		case flatSubgroup:
			if matchingSubgroups[entry.Subgroup] {
				r.filteredItems = append(r.filteredItems, entry)
			}
		case flatItem:
			if matchingItems[entry.Item] {
				r.filteredItems = append(r.filteredItems, entry)
			}
		}
	}

	r.selectFirstItem()
}

func (r *ResourceList) selectFirstItem() {
	r.selectedIndex = 0
	for index, entry := range r.filteredItems {
		if entry.Kind == flatItem {
			r.selectedIndex = index
			return
		}
	}
}

// UpdateItem sets the enabled state of an item in the groups.
func (r *ResourceList) UpdateItem(item *resourceItem, enabled bool) {
	item.Enabled = enabled
	for _, group := range r.groups() {
		for _, subgroup := range group.Subgroups {
			for _, found := range subgroup.Items {
				if found.Path == item.Path && found.ResourceType == item.ResourceType {
					found.Enabled = enabled
					return
				}
			}
		}
	}
}

// Render renders the list.
func (r *ResourceList) Render(width int) []string {
	theme := ActiveTheme()
	var lines []string
	lines = append(lines, r.searchInput.Render(width)...)
	lines = append(lines, "")

	if len(r.filteredItems) == 0 {
		lines = append(lines, theme.Fg("muted", "  No resources found"))
		return lines
	}

	startIndex := maxIntLocal(0, minIntLocal(r.selectedIndex-r.maxVisible/2, len(r.filteredItems)-r.maxVisible))
	endIndex := minIntLocal(startIndex+r.maxVisible, len(r.filteredItems))

	for i := startIndex; i < endIndex; i++ {
		entry := r.filteredItems[i]
		isSelected := i == r.selectedIndex

		switch entry.Kind {
		case flatGroup:
			inherited := r.writeScope == ConfigWriteScopeProject && entry.Group.Scope == coding.SourceScopeUser
			suffix := ""
			if inherited {
				suffix = " · inherited global"
			}
			color := "accent"
			if inherited {
				color = "dim"
			}
			groupLine := theme.Fg(color, theme.Bold(entry.Group.Label+suffix))
			lines = append(lines, tui.TruncateToWidth("  "+groupLine, width, "", false))
		case flatSubgroup:
			color := "muted"
			if r.writeScope == ConfigWriteScopeProject && entry.Group.Scope == coding.SourceScopeUser {
				color = "dim"
			}
			subgroupLine := theme.Fg(color, entry.Subgroup.Label)
			lines = append(lines, tui.TruncateToWidth("    "+subgroupLine, width, "", false))
		case flatItem:
			item := entry.Item
			cursor := "  "
			if isSelected {
				cursor = "> "
			}
			dimmed := r.isDimmedItem(item)
			nameText := item.DisplayName
			if isSelected && !dimmed {
				nameText = theme.Bold(nameText)
			}
			name := nameText
			if dimmed {
				name = theme.Fg("dim", nameText)
			}
			lines = append(lines, tui.TruncateToWidth(
				cursor+"    "+r.renderCheckbox(item)+" "+name+r.getItemSuffix(item), width, "...", false))
		}
	}

	if startIndex > 0 || endIndex < len(r.filteredItems) {
		itemCount := 0
		for _, entry := range r.filteredItems {
			if entry.Kind == flatItem {
				itemCount++
			}
		}
		currentItemIndex := 1
		for i := 0; i < r.selectedIndex && i < len(r.filteredItems); i++ {
			if r.filteredItems[i].Kind == flatItem {
				currentItemIndex++
			}
		}
		lines = append(lines, theme.Fg("dim", "  ("+itoa(currentItemIndex)+"/"+itoa(itemCount)+")"))
	}
	return lines
}

// HandleInput processes navigation, toggles and search input.
func (r *ResourceList) HandleInput(data string) {
	kb := tui.GetKeybindings()

	switch {
	case kb.Matches(data, "tui.select.up"):
		r.selectedIndex = r.findNextItem(r.selectedIndex, -1)
		return
	case kb.Matches(data, "tui.select.down"):
		r.selectedIndex = r.findNextItem(r.selectedIndex, 1)
		return
	case kb.Matches(data, "tui.select.pageUp"):
		target := maxIntLocal(0, r.selectedIndex-r.maxVisible)
		for target < len(r.filteredItems) && r.filteredItems[target].Kind != flatItem {
			target++
		}
		if target < len(r.filteredItems) {
			r.selectedIndex = target
		}
		return
	case kb.Matches(data, "tui.select.pageDown"):
		target := minIntLocal(len(r.filteredItems)-1, r.selectedIndex+r.maxVisible)
		for target >= 0 && r.filteredItems[target].Kind != flatItem {
			target--
		}
		if target >= 0 {
			r.selectedIndex = target
		}
		return
	case kb.Matches(data, "tui.select.cancel"):
		if r.OnCancel != nil {
			r.OnCancel()
		}
		return
	case tui.MatchesKey(data, "ctrl+c"):
		if r.OnExit != nil {
			r.OnExit()
		}
		return
	case kb.Matches(data, "tui.input.tab"):
		if r.OnSwitchMode != nil {
			r.OnSwitchMode()
		}
		return
	case data == " " || kb.Matches(data, "tui.select.confirm"):
		if r.selectedIndex >= 0 && r.selectedIndex < len(r.filteredItems) {
			entry := r.filteredItems[r.selectedIndex]
			if entry.Kind == flatItem &&
				(r.writeScope == ConfigWriteScopeProject || r.getItemScope(entry.Item) == coding.SourceScopeUser) {
				newEnabled, ok := r.toggleResource(entry.Item)
				if ok {
					r.UpdateItem(entry.Item, newEnabled)
					if r.OnToggle != nil {
						r.OnToggle(entry.Item, newEnabled)
					}
				}
			}
		}
		return
	}

	r.searchInput.HandleInput(data)
	r.filterItems(r.searchInput.Value())
}

func (r *ResourceList) toggleResource(item *resourceItem) (bool, bool) {
	if r.writeScope == ConfigWriteScopeProject {
		state := r.getNextOverrideState(item)
		if !r.setProjectResourceOverride(item, state) {
			return false, false
		}
		if state == ProjectOverrideInherit {
			return r.getInheritedEnabled(item), true
		}
		return state == ProjectOverrideLoad, true
	}

	enabled := !item.Enabled
	if item.Metadata.Origin == coding.SourceOriginTopLevel {
		r.toggleTopLevelResource(item, enabled)
	} else {
		r.togglePackageResource(item, enabled)
	}
	return enabled, true
}

func (r *ResourceList) toggleTopLevelResource(item *resourceItem, enabled bool) {
	scope := item.Metadata.Scope
	var settings *coding.Settings
	if scope == coding.SourceScopeProject {
		settings = r.settings.GetProjectSettings()
	} else {
		settings = r.settings.GetGlobalSettings()
	}

	current := settingsResourcePaths(settings, item.ResourceType)
	pattern := r.getResourcePattern(item)
	updated := filterResourcePatterns(current, pattern)
	if enabled {
		updated = append(updated, "+"+pattern)
	} else {
		updated = append(updated, "-"+pattern)
	}
	r.setTopLevelPaths(item.ResourceType, updated, scope == coding.SourceScopeProject)
}

func settingsResourcePaths(settings *coding.Settings, resourceType ResourceType) []string {
	switch resourceType {
	case ResourceTypeExtensions:
		return append([]string{}, settings.Extensions...)
	case ResourceTypeSkills:
		return append([]string{}, settings.Skills...)
	case ResourceTypePrompts:
		return append([]string{}, settings.Prompts...)
	default:
		return append([]string{}, settings.Themes...)
	}
}

// filterResourcePatterns drops the entries that target the given pattern.
func filterResourcePatterns(current []string, pattern string) []string {
	updated := make([]string, 0, len(current))
	for _, entry := range current {
		if patternEntryTarget(entry) != pattern {
			updated = append(updated, entry)
		}
	}
	return updated
}

func patternEntryTarget(entry string) string {
	if strings.HasPrefix(entry, "!") || strings.HasPrefix(entry, "+") || strings.HasPrefix(entry, "-") {
		return entry[1:]
	}
	return entry
}

func (r *ResourceList) setTopLevelPaths(resourceType ResourceType, paths []string, project bool) {
	switch resourceType {
	case ResourceTypeExtensions:
		if project {
			r.settings.SetProjectExtensionPaths(paths)
		} else {
			r.settings.SetExtensionPaths(paths)
		}
	case ResourceTypeSkills:
		if project {
			r.settings.SetProjectSkillPaths(paths)
		} else {
			r.settings.SetSkillPaths(paths)
		}
	case ResourceTypePrompts:
		if project {
			r.settings.SetProjectPromptTemplatePaths(paths)
		} else {
			r.settings.SetPromptTemplatePaths(paths)
		}
	default:
		if project {
			r.settings.SetProjectThemePaths(paths)
		} else {
			r.settings.SetThemePaths(paths)
		}
	}
}

func (r *ResourceList) togglePackageResource(item *resourceItem, enabled bool) {
	scope := item.Metadata.Scope
	project := scope == coding.SourceScopeProject
	var packages []any
	if project {
		packages = append([]any{}, r.settings.GetProjectSettings().Packages...)
	} else {
		packages = append([]any{}, r.settings.GetGlobalSettings().Packages...)
	}

	pkgIndex := -1
	for index, value := range packages {
		if packageSourceOf(value).Source == item.Metadata.Source {
			pkgIndex = index
			break
		}
	}
	if pkgIndex == -1 {
		return
	}

	pkg := packageSourceOf(packages[pkgIndex])
	current := append([]string{}, pkg.Filters[item.ResourceType]...)
	pattern := r.getPackageResourcePattern(item)
	updated := filterResourcePatterns(current, pattern)
	if enabled {
		updated = append(updated, "+"+pattern)
	} else {
		updated = append(updated, "-"+pattern)
	}
	pkg.SetFilters(item.ResourceType, updated)

	if pkg.HasFilters() {
		packages[pkgIndex] = pkg.ToAny(false)
	} else {
		packages[pkgIndex] = pkg.Source
	}

	if project {
		r.settings.SetProjectPackages(packages)
	} else {
		r.settings.SetPackages(packages)
	}
}

func (r *ResourceList) renderCheckbox(item *resourceItem) string {
	theme := ActiveTheme()
	if r.writeScope == ConfigWriteScopeProject {
		switch r.getProjectOverrideState(item) {
		case ProjectOverrideLoad:
			return theme.Fg("success", "[+]")
		case ProjectOverrideUnload:
			return theme.Fg("warning", "[-]")
		default:
			if item.Enabled {
				return theme.Fg("dim", "[x]")
			}
			return theme.Fg("dim", "[ ]")
		}
	}
	if item.Enabled {
		return theme.Fg("success", "[x]")
	}
	return theme.Fg("dim", "[ ]")
}

func (r *ResourceList) getItemSuffix(item *resourceItem) string {
	if r.writeScope != ConfigWriteScopeProject {
		return ""
	}
	theme := ActiveTheme()
	switch r.getProjectOverrideState(item) {
	case ProjectOverrideLoad:
		return theme.Fg("muted", "  project load")
	case ProjectOverrideUnload:
		return theme.Fg("muted", "  project unload")
	}
	if r.isInheritedGlobalItem(item) {
		return theme.Fg("dim", "  inherited global")
	}
	return ""
}

func (r *ResourceList) isDimmedItem(item *resourceItem) bool {
	return r.writeScope == ConfigWriteScopeProject &&
		r.isInheritedGlobalItem(item) &&
		r.getProjectOverrideState(item) == ProjectOverrideInherit
}

func (r *ResourceList) setProjectResourceOverride(item *resourceItem, state ProjectOverrideState) bool {
	if item.Metadata.Origin == coding.SourceOriginTopLevel {
		return r.setProjectTopLevelOverride(item, state)
	}
	return r.setProjectPackageOverride(item, state)
}

func (r *ResourceList) setProjectTopLevelOverride(item *resourceItem, state ProjectOverrideState) bool {
	current := settingsResourcePaths(r.settings.GetProjectSettings(), item.ResourceType)
	pattern := r.getResourcePatternForScope(item, coding.SourceScopeProject)
	if r.isInheritedGlobalItem(item) {
		pattern = item.Path
	}
	patterns := r.getTopLevelOverridePatterns(item, coding.SourceScopeProject)
	updated := make([]string, 0, len(current))
	for _, entry := range current {
		target := patternEntryTarget(entry)
		hasPrefix := strings.HasPrefix(entry, "!") || strings.HasPrefix(entry, "+") || strings.HasPrefix(entry, "-")
		if hasPrefix && patterns[target] {
			continue
		}
		if state == ProjectOverrideInherit && r.isInheritedGlobalItem(item) && target == pattern {
			continue
		}
		updated = append(updated, entry)
	}
	if state != ProjectOverrideInherit {
		if r.isInheritedGlobalItem(item) && !configContains(updated, pattern) {
			updated = append(updated, pattern)
		}
		prefix := "+"
		if state == ProjectOverrideUnload {
			prefix = "-"
		}
		updated = append(updated, prefix+pattern)
	}
	r.setTopLevelPaths(item.ResourceType, updated, true)
	return true
}

func (r *ResourceList) setProjectPackageOverride(item *resourceItem, state ProjectOverrideState) bool {
	packages := append([]any{}, r.settings.GetProjectSettings().Packages...)
	pkgIndex := -1
	for index, value := range packages {
		pkg := packageSourceOf(value)
		if r.packageSourceStringMatches(item.Metadata.Source, r.getItemScope(item), pkg.Source, coding.SourceScopeProject) {
			pkgIndex = index
			break
		}
	}
	if pkgIndex == -1 {
		if state == ProjectOverrideInherit {
			return false
		}
		created := r.createPackageOverrideSource(item)
		packages = append(packages, created.ToAny(true))
		pkgIndex = len(packages) - 1
	}

	pkg := packageSourceOf(packages[pkgIndex])
	pattern := r.getPackageResourcePattern(item)
	current := append([]string{}, pkg.Filters[item.ResourceType]...)
	updated := make([]string, 0, len(current))
	for _, entry := range current {
		if patternEntryTarget(entry) != pattern {
			updated = append(updated, entry)
		}
	}
	if state != ProjectOverrideInherit {
		prefix := "+"
		if state == ProjectOverrideUnload {
			prefix = "-"
		}
		updated = append(updated, prefix+pattern)
	}
	pkg.SetFilters(item.ResourceType, updated)

	if !pkg.HasFilters() {
		if pkg.AutoloadFalse() {
			packages = append(packages[:pkgIndex], packages[pkgIndex+1:]...)
		} else {
			packages[pkgIndex] = pkg.Source
		}
	} else {
		packages[pkgIndex] = pkg.ToAny(false)
	}
	r.settings.SetProjectPackages(packages)
	return true
}

func (r *ResourceList) getNextOverrideState(item *resourceItem) ProjectOverrideState {
	state := r.getProjectOverrideState(item)
	inheritedEnabled := r.getInheritedEnabled(item)
	switch state {
	case ProjectOverrideInherit:
		if inheritedEnabled {
			return ProjectOverrideUnload
		}
		return ProjectOverrideLoad
	case ProjectOverrideUnload:
		if inheritedEnabled {
			return ProjectOverrideLoad
		}
		return ProjectOverrideInherit
	default:
		if inheritedEnabled {
			return ProjectOverrideInherit
		}
		return ProjectOverrideUnload
	}
}

func (r *ResourceList) getProjectOverrideState(item *resourceItem) ProjectOverrideState {
	if r.writeScope != ConfigWriteScopeProject {
		return ProjectOverrideInherit
	}
	if item.Metadata.Origin == coding.SourceOriginTopLevel {
		return getOverrideStateFromEntries(
			settingsResourcePaths(r.settings.GetProjectSettings(), item.ResourceType),
			r.getTopLevelOverridePatterns(item, coding.SourceScopeProject),
			false,
		)
	}
	pkg := r.findMatchingPackageSource(item, coding.SourceScopeProject)
	if pkg == nil || !pkg.IsObject {
		return ProjectOverrideInherit
	}
	entries, ok := pkg.Filters[item.ResourceType]
	if !ok {
		return ProjectOverrideInherit
	}
	return getOverrideStateFromEntries(entries, map[string]bool{r.getPackageResourcePattern(item): true}, !pkg.AutoloadFalse())
}

func getOverrideStateFromEntries(entries []string, patterns map[string]bool, emptyArrayIsUnload bool) ProjectOverrideState {
	if len(entries) == 0 && emptyArrayIsUnload {
		return ProjectOverrideUnload
	}
	state := ProjectOverrideInherit
	for _, entry := range entries {
		if !patterns[patternEntryTarget(entry)] {
			continue
		}
		if strings.HasPrefix(entry, "!") || strings.HasPrefix(entry, "-") {
			state = ProjectOverrideUnload
		} else {
			state = ProjectOverrideLoad
		}
	}
	return state
}

func (r *ResourceList) getInheritedEnabled(item *resourceItem) bool {
	if enabled, ok := r.inherited[r.getResourceItemKey(item)]; ok {
		return enabled
	}
	if r.getItemScope(item) == coding.SourceScopeUser {
		return item.Enabled
	}
	return true
}

func (r *ResourceList) isInheritedGlobalItem(item *resourceItem) bool {
	return r.getItemScope(item) == coding.SourceScopeUser || r.inherited[r.getResourceItemKey(item)]
}

func (r *ResourceList) getTopLevelOverridePatterns(item *resourceItem, scope string) map[string]bool {
	baseDir := r.getTopLevelBaseDir(scope)
	patterns := map[string]bool{
		r.getResourcePatternForScope(item, scope): true,
		item.Path:                        true,
		relativePath(baseDir, item.Path): true,
	}
	if item.Metadata.BaseDir != "" {
		patterns[relativePath(item.Metadata.BaseDir, item.Path)] = true
	}
	return patterns
}

func (r *ResourceList) getResourcePatternForScope(item *resourceItem, scope string) string {
	sourceScope := r.getItemScope(item)
	if scope != sourceScope {
		return item.Path
	}
	baseDir := item.Metadata.BaseDir
	if baseDir == "" {
		baseDir = r.getTopLevelBaseDir(sourceScope)
	}
	return relativePath(baseDir, item.Path)
}

func (r *ResourceList) createPackageOverrideSource(item *resourceItem) packageSource {
	source := item.Metadata.Source
	if !configIsLocalPath(source) {
		autoload := false
		return packageSource{Source: source, Autoload: &autoload, IsObject: true}
	}
	sourcePath := coding.ResolvePath(source, r.getTopLevelBaseDir(r.getItemScope(item)), coding.PathInputOptions{Trim: true})
	relative := relativePath(r.getTopLevelBaseDir(coding.SourceScopeProject), sourcePath)
	if relative == "" {
		relative = "."
	}
	autoload := false
	return packageSource{Source: relative, Autoload: &autoload, IsObject: true}
}

func (r *ResourceList) packageSourceStringMatches(leftSource string, leftScope string, rightSource string, rightScope string) bool {
	if leftSource == rightSource {
		return true
	}
	if !configIsLocalPath(leftSource) || !configIsLocalPath(rightSource) {
		return false
	}
	left := coding.ResolvePath(leftSource, r.getTopLevelBaseDir(leftScope), coding.PathInputOptions{Trim: true})
	right := coding.ResolvePath(rightSource, r.getTopLevelBaseDir(rightScope), coding.PathInputOptions{Trim: true})
	return left == right
}

func (r *ResourceList) findMatchingPackageSource(item *resourceItem, targetScope string) *packageSource {
	var packages []any
	if targetScope == coding.SourceScopeProject {
		packages = r.settings.GetProjectSettings().Packages
	} else {
		packages = r.settings.GetGlobalSettings().Packages
	}
	for _, value := range packages {
		pkg := packageSourceOf(value)
		if r.packageSourceStringMatches(item.Metadata.Source, r.getItemScope(item), pkg.Source, targetScope) {
			return &pkg
		}
	}
	return nil
}

func (r *ResourceList) getResourceItemKey(item *resourceItem) string {
	return item.ResourceType + ":" + coding.CanonicalizePath(item.Path)
}

func (r *ResourceList) getItemScope(item *resourceItem) string {
	if item.Metadata.Scope == coding.SourceScopeProject {
		return coding.SourceScopeProject
	}
	return coding.SourceScopeUser
}

func (r *ResourceList) getTopLevelBaseDir(scope string) string {
	if scope == coding.SourceScopeProject {
		return filepath.Join(r.cwd, coding.ConfigDirName)
	}
	return r.agentDir
}

func (r *ResourceList) getResourcePattern(item *resourceItem) string {
	baseDir := item.Metadata.BaseDir
	if baseDir == "" {
		baseDir = r.getTopLevelBaseDir(item.Metadata.Scope)
	}
	return relativePath(baseDir, item.Path)
}

func (r *ResourceList) getPackageResourcePattern(item *resourceItem) string {
	baseDir := item.Metadata.BaseDir
	if baseDir == "" {
		baseDir = filepath.Dir(item.Path)
	}
	return relativePath(baseDir, item.Path)
}

func configContains(values []string, value string) bool {
	for _, entry := range values {
		if entry == value {
			return true
		}
	}
	return false
}

// relativePath is Node's path.relative: empty when equal, uses the OS
// separator.
func relativePath(from string, to string) string {
	relative, err := filepath.Rel(from, to)
	if err != nil {
		return to
	}
	if relative == "." {
		return ""
	}
	return relative
}

func configIsLocalPath(value string) bool {
	trimmed := strings.TrimSpace(value)
	for _, prefix := range []string{"npm:", "git:", "github:", "http:", "https:", "ssh:"} {
		if strings.HasPrefix(trimmed, prefix) {
			return false
		}
	}
	return true
}

// packageSource is the mutable view of a settings `packages` entry.
type packageSource struct {
	Source   string
	Autoload *bool
	Filters  map[string][]string
	IsObject bool
}

func packageSourceOf(value any) packageSource {
	switch typed := value.(type) {
	case string:
		return packageSource{Source: typed}
	case coding.SettingsPackageSource:
		return packageSourceFromSettings(typed.Source, typed.Autoload, map[ResourceType][]string{
			ResourceTypeExtensions: typed.Extensions,
			ResourceTypeSkills:     typed.Skills,
			ResourceTypePrompts:    typed.Prompts,
			ResourceTypeThemes:     typed.Themes,
		})
	case *coding.SettingsPackageSource:
		if typed == nil {
			return packageSource{}
		}
		return packageSourceFromSettings(typed.Source, typed.Autoload, map[ResourceType][]string{
			ResourceTypeExtensions: typed.Extensions,
			ResourceTypeSkills:     typed.Skills,
			ResourceTypePrompts:    typed.Prompts,
			ResourceTypeThemes:     typed.Themes,
		})
	case map[string]any:
		source, _ := typed["source"].(string)
		var autoload *bool
		if raw, ok := typed["autoload"].(bool); ok {
			autoload = &raw
		}
		filters := map[ResourceType][]string{}
		for _, resourceType := range ConfigResourceTypes {
			if raw, ok := typed[resourceType].([]any); ok {
				values := make([]string, 0, len(raw))
				for _, entry := range raw {
					if text, ok := entry.(string); ok {
						values = append(values, text)
					}
				}
				filters[resourceType] = values
			}
		}
		return packageSourceFromSettings(source, autoload, filters)
	}
	return packageSource{}
}

func packageSourceFromSettings(source string, autoload *bool, filters map[ResourceType][]string) packageSource {
	result := packageSource{Source: source, Autoload: autoload, Filters: map[ResourceType][]string{}}
	for resourceType, values := range filters {
		if values != nil {
			result.Filters[resourceType] = append([]string{}, values...)
		}
	}
	result.IsObject = autoload != nil || len(result.Filters) > 0
	return result
}

// SetFilters stores the filter entries for a resource type.
func (p *packageSource) SetFilters(resourceType ResourceType, values []string) {
	if p.Filters == nil {
		p.Filters = map[ResourceType][]string{}
	}
	if len(values) == 0 {
		delete(p.Filters, resourceType)
	} else {
		p.Filters[resourceType] = values
	}
	if len(values) > 0 {
		p.IsObject = true
	}
}

// HasFilters reports whether any resource type has entries.
func (p *packageSource) HasFilters() bool {
	for _, resourceType := range ConfigResourceTypes {
		if _, ok := p.Filters[resourceType]; ok {
			return true
		}
	}
	return false
}

// AutoloadFalse reports whether autoload is explicitly false.
func (p *packageSource) AutoloadFalse() bool { return p.Autoload != nil && !*p.Autoload }

// ToAny converts the view back to a settings value.
func (p *packageSource) ToAny(keepAutoload bool) any {
	if !p.HasFilters() && !keepAutoload {
		return p.Source
	}
	result := coding.SettingsPackageSource{Source: p.Source}
	if p.Autoload != nil {
		result.Autoload = p.Autoload
	}
	result.Extensions = p.Filters[ResourceTypeExtensions]
	result.Skills = p.Filters[ResourceTypeSkills]
	result.Prompts = p.Filters[ResourceTypePrompts]
	result.Themes = p.Filters[ResourceTypeThemes]
	return result
}

// ConfigSelectorComponent is the /config selector.
type ConfigSelectorComponent struct {
	*tui.Container

	header       *ConfigSelectorHeader
	resourceList *ResourceList
	writeScope   ConfigWriteScope
	focused      bool
}

// NewConfigSelectorComponent creates the selector.
func NewConfigSelectorComponent(resolvedPaths ScopedResolvedPaths, settings *coding.SettingsManager, cwd string, agentDir string, onClose func(), onExit func(), requestRender func(), terminalHeight int, writeScope ConfigWriteScope, projectModeAvailable bool) *ConfigSelectorComponent {
	component := &ConfigSelectorComponent{Container: &tui.Container{}, writeScope: writeScope}
	groupsByScope := map[ConfigWriteScope][]*resourceGroup{
		ConfigWriteScopeGlobal:  buildGroups(resolvedPaths.Global, agentDir),
		ConfigWriteScopeProject: buildGroups(resolvedPaths.Project, agentDir),
	}

	component.AddChild(tui.NewSpacer(1))
	component.AddChild(NewDynamicBorder(nil))
	component.AddChild(tui.NewSpacer(1))
	component.header = NewConfigSelectorHeader(writeScope, projectModeAvailable)
	component.AddChild(component.header)
	component.AddChild(tui.NewSpacer(1))

	component.resourceList = NewResourceList(groupsByScope, settings, cwd, agentDir, terminalHeight, writeScope)
	component.resourceList.OnCancel = onClose
	component.resourceList.OnExit = onExit
	component.resourceList.OnToggle = func(*resourceItem, bool) {
		if requestRender != nil {
			requestRender()
		}
	}
	if projectModeAvailable {
		component.resourceList.OnSwitchMode = func() {
			component.switchWriteScope()
			if requestRender != nil {
				requestRender()
			}
		}
	}
	component.AddChild(component.resourceList)

	component.AddChild(tui.NewSpacer(1))
	component.AddChild(NewDynamicBorder(nil))
	return component
}

func (c *ConfigSelectorComponent) switchWriteScope() {
	if c.writeScope == ConfigWriteScopeGlobal {
		c.writeScope = ConfigWriteScopeProject
	} else {
		c.writeScope = ConfigWriteScopeGlobal
	}
	c.header.SetWriteScope(c.writeScope)
	c.resourceList.SetWriteScope(c.writeScope)
}

// GetResourceList returns the resource list.
func (c *ConfigSelectorComponent) GetResourceList() *ResourceList { return c.resourceList }

// SetFocused focuses the component.
func (c *ConfigSelectorComponent) SetFocused(focused bool) {
	c.focused = focused
	c.resourceList.SetFocused(focused)
}

// Focused reports the focus state.
func (c *ConfigSelectorComponent) Focused() bool { return c.focused }

// HandleInput delegates to the resource list.
func (c *ConfigSelectorComponent) HandleInput(data string) { c.resourceList.HandleInput(data) }
