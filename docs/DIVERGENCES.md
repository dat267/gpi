# Divergences

Numbered **D-rows**: every place this port knowingly differs from upstream —
usually because upstream relies on a JS or Node behaviour that has no direct Go
equivalent, or because a defect upstream is fixed here. D-row numbers live in
code comments at the point of divergence; this file is the log, and it is
representative: the rows below carry a written-up rationale, while the rest live
only as the code comment that introduced them. The range is **D1–D168**.

- D30 — startup timings read `PI_TIMING` **per call** instead of once at module
  load (upstream reads the flag when the timing module is first imported), so a
  test can toggle the flag and observe the output without the process having to
  restart. `SetTimingsEnabled` is the same override in test form.
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
  never happens. The same rule covers the other extension-flavoured user-visible
  text: the update card names the module's install path instead of `<app>
  update` (there is no package manager and no update command), the trust row's
  description says "when no saved trust decision decides project trust", the
  trust prompt and the untrusted-project warning name only the `.pi` settings and
  resources this port actually gates (not installing packages or running
  extensions), and
  the package-update card is gone: its only caller upstream asks the package
  manager about the extension packages it installed, which this port has no
  equivalent of. The install-telemetry ping is not sent either: the port has no
  telemetry backend, so the fresh-install version recording stays (it drives the
  changelog) while the reporting seam went with the endpoint, and the extension
  selector's tools-expanded hook went with the extension UI. The `pi config`
  resource-manager TUI went for the same reason:
  it exists to enable and disable package resources, and with no package manager
  there is nothing for it to list — it was 1,315 lines reachable only from its own
  golden test.  The update card is reachable: the release check runs at startup off the UI
  loop and reports on it, and it asks **this module's** release feed — the Go
  module proxy path for the module, which is what the card's `go install …@latest`
  line resolves — rather than pi's own feed, whose versions are pi's. A tag keeps
  its `v` prefix (Go module versions do), so the semver subset accepts the one
  leading `v` npm's `valid` accepts. Upstream's feed also carries release notes
  and a package name, and the card rendered them; the proxy carries a version
  only, so the card carries a version only. Nothing is reported for an unstamped
  build: its version is `0.0.0`, and a proxy pseudo-version of `0.0.0` sorts
  older than `0.0.0`. Offline, nothing is checked: upstream folds `--offline`
  and a truthy `PI_OFFLINE` into `PI_OFFLINE` and `PI_SKIP_VERSION_CHECK` for
  the whole process, and `cmd` does the same (`applyOfflineMode`).
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
- D154 — **the port ships its own theme palette**.
  `coding/interactive/piertheme.go` defines two themes — one for a dark terminal
  background, one for a light one — and the CLI installs them at startup under
  the upstream names (`dark`, `light`), so the whole settings/terminal-detection
  path (`ResolveThemeSetting`, `ParseAutoThemeSetting`, `GetDefaultTheme`) is
  unchanged and still picks the variant from the terminal. Three deliberate
  departures from upstream's `dark.json`/`light.json`: **the decorative
  background tokens are left unset** (the selected list row, search matches,
  custom messages), which renders as the terminal's default background
  (`\x1b[49m`) so the theme never paints over a transparent or blurred terminal
  (primary text is the terminal's own foreground for the same reason); **the
  signal fills are kept** (`toolPendingBg` / `toolSuccessBg` / `toolErrorBg`, a
  neutral panel while a call runs and the success/error hues as a tint of it,
  plus `userMessageBg`), because they are not decoration. The tool three are the
  *only* signal upstream has that a tool call failed
  (`components/tool-execution.ts` picks between them and fills the whole block;
  the port does the same in `toolexecution.go`), so leaving them unset made a
  failed `bash` call byte-identical to a successful one; the user fill is the
  only thing that marks a block as yours, since the assistant message has no
  fill and both are otherwise plain markdown in the same box. All four are also
  painted by the HTML export (`.tool-execution.success/error` and the user
  block, via `--toolSuccessBg` and friends), which came out blank as well. Their
  values are the port's own, not upstream's: a user message is a warm panel tied
  to the accent (`#393630` dark, `#f4eee1` light) where upstream's is a cool
  blue-gray (`#343541`) or plain grey (`#e8e8e8`); and **the accent is amber
  rather than upstream's teal** — it carries the wordmark, borders, selection
  and list bullets, so which build is running is obvious at a glance. Selection
  stays legible without a fill because every list marks the current row with an
  accent-coloured `→ ` prefix. The fills are chosen to stay distinct after the
  256-colour conversion (`rgbTo256` sends a low-spread dark to the grey ramp and
  pushes a hue into the cube: the warm `#393630` lands on grey 237, one ramp
  step from `toolPendingBg`'s 235, and a subtler dark would have collapsed onto
  the same index), so the states remain distinguishable on a terminal without
  truecolor — and `piertheme_test.go` pins all of it. The embedded upstream
  palettes are kept: they are upstream's reference palette, they are what the
  **upstream-parity test corpus renders with** (those tests clear the theme
  registry first, so they are unaffected by the install), and they remain the
  fallback for library consumers that never call the installer. Two fixes fell
  out of this: `loadThemeJSON` checked the built-ins *before* the registry while
  `loadTheme` checked the registry first, so a theme shadowing `dark` rendered
  as the override but resolved its export and resolved-colour tokens from the
  built-in — both now prefer the registry — and `Theme` carries its source
  document, so a registered theme that has no file on disk can still be exported
  to HTML. The install must follow the truecolor/style capability switch, since
  a theme bakes its 256-colour or truecolor escapes at creation time.

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
  **`-e`/`--extension`** is parsed into `Extensions` and reported as ignored
  ("this build loads no extensions"), because extension mechanics stay out of
  scope (D41) and silently accepting a path that does nothing is worse than
  saying so; a missing value is an error like the neighbouring flags.
  **`-ne`/`--no-extensions`** is parsed into `NoExtensions` and says nothing: it
  asks for fewer extensions, and there are none. Neither is in `--help` any more,
  and the tool descriptions no longer mention extension tools.
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
- D160 — **the runtime follows the session's directory, and trust is resolved
  there**. Upstream builds a whole runtime per cwd: `main.ts`'s `createRuntime`
  factory takes `sessionManager.getCwd()`, resolves that directory's project
  trust (`resolveProjectTrusted`, its per-cwd cache `projectTrustByCwd`, and —
  only for the initial runtime — the interactive prompt), and builds a settings
  manager, resource loader and session for it. Every session replacement goes
  through it again, so resuming or switching into a session from another
  directory runs in *that* project: its settings, skills, prompt files, context
  files, system prompt and trust. This port keeps one settings manager per
  process, so:
  - **at boot the runtime cwd is the session's**, not the process's: `cmd`
    resolves trust, builds the runtime settings manager and creates the session
    with `sessions.GetCwd()`, which matters exactly when a resumed session
    belongs to another directory — the port used to boot such a session with the
    starting directory's settings and trust. CLI path flags stay
    process-relative, as upstream's `resolveCliPaths(cwd, …)` does.
  - **a switch resets the view the way a replacement does** (upstream
    `rebindCurrentSession` → `renderCurrentSessionState`): the loaded-resources
    and pending-message containers, the compaction queue, the streaming component
    and the pending tools are dropped, and the transcript is re-rendered through
    `renderInitialMessages` — not the reload's `rebuildChatFromMessages`, which
    skips the initial render's trust warning, its footer update and the editor
    history it repopulates. `StartupWiring.RenderCurrentSessionState` had no
    production caller before this.
  - **a switch re-points the settings manager** (`SettingsManager.RebindProject`)
    at the new cwd after resolving its trust with `hasUI` false, so an undecided
    project stays untrusted rather than being asked mid-session (upstream reports
    `hasUI: isInitialRuntime && mode === "interactive"`). Re-pointing rather than
    rebuilding is why no holder of the manager can go stale — a switch that
    rebuilt it would have to reach every wiring that cached it. The run's own
    overrides are re-applied (`--use-theme`), and the settings-derived UI state
    is re-applied (`applySettingsDependentUI`, upstream's `applyRuntimeSettings`
    from `rebindCurrentSession`).
  The trust answer is remembered per project for the run (upstream's
  `projectTrustByCwd`), seeded with the boot answer and filled by each switch, so
  a project resolved once is neither re-read nor re-asked. Theme sources follow
  the same boundary: a project's `.pi/themes` is discovered only for a trusted
  project, and the sources are installed again once trust is decided, because the
  install that the `-r` picker needs happens before any project resource is
  readable.
  What remains is D41's: there are no per-cwd extension services, and the model
  runtime is one process-wide instance. That costs nothing: `models.json` lives
  in the agent dir on both sides (upstream resolves the agent dir once and its
  cwd-bound services carry it over, so switching sessions never changes which
  file is read), and the port re-reads it on `/model` rather than on switch. Everything else about the project does follow: settings,
  skills, prompt templates, context files, system prompt, themes and trust.
- D159 — **the port's session-wide accounting is non-blocking**. Upstream
  computes the `/session` panel (`handleSessionCommand`: the session statistics,
  the cache-waste totals and the usage cost breakdown) inline on its UI loop, and
  re-walks `sessionManager.getEntries()` for the transcript rebuild's cache-miss
  notices. Both are fine upstream because entries hold parsed objects; this port
  stores raw JSON, so each scan re-parsed the session, and the UI loop is one
  goroutine ("dispatch, then render and route input"), which turned a panel into
  a freeze. Measured on a 19k-entry, 45 MB session: typing immediately after
  `/session` became visible only after 0.857s, and the rebuild's cache-miss scan
  cost 563ms cold — on `/reload`, a session switch, `/tree`, compaction end and
  startup.
  Two changes:
  - **the panel is built off the UI loop and posted back to it.** Dispatch
    returns at once, and the finished panel is appended on the next pump. What
    the formatting reads is mutex-guarded session state (SettingsManager,
    SessionManager, the agent's copied state, the projection), never the
    component tree; with no UI to post to (headless wiring) it still computes
    inline, as upstream does. The same session now echoes the keystroke after
    0.038s and the panel appears 0.69s later in the background.
  - **the session-wide accounting is folded, not re-walked.** The manager already
    read every entry's `role`/`usage`/`provider`/`model`/`timestamp` as entries
    arrived, to seed the running scan state that makes a live cache-miss notice
    O(1). That same pass now records what the statistics, the usage cost
    breakdown and the cache-miss scan need, and they account those records with
    `detectCacheMissFor`/the same aggregation the reference functions use — so the
    folded numbers cannot drift — decoding a message only for the misses that are
    actually counted (the transcript keys its notices by the memoized message
    pointer). The rebuild's scan went from 563ms to 30ms on that session, and the
    full scan is still there for callers without a session.
  Everything the panel needs is folded, so the panel itself is fast too: the same
  pass records each message's `role`, `usage`, `provider`, `model`,
  `responseModel`, `timestamp` and — for an assistant message only — the block
  types of its content array, which is what makes the tool-call count exact: the
  role and the array are read, the payloads are skipped, so the pass stays a scan
  rather than a decode. The statistics and the usage cost breakdown are then
  folds over those records. On that session the panel's scans went from 629ms to
  31ms, and the panel is visible 0.080s after the keystroke, against 0.857s for
  the freeze it replaced. `content` stays raw JSON in the facts because a user
  message keeps a string there — decoding it as a block list failed the whole
  record.
  Costs this moved rather than removed: reading the content array raises a load
  of that session from 507ms to 610ms (before the TUI exists at startup, but on
  the UI loop for `/reload` and a session switch), and the reference entry-list
  functions stay for callers without a session.
  The three gaps this D-row recorded are closed: the fold attributes
  `type === "usage"` entries (cache-warming spend) to their `provider/model`, as
  upstream's breakdown and statistics both do; the synthetic context messages
  are stamped from `entry.timestamp` instead of projection time; and a
  compaction's recorded system message is read back and emitted before its
  summary.
- D158 — **the session picker has a stacked layout for narrow terminals**.
  Upstream renders the resume selector — `/resume` in the app and `pier -r`
  (`components/session-selector.ts`) — as a single-line header (title on the
  left, scope/name/sort pinned right) plus one line per session with the
  message count, the age, and optionally the cwd or path in a right-hand column,
  and truncates each of those lines to the width. That is fine on a desktop
  terminal and unusable on a phone: this port is developed on Termux, where a
  portrait terminal is 40-55 columns, and there the header shows the title and
  at most one control, the hints end in `…` before the keys that do the work,
  and the metadata column leaves roughly twenty columns for the session's
  message. Below `mobileTerminalWidth` (60) the picker stacks instead: the title
  keeps its line and the controls move to the next one, the two hint lines are
  wrapped at their separators rather than truncated, and each session becomes
  two lines — the message at the full width, the metadata indented beneath it.
  A screenful therefore holds half as many sessions (`maxVisible/2`, which also
  becomes the page-up/page-down step), and the list's own windowing and paging
  follow the same rule. Above 60 columns nothing changes: upstream's layout is
  reproduced byte for byte, which is why the upstream golden corpus passes
  unchanged there — and why its one 44-column case (`narrow`) is now the single
  corpus render the port deliberately does not reproduce, skipped in
  `sessionselector_test.go` with a pointer here.
- D157 — **path completion treats a trailing `.` or `..` as a directory, not as
  part of the name to split on**. `getFileSuggestions` expands `~` first and then
  splits the result into directory + file prefix, and both `path.join` upstream
  and `filepath.Join` here *clean* the path — so expanding `~/.` collapses the
  `.`, `filepath.Dir` walks up to home's parent and `filepath.Base` hands back
  home's own name. Typing `~/.` and pressing Tab listed `/home` and offered
  `~/dat/` as the single suggestion, which the editor then applied. **Fixed**: the
  raw prefix is split before expanding, a trailing `.` keeps its role as the
  filter (a shell completes `~/.` with dotfiles only) while `..` clears the
  filter and stays in the suggestion. Upstream has the same bug, so the fix is a
  divergence, guarded by TestFileCompletionUnderHomeFiltersInsideHome.
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

- D161 — **the port sweeps its own temp output files**. Full tool output over the
  truncation limits is written to `<tmpdir>/pi-bash-<id>.log` — the shared
  accumulator's `pi-output` and the PowerShell tool's `pi-powershell` prefixes
  likewise — and handed to the model as "[Output truncated. Full output: <path>]".
  Upstream never deletes those files, leaving them to the OS (systemd-tmpfiles, a
  reboot). On a host where `/tmp` is tmpfs that scratch is RAM, and one 174 MB
  command sits there for days: measured on this project's host, 230 files /
  442 MB after three days, none of it read again. The port sweeps its own files,
  oldest first, down to a 256 MB budget, at the moment a temp file is created —
  exactly when the pile grows — and never touches the file being written, another
  tool's log, or a directory whose name happens to match. `TestSweepTempOutputFiles`
  pins that, including the oldest-first order.

- D162 — **print mode is entered only by an explicit flag**. Upstream's `main.ts`
  auto-enters print mode when stdin or stdout is not a TTY, so `echo hi | pi`
  runs headless without `-p`. The port requires `-p` or `--mode json`; without
  one of those it runs the interactive mode (or fails to open a terminal), so a
  non-TTY invocation is never silently answered by a single-shot run. Everything
  else in print mode follows `print-mode.ts`: the `-p` text mode prints the final
  assistant message's text, JSON mode streams the header followed by one line per
  session event with the cumulative `partial` stripped from `message_update`, and
  a prompt failure or an error/aborted stop reason goes to stderr with exit
  code 1. The JSON header line carries the session file's own `"type":"session"`
  discriminator (marshaled via `MarshalFileEntry`), matching upstream's
  `JSON.stringify(sessionManager.getHeader())`. `@file` images are warned and
  ignored (D153); extensions and the runtime-rebind plumbing upstream carries
  are out of scope (D41).

- D163 — **the renderer trims trailing padding over a remote shell**. Upstream
  pads every styled line to the viewport width and emits the padding, so an
  editor keystroke writes roughly a full row per changed row. Over SSH those
  cells are bytes on the wire (and this project's SSH users feel it): measured
  on the port, one keystroke cost 127 bytes, of which ~77 were trailing spaces
  after the visible text. When `SSH_CONNECTION`/`SSH_TTY` is set (or
  `PIER_LOW_BANDWIDTH=1`; `=0` forces the upstream form), the renderer trims
  trailing spaces from each emitted row, relies on the per-row `[2K` (already
  emitted) to clear the remainder, and skips the `[2K` when the new row is at
  least as wide as the old one or after a full-screen clear. The visible result
  is identical; the same keystroke drops to 48 bytes (−62%). The default path is
  untouched so the upstream-parity goldens keep asserting the padded bytes.

- D164 — **input paints only when the dispatch asked for one**. The interactive
  loop's input arms used to call `paint()` for every raw stdin chunk. Fullscreen
  mode enables `?1003h` (any-motion mouse tracking), so the terminal reports
  every pointer movement — one event per pixel — and one frame is O(the whole
  transcript): on a large session the loop spent its entire budget on full
  repaints, and mouse movement (and every keystroke queued behind it) was
  unusable. The arms now drain every raw chunk already queued, then paint once,
  and only if the dispatch queued a render request (`paintIfRequested` consumes
  the coalesced tick that `RequestRender` left behind, which also stops the 16 ms
  frame timer from repainting the same request). Measured on the port: 60 mouse
  moves plus one keystroke cost 2–3 paints, down from 63. Input that asks for no
  paint is therefore not painted; the renderer's contract is that components
  call `RequestRender` and every keyboard path signals one in
  `HandleTerminalInput`, so only events that changed nothing (a bare move, a key
  release, a terminal reply) skip a frame. Two supporting changes: the loop
  caches the renderer's animation walk between paints (the walk visits every
  mounted component and the loop asked for it once per input event), dropped by
  the paint that can change a component's animation state; and `VisibleWidth`
  short-circuits printable-ASCII text after stripping sequences, because a
  styled line never reached the plain-ASCII fast path and instead allocated a
  grapheme slice and range-searched twice per character (5.0 µs → 1.0 µs, 1104 →
  324 B/op on a styled tool-output line). Tests:
  `TestMouseMotionBurstDoesNotRepaint`, `TestAnimationScanCacheIsDroppedByAPaint`,
  `TestAltScreenMouseMoveRequestsNoRender`, `TestVisibleWidthStyledTextMatchesPlainText`.
- D165 — **the terminal background probe is listener-driven, not a blocking
  query**. Upstream detects the terminal's light/dark preference by querying it
  synchronously at theme-apply time (`queryTerminalBackgroundColor` waits on a
  channel, `setInterval`-free). The port cannot: the UI loop is the only
  dispatcher of terminal replies (`Renderer.HandleTerminalInput`), so a query
  called on the loop can never receive its own reply and always times out, and a
  query written before the pty is in raw mode is echoed back into the input
  stream (this is what froze the TUI over SSH when the feature was first
  attempted). The port instead writes a single OSC 11 query
  (`Renderer.RequestTerminalBackgroundColor`, `ESC ] 11 ; ? BEL`) after the
  terminal is started and returns immediately; the reply is consumed in
  `HandleTerminalInput`, classified by luminance (`GetThemeForRgbColor`), and
  applied by a `OnTerminalBackgroundColorChange` listener on the loop. The
  `CSI ? 2031` color-scheme notification toggle is likewise deferred: a request
  before `Start` only flips the flag and `Start` replays it, so the mode sequence
  is never echoed. `app.theme.ProbeTerminalBackground` is invoked from
  `RunWiring.OnStarted` (after `UI.Start`, i.e. raw mode + live reader) and
  re-armed on a renderer swap (`RebindTUI`). A terminal that does not answer
  leaves the `COLORFGBG`/fallback theme in place; nothing blocks on the reply.

- D166 — **the global theme is a stable handle, mirroring upstream's `theme`
  Proxy**. Upstream exports `theme` as a `Proxy` that reads
  `globalThis[THEME_KEY]` on every property access, so a component that calls
  `theme.fg(...)` at render always sees the current theme and a switch needs no
  rebuild (`onThemeChange` only invalidates and refreshes the editor border).
  The port returned the current concrete `*Theme` from `ActiveTheme()`;
  components captured that pointer at construction, so a switch left the
  transcript with the old colors and the first fix attempt added an ad-hoc
  `App.rebuildForTheme`. `ActiveTheme()` now returns one stable `*Theme` handle;
  `Fg`/`Bg`/`GetFgAnsi`/`GetBgAnsi`/`ColorMode` forward to `CurrentTheme()` on
  every call (`Theme.concrete`), factories such as `GetMarkdownTheme` close over
  the handle, and `Renderer.Invalidate()` clears the render caches so the next
  paint re-resolves. The header and other components that bake `theme.fg(...)`
  into a `Text` at construction stay baked in both implementations, so
  `onChanged` remains upstream's `updateEditorBorderColor`. Tests:
  `TestThemeHandleResolvesTheCurrentTheme`,
  `TestMarkdownThemeRecolorsAfterASwitch`.

- D167 — **a slice keeps ANSI order across its start boundary**. Upstream's
  `sliceWithWidth` (`utils.ts`) writes a code met inside the range as it is met,
  but buffers the codes from before the range and flushes them when the first
  text is written. A code that applies at the range's first column therefore
  lands ahead of the prefix it belongs after. A selection ending exactly where an
  inline-code span's reset sits is the case that shows it: the span renders
  without its backticks, so a double-click on the word inside it ends on the
  reset, the tail slice wrote the reset before the colour it cancels, and the
  rest of the line kept the span's colour — the remaining text turned yellow.
  The port flushes the carried prefix before writing an in-range code. The golden
  corpus is unchanged (it never covers a slice starting on a reset), and
  `tui/sliceansiorder_test.go` pins the order.

- D168 — **the Escape handler does not wait for the session to go idle**.
  `AgentSession.Abort` cancels the turn and then waits for idle; upstream's
  `session.abort()` is awaited the same way, but awaiting a promise in JavaScript
  yields to the event loop, so the window keeps painting and keeps taking input.
  The port's `waitForSessionIdle` parks the goroutine instead, and the Escape
  handler runs on the UI loop: pressing Escape while a turn streamed froze every
  later keystroke for as long as the turn took to unwind. Measured on a live
  session — `slow UI phase "raw-input" 188ms` with the loop parked in
  `waitForSessionIdle` (`HandleEscape` → `RestoreQueuedMessagesToEditor` →
  `Abort`) — while the neighbouring keystrokes showed `read=8.8ms write=91ms`.
  `AbortAsync` is the loop-safe half: it cancels the retry, the bash run and the
  agent run, and returns. `Abort` keeps its wait for the two callers that need
  the session settled before they continue — session replacement
  (`sessionswitch.go`, which disposes the session next) and tree navigation
  (`interactivemode_selectors.go`, which reads the tree next) — so those two stay
  deliberately blocking. The queue restore already happens before the abort, so
  nothing on the loop needs the turn to have unwound. Tests:
  `TestEscapeHandlerDoesNotWaitForIdle`.
