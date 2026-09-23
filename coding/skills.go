package coding

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Port of utils/frontmatter.ts (the YAML subset skill files use) and
// core/skills.ts.

// D-row D10: upstream parses YAML with the `yaml` package; the Go port
// implements the flat `key: value` subset the Agent Skills spec's
// frontmatter uses (string/boolean scalar keys, quoted or bare). Nested
// YAML structures are not supported.

// ParsedFrontmatter is the parse outcome.
type ParsedFrontmatter struct {
	Frontmatter map[string]string
	Booleans    map[string]bool
	Body        string
}

var bomRune = "\uFEFF"

// ParseFrontmatter splits and parses the skill file's frontmatter.
func ParseFrontmatter(content string) ParsedFrontmatter {
	normalized := strings.ReplaceAll(content, "\r\n", "\n")
	normalized = strings.ReplaceAll(normalized, "\r", "\n")
	normalized = strings.TrimPrefix(normalized, bomRune)

	result := ParsedFrontmatter{
		Frontmatter: map[string]string{},
		Booleans:    map[string]bool{},
	}
	if !strings.HasPrefix(normalized, "---") {
		result.Body = normalized
		return result
	}
	endIndex := strings.Index(normalized[3:], "\n---")
	if endIndex == -1 {
		result.Body = normalized
		return result
	}
	yamlString := normalized[4 : 3+endIndex+1]
	body := strings.TrimSpace(normalized[3+endIndex+4:])

	for _, line := range strings.Split(yamlString, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		colon := strings.Index(line, ":")
		if colon <= 0 {
			continue
		}
		key := strings.TrimSpace(line[:colon])
		value := strings.TrimSpace(line[colon+1:])
		// Strip surrounding quotes.
		if len(value) >= 2 && (value[0] == '"' && value[len(value)-1] == '"' || value[0] == '\'' && value[len(value)-1] == '\'') {
			value = value[1 : len(value)-1]
		}
		result.Frontmatter[key] = value
		if value == "true" {
			result.Booleans[key] = true
		}
	}
	result.Body = body
	return result
}

// StripFrontmatter returns the body only.
func StripFrontmatter(content string) string {
	return ParseFrontmatter(content).Body
}

// Skill is one discovered skill.
type Skill struct {
	Name        string
	Description string
	FilePath    string
	BaseDir     string
	// SourceInfo describes where the skill came from (upstream sourceInfo).
	SourceInfo SourceInfo
	// DisableModelInvocation hides the skill from the model prompt.
	DisableModelInvocation bool
}

// createSkillSourceInfo maps a loader source to its SourceInfo (upstream
// createSkillSourceInfo).
func createSkillSourceInfo(filePath string, baseDir string, source string) SourceInfo {
	switch source {
	case "user":
		return CreateSyntheticSourceInfo(filePath, "local", SourceScopeUser, SourceOriginTopLevel, baseDir)
	case "project":
		return CreateSyntheticSourceInfo(filePath, "local", SourceScopeProject, SourceOriginTopLevel, baseDir)
	case "path":
		return CreateSyntheticSourceInfo(filePath, "local", SourceScopeTemporary, SourceOriginTopLevel, baseDir)
	default:
		return CreateSyntheticSourceInfo(filePath, source, SourceScopeTemporary, SourceOriginTopLevel, baseDir)
	}
}

// ResourceCollision records which resource won a name collision (upstream
// ResourceDiagnostic.collision).
type ResourceCollision struct {
	ResourceType string
	Name         string
	WinnerPath   string
	LoserPath    string
}

// ResourceDiagnostic is a skill validation warning or name collision.
type ResourceDiagnostic struct {
	Type      string
	Message   string
	Path      string
	Collision *ResourceCollision
}

// LoadSkillsResult bundles skills and diagnostics.
type LoadSkillsResult struct {
	Skills      []Skill
	Diagnostics []ResourceDiagnostic
}

const maxSkillNameLength = 64

// validateSkillName validates per the Agent Skills spec.
// skillNameRegex matches a valid skill name: lowercase letters, digits and
// hyphens.
var skillNameRegex = regexp.MustCompile(`^[a-z0-9-]+$`)

func validateSkillName(name string) []string {
	var errors []string
	if len(name) > maxSkillNameLength {
		errors = append(errors, fmt.Sprintf("name exceeds %d characters (%d)", maxSkillNameLength, len(name)))
	}
	if !skillNameRegex.MatchString(name) {
		errors = append(errors, "name contains invalid characters (must be lowercase a-z, 0-9, hyphens only)")
	}
	if strings.HasPrefix(name, "-") || strings.HasSuffix(name, "-") {
		errors = append(errors, "name must not start or end with a hyphen")
	}
	if strings.Contains(name, "--") {
		errors = append(errors, "name must not contain consecutive hyphens")
	}
	return errors
}

// loadSkillFromFile parses one skill file.
func loadSkillFromFile(filePath string, source string) (*Skill, []ResourceDiagnostic) {
	var diagnostics []ResourceDiagnostic
	isDeclaredSkill := filepath.Base(filePath) == "SKILL.md"

	rawContent, err := os.ReadFile(filePath)
	if err != nil {
		diagnostics = append(diagnostics, ResourceDiagnostic{
			Type: "warning", Message: err.Error(), Path: filePath,
		})
		return nil, diagnostics
	}

	parsed := ParseFrontmatter(string(rawContent))
	description := parsed.Frontmatter["description"]
	hasDescription := strings.TrimSpace(description) != ""
	if !isDeclaredSkill && !hasDescription {
		return nil, diagnostics
	}

	skillDir := filepath.Dir(filePath)
	parentDirName := filepath.Base(skillDir)

	// Missing/empty description drops the skill (after validation warnings).
	if !hasDescription {
		if isDeclaredSkill {
			diagnostics = append(diagnostics, ResourceDiagnostic{
				Type: "warning", Message: "description is required", Path: filePath,
			})
		}
		return nil, diagnostics
	}

	name := parsed.Frontmatter["name"]
	if name == "" {
		name = parentDirName
	}
	for _, err := range validateSkillName(name) {
		diagnostics = append(diagnostics, ResourceDiagnostic{Type: "warning", Message: err, Path: filePath})
	}

	return &Skill{
		Name:                   name,
		Description:            description,
		FilePath:               filePath,
		BaseDir:                skillDir,
		SourceInfo:             createSkillSourceInfo(filePath, skillDir, source),
		DisableModelInvocation: parsed.Booleans["disable-model-invocation"],
	}, diagnostics
}

// LoadSkillsFromDir scans a directory tree: a root SKILL.md wins, then
// subdirectories are scanned recursively; dotfiles and node_modules are
// skipped (port of loadSkillsFromDir). The source identifier defaults to the
// temporary scope.
func LoadSkillsFromDir(dir string) LoadSkillsResult {
	return loadSkillsFromDir(dir, "")
}

// loadSkillsFromDir is LoadSkillsFromDir with the loader source identifier.
func loadSkillsFromDir(dir string, source string) LoadSkillsResult {
	var skills []Skill
	var diagnostics []ResourceDiagnostic
	if _, err := os.Stat(dir); err != nil {
		return LoadSkillsResult{}
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return LoadSkillsResult{}
	}

	// A root SKILL.md short-circuits (one skill per directory root).
	for _, entry := range entries {
		if entry.Name() != "SKILL.md" {
			continue
		}
		fullPath := filepath.Join(dir, entry.Name())
		if entry.IsDir() {
			continue
		}
		skill, diag := loadSkillFromFile(fullPath, source)
		if skill != nil {
			skills = append(skills, *skill)
		}
		diagnostics = append(diagnostics, diag...)
		return LoadSkillsResult{Skills: skills, Diagnostics: diagnostics}
	}

	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".") || entry.Name() == "node_modules" {
			continue
		}
		fullPath := filepath.Join(dir, entry.Name())
		if entry.IsDir() {
			sub := loadSkillsFromDir(fullPath, source)
			skills = append(skills, sub.Skills...)
			diagnostics = append(diagnostics, sub.Diagnostics...)
			continue
		}
		if !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		skill, diag := loadSkillFromFile(fullPath, source)
		if skill != nil {
			skills = append(skills, *skill)
		}
		diagnostics = append(diagnostics, diag...)
	}
	return LoadSkillsResult{Skills: skills, Diagnostics: diagnostics}
}

// LoadSkillsOptions are LoadSkills inputs.
type LoadSkillsOptions struct {
	Cwd             string
	AgentDir        string
	SkillPaths      []string
	IncludeDefaults bool
}

// LoadSkills loads skills from user and project locations. The project's
// `.pi/skills` is only scanned when trustProject is set (upstream's
// project-trust rule; pi answers "untrusted" headless by default).
func LoadSkills(options LoadSkillsOptions, trustProject bool) LoadSkillsResult {
	resolvedCwd := ResolvePath(options.Cwd, "", PathInputOptions{})
	resolvedAgentDir := ResolvePath(options.AgentDir, "", PathInputOptions{})

	// Insertion-ordered name map (upstream's Map preserves insertion order).
	var skills []Skill
	skillIndex := map[string]int{}
	realPathSet := map[string]bool{}
	var allDiagnostics []ResourceDiagnostic
	var collisionDiagnostics []ResourceDiagnostic

	addSkills := func(result LoadSkillsResult) {
		allDiagnostics = append(allDiagnostics, result.Diagnostics...)
		for _, skill := range result.Skills {
			// Resolve symlinks so the same file loaded twice is skipped silently.
			realPath := CanonicalizePath(skill.FilePath)
			if realPathSet[realPath] {
				continue
			}
			if existingIndex, ok := skillIndex[skill.Name]; ok {
				existing := skills[existingIndex]
				collisionDiagnostics = append(collisionDiagnostics, ResourceDiagnostic{
					Type: "collision", Message: "name \"" + skill.Name + "\" collision", Path: skill.FilePath,
					Collision: &ResourceCollision{
						ResourceType: "skill", Name: skill.Name,
						WinnerPath: existing.FilePath, LoserPath: skill.FilePath,
					},
				})
				continue
			}
			skillIndex[skill.Name] = len(skills)
			skills = append(skills, skill)
			realPathSet[realPath] = true
		}
	}

	userSkillsDir := filepath.Join(resolvedAgentDir, "skills")
	projectSkillsDir := filepath.Join(resolvedCwd, ".pi", "skills")
	isUnderPath := func(target string, root string) bool {
		normalizedRoot := filepath.Clean(root)
		if target == normalizedRoot {
			return true
		}
		prefix := normalizedRoot + string(filepath.Separator)
		return strings.HasPrefix(target, prefix)
	}

	if options.IncludeDefaults {
		addSkills(loadSkillsFromDir(userSkillsDir, "user"))
	}
	// The project skills dir stays gated on project trust (the Go loader's
	// added parameter; upstream gates it in the resource loader).
	if trustProject {
		addSkills(loadSkillsFromDir(projectSkillsDir, "project"))
	}

	getSource := func(resolvedPath string) string {
		if !options.IncludeDefaults {
			if isUnderPath(resolvedPath, userSkillsDir) {
				return "user"
			}
			if isUnderPath(resolvedPath, projectSkillsDir) {
				return "project"
			}
		}
		return "path"
	}

	for _, rawPath := range options.SkillPaths {
		resolvedPath := ResolvePath(rawPath, resolvedCwd, PathInputOptions{Trim: true})
		info, err := os.Stat(resolvedPath)
		if err != nil {
			allDiagnostics = append(allDiagnostics, ResourceDiagnostic{
				Type: "warning", Message: "skill path does not exist", Path: resolvedPath,
			})
			continue
		}
		source := getSource(resolvedPath)
		if info.IsDir() {
			addSkills(loadSkillsFromDir(resolvedPath, source))
			continue
		}
		if info.Mode().IsRegular() && strings.HasSuffix(resolvedPath, ".md") {
			skill, diag := loadSkillFromFile(resolvedPath, source)
			if skill != nil {
				addSkills(LoadSkillsResult{Skills: []Skill{*skill}})
			} else {
				allDiagnostics = append(allDiagnostics, diag...)
			}
			continue
		}
		allDiagnostics = append(allDiagnostics, ResourceDiagnostic{
			Type: "warning", Message: "skill path is not a markdown file", Path: resolvedPath,
		})
	}

	return LoadSkillsResult{Skills: skills, Diagnostics: append(allDiagnostics, collisionDiagnostics...)}
}

// escapeXML escapes XML entities (port of escapeXml).
func escapeXML(str string) string {
	str = strings.ReplaceAll(str, "&", "&amp;")
	str = strings.ReplaceAll(str, "<", "&lt;")
	str = strings.ReplaceAll(str, ">", "&gt;")
	str = strings.ReplaceAll(str, `"`, "&quot;")
	return strings.ReplaceAll(str, "'", "&apos;")
}

// FormatSkillsForPrompt renders the available_skills block (Agent Skills
// XML format). Skills with disableModelInvocation are excluded.
func FormatSkillsForPrompt(skills []Skill, fileReadTool string) string {
	var visible []Skill
	for _, skill := range skills {
		if !skill.DisableModelInvocation {
			visible = append(visible, skill)
		}
	}
	if len(visible) == 0 {
		return ""
	}
	lines := []string{
		"\n\nThe following skills provide specialized instructions for specific tasks.",
		"Use the read tool to load a skill's file when the task matches its description.",
		"When a skill file references a relative path, resolve it against the skill directory (parent of SKILL.md / dirname of the path) and use that absolute path in tool commands.",
		"",
		"<available_skills>",
	}
	if fileReadTool == "bash" {
		lines[1] = "Use bash to load a skill's file when the task matches its description."
	}
	for _, skill := range visible {
		lines = append(lines,
			"  <skill>",
			"    <name>"+escapeXML(skill.Name)+"</name>",
			"    <description>"+escapeXML(skill.Description)+"</description>",
			"    <location>"+escapeXML(skill.FilePath)+"</location>",
			"  </skill>")
	}
	lines = append(lines, "</available_skills>")
	return strings.Join(lines, "\n")
}
