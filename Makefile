## The Go port of pi (module github.com/dat267/pier).
##
## There is no package at the repository root: the CLI is ./cmd/pier, so
## `go install .` does not work — use `make install`, or
## `go install ./cmd/pier` directly.

MODULE  := github.com/dat267/pier
CMD     := ./cmd/pier
BIN     := bin/pier

# The CLI prints this for --version and uses it for changelog comparisons, so it
# is left at the source default unless you pass VERSION. The release workflow
# stamps it the same way (`-X $(MODULE)/coding.Version=...`).
VERSION ?=
LDFLAGS := -trimpath -ldflags "-s -w$(if $(VERSION), -X $(MODULE)/coding.Version=$(VERSION),)"
# Pure Go: no cgo anywhere in the port, and the release workflow builds the same
# way (GOOS=android CGO_ENABLED=0).
BUILD   := CGO_ENABLED=0 go build $(LDFLAGS)

.DEFAULT_GOAL := build

.PHONY: help build install test test-race fmt vet check clean

help: ## Show the targets
	@grep -E '^[a-z-]+:.*?## ' $(MAKEFILE_LIST) | sed 's/:.*## / — /' | sort

build: ## Build bin/pier
	@mkdir -p bin
	$(BUILD) -o $(BIN) $(CMD)
	@echo "built $(BIN)"

install: ## go install the CLI into GOBIN (or GOPATH/bin)
	@CGO_ENABLED=0 go install $(LDFLAGS) $(CMD)
	@dir=$$(go env GOBIN); if [ -z "$$dir" ]; then dir=$$(go env GOPATH)/bin; fi; \
	 echo "installed $$dir/pier"; \
	 case ":$$PATH:" in *":$$dir:"*) ;; *) echo "note: $$dir is not on PATH";; esac

test: ## Run the test suite
	go test ./...

test-race: ## Run the test suite under the race detector (not available on android/arm64 — CI runs this)
	go test -race ./...

fmt: ## Report formatting differences
	@out=$$(gofmt -l .); if [ -n "$$out" ]; then echo "$$out"; exit 1; fi

vet: ## Run go vet
	go vet ./...

check: fmt vet test ## What CI runs, minus -race

clean: ## Remove build output
	rm -rf bin
