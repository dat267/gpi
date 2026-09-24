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
| `main.go`, `cmd` | `main.ts` | the CLI entrypoint (a thin root main over the `cmd` package) |
| `chord`, `client`, `protocol`, `server`, `telemetry`, `durable` | same | supporting packages |
| `scripts` | — | catalog generators |

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
- **`PIER_STALL_MS=<ms>`** makes the loop record every phase it measures
  (`render`, `events`, `event-apply`, `beat`, `input`) above the threshold to
  `<agentDir>/pier-stall.log`, each with a goroutine dump. It is off by default
  and exists for stutters that do not reproduce on the development machine: a
  record names the phase and the stacks instead of only how long it took.
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
  `WriteDebugLog` and right-click-paste hooks shipped unwired this way).

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

Numbered D-rows live in code comments at the point of divergence and are
summarized in the README scoreboard. The range is **D1–D156**. Representative:

- D41 — extension mechanics are out of scope (extension discovery in the
  resource loader, the extension runner, package/tools managers); seams are
  function values or return nil. The resource loader's non-extension pieces are
  ported: context files, skills, and the SYSTEM.md / APPEND_SYSTEM.md prompt
  files. `/reload` follows upstream `AgentSession.reload` for them — settings
  re-read and queue modes, `ai.ResetAPIProviders`, the resource files, then the
  system prompt rebuilt from the active tool names — and the mode's keybindings,
  implicit project trust and UI re-application. Not reloaded: the extension
  runner and package manager (out of scope), the tool registry (built-in tools
  capture no settings-dependent state; the bash tool reads the shell settings
  per call), and extension-supplied prompt templates (the port does not load
  them at boot either). Theme files are re-read — `applyReloadedSettings` calls
  `ApplyFromSettings`, whose `SetTheme` reloads the named theme from disk — so
  the `/reload` notice speaks of keybindings, skills, prompts, themes and
  context files. It deliberately drops upstream's leading "extensions": nothing
  in the port implements them (D41), so the notice would announce work that
  never happens.
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
- D83 — `DisableAutoRender` test seam; obsolete since D146 (every renderer is
  caller-driven, so it is a no-op kept for the test/API surface).
- D90 — narrow runtime interfaces for testability across the wirings.
- D105/D106 — the ES `Proxy` renderer reference becomes an explicit forwarder;
  clipboard copying and reading are injected (native clipboard out of scope).
- D121/D123/D135 — cross-goroutine state made mutex-safe (model refresh,
  startup input/telemetry, lifecycle flags, footer watcher). D123's input lock
  is retired in stage 1 (the submission handoff is a buffered channel); the
  others retire in stages 3-4.
- D132 — branch summarization is tracked as compaction and abortable.
- D146 — **retired**: the renderer's internal timer is gone (D146 removed the
  timer-mode render path outright) and the standalone `-r` session picker runs
  its own loop (raw input → dispatch → paint on one goroutine), so input
  dispatch and painting share the owner goroutine everywhere.
  `Renderer.mu`, `renderMu`, `Container.mu`, `Editor.mu`, `AltScreen.mu` and
  `ScrollView.mu` are deleted; render requests coalesce onto the tick channel
  and callbacks cross goroutines only through `Post` (queue under `postMu`).
  The ScrollView's transient-scrollbar hide timer became a lazy deadline
  driven by the animation walk. Off-loop readers in tests go through
  `Post`-based snapshot helpers.
- D147 — **retired** (the decoder lock and the terminal's UI-state mutex are
  gone): `StdinBuffer` is owned by the input consumer — the interactive UI
  loop feeds raw stdin chunks through `ProcessTerminal.FeedInput` and drives
  force-flushes via `PendingTimeout`/`FlushExpired` (no mutex, no internal
  timer); the keyboard-protocol negotiation state is loop-owned too. The one
  retained lock is `ProcessTerminal.writeMu`: pure write serialization between
  the OSC 9;4 progress keepalive goroutine, loop-side writers, and the
  shutdown path's raw-mode restore — I/O serialization, not UI state. The
  legacy (non-raw) reader path remains for library consumers whose Terminal
  is not driven by a UI loop.
- D148 — **closed**: the model-catalog refresh registry is lock-free (atomic
  copy-on-write map with insert-if-absent/unpublish-if-matching, atomic per
  refresh outcome, waiter count and canceled flag). Two races were fixed on the
  way: publishing must not overwrite an entry that appeared after the load, and
  a waiter slot is only claimed once the entry is confirmed published.
- D155 — **the plumbing-to-nothing inventory** (the port's wiring structs
  inject their collaborators as func fields, and several call sites nil-check a
  seam and skip — so a seam nothing assigns is a feature that silently does
  nothing). **Fixed**: the queue controller's `ShowStatus`/`ShowError`/
  `ShowWarning` (every status it raised was dropped, so ctrl+t toggled with no
  feedback), `HandlerWiring.HandleBashCommand` (`!command`), `/new`,
  `HandleCloneCommand` + the selector's `RuntimeFork` (one runtime fork,
  `coding.ForkSessionAtEntry` + `App.forkAtEntry`), `/import`
  (`ImportFromJSONL`), `OnExternalEditor`, the auth `ScheduleTimer`,
  `OnLabelChange` and `ApplyFullscreenScrollbarSetting`. **The dialog seams are
  now wired too**: `ShowExtensionConfirm` and `PromptForMissingCwd` are
  **callback-based** (`onAnswer`/`onCwd`) rather than value-returning, because
  the selector slot's `Show` mounts a component and returns immediately while the
  seams returned the answer — upstream's equivalents are awaits, so the
  synchronous shape was a mis-port. That is what had left `/import`'s
  "Replace current session?" prompt and the missing-cwd prompt of `/import` and
  the resume flow unreachable; both flows are now callback chains matching
  upstream's sequential awaits, and the dialogs themselves are app-level helpers
  over the selector slot, built on the extension selector component that already
  existed (title, description, options, timeout). The retry also had to key on
  the typed `coding.MissingSessionCwdError`: the text check it used before
  (`strings.Contains(err, "cwd")`) could never match upstream's wording, which
  says "working directory". **Closed**: `ClearStatusContainerIfIdle`
  is implemented (upstream's `!enabled && !activeStatusIndicator &&
  statusContainer.clear()`, shared with the reload path instead of copied), and
  the last two are not work — `RebindSession` is **redundant**: upstream's
  runtime calls a mode-provided rebind after replacing a session, and the port
  does that inline in `applySessionReplacement` (swapping the session and every
  wiring's `SessionInfo`) with the initial binding done at composition, so an
  init-time hook has nothing left to do; `OnPartialEventApplied` is a **test
  seam**, labelled as one where it is declared. The login dialog's select step
  (`ShowAuthSelect`) is implemented — the Amazon Bedrock flow asks one, so the
  login could not get past its first prompt — as a list inside the login dialog,
  which already owns its input routing, so no focus switch was needed; and
  `OnPromptShown` is not dead but an explicitly-labelled test seam (nil simply
  means no hook), like the `Now`/`ScheduleTimer`/`DeleteSession` overrides. **Deliberately off** (out of
  scope): the HTTP dispatcher, the package manager, highlight languages (D74, and
  the user chose to keep the flat fallback), tmux, the process-level seams and
  extension mechanics (D41) — with them `ShowExtensionSelector`, the extension
  resource selector, which has no port counterpart.
  **Not dead, despite looking like the rest** — check whether the nil branch
  skips the work or falls back before treating an unassigned seam as a bug:
  `SessionSelectorOptions.DeleteSession` and `CopyActiveSelection` are overrides
  with working defaults (the deleter and the AltScreen's own selection copy), and
  `MaybeSaveTrust` is a field nothing references at all while the feature it
  names is implemented and called from the reload path. A source-scanning test
  that flags these mechanically was written and dropped as not worth its keep
  (AST heuristics plus a maintained allowlist, which drifts); this row is the
  record instead.
- D154 — **the port ships its own theme palette**. `coding/interactive/piertheme.go`
  defines two themes — one for a dark terminal background, one for a light one —
  and the CLI installs them at startup under the upstream names (`dark`,
  `light`), so the whole settings/terminal-detection path (`ResolveThemeSetting`,
  `ParseAutoThemeSetting`, `GetDefaultTheme`) is unchanged and still picks the
  variant from the terminal. Two deliberate departures from upstream's
  `dark.json`/`light.json`: **every background token is left unset**, which
  renders as the terminal's default background (`\x1b[49m`) so the theme never
  paints over a transparent or blurred terminal (primary text is the terminal's
  own foreground for the same reason), and **the accent is amber rather than
  upstream's teal** — it carries the wordmark, borders, selection and list
  bullets, so which build is running is obvious at a glance. Selection stays
  legible without a fill because every list marks the current row with an
  accent-coloured `→ ` prefix. The embedded upstream palettes are kept: they are
  upstream's reference palette, they are what the **upstream-parity test corpus
  renders with** (those tests clear the theme registry first, so they are
  unaffected by the install), and they remain the fallback for library consumers
  that never call the installer. Two fixes fell out of this: `loadThemeJSON`
  checked the built-ins *before* the registry while `loadTheme` checked the
  registry first, so a theme shadowing `dark` rendered as the override but
  resolved its export and resolved-colour tokens from the built-in — both now
  prefer the registry — and `Theme` carries its source document, so a registered
  theme that has no file on disk can still be exported to HTML. The install must
  follow the truecolor/style capability switch, since a theme bakes its 256-colour
  or truecolor escapes at creation time.
- D153 — **the CLI's startup flags are wired** rather than merely parsed
  (`cmd/main.go`, with the pure parts in `coding/clidiagnostics.go`,
  `coding/cliinitial.go`, `coding/listmodels.go`, `coding/paths.go` and
  `coding/sessionresourceload.go`). Fifteen documented flags plus two
  non-flag inputs were read by nothing: an unknown single-dash option, a bad
  `--thinking` value or a blank `--name` was accepted silently,
  `pier @notes.txt "explain"` sent no file at all, and `--use-theme` left the
  configured theme in place.
  - **Diagnostics** are reported right after parsing — before `--version`, as
    upstream does — and an error among them exits 1.
  - **`@file`** text is folded into the session's first message ahead of the
    first positional message, with the rest queued behind it, matching upstream
    `buildInitialMessage`. *One deliberate gap*: upstream attaches `@file`
    **images** to that message, but this build's interactive mode has no
    image-input path at all, so the images are dropped with a warning rather than
    letting the model be asked about an image it never received.
  - **`--skill`, `--prompt-template`, `--theme`** paths are resolved against the
    working directory (`ResolveCLIPaths`/`IsLocalPath`, a port of upstream
    `resolveCliPaths`), and each **survives its own `--no-*`**: refusing
    discovery is not refusing what was named, which is upstream's asymmetry in
    its resource loader. `--no-skills`, `--no-prompt-templates` and
    `--no-context-files` suppress the settings' paths and discovery;
    `--no-themes` drops the discovered set (the agent's `themes/` directory and
    the settings' theme paths) likewise. Prompt templates now reach the session
    for the first time — `LoadPromptTemplates` existed with **no caller**, so
    `/template` expansion, the prompt commands and the `[Prompts]` startup
    section were all inert.
  - **All of those switches survive `/reload`**: they are kept on the session and
    re-applied by `reloadResources`, which previously rebuilt the resource set
    from the settings alone — so a reload used to undo `--no-skills`, resurrect
    the settings' skill paths and drop an explicit `--skill`.
  - **Theme discovery is now the only lookup path**: a theme resolves by its
    *declared* name across the sources, as upstream's loader does, rather than by
    its file name under one directory. The old `<dir>/<name>.json` fallback is
    gone because it ignored the discovery switch, which made `--no-themes` leak —
    the theme vanished from the list but still loaded by name.
  - **`--models`** sets the model cycle scope, overriding the settings' enabled
    models, and reports a pattern that matched nothing at startup rather than
    silently scoping nothing. A scope also **seeds the model**, which it did not
    before: with nothing named and a new session, the settings' default is used
    when the scope contains it and the first scoped model otherwise (upstream
    `buildSessionOptions`), including the thinking level a
    `model:thinking-level` pattern carries unless one is pinned. **`--api-key`** pins the credential on the provider
    of the model the session resolved. **`--name`** records a `session_info`
    entry (blank is an error). **`--approve`/`--no-approve`** settle project trust
    for the run. **`--export`** and **`--list-models`** are implemented and exit
    before the TUI.
  **`--extensions`/`--no-extensions`** are parsed into `Extensions`/`NoExtensions`
  but load nothing, since extension mechanics stay out of scope (D41).
  Upstream's unknown-flag split is matched exactly, and tested: an unknown
  **long** flag is recorded in `UnknownFlags` for extensions to consume and is
  not an error (upstream's `args.js` does the same), while an unknown **short**
  flag is an `Unknown option: -z` error diagnostic.
- D152 — the Unix socket **publish is portable** (`server/unix.go`,
  `server/publish_linux.go`, `server/publish_other.go`). Upstream publishes a
  bound socket with a hard link, which is atomic and refuses to overwrite — so a
  socket another process created at the path in the meantime is never clobbered,
  and the surviving inode is the one whose device/inode the cleanup path
  recorded. **Android denies `link(2)` to the app domain outright**, so every
  bind failed there and the whole `server/unix` test suite failed with it.
  `publishSocket` now tries the link, then `renameat2(RENAME_NOREPLACE)` —
  atomic, and still refusing to overwrite — and only then a plain rename, which
  keeps atomicity but may replace a path that appeared in the window. An
  occupied destination is never replaced on any path: a link that fails
  `EEXIST` returns immediately rather than falling through. `renameNoReplace`
  is `//go:build linux` (Android satisfies the `linux` tag); elsewhere it
  reports `errors.ErrUnsupported` and the plain rename is the fallback.
- D151 — **modeldefault builtin** (`coding/modeldefault.go`, wired in
  `coding/sdk.go` and `coding/agent_session_reload.go`). A port of the user's
  modeldefault extension: pi scopes the model to the session, so a session that
  once picked a model keeps it across resumes and the settings default
  (`defaultProvider`/`defaultModel`) never reaches it again. Every session
  start — creation and `/reload` — now moves the session onto the settings
  default, which is what the extension did on `session_start`. A manual switch
  therefore lasts for the current session only; there is no persisted
  per-session claim, no command, and no opt-out short of clearing the default.
  The full catalog model object is applied, never a bare ref (a ref without its
  limits reaches the footer as `?/0`). **Two deliberate differences from the
  extension**: (1) it polls (150 ms over a 4 s budget) because pi's
  provider-auth snapshot lands asynchronously, whereas this port's
  `queueAvailabilityRefresh` runs inline, so one attempt is equivalent and the
  same two outcomes — `no configured auth`, or `no configured auth, or not in
  the catalog` — are reported verbatim; (2) an explicit `--model`/`--provider`
  choice (a non-nil `CreateAgentSessionOptions.Model`) suspends the sync for
  that session, including across reloads, because overruling an explicit
  command-line choice would be a surprise the extension never had to consider.
  This diverges from upstream's session-scoped model restoration by design;
  the notice is surfaced with the startup diagnostics (informational on a
  switch, a warning when the default cannot be applied).
- D150 — the startup "loaded resources" area ports the **Skills**, **Context**
  and **Prompts** sections of upstream `showLoadedResources` (collapsed name
  list plus the expanded project/user/path scope groups), the skill
  warnings/collisions block (`[Skill conflicts]`), and the ctrl+o expand
  toggle now drives the header and section expandables (`SetToolsExpanded`).
  Themes and Extensions sections are not rendered (the Go loader exposes no
  custom-theme SourceInfo and extension mechanics are out of scope, D41/D140).
  `coding.LoadSkills` now matches upstream's loader: paths resolve
  (`~`/relative, `ResolvePath`), skills dedupe by canonical real path, and
  name collisions keep the first and record a `collision` diagnostic — the
  prior port appended both the default agent-dir skills and a settings path
  pointing at the same directory, duplicating every skill.
- D149 — `FooterDataProvider.mu` (`coding/footerdata.go`) is the one lock the
  refactor leaves in place, by design. It guards the provider's cwd, git paths,
  branch, extension statuses and listener registry, which its own 500 ms
  **watcher goroutine** (git HEAD polling) shares with the UI loop's footer
  render. Closing it needs the same treatment as the accepted
  `SessionManager` fix plus a decision on the watcher's delivery: the poll
  result would have to be posted to the UI loop (like the theme watcher now is)
  and the listener fan-out delivered outside the lock. That is a `coding/`
  change outside this refactor's scope, so it is documented rather than
  retired; the provider is otherwise loop-idiomatic (its polling goroutine only
  reads git state and calls `Refresh`).
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
- D156 — **the login flow runs off the UI loop** (the fifth deadlock, and the
  first found by reading rather than by driving the binary in a PTY).
  `ShowLoginDialog`/`ShowApiKeyLoginDialog` called `LoginProvider` inline, and
  the flow waits on the dialog for its answer — `ShowAuthPrompt` selects over
  the dialog's input channel — so the loop sat inside the flow, waiting for a
  keystroke only the loop could deliver. Every provider whose login prompts
  hung: OAuth device flows, API-key entry, Bedrock's method/profile selects.
  The dispatch is on the loop, the same reason the neighbouring
  `HandleBashCommand` needed its off-loop fix. **Fixed**: `AuthWiring.startLogin`
  (shared by the OAuth and API-key dialogs, which differed only in their message
  prefixes) runs the flow on its own goroutine and posts its continuation —
  editor restore, error report, `CompleteProviderAuthentication` — and
  `LoginDialogComponent` takes a `post` func so its own `Show*` mutations are
  marshaled onto the loop. The prompt channel is still created synchronously and
  the browser opener stays on the flow's goroutine (exec can block); with a nil
  `post` the mutations apply inline, which is how the dialog tests drive it.
  Upstream awaits the flow inline, which its single-threaded runtime can afford.
  `TestApiKeyLoginRunsOffTheUILoop` models production's single loop goroutine —
  dispatch, then render and route input — and fails ("the login flow is running
  on the UI loop") instead of hanging when the dispatch blocks.

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
