package interactive

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/dat267/pier/ai"
	"github.com/dat267/pier/coding"
)

// makePersistedSession creates a persisted session with one full turn so the
// file exists on disk (persistEntry flushes on the first assistant message).
func makePersistedSession(t *testing.T, cwd string, text string) *coding.SessionManager {
	t.Helper()
	sm := coding.NewSessionManager(cwd, nil)
	sm.AppendMessage(&ai.UserMessage{Content: ai.StringOrBlocks{Text: text}})
	sm.AppendMessage(&ai.AssistantMessage{
		API:        ai.APIAnthropicMessages,
		Provider:   "anthropic",
		Model:      "m",
		Content:    ai.ContentList{ai.TextContent{Text: "ack"}},
		StopReason: ai.StopStop,
	})
	return sm
}

func TestSwitchSessionSwapsAndRebinds(t *testing.T) {
	app, cleanup := newTestApp(t)
	defer cleanup()
	t.Setenv("PI_CODING_AGENT_DIR", t.TempDir())

	targetCwd := t.TempDir()
	target := makePersistedSession(t, targetCwd, "from the other session")

	result, err := app.SwitchSession(context.Background(), target.GetSessionFile(), "")
	if err != nil {
		t.Fatalf("SwitchSession: %v", err)
	}
	if result == nil || result.Cancelled {
		t.Fatalf("switch cancelled: %+v", result)
	}
	if app.SessionMgr.GetSessionID() != target.GetSessionID() {
		t.Fatalf("app session manager id %s, want %s", app.SessionMgr.GetSessionID(), target.GetSessionID())
	}
	// The AppSession adapter is shared by every wiring; the embedded agent
	// session must follow the swap.
	if app.Session.AgentSession.SessionID() != target.GetSessionID() {
		t.Fatalf("app session id %s, want %s", app.Session.AgentSession.SessionID(), target.GetSessionID())
	}
	// The session-info holders must all point at the new manager.
	for _, info := range []*coding.SessionManager{
		app.Sessions.SessionInfo, app.Commands.SessionInfo, app.Events.SessionInfo,
		app.Startup.SessionInfo, app.Selectors.SessionInfo, app.Trust.SessionInfo,
		app.Autocomplete.SessionInfo, app.Transcript.SessionInfo,
	} {
		if info.GetSessionID() != target.GetSessionID() {
			t.Fatalf("wiring still holds session %s, want %s", info.GetSessionID(), target.GetSessionID())
		}
	}
	// The transcript was rebuilt from the new session.
	if len(app.Chat.Children) == 0 {
		t.Fatal("chat is empty after switching to a session with messages")
	}
	// The event pump is re-attached to the new session.
	if app.unsubscribe == nil {
		t.Fatal("session events are not subscribed after the switch")
	}
}

func TestSwitchSessionMissingCwdIsPromptable(t *testing.T) {
	app, cleanup := newTestApp(t)
	defer cleanup()
	t.Setenv("PI_CODING_AGENT_DIR", t.TempDir())

	targetCwd := t.TempDir()
	target := makePersistedSession(t, targetCwd, "gone")
	if err := os.Remove(targetCwd); err != nil {
		t.Fatalf("remove cwd: %v", err)
	}

	_, err := app.SwitchSession(context.Background(), target.GetSessionFile(), "")
	if err == nil {
		t.Fatal("expected an error for a missing session cwd")
	}
	if !strings.Contains(err.Error(), "cwd") {
		t.Fatalf("error %q should mention the cwd so the resume flow can prompt", err)
	}
}

func TestSessionNewStartsFreshSession(t *testing.T) {
	app, cleanup := newTestApp(t)
	defer cleanup()

	previous := app.SessionMgr.GetSessionID()
	result, err := app.SessionNew(context.Background())
	if err != nil {
		t.Fatalf("SessionNew: %v", err)
	}
	if result == nil || result.Cancelled {
		t.Fatalf("new session cancelled: %+v", result)
	}
	if app.SessionMgr.GetSessionID() == previous {
		t.Fatal("session id did not change")
	}
	if app.Session.SessionID() != app.SessionMgr.GetSessionID() {
		t.Fatalf("app session id %s does not match the manager %s",
			app.Session.AgentSession.SessionID(), app.SessionMgr.GetSessionID())
	}
	if len(app.Chat.Children) != 0 {
		t.Fatalf("chat should be empty for a fresh session, got %d children",
			len(app.Chat.Children))
	}
}

func TestSwitchSessionUpdatesFooterCwd(t *testing.T) {
	app, cleanup := newTestApp(t)
	defer cleanup()
	t.Setenv("PI_CODING_AGENT_DIR", t.TempDir())

	targetCwd := t.TempDir()
	target := makePersistedSession(t, targetCwd, "other project")

	if _, err := app.SwitchSession(context.Background(), target.GetSessionFile(), ""); err != nil {
		t.Fatalf("SwitchSession: %v", err)
	}
	if app.FooterData.Cwd() != targetCwd {
		t.Fatalf("footer cwd %s, want %s", app.FooterData.Cwd(), targetCwd)
	}
}
