package coding

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"golang.org/x/text/unicode/norm"
)

// Port of utils/paths.ts and core/tools/path-utils.ts.

var unicodeSpaces = regexp.MustCompile(`[\x{00A0}\x{2000}-\x{200A}\x{202F}\x{205F}\x{3000}]`)

// PathInputOptions tune path normalization.
type PathInputOptions struct {
	// Trim strips leading/trailing whitespace first.
	Trim bool
	// ExpandTilde expands a leading ~. Nil means upstream's default (true).
	ExpandTilde *bool
	// HomeDir overrides the home directory for ~ expansion.
	HomeDir string
	// StripAtPrefix strips a leading @ (CLI @file paths).
	StripAtPrefix bool
	// NormalizeUnicodeSpaces rewrites unicode space variants to spaces.
	NormalizeUnicodeSpaces bool
}

// CanonicalizePath resolves to the canonical (real) form following symlinks,
// falling back to the raw path when resolution fails (port of
// canonicalizePath).
func CanonicalizePath(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	return path
}

// GetFileRevision returns an opaque file revision string used to detect
// changes without reading the file (port of getFileRevision). It returns
// ("", false) when the file cannot be stat'ed.
//
// dev/ino come from the platform stat structure; on platforms that do not
// expose them (Windows) they are zero, and the size/timestamps still make the
// revision change-detecting.
func GetFileRevision(path string) (string, bool) {
	info, err := os.Stat(path)
	if err != nil {
		return "", false
	}
	dev, ino := statDeviceInode(info)
	return fmt.Sprintf("%d:%d:%d:%d:%d", dev, ino, info.Size(),
		info.ModTime().UnixNano(), statChangeTimeNano(info)), true
}

// PathExists reports whether the path is accessible.
func PathExists(filePath string) bool {
	_, err := os.Stat(filePath)
	return err == nil
}

func homeDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return home
}

// NormalizePath normalizes path input: trim, unicode-space folding, @-prefix
// stripping, ~ expansion, and file:// URLs (port of normalizePath). The
// Windows shell-path rewrite is Windows-only and unported (D-row D7).
func NormalizePath(input string, options PathInputOptions) string {
	normalized := input
	if options.Trim {
		normalized = strings.TrimSpace(normalized)
	}
	if options.NormalizeUnicodeSpaces {
		normalized = unicodeSpaces.ReplaceAllString(normalized, " ")
	}
	if options.StripAtPrefix && strings.HasPrefix(normalized, "@") {
		normalized = normalized[1:]
	}
	expandTilde := true
	if options.ExpandTilde != nil {
		expandTilde = *options.ExpandTilde
	}
	if expandTilde {
		home := options.HomeDir
		if home == "" {
			home = homeDir()
		}
		if normalized == "~" {
			return home
		}
		if strings.HasPrefix(normalized, "~/") {
			return filepath.Join(home, normalized[2:])
		}
	}
	if strings.HasPrefix(normalized, "file://") {
		if path, err := filePathFromURL(normalized); err == nil {
			return path
		}
	}
	return normalized
}

// filePathFromURL converts a file:// URL to a path (fileURLToPath).
func filePathFromURL(url string) (string, error) {
	const scheme = "file://"
	if !strings.HasPrefix(url, scheme) {
		return "", fmtPathError("not a file URL")
	}
	path := url[len(scheme):]
	// file:///abs/path → path starts with '/' (empty host); file://host/path
	// carries a host component.
	if !strings.HasPrefix(path, "/") {
		idx := strings.Index(path, "/")
		if idx < 0 {
			return "", fmtPathError("file URL with no path")
		}
		host := path[:idx]
		if host != "localhost" {
			return "", fmtPathError("file URL with non-localhost host")
		}
		path = path[idx:]
	}
	// Percent-decoding.
	decoded, err := percentDecode(path)
	if err != nil {
		return "", err
	}
	return decoded, nil
}

func percentDecode(s string) (string, error) {
	var out []byte
	for i := 0; i < len(s); i++ {
		if s[i] == '%' {
			if i+2 >= len(s) {
				return "", fmtPathError("invalid percent encoding")
			}
			hi := hexValue(s[i+1])
			lo := hexValue(s[i+2])
			if hi < 0 || lo < 0 {
				return "", fmtPathError("invalid percent encoding")
			}
			out = append(out, byte(hi<<4|lo))
			i += 2
			continue
		}
		out = append(out, s[i])
	}
	return string(out), nil
}

func hexValue(c byte) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'a' && c <= 'f':
		return int(c-'a') + 10
	case c >= 'A' && c <= 'F':
		return int(c-'A') + 10
	default:
		return -1
	}
}

type pathError string

func (e pathError) Error() string { return string(e) }

func fmtPathError(msg string) error { return pathError(msg) }

// ResolvePath resolves a path relative to baseDir (port of resolvePath):
// absolute paths are cleaned; relative paths resolve against the base.
func ResolvePath(input string, baseDir string, options PathInputOptions) string {
	normalized := NormalizePath(input, options)
	normalizedBase := NormalizePath(baseDir, PathInputOptions{})
	if filepath.IsAbs(normalized) {
		return filepath.Clean(normalized)
	}
	return filepath.Clean(filepath.Join(normalizedBase, normalized))
}

// ExpandPath normalizes with unicode-space folding and @-stripping
// (port of expandPath).
func ExpandPath(filePath string) string {
	return NormalizePath(filePath, PathInputOptions{
		NormalizeUnicodeSpaces: true,
		StripAtPrefix:          true,
	})
}

// ResolveToCwd resolves a path relative to cwd (port of resolveToCwd).
func ResolveToCwd(filePath string, cwd string) string {
	return ResolvePath(filePath, cwd, PathInputOptions{
		NormalizeUnicodeSpaces: true,
		StripAtPrefix:          true,
	})
}

// macOS filename fallbacks (port of path-utils.ts).

const narrowNoBreakSpace = "\u202F"

func tryMacOSScreenshotPath(filePath string) string {
	re := regexp.MustCompile(` (?i)(AM|PM)\.`)
	return re.ReplaceAllString(filePath, narrowNoBreakSpace+"$1.")
}

func tryNFDVariant(filePath string) string {
	return norm.NFD.String(filePath)
}

func tryCurlyQuoteVariant(filePath string) string {
	return strings.ReplaceAll(filePath, "'", "\u2019")
}

// ResolveReadPath resolves a read path, trying macOS filename variants when
// the resolved path does not exist (port of resolveReadPath).
func ResolveReadPath(filePath string, cwd string) string {
	resolved := ResolveToCwd(filePath, cwd)

	if fileExists(resolved) {
		return resolved
	}

	amPmVariant := tryMacOSScreenshotPath(resolved)
	if amPmVariant != resolved && fileExists(amPmVariant) {
		return amPmVariant
	}

	nfdVariant := tryNFDVariant(resolved)
	if nfdVariant != resolved && fileExists(nfdVariant) {
		return nfdVariant
	}

	curlyVariant := tryCurlyQuoteVariant(resolved)
	if curlyVariant != resolved && fileExists(curlyVariant) {
		return curlyVariant
	}

	nfdCurlyVariant := tryCurlyQuoteVariant(nfdVariant)
	if nfdCurlyVariant != resolved && fileExists(nfdCurlyVariant) {
		return nfdCurlyVariant
	}

	return resolved
}

func fileExists(filePath string) bool {
	_, err := os.Stat(filePath)
	return err == nil
}

// GetCwdRelativePath returns the path relative to cwd when inside it
// (port of getCwdRelativePath); "" otherwise.
func GetCwdRelativePath(filePath string, cwd string) (string, bool) {
	resolvedCwd := ResolvePath(cwd, "", PathInputOptions{})
	resolvedPath := ResolvePath(filePath, resolvedCwd, PathInputOptions{})
	rel, err := filepath.Rel(resolvedCwd, resolvedPath)
	if err != nil {
		return "", false
	}
	isInsideCwd := rel == "" || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel))
	if !isInsideCwd {
		return "", false
	}
	if rel == "" {
		return ".", true
	}
	return rel, true
}

// IsLocalPath reports whether a resource argument names a filesystem path
// rather than a package source (port of isLocalPath). A file: URL counts as
// local, as upstream resolves it.
func IsLocalPath(value string) bool {
	trimmed := strings.TrimSpace(value)
	for _, prefix := range []string{"npm:", "git:", "github:", "http:", "https:", "ssh:"} {
		if strings.HasPrefix(trimmed, prefix) {
			return false
		}
	}
	return true
}

// ResolveCLIPaths resolves the local entries of a CLI resource-path list
// against cwd, leaving package sources untouched so a loader can reject them
// with its own message (port of resolveCliPaths). Nil stays nil.
func ResolveCLIPaths(cwd string, paths []string) []string {
	if paths == nil {
		return nil
	}
	out := make([]string, len(paths))
	for i, path := range paths {
		if IsLocalPath(path) {
			out[i] = ResolvePath(path, cwd, PathInputOptions{})
			continue
		}
		out[i] = path
	}
	return out
}
