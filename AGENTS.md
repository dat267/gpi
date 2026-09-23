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
  producers blocked on a full channel are released by the run context's
  cancellation or by the queue's `Close` (the terminal reader and event
  subscribers never receive a context, so both arms are present).
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
- `cmd/pier/main.go` — boots settings/auth/model-runtime/agent-session and runs
  the `App` (port of `main.ts`'s boot).
- Each `*Wiring` has a constructor next to its struct (`newCommandWiring(app)`,
  `newRunWiring(app)`, …) that owns its full field wiring; `NewApp` is the
  object graph plus a sequence of constructor calls. A field cannot be silently
  skipped in the composition root because it lives with its struct —
  `TestAppWiringCompleteness` pins the required hooks (the `/debug`
  `WriteDebugLog` and right-click-paste hooks shipped unwired this way).

`tui/render.go` (+ `mainscreen.go`, `altscreen.go`, `terminal.go`,
`stdinbuffer.go`) is the differential renderer core. Its lock discipline is
load-bearing (see below).

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
call) and `TestBuildSessionContextDoesNotHoldTheSessionLock`.

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
summarized in the README scoreboard. The range is **D1–D150**. Representative:

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
  per call), and prompt templates/theme files (the port does not load them at
  boot either).
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
