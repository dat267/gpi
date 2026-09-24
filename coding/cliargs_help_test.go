package coding

import (
	"strings"
	"testing"
)

// Upstream's help lists seven subcommands (install, remove, uninstall, update,
// list, config, auth). This port implements none of them — the package manager
// and the extension mechanics are out of scope (D41), and the resource-manager
// TUI went with them — and a subcommand the parser does not know is not an error
// either: it becomes the first message to the model. Advertising them told the
// user to run commands that do not exist.
func TestHelpDoesNotAdvertiseSubcommands(t *testing.T) {
	help := PrintHelp()
	if strings.Contains(help, "Commands:") {
		t.Fatalf("help advertises a command table the port has no commands for:\n%s", help)
	}
	for _, ghost := range []string{"install <source>", "remove <source>", "uninstall <source>", "update [source", "config [-l]", "auth <command>"} {
		if strings.Contains(help, ghost) {
			t.Errorf("help mentions %q", ghost)
		}
	}
	// The parts that are real stay.
	for _, kept := range []string{"Usage:", "--offline", "--list-models", "Built-in Tool Names:"} {
		if !strings.Contains(help, kept) {
			t.Errorf("help lost %q:\n%s", kept, help)
		}
	}
}
