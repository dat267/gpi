package coding

import (
	"strings"
	"testing"
)

// Upstream's help lists seven subcommands (install, remove, uninstall, update,
// list, config, auth). Only `auth` is implemented here: the package manager and
// the extension mechanics are out of scope (D41), and the resource-manager TUI
// went with them. A subcommand the parser does not know is not an error either:
// it becomes the first message to the model, so advertising the missing ones
// would tell the user to run commands that do not exist.
func TestHelpAdvertisesOnlyImplementedSubcommands(t *testing.T) {
	help := PrintHelp()
	if !strings.Contains(help, "Commands:") {
		t.Fatalf("help has no command table:\n%s", help)
	}
	if !strings.Contains(help, "auth <command>") {
		t.Fatalf("help does not advertise the auth command:\n%s", help)
	}
	for _, ghost := range []string{"install <source>", "remove <source>", "uninstall <source>", "update [source", "config [-l]"} {
		if strings.Contains(help, ghost) {
			t.Errorf("help mentions %q", ghost)
		}
	}
	// Nothing in the help claims an extension or package facility (D41).
	if strings.Contains(strings.ToLower(help), "extension") {
		t.Errorf("help mentions extensions:\n%s", help)
	}
	// The parts that are real stay.
	for _, kept := range []string{"Usage:", "--offline", "--list-models", "Built-in Tool Names:"} {
		if !strings.Contains(help, kept) {
			t.Errorf("help lost %q:\n%s", kept, help)
		}
	}
}

// -e/--extension and -ne/--no-extensions stay parsed — an -e path must not become
// the first message to the model — but this build loads no extensions, so -e says
// so instead of silently doing nothing. --no-extensions asks for less of
// something that does not exist, so it stays quiet.
func TestExtensionFlagsAreReported(t *testing.T) {
	parsed := ParseArgs([]string{"-e", "my-extension.ts"})
	if len(parsed.Extensions) != 1 || parsed.Extensions[0] != "my-extension.ts" {
		t.Fatalf("extensions = %v", parsed.Extensions)
	}
	if len(parsed.Messages) != 0 {
		t.Errorf("the extension path became a message: %v", parsed.Messages)
	}
	warnings := 0
	for _, diagnostic := range parsed.Diagnostics {
		if diagnostic.Type == "warning" {
			warnings++
		}
	}
	if warnings != 1 {
		t.Errorf("diagnostics = %+v, want one warning", parsed.Diagnostics)
	}
	// A missing value is an error, like the neighbouring flags.
	missing := ParseArgs([]string{"--extension"})
	if !HasErrorDiagnostics(missing.Diagnostics) {
		t.Errorf("--extension without a value must be an error: %+v", missing.Diagnostics)
	}
	// --no-extensions is accepted without complaint.
	quiet := ParseArgs([]string{"-ne"})
	if len(quiet.Diagnostics) != 0 || !quiet.NoExtensions {
		t.Errorf("--no-extensions = %+v, %v", quiet.Diagnostics, quiet.NoExtensions)
	}
	// Neither flag is advertised any more.
	for _, ghost := range []string{"--extension, -e", "--no-extensions, -ne"} {
		if strings.Contains(PrintHelp(), ghost) {
			t.Errorf("help still lists %q", ghost)
		}
	}
}
