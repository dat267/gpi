package coding

import (
	"fmt"
)

// ForkSession creates a new session from sourcePath with a new id and the
// source's non-header entries (upstream SessionManager.forkFrom): the new
// header points at the source as parentSession and carries targetCwd.
func ForkSession(sourcePath string, targetCwd string, sessionDir string, options *NewSessionOptions) (*SessionManager, error) {
	resolvedSourcePath := ResolvePath(sourcePath, "", PathInputOptions{})
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

	// Every non-header entry travels (upstream forkFrom copies the whole log, not
	// just the current branch). writeForkedSession does the writing.
	entries := make([]SessionEntry, 0, len(sourceEntries))
	for _, fileEntry := range sourceEntries {
		if fileEntry.Entry != nil {
			entries = append(entries, *fileEntry.Entry)
		}
	}
	return writeForkedSession(targetCwd, sessionDir, &resolvedSourcePath, entries, options)
}
