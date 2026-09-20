package coding

import (
	"os"
	"path"
	"regexp"
	"strconv"
	"strings"
)

// Port of src/utils/changelog.ts: changelog parsing and link normalization.

// ChangelogEntry is one changelog version section.
type ChangelogEntry struct {
	Major   int    `json:"major"`
	Minor   int    `json:"minor"`
	Patch   int    `json:"patch"`
	Content string `json:"content"`
}

// ChangelogGitHubRepo is the repository the changelog links point at.
const ChangelogGitHubRepo = "earendil-works/pi"

const changelogLinkBasePath = "packages/coding-agent"

var (
	// RE2 has no lookahead (D70), so the trailing "/ or end" rule is checked
	// manually in replaceLegacyRepoPrefix.
	legacyRepoPrefixRe   = regexp.MustCompile(`^https://github\.com/(?:badlogic|earendil-works)/pi-mono`)
	urlSchemeRe          = regexp.MustCompile(`^[a-z][a-z0-9+.-]*:`)
	inlineMarkdownLinkRe = regexp.MustCompile(`(!?\[[^\]\n]+\]\()([^\s)]+)((?:\s+[^)]*)?\))`)
	changelogVersionRe   = regexp.MustCompile(`##\s+\[?(\d+)\.(\d+)\.(\d+)\]?`)
)

func changelogEntryVersion(entry ChangelogEntry) string {
	return strconv.Itoa(entry.Major) + "." + strconv.Itoa(entry.Minor) + "." + strconv.Itoa(entry.Patch)
}

func normalizeChangelogTag(version string) string {
	if strings.HasPrefix(version, "v") {
		return version
	}
	return "v" + version
}

type changelogTargetParts struct {
	fragment string
	pathPart string
	query    string
}

func splitChangelogLocalTarget(target string) changelogTargetParts {
	hashIndex := strings.Index(target, "#")
	beforeHash := target
	fragment := ""
	if hashIndex != -1 {
		beforeHash = target[:hashIndex]
		fragment = target[hashIndex:]
	}
	queryIndex := strings.Index(beforeHash, "?")
	if queryIndex == -1 {
		return changelogTargetParts{fragment: fragment, pathPart: beforeHash}
	}
	return changelogTargetParts{
		fragment: fragment,
		pathPart: beforeHash[:queryIndex],
		query:    beforeHash[queryIndex:],
	}
}

func normalizeChangelogPathPart(value string) string { return strings.ReplaceAll(value, "\\", "/") }

// replaceLegacyRepoPrefix rewrites the legacy pi-mono repository prefix.
func replaceLegacyRepoPrefix(target string) string {
	loc := legacyRepoPrefixRe.FindStringIndex(target)
	if loc == nil {
		return target
	}
	rest := target[loc[1]:]
	if rest != "" && !strings.HasPrefix(rest, "/") {
		return target
	}
	return "https://github.com/" + ChangelogGitHubRepo + rest
}

func resolveChangelogRepositoryPath(targetPath string) (string, bool) {
	normalizedTarget := normalizeChangelogPathPart(targetPath)
	var joined string
	// path.posix.normalize keeps a trailing slash; path.Clean drops it, so
	// restore it explicitly.
	if strings.HasPrefix(normalizedTarget, "/") {
		joined = path.Clean(strings.TrimLeft(normalizedTarget, "/"))
	} else {
		joined = path.Clean(path.Join(changelogLinkBasePath, normalizedTarget))
	}
	if strings.HasSuffix(normalizedTarget, "/") && joined != "." && !strings.HasSuffix(joined, "/") {
		joined += "/"
	}
	if joined == "." || joined == ".." || strings.HasPrefix(joined, "../") {
		return "", false
	}
	return joined, true
}

func isChangelogDirectoryTarget(originalPath string, repositoryPath string) bool {
	if strings.HasSuffix(originalPath, "/") {
		return true
	}
	basename := path.Base(repositoryPath)
	return !strings.Contains(basename, ".")
}

func normalizeChangelogLinkTarget(target string, tag string) string {
	canonicalTarget := replaceLegacyRepoPrefix(target)
	repoURL := "https://github.com/" + ChangelogGitHubRepo

	for _, route := range []string{"blob", "tree"} {
		for _, branch := range []string{"main", "master"} {
			floatingRefPrefix := repoURL + "/" + route + "/" + branch + "/"
			if strings.HasPrefix(canonicalTarget, floatingRefPrefix) {
				canonicalTarget = repoURL + "/" + route + "/" + tag + "/" + canonicalTarget[len(floatingRefPrefix):]
			}
		}
	}

	if strings.HasPrefix(canonicalTarget, "#") || strings.HasPrefix(canonicalTarget, "//") ||
		urlSchemeRe.MatchString(canonicalTarget) {
		return canonicalTarget
	}

	parts := splitChangelogLocalTarget(canonicalTarget)
	if parts.pathPart == "" {
		return canonicalTarget
	}
	repositoryPath, ok := resolveChangelogRepositoryPath(parts.pathPart)
	if !ok {
		return canonicalTarget
	}
	route := "blob"
	if isChangelogDirectoryTarget(parts.pathPart, repositoryPath) {
		route = "tree"
	}
	return "https://github.com/" + ChangelogGitHubRepo + "/" + route + "/" + tag + "/" +
		encodeURI(repositoryPath) + parts.query + parts.fragment
}

// encodeURI mirrors JS encodeURI (it leaves the reserved set untouched).
func encodeURI(value string) string {
	var builder strings.Builder
	for _, r := range value {
		if isURIUnreserved(r) {
			builder.WriteRune(r)
			continue
		}
		for _, b := range []byte(string(r)) {
			builder.WriteString("%")
			const hex = "0123456789ABCDEF"
			builder.WriteByte(hex[b>>4])
			builder.WriteByte(hex[b&0xf])
		}
	}
	return builder.String()
}

func isURIUnreserved(r rune) bool {
	if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
		return true
	}
	switch r {
	case '-', '_', '.', '!', '~', '*', '\'', '(', ')', ';', ',', '/', '?', ':', '@', '&', '=', '+', '$', '#':
		return true
	}
	return false
}

// NormalizeChangelogLinks rewrites the relative links in changelog markdown to
// versioned GitHub URLs.
func NormalizeChangelogLinks(markdown string, version string) string {
	tag := normalizeChangelogTag(version)
	return inlineMarkdownLinkRe.ReplaceAllStringFunc(markdown, func(match string) string {
		groups := inlineMarkdownLinkRe.FindStringSubmatch(match)
		if groups == nil {
			return match
		}
		return groups[1] + normalizeChangelogLinkTarget(groups[2], tag) + groups[3]
	})
}

// ParseChangelog parses the changelog sections from a CHANGELOG.md file.
func ParseChangelog(changelogPath string) []ChangelogEntry {
	content, err := os.ReadFile(changelogPath)
	if err != nil {
		return nil
	}
	lines := strings.Split(string(content), "\n")
	var entries []ChangelogEntry
	var currentLines []string
	var current *ChangelogEntry

	for _, line := range lines {
		if strings.HasPrefix(line, "## ") {
			if current != nil && len(currentLines) > 0 {
				entry := *current
				entry.Content = strings.TrimSpace(strings.Join(currentLines, "\n"))
				entries = append(entries, entry)
			}
			match := changelogVersionRe.FindStringSubmatch(line)
			if match != nil {
				major, _ := strconv.Atoi(match[1])
				minor, _ := strconv.Atoi(match[2])
				patch, _ := strconv.Atoi(match[3])
				current = &ChangelogEntry{Major: major, Minor: minor, Patch: patch}
				currentLines = []string{line}
			} else {
				current = nil
				currentLines = nil
			}
			continue
		}
		if current != nil {
			currentLines = append(currentLines, line)
		}
	}

	if current != nil && len(currentLines) > 0 {
		entry := *current
		entry.Content = strings.TrimSpace(strings.Join(currentLines, "\n"))
		entries = append(entries, entry)
	}
	return entries
}

// CompareChangelogVersions returns -1, 0, or 1.
func CompareChangelogVersions(v1 ChangelogEntry, v2 ChangelogEntry) int {
	if v1.Major != v2.Major {
		return v1.Major - v2.Major
	}
	if v1.Minor != v2.Minor {
		return v1.Minor - v2.Minor
	}
	return v1.Patch - v2.Patch
}

// GetNewChangelogEntries returns the entries newer than lastVersion.
func GetNewChangelogEntries(entries []ChangelogEntry, lastVersion string) []ChangelogEntry {
	parts := strings.Split(lastVersion, ".")
	numbers := make([]int, 0, 3)
	for index := 0; index < 3; index++ {
		value := 0
		if index < len(parts) {
			parsed, err := strconv.Atoi(strings.TrimSpace(parts[index]))
			if err == nil {
				value = parsed
			}
		}
		numbers = append(numbers, value)
	}
	last := ChangelogEntry{Major: numbers[0], Minor: numbers[1], Patch: numbers[2]}
	var result []ChangelogEntry
	for _, entry := range entries {
		if CompareChangelogVersions(entry, last) > 0 {
			result = append(result, entry)
		}
	}
	return result
}
