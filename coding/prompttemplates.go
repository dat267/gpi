package coding

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Port of core/prompt-templates.ts and core/source-info.ts.

// SourceScope names where a resource came from.
type SourceScope = string

const (
	SourceScopeUser      SourceScope = "user"
	SourceScopeProject   SourceScope = "project"
	SourceScopeTemporary SourceScope = "temporary"
)

// SourceOrigin names how a resource was discovered.
type SourceOrigin = string

const (
	SourceOriginPackage  SourceOrigin = "package"
	SourceOriginTopLevel SourceOrigin = "top-level"
)

// SourceInfo describes one resource's origin (upstream SourceInfo).
type SourceInfo struct {
	Path    string
	Source  string
	Scope   SourceScope
	Origin  SourceOrigin
	BaseDir string
}

// CreateSyntheticSourceInfo builds a source info for a directly discovered
// resource (upstream createSyntheticSourceInfo).
func CreateSyntheticSourceInfo(path string, source string, scope SourceScope, origin SourceOrigin, baseDir string) SourceInfo {
	if scope == "" {
		scope = SourceScopeTemporary
	}
	if origin == "" {
		origin = SourceOriginTopLevel
	}
	return SourceInfo{Path: path, Source: source, Scope: scope, Origin: origin, BaseDir: baseDir}
}

// PromptTemplate is one prompt template loaded from a markdown file.
type PromptTemplate struct {
	Name            string
	Description     string
	ArgumentHint    string
	HasArgumentHint bool
	Content         string
	SourceInfo      SourceInfo
	FilePath        string
}

// ParseCommandArgs splits a command argument string honoring bash-style quotes.
func ParseCommandArgs(argsString string) []string {
	var args []string
	current := strings.Builder{}
	var inQuote rune
	hasQuote := false

	for _, char := range argsString {
		switch {
		case hasQuote:
			if char == inQuote {
				hasQuote = false
			} else {
				current.WriteRune(char)
			}
		case char == '"' || char == '\'':
			inQuote = char
			hasQuote = true
		case isJSWhitespace(char):
			if current.Len() > 0 {
				args = append(args, current.String())
				current.Reset()
			}
		default:
			current.WriteRune(char)
		}
	}
	if current.Len() > 0 {
		args = append(args, current.String())
	}
	return args
}

// isJSWhitespace matches the `\s` class for the ASCII/unicode spaces pi's
// templates use.
func isJSWhitespace(char rune) bool {
	switch char {
	case ' ', '\t', '\n', '\v', '\f', '\r', 0x00a0, 0x2028, 0x2029, 0xfeff:
		return true
	}
	if char >= 0x2000 && char <= 0x200a {
		return true
	}
	return char == 0x202f || char == 0x205f || char == 0x3000 || char == 0x1680
}

var argPlaceholderPattern = regexp.MustCompile(
	`\$\{(\d+|ARGUMENTS|@):-([^}]*)\}|\$\{@:(\d+)(?::(\d+))?\}|\$(ARGUMENTS|@|\d+)`)

// SubstituteArgs substitutes argument placeholders in template content.
//
// Supported: $1, $2, ... positional args; $@ and $ARGUMENTS for all args;
// ${N:-default} positional with default; ${@:-default} and ${ARGUMENTS:-default}
// for all args with a default; ${@:N} bash-style slicing; ${@:N:L} slice with
// length.
//
// Replacement happens on the template string only: argument and default values
// containing patterns like $1, $@, or $ARGUMENTS are not recursively
// substituted.
func SubstituteArgs(content string, args []string) string {
	allArgs := strings.Join(args, " ")
	matches := argPlaceholderPattern.FindAllStringSubmatchIndex(content, -1)
	if len(matches) == 0 {
		return content
	}
	var builder strings.Builder
	last := 0
	for _, match := range matches {
		builder.WriteString(content[last:match[0]])
		last = match[1]
		group := func(index int) (string, bool) {
			start, end := match[2*index], match[2*index+1]
			if start < 0 {
				return "", false
			}
			return content[start:end], true
		}
		if defaultTarget, ok := group(1); ok {
			defaultValue, _ := group(2)
			value := ""
			if defaultTarget == "@" || defaultTarget == "ARGUMENTS" {
				value = allArgs
			} else if index, err := strconv.Atoi(defaultTarget); err == nil && index-1 >= 0 && index-1 < len(args) {
				value = args[index-1]
			}
			if value != "" {
				builder.WriteString(value)
			} else {
				builder.WriteString(defaultValue)
			}
			continue
		}
		if sliceStart, ok := group(3); ok {
			start, _ := strconv.Atoi(sliceStart)
			start-- // User-provided indices are 1-based.
			if start < 0 {
				start = 0
			}
			if start > len(args) {
				start = len(args)
			}
			if sliceLength, hasLength := group(4); hasLength {
				length, _ := strconv.Atoi(sliceLength)
				end := start + length
				if end > len(args) {
					end = len(args)
				}
				builder.WriteString(strings.Join(args[start:end], " "))
			} else {
				builder.WriteString(strings.Join(args[start:], " "))
			}
			continue
		}
		simple, _ := group(5)
		if simple == "ARGUMENTS" || simple == "@" {
			builder.WriteString(allArgs)
			continue
		}
		index, err := strconv.Atoi(simple)
		if err == nil && index-1 >= 0 && index-1 < len(args) {
			builder.WriteString(args[index-1])
		}
	}
	builder.WriteString(content[last:])
	return builder.String()
}

// LoadTemplateFromFile loads one template, or nil on read/parse failure.
func LoadTemplateFromFile(filePath string, sourceInfo SourceInfo) *PromptTemplate {
	raw, err := os.ReadFile(filePath)
	if err != nil {
		return nil
	}
	parsed := ParseFrontmatter(string(raw))

	name := strings.TrimSuffix(filepath.Base(filePath), ".md")

	description := parsed.Frontmatter["description"]
	if description == "" {
		for _, line := range strings.Split(parsed.Body, "\n") {
			if strings.TrimSpace(line) == "" {
				continue
			}
			description = line
			if len(line) > 60 {
				description = line[:60] + "..."
			}
			break
		}
	}

	template := &PromptTemplate{
		Name:        name,
		Description: description,
		Content:     parsed.Body,
		SourceInfo:  sourceInfo,
		FilePath:    filePath,
	}
	if hint, ok := parsed.Frontmatter["argument-hint"]; ok && hint != "" {
		template.ArgumentHint = hint
		template.HasArgumentHint = true
	}
	return template
}

// LoadTemplatesFromDir scans one directory (non-recursive) for .md templates.
func LoadTemplatesFromDir(dir string, getSourceInfo func(filePath string) SourceInfo) []PromptTemplate {
	var templates []PromptTemplate
	entries, err := os.ReadDir(dir)
	if err != nil {
		return templates
	}
	for _, entry := range entries {
		fullPath := filepath.Join(dir, entry.Name())
		isFile := entry.Type().IsRegular()
		if entry.Type()&os.ModeSymlink != 0 {
			info, err := os.Stat(fullPath)
			if err != nil {
				continue // Broken symlink.
			}
			isFile = info.Mode().IsRegular()
		}
		if !isFile || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		if template := LoadTemplateFromFile(fullPath, getSourceInfo(fullPath)); template != nil {
			templates = append(templates, *template)
		}
	}
	return templates
}

// LoadPromptTemplatesOptions configure LoadPromptTemplates.
type LoadPromptTemplatesOptions struct {
	Cwd             string
	AgentDir        string
	PromptPaths     []string
	IncludeDefaults bool
}

// LoadPromptTemplates loads global, project, and explicit prompt templates.
func LoadPromptTemplates(options LoadPromptTemplatesOptions) []PromptTemplate {
	resolvedCwd := ResolvePath(options.Cwd, ".", PathInputOptions{})
	resolvedAgentDir := ResolvePath(options.AgentDir, ".", PathInputOptions{})

	var templates []PromptTemplate
	globalPromptsDir := filepath.Join(resolvedAgentDir, "prompts")
	projectPromptsDir := filepath.Join(resolvedCwd, ConfigDirName, "prompts")

	isUnderPath := func(target, root string) bool {
		normalizedRoot := filepath.Clean(root)
		if target == normalizedRoot {
			return true
		}
		return strings.HasPrefix(target, normalizedRoot+string(filepath.Separator))
	}

	getSourceInfo := func(resolvedPath string) SourceInfo {
		if isUnderPath(resolvedPath, globalPromptsDir) {
			return CreateSyntheticSourceInfo(resolvedPath, "local", SourceScopeUser, "", globalPromptsDir)
		}
		if isUnderPath(resolvedPath, projectPromptsDir) {
			return CreateSyntheticSourceInfo(resolvedPath, "local", SourceScopeProject, "", projectPromptsDir)
		}
		info, err := os.Stat(resolvedPath)
		baseDir := filepath.Dir(resolvedPath)
		if err == nil && info.IsDir() {
			baseDir = resolvedPath
		}
		return CreateSyntheticSourceInfo(resolvedPath, "local", "", "", baseDir)
	}

	if options.IncludeDefaults {
		templates = append(templates, LoadTemplatesFromDir(globalPromptsDir, getSourceInfo)...)
		templates = append(templates, LoadTemplatesFromDir(projectPromptsDir, getSourceInfo)...)
	}

	for _, rawPath := range options.PromptPaths {
		resolvedPath := ResolvePath(rawPath, resolvedCwd, PathInputOptions{Trim: true})
		info, err := os.Stat(resolvedPath)
		if err != nil {
			continue
		}
		switch {
		case info.IsDir():
			templates = append(templates, LoadTemplatesFromDir(resolvedPath, getSourceInfo)...)
		case info.Mode().IsRegular() && strings.HasSuffix(resolvedPath, ".md"):
			if template := LoadTemplateFromFile(resolvedPath, getSourceInfo(resolvedPath)); template != nil {
				templates = append(templates, *template)
			}
		}
	}
	return templates
}

var promptCommandPattern = regexp.MustCompile(`^/([^\s]+)(?:\s+([\s\S]*))?$`)

// ExpandPromptTemplate expands a "/name args" command when it matches a
// template, returning the original text otherwise (upstream
// expandPromptTemplate).
func ExpandPromptTemplate(text string, templates []PromptTemplate) string {
	if !strings.HasPrefix(text, "/") {
		return text
	}
	match := promptCommandPattern.FindStringSubmatch(text)
	if match == nil {
		return text
	}
	templateName := match[1]
	argsString := match[2]
	for _, template := range templates {
		if template.Name != templateName {
			continue
		}
		return SubstituteArgs(template.Content, ParseCommandArgs(argsString))
	}
	return text
}
