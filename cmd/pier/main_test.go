package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/dat267/pier/ai"
	"github.com/dat267/pier/coding"
)

func strPtr(s string) *string { return &s }

// isolatedAgentDir points PI_CODING_AGENT_DIR at a temp dir so the default
// per-cwd session dirs stay inside the test sandbox.
func isolatedAgentDir(t *testing.T) {
	t.Helper()
	t.Setenv("PI_CODING_AGENT_DIR", t.TempDir())
}

// makeSessionFile writes a real persisted session with a full turn (the file
// flushes only once an assistant message exists, upstream
// flush-on-first-assistant) using the default per-cwd session dir.
func makeSessionFile(t *testing.T, cwd string) *coding.SessionManager {
	t.Helper()
	sm := coding.NewSessionManager(cwd, nil)
	sm.AppendMessage(&ai.UserMessage{Content: ai.StringOrBlocks{Text: "hello"}})
	sm.AppendMessage(&ai.AssistantMessage{
		API:        ai.APIAnthropicMessages,
		Provider:   "anthropic",
		Model:      "m",
		Content:    ai.ContentList{ai.TextContent{Text: "hi"}},
		StopReason: ai.StopStop,
	})
	return sm
}

func TestResumeSessionByExactID(t *testing.T) {
	isolatedAgentDir(t)
	cwd := t.TempDir()
	source := makeSessionFile(t, cwd)

	args := &coding.Args{Session: strPtr(source.GetSessionID())}
	sm, err := resumeSession(args, cwd, t.TempDir(), nil)
	if err != nil {
		t.Fatalf("resumeSession: %v", err)
	}
	if sm.GetSessionID() != source.GetSessionID() {
		t.Fatalf("resumed id %s, want %s", sm.GetSessionID(), source.GetSessionID())
	}
	if sm.GetSessionFile() != source.GetSessionFile() {
		t.Fatalf("resumed file %s, want %s", sm.GetSessionFile(), source.GetSessionFile())
	}
}

func TestResumeSessionByIDPrefix(t *testing.T) {
	isolatedAgentDir(t)
	cwd := t.TempDir()
	source := makeSessionFile(t, cwd)

	args := &coding.Args{Session: strPtr(source.GetSessionID()[:8])}
	sm, err := resumeSession(args, cwd, t.TempDir(), nil)
	if err != nil {
		t.Fatalf("resumeSession: %v", err)
	}
	if sm.GetSessionID() != source.GetSessionID() {
		t.Fatalf("resumed id %s, want %s", sm.GetSessionID(), source.GetSessionID())
	}
}

func TestResumeSessionByPathStillWorks(t *testing.T) {
	isolatedAgentDir(t)
	cwd := t.TempDir()
	source := makeSessionFile(t, cwd)

	args := &coding.Args{Session: strPtr(source.GetSessionFile())}
	sm, err := resumeSession(args, cwd, t.TempDir(), nil)
	if err != nil {
		t.Fatalf("resumeSession: %v", err)
	}
	if sm.GetSessionFile() != source.GetSessionFile() {
		t.Fatalf("resumed file %s, want %s", sm.GetSessionFile(), source.GetSessionFile())
	}
}

func TestResumeSessionGlobalMatchOpensInPlace(t *testing.T) {
	isolatedAgentDir(t)
	cwd := t.TempDir()
	otherCwd := t.TempDir()
	// Header cwd differs from the caller's cwd, so the local listing filters
	// it out; only the global scan finds it.
	source := makeSessionFile(t, otherCwd)

	args := &coding.Args{Session: strPtr(source.GetSessionID())}
	sm, err := resumeSession(args, cwd, t.TempDir(), nil)
	if err != nil {
		t.Fatalf("resumeSession: %v", err)
	}
	if sm.GetSessionID() != source.GetSessionID() {
		t.Fatalf("resumed id %s, want %s", sm.GetSessionID(), source.GetSessionID())
	}
	if sm.GetCwd() != otherCwd {
		t.Fatalf("resumed cwd %s, want %s", sm.GetCwd(), otherCwd)
	}
}

func TestResumeSessionNotFound(t *testing.T) {
	isolatedAgentDir(t)
	cwd := t.TempDir()

	args := &coding.Args{Session: strPtr("does-not-exist-anywhere")}
	_, err := resumeSession(args, cwd, t.TempDir(), nil)
	if err == nil {
		t.Fatal("expected an error for an unmatched --session argument")
	}
	if !strings.Contains(err.Error(), "No session found matching") {
		t.Fatalf("error %q, want it to name the unmatched argument", err)
	}
}

func TestResumeSessionIDCreatesNewSessionWithID(t *testing.T) {
	isolatedAgentDir(t)
	cwd := t.TempDir()

	args := &coding.Args{SessionID: strPtr("019463a7-1111-7111-8111-111111111111")}
	sm, err := resumeSession(args, cwd, t.TempDir(), nil)
	if err != nil {
		t.Fatalf("resumeSession: %v", err)
	}
	if sm.GetSessionID() != *args.SessionID {
		t.Fatalf("new session id %s, want %s", sm.GetSessionID(), *args.SessionID)
	}
	if !strings.HasSuffix(sm.GetSessionFile(), *args.SessionID+".jsonl") {
		t.Fatalf("session file %s should end with the custom id", sm.GetSessionFile())
	}
}

func TestResumeSessionIDConflict(t *testing.T) {
	isolatedAgentDir(t)
	cwd := t.TempDir()

	args := &coding.Args{SessionID: strPtr("019463a7-1111-7111-8111-111111111111"), Continue: true}
	if _, err := resumeSession(args, cwd, t.TempDir(), nil); err == nil {
		t.Fatal("expected an error when --session-id is combined with --continue")
	}
}

func TestResumeSessionIDInvalidFormat(t *testing.T) {
	isolatedAgentDir(t)
	cwd := t.TempDir()

	args := &coding.Args{SessionID: strPtr("bad id!")}
	if _, err := resumeSession(args, cwd, t.TempDir(), nil); err == nil {
		t.Fatal("expected an error for an invalid --session-id format")
	}
}

func TestResumeSessionFork(t *testing.T) {
	isolatedAgentDir(t)
	cwd := t.TempDir()
	source := makeSessionFile(t, cwd)

	args := &coding.Args{Fork: strPtr(source.GetSessionFile())}
	sm, err := resumeSession(args, cwd, t.TempDir(), nil)
	if err != nil {
		t.Fatalf("resumeSession: %v", err)
	}
	if sm.GetSessionID() == source.GetSessionID() {
		t.Fatal("fork must create a new session id")
	}
	header := sm.GetHeader()
	if header == nil || header.ParentSession == nil || *header.ParentSession != source.GetSessionFile() {
		t.Fatalf("fork header parentSession %+v, want %s", header, source.GetSessionFile())
	}
}

func TestResumeSessionForkByID(t *testing.T) {
	isolatedAgentDir(t)
	cwd := t.TempDir()
	source := makeSessionFile(t, cwd)

	args := &coding.Args{Fork: strPtr(source.GetSessionID())}
	sm, err := resumeSession(args, cwd, t.TempDir(), nil)
	if err != nil {
		t.Fatalf("resumeSession: %v", err)
	}
	if sm.GetSessionID() == source.GetSessionID() {
		t.Fatal("fork must create a new session id")
	}
}

func TestResumeSessionForkConflict(t *testing.T) {
	isolatedAgentDir(t)
	cwd := t.TempDir()

	args := &coding.Args{Fork: strPtr("x"), Continue: true}
	if _, err := resumeSession(args, cwd, t.TempDir(), nil); err == nil {
		t.Fatal("expected an error when --fork is combined with --continue")
	}

	args2 := &coding.Args{Fork: strPtr("x"), NoSession: true}
	if _, err := resumeSession(args2, cwd, t.TempDir(), nil); err == nil {
		t.Fatal("expected an error when --fork is combined with --no-session")
	}
}

func TestResumeSessionForkExistingSessionID(t *testing.T) {
	isolatedAgentDir(t)
	cwd := t.TempDir()
	source := makeSessionFile(t, cwd)

	args := &coding.Args{Fork: strPtr(source.GetSessionFile()), SessionID: strPtr(source.GetSessionID())}
	if _, err := resumeSession(args, cwd, t.TempDir(), nil); err == nil {
		t.Fatal("expected an error forking with an id that already exists")
	}
}

func TestResumeSessionForkNotFound(t *testing.T) {
	isolatedAgentDir(t)
	cwd := t.TempDir()

	args := &coding.Args{Fork: strPtr("does-not-exist-anywhere")}
	if _, err := resumeSession(args, cwd, t.TempDir(), nil); err == nil {
		t.Fatal("expected an error for an unmatched --fork argument")
	}
}

func TestResumeSessionPickerCancelled(t *testing.T) {
	isolatedAgentDir(t)
	cwd := t.TempDir()
	previous := selectResumeSession
	selectResumeSession = func(cwd, sessionDir string, settings *coding.SettingsManager) (string, bool) {
		return "", false
	}
	defer func() { selectResumeSession = previous }()

	args := &coding.Args{Resume: true}
	if _, err := resumeSession(args, cwd, t.TempDir(), nil); !errors.Is(err, errNoSessionSelected) {
		t.Fatalf("error %v, want errNoSessionSelected", err)
	}
}

func TestResumeSessionPickerSelection(t *testing.T) {
	isolatedAgentDir(t)
	cwd := t.TempDir()
	source := makeSessionFile(t, cwd)
	previous := selectResumeSession
	selectResumeSession = func(cwd, sessionDir string, settings *coding.SettingsManager) (string, bool) {
		return source.GetSessionFile(), true
	}
	defer func() { selectResumeSession = previous }()

	args := &coding.Args{Resume: true}
	sm, err := resumeSession(args, cwd, t.TempDir(), nil)
	if err != nil {
		t.Fatalf("resumeSession: %v", err)
	}
	if sm.GetSessionID() != source.GetSessionID() {
		t.Fatalf("resumed id %s, want %s", sm.GetSessionID(), source.GetSessionID())
	}
}
