package interactive

import (
	"strings"
	"sync"
	"testing"

	"github.com/dat267/pier/tui"
)

// recordingTerminal records writes so the theme probe tests can assert what was
// sent to the terminal.
type recordingTerminal struct {
	*fakeRendererTerminal
	mu     sync.Mutex
	writes []string
}

func (r *recordingTerminal) Write(data string) {
	r.mu.Lock()
	r.writes = append(r.writes, data)
	r.mu.Unlock()
}

func (r *recordingTerminal) hasWrite(sub string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, write := range r.writes {
		if strings.Contains(write, sub) {
			return true
		}
	}
	return false
}

func (r *recordingTerminal) joinedWrites() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.Join(r.writes, "")
}

// TestBackgroundProbeRoundTripThroughRenderer drives the real renderer through
// the safe detection path: the color-scheme notification toggle is deferred
// until Start, the OSC 11 probe writes the query without blocking, and a reply
// switches the auto theme. This is the end-to-end shape of the app wiring.
func TestBackgroundProbeRoundTripThroughRenderer(t *testing.T) {
	dir := t.TempDir()
	SetCustomThemesDir(dir)
	SetRegisteredThemes(nil)

	terminal := &recordingTerminal{fakeRendererTerminal: &fakeRendererTerminal{width: 80, height: 24}}
	screen := tui.NewMainScreen(terminal, false, "")
	setting := "light/dark"
	controller := NewInteractiveThemeController(ThemeControllerOptions{
		UI:                  themeUIAdapter{ui: screen},
		InitialThemeSetting: &setting,
		TimeoutMS:           1,
		Env:                 func(key string) string { return "" },
	})
	controller.ApplyFromSettings()

	if terminal.hasWrite("\x1b[?2031h") {
		t.Fatal("color-scheme notification written before Start")
	}
	screen.Start()
	if !terminal.hasWrite("\x1b[?2031h") {
		t.Fatal("Start did not replay the color-scheme notification")
	}

	controller.ProbeTerminalBackground()
	if !terminal.hasWrite("\x1b]11;?") {
		t.Fatal("probe did not write the OSC 11 query")
	}
	if controller.ActiveThemeName() != "dark" {
		t.Fatalf("active before reply = %q", controller.ActiveThemeName())
	}

	screen.HandleTerminalInput("\x1b]11;#ffffff\x07")
	if controller.ActiveThemeName() != "light" {
		t.Fatalf("active after light reply = %q", controller.ActiveThemeName())
	}
	screen.HandleTerminalInput("\x1b]11;#000000\x07")
	if controller.ActiveThemeName() != "dark" {
		t.Fatalf("active after dark reply = %q", controller.ActiveThemeName())
	}
	screen.Stop(tui.TuiStopOptions{})
}
