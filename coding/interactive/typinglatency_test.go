package interactive

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/dat267/pier/agent"
	"github.com/dat267/pier/ai"
	"github.com/dat267/pier/coding"
	"github.com/dat267/pier/tui"
)

// loopEditorText reads the editor on the loop goroutine (the editor is
// loop-owned since D146). A read is serviced during a render pass, so a value
// containing the keystroke means the input was dispatched and painted.
func loopEditorText(t *testing.T, app *App) string {
	t.Helper()
	done := make(chan string, 1)
	app.ui.Post(func() { done <- app.defaultEditor.GetText() })
	select {
	case text := <-done:
		return text
	case <-time.After(3 * time.Second):
		t.Fatal("the loop stopped answering editor reads")
		return ""
	}
}

// keystrokeSample is one measured keystroke plus what the loop did meanwhile.
type keystrokeSample struct {
	char      rune
	latency   time.Duration
	renders   int64
	beats     uint64
	editorLen int
}

// typeProbe types probe one character at a time, waiting for each character to
// reach the editor, and reports the latency plus the loop's activity.
func typeProbe(t *testing.T, app *App, probe string) []keystrokeSample {
	t.Helper()
	samples := make([]keystrokeSample, 0, len(probe))
	for _, char := range probe {
		before := loopEditorText(t, app)
		expected := before + string(char)
		renders := app.ui.RenderCount()
		beats := app.loopBeats()

		start := time.Now()
		app.postTerminalInput(string(char))
		deadline := time.Now().Add(5 * time.Second)
		for loopEditorText(t, app) != expected {
			if time.Now().After(deadline) {
				t.Fatalf("keystroke %q never reached the editor", string(char))
			}
		}
		samples = append(samples, keystrokeSample{
			char: char, latency: time.Since(start),
			renders: app.ui.RenderCount() - renders, beats: app.loopBeats() - beats,
			editorLen: len(expected),
		})
		time.Sleep(15 * time.Millisecond)
	}
	return samples
}

// logSamples logs one scenario and returns its worst keystroke latency.
func logSamples(t *testing.T, label string, samples []keystrokeSample) time.Duration {
	t.Helper()
	var worst, total time.Duration
	for _, sample := range samples {
		if sample.latency > worst {
			worst = sample.latency
		}
		total += sample.latency
		t.Logf("%s: %q latency=%8v renders+%d beats+%d", label, string(sample.char),
			sample.latency.Round(time.Microsecond), sample.renders, sample.beats)
	}
	t.Logf("%s: %d keystrokes, avg=%v, worst=%v", label, len(samples),
		(total / time.Duration(len(samples))).Round(time.Microsecond), worst.Round(time.Microsecond))
	return worst
}

// streamToolOutput emits a running bash tool call the way the tool does:
// partial results carry the tail window an OutputAccumulator reports (not the
// whole output), and updates are emitted at the production cadence. interval
// can be shortened to stress the partial-event queue.
func streamToolOutput(app *App, totalLines, linesPerUpdate int, interval time.Duration) func() {
	done := make(chan struct{})
	args, _ := ai.MarshalJSON(map[string]any{"command": "go test ./..."})
	app.sessionEvents.enqueue(&coding.SessionEvent{
		Type: coding.SessionToolExecutionStart,
		Agent: &agent.AgentEvent{
			Type: "tool_execution_start", ToolCallID: "t1", ToolName: "bash", Args: args,
		},
	})
	go func() {
		accumulator := coding.NewOutputAccumulator(0, 0, "pi-typing-probe")
		defer accumulator.CloseTempFile()
		line := 0
		for line < totalLines {
			select {
			case <-done:
				return
			default:
			}
			var chunk strings.Builder
			for i := 0; i < linesPerUpdate && line < totalLines; i++ {
				fmt.Fprintf(&chunk, "ok  github.com/dat267/pier/coding  0.12s (line %d)\n", line)
				line++
			}
			if err := accumulator.Append([]byte(chunk.String())); err != nil {
				return
			}
			snapshot := accumulator.Snapshot(true)
			app.sessionEvents.enqueue(&coding.SessionEvent{
				Type: coding.SessionToolExecutionUpdate,
				Agent: &agent.AgentEvent{
					Type: "tool_execution_update", ToolCallID: "t1",
					PartialResult: agent.AgentToolResult{Content: []ai.Content{ai.TextContent{Text: snapshot.Content}}},
				},
			})
			time.Sleep(interval)
		}
		app.sessionEvents.enqueue(&coding.SessionEvent{
			Type: coding.SessionToolExecutionEnd,
			Agent: &agent.AgentEvent{
				Type: "tool_execution_end", ToolCallID: "t1",
				Result: agent.AgentToolResult{Content: []ai.Content{ai.TextContent{Text: accumulator.Snapshot(true).Content}}},
			},
		})
	}()
	return func() { close(done) }
}

// slowTerminal simulates a terminal that cannot absorb frames instantly (a
// remote/tmux session, or an emulator busy with scrollback). Writes happen on
// the loop goroutine, so a stall there is a stall in input handling.
type slowTerminal struct {
	*fakeRendererTerminal
	perKB time.Duration
}

func (s *slowTerminal) Write(data string) {
	if s.perKB > 0 {
		time.Sleep(time.Duration(len(data)) * s.perKB / 1024)
	}
}

// onLoop runs fn on the loop goroutine and waits for it: the renderer and the
// display options are loop-owned (D146).
func onLoop(t *testing.T, app *App, fn func()) {
	t.Helper()
	done := make(chan struct{})
	app.ui.Post(func() {
		defer close(done)
		fn()
	})
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("the loop did not run the posted callback")
	}
}

// swapTerminalRender replaces the active renderer's terminal (on the loop) and
// returns a restore function.
func swapTerminalRender(t *testing.T, app *App, terminal tui.Terminal) func() {
	t.Helper()
	var original tui.Terminal
	switch renderer := app.initialUI.(type) {
	case *tui.AltScreen:
		onLoop(t, app, func() {
			original = renderer.Renderer.Terminal
			renderer.Renderer.Terminal = terminal
		})
		return func() { onLoop(t, app, func() { renderer.Renderer.Terminal = original }) }
	case *tui.MainScreen:
		onLoop(t, app, func() {
			original = renderer.Renderer.Terminal
			renderer.Renderer.Terminal = terminal
		})
		return func() { onLoop(t, app, func() { renderer.Renderer.Terminal = original }) }
	default:
		t.Fatalf("unexpected renderer %T", app.initialUI)
		return func() {}
	}
}

// TestTypingLatencyDuringToolCall is the reproduction harness for the reported
// stutter: a tool call streams output while the user types, and every keystroke
// is timed from the input post to the paint that shows it, together with what
// the loop did meanwhile (renders, iterations).
//
// It is also a regression guard. The stutter had two causes, both measured
// here: the bash tool snapshotted its whole output per 64 KB read (~6-9 ms and
// 2713 allocations per chunk, upstream throttles to 100 ms), and the snapshot
// itself built its tail window by prepending per line.
func TestTypingLatencyDuringToolCall(t *testing.T) {
	// The probe accumulator writes its full output to a temp file, and
	// os.TempDir() follows TMPDIR: without this the suite leaves one
	// pi-typing-probe-*.log per run in the shared /tmp (215 had accumulated).
	t.Setenv("TMPDIR", t.TempDir())
	app, cleanup := newTestApp(t)
	defer cleanup()

	// A transcript with history, so the paint has real work to do.
	for i := 0; i < 120; i++ {
		app.events.HandleEvent(&coding.SessionEvent{
			Type: coding.SessionMessageStart,
			Agent: agentEvent("message_start", &ai.UserMessage{
				Content: ai.StringOrBlocks{Text: fmt.Sprintf("user message %d with enough text to wrap across a couple of terminal lines", i)},
			}),
		})
		assistant := &ai.AssistantMessage{
			API: ai.APIAnthropicMessages, Provider: "test", Model: "m",
			Content: ai.ContentList{ai.TextContent{Text: fmt.Sprintf("reply %d\n\n```go\nfunc f%d() int { return %d }\n```\n", i, i, i)}},
		}
		app.events.HandleEvent(&coding.SessionEvent{Type: coding.SessionMessageStart, Agent: agentEvent("message_start", assistant)})
		app.events.HandleEvent(&coding.SessionEvent{Type: coding.SessionMessageEnd, Agent: agentEvent("message_end", assistant)})
	}

	stop := startLoopApp(t, app)
	defer stop()

	worst := logSamples(t, "idle", typeProbe(t, app, "qzx"))

	// Production cadence: a long command streaming with throttled updates.
	stopProd := streamToolOutput(app, 20000, 60, coding.BashUpdateThrottleMS)
	time.Sleep(30 * time.Millisecond)
	if w := logSamples(t, "streaming", typeProbe(t, app, "qzy")); w > worst {
		worst = w
	}
	stopProd()
	time.Sleep(30 * time.Millisecond)

	// A hostile producer: an update every 2 ms with full tail windows keeps the
	// partial-event queue saturated, which must coalesce rather than stall input.
	stopFast := streamToolOutput(app, 20000, 60, 2*time.Millisecond)
	time.Sleep(30 * time.Millisecond)
	if w := logSamples(t, "streaming-fast", typeProbe(t, app, "qzz")); w > worst {
		worst = w
	}
	stopFast()
	time.Sleep(30 * time.Millisecond)

	// Output expanded: the component restyles its whole tail window per update.
	onLoop(t, app, func() { app.display.ToolOutputExpanded = true })
	stopExpanded := streamToolOutput(app, 20000, 60, coding.BashUpdateThrottleMS)
	time.Sleep(30 * time.Millisecond)
	if w := logSamples(t, "streaming-expanded", typeProbe(t, app, "qzw")); w > worst {
		worst = w
	}
	stopExpanded()
	onLoop(t, app, func() { app.display.ToolOutputExpanded = false })
	time.Sleep(30 * time.Millisecond)

	// A slow terminal: frame writes happen on the loop, so their cost is input
	// latency.
	restore := swapTerminalRender(t, app, &slowTerminal{
		fakeRendererTerminal: &fakeRendererTerminal{width: 100, height: 30},
		perKB:                2 * time.Millisecond,
	})
	stopSlow := streamToolOutput(app, 20000, 60, coding.BashUpdateThrottleMS)
	time.Sleep(30 * time.Millisecond)
	if w := logSamples(t, "slow-terminal", typeProbe(t, app, "qz1")); w > worst {
		worst = w
	}
	stopSlow()
	restore()

	t.Logf("worst keystroke latency: %v", worst.Round(time.Microsecond))
	if worst > 150*time.Millisecond {
		t.Fatalf("typing during a tool call took %v; the UI is not keeping up", worst.Round(time.Millisecond))
	}
}
