# pi (Go) — a faithful port of [pi](https://github.com/earendil-works/pi)

This is a from-scratch Go port of Mario Zechner's [pi](https://github.com/earendil-works/pi)
(`@earendil-works/pi-ai`, `pi-agent-core`, `pi-coding-agent`). The TypeScript
monorepo is **ground truth**: every type, field name, and behavior is ported
from upstream source, not inferred. The porting rules live in `AGENTS.md`.

## Upstream pin

| What | Value |
|---|---|
| Repository | https://github.com/earendil-works/pi (cloned at `../pi`) |
| Pin | `16787ad5b` — Release v0.87.0 |
| Stale upstream test | `packages/ai/test/faux-provider.test.ts` "estimates prompt and output tokens" still expects pre-`9e05370b2` faux serialization; source wins |

## Status

`docs/PORTING.md` is the authoritative per-area table — what is ported,
partially ported or out of scope, file by file — plus the pin's change log.
`docs/DIVERGENCES.md` logs the numbered D-rows, the places this port knowingly
differs from upstream.

Every upstream runtime package is accounted for: `ai` (all ten wire APIs, all provider factories, the seven OAuth flows, image generation, the api/images registries, overflow detection), `agent` (the loop), `coding-agent` (the complete core: session, tools, compaction, branch summarization, retry, cache warming, model runtime/registry/composer, stores, exports, bug report, crash log, trust, utilities, and the createAgentSession assembly), `protocol`, `client`, `chord` (+delta/services/facets), `server` (+unix/testing), `telemetry`, and `durable`.

The `pi-tui` library and the interactive coding-agent mode are ported (`tui/*` and `coding/interactive/*`), including the theme, every component, and the interactive-mode method groups; the root `main.go` and the `cmd` package compose them into a runnable CLI. The remaining items are the documented out-of-scope set below.

Deliberately out of scope, with divergences recorded in code: the extension mechanics (resource loader, sdk extension surface, agent-session services/runtime, package and tools managers), the native clipboard, the kitty/iterm image transport internals, the node-specific chord bundler, the bug-report upload transport, and the optional `session-backends` sqlite driver (a host-provided storage backend imported by no runtime package) plus the `evals` Docker harness (a development tool).

## Build & test

```bash
just build   # bin/pier: pure Go (CGO_ENABLED=0), the flags the release workflow uses
just check   # gofmt + go vet + go test
just --list  # the other recipes (install, test-race, clean)
```

Plain Go works too. The CLI is the module root — `main.go` is a thin wrapper
over the `cmd` package, which holds the boot — so `go install .` works:

```bash
go build -o bin/pier .
go test ./...
```

CI runs `go test -race ./...`, but **the race detector cannot run on
android/arm64** — Go rejects it outright (`-race is not supported on
android/arm64`) — so `just test-race` only fails on Termux. That check is
CI-only, and it is the only one that cannot be reproduced locally.

## Install the CLI

```bash
just install                 # into $(go env GOBIN), or $(go env GOPATH)/bin
just VERSION=1.2.3 install   # stamps --version and the changelog comparison
```

`just install` prints where it landed and warns if that directory is not on
`PATH`. It is a thin wrapper over `go install .`, which you can run
directly if you prefer. The binary derives its display name from its own file
name, so a symlink named `pi` would show `pi`.

Then run `pier`. `pier --help` lists the flags; `pier --version` prints the version. The CLI supports the interactive mode, resume (`-c` continues the newest session; `-r` opens the interactive session picker; `--session` accepts a file path, a session id, an id prefix, or matches globally across projects; `--session-id` opens a matching session or creates a new one with that id; `--fork` forks a session into a new one in the current cwd), model selection (`-m`, `-p`), `--offline`, `--tui-mode` and initial prompts. The print/json/rpc modes, package manager, extensions and migrations are not wired.
