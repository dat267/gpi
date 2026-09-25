//go:build linux

package interactive

// The project trust boundary as the real binary draws it: trust belongs to the
// project a session runs in, not to the directory pier happened to start in.

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dat267/pier/ai"
	"github.com/dat267/pier/coding"
)

// sessionInProject writes a persisted session whose recorded cwd is project.
func sessionInProject(t *testing.T, project string) string {
	t.Helper()
	// Keep the session file out of the real agent dir.
	t.Setenv("PI_CODING_AGENT_DIR", t.TempDir())
	manager := coding.NewSessionManager(project, nil)
	manager.AppendMessage(&ai.UserMessage{Content: ai.StringOrBlocks{Text: "hello"}})
	manager.AppendMessage(&ai.AssistantMessage{
		API: ai.APIAnthropicMessages, Provider: "anthropic", Model: "m",
		Content: ai.ContentList{ai.TextContent{Text: "hi"}}, StopReason: ai.StopStop,
	})
	file := manager.GetSessionFile()
	if _, err := os.Stat(file); err != nil {
		t.Fatalf("session file: %v", err)
	}
	return file
}

// trustRequiringProject makes a project directory with a .pi settings file, which
// is what makes a project ask for a trust decision at all.
func trustRequiringProject(t *testing.T) string {
	t.Helper()
	project := t.TempDir()
	dir := filepath.Join(project, coding.ConfigDirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{"theme":"dark"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	return project
}

// Resuming a session from a project that was never trusted asks about *that*
// project: pier runs with the session's cwd (upstream builds the runtime for
// sessionManager.getCwd()), so the trust question is about that directory even
// though pier was started somewhere else entirely. A stored decision answers it
// without a prompt, and an untrusted answer hides the project's resources — the
// warning says so.
func TestResumedSessionAsksTrustForItsOwnProject(t *testing.T) {
	project := trustRequiringProject(t)
	sessionFile := sessionInProject(t, project)

	t.Run("undecided", func(t *testing.T) {
		session := startPier(t, "--session", sessionFile)
		if !session.waitForOutput("Trust project folder?", 30*time.Second) {
			t.Fatalf("the session's project was not asked about:\n%s", session.output())
		}
		if !session.waitForOutput(project, 5*time.Second) {
			t.Fatalf("the prompt named another directory:\n%s", session.output())
		}
	})

	t.Run("stored decision", func(t *testing.T) {
		session := startPierConfigured(t, func(agentDir string) {
			if err := coding.NewProjectTrustStore(agentDir).Set(project, boolRef(true)); err != nil {
				t.Fatal(err)
			}
		}, "--session", sessionFile)
		if !session.waitForOutput("Press ctrl+o", 30*time.Second) {
			t.Fatalf("pier did not finish starting:\n%s", session.output())
		}
		if session.waitForOutput("Trust project folder?", 2*time.Second) {
			t.Fatalf("a decided project was asked about anyway:\n%s", session.output())
		}
		if session.waitForOutput("This project is not trusted", 1*time.Second) {
			t.Fatalf("a trusted project warned anyway:\n%s", session.output())
		}
	})

	t.Run("stored refusal", func(t *testing.T) {
		session := startPierConfigured(t, func(agentDir string) {
			if err := coding.NewProjectTrustStore(agentDir).Set(project, boolRef(false)); err != nil {
				t.Fatal(err)
			}
		}, "--session", sessionFile)
		if !session.waitForOutput("This project is not trusted", 30*time.Second) {
			t.Fatalf("a refused project did not warn:\n%s", session.output())
		}
	})
}
