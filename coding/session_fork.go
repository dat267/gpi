package coding

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ForkSession creates a new session from sourcePath with a new id and the
// source's non-header entries (upstream SessionManager.forkFrom): the new
// header points at the source as parentSession and carries targetCwd.
func ForkSession(sourcePath string, targetCwd string, sessionDir string, options *NewSessionOptions) (*SessionManager, error) {
	resolvedSourcePath := ResolvePath(sourcePath, "", PathInputOptions{})
	resolvedTargetCwd := ResolvePath(targetCwd, "", PathInputOptions{})
	sourceEntries, err := LoadEntriesFromFile(resolvedSourcePath)
	if err != nil {
		return nil, err
	}
	if len(sourceEntries) == 0 {
		return nil, fmt.Errorf("Cannot fork: source session file is empty or invalid: %s", resolvedSourcePath)
	}

	var sourceHeader *SessionHeader
	for i := range sourceEntries {
		if sourceEntries[i].Header != nil {
			sourceHeader = sourceEntries[i].Header
			break
		}
	}
	if sourceHeader == nil {
		return nil, fmt.Errorf("Cannot fork: source session has no header: %s", resolvedSourcePath)
	}

	dir := sessionDir
	if dir != "" {
		dir = NormalizePath(dir, PathInputOptions{})
	} else {
		dir = DefaultSessionDir(resolvedTargetCwd, "")
	}
	if _, err := os.Stat(dir); err != nil {
		_ = os.MkdirAll(dir, 0o755)
	}

	if options != nil && options.ID != "" {
		if err := AssertValidSessionID(options.ID); err != nil {
			return nil, err
		}
	}
	newSessionID := UUIDv7()
	if options != nil && options.ID != "" {
		newSessionID = options.ID
	}
	timestamp := time.Now().UTC().Format(time.RFC3339Nano)
	fileTimestamp := strings.NewReplacer(":", "-", ".", "-").Replace(timestamp)
	newSessionFile := filepath.Join(dir, fmt.Sprintf("%s_%s.jsonl", fileTimestamp, newSessionID))

	newHeader := SessionHeader{
		Version:       intPtr(CurrentSessionVersion),
		ID:            newSessionID,
		Timestamp:     timestamp,
		Cwd:           resolvedTargetCwd,
		ParentSession: &resolvedSourcePath,
	}
	headerLine, err := MarshalFileEntry(FileEntry{Header: &newHeader})
	if err != nil {
		return nil, err
	}

	// flag: "wx" — refuse to clobber an existing file.
	file, err := os.OpenFile(newSessionFile, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return nil, err
	}
	writer := bufio.NewWriter(file)
	if _, err := writer.WriteString(headerLine + "\n"); err != nil {
		file.Close()
		return nil, err
	}
	for _, entry := range sourceEntries {
		if entry.Header != nil {
			continue
		}
		line, err := MarshalFileEntry(FileEntry{Entry: entry.Entry})
		if err != nil {
			continue
		}
		if _, err := writer.WriteString(line + "\n"); err != nil {
			file.Close()
			return nil, err
		}
	}
	if err := writer.Flush(); err != nil {
		file.Close()
		return nil, err
	}
	if err := file.Close(); err != nil {
		return nil, err
	}

	return OpenSession(newSessionFile, "", "")
}
