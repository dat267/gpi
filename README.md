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
| `agent` | `agent.ts` (stateful Agent), harness/ | queued |
| `coding` | `packages/coding-agent` core (tools, system prompt, sessions) | queued |
| `chord` | `packages/chord` | queued |

## Build & test

```bash
go build ./...
go test -race ./...
```
