// Command pier is the Go port's interactive coding-agent CLI.
//
// It is a thin wrapper: the CLI itself lives in the cmd package (boot,
// flags, session resolution, run), so the module root is installable —
// `go build -o bin/pier .`, `go install github.com/dat267/pier@latest` —
// the layout github.com/dat267/min uses.
package main

import "github.com/dat267/pier/cmd"

func main() {
	cmd.Execute()
}
