package interactive

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dat267/pier/ai"
	"github.com/dat267/pier/coding"
)

// withSessionDir gives the app's session manager an explicit session directory:
// the test app's manager is in-memory, so its default would be derived from the
// real agent directory.
func withSessionDir(t *testing.T, app *App) string {
	t.Helper()
	dir := t.TempDir()
	persist := false
	app.SessionMgr = coding.NewSessionManager(app.SessionMgr.GetCwd(), &coding.SessionManagerOptions{
		SessionDir: dir, Persist: &persist,
	})
	return dir
}

// writeImportableSession writes a session file under its own directory, the way
// a session from another project or machine would look.
func writeImportableSession(t *testing.T, dir string) string {
	t.Helper()
	manager := coding.NewSessionManager(dir, nil)
	manager.AppendMessage(&ai.UserMessage{Content: ai.StringOrBlocks{Text: "imported question"}})
	manager.AppendMessage(&ai.AssistantMessage{
		API: ai.APIAnthropicMessages, Provider: "test", Model: "m",
		Content: ai.ContentList{ai.TextContent{Text: "imported answer"}}, StopReason: ai.StopStop,
	})
	file := manager.GetSessionFile()
	if file == "" {
		t.Fatal("no session file was written")
	}
	if _, err := os.Stat(file); err != nil {
		t.Fatalf("session file: %v", err)
	}
	return file
}

// /import copies the file into this project's session directory and switches to
// it, so the imported session becomes part of the local session list.
func TestImportCommandCopiesIntoTheSessionDir(t *testing.T) {
	app, cleanup := newTestApp(t)
	defer cleanup()

	sessionDir := withSessionDir(t, app)
	source := writeImportableSession(t, t.TempDir())
	newCommandWiring(app).HandleImportCommand(context.Background(), "/import "+source)

	copied := filepath.Join(sessionDir, filepath.Base(source))
	if _, err := os.Stat(copied); err != nil {
		t.Fatalf("not copied into the session dir: %v", err)
	}
	if got := app.SessionMgr.GetSessionFile(); got != copied {
		t.Errorf("session file = %q, want the copy %q", got, copied)
	}
	if messages := app.SessionMgr.GetEntries(); len(messages) == 0 {
		t.Error("the imported session has no entries")
	}
	if !transcriptHasStatus(app, "Session imported from: "+source) {
		t.Errorf("no import status: %q", transcriptTexts(app))
	}
}

// An existing session with the same name is never clobbered: the copy takes a
// numbered name instead.
func TestImportCommandDoesNotClobber(t *testing.T) {
	app, cleanup := newTestApp(t)
	defer cleanup()

	sessionDir := withSessionDir(t, app)
	source := writeImportableSession(t, t.TempDir())
	existing := filepath.Join(sessionDir, filepath.Base(source))
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(existing, []byte("someone else's session\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	newCommandWiring(app).HandleImportCommand(context.Background(), "/import "+source)

	content, err := os.ReadFile(existing)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "someone else's session\n" {
		t.Errorf("the existing session file was replaced: %q", content)
	}
	numbered := strings.TrimSuffix(existing, ".jsonl") + "-1.jsonl"
	if _, err := os.Stat(numbered); err != nil {
		t.Errorf("the copy did not take a numbered name: %v", err)
	}
}

// A file that is already stored is used in place rather than copied again.
func TestImportCommandUsesAStoredFileInPlace(t *testing.T) {
	app, cleanup := newTestApp(t)
	defer cleanup()

	sessionDir := withSessionDir(t, app)
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	stored := writeImportableSession(t, t.TempDir())
	target := filepath.Join(sessionDir, filepath.Base(stored))
	content, err := os.ReadFile(stored)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, content, 0o644); err != nil {
		t.Fatal(err)
	}

	newCommandWiring(app).HandleImportCommand(context.Background(), "/import "+target)

	if got := app.SessionMgr.GetSessionFile(); got != target {
		t.Errorf("session file = %q, want the stored file %q", got, target)
	}
	if numbered := strings.TrimSuffix(target, ".jsonl") + "-1.jsonl"; fileExists(numbered) {
		t.Error("a stored file was copied instead of used in place")
	}
}

func TestImportCommandMissingFile(t *testing.T) {
	app, cleanup := newTestApp(t)
	defer cleanup()

	missing := filepath.Join(t.TempDir(), "nope.jsonl")
	newCommandWiring(app).HandleImportCommand(context.Background(), "/import "+missing)

	if !transcriptContains(app, "Failed to import session: File not found: ") {
		t.Errorf("no file-not-found report: %q", transcriptTexts(app))
	}
}

// A session whose stored working directory is gone reports the cwd, which is
// what the retry path looks for (upstream MissingSessionCwdError).
func TestImportCommandMissingCwd(t *testing.T) {
	app, cleanup := newTestApp(t)
	defer cleanup()

	gone := t.TempDir()
	source := writeImportableSession(t, gone)
	if err := os.RemoveAll(gone); err != nil {
		t.Fatal(err)
	}

	newCommandWiring(app).HandleImportCommand(context.Background(), "/import "+source)

	if !transcriptContains(app, "cwd") {
		t.Errorf("no cwd report: %q", transcriptTexts(app))
	}
}

// A usage error is reported rather than silently ignored.
func TestImportCommandRequiresAPath(t *testing.T) {
	app, cleanup := newTestApp(t)
	defer cleanup()

	newCommandWiring(app).HandleImportCommand(context.Background(), "/import")
	if !transcriptContains(app, "Usage: /import <path.jsonl>") {
		t.Errorf("no usage error: %q", transcriptTexts(app))
	}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// transcriptContains looks for a line in the chat text components.
func transcriptContains(app *App, needle string) bool {
	for _, text := range transcriptTexts(app) {
		if strings.Contains(text, needle) {
			return true
		}
	}
	return false
}
