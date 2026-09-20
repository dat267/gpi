package coding

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dat267/gpi/ai"
)

// Port of core/session-export.ts: serializing the current branch as JSONL.

// SerializeSessionBranch renders the session header followed by every entry on
// the current branch, re-chained parent ids, plus optional export-only trailing
// entries.
func SerializeSessionBranch(sessionManager *SessionManager, createTrailingEntries func(parentID *string, timestamp string) []*SessionEntry) string {
	timestamp := time.Now().UTC().Format(time.RFC3339Nano)
	version := CurrentSessionVersion
	header := SessionHeader{
		Version:   &version,
		ID:        sessionManager.GetSessionID(),
		Timestamp: timestamp,
		Cwd:       sessionManager.GetCwd(),
	}
	lines := []string{string(mustMarshalJSON(header))}
	var parentID *string
	for _, entry := range sessionManager.GetBranch("") {
		// A fresh pointer per entry: they all re-chain to the previous entry.
		current := parentID
		entry.ParentID = current
		lines = append(lines, string(mustMarshalJSON(&entry)))
		parentID = &entry.ID
	}
	if createTrailingEntries != nil {
		for _, entry := range createTrailingEntries(parentID, timestamp) {
			lines = append(lines, string(mustMarshalJSON(entry)))
		}
	}
	return strings.Join(lines, "\n") + "\n"
}

// ExportSessionToJsonl writes the current branch to a JSONL file and returns
// the resolved path.
func ExportSessionToJsonl(sessionManager *SessionManager, outputPath string, createTrailingEntries func(parentID *string, timestamp string) []*SessionEntry) (string, error) {
	filePath := ResolvePath(outputPath, "", PathInputOptions{})
	if outputPath == "" {
		filePath = ResolvePath("session-"+strings.ReplaceAll(strings.ReplaceAll(time.Now().UTC().Format(time.RFC3339Nano), ":", "-"), ".", "-")+".jsonl", "", PathInputOptions{})
	}
	dir := filepath.Dir(filePath)
	if !PathExists(dir) {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", err
		}
	}
	if err := os.WriteFile(filePath, []byte(SerializeSessionBranch(sessionManager, createTrailingEntries)), 0o644); err != nil {
		return "", err
	}
	return filePath, nil
}

// ExportToJsonl exports the current session branch (upstream exportToJsonl).
func (s *AgentSession) ExportToJsonl(outputPath string) (string, error) {
	return ExportSessionToJsonl(s.Sessions, outputPath, nil)
}

var _ = ai.RoleUser
