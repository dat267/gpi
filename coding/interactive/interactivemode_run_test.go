package interactive

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dat267/pier/coding"
	"github.com/dat267/pier/tui"
)

func newRunTestWiring(t *testing.T) (*RunWiring, *coding.SettingsManager) {
	t.Helper()
	SetCustomThemesDir(t.TempDir())
	SetRegisteredThemes(nil)
	SetTrueColorSupport(true)
	SetStyleColorsEnabled(true)
	InitTheme("dark", false)

	appKeybindings := NewAppKeybindingsManager(nil, "")
	previous := tui.GetKeybindings()
	tui.SetKeybindings(appKeybindings.KeybindingsManager)
	t.Cleanup(func() { tui.SetKeybindings(previous) })

	screen := tui.NewMainScreen(&fakeRendererTerminal{width: 80, height: 24}, false, "")
	screen.DisableAutoRender()
	settings := coding.NewInMemorySettingsManager(nil, coding.SettingsManagerCreateOptions{})
	startup := &StartupWiring{Settings: settings, Version: "1.0.0"}
	wiring := &RunWiring{
		Startup:         startup,
		UI:              screen,
		Settings:        settings,
		AppName:         "pi",
		Version:         "1.0.0",
		Chat:            &tui.Container{},
		HeaderContainer: &tui.Container{},
		Display:         &DisplayOptions{OutputPad: 1},
	}
	return wiring, settings
}

// TestRunChatStatuses covers the error/warning chat lines.
func TestRunChatStatuses(t *testing.T) {
	wiring, _ := newRunTestWiring(t)
	wiring.ShowChatError("boom")
	wiring.ShowChatWarning("careful")
	lines := strings.Join(wiring.Chat.Render(80), "\n")
	if !strings.Contains(lines, "Error: boom") || !strings.Contains(lines, "Warning: careful") {
		t.Fatalf("chat = %q", lines)
	}
	// ClearEditor empties the editor and requests a render.
	renders := 0
	wiring.RequestRender = func() { renders++ }
	value := "text"
	wiring.ClearEditor(func(text string) { value = text })
	if value != "" || renders != 1 {
		t.Fatalf("value = %q renders = %d", value, renders)
	}
}

// TestRunNotifications covers the update cards.
func TestRunNotifications(t *testing.T) {
	wiring, _ := newRunTestWiring(t)
	wiring.ShowNewVersionNotification(LatestRelease{Version: "v1.1.0"}, false)
	lines := strings.Join(wiring.Chat.Render(80), "\n")
	if !strings.Contains(lines, "Update Available") || !strings.Contains(lines, "New version v1.1.0 is available") ||
		!strings.Contains(lines, "https://pi.dev/changelog") {
		t.Fatalf("notification = %q", lines)
	}

	// Hyperlinks wrap the changelog URL.
	linked, _ := newRunTestWiring(t)
	linked.ShowNewVersionNotification(LatestRelease{Version: "1.1.0"}, true)
	if got := strings.Join(linked.Chat.Render(80), "\n"); !strings.Contains(got, "\x1b]8;;https://pi.dev/changelog") {
		t.Fatalf("hyperlink missing: %q", got)
	}

	// The card used to tell the user to run `<app> update`: upstream's command,
	// which ships with its package manager. The port has no package manager and
	// no update command (D41), so the card names the way this module is actually
	// installed instead.
	version, _ := newRunTestWiring(t)
	version.ShowNewVersionNotification(LatestRelease{Version: "1.1.0"}, false)
	got := strings.Join(version.Chat.Render(80), "\n")
	if strings.Contains(got, version.AppName+" update") {
		t.Fatalf("version card instructs a command the port does not have: %q", got)
	}
	if !strings.Contains(got, "go install github.com/dat267/pier@latest") {
		t.Fatalf("version card lost its upgrade path: %q", got)
	}
}

// The new-version seam answers "is there a card to show", so a check that found
// nothing — offline, no newer release, or a failed request — must stay silent
// rather than render an empty card.
func TestVersionNotificationOnlyForARelease(t *testing.T) {
	if release, ok := versionNotification(nil); ok || release != nil {
		t.Fatalf("no release produced %+v, %v", release, ok)
	}
	release, ok := versionNotification(&coding.LatestRelease{Version: "v9.9.9"})
	if !ok || release == nil || release.Version != "v9.9.9" {
		t.Fatalf("release = %+v, %v", release, ok)
	}
}

// The startup checks run on their own goroutines, so whatever they render has to
// be built on the UI loop (upstream's checks run on its single-threaded event
// loop). Headless wirings have no loop and render inline.
func TestRunChecksRenderOnTheUILoop(t *testing.T) {
	wiring, _ := newRunTestWiring(t)
	checked := make(chan struct{}, 1)
	wiring.CheckVersion = func(string) (*LatestRelease, bool) {
		select {
		case checked <- struct{}{}:
		default:
		}
		return &LatestRelease{Version: "v9.9.9"}, true
	}
	go wiring.notifyNewVersion(false)
	<-checked
	// Posted, not rendered: the queue is only drained on the loop's next pass.
	rendered := func() string { return strings.Join(wiring.Chat.Render(80), "\n") }
	if strings.Contains(rendered(), "Update Available") {
		t.Fatal("the release card was rendered off the UI loop")
	}
	wiring.UI.RenderNow(true)
	if !strings.Contains(rendered(), "Update Available") {
		t.Fatal("the release card never reached the loop")
	}

	// Headless: no loop to post to, so the work runs where it is.
	headless, _ := newRunTestWiring(t)
	headless.UI = nil
	headless.CheckVersion = func(string) (*LatestRelease, bool) {
		return &LatestRelease{Version: "v9.9.9"}, true
	}
	headless.notifyNewVersion(false)
	if !strings.Contains(strings.Join(headless.Chat.Render(80), "\n"), "Update Available") {
		t.Fatal("the headless wiring must render inline")
	}
}

// TestRunStartupHeader covers the header construction.
func TestRunStartupHeader(t *testing.T) {
	wiring, _ := newRunTestWiring(t)
	compact := wiring.BuildStartupHeader(nil)
	expandable, ok := compact.(*ExpandableText)
	if !ok {
		t.Fatalf("header = %T", compact)
	}
	lines := coding.StripAnsi(strings.Join(expandable.Render(200), "\n"))
	if !strings.Contains(lines, "pi v1.0.0") || !strings.Contains(lines, "Press ctrl+o") ||
		!strings.Contains(lines, "ctrl+c/ctrl+d clear/exit") {
		t.Fatalf("compact header = %q", lines)
	}

	// Verbose expands the header.
	wiring.Verbose = true
	expanded := wiring.BuildStartupHeader(nil).(*ExpandableText)
	lines = coding.StripAnsi(strings.Join(expanded.Render(200), "\n"))
	if !strings.Contains(lines, "to interrupt") || !strings.Contains(lines, "drop files") {
		t.Fatalf("expanded header = %q", lines)
	}
	if !wiring.GetStartupExpansionState() {
		t.Fatal("expansion state")
	}
}

// TestRunInit covers the init orchestration order.
func TestRunInit(t *testing.T) {
	wiring, _ := newRunTestWiring(t)
	order := []string{}
	wiring.SetupKeyHandlers = func() { order = append(order, "keys") }
	wiring.SetupSubmitHandler = func() { order = append(order, "submit") }
	wiring.RebindSession = func(context.Context) error { order = append(order, "rebind"); return nil }
	wiring.RenderInitialMessages = func() { order = append(order, "messages") }
	wiring.LoadHighlightLanguages = func() error { return nil }
	dispatcher := NewEventDispatcher(nil, nil, nil, nil, nil, nil, nil)
	wiring.Events = dispatcher

	wiring.Init(context.Background(), nil, func() { order = append(order, "signals") },
		func() { order = append(order, "mount") }, false)

	if !wiring.initialized {
		t.Fatal("not initialized")
	}
	want := "signals,mount,keys,submit,rebind,messages"
	if strings.Join(order, ",") != want {
		t.Fatalf("order = %v", order)
	}
	if len(wiring.HeaderContainer.Children) != 3 {
		t.Fatalf("header children = %d", len(wiring.HeaderContainer.Children))
	}
	// A second init is a no-op.
	wiring.Init(context.Background(), nil, nil, nil, false)
	if strings.Join(order, ",") != want {
		t.Fatalf("order after second init = %v", order)
	}
	// The dispatcher must be marked initialized during startup (upstream sets
	// isInitialized inside init, before events flow): otherwise the
	// first-event fallback re-runs init mid-session — RenderInitialMessages
	// re-rendered the whole transcript without clearing, duplicating every
	// message after the user's first submission.
	if !dispatcher.Initialized {
		t.Fatal("dispatcher not marked initialized during startup init")
	}
}

// TestRunInitQuiet covers the silenced header.
func TestRunInitQuiet(t *testing.T) {
	wiring, _ := newRunTestWiring(t)
	wiring.Init(context.Background(), nil, nil, nil, true)
	if len(wiring.HeaderContainer.Children) != 1 {
		t.Fatalf("header children = %d", len(wiring.HeaderContainer.Children))
	}
	if lines := wiring.HeaderContainer.Render(80); len(lines) != 0 && strings.TrimSpace(strings.Join(lines, "")) != "" {
		t.Fatalf("quiet header = %v", lines)
	}
}

// TestRunLoop covers the run orchestration and main loop.
func TestRunLoop(t *testing.T) {
	wiring, _ := newRunTestWiring(t)
	// The startup wiring's input queue feeds the loop. Prompts run on the
	// loop's work goroutine (stage 1), so the recorder is test-guarded.
	var promptsMu sync.Mutex
	prompts := []string{}
	recordedPrompts := func() []string {
		promptsMu.Lock()
		defer promptsMu.Unlock()
		return append([]string{}, prompts...)
	}
	wiring.Prompt = func(_ context.Context, text string) error {
		promptsMu.Lock()
		defer promptsMu.Unlock()
		prompts = append(prompts, text)
		return nil
	}
	diagnostics := []StartupDiagnostic{{Type: "error", Message: "diag-error"}, {Type: "warning", Message: "diag-warn"}, {Type: "info", Message: "diag-info"}}
	wiring.TakeCrash = func() *coding.CrashRecord { return &coding.CrashRecord{Timestamp: "2026-01-01", Message: "kaboom"} }
	statuses := []string{}
	wiring.ShowStatus = func(message string) { statuses = append(statuses, message) }

	ctx, cancel := context.WithCancel(context.Background())
	// The submission channel is buffered, so the loop picks this up in order
	// after the seeded initial messages.
	wiring.Startup.QueueUserInput("loop input")
	go func() {
		waitForConditionWithin(t, func() bool { return len(recordedPrompts()) >= 3 }, 5*time.Second)
		cancel()
	}()
	wiring.Run(ctx, InitOptions{QuietStartup: true}, RunOptions{
		Offline:              true,
		StartupDiagnostics:   diagnostics,
		MigratedProviders:    []string{"p"},
		ModelsJSONError:      "bad json",
		ModelFallbackMessage: "fallback",
		InitialMessage:       "initial",
		InitialMessages:      []string{"second"},
	})

	prompts = recordedPrompts()
	if len(prompts) != 3 || prompts[0] != "initial" || prompts[1] != "second" || prompts[2] != "loop input" {
		t.Fatalf("prompts = %v", prompts)
	}
	chat := strings.Join(wiring.Chat.Render(100), "\n")
	for _, expected := range []string{"Error: diag-error", "Warning: diag-warn",
		"Migrated credentials", "models.json error: bad json", "fallback", "crashed on 2026-01-01"} {
		if !strings.Contains(chat, expected) {
			t.Fatalf("chat missing %q: %q", expected, chat)
		}
	}
	// The info diagnostic goes through the status callback.
	foundInfo := false
	for _, status := range statuses {
		if status == "diag-info" {
			foundInfo = true
		}
	}
	if !foundInfo {
		t.Fatalf("statuses = %v", statuses)
	}
	_ = time.Second
}

// animationProbe is a leaf that always requests an animation frame.
type animationProbe struct {
	tui.Component
	frames int
}

func (a *animationProbe) AnimationFrame(time.Time) (bool, time.Duration) {
	a.frames++
	return true, time.Second
}
func (a *animationProbe) Render(int) []string { return nil }
func (a *animationProbe) Invalidate()         {}

// TestArmAnimationFindsARunningToolInTheLayout pins that the loop's animation
// scan reaches a component under the fullscreen layout (VStack -> ScrollView ->
// DocumentContainer -> Chat), so the elapsed timer arms.
func TestArmAnimationFindsARunningToolInTheLayout(t *testing.T) {
	wiring, _ := newRunTestWiring(t)
	probe := &animationProbe{}
	wiring.Chat.AddChild(probe)

	document := &tui.Container{}
	document.AddChild(wiring.Chat)
	transcript := tui.NewScrollView(document, tui.ScrollViewOptions{Follow: "end", Primary: true})
	root := tui.NewVStack(nil, tui.StackOptions{})
	root.AddChild(transcript)

	screen := tui.NewAltScreen(&fakeRendererTerminal{width: 80, height: 24}, false, t.TempDir(), tui.AltScreenOptions{})
	screen.SetLayoutRoot(root)
	wiring.UI = screen

	timer := time.NewTimer(time.Hour)
	timer.Stop()
	var deadline time.Time
	ch := wiring.armAnimation(timer, &deadline)
	if ch == nil || deadline.IsZero() {
		t.Fatalf("animation not armed: ch=%v deadline=%v (probe frames=%d)", ch, deadline, probe.frames)
	}
	if probe.frames == 0 {
		t.Fatal("animation probe was not visited")
	}
}

// TestArmAnimationTicksWhileWorkIsActive pins the fallback that keeps a running
// tool's elapsed label live even when the animation walk does not reach it.
func TestArmAnimationTicksWhileWorkIsActive(t *testing.T) {
	wiring, _ := newRunTestWiring(t)
	wiring.work.active = true

	timer := time.NewTimer(time.Hour)
	timer.Stop()
	var deadline time.Time
	ch := wiring.armAnimation(timer, &deadline)
	if ch == nil || deadline.IsZero() {
		t.Fatalf("animation not armed while work is active: ch=%v deadline=%v", ch, deadline)
	}
	if delay := time.Until(deadline); delay <= 0 || delay > 2*time.Second {
		t.Fatalf("tick delay = %v", delay)
	}
}
