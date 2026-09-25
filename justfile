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

# The Unix variant uses the global bash shell; the Windows variant is
# PowerShell, so install works there without a POSIX shell on PATH.
#
# Install the CLI into GOBIN (or GOPATH/bin) and warn if that is not on PATH.
[unix]
install:
	#!/usr/bin/env bash
	set -euo pipefail
	CGO_ENABLED=0 go install {{ldflags}} .
	dir="$(go env GOBIN)"
	if [[ -z "$dir" ]]; then dir="$(go env GOPATH)/bin"; fi
	echo "installed $dir/pier"
	case ":$PATH:" in *":$dir:"*) ;; *) echo "note: $dir is not on PATH" ;; esac

[windows]
install:
	#!powershell
	$ErrorActionPreference = "Stop"
	$env:CGO_ENABLED = "0"
	$ld = "-s -w"
	$version = '{{VERSION}}'
	if ($version) { $ld = "$ld -X github.com/dat267/pier/coding.Version=$version" }
	$goArgs = @("install", "-trimpath", "-ldflags", $ld, ".")
	& go @goArgs
	$dir = (& go env GOBIN | Out-String).Trim()
	if (-not $dir) { $dir = Join-Path ((& go env GOPATH | Out-String).Trim()) "bin" }
	Write-Output ("installed " + (Join-Path $dir "pier.exe"))
	$target = $dir.TrimEnd('\', '/')
	$onPath = $false
	foreach ($entry in ($env:PATH -split ';')) {
		if ($entry.Trim().TrimEnd('\', '/') -ieq $target) { $onPath = $true; break }
	}
	if (-not $onPath) { Write-Output ("note: " + $dir + " is not on PATH") }

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

# What CI runs, minus -race (the test and cross-build jobs).
check: fmt vet test

# Cross-build and cross-vet every OS the port targets (mirrors the CI matrix).
cross:
	#!/usr/bin/env bash
	set -euo pipefail
	for pair in windows/amd64 darwin/arm64 darwin/amd64 linux/arm64 \
		freebsd/amd64 openbsd/amd64 netbsd/amd64 dragonfly/amd64 solaris/amd64 aix/ppc64; do
		echo "$pair"
		CGO_ENABLED=0 GOOS="${pair%/*}" GOARCH="${pair#*/}" go build ./...
		CGO_ENABLED=0 GOOS="${pair%/*}" GOARCH="${pair#*/}" go vet ./...
	done

# Remove build output.
clean:
	rm -rf bin