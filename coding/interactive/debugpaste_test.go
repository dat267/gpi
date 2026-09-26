package interactive

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dat267/pier/coding"
)

// TestDebugCommandWritesLog covers the /debug wiring: the command handler used
// to exist but CommandWiring.WriteDebugLog was never assigned, so the command
// silently did nothing. It writes the rendered lines and session entries to
// the standard debug-log path (coding.GetDebugLogPath, upstream
// getDebugLogPath).
func TestDebugCommandWritesLog(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", dir)

	app, cleanup := newTestApp(t)
	defer cleanup()
	app.Init(context.Background())

	app.submit.HandleSubmit(context.Background(), "/debug")

	logPath := filepath.Join(dir, coding.AppName+"-debug.log")
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read debug log: %v", err)
	}
	text := string(data)
	for _, want := range []string{
		"Debug output at 20",
		"=== All rendered lines with visible widths ===",
		"=== Agent messages (JSONL) ===",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("debug log missing %q:\n%s", want, text)
		}
	}
}

// TestHandleRightClickPasteInsertsClipboard covers the onRightClickPaste hook:
// the renderer's Windows right-click paste had no handler wired. Upstream
// handleRightClickPaste reads the clipboard, re-checks the focus, then feeds
// the text to the focused component as a bracketed paste.
func TestHandleRightClickPasteInsertsClipboard(t *testing.T) {
	app, cleanup := newTestApp(t)
	defer cleanup()
	app.Init(context.Background())

	SetClipboardReader(func() (string, error) { return "hello paste", nil })
	t.Cleanup(func() { SetClipboardReader(coding.ReadClipboardText) })

	app.handleRightClickPaste()

	if got := app.defaultEditor.GetText(); !strings.Contains(got, "hello paste") {
		t.Fatalf("editor text = %q, want it to contain the pasted clipboard", got)
	}
}
