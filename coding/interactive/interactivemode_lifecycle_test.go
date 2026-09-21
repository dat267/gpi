package interactive

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/dat267/gpi/coding"
	"github.com/dat267/gpi/tui"
)

// lifecycleTestSession implements LifecycleSession.
type lifecycleTestSession struct {
	streaming  bool
	compacting bool
	prompts    []string
}

func (s *lifecycleTestSession) IsStreaming() bool  { return s.streaming }
func (s *lifecycleTestSession) IsCompacting() bool { return s.compacting }
func (s *lifecycleTestSession) Prompt(_ context.Context, text string, _ *coding.PromptOptions) error {
	s.prompts = append(s.prompts, text)
	return nil
}

// titleTerminal records SetTitle calls.
type titleTerminal struct {
	fakeRendererTerminal
	titles []string
}

func (t *titleTerminal) SetTitle(title string) { t.titles = append(t.titles, title) }

func newLifecycleTest(t *testing.T) (*Lifecycle, *lifecycleTestSession, *[]int, *titleTerminal) {
	t.Helper()
	SetCustomThemesDir(t.TempDir())
	SetRegisteredThemes(nil)
	SetTrueColorSupport(true)
	SetStyleColorsEnabled(true)
	InitTheme("dark", false)

	terminal := &titleTerminal{fakeRendererTerminal: fakeRendererTerminal{width: 80, height: 24}}
	screen := tui.NewMainScreen(terminal, false, "")
	screen.DisableAutoRender()
	session := &lifecycleTestSession{}
	settings := coding.NewInMemorySettingsManager(nil, coding.SettingsManagerCreateOptions{})
	exits := &[]int{}
	now := time.Unix(1000, 0)
	lifecycle := NewLifecycle(LifecycleOptions{
		UI: screen, Session: session, Settings: settings, Terminal: terminal,
		TuiMode: "regular", AppTitle: "pi", Platform: "linux",
		Now:  func() time.Time { return now },
		Exit: func(code int) { *exits = append(*exits, code) },
	})
	return lifecycle, session, exits, terminal
}

// TestLifecycleSwitchTuiMode covers the renderer swap.
func TestLifecycleSwitchTuiMode(t *testing.T) {
	lifecycle, _, _, terminal := newLifecycleTest(t)
	screen := lifecycle.options.UI

	// Same mode: no-op success.
	if !lifecycle.SwitchTuiMode("regular", true, true) {
		t.Fatal("same mode should succeed")
	}

	// An overlay blocks the switch.
	screen.ShowOverlay(&staticRendererComponent{lines: []string{"overlay"}}, nil)
	if lifecycle.SwitchTuiMode("fullscreen", true, true) {
		t.Fatal("overlay should block the switch")
	}
	screen.HideOverlay()

	// Switching to fullscreen swaps the renderer and remounts the children.
	child := &staticRendererComponent{lines: []string{"child"}}
	screen.AddChild(child)
	if !lifecycle.SwitchTuiMode("fullscreen", true, true) {
		t.Fatal("switch failed")
	}
	next := lifecycle.options.UI
	if _, ok := next.(*tui.AltScreen); !ok {
		t.Fatalf("renderer = %T", next)
	}
	if len(next.GetMountedRoots()) != 1 {
		t.Fatalf("children = %d", len(next.GetMountedRoots()))
	}
	if next.GetTerminal() != tui.Terminal(terminal) {
		t.Fatal("terminal not preserved")
	}
	// Switching back restores the main screen.
	if !lifecycle.SwitchTuiMode("regular", true, true) {
		t.Fatal("switch back failed")
	}
	if _, ok := lifecycle.options.UI.(*tui.MainScreen); !ok {
		t.Fatalf("renderer = %T", lifecycle.options.UI)
	}
}

// TestLifecycleStopInteractiveTui covers the fullscreen transcript replay.
func TestLifecycleStopInteractiveTui(t *testing.T) {
	lifecycle, _, _, _ := newLifecycleTest(t)
	lifecycle.SwitchTuiMode("fullscreen", false, false)
	lifecycle.StopInteractiveTui("transcript")
	if TuiMode(lifecycle.options.UI) != "regular" {
		t.Fatalf("mode = %q", TuiMode(lifecycle.options.UI))
	}
}

// TestLifecycleShutdown covers the graceful shutdown.
func TestLifecycleShutdown(t *testing.T) {
	lifecycle, _, exits, _ := newLifecycleTest(t)
	writes := []string{}
	lifecycle.options.WriteOut = func(text string) { writes = append(writes, text) }
	lifecycle.options.ResumeCommand = func() string { return "pi --session abc" }
	lifecycle.options.FormatResumeMessage = func(command string) string { return "To resume this session: " + command }
	disposed := 0
	lifecycle.options.DisposeRuntime = func() { disposed++ }
	synced := 0
	lifecycle.options.DisableThemeAutoSync = func() { synced++ }

	lifecycle.Shutdown(false)
	if len(*exits) != 1 || (*exits)[0] != 0 {
		t.Fatalf("exits = %v", *exits)
	}
	if len(writes) != 1 || !strings.Contains(writes[0], "To resume this session: pi --session abc") {
		t.Fatalf("writes = %v", writes)
	}
	if disposed != 1 || synced != 1 {
		t.Fatalf("disposed = %d, synced = %d", disposed, synced)
	}
	if !lifecycle.IsShuttingDown() {
		t.Fatal("not marked as shutting down")
	}

	// A second shutdown is a no-op.
	lifecycle.Shutdown(false)
	if len(*exits) != 1 {
		t.Fatalf("exits = %v", *exits)
	}
}

// TestLifecycleShutdownFromSignal covers the signal shutdown order.
func TestLifecycleShutdownFromSignal(t *testing.T) {
	lifecycle, _, exits, _ := newLifecycleTest(t)
	order := []string{}
	lifecycle.options.DisposeRuntime = func() { order = append(order, "dispose") }
	lifecycle.options.DisableThemeAutoSync = func() { order = append(order, "sync") }
	lifecycle.options.WriteOut = func(string) { order = append(order, "write") }
	lifecycle.options.ResumeCommand = func() string { return "cmd" }

	lifecycle.Shutdown(true)
	if len(*exits) != 1 || (*exits)[0] != 0 {
		t.Fatalf("exits = %v", *exits)
	}
	// The signal path disposes before touching the terminal and skips the
	// resume message.
	if len(order) != 2 || order[0] != "dispose" || order[1] != "sync" {
		t.Fatalf("order = %v", order)
	}
}

// TestLifecycleCtrlC covers the double-press shutdown.
func TestLifecycleCtrlC(t *testing.T) {
	lifecycle, _, exits, _ := newLifecycleTest(t)
	cleared := 0
	now := time.Unix(2000, 0)
	lifecycle.now = func() time.Time { return now }

	lifecycle.HandleCtrlC(func() { cleared++ })
	if cleared != 1 || len(*exits) != 0 {
		t.Fatalf("cleared = %d, exits = %v", cleared, *exits)
	}
	// Within 500ms: shutdown.
	now = now.Add(100 * time.Millisecond)
	lifecycle.HandleCtrlC(func() { cleared++ })
	if len(*exits) != 1 || cleared != 1 {
		t.Fatalf("cleared = %d, exits = %v", cleared, *exits)
	}

	// A slow second press only clears again.
	lifecycle2, _, exits2, _ := newLifecycleTest(t)
	clock := time.Unix(3000, 0)
	lifecycle2.now = func() time.Time { return clock }
	lifecycle2.HandleCtrlC(nil)
	clock = clock.Add(time.Second)
	lifecycle2.HandleCtrlC(nil)
	if len(*exits2) != 0 {
		t.Fatalf("exits = %v", *exits2)
	}
}

// TestLifecycleCtrlDAndRequest covers the other shutdown triggers.
func TestLifecycleCtrlDAndRequest(t *testing.T) {
	lifecycle, _, exits, _ := newLifecycleTest(t)
	lifecycle.HandleCtrlD()
	if len(*exits) != 1 {
		t.Fatalf("exits = %v", *exits)
	}

	lifecycle2, _, exits2, _ := newLifecycleTest(t)
	lifecycle2.CheckShutdownRequested()
	if len(*exits2) != 0 {
		t.Fatal("shutdown without a request")
	}
	lifecycle2.RequestShutdown()
	lifecycle2.CheckShutdownRequested()
	if len(*exits2) != 1 {
		t.Fatalf("exits = %v", *exits2)
	}
}

// TestLifecycleCrashPaths covers the emergency and uncaught paths.
func TestLifecycleCrashPaths(t *testing.T) {
	lifecycle, _, exits, _ := newLifecycleTest(t)
	killed := 0
	lifecycle.options.KillDetachedChildren = func() { killed++ }
	lifecycle.EmergencyTerminalExit()
	if len(*exits) != 1 || (*exits)[0] != 129 || killed != 1 {
		t.Fatalf("exits = %v, killed = %d", *exits, killed)
	}

	crash, _, exits2, _ := newLifecycleTest(t)
	errText := []string{}
	crash.options.WriteErr = func(text string) { errText = append(errText, text) }
	crash.options.RecordCrash = func(kind string, err error) bool {
		return kind == "uncaught_exception" && err.Error() == "boom"
	}
	crash.options.CrashReportInstructions = func() string { return "Run /bug" }
	crash.UncaughtCrash(errorsNew("boom"))
	if len(*exits2) != 1 || (*exits2)[0] != 1 {
		t.Fatalf("exits = %v", *exits2)
	}
	joined := strings.Join(errText, "")
	if !strings.Contains(joined, "exiting due to uncaughtException") || !strings.Contains(joined, "Run /bug") {
		t.Fatalf("stderr = %q", joined)
	}

	// A crash while already shutting down exits without reporting.
	again, _, exits3, _ := newLifecycleTest(t)
	again.shuttingDown = true
	again.UncaughtCrash(errorsNew("late"))
	if len(*exits3) != 1 || (*exits3)[0] != 1 {
		t.Fatalf("exits = %v", *exits3)
	}
}

// TestLifecycleSignalHandlers covers the handler registry.
func TestLifecycleSignalHandlers(t *testing.T) {
	lifecycle, _, exits, _ := newLifecycleTest(t)

	// The real registration installs SIGTERM and SIGHUP on Linux.
	lifecycle.RegisterSignalHandlers()
	if got := lifecycle.SignalHandlerCount(); got != 2 {
		t.Fatalf("handlers = %d", got)
	}
	lifecycle.UnregisterSignalHandlers()
	if got := lifecycle.SignalHandlerCount(); got != 0 {
		t.Fatalf("handlers after unregister = %d", got)
	}

	// The process-error and uncaught-exception seams are registered too.
	terminalErrors := []func(error){}
	uncaught := []func(error){}
	lifecycle.options.RegisterSignal = func(os.Signal, func()) func() { return func() {} }
	lifecycle.options.OnTerminalError = func(handler func(error)) func() {
		terminalErrors = append(terminalErrors, handler)
		return func() {}
	}
	lifecycle.options.OnUncaughtException = func(handler func(error)) func() {
		uncaught = append(uncaught, handler)
		return func() {}
	}
	lifecycle.RegisterSignalHandlers()
	if len(terminalErrors) != 1 || len(uncaught) != 1 {
		t.Fatalf("terminal errors = %d, uncaught = %d", len(terminalErrors), len(uncaught))
	}
	// A dead-terminal error exits 129.
	func() {
		defer func() { _ = recover() }()
		terminalErrors[0](&lifecycleTestError{message: "EIO"})
	}()
	if len(*exits) != 1 || (*exits)[0] != 129 {
		t.Fatalf("exits = %v", *exits)
	}

	// The uncaught handler reports the crash and exits 1.
	crash, _, crashExits, _ := newLifecycleTest(t)
	var uncaughtHandler func(error)
	crash.options.RegisterSignal = func(os.Signal, func()) func() { return func() {} }
	crash.options.OnUncaughtException = func(handler func(error)) func() {
		uncaughtHandler = handler
		return func() {}
	}
	crash.options.RecordCrash = func(string, error) bool { return false }
	crash.RegisterSignalHandlers()
	uncaughtHandler(errorsNew("boom"))
	if len(*crashExits) != 1 || (*crashExits)[0] != 1 {
		t.Fatalf("exits = %v", *crashExits)
	}
}

// TestLifecycleTerminalTitle covers the title updates.
func TestLifecycleTerminalTitle(t *testing.T) {
	lifecycle, _, _, terminal := newLifecycleTest(t)
	lifecycle.options.SessionCwd = func() string { return "/tmp/proj" }
	lifecycle.options.SessionName = func() string { return "" }
	lifecycle.UpdateTerminalTitle()
	if len(terminal.titles) != 1 || terminal.titles[0] != "pi - proj" {
		t.Fatalf("titles = %v", terminal.titles)
	}
	lifecycle.options.SessionName = func() string { return "My Session" }
	lifecycle.UpdateTerminalTitle()
	if terminal.titles[1] != "pi - My Session - proj" {
		t.Fatalf("titles = %v", terminal.titles)
	}
}

// TestLifecycleCtrlZ covers the suspend paths.
func TestLifecycleCtrlZ(t *testing.T) {
	lifecycle, _, _, _ := newLifecycleTest(t)
	statuses := []string{}
	suspended := 0
	lifecycle.options.Platform = "win32"
	lifecycle.HandleCtrlZ(func(message string) { statuses = append(statuses, message) }, func() { suspended++ })
	if len(statuses) != 1 || suspended != 0 {
		t.Fatalf("statuses = %v, suspended = %d", statuses, suspended)
	}
	lifecycle.options.Platform = "linux"
	lifecycle.HandleCtrlZ(nil, func() { suspended++ })
	if suspended != 1 {
		t.Fatalf("suspended = %d", suspended)
	}
}

func errorsNew(message string) error { return &lifecycleTestError{message: message} }

type lifecycleTestError struct{ message string }

func (e *lifecycleTestError) Error() string { return e.message }

// TestLifecycleShutdownStopMode covers the fullscreenExitOutput setting on
// shutdown (upstream's stop() reads settingsManager.getFullscreenExitOutput()
// and stopInteractiveTui consumes it).
func TestLifecycleShutdownStopMode(t *testing.T) {
	lifecycle, _, exits, _ := newLifecycleTest(t)
	outputs := []string{}
	lifecycle.options.StopMode = func(output string) { outputs = append(outputs, output) }
	lifecycle.options.FullscreenExitOutput = func() string { return "resume-hint" }
	lifecycle.Shutdown(false)
	if len(outputs) != 1 || outputs[0] != "resume-hint" {
		t.Fatalf("stopMode outputs = %v", outputs)
	}
	if len(*exits) != 1 || (*exits)[0] != 0 {
		t.Fatalf("exits = %v", *exits)
	}

	// Without a setting reader the default is "transcript".
	lifecycle2, _, _, _ := newLifecycleTest(t)
	got := ""
	lifecycle2.options.StopMode = func(output string) { got = output }
	lifecycle2.Shutdown(false)
	if got != "transcript" {
		t.Fatalf("default output = %q", got)
	}

	// Without a StopMode wiring the shutdown still exits (plain renderer stop).
	lifecycle3, _, exits3, _ := newLifecycleTest(t)
	lifecycle3.Shutdown(false)
	if len(*exits3) != 1 || (*exits3)[0] != 0 {
		t.Fatalf("exits = %v", *exits3)
	}
}
