# code-cli

Go utilities for the Claude Code rewrite work.

## Phase 0 API audit

The first command implements the Phase 0 audit for `claude-code-source/src/services/api`:

- list exported symbols from the API service folder
- find external callers outside that folder
- produce a compatibility matrix with keep/replace/remove guidance

Run from this folder:

```bash
go run ./cmd/code-cli
```

Or run against an explicit repository root:

```bash
go run ./cmd/code-cli -repo /workspace/cc
```

JSON output is also available:

```bash
go run ./cmd/code-cli -format json
```

## Phase 1 API contracts

Phase 1 adds normalized Go contracts for the future Claude API boundary:

- `internal/core`: shared message, content block, model, config, usage, and error types
- `internal/anthropicapi`: API-facing request, response, stream event, token-counting, and call option types

This phase intentionally does not implement the API client yet. Client construction, SDK calls, streaming parsing, retries, and error classification come in later phases.

## Phase 2 SDK-backed Claude API client

Phase 2 implements the `internal/anthropicapi.Client` boundary with the official Anthropic Go SDK:

- constructs an SDK-backed Claude client from `core.APIConfig`
- converts normalized message and token-count requests into SDK parameters
- normalizes message responses, stream events, usage, and API errors back into `internal/core` types
- preserves prompt-cache usage accounting with `cache_creation_input_tokens` and `cache_read_input_tokens`

Default tests do not make live Claude API calls. Live smoke tests should stay opt-in through an explicit environment variable such as `ANTHROPIC_API_KEY`.

## Phase 3 retry and client robustness

Phase 3 adds a bounded retry layer around the SDK-backed client:

- configurable retry defaults live in `internal/core`
- per-call retry overrides are available through `internal/anthropicapi` call options
- transient rate-limit, overloaded, timeout, network, and server errors retry with exponential backoff
- stream requests retry only setup-time failures before any events are returned

Claude Code-specific behavior from the TypeScript runtime, such as fast-mode fallback, OAuth refresh, telemetry, provider credential cache clearing, unattended persistent retries, and model fallback, is intentionally deferred until the surrounding Go runtime layers exist.

## Phase 4 control-plane API endpoints

Phase 4 adds a separate `internal/controlplane` package for authenticated non-Messages HTTP endpoints from the TypeScript API layer:

- usage utilization from `usage.ts`
- admin request create/list/eligibility flows from `adminRequests.ts`
- bootstrap fetch/parsing from `bootstrap.ts`
- first-token-date fetch/validation from `firstTokenDate.ts`

The package uses an injectable standard-library HTTP client and typed JSON contracts so behavior can be covered with `httptest` and no live Claude API calls. Runtime-coupled behavior remains deferred, including logging, prompt-cache break detection, files API upload/download behavior, OAuth refresh, subscriber/profile gating, and persistence into global config.

## Phase 5 control-plane hardening

Phase 5 tightens the Phase 4 control-plane layer:

- control-plane calls use an explicit production OAuth base URL default and still allow caller-provided staging/local/custom base URLs
- organization-scoped admin request methods reject empty organization UUIDs before sending requests
- tests cover malformed success responses, empty response bodies, request ID fallback headers, base URL path joining, and top-level `null` admin request lists

OAuth environment resolution, token refresh, and global config persistence remain deferred to a future auth/config phase.

## Phase 6 additional control-plane endpoints

Phase 6 expands the typed control-plane surface with two more small HTTP endpoints from the TypeScript API layer:

- ultrareview quota lookup from `ultrareviewQuota.ts`, including organization UUID validation and `x-organization-uuid` propagation
- overage credit grant lookup from `overageCreditGrant.ts`, including organization-scoped path validation and USD grant amount formatting

The Go layer intentionally exposes direct fetch/format primitives only. Subscriber gating, privacy checks, cache TTLs, global config persistence, and fire-and-forget refresh behavior remain deferred until auth/config/runtime infrastructure exists.
