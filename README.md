# pi (Go) — a faithful port of [pi](https://github.com/earendil-works/pi)

This is a from-scratch Go port of Mario Zechner's [pi](https://github.com/earendil-works/pi)
(`@earendil-works/pi-ai`, `pi-agent-core`, `pi-coding-agent`). The TypeScript
monorepo is **ground truth**: every type, field name, and behavior is ported
from upstream source, not inferred.

## Ground-truth rules

1. **Upstream is the spec.** A port commit must reference the upstream file
   (and commit, when porting a change) it mirrors.
2. **JSON is byte-parity.** Transcripts, session files, and wire payloads
   must round-trip identically to pi's: same key names, same key order, no
   HTML escaping (use `ai.MarshalJSON`, never `json.Marshal`, on anything
   that ends up on disk or the wire). JS object key order is tracked
   explicitly (see `SystemMessage` sections).
3. **JS semantics where pi relies on them.** JS object insertion order,
   `JSON.stringify` behavior, truthiness at API boundaries — divergences get
   a numbered D-row in `docs/PORTING.md` with the reproducing scenario.
4. **Every behavior change is test-observed.** Tests are ported from
   upstream's `test/` files where they exist; Go-only tests must assert the
   upstream-derived expectation, with the upstream source referenced in a
   comment.

## Upstream pin

| What | Value |
|---|---|
| Repository | https://github.com/earendil-works/pi (cloned at `../pi`) |
| Pin | `36b60d2e8` — "fix: clean up delta test lint diagnostics" |
| Stale upstream test | `packages/ai/test/faux-provider.test.ts` "estimates prompt and output tokens" still expects pre-`9e05370b2` faux serialization; source wins |

Update the pin whenever upstream source is re-read for a port.

## Layout (mirrors upstream `packages/`)

| Go package | Upstream | Status |
|---|---|---|
| `ai` | `packages/ai` core (`types.ts`, `utils/event-stream.ts`, `utils/text.ts`, `utils/transcript.ts`, `utils/diagnostics.ts`) | ported |
| `ai` | `packages/ai` models/compat (`models.ts` surface, compat interfaces) | ported (types) |
| `ai/providers` (faux) | `packages/ai/src/providers/faux.ts` | ported |
| `ai` (auth) | `packages/ai/src/auth/*` (types, resolve, credential-store, helpers) | ported |
| `ai` (models runtime) | `packages/ai/src/models.ts`, `models-store.ts`, `api/lazy.ts` (lazyStream) | ported |
| `ai` (catalog) | generated model data via upstream's own generator (`scripts/gen_catalog.py`), `env-api-keys.ts`, `utils/provider-env.ts` | ported |
| `ai` (anthropic wire) | `api/anthropic-messages.ts` request construction (`buildParams`), `transform-messages.ts`, `simple-options.ts`, `constrained-sampling.ts`, `utils/{json-parse,estimate,sanitize-unicode}.ts` | ported |
| `ai` (google-vertex) | `api/google-vertex.ts` — Vertex AI dialect over the shared Google streaming loop: `{location}`-templated and custom base URLs with API-version detection, project/location resolution with upstream's error messages, Vertex API keys (including the ADC marker and `<placeholder>` fallback), Application Default Credentials (authorized-user refresh-token and GCE metadata flows with token caching; service-account keys reported per D27), bearer + `x-goog-user-project` headers, and the Vertex thinking budget/level mapping | ported |
| `ai` (azure-openai-responses) | `api/azure-openai-responses.ts` — Azure dialect over the shared Responses loop: base-URL resolution (options → env → resource name → model base URL) with Azure-host normalization to `/openai/v1`, query stripping only on Azure hosts, `v1` default API version, the `api-version` query parameter, `api-key` header auth, deployment-name resolution (explicit → `AZURE_OPENAI_DEPLOYMENT_NAME_MAP` → model id) sent as the body model, and the Azure tool-call provider set | ported |
| `ai` (mistral) | `api/mistral-conversations.ts` — the native Mistral chat completions endpoint: SDK-to-wire key remapping (maxTokens/toolChoice/promptMode/... and per-message/per-chunk keys), message conversion (string user content, image data URLs with omission notes, assistant thinking parts and tool calls, tool-result text forms), function tools with strict sampling, deterministic 9-character tool-call id normalization, prompt caching via `prompt_cache_key` plus the `x-affinity` header, the CRLF-tolerant SSE event reader with the `[DONE]` sentinel, streaming assembly of text/thinking/tool-call blocks with incremental argument parsing, cached-token usage accounting, reasoning effort vs prompt mode per model, and upstream's error formatting with body truncation | ported |
| `ai/providers` (factories) | `providers/{all,thin factories,opencode-headers,github-copilot,google-vertex,azure-openai-responses,mistral}.ts` — the built-in provider list in upstream order with per-provider base URLs, names, and env API-key auth (28 thin specs plus the anthropic/google/vertex/openai/azure/mistral/copilot factories), the OpenCode session-header wrapper, the GitHub Copilot OAuth available-model filter, a `builtinProviders`/`builtinModels` aggregate, and spec/catalog api consistency | ported (amazon-bedrock, cloudflare, openai-codex, radius factories await their adapters) |
| `ai` (cloudflare) | `providers/{cloudflare-auth,cloudflare-stream}.ts` + the endpoint constants in `api/cloudflare.ts` — per-field credential/env merging for the API key plus account/gateway ids (a key-only credential still picks those up from the environment), the Workers AI key auth vs. the AI Gateway `cf-aig-authorization` form with the SDK placeholder headers suppressed, the login prompts, `{CLOUDFLARE_ACCOUNT_ID}`/`{CLOUDFLARE_GATEWAY_ID}` base-URL materialization before dispatch (cloning only when the URL changes), and both providers registered with the gateway's three-API map pinned | ported (the Workers AI binding fetch is Workers-runtime specific and out of scope) |
| `ai` | openai-codex, bedrock adapters; image providers; OAuth login flows | queued |
| `agent` | `packages/agent/src/{agent-loop.ts, types.ts, stream-fn.ts}` — the loop, tool execution (sequential/parallel), steering/follow-up queues, terminate rules, validation (D6 subset) | ported |
| `agent` (Agent) | `agent.ts` — stateful wrapper: state, subscribe, steer/follow-up queues (one-at-a-time default), continue semantics, run-failure lifecycle, reset baseline | ported |
| `agent` | harness/ (compaction, context, execution) | queued |
| `coding` (tools) | `core/tools/{truncate,path-utils,file-mutation-queue,read,write,ls}.ts` | ported |
| `coding` (edit) | `core/tools/{edit.ts, edit-diff.ts}` — exact/fuzzy matching, overlap detection, CRLF/BOM preservation, diffs | ported |
| `coding` (search) | `core/tools/{find.ts, grep.ts}` — real fd/rg backends, gitignore respect, relativized output, match/context formatting | ported |
| `coding` (bash) | `core/tools/bash.ts` + `output-accumulator.ts` — bounded-memory streaming, temp-file full output, tree-kill, 128+N signal mapping, timeouts, aborts, PI_* env handling | ported |
| `coding` (sessions) | `core/session-manager.ts` — v3 JSONL trees, branching, compaction context, migrations, discovery/listing; `core/messages.ts` custom roles | ported |
| `coding` (prompt) | `core/system-prompt.ts` (structured sections, forced prompts, section diffing) + `core/skills.ts` + `utils/frontmatter.ts` | ported |
| `coding` (compaction) | `core/compaction/{compaction.ts, utils.ts}` — cut points, token estimation, summarization (retry-wrapped), file-op tracking; `core/messages.ts` convertToLlm | ported |
| `coding` (session facade) | `core/agent-session.ts` (extension-free reduction: persistence, queue tracking, auto-compaction/retry, stats, skill blocks) + `core/defaults.ts` | ported |
| `coding` (powershell + registry) | `core/tools/powershell.ts` (PowerShell variant of the shared shell tool: `-NoProfile -NonInteractive -ExecutionPolicy Bypass -Command` args, UTF-8 console prefix, PS> prompt, pi-powershell temp files, Windows-only exec failure), `core/tools/index.ts` (tool names, `createTool`, coding/read-only/all tool sets), `utils/shell.ts` (bash resolution incl. legacy-WSL stdin transport and Git Bash discovery, PowerShell resolution preferring pwsh, binary-output sanitizing) | ported |
| `coding` (model resolution) | `core/model-resolver.ts` — default models per provider, exact reference matching (bare id or canonical provider/model, ambiguous bare ids rejected), fuzzy partial matching preferring aliases over dated ids, colon-aware thinking-level parsing (OpenRouter-style `model:exacto:high`), scope resolution with glob matching and structured diagnostics, CLI model resolution (provider inference from `provider/model`, authenticated-provider tie-breaks for ambiguous ids, provider/model split preference over gateway ids, custom-id fallback with `:thinking` stripping), initial model priority chain, and session model restoration; `utils/shell.ts`-style glob semantics in `coding/glob.go` | ported |
| `coding` (settings) | `core/settings-manager.ts` + `core/http-dispatcher.ts` timeout parsing — the full settings document (all upstream fields), deep merge (nested objects merge, arrays/primitives replace, undefined skipped), the four legacy migrations (queueMode, websockets, skills object, retry.maxDelayMs), ordered set-aware JSON writing, locked file storage (proper-lockfile semantics) and in-memory storage, scoped managers with global/project trust, reload, overrides, recorded errors, modified-field persistence that preserves externally added keys, and the complete getter/setter surface (models, thinking levels, compaction token resolution, retry, HTTP idle timeouts, editor, shell, resources, terminal/TUI, markdown, warnings) | ported |
| `coding` (cli + json events) | `cli/args.ts` (full flag parser: value flags, shorthands, `--` separator, @file args, unknown-flag capture for extensions, thinking-level validation diagnostics, session-name normalization, help text) and `modes/json-event.ts` (streaming `message_update` events stripped of cumulative partials, toolcall_start enriched with id/toolName, cumulative usage preserved, passthrough payloads for the other event types) | ported |
| `coding` (context files) | `core/resource-loader.ts` context discovery + `core/footer-data-provider.ts` git path resolution — AGENTS.override.md/AGENTS.md/AGENTS.MD/CLAUDE.md/CLAUDE.MD preference per directory, directory candidates skipped, BOM stripping, global agent file first then ancestors outermost-to-innermost with path dedup, and the nested-worktree shadow rule (commondir/worktree detection, bare-layout and submodule exclusions) | ported |
| `coding` (prompt templates) | `core/prompt-templates.ts` + `core/source-info.ts` — bash-style command argument parsing (including the implementation's literal-backslash quote behaviour), the full placeholder vocabulary ($N, $@, $ARGUMENTS, `${N:-default}`, `${@:-default}`, `${@:N}`, `${@:N:L}`) with non-recursive substitution, template loading from global/project/explicit paths (markdown only, symlink-to-file resolution, broken symlinks skipped), frontmatter description/argument-hint with the first-line truncation fallback, source info (user/project/temporary, top-level origin), and `/name args` expansion | ported |
| `coding` (file args + mime) | `cli/file-processor.ts`, `utils/mime.ts`, and the inline-image limits from `utils/image-resize*.ts` — @file argument expansion (path resolution, empty files skipped, missing/unreadable files erroring, BOM-stripped text wrapped in `<file name>` blocks), image sniffing (JPEG with the JPEG-2000 marker rejected, PNG with IHDR validation and animated-PNG detection, GIF, WebP, structurally validated BMP), and inline-image preparation (MIME normalization, byte-identical pass-through within the 2000x2000/4.5MB limits, conversion hints, dimension notes, upstream omission messages) | ported |
| `coding` (print mode) | `modes/print-mode.ts` (extension-free core) — JSON mode writing the session header before one JSON line per event, text mode printing the final assistant message's text blocks, the error/aborted stop-reason path reporting to stderr with exit code 1, initial images reaching the provider, sequential prompts, and prompt-failure exit handling | ported |
| `coding` | modes (interactive TUI, rpc runner), remaining resource loading (packages, themes) | queued |
| `protocol` | `packages/protocol` — strict definite-length CBOR subset with limits, 4-byte length framing, v8 message schemas (hello/request/cancel/response/service_update/attachment), strict validation codecs | ported |
| `client` | `packages/client` — transport contract, connection state machine (handshake validation, sequence fencing, fail/close semantics), requests with cancel-on-abort, attachment routing, state listeners, service catalogue + subscriptions (hydration, queued updates, start gating, dispose), Chord service transport, dispose | ported |
| `chord/services` | `packages/chord/src/services/{wire,state-codec,errors}.ts` — control calls, strict snapshot/update validation, per-subscription state codec registries, remote-service error codes | ported |
| `chord/delta` | `packages/chord/src/delta` — op vocabulary + wire compression, diff engine, applier (mutable/immutable), validation; JSON value contract (`chord` root) | ported |
| `telemetry` | `packages/telemetry` — span/context contracts, noop + in-memory recorder (settlement, explicit-vs-automatic status, atomic recording), schema definition data, typed span starter, and the adapter conformance suite | ported |
| `durable` | `packages/durable` — conversation/entry/task/input/document record types, the Storage atomic persistence contract, and the detached in-memory reference storage (commit sequences, id ownership, cursors, fork-aware history, task/input indexes) | ported |
| `server` | `packages/server` core — connection state machine (handshake timeout/version checks, hello_error failures, inbound pump ordering), request dispatch (service-call parsing, session vs server-scope routing, subscribe snapshot encoding with update flush ordering, unsubscribe, cancel), SessionRouter (per-client serialization, attachment leases, session open dedup, terminate invalidation), bounded error mapping, drain/close | ported |
| `server` (unix) | `transports/unix/*` — socket path derivation, listener with stale-socket probing (live-listener refusal, rename-and-verify removal), atomic bind path + hard link + mode, owned-socket cleanup by device/inode identity, per-connection byte accounting, graceful final-chunk close with force-close timer, `createUnixServer` preset | ported |
| `server/testing` | `testing/*` — `Deferred`, scriptable `TestHarness` (gated close/service calls, release counting), `TestServerHost` (seeded metadata, ambiguity, gated opens), `pi.session-management` test services, `ProtocolTestClient` with fragmented sends and attachment tracking, `connectUnixTestClient`, `createTestServer` | ported |
| `chord/facets` | `facets/{host,loader}.ts` + the facet surface of `api.ts` — `FacetKernel` (facet setup with setup/active/disposing/dead lifecycle guards and revoking service access, provision recording and shape comparison, remote+local+deferred service-source resolution with duplicate-offer rejection, provider/internal-binding/local-keyed assembly, dependency validation with mode and ownership checks, topological activation order with cycle detection, connect/ready gating, reload with staged candidates, replacement validation, cutover, retirement and abort, terminate/dispose error collection), `HostServiceSlots` (per-service slots with late-binding guarded views), the keyed-spawner staging used by reload, `LocalKeyedServiceRegistry`, and the static/combined facet loaders | ported |
| `chord/services` (provider/consumer) | `services/{provider,consumer,handle,loopback,errors}.ts` — `RemoteServiceProvider` (catalogue validation, singleton provide/withdraw/validate/replace with member-shape preservation, keyed spawn/generation/close, coded invoke errors, subscriptions with pre-activation buffering, dispose), `createRemoteServiceEndpoint` (catalogue/subscribe/unsubscribe control calls, update publishing, subscription disposal), `createLoopbackServiceTransport`, and the consumer `RemoteServiceBinding` (allowlist + mode exclusivity, facade member handles with kind tracking and access guards, singleton snapshots/replacement/unavailable, keyed instance directory with guarded observations, ready gating, rebind, dispose) | ported |
| `chord/services` (replicated state) | `services/{state,state-internals,instances}.ts` — host-owned mutable state (deep-clone initial publication, diff-based publish, op-batch source listeners, hydrate-then-update deliveries), the cold consumer replica (base-batch-only hydration, sequence-gap clearing, listener-failure reporting), the internals lookup, and the keyed instance directory (generation identity, readiness gating, cancellable per-instance observation tasks, reset/dispose) | ported |
| `chord` (node bundler: esbuild/rolldown JS facet bundling), `tui` | upstream packages | queued |
| `chord` | `packages/chord` | queued |

## Build & test

```bash
go build ./...
go test -race ./...
```
