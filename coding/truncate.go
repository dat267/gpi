// Package coding is a faithful Go port of @mariozechner/pi-coding-agent's
// core (pi/packages/coding-agent): the built-in coding tools, path
// utilities, truncation rules, and (in progress) the session runner.
//
// Ground truth: pi/packages/coding-agent/src/core/tools at the pinned
// upstream commit.
package coding

import (
	"fmt"
	"strings"

	"github.com/dat267/pier/ai"
)

// Port of core/tools/truncate.ts.
//
// Truncation is based on two independent limits — whichever is hit first
// wins. Never returns partial lines (except the bash tail-truncation edge
// case).

const (
	// DefaultMaxLines is the default line limit.
	DefaultMaxLines = 2000
	// DefaultMaxBytes is the default byte limit (50KB).
	DefaultMaxBytes = 50 * 1024
	// GrepMaxLineLength is the max chars per grep match line.
	GrepMaxLineLength = 500
)

// TruncatedBy identifies which limit fired.
type TruncatedBy = string

const (
	TruncatedByLines TruncatedBy = "lines"
	TruncatedByBytes TruncatedBy = "bytes"
)

// TruncationResult is the shared truncation outcome.
type TruncationResult struct {
	// Content is the truncated content.
	Content string
	// Truncated reports whether truncation occurred.
	Truncated bool
	// TruncatedBy: "lines", "bytes", or "" when not truncated.
	TruncatedBy TruncatedBy
	// TotalLines/TotalBytes describe the original content.
	TotalLines int
	TotalBytes int
	// OutputLines/OutputBytes describe the truncated output.
	OutputLines int
	OutputBytes int
	// LastLinePartial: only for the tail-truncation edge case.
	LastLinePartial bool
	// FirstLineExceedsLimit: for head truncation.
	FirstLineExceedsLimit bool
	// MaxLines/MaxBytes are the applied limits.
	MaxLines int
	MaxBytes int
}

// TruncationOptions carry the limits (zero → defaults).
type TruncationOptions struct {
	MaxLines int
	MaxBytes int
}

func splitLinesForCounting(content string) []string {
	if len(content) == 0 {
		return nil
	}
	lines := strings.Split(content, "\n")
	if strings.HasSuffix(content, "\n") {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// FormatSize formats bytes as a human-readable size.
func FormatSize(bytes int64) string {
	if bytes < 1024 {
		return fmt.Sprintf("%dB", bytes)
	} else if bytes < 1024*1024 {
		return fmt.Sprintf("%.1fKB", float64(bytes)/1024)
	}
	return fmt.Sprintf("%.1fMB", float64(bytes)/(1024*1024))
}

// TruncateHead keeps the first N lines/bytes (file reads). Never returns
// partial lines; a first line over the byte limit yields empty content with
// FirstLineExceedsLimit.
func TruncateHead(content string, options TruncationOptions) TruncationResult {
	maxLines := options.MaxLines
	if maxLines == 0 {
		maxLines = DefaultMaxLines
	}
	maxBytes := options.MaxBytes
	if maxBytes == 0 {
		maxBytes = DefaultMaxBytes
	}

	totalBytes := len(content)
	lines := splitLinesForCounting(content)
	totalLines := len(lines)

	if totalLines <= maxLines && totalBytes <= maxBytes {
		return TruncationResult{
			Content: content, Truncated: false,
			TotalLines: totalLines, TotalBytes: totalBytes,
			OutputLines: totalLines, OutputBytes: totalBytes,
			MaxLines: maxLines, MaxBytes: maxBytes,
		}
	}

	if firstLineBytes := len(lines[0]); firstLineBytes > maxBytes {
		return TruncationResult{
			Content: "", Truncated: true, TruncatedBy: TruncatedByBytes,
			TotalLines: totalLines, TotalBytes: totalBytes,
			FirstLineExceedsLimit: true,
			MaxLines:              maxLines, MaxBytes: maxBytes,
		}
	}

	var outputLines []string
	outputBytesCount := 0
	truncatedBy := TruncatedByLines

	for i := 0; i < len(lines) && i < maxLines; i++ {
		lineBytes := len(lines[i]) + min(i, 1) // +1 for the newline after the first
		if outputBytesCount+lineBytes > maxBytes {
			truncatedBy = TruncatedByBytes
			break
		}
		outputLines = append(outputLines, lines[i])
		outputBytesCount += lineBytes
	}

	if len(outputLines) >= maxLines && outputBytesCount <= maxBytes {
		truncatedBy = TruncatedByLines
	}

	outputContent := strings.Join(outputLines, "\n")
	return TruncationResult{
		Content: outputContent, Truncated: true, TruncatedBy: truncatedBy,
		TotalLines: totalLines, TotalBytes: totalBytes,
		OutputLines: len(outputLines), OutputBytes: len(outputContent),
		MaxLines: maxLines, MaxBytes: maxBytes,
	}
}

// TruncateTailLines is the line-slice path of TruncateTail: the caller already
// holds the logical lines (bash streams into a line buffer and recomputes its
// display on every chunk, where re-joining and re-splitting the whole output
// was quadratic). `lines` is strings.Split(content, "\n") and totalBytes is
// len(content).
//
// It returns the tail window (nil when the truncated content is empty, so a
// caller can treat nil as "no lines", matching a TruncateTail Content of "")
// and whether truncation happened. The count fields are not materialized: the
// caller keeps the lines.
func TruncateTailLines(lines []string, totalBytes int, options TruncationOptions) ([]string, bool) {
	maxLines := options.MaxLines
	if maxLines == 0 {
		maxLines = DefaultMaxLines
	}
	maxBytes := options.MaxBytes
	if maxBytes == 0 {
		maxBytes = DefaultMaxBytes
	}

	// splitLinesForCounting drops the element after a trailing newline.
	counted := lines
	totalLines := len(counted)
	if totalLines > 0 && counted[totalLines-1] == "" {
		counted = counted[:totalLines-1]
		totalLines--
	}

	if totalLines <= maxLines && totalBytes <= maxBytes {
		return emptyWindowToNil(lines), false
	}

	window := make([]string, 0, min(maxLines, totalLines))
	outputBytesCount := 0
	for i := totalLines - 1; i >= 0 && len(window) < maxLines; i-- {
		lineBytes := len(counted[i])
		if len(window) > 0 {
			lineBytes++ // +1 for the newline
		}
		if outputBytesCount+lineBytes > maxBytes {
			if len(window) == 0 {
				window = append(window, truncateStringToBytesFromEnd(counted[i], maxBytes))
			}
			break
		}
		window = append(window, counted[i])
		outputBytesCount += lineBytes
	}
	for i, j := 0, len(window)-1; i < j; i, j = i+1, j-1 {
		window[i], window[j] = window[j], window[i]
	}
	return emptyWindowToNil(window), true
}

// emptyWindowToNil reports an empty window as nil, matching TruncateTail's
// empty Content: strings.Join([]string{""}, "\n") is "".
func emptyWindowToNil(window []string) []string {
	if len(window) == 1 && window[0] == "" {
		return nil
	}
	return window
}

// TruncateTail keeps the last N lines/bytes (bash output). May return a
// partial first line when the last original line exceeds the byte limit.
func TruncateTail(content string, options TruncationOptions) TruncationResult {
	maxLines := options.MaxLines
	if maxLines == 0 {
		maxLines = DefaultMaxLines
	}
	maxBytes := options.MaxBytes
	if maxBytes == 0 {
		maxBytes = DefaultMaxBytes
	}

	totalBytes := len(content)
	lines := splitLinesForCounting(content)
	totalLines := len(lines)

	if totalLines <= maxLines && totalBytes <= maxBytes {
		return TruncationResult{
			Content: content, Truncated: false,
			TotalLines: totalLines, TotalBytes: totalBytes,
			OutputLines: totalLines, OutputBytes: totalBytes,
			MaxLines: maxLines, MaxBytes: maxBytes,
		}
	}

	var outputLines []string
	outputBytesCount := 0
	truncatedBy := TruncatedByLines
	lastLinePartial := false

	for i := len(lines) - 1; i >= 0 && len(outputLines) < maxLines; i-- {
		lineBytes := len(lines[i])
		if len(outputLines) > 0 {
			lineBytes++ // +1 for the newline
		}
		if outputBytesCount+lineBytes > maxBytes {
			truncatedBy = TruncatedByBytes
			if len(outputLines) == 0 {
				truncatedLine := truncateStringToBytesFromEnd(lines[i], maxBytes)
				outputLines = append([]string{truncatedLine}, outputLines...)
				outputBytesCount = len(truncatedLine)
				lastLinePartial = true
			}
			break
		}
		outputLines = append([]string{lines[i]}, outputLines...)
		outputBytesCount += lineBytes
	}

	if len(outputLines) >= maxLines && outputBytesCount <= maxBytes {
		truncatedBy = TruncatedByLines
	}

	outputContent := strings.Join(outputLines, "\n")
	return TruncationResult{
		Content: outputContent, Truncated: true, TruncatedBy: truncatedBy,
		TotalLines: totalLines, TotalBytes: totalBytes,
		OutputLines: len(outputLines), OutputBytes: len(outputContent),
		LastLinePartial: lastLinePartial,
		MaxLines:        maxLines, MaxBytes: maxBytes,
	}
}

// truncateStringToBytesFromEnd truncates to a byte limit from the end,
// respecting UTF-8 boundaries.
func truncateStringToBytesFromEnd(str string, maxBytes int) string {
	if len(str) <= maxBytes {
		return str
	}
	start := len(str) - maxBytes
	// Find a valid UTF-8 boundary.
	for start < len(str) && (str[start]&0xc0) == 0x80 {
		start++
	}
	return str[start:]
}

// TruncateLineOutput is the truncateLine result.
type TruncateLineOutput struct {
	Text         string
	WasTruncated bool
}

// TruncateLine truncates a single line to maxChars with a [truncated] suffix
// (grep match lines).
func TruncateLine(line string, maxChars int) TruncateLineOutput {
	if maxChars == 0 {
		maxChars = GrepMaxLineLength
	}
	if ai.JSLength(line) <= maxChars {
		return TruncateLineOutput{Text: line, WasTruncated: false}
	}
	return TruncateLineOutput{
		Text:         ai.JSSlice(line, 0, maxChars) + "... [truncated]",
		WasTruncated: true,
	}
}
