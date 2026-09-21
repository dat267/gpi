package coding

import (
	"testing"

	"github.com/dat267/pier/ai"
)

// seedSession creates a persisted session with a full turn (the file flushes
// only once an assistant message exists, upstream flush-on-first-assistant).
func seedSession(t *testing.T, cwd string) *SessionManager {
	t.Helper()
	sm := NewSessionManager(cwd, nil)
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

// TestListAllSessionsIncludesAllProjects asserts the global scan returns
// sessions from every project dir regardless of their header cwd (upstream
// listAll -> listSessionsFromDir, which never cwd-filters).
func TestListAllSessionsIncludesAllProjects(t *testing.T) {
	t.Setenv("PI_CODING_AGENT_DIR", t.TempDir())
	cwdA := t.TempDir()
	cwdB := t.TempDir()
	a := seedSession(t, cwdA)
	b := seedSession(t, cwdB)

	all := ListAllSessions("")
	ids := map[string]bool{}
	for _, info := range all {
		ids[info.ID] = true
	}
	if !ids[a.GetSessionID()] {
		t.Fatalf("ListAllSessions missing session %s from %s", a.GetSessionID(), cwdA)
	}
	if !ids[b.GetSessionID()] {
		t.Fatalf("ListAllSessions missing session %s from %s", b.GetSessionID(), cwdB)
	}
}

// TestListAllSessionsExplicitDir asserts the explicit-directory overload lists
// that directory without a cwd filter (upstream listAll(customSessionDir)).
func TestListAllSessionsExplicitDir(t *testing.T) {
	t.Setenv("PI_CODING_AGENT_DIR", t.TempDir())
	sessionDir := t.TempDir()
	otherCwd := t.TempDir()
	sm := NewSessionManager(otherCwd, &SessionManagerOptions{SessionDir: sessionDir})
	sm.AppendMessage(&ai.UserMessage{Content: ai.StringOrBlocks{Text: "hello"}})
	sm.AppendMessage(&ai.AssistantMessage{
		API:        ai.APIAnthropicMessages,
		Provider:   "anthropic",
		Model:      "m",
		Content:    ai.ContentList{ai.TextContent{Text: "hi"}},
		StopReason: ai.StopStop,
	})

	all := ListAllSessions(sessionDir)
	found := false
	for _, info := range all {
		if info.ID == sm.GetSessionID() {
			found = true
		}
	}
	if !found {
		t.Fatalf("ListAllSessions(%q) missing session %s", sessionDir, sm.GetSessionID())
	}
}
