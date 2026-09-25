package interactive

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dat267/pier/tui"
)

func TestInputLatencyRecorderLogsOnlySlowKeystrokes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pier-input-latency.log")
	recorder := newInputLatencyRecorder(path, 50*time.Millisecond)
	base := time.Now()

	// A fast round trip is ignored.
	recorder.flushed(base, base.Add(2*time.Millisecond), base.Add(3*time.Millisecond))
	if data, err := os.ReadFile(path); err == nil && len(data) != 0 {
		t.Fatalf("fast keystroke logged: %q", data)
	}

	// A slow one is logged with the read and write segments named.
	recorder.flushed(base, base.Add(10*time.Millisecond), base.Add(120*time.Millisecond))
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	line := string(data)
	if !strings.Contains(line, "keystroke latency") {
		t.Fatalf("missing latency record: %q", line)
	}
	if !strings.Contains(line, "read=") || !strings.Contains(line, "write=") {
		t.Fatalf("record must name both segments: %q", line)
	}
}

func TestInputLatencyRecorderDisabled(t *testing.T) {
	if recorder := newInputLatencyRecorder("", 50*time.Millisecond); recorder != nil {
		t.Fatal("empty path must disable the recorder")
	}
	if recorder := newInputLatencyRecorder("/tmp/pier-lat.log", 0); recorder != nil {
		t.Fatal("zero threshold must disable the recorder")
	}
	// A nil recorder must be safe: the wiring calls it unconditionally.
	var recorder *inputLatencyRecorder
	now := time.Now()
	recorder.flushed(now, now, now)
}

// fakeRawTerminal satisfies tui.RawInputTerminal for the wiring tests: it
// embeds the existing Terminal fake and records the latency hooks.
type fakeRawTerminal struct {
	*fakeRendererTerminal
	readAt   time.Time
	observer tui.InputLatencyObserver
	marked   []time.Time
}

func (f *fakeRawTerminal) EnableRawInput()                           {}
func (f *fakeRawTerminal) FeedInput([]byte) []string                 { return nil }
func (f *fakeRawTerminal) NextInputFlushDeadline() (time.Time, bool) { return time.Time{}, false }
func (f *fakeRawTerminal) FlushPendingInput() []string               { return nil }
func (f *fakeRawTerminal) MarkInputRead(readAt time.Time)            { f.marked = append(f.marked, readAt) }
func (f *fakeRawTerminal) LastInputAt() time.Time                    { return f.readAt }
func (f *fakeRawTerminal) SetInputLatencyObserver(fn tui.InputLatencyObserver) {
	f.observer = fn
}

func TestRunWiringMarksInputRead(t *testing.T) {
	readAt := time.Now()
	terminal := &fakeRawTerminal{fakeRendererTerminal: &fakeRendererTerminal{width: 80, height: 24}, readAt: readAt}
	wiring := &RunWiring{RawTerminal: terminal}

	wiring.markInputRead()
	if len(terminal.marked) != 1 || !terminal.marked[0].Equal(readAt) {
		t.Fatalf("marked = %v, want [%v]", terminal.marked, readAt)
	}

	// A zero read stamp (no input yet) must not be marked: an untagged next
	// frame would inherit it.
	terminal.readAt = time.Time{}
	terminal.marked = nil
	wiring.markInputRead()
	if len(terminal.marked) != 0 {
		t.Fatalf("zero readAt marked: %v", terminal.marked)
	}
}

func TestInstallInputLatencyObserverRespectsThreshold(t *testing.T) {
	terminal := &fakeRawTerminal{fakeRendererTerminal: &fakeRendererTerminal{width: 80, height: 24}}
	app := &App{rawTerminal: terminal}
	app.options.AgentDir = t.TempDir()

	t.Setenv("PIER_INPUT_LAT_MS", "1")
	installInputLatencyObserver(app)
	if terminal.observer == nil {
		t.Fatal("observer not installed when enabled")
	}

	terminal.observer = nil
	t.Setenv("PIER_INPUT_LAT_MS", "0")
	installInputLatencyObserver(app)
	if terminal.observer != nil {
		t.Fatal("observer installed when disabled")
	}
}
