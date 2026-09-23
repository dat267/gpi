package interactive

import (
	"context"
	"testing"

	"github.com/dat267/pier/ai"
)

// appendTurn adds a user message and its answer. The session already holds the
// model and thinking-level bookkeeping entries that creation records, so tests
// address messages through GetUserMessagesForForking rather than by position.
func appendTurn(t *testing.T, app *App, userText string) {
	t.Helper()
	app.SessionMgr.AppendMessage(&ai.UserMessage{Content: ai.StringOrBlocks{Text: userText}})
	app.SessionMgr.AppendMessage(&ai.AssistantMessage{
		API: ai.APIAnthropicMessages, Provider: "anthropic", Model: "m",
		Content: ai.ContentList{ai.TextContent{Text: "ok"}}, StopReason: ai.StopStop,
	})
}

func userMessageTexts(app *App) []string {
	forks := app.Session.GetUserMessagesForForking()
	texts := make([]string, 0, len(forks))
	for _, fork := range forks {
		texts = append(texts, fork.Text)
	}
	return texts
}

// lastEntryID returns the id of the session's final entry (the assistant message
// after appendTurn).
func lastEntryID(t *testing.T, app *App) string {
	t.Helper()
	entries := app.SessionMgr.GetEntries()
	if len(entries) == 0 {
		t.Fatal("no entries")
	}
	return entries[len(entries)-1].ID
}

// /clone duplicates the session at the current position through the runtime
// fork, which the message-fork shares.
func TestCloneCommandDuplicatesAtTheCurrentPosition(t *testing.T) {
	app, cleanup := newTestApp(t)
	defer cleanup()

	run := newSubmitWiring(app).Handlers.HandleCloneCommand
	if run == nil {
		t.Fatal("the submit wiring has no clone handler")
	}

	appendTurn(t, app, "hello")
	leaf := app.SessionMgr.GetLeafID()
	if leaf == nil {
		t.Fatal("no leaf after appending a turn")
	}
	beforeID := app.SessionMgr.GetSessionID()

	if err := run(); err != nil {
		t.Fatal(err)
	}
	if app.SessionMgr.GetSessionID() == beforeID {
		t.Error("the clone kept the source session id")
	}
	// The clone holds the same entries, so it sits at the same position.
	if got := app.SessionMgr.GetLeafID(); got == nil || *got != *leaf {
		t.Errorf("leaf = %v, want %q", got, *leaf)
	}
	if texts := userMessageTexts(app); len(texts) != 1 || texts[0] != "hello" {
		t.Errorf("the clone's context = %v, want the conversation", texts)
	}
	if !transcriptHasStatus(app, "Cloned to new session") {
		t.Errorf("no clone status: %q", transcriptTexts(app))
	}
}

// Forking "before" a user message leaves the message out and hands its text
// back for the editor, which is what the message selector does.
func TestForkAtEntryBeforeAMessage(t *testing.T) {
	app, cleanup := newTestApp(t)
	defer cleanup()

	appendTurn(t, app, "first question")
	forks := app.Session.GetUserMessagesForForking()
	if len(forks) != 1 {
		t.Fatalf("user messages = %d, want 1", len(forks))
	}

	result, err := app.forkAtEntry(context.Background(), forks[0].EntryID, false)
	if err != nil {
		t.Fatal(err)
	}
	if result.SelectedText == nil || *result.SelectedText != "first question" {
		t.Errorf("selected text = %v, want the message", result.SelectedText)
	}
	if texts := userMessageTexts(app); len(texts) != 0 {
		t.Errorf("the fork kept %v, want the message left out", texts)
	}
	// The app-level fork re-records model/thinking entries on the new session, so
	// a leaf exists again; the session-level test pins the fork's own leaf.
}

// "at" keeps the entry instead, and a non-user or unknown entry is refused.
func TestForkAtEntryBoundaries(t *testing.T) {
	app, cleanup := newTestApp(t)
	defer cleanup()

	appendTurn(t, app, "hello")
	forks := app.Session.GetUserMessagesForForking()
	if len(forks) != 1 {
		t.Fatalf("user messages = %d, want 1", len(forks))
	}
	// Captured before the at-fork replaces the session, which drops the answer.
	assistantID := lastEntryID(t, app)

	result, err := app.forkAtEntry(context.Background(), forks[0].EntryID, true)
	if err != nil {
		t.Fatal(err)
	}
	if result.SelectedText != nil {
		t.Errorf("selected text = %v, want none for an at-fork", result.SelectedText)
	}
	if texts := userMessageTexts(app); len(texts) != 1 || texts[0] != "hello" {
		t.Errorf("the at-fork kept %v, want the message", texts)
	}

	// "before" only accepts a user message.
	if _, err := app.forkAtEntry(context.Background(), assistantID, false); err == nil {
		t.Error("forking before an assistant message should be refused")
	}
	if _, err := app.forkAtEntry(context.Background(), "no-such-entry", true); err == nil {
		t.Error("an unknown entry id should be refused")
	}
}

// The message selector forks through the same runtime fork, so /fork is wired by
// the same assignment as /clone.
func TestMessageSelectorForkIsWired(t *testing.T) {
	app, cleanup := newTestApp(t)
	defer cleanup()
	if newSelectorWiring(app).RuntimeFork == nil {
		t.Fatal("the selector wiring has no runtime fork, so forking from a message does nothing")
	}
}
