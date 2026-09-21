# AGENTS.md

Guidance for agents working in this repository. Read this first, then the
README's scoreboard (the authoritative per-file port status).

## What this is

A from-scratch Go port of Mario Zechner's **pi** (upstream:
`https://github.com/earendil-works/pi`), an AI coding agent with a terminal UI.
It is a sibling of the TypeScript monorepo, not a fork: the Go code mirrors the
upstream packages file-for-file.

- Module: `github.com/dat267/pier`, Go `1.27`.
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
| `cmd/pier` | `main.ts` | the CLI entrypoint |
| `chord`, `client`, `protocol`, `server`, `telemetry`, `durable` | same | supporting packages |
| `scripts` | — | catalog generators |

## Build, test, run

```bash
go build ./...                     # everything
go vet ./...                       # must be clean
gofmt -l .                         # must be empty
go test -race -count=2 -timeout 60s ./...   # the completion gate (all packages)

# the CLI (binary derives its display name from the file name)
go build -o bin/pier ./cmd/pier
./bin/pier --help
./bin/pier -r                       # resume the newest session
```

The gate runs with `GOTRACEBACK=all` in CI so a hung test prints every
goroutine. The PTY watchdogs reuse a prebuilt binary via `PIER_TEST_BIN`
(CI builds `./cmd/pier` first); locally they fall back to `./bin/pier`.

The interactive mode reads credentials from `~/.pi/agent/auth.json` (or
`PI_CODING_AGENT_DIR`). `/login` is wired, but the CLI does not install a
browser opener (`interactive.SetBrowserOpener`), so the OAuth browser flow is a
no-op; seed credentials with upstream pi or by hand. `--offline` skips the
catalog refresh.

## Concurrency architecture (UI event loop refactor)

The interactive mode is being moved from mutex-guarded shared state to a
single-writer UI loop (upstream is single-threaded; every Go-side lock was
invented to bridge that gap). Stage 1 has landed:

- **`sessionEventQueue`** (`interactivemode_eventqueue.go`) decouples the
  session-event producers from the UI. Two buffered channels:
  `lossless` (cap 256: message start/end, tool end, agent settled, compaction,
  retries) and `partial` (cap 8: `message_update`, `tool_execution_update`,
  `bash_execution_update`), where partials are latest-wins with an explicit
  drop-oldest policy so a fast token stream cannot back-pressure the agent or
  starve input. The subscription callback only enqueues; producers never touch
  UI state or UI locks. `Close` releases a producer parked on a full channel.
- **`runLoop`** (`interactivemode_run.go`) is the single writer: a `select`
  over the two event channels, the submission channel
  (`StartupWiring.inputs`, cap 64), work completion, and `ctx.Done()`.
  Blocking work (a turn, the compaction-queue flush, which can start a turn)
  runs in its own goroutine through `RunWiring.RunWork`, so a turn's events
  drain while it runs; the loop only accepts submissions when idle, matching
  upstream's awaited prompt. Initial messages are seeded as pending loop work
  ahead of submissions.
- Every channel is buffered; every consumer select has a `ctx.Done()` arm;
  producers blocked on a full channel are released at shutdown.
- **D143** records the divergence: upstream is single-threaded (await +
  microtask order), the port is an explicit select loop with off-loop work and
  partial coalescing.
Stage 3 (input and signals on the loop) has landed:

- **Terminal producers.** The stdin reader delivers complete sequences on
  `loopInputs` (cap 256) and the SIGWINCH watcher posts to `loopResizes`
  (cap 1); the lifecycle's signal handlers post to `loopSignals` (cap 4,
  non-blocking so shutdown signals coalesce). `Renderer.EnableLoopInput` (wired
  in `App.newLoopTui`, so renderer swaps keep it) stops the renderer from
  dispatching input inline; the run loop calls `HandleTerminalInput`, renders
  on resize, and runs the signal shutdown work on its own goroutine.
- The `DrainInput` last-input tracking is an atomic stamp written by the reader
  instead of an `OnData` swap from another goroutine (one lock retired).
- **`/compact` no longer blocks the UI**: the command is split into
  `ClearCompactionStatus` (loop side) and `CompactSession` (off-loop through
  `RunWiring.RunWork`), so the loop keeps dispatching input — including the
  advertised ESC cancel — while the summarization runs.

Stage 2 (rendering on the loop) has landed:

- **`Renderer.EnableRenderTicks`** (`tui/render.go`) switches a renderer from
  its internal timer to a caller-driven mode: `RequestRender` coalesces onto a
  capacity-1 `renderTicks` channel (a channel send, so no callback runs under
  the renderer lock) and no longer arms a timer. The app's renderers enable it
  in `CreateInteractiveTui`. Channel default (timer + throttle) remains for the
  standalone session picker and library users.
- The run loop selects on `UI.RenderTicks()`, drains every already-queued
  session event (`RunWiring.drainReadyEvents`) and paints once
  (`renderUI`), so a burst of N messages produces one render, not N.
- `Renderer.RenderCount()` counts paints (test seam).
- **D144**: the interactive renderer is caller-driven (the loop owns the frame
  schedule) where upstream schedules its own throttled frames.

- Stage 1 also fixed the read side of `coding.SessionManager`
  (`GetEntries`, `BuildContextEntriesForLeaf`, `BuildSessionContext` now take
  `m.mu`; `AppendCompaction`/`GetSessionName` use the locked helpers): those
  accessors were the first legitimate off-thread readers, and the agent writes
  the same state from its own goroutine.

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
- `cmd/pier/main.go` — boots settings/auth/model-runtime/agent-session and runs
  the `App` (port of `main.ts`'s boot).

`tui/render.go` (+ `mainscreen.go`, `altscreen.go`, `terminal.go`,
`stdinbuffer.go`) is the differential renderer core. Its lock discipline is
load-bearing (see below).

## Conventions and gotchas

- **Locking (hard-won).** `tui.Container` methods lock `Container.mu`
  (session-event goroutines mutate the chat/document containers while the
  render timer renders them; upstream is single-threaded). Locks always nest
  parent→child; callbacks/invalidate fan-out is snapshotted under the lock and
  delivered outside it. Never call out to user code / callbacks while holding
  a mutex; the callback may re-enter the same object or trigger shutdown.
  `Render` runs under the render lock and takes component locks; input runs
  under the render lock but not the renderer lock. Known-good patterns:
  snapshot listeners/handlers under the lock, deliver outside it; collect
  emissions and flush after unlock; run `OnSubmit`/`OnData`/`OnPaste` and
  selector callbacks outside component locks. D136–D139 are the concrete bugs
  this caused (editor submit, model selector, terminal↔screen scroll, stdin
  buffer exit). A re-entrancy/lock-order audit is worth re-running after any
  new locking code.
- **PTY watchdogs.** `coding/interactive/ptywatch_test.go` drives the real
  binary through the D136–D139 flows (editor submit, model selector,
  fullscreen scroll, exit) inside a pty, then sends `SIGQUIT` and fails when
  the stack dump shows any goroutine parked in `sync.runtime_SemacquireMutex`.
  The tests use `$PIER_TEST_BIN`, else `./bin/pier`, else they build once into
  a temp dir (and skip if that fails), so the package gate stays fast. CI
  prebuilds the binary and sets `PIER_TEST_BIN`. `TestMutexBlockedDetectorHasTeeth`
  proves the parser still catches a genuine mutex deadlock.

### Lock inventory

Every `sync.Mutex`/`sync.RWMutex` in `tui/` and `coding/interactive/`
(non-test), what it protects, its ordering position, and the UI-loop refactor
stage that retires it. Ordering classes: **(A)** `Renderer.renderMu` serializes
rendering against input dispatch and is the outermost UI lock; **(B)** UI
component/metadata locks nest inside (A), parents before children; **(C)** leaf
state/registry locks never wrap component, renderer or callback calls; **(D)**
independent of the UI locks. The invariant above still holds throughout: no
user code under a lock, snapshot under and deliver outside.

| Lock | Where | Protects | Order | Retirement |
|---|---|---|---|---|
| `Renderer.renderMu` | `tui/render.go` | render vs input dispatch (D84) | A | **stage 3: loop-mode conditional — in loop mode it is never taken (input and painting share the loop goroutine); only the legacy timer mode (session picker/library) locks** |
| `Renderer.mu` | `tui/render.go` | focus, input listeners, posted queue, stopped flag, tick channel | B | stage 3/4 (loop-owned once input and the remaining callers move) |
| `Container.mu` | `tui/component.go:190` | child list + render cache (event goroutines vs render, D136 class) | B, parent→child | stage 1 (events arrive on the loop) |
| `Editor.mu` | `tui/editor.go:70` | buffer, cursor, history (D136) | B | stage 1 (input on the loop) |
| `AltScreen.mu` | `tui/altscreen.go:142` | fullscreen scroll/selection/scrollback (D138) | B | stage 3 (input + tick on the loop) |
| `ScrollView.mu` | `tui/scrollview.go:48` | scroll position/follow-end (D138) | B | stage 3 (auto-scroll ticker → loop message) |
| `Loader.mu` | `tui/selectlist.go:442` | loader animation frames | B | stage 4 (animation timer → loop tick) |
| `AltScreenFlashContainer.mu` | `tui/selectlist.go:705` | flash entries + expiry timers | B | stage 4 (timer → loop message) |
| `StdinBuffer.mu` | `tui/stdinbuffer.go` | sequence assembly, paste re-wrap, Kitty dedup (D51/D139) | C | **stage 3: reduced** — the DrainInput callback swap is gone (the reader stamps an atomic), so the lock now only guards the decoder's own escape/sequence timeout timer; never touches UI state. Stage 4 folds the timer into the reader goroutine (D-row if retained) |
| `ProcessTerminal.mu` | `tui/terminal.go:127` | terminal writes, raw mode, Kitty negotiation, resize bookkeeping | C | **stage 3: writes are loop-owned, but the stdin reader still shares negotiation state → retained; stage 4 D-row candidate (not UI state)** |
| ~~`negotiationResult.lastDataMu`~~ | `tui/terminal.go` | DrainInput's last-input tracking | C | **retired in stage 3** (the reader stamps an atomic timestamp instead of swapping the buffer callback) |
| `ModelSelectorComponent.mu` | `coding/interactive/modelselector.go:59` | selector state + background refresh (D137) | B | stage 4 |
| `ScopedModelsSelectorComponent.mu` | `coding/interactive/scopedmodelsselector.go:257` | scoped-models state + refresh | B | stage 4 |
| `SessionSelectorComponent.mu` | `coding/interactive/sessionselector.go:830` | selector state + queued loader applies (D103) | B | stage 4 |
| `scheduleOnce` local `mu` | `coding/interactive/sessionselector.go:228` | cancelled flag of the auto-cancel timer | D | stage 4 (timer → loop message) |
| `editCallComponent.previewMu` | `coding/interactive/toolrenderers.go:1046` | async edit-preview handoff | B | stage 4 (`TUI.Post`) |
| `FooterComponent.cacheMu` | `coding/interactive/footer.go:208` | footer render cache (D141) | C | stage 4 (invalidations on the loop) |
| `ModelCatalogRefreshCoordinator.mu` | `coding/interactive/catalogrefresh.go:34` | per-runtime refresh dedup (D98) | C | stage 4 |
| `activeCatalogRefresh.mu` | `coding/interactive/catalogrefresh.go:22` | one refresh's result/cancel state | C | stage 4 |
| `Lifecycle.mu` | `coding/interactive/interactivemode_lifecycle.go:107` | shutdown/suspend/lifecycle flags (D135) | C | stage 3/4 |
| ~~`StartupWiring.mu`~~ | `coding/interactive/interactivemode_startup.go` | pending user inputs + telemetry-once flag (D123) | C | **retired in stage 1** (input handoff is a channel) |
| `Theme.mu` (style colors) | `coding/interactive/theme.go:100` | style-color enable flag (test seam) | C | stage 4 |
| `themeState.mu` | `coding/interactive/theme.go:802` | global theme registry + watcher (D85) | C | stage 4 |
| `trueColorState.mu` | `coding/interactive/theme.go:1019` | truecolor capability (test seam) | C | stage 4 |
| `customThemesDirState.mu` | `coding/interactive/theme.go:1041` | custom themes dir (test seam) | C | stage 4 |
| `KeybindingsManager.mu` | `tui/keybindings.go:142` | user override definitions | C | stage 4 |
| `globalKeybindingsState.mu` | `tui/keybindings.go:340` | global manager accessor (`SetKeybindings` seam) | C | stage 4 |
| `kittyProtocolState.mu` | `tui/keys.go:21` | global Kitty active flag | C/D | stage 4 |
| `lastEventTypeState.mu` | `tui/keys.go:342` | last parsed key event type (fidelity port) | D | stage 4 |
| `widthCacheMu` | `tui/width.go:471` | memoized `VisibleWidth` cache | C | stage 4 (loop-confined; D-row if non-UI callers remain) |

Stage 4's target is that this table is empty (or reduced to D-rowed exceptions
where a shared non-UI caller genuinely requires a lock).
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
- D133 — tool renderers always resolve to the built-in set (no extension
  definitions); `computeEditsPreview` is synchronous.
- D140 — the user's provider extensions (hyper, commandcode) are compiled in as
  `providers.ExtensionProviders()` instead of being loaded from
  `~/.pi/agent/extensions` (extension loading is out of scope).
- D141 — the builtin footer renders the user's footer-extension format (one dim
  line `3.5%/1M · <statuses> · model · cwd`) instead of upstream's two-line
  footer: upstream's render walks every session entry per frame, which in Go
  means a json.Unmarshal per message per frame (O(session size); measured 445ms
  per frame on a 10k-entry session). The footer render is cached and invalidated
  on every session event; FormatTokens/FormatCwdForFooter keep upstream
  behavior. app.UI is the TuiReference forwarder (D105) so the /tui and
  exit-replay renderer swaps reach every holder.
- D142 — the compaction summarizer follows the user's compaction extension
  (~/.pi/agent/extensions/compaction) instead of stock: a 32k no-reasoning
  call with the pi-better-compact structured prompts, the previous summary's
  file lists stripped before it is fed back, regenerated file lists capped to
  the most recent 40 per list, the PI_COMPACT_MODEL override and opencode
  routing headers. On any failure or an unusable summary it falls back to the
  stock summarizer.
- D44/D47 — Go uses function fields instead of overridable methods; listeners
  are compared by code pointer.
- D51 — `StdinBuffer` uses callbacks instead of `EventEmitter`.
- D71 — the editor requests autocomplete synchronously (upstream debounces).
- D83 — `DisableAutoRender` test seam for the Go render timer.
- D90 — narrow runtime interfaces for testability across the wirings.
- D105/D106 — the ES `Proxy` renderer reference becomes an explicit forwarder;
  clipboard copying is injected (native clipboard out of scope).
- D121/D123/D135 — cross-goroutine state made mutex-safe (model refresh,
  startup input/telemetry, lifecycle flags, footer watcher). D123's input lock
  is retired in stage 1 (the submission handoff is a buffered channel); the
  others retire in stages 3-4.
- D132 — branch summarization is tracked as compaction and abortable.
- D145 — terminal input, resize and process signals reach the UI loop as
  channel messages from pure producers (the stdin reader, the SIGWINCH
  watcher, the signal handlers); the loop dispatches input, paints on resize
  and runs the shutdown work on its own goroutine, where upstream delivers
  these on the single JS thread.
- D144 — the interactive renderer runs in caller-driven tick mode (the run
  loop coalesces render requests and paints once per drain) instead of
  arming its own throttled render timer; the standalone session picker and
  library users keep the timer.
- D143 — the interactive run loop is an explicit `select` over producer
  channels (session events, submissions, work completion, `ctx.Done()`) with
  blocking work off-loop and latest-wins coalescing for streaming partials,
  where upstream is single-threaded and awaits the prompt; the stage-3 to
  stage-4 stages move the remaining producers (input/signals, selector and
  lifecycle callbacks) onto the same loop.
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
(`cmd/pier`, originally `cmd/pi`) and its composition layer (`app.go`) came last,
followed by four deadlock fixes found by driving the real binary in a PTY:
D136 (editor submit), D137 (model selector), D138 (scroll), D139 (exit). The
executable was then renamed from `pi` to `gpi` and finally to `pier` (the display name follows the
invoked file name).

To reproduce the exit/scroll checks, drive `bin/pier` under a PTY, exercise the
flow, then send `SIGQUIT` to the process and look for goroutines blocked on
`sync.Mutex`.
