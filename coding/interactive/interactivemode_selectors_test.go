package interactive

import (
	"context"
	"strings"
	"testing"

	"github.com/dat267/gpi/ai"
	"github.com/dat267/gpi/coding"
	"github.com/dat267/gpi/tui"
)

// selectorTestSession implements SelectorSession.
type selectorTestSession struct {
	thinkingLevel  ai.ThinkingLevel
	levels         []ai.ThinkingLevel
	forkMessages   []coding.UserMessageFork
	abortCalls     int
	abortSummary   int
	compacting     bool
	streaming      bool
	navigateResult *coding.NavigateTreeResult
	navigateErr    error
	navigateCalls  []string
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
func (s *selectorTestSession) NavigateTree(_ context.Context, targetID string, _ coding.NavigateTreeOptions) (*coding.NavigateTreeResult, error) {
	s.navigateCalls = append(s.navigateCalls, targetID)
	return s.navigateResult, s.navigateErr
}
func (s *selectorTestSession) IsStreaming() bool     { return s.streaming }
func (s *selectorTestSession) IsCompacting() bool    { return s.compacting }
func (s *selectorTestSession) Abort(context.Context) { s.abortCalls++ }
func (s *selectorTestSession) AbortBranchSummary()   { s.abortSummary++ }

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
