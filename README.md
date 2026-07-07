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
