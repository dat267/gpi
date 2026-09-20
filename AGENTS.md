# AGENTS.md

Guidance for agents working in this repository. Read this first, then the
README's scoreboard (the authoritative per-file port status).

## What this is

A from-scratch Go port of Mario Zechner's **pi** (upstream:
`https://github.com/earendil-works/pi`), an AI coding agent with a terminal UI.
It is a sibling of the TypeScript monorepo, not a fork: the Go code mirrors the
upstream packages file-for-file.

- Module: `github.com/dat267/gpi`, Go `1.27`.
- Dependencies are offline-cached only: `golang.org/x/text`, `x/term`, `x/sys`.
- Upstream checkout used while porting: `/tmp/pi` (see the README's pin table).
- License: MIT (see `LICENSE`).

## Ground-truth rules

1. **Upstream is the spec.** A port commit references the upstream file it
   mirrors. Never infer behavior; read the TypeScript source.
2. **JSON is byte-parity.** Session files, transcripts and wire payloads must
   round-trip identically: same key names, same key order, no HTML escaping.
   Use `ai.MarshalJSON`, never `encoding/json.Marshal`, for anything persisted
   or sent. JS object insertion order is tracked explicitly where it matters.
3. **JS semantics where pi relies on them.** Object insertion order,
   `JSON.stringify` behavior, truthiness at API boundaries. Divergences get a
   numbered **D-row** in a code comment and a README scoreboard note.
4. **Every behavior change is test-observed.** Tests mirror upstream `test/`
   files where they exist; Go-only tests assert the upstream-derived
   expectation and cite the upstream source in a comment.

## Layout

Packages mirror upstream `packages/`:

| Go package | Upstream | Notes |
|---|---|---|
| `ai` | `packages/ai` | models, wire APIs, providers, OAuth, images |
| `agent` | `packages/agent` | the agent loop and stateful Agent |
| `coding` | `packages/coding-agent` core | session, tools, compaction, settings, model runtime |
| `coding/interactive` | `packages/coding-agent` interactive mode | components, theme, the mode wirings |
| `tui` | `packages/pi-tui` | terminal abstraction, renderer, components |
| `cmd/gpi` | `main.ts` | the CLI entrypoint |
| `chord`, `client`, `protocol`, `server`, `telemetry`, `durable` | same | supporting packages |
| `scripts` | — | catalog generators |

## Build, test, run

```bash
go build ./...                     # everything
go vet ./...                       # must be clean
gofmt -l .                         # must be empty
go test -race -count=2 ./...       # the completion gate (all 15 packages)

# the CLI (binary derives its display name from the file name)
go build -o bin/gpi ./cmd/gpi
./bin/gpi --help
./bin/gpi -r                       # resume the newest session
```

The interactive mode reads credentials from `~/.pi/agent/auth.json` (or
`PI_CODING_AGENT_DIR`). `/login` is wired, but the CLI does not install a
browser opener (`interactive.SetBrowserOpener`), so the OAuth browser flow is a
no-op; seed credentials with upstream pi or by hand. `--offline` skips the
catalog refresh.

## Architecture of the interactive mode

Upstream `interactive-mode.ts` is one ~4000-line class. The Go port splits it
into narrow, injectable wirings (all in `coding/interactive`):

- `app.go` — **the composition layer** (port of the `InteractiveMode`
  constructor). It builds the containers, editor, footer, transcript, event
  dispatcher, queue, selector slot, lifecycle, and every wiring, then exposes
  `App.Init`/`App.Run`.
- `interactivemode_lifecycle.go` — mount/stop, renderer swap, signals, shutdown.
- `interactivemode_run.go` / `_startup.go` — init, the main input loop, helpers.
- `interactivemode_handlers.go` — key wiring (`KeyWiring`) and submit dispatch
  (`SubmitWiring`, the full slash-command chain).
- `interactivemode_selectors.go`, `_settings.go`, `_models.go`, `_auth.go`,
  `_commands.go`, `_queue.go`, `_events.go`, `_ui.go` — the named surfaces.
- `transcript.go`, `footer.go`, `customeditor.go`, `tuirenderer.go`,
  `extensionsselector.go`, `sessionshare.go`, `interactivemode_helpers.go`.
- `cmd/gpi/main.go` — boots settings/auth/model-runtime/agent-session and runs
  the `App` (port of `main.ts`'s boot).

`tui/render.go` (+ `mainscreen.go`, `altscreen.go`, `terminal.go`,
`stdinbuffer.go`) is the differential renderer core. Its lock discipline is
load-bearing (see below).

## Conventions and gotchas

- **Locking (hard-won).** Never call out to user code / callbacks while holding
  a mutex; the callback may re-enter the same object or trigger shutdown.
  `Render` runs under the render lock and takes component locks; input runs
  under the render lock but not the renderer lock. Known-good patterns:
  snapshot listeners/handlers under the lock, deliver outside it; collect
  emissions and flush after unlock; run `OnSubmit`/`OnData`/`OnPaste` and
  selector callbacks outside component locks. D136–D139 are the concrete bugs
  this caused (editor submit, model selector, terminal↔screen scroll, stdin
  buffer exit). A re-entrancy/lock-order audit is worth re-running after any
  new locking code.
- **Go 1.27 quirk.** Function literals passed as arguments need an explicit
  result type when the parameter's function type has one.
- **RE2 regex** (no lookaround/backreferences). Put `-` first in a character
  class; `\s` is invalid inside `[...]`.
- **JSON struct tags** are required for field-name parity with upstream.
- **`range` is a keyword**; avoid it as an identifier.
- **`filepath.Rel`** returns `"."` for equal paths (Node returns `""`);
  `path.Clean` drops trailing slashes (`path.posix.normalize` keeps them).
- **Nil vs empty slices**: normalize in render comparisons.
- **Test seams already wired**: `SetCustomThemesDir`, `SetRegisteredThemes`,
  `tui.SetKeybindings`, `SetSessionFileDeleter`, `SetClipboardCopier`,
  `SetBrowserOpener`, `coding.SetPackageDir`. The interactive test screens call
  `DisableAutoRender()` so tests can drive rendering deterministically.
- **Goldens**: `tui/testdata/`, `coding/interactive/testdata/`,
  `coding/testdata/` hold `label => JSON` lines generated by driving the
  upstream TypeScript with Node; Go replays the same scripted input.

## Divergences

Numbered D-rows live in code comments at the point of divergence and are
summarized in the README scoreboard. The range is **D1–D139**. Representative:

- D41 — extension mechanics are out of scope (resource loader, extension
  runner, package/tools managers); seams are function values or return nil.
- D44/D47 — Go uses function fields instead of overridable methods; listeners
  are compared by code pointer.
- D51 — `StdinBuffer` uses callbacks instead of `EventEmitter`.
- D71 — the editor requests autocomplete synchronously (upstream debounces).
- D83 — `DisableAutoRender` test seam for the Go render timer.
- D90 — narrow runtime interfaces for testability across the wirings.
- D105/D106 — the ES `Proxy` renderer reference becomes an explicit forwarder;
  clipboard copying is injected (native clipboard out of scope).
- D121/D123/D135 — cross-goroutine state made mutex-safe (model refresh,
  startup input/telemetry, lifecycle flags, footer watcher).
- D132 — branch summarization is tracked as compaction and abortable.
- D136 — editor `OnSubmit` runs outside the editor lock.
- D137 — model-selector callbacks run outside the state mutex.
- D138 — terminal input is delivered outside the terminal lock.
- D139 — stdin-buffer callbacks are emitted outside the buffer lock.

## Out of scope (documented)

Native clipboard, kitty/iterm image transport internals beyond the
line-detection helpers, extension mechanics, the package manager, and the
print/json/rpc modes. Mermaid rendering needs `grok-mermaid` and is inert.

## Session history (high level)

The repository was built as a long port: the `ai`/`agent`/`coding` cores first,
then the `pi-tui` library (renderer, layout, terminals, keys, markdown,
components), then the interactive coding-agent mode (theme, every component,
and the mode method groups), each round verified against upstream Node goldens
and committed locally with a README scoreboard update. The CLI
(`cmd/gpi`, originally `cmd/pi`) and its composition layer (`app.go`) came last,
followed by four deadlock fixes found by driving the real binary in a PTY:
D136 (editor submit), D137 (model selector), D138 (scroll), D139 (exit). The
executable was then renamed from `pi` to `gpi` (the display name follows the
invoked file name).

To reproduce the exit/scroll checks, drive `bin/gpi` under a PTY, exercise the
flow, then send `SIGQUIT` to the process and look for goroutines blocked on
`sync.Mutex`.
