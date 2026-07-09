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

## Phase 7 referral control-plane endpoints

Phase 7 migrates the direct guest-pass referral HTTP calls from `referral.ts`:

- referral eligibility lookup at `/api/oauth/organizations/{orgUUID}/referral/eligibility`
- referral redemptions lookup at `/api/oauth/organizations/{orgUUID}/referral/redemptions`
- default campaign handling for `claude_code_guest_pass`
- organization UUID validation, `x-organization-uuid` propagation, and endpoint-specific timeouts
- referrer reward formatting for the currency symbols used by the TypeScript UI

The Go package still does not implement subscriber/max-plan gating, in-memory fetch de-duplication, 24-hour cache expiry, background refresh, or global config persistence. Those remain deferred to the future auth/config/runtime layer.

## Phase 8 metrics opt-out endpoint

Phase 8 migrates the direct metrics-enabled check from `metricsOptOut.ts`:

- metrics logging status lookup at `/api/claude_code/organizations/metrics_enabled`
- typed `metrics_logging_enabled` response parsing
- request behavior matching the TypeScript direct fetch path, including JSON content type and a 5-second endpoint timeout
- auth/user-agent propagation through the existing control-plane client header configuration

The Go package intentionally exposes only the direct fetch primitive. OAuth 401 retry, profile-scope gating, essential-traffic checks, in-memory memoization, disk cache TTLs, background refresh, and config persistence remain deferred to the future auth/config/runtime layer.

## Phase 9 Grove control-plane endpoints

Phase 9 migrates the direct Grove HTTP calls and notice decision helper from `grove.ts`:

- account settings lookup at `/api/oauth/account/settings`
- Grove notice viewed marker at `/api/oauth/account/grove_notice_viewed`
- Grove setting update through `PATCH /api/oauth/account/settings`
- Grove notice config lookup at `/api/claude_code_grove`, including TypeScript-compatible defaults for omitted `domain_excluded` and `notice_is_grace_period`
- pure `CalculateShouldShowGrove` logic for deciding whether the notice should render

The Go package still does not implement OAuth 401 retry, essential-traffic gating, consumer-subscriber qualification, session memoization, 24-hour cache refresh, analytics, stderr messaging, graceful shutdown, or global config persistence. Those remain runtime-layer responsibilities.

## Phase 10 Files API read/list foundation

Phase 10 starts migrating the public Files API client from `filesApi.ts` into a new `internal/filesapi` package:

- Files API client construction with injectable `http.Client`, configurable base URL, OAuth bearer token, and TypeScript-compatible Anthropic version/beta headers
- file content download from `/v1/files/{file_id}/content` with bounded retry for transient failures
- file metadata listing from `/v1/files` with `after_created_at` filtering and `after_id` pagination
- file attachment spec parsing for `file_id:relative_path` inputs, including space-expanded specs
- download path construction under `{basePath}/{sessionID}/uploads`, including traversal rejection and redundant prefix stripping

Upload, filesystem write orchestration, download concurrency, analytics events, debug logging, environment-driven base URL selection, and BYOC/1P mode integration remain deferred to later Files API phases.

## Phase 11 Files API upload foundation

Phase 11 adds the single-file upload path from `filesApi.ts` to `internal/filesapi`:

- multipart `POST /v1/files` upload with `file` and `purpose=user_data` parts
- local file read and size validation against the TypeScript 500 MB limit
- typed `UploadResult` success/failure output matching the TypeScript caller shape
- non-retryable handling for auth, forbidden, and too-large responses
- bounded retry for transient upload failures, rebuilding the multipart body for each attempt

Batch upload orchestration, concurrency limiting, filesystem download/save helpers, analytics events, debug logging, cancellation wiring into higher-level commands, and environment-driven base URL selection remain deferred to later runtime integration phases.

## Phase 12 Files API session orchestration

Phase 12 adds the next Files API orchestration layer around the read/list and upload primitives:

- download-and-save support that writes Files API content under `{basePath}/{sessionID}/uploads`
- session download orchestration with bounded concurrency and stable result ordering
- session upload orchestration for local files with bounded concurrency and stable result ordering
- typed `DownloadResult` and `LocalFile` shapes for higher-level runtime integration
- deterministic filesystem and HTTP tests for success, invalid paths, ordering, and multipart batch uploads

Analytics events, debug logging, command-layer cancellation UX, environment-driven base URL selection, BYOC/1P mode decisions, and integration with higher-level session startup/runtime flows remain deferred to future phases.

## Phase 13 Files API runtime configuration parity

Phase 13 tightens Files API configuration parity with `filesApi.ts`:

- default base URL resolution now follows `ANTHROPIC_BASE_URL`, then `CLAUDE_CODE_API_BASE_URL`, then `https://api.anthropic.com`
- upload calls use a separate 120-second default timeout, matching the TypeScript upload path rather than the 60-second download/list timeout
- callers can override the upload timeout independently through `Config.UploadTimeout`
- tests cover environment precedence, default timeout values, and explicit upload timeout configuration

Analytics events, debug logging, command-layer cancellation UX, BYOC/1P mode decisions, and integration with higher-level session startup/runtime flows remain deferred to future phases.

## Phase 14 session ingress foundation

Phase 14 starts migrating the transcript persistence paths from `sessionIngress.ts` into a new `internal/sessioningress` package:

- session log append via `PUT /v1/session_ingress/session/{sessionID}` with bearer auth and JSON content type
- optimistic append ordering with cached `Last-Uuid` state per session
- 409 conflict recovery when the server returns `x-last-uuid`, including the “already stored” success case
- session log fetch via `GET /v1/session_ingress/session/{sessionID}`, including optional `after_last_compact=true`
- typed raw transcript entry handling plus helpers to clear per-session or all cached append state
- deterministic `httptest` coverage for fetch, append, 401 handling, 404-as-empty, conflict recovery, and cache clearing

JWT discovery, OAuth token refresh, per-session sequential execution wrappers, diagnostics/debug logging, Teleport Events pagination, and integration with the higher-level transcript runtime remain deferred to later phases.

## Phase 15 Teleport Events foundation

Phase 15 extends `internal/sessioningress` with the CCR v2 Teleport Events read path from `sessionIngress.ts`:

- transcript event fetch from `/v1/code/sessions/{sessionID}/teleport-events`
- bearer auth and optional organization UUID propagation through the shared session ingress client
- pagination with `limit=1000`, opaque cursor echoing, and a 100-page guard
- null payload filtering while preserving raw transcript payloads as `Entry` values
- TypeScript-compatible 404 behavior: first-page 404 returns no events, while later-page 404 returns the partial transcript already fetched
- deterministic tests for pagination, null payload filtering, 404 handling, auth errors, and the page cap

JWT discovery, OAuth token refresh, diagnostics/debug logging, fallback coordination between Teleport Events and legacy session ingress, and integration with the higher-level transcript runtime remain deferred to later phases.

## Phase 16 session ingress append sequencing

Phase 16 hardens the session ingress append path to match the per-session sequential behavior in `sessionIngress.ts`:

- concurrent appends for the same session now run through a per-session lock before reading or updating optimistic `Last-Uuid` state
- appends for different sessions can still proceed concurrently, avoiding a process-wide transcript append bottleneck
- cached last-UUID state is protected by a mutex for append, fetch, conflict recovery, and clear operations
- deterministic `httptest` coverage verifies same-session serialization, cross-session concurrency, and continued `Last-Uuid` chaining

JWT discovery, OAuth token refresh, diagnostics/debug logging, fallback coordination between Teleport Events and legacy session ingress, and integration with the higher-level transcript runtime remain deferred to later phases.

## Phase 17 session transcript fallback coordination

Phase 17 adds the higher-level transcript read coordination used by teleport flows in `sessionIngress.ts`:

- `FetchSessionTranscript` tries CCR v2 Teleport Events before the legacy session ingress endpoint
- first-page Teleport Events 404 and non-auth failures fall back to `GET /v1/session_ingress/session/{sessionID}`
- Teleport Events auth failures remain non-fallback errors so expired credentials are surfaced directly
- callers receive a typed `TranscriptSource` value identifying whether transcript entries came from Teleport Events or legacy session ingress
- clearing cached session state now also clears per-session append locks, matching the TypeScript cache cleanup path
- deterministic tests cover Teleport Events success, fallback on 404/server failure, auth-error non-fallback, and append-lock cleanup

JWT discovery, OAuth token refresh, diagnostics/debug logging, command-layer teleport progress integration, and integration with the higher-level transcript runtime remain deferred to later phases.

## Phase 18 session ingress auth header parity

Phase 18 adds the next session ingress auth/configuration parity slice from `sessionIngressAuth.ts`:

- `ConfigFromEnv` reads `CLAUDE_CODE_SESSION_ACCESS_TOKEN` and `CLAUDE_CODE_ORGANIZATION_UUID` for runtime session ingress configuration
- `NewClient` uses those environment values as auth defaults when explicit config values are absent
- session keys with the `sk-ant-sid` prefix now use `Cookie: sessionKey=...` instead of bearer auth
- session-key auth includes `X-Organization-Uuid` when an organization UUID is configured
- bearer-token behavior remains available for JWT/OAuth session ingress calls
- deterministic tests cover environment config loading, default client auth, and session-key cookie headers

File-descriptor token discovery, well-known token-file fallback, token persistence for subprocesses, OAuth token refresh, diagnostics/debug logging, and command-layer teleport progress integration remain deferred to later phases.

## Phase 19 session ingress token discovery

Phase 19 extends session ingress auth discovery to match the remaining token-source priority from `sessionIngressAuth.ts`:

- `SessionIngressAuthTokenFromEnv` now checks `CLAUDE_CODE_SESSION_ACCESS_TOKEN` first
- when no direct token is present, it can read the legacy `CLAUDE_CODE_WEBSOCKET_AUTH_FILE_DESCRIPTOR` file descriptor path
- failed descriptor reads fall back to a well-known token file
- `CLAUDE_SESSION_INGRESS_TOKEN_FILE` can override the well-known token-file path
- the default CCR fallback path is `/home/claude/.claude/remote/.session_ingress_token`
- invalid descriptor values fail closed instead of reading a fallback token file
- deterministic tests cover environment precedence, custom token files, descriptor reads, descriptor-read fallback, and invalid descriptor handling

Token persistence for subprocesses, OAuth token refresh, diagnostics/debug logging, and command-layer teleport progress integration remain deferred to later phases.

## Phase 20 session ingress token persistence

Phase 20 completes the subprocess token handoff slice from `authFileDescriptor.ts` and `sessionIngressAuth.ts`:

- file-descriptor token reads now persist the discovered token for subprocess access when `CLAUDE_CODE_REMOTE` is truthy
- persisted token files are written with `0600` permissions and parent directories are created with `0700`
- persistence uses the configured session ingress token-file path, defaulting to `/home/claude/.claude/remote/.session_ingress_token`
- non-remote runs keep descriptor-read tokens in memory only and do not write a fallback file
- deterministic tests cover remote persistence, no persistence outside remote mode, and the existing token discovery order

OAuth token refresh, diagnostics/debug logging, and command-layer teleport progress integration remain deferred to later phases.

## Phase 22 Teleport Sessions API foundation

Phase 22 starts migrating `utils/teleport/api.ts` by adding a focused Sessions API package:

- `internal/sessionsapi` models the CCR BYOC Sessions API resource and list response shapes from `/v1/sessions`
- `ListCodeSessions` calls `GET /v1/sessions` and transforms raw session resources into the legacy code-session shape used by teleport UI flows
- `FetchSession` calls `GET /v1/sessions/{sessionID}` and returns typed session resources with deterministic 404, auth, and API-error handling
- OAuth request headers now include bearer auth, `Content-Type: application/json`, `anthropic-version: 2023-06-01`, `anthropic-beta: ccr-byoc-2025-07-29`, and `x-organization-uuid` when configured
- list-session reads retry transient network and 5xx failures with the TypeScript backoff sequence of 2s, 4s, 8s, and 16s, while client errors remain non-retryable
- deterministic `httptest` coverage verifies headers, transformation behavior, retry behavior, non-retryable 4xx handling, single-session fetch errors, and GitHub repository parsing

Session event sending, title updates, richer session resource helpers, OAuth config parity, OAuth token/profile client integration, and prompt-cache diagnostics remain deferred to later phases.

## Phase 23 Sessions API event send

Phase 23 adds the remote-session user event send path from `utils/teleport/api.ts`:

- `SendEventToRemoteSession` posts user message events to `POST /v1/sessions/{sessionID}/events`
- request bodies use the TypeScript event shape with `events`, `uuid`, `session_id`, `type: "user"`, `parent_tool_use_id: null`, and `message.role: "user"`
- callers can provide an event UUID for local echo de-duplication, or let the Go client generate an RFC 4122 version 4 UUID
- event sends use the CCR BYOC OAuth headers from Phase 22 and a separate 30-second timeout to match the cold-start margin in the TypeScript implementation
- successful `200` and `201` responses return `true`; API failures return `false` with a normalized `core.APIError`
- deterministic `httptest` coverage verifies request headers, body shape, caller-provided UUIDs, generated UUIDs, API-error handling, and local input validation

Title updates, branch extraction helpers, OAuth config parity, OAuth token/profile client integration, and prompt-cache diagnostics remain deferred to later phases.

## Phase 24 Sessions API title update

Phase 24 adds the remote-session title update path from `utils/teleport/api.ts`:

- `UpdateSessionTitle` patches existing sessions via `PATCH /v1/sessions/{sessionID}`
- request bodies use the TypeScript shape `{ "title": "..." }`
- title updates reuse the CCR BYOC OAuth headers from Phase 22 and the standard Sessions API timeout
- successful `200` responses return `true`; API failures return `false` with a normalized `core.APIError`
- deterministic `httptest` coverage verifies request method/path, headers, JSON body shape, API-error handling, and local session-ID validation

Branch extraction helpers, OAuth config parity, OAuth token/profile client integration, prompt-cache diagnostics, and remaining teleport integration wiring remain deferred to later phases.

## Phase 25 session resource parsing and branch extraction

Phase 25 completes the remaining pure helper slice from `utils/teleport/api.ts` for Sessions API resources:

- `CodeSessionFromResource` is now an exported helper for transforming raw `SessionResource` values into legacy code-session UI resources
- list-session reads reuse `CodeSessionFromResource`, keeping direct helper usage and endpoint behavior consistent
- `GetBranchFromSession` extracts the first branch from the first `git_repository` outcome, matching the TypeScript `getBranchFromSession` helper
- branch extraction safely handles nil outcomes, non-git outcomes, and git outcomes with no branches
- deterministic tests cover code-session transformation, GitHub source parsing, first-branch extraction, and missing-branch cases

OAuth config parity, OAuth token/profile client integration, prompt-cache diagnostics, and remaining teleport integration wiring remain deferred to later phases.

## Phase 26 OAuth config parity

Phase 26 adds the OAuth URL/configuration resolver from `constants/oauth.ts` as a reusable Go package:

- `internal/oauthconfig` models production, staging, local, and allowlisted custom OAuth endpoint configuration
- environment selection matches the TypeScript ordering: production by default, staging/local only for `USER_TYPE=ant`, local taking precedence over staging, and `CLAUDE_CODE_CUSTOM_OAUTH_URL` overriding URL fields when allowlisted
- local OAuth base overrides trim trailing slashes for `CLAUDE_LOCAL_OAUTH_API_BASE`, `CLAUDE_LOCAL_OAUTH_APPS_BASE`, and `CLAUDE_LOCAL_OAUTH_CONSOLE_BASE`
- `CLAUDE_CODE_OAUTH_CLIENT_ID` overrides the selected client ID
- file suffix helpers preserve production, staging, local, and custom OAuth credential suffix behavior
- OAuth scope constants and MCP client metadata/proxy configuration are represented for later auth/client phases
- deterministic tests cover production, staging, local, custom allowlist rejection, client ID overrides, file suffixes, scope ordering, and local URL trimming

Wiring this resolver into all API clients, prompt-cache diagnostics, and remaining teleport integration wiring remain deferred to later phases.

## Phase 27 OAuth token/profile client foundation

Phase 27 adds the OAuth token and profile client behavior from `services/oauth/client.ts` and `services/oauth/getOauthProfile.ts`:

- `internal/oauthclient` builds OAuth authorization URLs for console and Claude.ai login flows, including manual redirects, inference-only scope requests, org UUID, login hints, and login method query parameters
- authorization-code exchange posts the TypeScript-compatible JSON body to the configured token URL, supports optional `expires_in`, and normalizes unauthorized responses to the invalid-code auth message
- refresh-token exchange requests the Claude.ai OAuth scopes by default, preserves the previous refresh token when the response omits a replacement, computes `expiresAt`, parses scopes, and fetches profile info for subscription and rate-limit metadata
- profile reads cover both bearer-token `/api/oauth/profile` calls and API-key `/api/claude_cli_profile?account_uuid=...` calls with the OAuth beta header
- user-role and API-key helpers call the configured roles and create-key endpoints with bearer authorization
- helper functions preserve TypeScript behavior for scope parsing, Claude.ai auth detection, OAuth expiration buffering, and profile-to-subscription mapping
- deterministic `httptest` coverage verifies URL construction, request bodies and headers, token refresh/profile sequencing, API-key profile reads, roles/API-key endpoints, helper behavior, validation, and normalized API errors

Wiring prompt-cache diagnostics and remaining teleport integration wiring remain deferred to later phases.

## Phase 28 auth file descriptor credential reader

Phase 28 migrates the shared CCR file-descriptor credential reader from `utils/authFileDescriptor.ts`:

- `internal/authfiledescriptor` models the well-known CCR token directory and fallback files for OAuth tokens, API keys, and session ingress tokens
- credential discovery follows the TypeScript order: cached read result, inherited file descriptor, then well-known file fallback
- invalid file descriptor environment values return no credential without falling back, matching the TypeScript safety behavior
- failed descriptor reads fall back to disk so subprocesses that inherited an env var but not the FD can still authenticate
- remote CCR environments best-effort persist successfully read FD credentials with `0700` directories and `0600` files for subprocess access
- `internal/sessioningress` now reuses the shared descriptor reader for its legacy websocket auth descriptor while preserving `CLAUDE_CODE_SESSION_ACCESS_TOKEN` precedence and `CLAUDE_SESSION_INGRESS_TOKEN_FILE` overrides
- deterministic tests cover OAuth/API-key/session source constants, FD reads, well-known file fallback, caching, remote persistence, non-remote non-persistence, failed FD fallback, invalid FD rejection, and platform-specific descriptor paths

Prompt-cache diagnostics and remaining teleport integration wiring remain deferred to later phases.

## Phase 29 OAuth credential storage foundation

Phase 29 migrates the durable OAuth credential storage slice from `utils/auth.ts` and `utils/secureStorage/plainTextStorage.ts`:

- `internal/oauthstorage` models Claude Code's plaintext secure-storage fallback at `.credentials.json`
- token discovery follows the TypeScript order: bare mode disables OAuth, `CLAUDE_CODE_OAUTH_TOKEN` yields inference-only credentials, CCR OAuth file-descriptor credentials yield inference-only credentials, then stored `claudeAiOauth` data is read from disk
- stored credentials preserve the TypeScript JSON shape with `accessToken`, `refreshToken`, `expiresAt` as Unix milliseconds, `scopes`, `subscriptionType`, and `rateLimitTier`
- saving OAuth tokens skips non-Claude.ai scopes and inference-only tokens without treating that as a failure
- durable token writes preserve existing subscription and rate-limit metadata when a refresh/profile lookup returns nil, matching the TypeScript guard against clobbering valid cached plan data
- plaintext updates create parent directories, write credentials with `0600` permissions, preserve unknown top-level credential fields, and return the plaintext storage warning
- deterministic tests cover environment and file-descriptor precedence, bare mode, stored token reads, durable saves, skipped saves, metadata preservation, unknown-field preservation, delete semantics, path construction, and file permissions

Remaining teleport integration wiring remains deferred to later phases.

## Phase 30 prompt-cache diagnostics foundation

Phase 30 migrates the core prompt-cache break-detection logic from `services/api/promptCacheBreakDetection.ts` into a reusable Go package:

- `internal/promptcache` tracks prompt-cache relevant request state by query source, including system prompt text, tool schemas, model, fast mode, cache strategy, beta headers, auto-mode, overage, cached microcompact, effort, and extra body parameters
- tracked source behavior matches the TypeScript detector: compact shares the main REPL tracking key, common REPL/SDK/agent sources are tracked, short-lived sources are ignored, and the number of tracked sources is capped
- response checks compare cache-read token drops against the previous response and only report drops that exceed both the 5% relative threshold and the 2,000-token absolute threshold
- reports explain likely causes from pending request-state changes, TTL windows, or likely server-side cache behavior when the prompt is unchanged
- cache deletion, compaction, agent cleanup, reset, Haiku-model exclusion, sorted beta comparisons, cache-control-only changes, and MCP tool-name sanitization are represented for later runtime logging integration
- deterministic tests cover changed request state, small-drop suppression, TTL/server-side reasons, expected cache-deletion and compaction drops, tracking-key behavior, Haiku exclusion, cache-control changes, and conversion from core tool definitions

Remaining teleport integration wiring remains deferred to later phases.
