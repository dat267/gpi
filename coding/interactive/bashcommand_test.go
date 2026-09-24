package interactive

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/dat267/pier/ai"
	"github.com/dat267/pier/coding"
)

// bashComponentIn returns the transcript's bash component, if any.
func bashComponentIn(app *App) *BashExecutionComponent {
	for _, child := range app.Chat.Children {
		if component, ok := child.(*BashExecutionComponent); ok {
			return component
		}
	}
	return nil
}

// pumpUntilBashOutput drains the renderer's posted callbacks — the component
// work is posted from the command's goroutine — until the output arrives.
func pumpUntilBashOutput(t *testing.T, app *App, want string) *BashExecutionComponent {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		app.UI.RenderNow(true)
		component := bashComponentIn(app)
		if component != nil && strings.Contains(component.GetOutput(), want) {
			return component
		}
		if time.Now().After(deadline) {
			if component == nil {
				t.Fatalf("no bash component in the transcript")
			}
			t.Fatalf("bash output = %q, want it to contain %q", component.GetOutput(), want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// recordedBashMessage returns the session's bash-execution message, decoded.
func recordedBashMessage(t *testing.T, app *App) (command, output string, excludeFromContext bool) {
	t.Helper()
	for _, message := range app.Session.Agent.State().Messages {
		custom, ok := message.(*ai.CustomMessage)
		if !ok || custom.Role != coding.RoleBashExecution {
			continue
		}
		var fields struct {
			Command            string `json:"command"`
			Output             string `json:"output"`
			ExcludeFromContext bool   `json:"excludeFromContext"`
		}
		if err := json.Unmarshal(custom.Content, &fields); err != nil {
			t.Fatalf("decode bash message: %v", err)
		}
		return fields.Command, fields.Output, fields.ExcludeFromContext
	}
	return "", "", false
}

// pumpUntilRecorded drains the loop until the command's result has been
// recorded in the session. The output appears a callback earlier than the
// record, so waiting for output alone leaves the recording to chance.
func pumpUntilRecorded(t *testing.T, app *App, want string) (command, output string, exclude bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		app.UI.RenderNow(true)
		if command, output, exclude = recordedBashMessage(t, app); command == want {
			return command, output, exclude
		}
		if time.Now().After(deadline) {
			t.Fatalf("no recorded bash message for %q (got %q)", want, command)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// `!command` in the editor runs the shell command, streams its output into the
// transcript, and records the result in the session history.
func TestBashCommandFromTheEditor(t *testing.T) {
	app, cleanup := newTestApp(t)
	defer cleanup()

	run := newSubmitWiring(app).Handlers.HandleBashCommand
	if run == nil {
		t.Fatal("the submit wiring has no bash-command handler")
	}
	if err := run("echo hello-from-bash", false); err != nil {
		t.Fatal(err)
	}

	component := pumpUntilBashOutput(t, app, "hello-from-bash")
	if component.GetCommand() != "echo hello-from-bash" {
		t.Errorf("command = %q", component.GetCommand())
	}

	// The result also joins the session, which is what keeps it in the
	// transcript replay.
	command, output, exclude := pumpUntilRecorded(t, app, "echo hello-from-bash")
	if command != "echo hello-from-bash" || !strings.Contains(output, "hello-from-bash") {
		t.Errorf("recorded command = %q output = %q", command, output)
	}
	if exclude {
		t.Error("a single-! command must stay in the model's context")
	}
}

// `!!command` is the excluded form: same rendering, but the recorded result is
// kept out of the model's context.
func TestBashCommandExcludedForm(t *testing.T) {
	app, cleanup := newTestApp(t)
	defer cleanup()

	run := newSubmitWiring(app).Handlers.HandleBashCommand
	if err := run("echo not-for-the-model", true); err != nil {
		t.Fatal(err)
	}
	pumpUntilBashOutput(t, app, "not-for-the-model")

	_, _, exclude := pumpUntilRecorded(t, app, "echo not-for-the-model")
	if !exclude {
		t.Error("the !! form must record excludeFromContext")
	}
}

// A command that exits non-zero still completes its component, showing the exit
// code rather than looking like it succeeded.
func TestBashCommandShowsTheExitCode(t *testing.T) {
	app, cleanup := newTestApp(t)
	defer cleanup()

	run := newSubmitWiring(app).Handlers.HandleBashCommand
	if err := run("exit 3", false); err != nil {
		t.Fatal(err)
	}

	waitForBashExitCode(t, app, "(exit 3)")
}

// waitForBashExitCode drains the loop until the component renders its exit
// code. SetComplete and the recording both happen in one posted callback, so
// the rendered exit code means every record that is going to happen has
// happened.
func waitForBashExitCode(t *testing.T, app *App, marker string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		app.UI.RenderNow(true)
		if component := bashComponentIn(app); component != nil {
			if rendered := strings.Join(component.Render(80), "\n"); strings.Contains(rendered, marker) {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("the component never showed %q; children = %d", marker, len(app.Chat.Children))
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// countBashExecutionEntries counts the session's bash-execution entries: the
// transcript replay (and therefore what `pier -c` renders) is built from those.
func countBashExecutionEntries(t *testing.T, app *App) int {
	t.Helper()
	count := 0
	for _, entry := range app.Session.Sessions.GetEntries() {
		if len(entry.Message) == 0 {
			continue
		}
		var message struct {
			Role string `json:"role"`
		}
		if err := json.Unmarshal(entry.Message, &message); err != nil {
			t.Fatalf("decode entry message: %v", err)
		}
		if message.Role == coding.RoleBashExecution {
			count++
		}
	}
	return count
}

// TestBashCommandIsRecordedOnce pins one session entry per `!` run. The
// session's ExecuteBash records the result itself (upstream executeBash calls
// recordBashResult), so the handler must not record it again — it did, which
// wrote every run to the session twice and made a reopened session render the
// command and its output twice.
func TestBashCommandIsRecordedOnce(t *testing.T) {
	app, cleanup := newTestApp(t)
	defer cleanup()

	// A non-zero exit, because that is what renders the completion marker the
	// wait below keys on.
	run := newSubmitWiring(app).Handlers.HandleBashCommand
	if err := run("exit 3", false); err != nil {
		t.Fatal(err)
	}
	pumpUntilRecorded(t, app, "exit 3")
	waitForBashExitCode(t, app, "(exit 3)")

	if count := countBashExecutionEntries(t, app); count != 1 {
		t.Fatalf("session holds %d bash-execution entries for one command; want 1", count)
	}
}

// The whole path, not just the handler: typing "!command" and submitting runs
// it, which is what was dead before (the seam existed, the call site existed,
// nothing was connected).
func TestBashCommandThroughSubmit(t *testing.T) {
	app, cleanup := newTestApp(t)
	defer cleanup()

	wiring := newSubmitWiring(app)
	wiring.HandleSubmit(context.Background(), "!echo through-submit")

	pumpUntilBashOutput(t, app, "through-submit")
	if command, _, exclude := pumpUntilRecorded(t, app, "echo through-submit"); command != "echo through-submit" || exclude {
		t.Errorf("recorded command = %q exclude = %v", command, exclude)
	}
}

// The "!!" form reaches the same handler with the exclusion flag.
func TestBashCommandExcludedThroughSubmit(t *testing.T) {
	app, cleanup := newTestApp(t)
	defer cleanup()

	wiring := newSubmitWiring(app)
	wiring.HandleSubmit(context.Background(), "!!echo excluded-submit")

	pumpUntilBashOutput(t, app, "excluded-submit")
	if _, _, exclude := pumpUntilRecorded(t, app, "echo excluded-submit"); !exclude {
		t.Error("the !! form did not reach the handler as excluded")
	}
}

// `/new` starts a fresh session and says so. The command's NewSession seam was
// never assigned, so it cleared the editor and returned silently.
func TestNewCommandStartsASession(t *testing.T) {
	app, cleanup := newTestApp(t)
	defer cleanup()

	before := app.SessionMgr.GetSessionID()
	app.Commands.HandleClearCommand(context.Background())

	if after := app.SessionMgr.GetSessionID(); after == before {
		t.Errorf("session id unchanged (%q)", after)
	}
	found := false
	for _, text := range transcriptTexts(app) {
		if strings.Contains(text, "New session started") {
			found = true
		}
	}
	if !found {
		t.Errorf("no confirmation in the transcript: %q", transcriptTexts(app))
	}
}
