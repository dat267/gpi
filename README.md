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
| `ai` | anthropic-messages SSE streaming + HTTP client; openai-*/google-* APIs; remaining provider factories | queued |
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
| `coding` | powershell tool (windows), CLI/modes, TUI | queued |
| `protocol` | `packages/protocol` — strict definite-length CBOR subset with limits, 4-byte length framing, v8 message schemas (hello/request/cancel/response/service_update/attachment), strict validation codecs | ported |
| `client` | `packages/client` — transport contract, connection state machine (handshake validation, sequence fencing, fail/close semantics), requests with cancel-on-abort, attachment routing, state listeners, service catalogue + subscriptions (hydration, queued updates, start gating, dispose), Chord service transport, dispose | ported |
| `chord/services` | `packages/chord/src/services/{wire,state-codec,errors}.ts` — control calls, strict snapshot/update validation, per-subscription state codec registries, remote-service error codes | ported |
| `chord/delta` | `packages/chord/src/delta` — op vocabulary + wire compression, diff engine, applier (mutable/immutable), validation; JSON value contract (`chord` root) | ported |
| `telemetry` | `packages/telemetry` — span/context contracts, noop + in-memory recorder (settlement, explicit-vs-automatic status, atomic recording), schema definition data, typed span starter, and the adapter conformance suite | ported |
| `durable` | `packages/durable` — conversation/entry/task/input/document record types, the Storage atomic persistence contract, and the detached in-memory reference storage (commit sequences, id ownership, cursors, fork-aware history, task/input indexes) | ported |
| `server` | `packages/server` core — connection state machine (handshake timeout/version checks, hello_error failures, inbound pump ordering), request dispatch (service-call parsing, session vs server-scope routing, subscribe snapshot encoding with update flush ordering, unsubscribe, cancel), SessionRouter (per-client serialization, attachment leases, session open dedup, terminate invalidation), bounded error mapping, drain/close | ported |
| `server` (unix) | `transports/unix/*` — socket path derivation, listener with stale-socket probing (live-listener refusal, rename-and-verify removal), atomic bind path + hard link + mode, owned-socket cleanup by device/inode identity, per-connection byte accounting, graceful final-chunk close with force-close timer, `createUnixServer` preset | ported |
| `server/testing` | `testing/*` — `Deferred`, scriptable `TestHarness` (gated close/service calls, release counting), `TestServerHost` (seeded metadata, ambiguity, gated opens), `pi.session-management` test services, `ProtocolTestClient` with fragmented sends and attachment tracking, `connectUnixTestClient`, `createTestServer` | ported |
| `chord` (facets, bundler, replicated-state runtime), `tui` | upstream packages | queued |
| `chord` | `packages/chord` | queued |

## Build & test

```bash
go build ./...
go test -race ./...
```
