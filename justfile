# The Go port of pi (module github.com/dat267/pier).
#
# The CLI is the module root (a thin main.go) over the cmd package, so
# `go build .` and `go install .` both work — `install` wraps the latter.
#
# `just --list` shows the recipes; the default is `build`.

set shell := ["bash", "-euo", "pipefail", "-c"]

module := "github.com/dat267/pier"
bin := "bin/pier"

# The CLI prints this for --version and uses it for changelog comparisons, so it
# is left at the source default unless you pass VERSION — either way works:
# `just VERSION=1.2.3 install` or `VERSION=1.2.3 just install`. The release
# workflow stamps it the same way (coding.Version).
VERSION := env_var_or_default("VERSION", "")

# Pure Go: no cgo anywhere in the port, and the release workflow builds the same
# way (GOOS=android CGO_ENABLED=0).
ldflags := "-trimpath -ldflags \"-s -w" + (if VERSION == "" { "" } else { " -X " + module + "/coding.Version=" + VERSION }) + "\""

# Build bin/pier.
build:
	@mkdir -p bin
	CGO_ENABLED=0 go build {{ldflags}} -o {{bin}} .
	@echo "built {{bin}}"

# Install the CLI into GOBIN (or GOPATH/bin) and warn if that is not on PATH.
install:
	#!/usr/bin/env bash
	set -euo pipefail
	CGO_ENABLED=0 go install {{ldflags}} .
	dir="$(go env GOBIN)"
	if [[ -z "$dir" ]]; then dir="$(go env GOPATH)/bin"; fi
	echo "installed $dir/pier"
	case ":$PATH:" in *":$dir:"*) ;; *) echo "note: $dir is not on PATH" ;; esac

# Run the test suite.
test:
	go test ./...

# Run the test suite under the race detector (not available on android/arm64 — CI runs this).
test-race:
	go test -race ./...

# Report formatting differences.
fmt:
	#!/usr/bin/env bash
	set -euo pipefail
	out="$(gofmt -l .)"
	if [[ -n "$out" ]]; then echo "$out"; exit 1; fi

# Run go vet.
vet:
	go vet ./...

# What CI runs, minus -race.
check: fmt vet test

# Remove build output.
clean:
	rm -rf bin