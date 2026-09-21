package interactive

import (
	"context"
	"strings"
	"testing"

	"github.com/dat267/pier/ai"
	"github.com/dat267/pier/coding"
	"github.com/dat267/pier/tui"
)

// handlerTestSession implements KeySession and SubmitSession.
type handlerTestSession struct {
	streaming          bool
	bashRunning        bool
	compacting         bool
	bashAborts         int
	abortCalls         int
	steering           []string
	followUp           []string
	prompts            []string
	streamingBehaviors []string
	cleared            int
	thinking           ai.ThinkingLevel
}

func (s *handlerTestSession) GetSteeringMessages() []string { return append([]string{}, s.steering...) }
func (s *handlerTestSession) GetFollowUpMessages() []string { return append([]string{}, s.followUp...) }
func (s *handlerTestSession) ClearQueue() ([]string, []string) {
	s.cleared++
	steering, followUp := s.steering, s.followUp
	s.steering, s.followUp = nil, nil
	return steering, followUp
}
func (s *handlerTestSession) Steer(ai.Message)      {}
func (s *handlerTestSession) FollowUp(ai.Message)   {}
func (s *handlerTestSession) Abort(context.Context) { s.abortCalls++ }
func (s *handlerTestSession) CycleThinkingLevel(coding.ModelMutationOptions) (ai.ThinkingLevel, bool) {
	return s.thinking, s.thinking != ""
}
func (s *handlerTestSession) CycleModel(context.Context, string, coding.ModelMutationOptions) (*coding.ModelCycleResult, error) {
	return nil, nil
}
func (s *handlerTestSession) SupportsThinking() bool          { return true }
func (s *handlerTestSession) ThinkingLevel() ai.ThinkingLevel { return s.thinking }

func (s *handlerTestSession) IsStreaming() bool   { return s.streaming }
func (s *handlerTestSession) IsBashRunning() bool { return s.bashRunning }
func (s *handlerTestSession) IsCompacting() bool  { return s.compacting }
func (s *handlerTestSession) AbortBash()          { s.bashAborts++ }
func (s *handlerTestSession) Prompt(_ context.Context, text string, options *coding.PromptOptions) error {
	s.prompts = append(s.prompts, text)
	behavior := ""
	if options != nil {
		behavior = options.StreamingBehavior
	}
	s.streamingBehaviors = append(s.streamingBehaviors, behavior)
	return nil
}

func newHandlerTestWiring(t *testing.T) (*KeyWiring, *SubmitWiring, *handlerTestSession, *CustomEditor) {
	t.Helper()
	SetCustomThemesDir(t.TempDir())
	SetRegisteredThemes(nil)
	SetTrueColorSupport(true)
	SetStyleColorsEnabled(true)
	InitTheme("dark", false)

	settings := coding.NewInMemorySettingsManager(nil, coding.SettingsManagerCreateOptions{})
	editor := NewCustomEditor(editorTestHost{}, tui.EditorTheme{}, NewAppKeybindingsManager(nil, ""), CustomEditorOptions{})
	session := &handlerTestSession{}
	queue := NewQueueController(nil, session, settings, editor, &tui.Container{}, &tui.Container{})

	keys := &KeyWiring{Session: session, Editor: editor, Settings: settings, Queue: queue}
	submit := &SubmitWiring{Editor: editor, Session: session, Settings: settings, Queue: queue}
	return keys, submit, session, editor
}

// TestKeyWiringEscape covers the escape handler branches.
func TestKeyWiringEscape(t *testing.T) {
	keys, _, session, editor := newHandlerTestWiring(t)
	now := int64(1000)
	keys.SetupKeyHandlers(func() int64 { return now })

	// Streaming: restores the queue and aborts.
	session.streaming = true
	session.steering = []string{"queued"}
	editor.OnEscape()
	if session.abortCalls != 1 {
		t.Fatalf("abort calls = %d", session.abortCalls)
	}
	if editor.GetText() != "queued" {
		t.Fatalf("editor = %q", editor.GetText())
	}
	session.streaming = false

	// Bash running: aborts bash.
	session.bashRunning = true
	editor.OnEscape()
	if session.bashAborts != 1 {
		t.Fatalf("bash aborts = %d", session.bashAborts)
	}
	session.bashRunning = false

	// Bash mode: clears the editor and exits the mode.
	keys.Queue.SetBashMode(true)
	editor.SetText("!ls")
	editor.OnEscape()
	if editor.GetText() != "" || keys.Queue.IsBashMode() {
		t.Fatalf("bash mode not cleared: %q", editor.GetText())
	}

	// Double escape with an empty editor triggers the configured action.
	treeShown := 0
	keys.ShowTreeSelector = func() { treeShown++ }
	editor.SetText("")
	editor.OnEscape()
	if treeShown != 0 {
		t.Fatal("tree shown on the first escape")
	}
	now += 100
	editor.OnEscape()
	if treeShown != 1 {
		t.Fatalf("tree shown = %d", treeShown)
	}
	// The timestamp resets, so a third escape starts over.
	editor.OnEscape()
	if treeShown != 1 {
		t.Fatalf("tree shown = %d", treeShown)
	}

	// The fork action uses the user-message selector.
	keys.Settings.SetDoubleEscapeAction("fork")
	forkShown := 0
	keys.ShowUserMessageSelector = func() { forkShown++ }
	editor.SetText("")
	editor.OnEscape()
	now += 100
	editor.OnEscape()
	if forkShown != 1 {
		t.Fatalf("fork shown = %d", forkShown)
	}

	// "none" disables the action.
	keys.Settings.SetDoubleEscapeAction("none")
	editor.SetText("")
	editor.OnEscape()
	now += 100
	editor.OnEscape()
	if treeShown+forkShown != 2 {
		t.Fatal("action ran with none")
	}
}

// TestKeyWiringActions covers the app action registration.
func TestKeyWiringActions(t *testing.T) {
	keys, _, _, editor := newHandlerTestWiring(t)
	actions := map[string]int{}
	keys.OnClear = func() { actions["clear"]++ }
	keys.OnExit = func() { actions["exit"]++ }
	keys.OnSuspend = func() { actions["suspend"]++ }
	keys.OnThinkingCycle = func() { actions["thinking"]++ }
	keys.OnModelCycleForward = func() { actions["forward"]++ }
	keys.OnToolsExpand = func() { actions["tools"]++ }
	keys.SetupKeyHandlers(nil)

	// The ctrl+c binding maps to app.clear.
	editor.HandleInput("\x03")
	if actions["clear"] != 1 {
		t.Fatalf("actions = %v", actions)
	}
	// ctrl+d maps to app.exit via OnCtrlD.
	editor.HandleInput("\x04")
	if actions["exit"] != 1 {
		t.Fatalf("actions = %v", actions)
	}
	// ctrl+z suspends.
	editor.HandleInput("\x1a")
	if actions["suspend"] != 1 {
		t.Fatalf("actions = %v", actions)
	}
	// ctrl+o toggles tools.
	editor.HandleInput("\x0f")
	if actions["tools"] != 1 {
		t.Fatalf("actions = %v", actions)
	}
	// ctrl+t toggles thinking.
	keys.OnThinkingToggle = func() { actions["thinkingToggle"]++ }
	keys.SetupKeyHandlers(nil)
	editor.HandleInput("\x14")
	if actions["thinkingToggle"] != 1 {
		t.Fatalf("actions = %v", actions)
	}
	// The onChange handler toggles bash mode.
	editor.SetText("!ls")
	editor.HandleInput("x")
	if !keys.Queue.IsBashMode() {
		t.Fatal("bash mode not detected from the editor text")
	}
}

// TestSubmitCommandDispatch covers the slash-command chain.
func TestSubmitCommandDispatch(t *testing.T) {
	_, submit, _, editor := newHandlerTestWiring(t)
	calls := []string{}
	submit.Handlers.ShowSettingsSelector = func() { calls = append(calls, "settings") }
	submit.Handlers.HandleModelCommand = func(search string) error { calls = append(calls, "model:"+search); return nil }
	submit.Handlers.HandleThinkingCommand = func(search string) { calls = append(calls, "thinking:"+search) }
	submit.Handlers.HandleBugCommand = func(hint string) error { calls = append(calls, "bug:"+hint); return nil }
	submit.Handlers.HandleNameCommand = func(text string) { calls = append(calls, "name") }
	submit.Handlers.ShowTreeSelector = func() { calls = append(calls, "tree") }
	submit.Handlers.HandleLoginCommand = func(providerRef string) error { calls = append(calls, "login:"+providerRef); return nil }
	submit.Handlers.HandleCompactCommand = func(instructions string) error { calls = append(calls, "compact:"+instructions); return nil }
	submit.Handlers.Shutdown = func() error { calls = append(calls, "quit"); return nil }

	inputs := []string{
		"/settings", "/model", "/model gpt", "/thinking high", "/bug", "/bug please",
		"/name", "/tree", "/login", "/login anthropic", "/compact", "/compact now", "/quit",
	}
	for _, input := range inputs {
		editor.SetText(input)
		submit.HandleSubmit(context.Background(), input)
	}
	want := []string{
		"settings", "model:", "model:gpt", "thinking:high", "bug:", "bug:please",
		"name", "tree", "login:", "login:anthropic", "compact:", "compact:now", "quit",
	}
	if strings.Join(calls, ",") != strings.Join(want, ",") {
		t.Fatalf("calls = %v", calls)
	}
	// The editor is cleared for every command.
	if editor.GetText() != "" {
		t.Fatalf("editor = %q", editor.GetText())
	}
}

// TestSubmitBashAndQueues covers the bash/compaction/streaming branches.
func TestSubmitBashAndQueues(t *testing.T) {
	_, submit, session, editor := newHandlerTestWiring(t)
	bashCalls := []string{}
	submit.Handlers.HandleBashCommand = func(command string, exclude bool) error {
		suffix := ""
		if exclude {
			suffix = "!"
		}
		bashCalls = append(bashCalls, command+suffix)
		return nil
	}
	submit.ShowWarning = func(message string) {}
	submit.RequestRender = func() {}

	// Bash commands (normal and excluded).
	submit.HandleSubmit(context.Background(), "!ls -la")
	submit.HandleSubmit(context.Background(), "!!rm -rf /")
	if len(bashCalls) != 2 || bashCalls[0] != "ls -la" || bashCalls[1] != "rm -rf /!" {
		t.Fatalf("bash calls = %v", bashCalls)
	}

	// A running bash command warns and restores the text.
	session.bashRunning = true
	editor.SetText("")
	submit.HandleSubmit(context.Background(), "!pwd")
	if editor.GetText() != "!pwd" || len(bashCalls) != 2 {
		t.Fatalf("editor = %q, bash calls = %v", editor.GetText(), bashCalls)
	}
	session.bashRunning = false

	// Compaction queues the prompt without steering.
	session.compacting = true
	submit.HandleSubmit(context.Background(), "during compaction")
	if len(session.prompts) != 1 || session.prompts[0] != "during compaction" || session.streamingBehaviors[0] != "" {
		t.Fatalf("prompts = %v, behaviors = %v", session.prompts, session.streamingBehaviors)
	}
	session.compacting = false

	// Streaming steers.
	session.streaming = true
	submit.HandleSubmit(context.Background(), "steer me")
	if session.streamingBehaviors[1] != "steer" {
		t.Fatalf("behaviors = %v", session.streamingBehaviors)
	}
	session.streaming = false

	// A normal submission calls OnInput and records history.
	var received []string
	submit.OnInput = func(text string) { received = append(received, text) }
	submit.HandleSubmit(context.Background(), "normal message")
	if len(received) != 1 || received[0] != "normal message" {
		t.Fatalf("received = %v", received)
	}
	editor.HandleInput("\x1b[A")
	if editor.GetText() != "normal message" {
		t.Fatalf("history = %q", editor.GetText())
	}

	// Without OnInput the text is queued.
	pending := []string{}
	submit.OnInput = nil
	submit.PendingUserInputs = &pending
	submit.HandleSubmit(context.Background(), "queued input")
	if len(pending) != 1 || pending[0] != "queued input" {
		t.Fatalf("pending = %v", pending)
	}
}

// TestSubmitStartup covers the startup submit.
func TestSubmitStartup(t *testing.T) {
	_, submit, _, editor := newHandlerTestWiring(t)
	statuses := []string{}
	submit.ShowStatus = func(message string) { statuses = append(statuses, message) }
	submit.HandleStartupSubmit("early text")
	if editor.GetText() != "early text" {
		t.Fatalf("editor = %q", editor.GetText())
	}
	if len(statuses) != 1 || statuses[0] != "Startup is still in progress" {
		t.Fatalf("statuses = %v", statuses)
	}
}

// TestSubmitEmpty covers the empty submission.
func TestSubmitEmpty(t *testing.T) {
	_, submit, session, _ := newHandlerTestWiring(t)
	submit.HandleSubmit(context.Background(), "   ")
	if len(session.prompts) != 0 {
		t.Fatal("empty submission prompted")
	}
}
