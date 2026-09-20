package coding

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// Port of core/crash-log.ts: best-effort crash persistence feeding the bug
// report's crash records.

// CrashKind discriminates crash records.
type CrashKind = string

const (
	CrashKindUncaughtException CrashKind = "uncaught_exception"
	CrashKindFatalError        CrashKind = "fatal_error"
)

// CrashRecord is one persisted crash.
type CrashRecord struct {
	Timestamp   string  `json:"timestamp"`
	Version     string  `json:"version"`
	Kind        string  `json:"kind"`
	Message     string  `json:"message"`
	Stack       *string `json:"stack"`
	SessionFile *string `json:"sessionFile"`
	Cwd         string  `json:"cwd"`
	Notified    *bool   `json:"notified,omitempty"`
}

// maxCrashRecords caps the log; maxCrashAge bounds the notice window.
const (
	maxCrashRecords = 5
	maxCrashAge     = 7 * 24 * 60 * 60 * 1000
)

// GetCrashLogPath resolves the crash log under the agent dir.
func GetCrashLogPath(agentDir string) string {
	if agentDir == "" {
		agentDir = GetAgentDir()
	}
	return filepath.Join(agentDir, "crashes.json")
}

// ReadCrashLog reads the crash log, dropping malformed records.
func ReadCrashLog(path string) []CrashRecord {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	// Upstream parses the whole document and filters per record, so one
	// malformed entry cannot hide the valid ones.
	var rawRecords []json.RawMessage
	if err := json.Unmarshal(raw, &rawRecords); err != nil {
		return nil
	}
	var records []CrashRecord
	for _, rawRecord := range rawRecords {
		var record CrashRecord
		if json.Unmarshal(rawRecord, &record) != nil {
			continue
		}
		if record.Timestamp != "" && record.Message != "" {
			records = append(records, record)
		}
	}
	return records
}

// writeCrashLog persists the log with a trailing newline.
func writeCrashLog(records []CrashRecord, path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	encoded, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(encoded, '\n'), 0o644)
}

// CrashInput describes a crash to persist.
type CrashInput struct {
	Kind        CrashKind
	Error       error
	SessionFile string
	Cwd         string
}

// RecordCrash persists a crash best-effort for callers that are already
// crashing; failures return nil.
func RecordCrash(crash CrashInput, path string) *CrashRecord {
	defer func() { _ = recover() }()
	if path == "" {
		path = GetCrashLogPath("")
	}
	message := "unknown error"
	var stack string
	if crash.Error != nil {
		// Upstream prefers error.message, falling back to error.name; Go errors
		// only have Error().
		if message = crash.Error.Error(); message == "" {
			message = "error"
		}
		if stacer, ok := crash.Error.(interface{ StackTrace() string }); ok {
			stack = stacer.StackTrace()
		}
	}
	var sessionFile *string
	if crash.SessionFile != "" {
		sessionFile = &crash.SessionFile
	}
	record := CrashRecord{
		Timestamp:   time.Now().UTC().Format(time.RFC3339Nano),
		Version:     Version,
		Kind:        crash.Kind,
		Message:     message,
		Stack:       nil,
		SessionFile: sessionFile,
		Cwd:         crash.Cwd,
	}
	if stack != "" {
		stackCopy := stack
		record.Stack = &stackCopy
	}
	records := append(ReadCrashLog(path), record)
	if len(records) > maxCrashRecords {
		records = records[len(records)-maxCrashRecords:]
	}
	if err := writeCrashLog(records, path); err != nil {
		return nil
	}
	return &record
}

// TakeUnnotifiedCrash returns the newest recent unannounced crash and marks
// pending records as announced.
func TakeUnnotifiedCrash(path string, now int64) *CrashRecord {
	records := ReadCrashLog(path)
	if now == 0 {
		now = time.Now().UnixMilli()
	}
	var found *CrashRecord
	for index := len(records) - 1; index >= 0; index-- {
		record := records[index]
		if record.Notified != nil && *record.Notified {
			continue
		}
		timestamp, err := time.Parse(time.RFC3339Nano, record.Timestamp)
		if err != nil {
			continue
		}
		if now-timestamp.UnixMilli() <= maxCrashAge {
			found = &records[index]
			break
		}
	}
	if found == nil {
		return nil
	}
	// Showing the notice again is harmless.
	for index := range records {
		if records[index].Notified == nil || !*records[index].Notified {
			notified := true
			records[index].Notified = &notified
		}
	}
	_ = writeCrashLog(records, path)
	copied := *found
	return &copied
}

// ClearCrashLog removes the crash log; failures are ignored because the records
// can be attached again.
func ClearCrashLog(path string) {
	_ = os.Remove(path)
}
