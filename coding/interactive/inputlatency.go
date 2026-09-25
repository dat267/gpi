package interactive

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

// defaultInputLatencyThreshold is the total keystroke round trip (stdin read →
// console write) above which a record is written. 50 ms is below what a user
// notices on a local terminal, so ordinary operation stays silent.
const defaultInputLatencyThreshold = 50 * time.Millisecond

// inputLatencyRecorder logs keystrokes whose end-to-end round trip exceeds a
// threshold, splitting it into the part before the app (stdin read), the loop
// (dispatch + paint) and the writer (console write). It exists to bisect a
// slow terminal — Windows ConPTY stops draining while a selection is active —
// without guessing: one record names the offending segment.
//
// It is driven by tui.InputLatencyObserver, which runs on the terminal writer
// goroutine, so append is serialized by its own mutex.
type inputLatencyRecorder struct {
	path      string
	threshold time.Duration

	mu sync.Mutex
}

// newInputLatencyRecorder returns nil when instrumentation is disabled (no
// path, or a zero threshold), so the observer callback is a no-op and the
// terminal never allocates a frame tag.
func newInputLatencyRecorder(path string, threshold time.Duration) *inputLatencyRecorder {
	if path == "" || threshold <= 0 {
		return nil
	}
	return &inputLatencyRecorder{path: path, threshold: threshold}
}

// flushed is the tui.InputLatencyObserver. readAt is when the reader read the
// keystroke, paintAt when the loop committed its frame, writtenAt when the
// console accepted it. A nil recorder ignores the call.
func (r *inputLatencyRecorder) flushed(readAt, paintAt, writtenAt time.Time) {
	if r == nil {
		return
	}
	total := writtenAt.Sub(readAt)
	if total < r.threshold {
		return
	}
	read := paintAt.Sub(readAt)
	write := writtenAt.Sub(paintAt)
	r.append(fmt.Sprintf("%s keystroke latency total=%v read=%v write=%v\n",
		time.Now().Format(time.RFC3339Nano),
		total.Round(time.Microsecond), read.Round(time.Microsecond), write.Round(time.Microsecond)))
}

// append writes one record, truncating an oversized log first (same bound as
// the stall log) so a pathological terminal cannot fill the disk.
func (r *inputLatencyRecorder) append(line string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if info, err := os.Stat(r.path); err == nil && info.Size() > stallLogMaxBytes {
		_ = os.Truncate(r.path, 0)
	}
	file, err := os.OpenFile(r.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	_, _ = file.WriteString(line)
	_ = file.Close()
}

// inputLatencyThreshold reads PIER_INPUT_LAT_MS (0 disables instrumentation).
func inputLatencyThreshold() time.Duration {
	if raw := os.Getenv("PIER_INPUT_LAT_MS"); raw != "" {
		if ms, err := strconv.Atoi(raw); err == nil {
			return time.Duration(ms) * time.Millisecond
		}
	}
	return defaultInputLatencyThreshold
}

// inputLatencyLogPath is where slow keystroke records are written.
func inputLatencyLogPath(agentDir string) string {
	if agentDir == "" {
		return ""
	}
	return filepath.Join(agentDir, "pier-input-latency.log")
}

// installInputLatencyObserver wires the recorder to the raw terminal. No-op
// when instrumentation is disabled or the terminal is not a raw-input one.
func installInputLatencyObserver(app *App) {
	if app == nil || app.rawTerminal == nil {
		return
	}
	recorder := newInputLatencyRecorder(inputLatencyLogPath(app.options.AgentDir), inputLatencyThreshold())
	if recorder == nil {
		return
	}
	app.rawTerminal.SetInputLatencyObserver(recorder.flushed)
}
