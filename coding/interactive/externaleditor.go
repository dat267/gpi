package interactive

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// Port of src/modes/interactive/external-editor.ts: edit the prompt in an
// external editor.
//
// Divergences: Go writes the file and waits for the child process (upstream
// avoids spawnSync on Windows for console-input reasons that do not apply to
// Go) (D77).
//
// The child inherits this process's console (os.Stdin/os.Stdout/os.Stderr) and
// its environment, which is the point when a user pressed ctrl+g and the wrong
// thing inside a test: it launches the developer's editor. It is worse on
// Windows, where the spawn is `cmd /c <command> <tempfile>` and a command that
// cannot be executed hands the temp file to the shell's association fallback
// instead of failing. Tests stub externalEditorRunner for that reason.

// ExternalEditorOptions configure the external editor invocation.
type ExternalEditorOptions struct {
	Command string
	Content string
}

// ExternalEditorResult is either the edited content or a failure.
type ExternalEditorResult struct {
	Status  string // "complete" | "failed"
	Content string
}

// externalEditorRunner runs the editor, blocking until it exits. It is a seam:
// the real runner hands this process's console and environment to the developer's
// $EDITOR, which must not happen inside a test run (see the note above), so tests
// replace it with a stub (stubExternalEditorRunner). The package's tests do not
// run in parallel, so a swap cannot race.
var externalEditorRunner = func(command *exec.Cmd) error { return command.Run() }

// EditInExternalEditor runs $EDITOR (or the configured command) on a temporary
// prompt file and returns the edited content.
func EditInExternalEditor(options ExternalEditorOptions) ExternalEditorResult {
	directory, err := os.MkdirTemp("", "pi-editor-")
	if err != nil {
		return ExternalEditorResult{Status: "failed"}
	}
	filePath := filepath.Join(directory, "prompt.md")
	defer func() {
		_ = os.RemoveAll(directory)
	}()

	if err := os.WriteFile(filePath, []byte(options.Content), 0o644); err != nil {
		return ExternalEditorResult{Status: "failed"}
	}
	_, _ = os.Stdout.WriteString("Launching external editor: " + options.Command +
		"\nPi will resume when the editor exits.\n")

	parts := strings.Split(options.Command, " ")
	editor := parts[0]
	args := append(append([]string(nil), parts[1:]...), filePath)
	if editor == "" {
		return ExternalEditorResult{Status: "failed"}
	}

	command := exec.Command(editor, args...)
	command.Stdin = os.Stdin
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if runtime.GOOS == "windows" {
		// Upstream spawns through a shell on Windows.
		command = exec.Command("cmd", "/c", options.Command, filePath)
		command.Stdin = os.Stdin
		command.Stdout = os.Stdout
		command.Stderr = os.Stderr
	}
	if err := externalEditorRunner(command); err != nil {
		return ExternalEditorResult{Status: "failed"}
	}

	data, err := os.ReadFile(filePath)
	if err != nil {
		return ExternalEditorResult{Status: "failed"}
	}
	content := strings.TrimSuffix(stripBOMString(string(data)), "\n")
	return ExternalEditorResult{Status: "complete", Content: content}
}

func stripBOMString(value string) string {
	return strings.TrimPrefix(value, "\uFEFF")
}
