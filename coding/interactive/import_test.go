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
	app.sessionMgr = coding.NewSessionManager(app.sessionMgr.GetCwd(), &coding.SessionManagerOptions{
		SessionDir: dir, Persist: &persist,
	})
	return dir
}

// answerConfirm drives the confirm dialog the import flow shows, choosing the
// option at the given index (0 = Yes/Continue).
func answerConfirm(t *testing.T, app *App, option int) {
	t.Helper()
	if !app.slot.HasActiveSelector() {
		t.Fatal("the import flow showed no dialog")
	}
	selector, ok := app.slot.ActiveSelectorComponent().(*ExtensionSelectorComponent)
	if !ok {
		t.Fatalf("dialog = %T, want the extension selector", app.slot.ActiveSelectorComponent())
	}
	for i := 0; i < option; i++ {
		selector.HandleInput("\x1b[B")
	}
	selector.HandleInput("\r")
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
	answerConfirm(t, app, 0)

	copied := filepath.Join(sessionDir, filepath.Base(source))
	if _, err := os.Stat(copied); err != nil {
		t.Fatalf("not copied into the session dir: %v", err)
	}
	if got := app.sessionMgr.GetSessionFile(); got != copied {
		t.Errorf("session file = %q, want the copy %q", got, copied)
	}
	if messages := app.sessionMgr.GetEntries(); len(messages) == 0 {
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
	answerConfirm(t, app, 0)

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
	answerConfirm(t, app, 0)

	if got := app.sessionMgr.GetSessionFile(); got != target {
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
	answerConfirm(t, app, 0)

	if !transcriptContains(app, "Failed to import session: File not found: ") {
		t.Errorf("no file-not-found report: %q", transcriptTexts(app))
	}
}

// A session whose stored working directory is gone asks whether to continue in
// the fallback, and imports there when the answer is yes (upstream
// MissingSessionCwdError → promptForMissingSessionCwd → retry).
func TestImportCommandMissingCwdPrompt(t *testing.T) {
	newGoneSession := func(t *testing.T) string {
		t.Helper()
		gone := t.TempDir()
		source := writeImportableSession(t, gone)
		if err := os.RemoveAll(gone); err != nil {
			t.Fatal(err)
		}
		return source
	}

	t.Run("continue imports in the fallback cwd", func(t *testing.T) {
		app, cleanup := newTestApp(t)
		defer cleanup()
		sessionDir := withSessionDir(t, app)
		source := newGoneSession(t)

		newCommandWiring(app).HandleImportCommand(context.Background(), "/import "+source)
		answerConfirm(t, app, 0) // "Replace current session?" — yes
		answerConfirm(t, app, 0) // "Session cwd not found" — continue in the fallback

		if !transcriptHasStatus(app, "Session imported from: "+source) {
			t.Errorf("no import status: %q", transcriptTexts(app))
		}
		// The imported session lives in this project's session directory. Its name
		// is not pinned: the first attempt copies before it fails the cwd check, so
		// the retry takes a numbered name — upstream leaves that copy behind too.
		imported := app.sessionMgr.GetSessionFile()
		if filepath.Dir(imported) != sessionDir {
			t.Errorf("session file = %q, want a copy in %q", imported, sessionDir)
		}
		if !strings.HasPrefix(filepath.Base(imported), strings.TrimSuffix(filepath.Base(source), ".jsonl")) {
			t.Errorf("session file = %q, want it derived from %q", imported, filepath.Base(source))
		}
	})

	t.Run("cancel reports the cancellation", func(t *testing.T) {
		app, cleanup := newTestApp(t)
		defer cleanup()
		withSessionDir(t, app)
		source := newGoneSession(t)

		newCommandWiring(app).HandleImportCommand(context.Background(), "/import "+source)
		answerConfirm(t, app, 0) // replace the current session
		answerConfirm(t, app, 1) // cancel at the cwd prompt

		if !transcriptHasStatus(app, "Import cancelled") {
			t.Errorf("no cancellation status: %q", transcriptTexts(app))
		}
	})
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
