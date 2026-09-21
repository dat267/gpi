package coding

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"

	"github.com/dat267/pier/agent"
	"github.com/dat267/pier/ai"
)

// Port of core/tools/find.ts. The default implementation shells out to `fd`
// (upstream ensureTool downloads it when missing — the download manager is
// host surface and unported; PATH lookup is the Go equivalent).

const findDefaultLimit = 1000

// FindToolDescription is upstream's find description.
const FindToolDescription = "Search for files by glob pattern. Returns matching file paths relative to the search directory. Respects .gitignore. Output is truncated to 1000 results or 50KB (whichever is hit first)."

// FindToolDetails carries find truncation info.
type FindToolDetails struct {
	Truncation         *TruncationResult `json:"truncation,omitempty"`
	ResultLimitReached *int              `json:"resultLimitReached,omitempty"`
}

var findSchemaJSON = mustSchemaJSON(map[string]any{
	"type": "object",
	"properties": map[string]any{
		"pattern": map[string]any{"type": "string", "description": "Glob pattern to match files, e.g. '*.ts', '**/*.json', or 'src/**/*.spec.ts'"},
		"path":    map[string]any{"type": "string", "description": "Directory to search in (default: current directory)"},
		"limit":   numSchema("Maximum number of results (default: 1000)"),
	},
	"required": []string{"pattern"},
})

// RelativizeFindResultPath relativizes a find result against the search
// root and normalizes to posix separators.
func RelativizeFindResultPath(resultPath string, searchPath string) string {
	hadTrailingSeparator := strings.HasSuffix(resultPath, "/")
	relativePath := resultPath
	if path.IsAbs(resultPath) {
		if rel, err := filepath.Rel(searchPath, resultPath); err == nil {
			relativePath = rel
		}
	}
	posixPath := filepath.ToSlash(relativePath)
	if hadTrailingSeparator && !strings.HasSuffix(posixPath, "/") {
		return posixPath + "/"
	}
	return posixPath
}

// CreateFindTool builds the find tool.
func CreateFindTool(cwd string) agent.AgentTool {
	return agent.AgentTool{
		Name:        "find",
		Description: FindToolDescription,
		Parameters:  findSchemaJSON,
		Label:       "find",
		Execute: func(toolCallID string, params json.RawMessage, ctx context.Context, onUpdate func(agent.AgentToolResult)) (agent.AgentToolResult, error) {
			if ctxErrOf(ctx) != nil {
				return agent.AgentToolResult{}, fmt.Errorf("Operation aborted")
			}
			var input struct {
				Pattern string `json:"pattern"`
				Path    string `json:"path"`
				Limit   *int   `json:"limit"`
			}
			if len(params) > 0 {
				if err := jsonUnmarshalStrictTool(params, &input); err != nil {
					return agent.AgentToolResult{}, err
				}
			}
			searchPath := ResolveToCwd(orDefault(input.Path, "."), cwd)
			effectiveLimit := findDefaultLimit
			if input.Limit != nil {
				effectiveLimit = *input.Limit
			}

			if _, err := os.Stat(searchPath); err != nil {
				return agent.AgentToolResult{}, fmt.Errorf("Path not found: %s", searchPath)
			}

			fdPath, err := exec.LookPath("fd")
			if err != nil {
				return agent.AgentToolResult{}, fmt.Errorf("fd is not available and could not be downloaded")
			}
			if ctxErrOf(ctx) != nil {
				return agent.AgentToolResult{}, fmt.Errorf("Operation aborted")
			}

			args := []string{"--glob", "--color=never", "--hidden"}
			// fd normally ignores .gitignore outside git repos, so keep
			// --no-require-git there; inside repos use fd's git-aware default
			// so parent .gitignore rules stop at nested repo boundaries.
			insideGitRepo := false
			for current := searchPath; ; {
				if fileExists(filepath.Join(current, ".git")) {
					insideGitRepo = true
					break
				}
				parent := filepath.Dir(current)
				if parent == current {
					break
				}
				current = parent
			}
			if !insideGitRepo {
				args = append(args, "--no-require-git")
			}
			args = append(args, "--max-results", fmt.Sprintf("%d", effectiveLimit))

			// fd --glob matches basenames unless --full-path; a path-
			// containing pattern needs a leading **/ to match anything.
			effectivePattern := input.Pattern
			if strings.Contains(input.Pattern, "/") {
				args = append(args, "--full-path")
				if !strings.HasPrefix(input.Pattern, "/") && !strings.HasPrefix(input.Pattern, "**/") && input.Pattern != "**" {
					effectivePattern = "**/" + effectivePattern
				}
			}
			args = append(args, "--", effectivePattern, searchPath)

			cmd := exec.CommandContext(ctx, fdPath, args...)
			var stdout, stderr strings.Builder
			cmd.Stdout = &stdout
			cmd.Stderr = &stderr
			runErr := cmd.Run()
			if ctxErrOf(ctx) != nil {
				return agent.AgentToolResult{}, fmt.Errorf("Operation aborted")
			}
			output := strings.TrimRight(stdout.String(), "\n")
			if runErr != nil {
				errorMsg := strings.TrimSpace(stderr.String())
				if errorMsg == "" {
					errorMsg = fmt.Sprintf("fd exited with code %d", cmd.ProcessState.ExitCode())
				}
				if output == "" {
					return agent.AgentToolResult{}, fmt.Errorf("%s", errorMsg)
				}
			}
			if output == "" {
				return agent.AgentToolResult{
					Content: []ai.Content{ai.TextContent{Text: "No files found matching pattern"}},
					Details: json.RawMessage(`{}`),
				}, nil
			}

			var relativized []string
			for _, rawLine := range strings.Split(output, "\n") {
				line := strings.TrimRight(rawLine, "\r")
				line = strings.TrimSpace(line)
				if line == "" {
					continue
				}
				relativized = append(relativized, RelativizeFindResultPath(line, searchPath))
			}
			resultLimitReached := len(relativized) >= effectiveLimit
			rawOutput2 := strings.Join(relativized, "\n")
			truncation := TruncateHead(rawOutput2, TruncationOptions{MaxLines: 1 << 53})
			resultOutput := truncation.Content
			details := FindToolDetails{}
			var notices []string
			if resultLimitReached {
				notices = append(notices, fmt.Sprintf("%d results limit reached. Use limit=%d for more, or refine pattern", effectiveLimit, effectiveLimit*2))
				details.ResultLimitReached = &effectiveLimit
			}
			if truncation.Truncated {
				notices = append(notices, fmt.Sprintf("%s limit reached", FormatSize(DefaultMaxBytes)))
				details.Truncation = &truncation
			}
			if len(notices) > 0 {
				resultOutput += "\n\n[" + strings.Join(notices, ". ") + "]"
			}
			detailsJSON, _ := ai.MarshalJSON(details)
			return agent.AgentToolResult{
				Content: []ai.Content{ai.TextContent{Text: resultOutput}},
				Details: detailsJSON,
			}, nil
		},
	}
}

// --- grep ---

const grepDefaultLimit = 100

// GrepToolDescription is upstream's grep description.
const GrepToolDescription = "Search file contents for a pattern. Returns matching lines with file paths and line numbers. Respects .gitignore. Output is truncated to 100 matches or 50KB (whichever is hit first). Long lines are truncated to 500 chars."

// GrepToolDetails carries grep truncation info.
type GrepToolDetails struct {
	Truncation        *TruncationResult `json:"truncation,omitempty"`
	MatchLimitReached *int              `json:"matchLimitReached,omitempty"`
	LinesTruncated    *bool             `json:"linesTruncated,omitempty"`
}

var grepSchemaJSON = mustSchemaJSON(map[string]any{
	"type": "object",
	"properties": map[string]any{
		"pattern":    map[string]any{"type": "string", "description": "Search pattern (regex or literal string)"},
		"path":       map[string]any{"type": "string", "description": "Directory or file to search (default: current directory)"},
		"glob":       map[string]any{"type": "string", "description": "Filter files by glob pattern, e.g. '*.ts' or '**/*.spec.ts'"},
		"ignoreCase": map[string]any{"type": "boolean", "description": "Case-insensitive search (default: false)"},
		"literal":    map[string]any{"type": "boolean", "description": "Treat pattern as literal string instead of regex (default: false)"},
		"context":    numSchema("Number of lines to show before and after each match (default: 0)"),
		"limit":      numSchema("Maximum number of matches to return (default: 100)"),
	},
	"required": []string{"pattern"},
})

// rgMatchEvent is one ripgrep --json event.
type rgMatchEvent struct {
	Type string `json:"type"`
	Data struct {
		Path struct {
			Text string `json:"text"`
		} `json:"path"`
		LineNumber *int64 `json:"line_number"`
		Lines      struct {
			Text string `json:"text"`
		} `json:"lines"`
	} `json:"data"`
}

// CreateGrepTool builds the grep tool.
func CreateGrepTool(cwd string) agent.AgentTool {
	return agent.AgentTool{
		Name:        "grep",
		Description: GrepToolDescription,
		Parameters:  grepSchemaJSON,
		Label:       "grep",
		Execute: func(toolCallID string, params json.RawMessage, ctx context.Context, onUpdate func(agent.AgentToolResult)) (agent.AgentToolResult, error) {
			if ctxErrOf(ctx) != nil {
				return agent.AgentToolResult{}, fmt.Errorf("Operation aborted")
			}
			var input struct {
				Pattern    string `json:"pattern"`
				Path       string `json:"path"`
				Glob       string `json:"glob"`
				IgnoreCase *bool  `json:"ignoreCase"`
				Literal    *bool  `json:"literal"`
				Context    *int   `json:"context"`
				Limit      *int   `json:"limit"`
			}
			if len(params) > 0 {
				if err := jsonUnmarshalStrictTool(params, &input); err != nil {
					return agent.AgentToolResult{}, err
				}
			}

			rgPath, err := exec.LookPath("rg")
			if err != nil {
				return agent.AgentToolResult{}, fmt.Errorf("ripgrep (rg) is not available and could not be downloaded")
			}
			searchPath := ResolveToCwd(orDefault(input.Path, "."), cwd)
			info, statErr := os.Stat(searchPath)
			if statErr != nil {
				return agent.AgentToolResult{}, fmt.Errorf("Path not found: %s", searchPath)
			}
			isDirectory := info.IsDir()

			contextValue := 0
			if input.Context != nil && *input.Context > 0 {
				contextValue = *input.Context
			}
			effectiveLimit := grepDefaultLimit
			if input.Limit != nil {
				effectiveLimit = max(1, *input.Limit)
			}
			formatPath := func(filePath string) string {
				if isDirectory {
					if rel, relErr := filepath.Rel(searchPath, filePath); relErr == nil && rel != "" && !strings.HasPrefix(rel, "..") {
						return filepath.ToSlash(rel)
					}
				}
				return filepath.Base(filePath)
			}

			args := []string{"--json", "--line-number", "--color=never", "--hidden"}
			if input.IgnoreCase != nil && *input.IgnoreCase {
				args = append(args, "--ignore-case")
			}
			if input.Literal != nil && *input.Literal {
				args = append(args, "--fixed-strings")
			}
			if input.Glob != "" {
				args = append(args, "--glob", input.Glob)
			}
			args = append(args, "--", input.Pattern, searchPath)

			cmd := exec.CommandContext(ctx, rgPath, args...)
			outputBytes, runErr := cmd.Output()
			if ctxErrOf(ctx) != nil {
				return agent.AgentToolResult{}, fmt.Errorf("Operation aborted")
			}
			// Upstream rejects on any exit code other than 0/1 (rg uses 2 for
			// usage errors like bad regexes).
			if runErr != nil {
				if exitErr, ok := runErr.(*exec.ExitError); ok {
					code := exitErr.ExitCode()
					if code != 0 && code != 1 {
						errorMsg := strings.TrimSpace(string(exitErr.Stderr))
						if errorMsg == "" {
							errorMsg = fmt.Sprintf("ripgrep exited with code %d", code)
						}
						return agent.AgentToolResult{}, fmt.Errorf("%s", errorMsg)
					}
				} else {
					return agent.AgentToolResult{}, fmt.Errorf("Failed to run ripgrep: %s", runErr.Error())
				}
			}

			// Collect matches, then format (upstream formats after rg exits).
			var matches []rgMatchEvent
			matchCount := 0
			matchLimitReached := false

			for _, line := range strings.Split(string(outputBytes), "\n") {
				if strings.TrimSpace(line) == "" || matchCount >= effectiveLimit {
					continue
				}
				var event rgMatchEvent
				if jsonUnmarshalStrictTool(json.RawMessage(line), &event) != nil || event.Type != "match" {
					continue
				}
				matchCount++
				if event.Data.Path.Text != "" && event.Data.LineNumber != nil {
					matches = append(matches, event)
				}
				if matchCount >= effectiveLimit {
					matchLimitReached = true
				}
			}
			if matchCount == 0 {
				return agent.AgentToolResult{
					Content: []ai.Content{ai.TextContent{Text: "No matches found"}},
					Details: json.RawMessage(`{}`),
				}, nil
			}

			linesTruncated := false
			var outputLines []string

			// Context-line reads (cached per file).
			fileCache := map[string][]string{}
			getFileLines := func(filePath string) []string {
				if lines, ok := fileCache[filePath]; ok {
					return lines
				}
				content, err := os.ReadFile(filePath)
				lines := []string{}
				if err == nil {
					text := strings.ReplaceAll(string(content), "\r\n", "\n")
					text = strings.ReplaceAll(text, "\r", "\n")
					lines = strings.Split(text, "\n")
				}
				fileCache[filePath] = lines
				return lines
			}

			formatBlock := func(filePath string, lineNumber int64) []string {
				relativePath := formatPath(filePath)
				lines := getFileLines(filePath)
				if len(lines) == 0 {
					return []string{fmt.Sprintf("%s:%d: (unable to read file)", relativePath, lineNumber)}
				}
				var block []string
				start := lineNumber
				end := lineNumber
				if contextValue > 0 {
					start = max(1, lineNumber-int64(contextValue))
					end = min(int64(len(lines)), lineNumber+int64(contextValue))
				}
				for current := start; current <= end; current++ {
					lineText := ""
					if current-1 < int64(len(lines)) {
						lineText = lines[current-1]
					}
					sanitized := strings.ReplaceAll(lineText, "\r", "")
					truncated := TruncateLine(sanitized, GrepMaxLineLength)
					if truncated.WasTruncated {
						linesTruncated = true
					}
					if current == lineNumber {
						block = append(block, fmt.Sprintf("%s:%d: %s", relativePath, current, truncated.Text))
					} else {
						block = append(block, fmt.Sprintf("%s-%d- %s", relativePath, current, truncated.Text))
					}
				}
				return block
			}

			for _, match := range matches {
				if contextValue == 0 {
					relativePath := formatPath(match.Data.Path.Text)
					sanitized := strings.ReplaceAll(match.Data.Lines.Text, "\r\n", "\n")
					sanitized = strings.ReplaceAll(sanitized, "\r", "")
					sanitized = strings.TrimRight(sanitized, "\n")
					truncated := TruncateLine(sanitized, GrepMaxLineLength)
					if truncated.WasTruncated {
						linesTruncated = true
					}
					outputLines = append(outputLines, fmt.Sprintf("%s:%d: %s", relativePath, *match.Data.LineNumber, truncated.Text))
				} else {
					outputLines = append(outputLines, formatBlock(match.Data.Path.Text, *match.Data.LineNumber)...)
				}
			}

			rawOutput := strings.Join(outputLines, "\n")
			truncation := TruncateHead(rawOutput, TruncationOptions{MaxLines: 1 << 53})
			output := truncation.Content
			details := GrepToolDetails{}
			var notices []string
			if matchLimitReached {
				notices = append(notices, fmt.Sprintf("%d matches limit reached. Use limit=%d for more, or refine pattern", effectiveLimit, effectiveLimit*2))
				details.MatchLimitReached = &effectiveLimit
			}
			if truncation.Truncated {
				notices = append(notices, fmt.Sprintf("%s limit reached", FormatSize(DefaultMaxBytes)))
				details.Truncation = &truncation
			}
			if linesTruncated {
				notices = append(notices, fmt.Sprintf("Some lines truncated to %d chars. Use read tool to see full lines", GrepMaxLineLength))
				details.LinesTruncated = boolPtr(true)
			}
			if len(notices) > 0 {
				output += "\n\n[" + strings.Join(notices, ". ") + "]"
			}
			detailsJSON, _ := ai.MarshalJSON(details)
			return agent.AgentToolResult{
				Content: []ai.Content{ai.TextContent{Text: output}},
				Details: detailsJSON,
			}, nil
		},
	}
}
