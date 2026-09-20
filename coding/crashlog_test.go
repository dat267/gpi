package coding

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Round 115 tests: the crash log.

func TestRecordAndReadCrashLog(t *testing.T) {
	dir := t.TempDir()
	path := GetCrashLogPath(dir)
	if path != filepath.Join(dir, "crashes.json") {
		t.Fatalf("path = %q", path)
	}

	boom := errors.New("exploded")
	record := RecordCrash(CrashInput{
		Kind: CrashKindUncaughtException, Error: boom, SessionFile: "/s.json", Cwd: "/tmp",
	}, path)
	if record == nil || record.Message != "exploded" || record.Kind != CrashKindUncaughtException ||
		record.Version != Version || record.Cwd != "/tmp" {
		t.Fatalf("record = %+v", record)
	}
	if record.SessionFile == nil || *record.SessionFile != "/s.json" {
		t.Fatalf("record = %+v", record)
	}
	if record.Stack != nil {
		t.Fatalf("plain errors have no stack: %+v", record)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}

	// Records accumulate and cap at five.
	for index := 0; index < 7; index++ {
		RecordCrash(CrashInput{Kind: CrashKindFatalError, Error: errors.New("again")}, path)
	}
	records := ReadCrashLog(path)
	if len(records) != maxCrashRecords {
		t.Fatalf("records = %d", len(records))
	}
	// The oldest records were dropped; the first crash is gone.
	if records[0].Message == "exploded" {
		t.Fatalf("records = %+v", records)
	}

	// The file is JSON with a trailing newline.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(raw) || !trailingNewline(raw) {
		t.Fatalf("file = %q", raw)
	}
}

// trailingNewline reports a trailing newline.
func trailingNewline(raw []byte) bool {
	return len(raw) > 0 && raw[len(raw)-1] == '\n'
}

func TestRecordCrashBestEffort(t *testing.T) {
	dir := t.TempDir()

	// A directory in place of the log makes writes fail; the caller gets nil
	// and does not crash.
	dirPath := filepath.Join(dir, "dir.json")
	if err := os.Mkdir(dirPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if record := RecordCrash(CrashInput{Kind: CrashKindFatalError, Error: errors.New("x"), Cwd: "/"}, dirPath); record != nil {
		t.Fatalf("record = %+v", record)
	}
	// Reading a directory path fails and reads as empty.
	if records := ReadCrashLog(dirPath); len(records) != 0 {
		t.Fatalf("records = %+v", records)
	}

	// A missing file reads as empty.
	if records := ReadCrashLog(filepath.Join(dir, "missing.json")); len(records) != 0 {
		t.Fatalf("records = %+v", records)
	}

	// Malformed records are dropped.
	path := filepath.Join(dir, "crashes.json")
	if err := os.WriteFile(path, []byte(`[{"timestamp":"t","message":"ok"},"junk",{"nope":1}]`), 0o644); err != nil {
		t.Fatal(err)
	}
	records := ReadCrashLog(path)
	if len(records) != 1 || records[0].Message != "ok" {
		t.Fatalf("records = %+v", records)
	}
}

func TestTakeUnnotifiedCrash(t *testing.T) {
	dir := t.TempDir()
	path := GetCrashLogPath(dir)
	if crash := TakeUnnotifiedCrash(path, 0); crash != nil {
		t.Fatalf("crash = %+v", crash)
	}

	// A fresh crash is returned once, then marked announced.
	RecordCrash(CrashInput{Kind: CrashKindFatalError, Error: errors.New("boom"), Cwd: "/"}, path)
	crash := TakeUnnotifiedCrash(path, 0)
	if crash == nil || crash.Message != "boom" {
		t.Fatalf("crash = %+v", crash)
	}
	if again := TakeUnnotifiedCrash(path, 0); again != nil {
		t.Fatalf("crash = %+v", again)
	}

	// An old crash is outside the notice window.
	old := time.Now().Add(-8 * 24 * time.Hour).UTC().Format(time.RFC3339Nano)
	if err := os.WriteFile(path, []byte(`[{"timestamp":"`+old+`","version":"1","kind":"fatal_error","message":"old","cwd":"/"}]`), 0o644); err != nil {
		t.Fatal(err)
	}
	if crash := TakeUnnotifiedCrash(path, 0); crash != nil {
		t.Fatalf("crash = %+v", crash)
	}

	// The newest of several unnotified crashes wins.
	RecordCrash(CrashInput{Kind: CrashKindFatalError, Error: errors.New("first"), Cwd: "/"}, path)
	RecordCrash(CrashInput{Kind: CrashKindFatalError, Error: errors.New("second"), Cwd: "/"}, path)
	crash = TakeUnnotifiedCrash(path, 0)
	if crash == nil || crash.Message != "second" {
		t.Fatalf("crash = %+v", crash)
	}

	ClearCrashLog(path)
	if records := ReadCrashLog(path); len(records) != 0 {
		t.Fatalf("records = %+v", records)
	}
	// Clearing twice is fine.
	ClearCrashLog(path)
}
