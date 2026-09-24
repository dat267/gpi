# AGENTS.md

Guidance for agents working in this repository. Read this first; the
authoritative per-area port status is `docs/PORTING.md`, the divergence log is
`docs/DIVERGENCES.md`, and the README covers install and usage.

## What this is

A from-scratch Go port of Mario Zechner's **pi** (upstream:
`https://github.com/earendil-works/pi`), an AI coding agent with a terminal UI.
It is a sibling of the TypeScript monorepo, not a fork: the Go code mirrors the
upstream packages file-for-file.

- Module: `github.com/dat267/pier`, Go `1.27`.
- Dependencies are offline-cached only: `golang.org/x/text`, `x/term`, `x/sys`.
- Upstream checkout used while porting: `/tmp/pi` at tag `v0.87.0`
  (`16787ad5b`; see the README's pin table). The installed 0.86.1 bundle and the
  old pin `36b60d2e8` are on divergent upstream lines (no common ancestor), so
  the released tags are the reference; 0.87 diff checks go through `/tmp/pi`
  sources (`node --experimental-strip-types`, `FORCE_COLOR=1` for chalk parity).
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
   numbered **D-row** in a code comment and an entry in `docs/DIVERGENCES.md`.
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
| `main.go`, `cmd` | `main.ts` | the CLI entrypoint (a thin root main over the `cmd` package) |
| `chord`, `client`, `protocol`, `server`, `telemetry`, `durable` | same | supporting packages |
| `scripts` | — | catalog generators |
| `docs` | — | port status (`PORTING.md`) and the divergence log (`DIVERGENCES.md`) |

## Build, test, run

```bash
go build ./...                     # everything
go vet ./...                       # must be clean
gofmt -l .                         # must be empty
go test -race -count=2 -timeout 60s ./...   # the completion gate (all packages)

# the CLI (binary derives its display name from the file name)
go build -o bin/pier .
./bin/pier --help
./bin/pier -r                       # resume the newest session
```

A `justfile` wraps the commands above (`just`, `just check`, `just --list`;
`just VERSION=1.2.3 install` stamps the version `make install` used to, and the
recipe names match the old Makefile targets). The build stays plain Go, so the
gate runs the `go` commands directly.

The module root is the command: `main.go` is a thin wrapper over
`cmd.Execute`, and the `cmd` package holds the flags, boot and session
resolution. That makes `go install github.com/dat267/pier@latest` work and
matches the layout of `github.com/dat267/min`.

The gate runs with `GOTRACEBACK=all` in CI so a hung test prints every
goroutine. The PTY watchdogs reuse a prebuilt binary via `PIER_TEST_BIN`
(CI builds `.` first); locally they fall back to `./bin/pier`.

Keep the suite quick — the race detector slows every path 5-10x, and the gate
runs each test twice (`-p 8` overlaps the test-binary builds; the whole gate is
~50 s, `-race -count=1` ~30 s on four cores):

- Size fixtures to the *property*, not to production scale. The projection tests
  assert ratios (window vs session, warm memo vs cold), so a few hundred
  message pairs discriminate as well as a few thousand; at 60k appends one test
  cost 44 s under `-race` on its own. Do not grow a fixture back without
  re-measuring the package.
- Avoid `testing.Benchmark` in tests: it has a hard one-second floor per
  measurement, so two scaling tests paid ~5 s of pure timer wait. Use
  `allocatedBytesPerRun` (`coding/allocmeasure_test.go`) for bytes and
  `testing.AllocsPerRun` for counts.
- Never sleep a spec-mandated interval in a test. RFC 8628's device-code poll
  interval is a whole second, which made the OAuth tests spend ~25 s asleep;
  they swap `deviceCodeSleep` (`ai/oauthpkce.go`) for a millisecond instead.
- `-short` skips the PTY watchdogs (they wait on real rendering).

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
  producers blocked on a full channel are released by the run context's
  cancellation or by the queue's `Close` (the terminal reader and event
  subscribers never receive a context, so both arms are present).
- **Slow-phase logging is on by default at 100 ms**; `PIER_STALL_MS=<ms>`
  overrides the threshold and `PIER_STALL_MS=0` disables it. Every phase the
  loop measures (`render`, `events`, `event-apply`, `partial-event-apply`, `beat`, `input`,
  `raw-input`) that
  exceeds the threshold is recorded to `<agentDir>/pier-stall.log`, each with a
  goroutine dump. It exists for stutters that do not reproduce on the
  development machine: a record names the phase and the stacks instead of only
  how long it took. A phase still running at the threshold is also recorded
  mid-flight by a timer goroutine, whose dump shows where the loop was — the
  after-phase dump only shows the loop back in its event loop. Heavy tool-side
  commands (test suites, builds) run `nice`d: on a 4-CPU machine an ungated
  test run competes with the UI goroutine and looks like a freeze. It was opt-in first, but both post-fix freezes struck
  sessions started without the variable, so the default flipped — a capture
  that requires remembering to set an env var does not survive real usage.
- The loop calls a **watchdog beat** once per iteration
  (`RunWiring.LoopBeats`, exposed as `App.LoopBeats`): a stalled loop stops
  advancing it, so a watchdog can detect a hang.
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
  schedule) where upstream schedules its own throttled frames. The loop keeps
  upstream's 16 ms frame throttle for coalesced render ticks, so a streaming
  delta burst cannot paint back-to-back; input, resize and animation paints
  stay immediate.

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
- `main.go` (thin) and `cmd/main.go` — the `cmd` package boots
  settings/auth/model-runtime/agent-session and runs the `App` (port of
  `main.ts`'s boot); the root file only calls `cmd.Execute`.
- Each `*Wiring` has a constructor next to its struct (`newCommandWiring(app)`,
  `newRunWiring(app)`, …) that owns its full field wiring; `NewApp` is the
  object graph plus a sequence of constructor calls. A field cannot be silently
  skipped in the composition root because it lives with its struct —
  `TestAppWiringCompleteness` pins the required hooks (the `/debug`
  `WriteDebugLog` and right-click-paste hooks shipped unwired this way). Read the
  target before wiring an unassigned seam: some seams are unassigned because
  assigning them is wrong. `ConfigureHTTPIdleTimeout` fed a helper that wrote the
  process-wide `http.DefaultTransport` — a data race against in-flight requests —
  and the setting already reached the wire per request (D40).

`tui/render.go` (+ `mainscreen.go`, `altscreen.go`, `terminal.go`,
`stdinbuffer.go`) is the differential renderer core. Its lock discipline is
load-bearing (see below).

**The session projection lives in one module.** `coding/session_projection.go`
owns what the next request carries: `SessionManager.Projection()` resolves the
branch path, the compaction window, the context settings and the messages in one
walk, caches the result by branch version (leaf + entry count), and hands its
slices out with no spare capacity so a caller's append cannot write into the
cache. `CurrentSystemMessage`, `LatestCompaction` and `ContextSignature` resolve
from the same place. Before this, ten call sites assembled the projection by hand
from `buildSessionPath` / `applyCompactionWindow` / `getSessionContextSettings` /
`getEntriesLocked`, which is how the cache-warmer currency check ended up
re-projecting the whole session per request: nothing owned the answer and the
cost was invisible at the call site. Measured on the 45 MB / 19.4k-entry session:
cold `Projection` 37 ms, cached 0, `AppendCompaction` 0 ms with a warm
projection (247 ms originally, 56 ms before the cache).

A resolution now walks the branch **by pointer** under the session lock
(`branchPointersLocked`) and copies only the entries the compaction window
keeps, instead of copying the whole entry tree and rebuilding an id map on every
append. Profiling the 45 MB / 19.4k-entry session's projection after an append
(the version changes on every append, so this ran per request) put 11 ms in
`getEntriesLocked`'s entry copy, 17 ms in `buildSessionPath`'s value walk, 1 ms
in the window, and **8 µs in decoding every message**: the session has 17
compactions, so the decoded window was 3 entries. Decoding was never the cost;
the copies were. With the pointer walk the projection is 3.3 ms cold and 2.3 ms
after an append, and `coding/session_messagecache.go` memoizes each message
entry's decoded messages and role (entries are append-only, so a memo never
needs invalidation) for the uncompacted case, where the window is the whole
path. `TestCompactedProjectionCostsTheWindowNotTheSession` pins the shape: a
2001-entry session compacted to an 8-entry window resolves in 9.5 KB of
allocation, where the tree copy measured ~800 KB. `BuildSessionContext` and
`BuildContextEntries` stay as the value-based reference (the manager's
resolution is compared against them by `TestProjectionCacheMatchesTheReference`),
and `applyCompactionWindow` became a wrapper over `walkCompactionWindow`, which
lets `ContextSignature` count the window without materializing it.

**Cache-miss notices are O(1).** `SessionManager.CacheMissFor` answers the
notice for an assistant message from `cacheScanState`, which advances as entries
append (`appendEntry` consumes) and is seeded over the loaded entries in
`buildIndex`. Upstream rescans parsed entries per assistant message
(`detectCacheMiss(getEntries(), …)`); the port stores raw JSON, so the same call
re-decoded every message entry: **713 ms** on the 45 MB session, on the UI loop,
on every assistant message end, for anyone with `showCacheMissNotices: true`
(which the reporting user had). The running state is 52 ns per notice and adds
167 ms to loading that session, where a full scan of the seeded entries is
504 ms. `CollectCacheMisses` keeps the full scan for transcript rebuilds (resume,
compaction, reload), which still costs ~500 ms on that session.
`TestCacheMissForMatchesTheFullScan` is the differential guard: the incremental
notice must equal `DetectCacheMiss` on the session's entries for every assistant
message, including after a compaction and a branch summary (which reset the
scan). `scanAssistantRequest` reads only the role, usage, model and timestamp
for the state; decoding the whole message (thinking, tool arguments) was what
made seeding cost 500 ms.

**Large-session rendering.** `RenderSessionItems` (`transcript.go`) renders a
session eagerly below 400 items; above the threshold it collects the items into
a collector container and attaches only the trailing window (120 components),
materializing the rest 64 per loop beat via `RunWiring.OnBeat` →
`MaterializeDeferred` (`Container.InsertChildAt`). A 30 MB / 15k-entry session
paints its first frame in ~20 ms instead of ~400 ms. Resizing a huge session
still costs ~350–430 ms per new width because the layout renders the whole
attached scroll content at the content width (`ScrollContentLines` calls the
component render) — this matches upstream `layout.ts` and is deliberately not
staggered (a partial-width render would show stale text). A manual `/compact`
on that session spent ~20 s and ~325 GB of allocations in `PrepareCompaction`
before the summarization request was even built, because the port re-derived
the context projection inside the loop over it (`BuildContextEntries` once per
entry); it now builds the projection once (~105 ms at 15.5k entries), pinned by
`TestPrepareCompactionProjectsTheContextOnce`. The other projection was on the
request path: `cacheContextIsCurrent` (the cache-warmer currency check) built
the whole `SessionManager` context — ~190 ms on that session — on **every**
model request, and `BuildSessionContext` held the session mutex while it ran,
so the UI thread (the footer re-reads the session on each status change) parked
behind it and the TUI froze for ~200 ms right after every tool result, which is
how it was reported ("running bash freezes the TUI"). The check now compares
cheap `SessionManager.ContextSignature` values (branch-cache lookup + entry
scan) and the projection snapshots the entries and projects outside the lock;
see `TestCacheContextIsCurrentDoesNotProjectTheSession` (37 MB → 0 allocated per
call) and `TestBuildSessionContextDoesNotHoldTheSessionLock`. The remaining
frame cost was the bash preview: counting a command's output (for the
"… (N earlier lines" hint) went through `tui.WrapTextWithAnsi` per line, ~1 ms
and ~8000 allocations per 2000-line output, and a rebuilt transcript (compaction,
`/reload`, resume) starts with every attached bash component cold, so the first
frame after a rebuild stacked that into a 100+ ms frame (caught by a SIGQUIT
dump inside `bashPreviewComponent.Render` → `visualLineCount`). Plain ASCII
lines now count in one allocation-free pass (`plainWrappedLineCount`), pinned to
the generic wrapper by `TestPlainWrappedLineCountMatchesTheGenericWrapper` and
capped by `TestBashPreviewCountIsAllocationFree`. `AppendCompaction` had the
same shape: its entry records the projected system message, and the port
resolved that from `buildSessionContextLocked` **while holding the session
mutex** — 247 ms on a 45 MB session, so every UI read waited. It now projects
before taking the lock (upstream is single-threaded and its parsed-object
entries make the same projection cheap), and `getSessionContextSettings`
resolves the model/thinking level by scanning the path backwards instead of
decoding every message entry to find the last assistant, which cut a
post-compaction projection from 210 ms to 56 ms. Pinned by
`TestAppendCompactionDoesNotHoldTheSessionLock` and
`TestProjectedSettingsResolveLastWriteWins`. Measured end-to-end (real binary in
a pty, scripted model, the 45 MB / 19.4k-entry session resumed, bash tool call,
both TUI modes, silent and output-streaming commands, with and without
auto-compaction in the turn): the worst frame gap during the run is 82–83 ms,
which is the spinner's own ~80 ms animation interval, with keystroke echo at
5–6 ms; before these fixes the same run showed 200–300 ms lock-blocked stalls
and a 121 ms frame after a transcript rebuild.

## Conventions and gotchas

- **A session always has its collaborators.** `SessionConfig.Control` carries
  the model runtime, settings manager, tool registry and toggles;
  `NewAgentSession` installs it (or an empty block), so no session method guards
  against a half-built session. The fields inside the block stay optional
  (`ModelRuntime` nil means no auth/catalog, `Settings` nil means no live
  settings), and an empty block means auto-compaction and auto-retry are off.
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
- **Tests never write into the shared temp dir.** `os.TempDir()` follows
  `TMPDIR`, so a test that exercises temp-file code paths pins it
  (`t.Setenv("TMPDIR", t.TempDir())`). Two leaks came from ignoring this: 492
  empty `pi-agent-dir*` directories, and 215 `pi-typing-probe-*.log` files.
- **Render cost scales with content.** The loop is one goroutine, so a
  value-shaped loop on it is a freeze rather than a slowdown. `renderInlineTokens`
  accumulated a rendered message into a string with `result += ...` while looping
  once per token, so every append copied the whole accumulation: one 1 MB message
  allocated 46 GB and took 11.5 s on the loop (100 KB: 113 ms, which is what a
  captured "render" phase of 126 ms was). Use `strings.Builder` whenever the
  iteration count scales with the input, and read a GC-dominated CPU profile on
  the render path as this class — `TestMarkdownRenderScalesWithInputSize` bounds
  the allocation volume so a regression cannot come back unnoticed.
- **PTY watchdogs.** `coding/interactive/ptywatch_test.go` drives the real
  binary through the D136–D139 flows (editor submit, model selector,
  fullscreen scroll, exit) inside a pty, then sends `SIGQUIT` and fails when
  the stack dump shows any goroutine parked in `sync.runtime_SemacquireMutex`.
  The tests use `$PIER_TEST_BIN`, else `./bin/pier`, else they build once into
  a temp dir (and skip if that fails), so the package gate stays fast. CI
  prebuilds the binary and sets `PIER_TEST_BIN`. `TestMutexBlockedDetectorHasTeeth`
  proves the parser still catches a genuine mutex deadlock.

### Lock inventory

The `tui/` and `coding/interactive/` refactor retired every UI mutex. What
remains in non-test code is two pure handoff locks plus one documented
`coding/` exception:

| Lock | Where | Protects | Retirement |
|---|---|---|---|
| `Renderer.postMu` | `tui/render.go:143` | the posted-callback queue (`Post` is called from off-loop goroutines: loaders, watchers) | **retained (D146)**: serializes the handoff only; the owner drains it in the render pass and never runs a callback under it |
| `ProcessTerminal.writeMu` | `tui/terminal.go:160` | terminal writes, raw-mode transitions, Kitty negotiation bookkeeping shared with the reader | **retained (D147)**: not UI state |
| `FooterDataProvider.mu` | `coding/footerdata.go` | cwd/git/status + listener registry, shared with its 500 ms git-HEAD watcher | **retained (D149)**: closing it needs the poll result posted to the loop and the listener fan-out delivered outside the lock; a `coding/` change outside this refactor |

Retired along the way (all struck from the code; `grep 'sync.Mutex'
tui/ coding/interactive/` outside tests returns only the two above):

| Lock | Where | Retired in |
|---|---|---|
| `Renderer.renderMu` | `tui/render.go` | D146 (input and painting share the owner goroutine) |
| `Renderer.mu` | `tui/render.go` | D146 (loop-owned) |
| `Container.mu` | `tui/component.go` | D146 (loop-owned) |
| `Editor.mu` | `tui/editor.go` | D146 (loop-owned) |
| `AltScreen.mu` | `tui/altscreen.go` | D146 (loop-owned) |
| `ScrollView.mu` | `tui/scrollview.go` | D146; the scrollbar hide timer is a lazy deadline |
| `Loader.mu` | `tui/selectlist.go` | stage 4 |
| `AltScreenFlashContainer.mu` | `tui/selectlist.go` | stage 4 |
| `StdinBuffer.mu` | `tui/stdinbuffer.go` | D147 (consumer-driven flush) |
| `negotiationResult.lastDataMu` | `tui/terminal.go` | stage 3 (reader stamps an atomic) |
| `KeybindingsManager.mu` | `tui/keybindings.go` | stage 4 |
| `globalKeybindingsState.mu` | `tui/keybindings.go` | stage 4 |
| `kittyProtocolState.mu` | `tui/keys.go` | stage 4 |
| `lastEventTypeState.mu` | `tui/keys.go` | stage 4 |
| `widthCacheMu` | `tui/width.go` | stage 4 |
| `ModelSelectorComponent.mu` | `coding/interactive/modelselector.go` | stage 4 |
| `ScopedModelsSelectorComponent.mu` | `coding/interactive/scopedmodelsselector.go` | stage 4 |
| `SessionSelectorComponent.mu` | `coding/interactive/sessionselector.go` | stage 4 |
| `scheduleOnce` local `mu` | `coding/interactive/sessionselector.go` | stage 4 |
| `editCallComponent.previewMu` | `coding/interactive/toolrenderer_edit.go` | stage 4 |
| `FooterComponent.cacheMu` | `coding/interactive/footer.go` | stage 4 |
| `ModelCatalogRefreshCoordinator.mu` | `coding/interactive/catalogrefresh.go` | stage 4 (D148) |
| `activeCatalogRefresh.mu` | `coding/interactive/catalogrefresh.go` | stage 4 (D148) |
| `Lifecycle.mu` | `coding/interactive/interactivemode_lifecycle.go` | stage 4 |
| `StartupWiring.mu` | `coding/interactive/interactivemode_startup.go` | stage 1 (input handoff is a channel) |
| `Theme.mu`, `themeState.mu`, `trueColorState.mu`, `customThemesDirState.mu` | `coding/interactive/theme.go` | stage 4 (atomics / copy-on-write) |

When retiring a lock, the invariant is unchanged: no user code under a lock;
snapshot under and deliver outside.

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
  `SetClipboardReader`, `SetBrowserOpener`, `coding.SetPackageDir`. The
  interactive test screens call `DisableAutoRender()` (now a no-op: every
  renderer is caller-driven since D146) so tests drive rendering with
  `RenderNow`.
- **Goldens**: `tui/testdata/`, `coding/interactive/testdata/`,
  `coding/testdata/` hold `label => JSON` lines generated by driving the
  upstream TypeScript with Node; Go replays the same scripted input.

## Divergences

Where upstream relies on JSON/JS semantics Go has no equivalent for, or where
upstream has a defect, the port diverges deliberately: a numbered **D-row** in a
code comment at the point of divergence, with the reproducing scenario, and an
entry in `docs/DIVERGENCES.md` (range **D1–D161**). Prefer a D-row over silently
approximating upstream.

## Out of scope (documented)

Native clipboard, kitty/iterm image transport internals beyond the
line-detection helpers, extension mechanics, the package manager, and the
print/json/rpc modes. Mermaid rendering needs `grok-mermaid` and is inert.

## Session history (high level)

The repository was built as a long port: the `ai`/`agent`/`coding` cores first,
then the `pi-tui` library (renderer, layout, terminals, keys, markdown,
components), then the interactive coding-agent mode (theme, every component,
and the mode method groups), each round verified against upstream Node goldens
and committed locally with a `docs/PORTING.md` update. The CLI
(the root `main.go` over the `cmd` package, originally `cmd/pi`) and its
composition layer (`app.go`) came last,
followed by five deadlock fixes: D136 (editor submit), D137 (model selector),
D138 (scroll) and D139 (exit) were found by driving the real binary in a PTY,
and D156 (login) by reading the dispatch path, with a test that fails instead of
hanging when the regression returns. The
executable was then renamed from `pi` to `gpi` and finally to `pier` (the display name follows the
invoked file name).

To reproduce the exit/scroll checks, drive `bin/pier` under a PTY, exercise the
flow, then send `SIGQUIT` to the process and look for goroutines blocked on
`sync.Mutex`.
