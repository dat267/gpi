package interactive

import (
	"bufio"
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dat267/gpi/ai"
	"github.com/dat267/gpi/coding"
	"github.com/dat267/gpi/tui"
)

// ---- Chat viewport ----

type staticLines struct {
	lines []string
}

func (s *staticLines) Render(width int) []string { return s.lines }
func (s *staticLines) Invalidate()               {}

// TestChatViewportLayout verifies the viewport composition.
func TestChatViewportLayout(t *testing.T) {
	document := &staticLines{lines: []string{"doc"}}
	pending := &staticLines{lines: []string{"pending"}}
	status := &staticLines{lines: []string{"status"}}
	editor := &staticLines{lines: []string{"editor"}}
	footer := &staticLines{lines: []string{"footer"}}
	above := &staticLines{lines: []string{"above"}}
	below := &staticLines{lines: []string{"below"}}

	viewport := CreateChatViewport(ChatViewportOptions{
		Document: document, PendingMessages: pending, Status: status, Editor: editor, Footer: footer,
		WidgetsAbove: above, WidgetsBelow: below,
	})
	if viewport.Root == nil || viewport.Transcript == nil {
		t.Fatal("viewport not composed")
	}
	if !viewport.Transcript.Primary() || viewport.Transcript.Overscroll() != "chain" {
		t.Fatalf("transcript = primary:%v overscroll:%q", viewport.Transcript.Primary(), viewport.Transcript.Overscroll())
	}
	if viewport.Transcript.Scrollbar() != tui.ScrollbarAuto {
		t.Fatalf("scrollbar = %q", viewport.Transcript.Scrollbar())
	}

	frame := tui.RenderLayoutFrame(viewport.Root, 20, 12, func() {})
	rendered := strings.Join(frame.Lines, "\n")
	for _, expected := range []string{"doc", "pending", "status", "above", "editor", "below", "footer"} {
		if !strings.Contains(rendered, expected) {
			t.Fatalf("missing %q in:\n%s", expected, rendered)
		}
	}

	// Without widgets the dock still renders.
	plain := CreateChatViewport(ChatViewportOptions{
		Document: document, PendingMessages: pending, Status: status, Editor: editor, Footer: footer,
		Scrollbar: tui.ScrollbarHidden,
	})
	if plain.Transcript.Scrollbar() != tui.ScrollbarHidden {
		t.Fatalf("scrollbar = %q", plain.Transcript.Scrollbar())
	}
	plainFrame := tui.RenderLayoutFrame(plain.Root, 20, 8, func() {})
	if joined := strings.Join(plainFrame.Lines, "\n"); strings.Contains(joined, "above") {
		t.Fatalf("unexpected widget:\n%s", joined)
	}
}

// ---- Model search ----

// TestModelSearchAgainstUpstreamGolden verifies the search text builders.
func TestModelSearchAgainstUpstreamGolden(t *testing.T) {
	file, err := os.Open("testdata/modelsearch_golden.txt")
	if err != nil {
		t.Fatalf("open golden: %v", err)
	}
	defer file.Close()
	golden := map[string]string{}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		parts := strings.SplitN(line, " => ", 2)
		if len(parts) != 2 {
			t.Fatalf("bad golden line %q", line)
		}
		golden[parts[0]] = parts[1]
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan: %v", err)
	}

	items := []ModelSearchItem{
		{ID: "gpt-5", Provider: "openai"},
		{ID: "gpt-5", Provider: "openai", Name: "GPT-5", HasName: true},
		{ID: "gpt-5", Provider: "openrouter/openai", Name: "OR", HasName: true},
		{ID: "", Provider: "p"},
	}
	for _, item := range items {
		key := `{"id":"` + item.ID + `","provider":"` + item.Provider + `"`
		if item.HasName {
			key += `,"name":"` + item.Name + `"`
		}
		key += `}`
		searchKey := "search " + key
		selectorKey := "selector " + key
		if want, ok := golden[searchKey]; ok {
			if got := mustJSON(GetModelSearchText(item)); got != want {
				t.Fatalf("%s: got %s want %s", searchKey, got, want)
			}
		}
		if want, ok := golden[selectorKey]; ok {
			if got := mustJSON(GetModelSelectorSearchText(item)); got != want {
				t.Fatalf("%s: got %s want %s", selectorKey, got, want)
			}
		}
	}
}

// ---- External editor ----

// TestExternalEditor verifies the editor invocation and failure handling.
func TestExternalEditor(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "editor.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf 'edited' > \"$1\"\n"), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}
	result := EditInExternalEditor(ExternalEditorOptions{Command: script, Content: "original"})
	if result.Status != "complete" || result.Content != "edited" {
		t.Fatalf("result = %+v", result)
	}

	// A BOM is stripped and the trailing newline removed.
	script2 := filepath.Join(dir, "editor2.sh")
	if err := os.WriteFile(script2, []byte("#!/bin/sh\nprintf '\\357\\273\\277content\\n' > \"$1\"\n"), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}
	result = EditInExternalEditor(ExternalEditorOptions{Command: script2, Content: "x"})
	if result.Status != "complete" || result.Content != "content" {
		t.Fatalf("result = %+v", result)
	}

	// A failing editor reports failure.
	result = EditInExternalEditor(ExternalEditorOptions{Command: filepath.Join(dir, "missing-editor"), Content: "x"})
	if result.Status != "failed" {
		t.Fatalf("result = %+v", result)
	}
}

// ---- Catalog refresh ----

type fakeCatalogRuntime struct {
	mu      sync.Mutex
	calls   int
	started chan struct{}
	release chan struct{}
}

func (f *fakeCatalogRuntime) Refresh(ctx context.Context, options *coding.ModelsRefreshCallOptions) (ai.ModelsRefreshResult, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	if f.started != nil {
		select {
		case f.started <- struct{}{}:
		default:
		}
	}
	if f.release != nil {
		select {
		case <-f.release:
		case <-ctx.Done():
			return ai.ModelsRefreshResult{Aborted: true}, ctx.Err()
		}
	}
	return ai.ModelsRefreshResult{}, nil
}

// TestCatalogRefreshSharing verifies that concurrent refreshes share one call.
func TestCatalogRefreshSharing(t *testing.T) {
	runtime := &fakeCatalogRuntime{started: make(chan struct{}, 4), release: make(chan struct{})}
	coordinator := &ModelCatalogRefreshCoordinator{}

	var wg sync.WaitGroup
	results := make([]error, 3)
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			_, err := coordinator.Refresh(context.Background(), runtime)
			results[index] = err
		}(i)
	}

	// Wait for the shared refresh to start, then release it.
	select {
	case <-runtime.started:
	case <-time.After(2 * time.Second):
		t.Fatal("refresh did not start")
	}
	close(runtime.release)
	wg.Wait()

	for _, err := range results {
		if err != nil {
			t.Fatalf("refresh error: %v", err)
		}
	}
	runtime.mu.Lock()
	calls := runtime.calls
	runtime.mu.Unlock()
	if calls != 1 {
		t.Fatalf("refresh calls = %d", calls)
	}
}

// TestCatalogRefreshCancellation verifies per-caller cancellation.
func TestCatalogRefreshCancellation(t *testing.T) {
	runtime := &fakeCatalogRuntime{started: make(chan struct{}, 4), release: make(chan struct{})}
	coordinator := &ModelCatalogRefreshCoordinator{}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := coordinator.Refresh(ctx, runtime)
		done <- err
	}()
	select {
	case <-runtime.started:
	case <-time.After(2 * time.Second):
		t.Fatal("refresh did not start")
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected cancellation error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("refresh did not return on cancellation")
	}
	close(runtime.release)
}

// ---- Theme controller ----

type fakeThemeUI struct {
	mu            sync.Mutex
	invalidates   int
	renders       int
	notifications bool
	listener      func(TerminalTheme)
}

func (f *fakeThemeUI) Invalidate() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.invalidates++
}

func (f *fakeThemeUI) RequestRender() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.renders++
}

func (f *fakeThemeUI) SetTerminalColorSchemeNotifications(enabled bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.notifications = enabled
}

func (f *fakeThemeUI) OnTerminalColorSchemeChange(listener func(theme TerminalTheme)) func() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listener = listener
	return func() {}
}

func (f *fakeThemeUI) emit(theme TerminalTheme) {
	f.mu.Lock()
	listener := f.listener
	f.mu.Unlock()
	if listener != nil {
		listener(theme)
	}
}

type fakeThemeSettings struct {
	setting  *string
	setCalls []string
	flushes  int
}

func (f *fakeThemeSettings) GetThemeSetting() *string { return f.setting }
func (f *fakeThemeSettings) SetTheme(theme string) {
	value := theme
	f.setting = &value
	f.setCalls = append(f.setCalls, theme)
}
func (f *fakeThemeSettings) Flush() { f.flushes++ }

// TestThemeControllerLifecycle verifies the controller's settings-driven flow.
func TestThemeControllerLifecycle(t *testing.T) {
	dir := t.TempDir()
	SetCustomThemesDir(dir)
	SetRegisteredThemes(nil)

	ui := &fakeThemeUI{}
	settings := &fakeThemeSettings{}
	errors := []string{}
	changed := 0

	controller := NewInteractiveThemeController(ThemeControllerOptions{
		UI:                 ui,
		GetSettingsManager: func() ThemeSettings { return settings },
		ShowError:          func(message string) { errors = append(errors, message) },
		OnChanged:          func() { changed++ },
		Detector:           nil,
		TimeoutMS:          1,
		Env:                func(key string) string { return "" },
	})
	if CurrentThemeName() == "" {
		t.Fatal("no theme initialized")
	}
	if controller.GetTerminalTheme() != TerminalThemeDark {
		t.Fatalf("terminal theme = %q", controller.GetTerminalTheme())
	}

	// Selecting a theme by name persists the selection and notifies.
	result := controller.SetThemeName("light", true)
	if !result.Success || controller.ActiveThemeName() != "light" {
		t.Fatalf("set theme = %+v active=%q", result, controller.ActiveThemeName())
	}
	if changed == 0 || ui.invalidates == 0 {
		t.Fatal("change not propagated")
	}
	if selection := controller.GetThemeSelection(); selection != "light" {
		t.Fatalf("selection = %q", selection)
	}

	// An unknown theme reports the error and falls back to dark.
	result = controller.SetThemeName("missing", true)
	if result.Success {
		t.Fatal("missing theme accepted")
	}
	if controller.ActiveThemeName() != "dark" {
		t.Fatalf("active = %q", controller.ActiveThemeName())
	}
	if len(errors) != 1 || !strings.Contains(errors[0], "Fell back to dark theme") {
		t.Fatalf("errors = %v", errors)
	}

	// Auto settings sync with the terminal color scheme.
	light := "light"
	dark := "dark"
	setting := light + "/" + dark
	controller.SetThemeSetting(setting)
	if controller.GetThemeSelection() != setting {
		t.Fatalf("selection = %q", controller.GetThemeSelection())
	}
	ui.emit(TerminalThemeLight)
	if controller.ActiveThemeName() != "light" {
		t.Fatalf("auto light = %q", controller.ActiveThemeName())
	}
	ui.emit(TerminalThemeDark)
	if controller.ActiveThemeName() != "dark" {
		t.Fatalf("auto dark = %q", controller.ActiveThemeName())
	}
	ui.mu.Lock()
	notifications := ui.notifications
	ui.mu.Unlock()
	if !notifications {
		t.Fatal("auto-sync notifications not enabled")
	}

	// Preview switches without persisting.
	controller.Preview("light")
	if CurrentThemeName() != "light" {
		t.Fatalf("preview = %q", CurrentThemeName())
	}

	// In-memory instances.
	instance := CreateTheme(getBuiltinThemes()["dark"], ColorModeTruecolor, "")
	if result := controller.SetThemeInstance(instance); !result.Success || controller.ActiveThemeName() != "<in-memory>" {
		t.Fatalf("instance = %+v %q", result, controller.ActiveThemeName())
	}

	controller.DisableAutoSync()
	ui.mu.Lock()
	notifications = ui.notifications
	ui.mu.Unlock()
	if notifications {
		t.Fatal("notifications still enabled")
	}
	controller.Dispose()
}

// TestThemeControllerAutoFromEnvironment verifies COLORFGBG-driven defaults.
func TestThemeControllerAutoFromEnvironment(t *testing.T) {
	SetCustomThemesDir(t.TempDir())
	SetRegisteredThemes(nil)
	ui := &fakeThemeUI{}
	settings := &fakeThemeSettings{}
	env := map[string]string{"COLORFGBG": "0;15"}
	controller := NewInteractiveThemeController(ThemeControllerOptions{
		UI:                 ui,
		GetSettingsManager: func() ThemeSettings { return settings },
		Detector:           nil,
		TimeoutMS:          1,
		Env:                func(key string) string { return env[key] },
	})
	if controller.GetTerminalTheme() != TerminalThemeLight {
		t.Fatalf("terminal theme = %q", controller.GetTerminalTheme())
	}

	// Applying from settings without a stored setting persists a high-confidence
	// detection.
	controller.ApplyFromSettings()
	if len(settings.setCalls) != 1 || settings.setCalls[0] != "light" {
		t.Fatalf("set calls = %v", settings.setCalls)
	}
	if settings.flushes == 0 {
		t.Fatal("settings not flushed")
	}
}

// ---- Detection helpers ----

type fakeDetector struct {
	rgb         RgbColor
	hasRGB      bool
	scheme      TerminalTheme
	hasScheme   bool
	rgbCalls    int
	schemeCalls int
}

func (f *fakeDetector) QueryTerminalBackgroundColor(timeoutMs int) (RgbColor, bool) {
	f.rgbCalls++
	return f.rgb, f.hasRGB
}

func (f *fakeDetector) QueryTerminalColorScheme(timeoutMs int) (TerminalTheme, bool) {
	f.schemeCalls++
	return f.scheme, f.hasScheme
}

// TestTerminalDetection verifies the query-based detection helpers.
func TestTerminalDetection(t *testing.T) {
	detector := &fakeDetector{rgb: RgbColor{R: 250, G: 250, B: 250}, hasRGB: true}
	detection := DetectTerminalBackgroundTheme(detector, 1, func(string) string { return "" })
	if detection.Theme != TerminalThemeLight || detection.Source != "terminal background" {
		t.Fatalf("detection = %+v", detection)
	}
	if detection.Detail != "OSC 11 background rgb(250, 250, 250)" {
		t.Fatalf("detail = %q", detection.Detail)
	}

	// A missing background falls back to the environment.
	detector = &fakeDetector{}
	detection = DetectTerminalBackgroundTheme(detector, 1, func(key string) string {
		if key == "COLORFGBG" {
			return "0;15"
		}
		return ""
	})
	if detection.Source != "COLORFGBG" || detection.Theme != TerminalThemeLight {
		t.Fatalf("detection = %+v", detection)
	}

	// The color-scheme report wins for auto detection.
	detector = &fakeDetector{scheme: TerminalThemeDark, hasScheme: true, rgb: RgbColor{R: 255, G: 255, B: 255}, hasRGB: true}
	if theme := DetectTerminalThemeForAuto(detector, 1, func(string) string { return "" }); theme != TerminalThemeDark {
		t.Fatalf("auto theme = %q", theme)
	}
}
