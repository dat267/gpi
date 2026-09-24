package interactive

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/dat267/pier/ai"
	"github.com/dat267/pier/coding"
	"github.com/dat267/pier/tui"
)

// selectorTestSession implements SelectorSession.
type selectorTestSession struct {
	thinkingLevel   ai.ThinkingLevel
	levels          []ai.ThinkingLevel
	forkMessages    []coding.UserMessageFork
	abortCalls      int
	abortSummary    int
	compacting      bool
	streaming       bool
	navigateResult  *coding.NavigateTreeResult
	navigateErr     error
	navigateCalls   []string
	navigateOptions []coding.NavigateTreeOptions
	onAbort         func()
	onNavigate      func()
}

func (s *selectorTestSession) ThinkingLevel() ai.ThinkingLevel { return s.thinkingLevel }
func (s *selectorTestSession) GetAvailableThinkingLevels() []ai.ThinkingLevel {
	return s.levels
}
func (s *selectorTestSession) SetThinkingLevel(level ai.ThinkingLevel, _ ...coding.ModelMutationOptions) {
	s.thinkingLevel = level
}
func (s *selectorTestSession) GetUserMessagesForForking() []coding.UserMessageFork {
	return s.forkMessages
}
func (s *selectorTestSession) NavigateTree(_ context.Context, targetID string, options coding.NavigateTreeOptions) (*coding.NavigateTreeResult, error) {
	s.navigateCalls = append(s.navigateCalls, targetID)
	s.navigateOptions = append(s.navigateOptions, options)
	if s.onNavigate != nil {
		s.onNavigate()
	}
	return s.navigateResult, s.navigateErr
}
func (s *selectorTestSession) IsStreaming() bool  { return s.streaming }
func (s *selectorTestSession) IsCompacting() bool { return s.compacting }
func (s *selectorTestSession) Abort(context.Context) {
	s.abortCalls++
	if s.onAbort != nil {
		s.onAbort()
	}
}
func (s *selectorTestSession) AbortBranchSummary() { s.abortSummary++ }

func newSelectorTestWiring(t *testing.T) (*SelectorWiring, *selectorTestSession, *tui.Container, *CustomEditor) {
	t.Helper()
	SetCustomThemesDir(t.TempDir())
	SetRegisteredThemes(nil)
	SetTrueColorSupport(true)
	SetStyleColorsEnabled(true)
	InitTheme("dark", false)

	screen := tui.NewMainScreen(&fakeRendererTerminal{width: 80, height: 24}, false, "")
	screen.DisableAutoRender()
	editorContainer := &tui.Container{}
	editor := NewCustomEditor(editorTestHost{}, tui.EditorTheme{}, NewAppKeybindingsManager(nil, ""), CustomEditorOptions{})
	editorContainer.AddChild(editor)
	settings := coding.NewInMemorySettingsManager(nil, coding.SettingsManagerCreateOptions{})
	session := &selectorTestSession{thinkingLevel: "medium", levels: []ai.ThinkingLevel{"off", "medium", "high"}}
	wiring := &SelectorWiring{
		Slot:     NewSelectorSlot(screen, editorContainer, editor),
		Session:  session,
		Settings: settings,
	}
	return wiring, session, editorContainer, editor
}

// TestSelectorSlot covers the editor-replacement plumbing.
func TestSelectorSlot(t *testing.T) {
	wiring, _, container, editor := newSelectorTestWiring(t)
	slot := wiring.Slot

	selector := &staticRendererComponent{lines: []string{"selector"}}
	focus := &staticRendererComponent{lines: []string{"focus"}}
	var done1 func()
	slot.Show(func(done func()) CreatedSelector {
		done1 = done
		return CreatedSelector{Component: selector, Focus: focus}
	})
	if !slot.HasActiveSelector() {
		t.Fatal("selector not active")
	}
	if len(container.Children) != 1 || container.Children[0] != tui.Component(selector) {
		t.Fatalf("children = %v", container.Children)
	}
	done1()
	if slot.HasActiveSelector() {
		t.Fatal("selector still active")
	}
	if len(container.Children) != 1 || container.Children[0] != tui.Component(editor) {
		t.Fatal("editor not restored")
	}

	// A stale done callback from a replaced selector is ignored.
	var done2, done3 func()
	slot.Show(func(done func()) CreatedSelector {
		done2 = done
		return CreatedSelector{Component: selector, Focus: focus}
	})
	slot.Show(func(done func()) CreatedSelector {
		done3 = done
		return CreatedSelector{Component: selector, Focus: focus}
	})
	done2() // stale
	if !slot.HasActiveSelector() {
		t.Fatal("stale done closed the active selector")
	}
	done3()
	if slot.HasActiveSelector() {
		t.Fatal("active selector not closed")
	}

	// DisposeActiveSelector does not restore the editor.
	slot.Show(func(done func()) CreatedSelector {
		return CreatedSelector{Component: selector, Focus: focus}
	})
	slot.DisposeActiveSelector()
	if slot.HasActiveSelector() {
		t.Fatal("selector still active after dispose")
	}
}

// TestSelectorThinking covers the thinking selector and command.
func TestSelectorThinking(t *testing.T) {
	wiring, session, _, _ := newSelectorTestWiring(t)
	statuses := []string{}
	errors := []string{}
	wiring.ShowStatus = func(message string) { statuses = append(statuses, message) }
	wiring.ShowError = func(message string) { errors = append(errors, message) }

	// Unknown level.
	wiring.HandleThinkingCommand("bogus")
	if len(errors) != 1 || !strings.Contains(errors[0], `Unknown thinking level "bogus"`) {
		t.Fatalf("errors = %v", errors)
	}
	// Exact match (case-insensitive).
	wiring.HandleThinkingCommand("HIGH")
	if session.thinkingLevel != "high" {
		t.Fatalf("level = %q", session.thinkingLevel)
	}
	if statuses[len(statuses)-1] != "Thinking level: high" {
		t.Fatalf("statuses = %v", statuses)
	}
	// Empty search opens the selector.
	wiring.HandleThinkingCommand("")
	if !wiring.Slot.HasActiveSelector() {
		t.Fatal("selector not opened")
	}
	// The selector's select callback sets the level and closes the slot.
	selector := wiring.Slot.ActiveSelectorComponent()
	thinking, ok := selector.(*ThinkingSelectorComponent)
	if !ok {
		t.Fatalf("selector type = %T", selector)
	}
	thinking.Select("off")
	if session.thinkingLevel != "off" || wiring.Slot.HasActiveSelector() {
		t.Fatalf("level = %q, active = %v", session.thinkingLevel, wiring.Slot.HasActiveSelector())
	}
}

// TestSelectorUserMessages covers the fork selector.
func TestSelectorUserMessages(t *testing.T) {
	wiring, session, _, _ := newSelectorTestWiring(t)
	statuses := []string{}
	wiring.ShowStatus = func(message string) { statuses = append(statuses, message) }

	// No messages.
	wiring.ShowUserMessageSelector(context.Background())
	if statuses[len(statuses)-1] != "No messages to fork from" {
		t.Fatalf("statuses = %v", statuses)
	}

	session.forkMessages = []coding.UserMessageFork{
		{EntryID: "e1", Text: "first"},
		{EntryID: "e2", Text: "second"},
	}
	selected := ""
	wiring.RuntimeFork = func(_ context.Context, entryID string, _ bool) (*SelectorForkResult, error) {
		selected = entryID
		text := "forked text"
		return &SelectorForkResult{SelectedText: &text}, nil
	}
	wiring.OnEditorText = func(text string) { selected += ":" + text }
	wiring.ShowUserMessageSelector(context.Background())
	selector := wiring.Slot.ActiveSelectorComponent()
	userSelector, ok := selector.(*UserMessageSelectorComponent)
	if !ok {
		t.Fatalf("selector type = %T", selector)
	}
	// The last message is preselected.
	if userSelector.GetMessageList().SelectedIndex() != 1 {
		t.Fatalf("selected index = %d", userSelector.GetMessageList().SelectedIndex())
	}
	userSelector.Select("e2")
	if selected != "e2:forked text" {
		t.Fatalf("selected = %q", selected)
	}
	if statuses[len(statuses)-1] != "Forked to new session" {
		t.Fatalf("statuses = %v", statuses)
	}
}

// TestSelectorTrust covers the trust selector.
func TestSelectorTrust(t *testing.T) {
	wiring, _, _, _ := newSelectorTestWiring(t)
	wiring.AgentDir = t.TempDir()
	manager := coding.NewSessionManager("/tmp/proj", &coding.SessionManagerOptions{Persist: boolPtr(false)})
	wiring.SessionInfo = manager
	statuses := []string{}
	wiring.ShowStatus = func(message string) { statuses = append(statuses, message) }

	wiring.ShowTrustSelector()
	if !wiring.Slot.HasActiveSelector() {
		t.Fatal("trust selector not opened")
	}
	selector := wiring.Slot.ActiveSelectorComponent()
	trust, ok := selector.(*TrustSelectorComponent)
	if !ok {
		t.Fatalf("selector type = %T", selector)
	}
	// Select trust: the decision is persisted and the status shown.
	trust.Select(TrustSelection{Trusted: true, Updates: []coding.ProjectTrustUpdate{
		{Path: "/tmp/proj", Decision: boolPtr(true)},
	}})
	if !strings.Contains(statuses[len(statuses)-1], "Saved trust decision: trusted") {
		t.Fatalf("statuses = %v", statuses)
	}
	store := coding.NewProjectTrustStore(wiring.AgentDir)
	if entry := store.GetEntry("/tmp/proj"); entry == nil || !entry.Decision {
		t.Fatalf("trust not persisted: %+v", entry)
	}
}

// TestSelectorTree covers the tree selector and navigation.
func TestSelectorTree(t *testing.T) {
	wiring, session, _, _ := newSelectorTestWiring(t)
	statuses := []string{}
	errors := []string{}
	wiring.ShowStatus = func(message string) { statuses = append(statuses, message) }
	wiring.ShowError = func(message string) { errors = append(errors, message) }
	wiring.TerminalRows = func() int { return 30 }

	manager := coding.NewSessionManager("/tmp/proj", &coding.SessionManagerOptions{Persist: boolPtr(false)})
	wiring.SessionInfo = manager

	// No entries.
	wiring.ShowTreeSelector(context.Background(), "", false)
	if statuses[len(statuses)-1] != "No entries in session" {
		t.Fatalf("statuses = %v", statuses)
	}

	// Add a user message so the tree has one entry.
	manager.AppendMessage(ai.Message(&ai.UserMessage{Content: ai.StringOrBlocks{Text: "hello"}}))
	wiring.ShowTreeSelector(context.Background(), "", false)
	selector := wiring.Slot.ActiveSelectorComponent()
	tree, ok := selector.(*TreeSelectorComponent)
	if !ok {
		t.Fatalf("selector type = %T", selector)
	}
	// Selecting the current leaf is a no-op.
	leaf := manager.GetLeafID()
	tree.GetTreeList().OnSelect(*leaf)
	if statuses[len(statuses)-1] != "Already at this point" {
		t.Fatalf("statuses = %v", statuses)
	}

	// Navigating to another entry reports success.
	session.navigateResult = &coding.NavigateTreeResult{}
	wiring.ShowTreeSelector(context.Background(), "", false)
	tree = wiring.Slot.ActiveSelectorComponent().(*TreeSelectorComponent)
	tree.GetTreeList().OnSelect("other-entry")
	if len(session.navigateCalls) != 1 || session.navigateCalls[0] != "other-entry" {
		t.Fatalf("navigate calls = %v", session.navigateCalls)
	}
	if statuses[len(statuses)-1] != "Navigated to selected point" {
		t.Fatalf("statuses = %v", statuses)
	}

	// A compaction in flight reports the guard error.
	session.compacting = true
	wiring.ShowTreeSelector(context.Background(), "", false)
	tree = wiring.Slot.ActiveSelectorComponent().(*TreeSelectorComponent)
	tree.GetTreeList().OnSelect("another")
	if len(errors) != 1 || !strings.Contains(errors[0], "Wait for the current compaction") {
		t.Fatalf("errors = %v", errors)
	}
}

// Upstream's /tree navigation does more than move the leaf: it rebuilds the
// transcript (chatContainer.clear() then renderInitialMessages()), offers the
// navigated point's message text in the editor when the editor is empty, and
// flushes the compaction queue (interactive-mode.ts, handleTreeSelection). The
// port moved the leaf and reported success, so the screen kept the old branch.
func TestTreeNavigationRebuildsTranscript(t *testing.T) {
	wiring, session, _, _ := newSelectorTestWiring(t)
	statuses := []string{}
	wiring.ShowStatus = func(message string) { statuses = append(statuses, message) }
	wiring.TerminalRows = func() int { return 30 }

	manager := coding.NewSessionManager("/tmp/proj", &coding.SessionManagerOptions{Persist: boolPtr(false)})
	wiring.SessionInfo = manager
	manager.AppendMessage(ai.Message(&ai.UserMessage{Content: ai.StringOrBlocks{Text: "hello"}}))

	rebuilds := 0
	flushes := 0
	editorTexts := []string{}
	wiring.RebuildChat = func() { rebuilds++ }
	wiring.FlushCompactionQueue = func() { flushes++ }
	wiring.SetNavigatedEditorText = func(text string) { editorTexts = append(editorTexts, text) }

	selectEntry := func(entryID string) {
		t.Helper()
		wiring.ShowTreeSelector(context.Background(), "", false)
		tree, ok := wiring.Slot.ActiveSelectorComponent().(*TreeSelectorComponent)
		if !ok {
			t.Fatalf("selector type = %T", wiring.Slot.ActiveSelectorComponent())
		}
		tree.GetTreeList().OnSelect(entryID)
	}

	// A successful navigation rebuilds the transcript and offers the point's text.
	session.navigateResult = &coding.NavigateTreeResult{EditorText: "SECOND QUESTION about beta"}
	selectEntry("earlier-entry")
	if rebuilds != 1 {
		t.Errorf("rebuilds = %d, want the transcript rebuilt once", rebuilds)
	}
	if len(editorTexts) != 1 || editorTexts[0] != "SECOND QUESTION about beta" {
		t.Errorf("editor texts = %v", editorTexts)
	}
	if flushes != 1 {
		t.Errorf("flushes = %d, want the compaction queue flushed once", flushes)
	}
	if statuses[len(statuses)-1] != "Navigated to selected point" {
		t.Errorf("statuses = %v", statuses)
	}

	// A point whose message has no text leaves the editor alone.
	session.navigateResult = &coding.NavigateTreeResult{}
	selectEntry("no-text-entry")
	if rebuilds != 2 {
		t.Errorf("rebuilds = %d", rebuilds)
	}
	if len(editorTexts) != 1 {
		t.Errorf("an empty editor text should not reach the editor: %v", editorTexts)
	}

	// Cancelled and aborted navigations leave the screen alone.
	session.navigateResult = &coding.NavigateTreeResult{Cancelled: true}
	selectEntry("cancelled-entry")
	if rebuilds != 2 || flushes != 2 || len(editorTexts) != 1 {
		t.Errorf("a cancelled navigation rebuilt the transcript: rebuilds=%d flushes=%d texts=%v", rebuilds, flushes, editorTexts)
	}
	session.navigateResult = &coding.NavigateTreeResult{Aborted: true}
	selectEntry("aborted-entry")
	if rebuilds != 2 || flushes != 2 || len(editorTexts) != 1 {
		t.Errorf("an aborted navigation rebuilt the transcript: rebuilds=%d flushes=%d texts=%v", rebuilds, flushes, editorTexts)
	}
}

// Upstream's tree navigation shows the branch-summary indicator while the
// summary runs, points the editor's escape at abortBranchSummary, and clears
// both when the navigation finishes — on success, cancel and error alike
// (interactive-mode.ts, handleTreeSelection's try/finally). The port computed
// showingSummaryIndicator and then discarded it, and the indicator seam was a
// no-op, so a long summarization ran with nothing on screen saying escape
// would abort it.
func TestTreeNavigationSummaryIndicatorAndEscape(t *testing.T) {
	wiring, session, _, _ := newSelectorTestWiring(t)
	errorMessages := []string{}
	wiring.ShowError = func(message string) { errorMessages = append(errorMessages, message) }
	wiring.TerminalRows = func() int { return 30 }

	manager := coding.NewSessionManager("/tmp/proj", &coding.SessionManagerOptions{Persist: boolPtr(false)})
	wiring.SessionInfo = manager
	manager.AppendMessage(ai.Message(&ai.UserMessage{Content: ai.StringOrBlocks{Text: "hello"}}))

	// "Summarize" is chosen in the dialog every time.
	wiring.ShowExtensionSelector = func(context.Context, string, []string) (string, bool) {
		return "Summarize", true
	}

	shown := []StatusIndicatorKind{}
	cleared := []StatusIndicatorKind{}
	spacers := 0
	wiring.ShowStatusIndicator = func(kind StatusIndicatorKind) { shown = append(shown, kind) }
	wiring.ClearStatusIndicator = func(kind StatusIndicatorKind) { cleared = append(cleared, kind) }
	wiring.AddChatSpacer = func() { spacers++ }

	original := func() {}
	escape := original
	wiring.EditorEscapeHandler = func() func() { return escape }
	wiring.SetEditorEscapeHandler = func(handler func()) { escape = handler }

	// What the UI looks like from inside navigateTree, plus what escape does then.
	escapeDuringNavigation := func() {}
	session.onNavigate = func() { escapeDuringNavigation = escape }

	selectEntry := func(entryID string) {
		t.Helper()
		wiring.ShowTreeSelector(context.Background(), "", false)
		tree, ok := wiring.Slot.ActiveSelectorComponent().(*TreeSelectorComponent)
		if !ok {
			t.Fatalf("selector type = %T", wiring.Slot.ActiveSelectorComponent())
		}
		tree.GetTreeList().OnSelect(entryID)
	}

	// A completed summary: indicator shown and cleared, spacer, escape restored.
	session.navigateResult = &coding.NavigateTreeResult{}
	selectEntry("summarized-entry")
	if len(shown) != 1 || shown[0] != StatusBranchSummary {
		t.Errorf("indicators shown = %v, want one branch-summary indicator", shown)
	}
	if len(cleared) != 1 || cleared[0] != StatusBranchSummary {
		t.Errorf("indicators cleared = %v", cleared)
	}
	if spacers != 1 {
		t.Errorf("chat spacers = %d, want 1", spacers)
	}
	if !sameFunc(escape, original) {
		t.Errorf("the escape handler was not restored")
	}

	// Escape aborts the summary: the handler installed during navigation must be
	// the abort (and not the previous one).
	before := session.abortSummary
	escapeDuringNavigation()
	if session.abortSummary != before+1 {
		t.Errorf("escape during the summary did not abort it: %d -> %d", before, session.abortSummary)
	}

	// Cancelled and aborted summaries clear the indicator and restore escape too.
	for _, result := range []*coding.NavigateTreeResult{{Cancelled: true}, {Aborted: true}} {
		shown, cleared = nil, nil
		session.navigateResult = result
		selectEntry("cancelled-entry")
		if len(cleared) != 1 || cleared[0] != StatusBranchSummary {
			t.Errorf("result %+v: cleared = %v", *result, cleared)
		}
		if !sameFunc(escape, original) {
			t.Errorf("result %+v: escape handler not restored", *result)
		}
	}

	// An error from the session is reported and still tears the summary UI down.
	shown, cleared = nil, nil
	session.navigateResult = nil
	session.navigateErr = errors.New("navigate failed")
	selectEntry("failing-entry")
	if len(errorMessages) != 1 || errorMessages[0] != "navigate failed" {
		t.Errorf("errors = %v", errorMessages)
	}
	if len(cleared) != 1 || cleared[0] != StatusBranchSummary {
		t.Errorf("after an error: cleared = %v", cleared)
	}
	if !sameFunc(escape, original) {
		t.Errorf("after an error: escape handler not restored")
	}
}

// sameFunc reports whether two function values are the same function.
func sameFunc(a, b func()) bool {
	return reflect.ValueOf(a).Pointer() == reflect.ValueOf(b).Pointer()
}

// Port of test/interactive-mode-tree-navigation.test.ts (upstream regression
// #9178 / PR #9179): the availability checks run after the summary dialogs and
// after an active response is aborted, and a rejection must not put the
// navigation's own UI on screen.
func TestTreeNavigationAvailability(t *testing.T) {
	const busy = "Wait for the current compaction or tree navigation to finish before navigating the session tree."

	newWiring := func(t *testing.T) (*SelectorWiring, *selectorTestSession, *int) {
		t.Helper()
		wiring, session, _, _ := newSelectorTestWiring(t)
		wiring.TerminalRows = func() int { return 30 }
		manager := coding.NewSessionManager("/tmp/proj", &coding.SessionManagerOptions{Persist: boolPtr(false)})
		wiring.SessionInfo = manager
		manager.AppendMessage(ai.Message(&ai.UserMessage{Content: ai.StringOrBlocks{Text: "hello"}}))
		restores := 0
		wiring.RestoreQueuedMessagesToEditor = func() { restores++ }
		return wiring, session, &restores
	}
	selectEntry := func(t *testing.T, wiring *SelectorWiring, entryID string) {
		t.Helper()
		wiring.ShowTreeSelector(context.Background(), "", false)
		tree, ok := wiring.Slot.ActiveSelectorComponent().(*TreeSelectorComponent)
		if !ok {
			t.Fatalf("selector type = %T", wiring.Slot.ActiveSelectorComponent())
		}
		tree.GetTreeList().OnSelect(entryID)
	}

	for _, choice := range []string{"Summarize", "No summary"} {
		t.Run("preserves operation UI when choosing "+choice+" while busy", func(t *testing.T) {
			wiring, session, restores := newWiring(t)
			errorMessages := []string{}
			shown, cleared := []StatusIndicatorKind{}, []StatusIndicatorKind{}
			wiring.ShowError = func(message string) { errorMessages = append(errorMessages, message) }
			wiring.ShowStatusIndicator = func(kind StatusIndicatorKind) { shown = append(shown, kind) }
			wiring.ClearStatusIndicator = func(kind StatusIndicatorKind) { cleared = append(cleared, kind) }
			original := func() {}
			escape := original
			wiring.EditorEscapeHandler = func() func() { return escape }
			wiring.SetEditorEscapeHandler = func(handler func()) { escape = handler }
			leafBefore := *wiring.SessionInfo.GetLeafID()

			// A compaction can start while the dialog is open.
			wiring.ShowExtensionSelector = func(context.Context, string, []string) (string, bool) {
				session.compacting = true
				return choice, true
			}
			selectEntry(t, wiring, "target-entry")

			if len(errorMessages) != 1 || errorMessages[0] != busy {
				t.Errorf("errors = %v, want the busy message", errorMessages)
			}
			if len(shown) != 0 || len(cleared) != 0 {
				t.Errorf("indicators shown/cleared = %v/%v, want none", shown, cleared)
			}
			if !sameFunc(escape, original) {
				t.Errorf("the escape handler was replaced")
			}
			if len(session.navigateCalls) != 0 {
				t.Errorf("navigation ran while busy: %v", session.navigateCalls)
			}
			if session.abortCalls != 0 || *restores != 0 {
				t.Errorf("abort/restore ran while not streaming: %d/%d", session.abortCalls, *restores)
			}
			if leaf := *wiring.SessionInfo.GetLeafID(); leaf != leafBefore {
				t.Errorf("the leaf moved: %s -> %s", leafBefore, leaf)
			}
		})
	}

	t.Run("allows navigation when compaction finishes while the dialog is open", func(t *testing.T) {
		wiring, session, _ := newWiring(t)
		errorMessages := []string{}
		wiring.ShowError = func(message string) { errorMessages = append(errorMessages, message) }
		wiring.ShowStatusIndicator = func(StatusIndicatorKind) {}
		wiring.ClearStatusIndicator = func(StatusIndicatorKind) {}
		wiring.EditorEscapeHandler = func() func() { return func() {} }
		wiring.SetEditorEscapeHandler = func(func()) {}

		session.compacting = true
		wiring.ShowExtensionSelector = func(context.Context, string, []string) (string, bool) {
			session.compacting = false
			return "No summary", true
		}
		selectEntry(t, wiring, "target-entry")

		if len(errorMessages) != 0 {
			t.Errorf("errors = %v", errorMessages)
		}
		if len(session.navigateCalls) != 1 || session.navigateCalls[0] != "target-entry" {
			t.Fatalf("navigate calls = %v", session.navigateCalls)
		}
		if len(session.navigateOptions) != 1 || session.navigateOptions[0].Summarize {
			t.Errorf("navigate options = %+v, want no summarization", session.navigateOptions)
		}
	})

	t.Run("still aborts an active response before navigating", func(t *testing.T) {
		wiring, session, restores := newWiring(t)
		errorMessages := []string{}
		wiring.ShowError = func(message string) { errorMessages = append(errorMessages, message) }
		wiring.ShowExtensionSelector = func(context.Context, string, []string) (string, bool) {
			return "No summary", true
		}
		session.streaming = true
		session.onAbort = func() { session.streaming = false }
		abortSaw := false
		session.onNavigate = func() {
			abortSaw = session.streaming == false && session.abortCalls == 1 && *restores == 1
		}
		selectEntry(t, wiring, "target-entry")

		if !abortSaw {
			t.Errorf("the response was not aborted before navigating (aborts=%d restores=%d streaming=%v)",
				session.abortCalls, *restores, session.streaming)
		}
		if len(session.navigateCalls) != 1 {
			t.Errorf("navigate calls = %v", session.navigateCalls)
		}
		if len(errorMessages) != 0 {
			t.Errorf("errors = %v", errorMessages)
		}
	})

	t.Run("rechecks availability after the response abort settles", func(t *testing.T) {
		wiring, session, _ := newWiring(t)
		errorMessages := []string{}
		shown, cleared := []StatusIndicatorKind{}, []StatusIndicatorKind{}
		wiring.ShowError = func(message string) { errorMessages = append(errorMessages, message) }
		wiring.ShowStatusIndicator = func(kind StatusIndicatorKind) { shown = append(shown, kind) }
		wiring.ClearStatusIndicator = func(kind StatusIndicatorKind) { cleared = append(cleared, kind) }
		original := func() {}
		escape := original
		wiring.EditorEscapeHandler = func() func() { return escape }
		wiring.SetEditorEscapeHandler = func(handler func()) { escape = handler }

		session.streaming = true
		wiring.ShowExtensionSelector = func(context.Context, string, []string) (string, bool) {
			return "Summarize", true
		}
		// The abort settles into a compaction: the navigation must not proceed.
		session.onAbort = func() {
			session.streaming = false
			session.compacting = true
		}
		selectEntry(t, wiring, "target-entry")

		if len(errorMessages) != 1 || errorMessages[0] != busy {
			t.Errorf("errors = %v, want the busy message", errorMessages)
		}
		if len(shown) != 0 || len(cleared) != 0 {
			t.Errorf("indicators shown/cleared = %v/%v, want none", shown, cleared)
		}
		if !sameFunc(escape, original) {
			t.Errorf("the escape handler was replaced")
		}
		if len(session.navigateCalls) != 0 {
			t.Errorf("navigation ran after aborting into a compaction: %v", session.navigateCalls)
		}
	})
}
