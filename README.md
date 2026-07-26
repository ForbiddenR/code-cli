# code-cli

Go implementation of the core model-streaming API boundary used by the Claude
Code query flow corresponding to `queryModelWithStreaming` in
`claude-code-source/src/services/api/claude.ts`.

## Retained packages

- `internal/core` — normalized message, content, model, usage, retry, error, and
  API configuration contracts
- `internal/anthropicapi` — official Anthropic SDK integration for model
  requests, streaming events, response conversion, token counting, retries,
  API error normalization, server-side web-search tools, and forced tool choice
- `internal/tools/websearch` — focused server-side WebSearchTool implementation
  with input validation, provider/model enablement, streaming progress,
  structured result conversion, and tool-result formatting
- `internal/tools/webfetch` — local WebFetchTool implementation with URL validation,
  HTTPS upgrading, Anthropic domain preflight, controlled redirects, response
  limits, HTML-to-Markdown conversion, in-memory caching, optional binary
  persistence, and nonstreaming small-model prompt application
- `internal/tools/brief` — local `SendUserMessage`/`Brief` tool boundary with
  strict input parsing, ordered attachment metadata resolution, optional injected
  best-effort upload, structured host-facing output, and a delivery-only model
  acknowledgement
- `internal/tools/grep` — local ripgrep-backed `Grep` tool with strict semantic
  input parsing, deterministic non-shell argument construction, bounded and
  cancellable execution, structured result modes, sorting, and pagination
- `internal/tools/bash` — focused local `Bash` boundary for bounded foreground
  shell execution, timeout and cancellation handling, process-tree cleanup,
  structured results, and model-facing error mapping

The retained streaming path covers the API-facing responsibilities of
`queryModelWithStreaming`:

1. convert normalized conversation messages and request options into Anthropic
   SDK parameters
2. create and stream a Messages API request
3. convert SDK stream events and final responses into normalized Go contracts
4. retry eligible setup/transient failures without replaying a partially
   delivered stream
5. expose normalized API errors and usage, including prompt-cache accounting

The `websearch` package intentionally stops at the pure tool boundary: Ink
rendering, permission persistence, GrowthBook feature flags, full provider state,
REPL registration, telemetry, live web-search calls, and broader tool
orchestration remain outside this reduced module.

The `webfetch` package performs the primary fetch with an injected local HTTP client. It does
not declare Anthropic's hosted `web_fetch` server tool. Successful content is
cached before prompt application; nontrivial content is passed to the injected
Anthropic client with `CreateMessage` using Haiku 4.5 by default. Tests inject
all HTTP and model responses and make no public network or live Claude calls.

The `brief` package validates the canonical `SendUserMessage` input (and retains
`Brief` as a legacy alias), resolves local files without network access, and
returns the full message, attachment metadata, and execution timestamp to its
host. Its model-facing tool result contains only a delivery acknowledgement and
attachment count. A host may inject a best-effort uploader; concrete bridge
networking and OAuth are not implemented.

The `grep` package executes `rg` directly, never through a shell. Hosts must
provide ripgrep on `PATH` or inject another executable and runner. The package
validates and expands local targets, excludes common VCS metadata directories,
retries resource-exhaustion failures with one worker, bounds captured output,
and maps content, file-list, and count results into the model-facing format.
Tests use fake runners and temporary files, so they do not require ripgrep.
Vendored or embedded ripgrep selection, code signing, availability telemetry,
permission rules and UI, plugin-cache exclusions, tool-registry integration,
and generic oversized-result persistence are not implemented.

**Security warning:** calling the `bash` package executes arbitrary shell code
with the privileges and snapshotted environment of the hosting process. The
package does not authorize or sandbox commands. Hosts must apply their own
policy and obtain any required user confirmation before invoking it.

The `bash` package strictly accepts `command`, optional `timeout`, and optional
`description`. Each foreground call starts from immutable absolute working
directory and environment snapshots, runs through `bash -c` by default without
login-profile initialization, captures bounded combined stdout/stderr, and
terminates the process group on timeout or cancellation where the platform
supports it. Shell-local cwd changes, variables, aliases, and functions do not
persist between calls. Exit code 1 retains Claude Code's non-error meanings for
`grep`/`rg`, `find`, `diff`, `test`, and `[`. Nonzero failures still return their
captured structured output alongside an error.

The package intentionally excludes permission prompts and persistence,
command-authorization policy, sandboxing and sandbox bypass,
`run_in_background`, background task/output registries, progress/UI rendering,
persistent cwd or shell sessions, profile/environment snapshot generation,
generic output-file persistence, PTY or interactive stdin forwarding, tool
registry/query-loop integration, telemetry, and feature flags.

Full permission UI, CLI registration, session-global storage, proxy/mTLS
application wiring, generic oversized-result persistence, concrete Brief UI and
upload transport, feature gating, query-loop registration, conversation
recovery, and other UI, session-transport, OAuth, control-plane, telemetry, and
repository helpers are intentionally excluded from this reduced module.

## Development

Tests use deterministic fixtures and do not require live Claude API credentials.

```bash
CGO_ENABLED=0 go mod tidy
CGO_ENABLED=0 go fix ./...
CGO_ENABLED=0 go fmt ./...
CGO_ENABLED=0 go test ./...
CGO_ENABLED=0 go build ./...
git diff --check
```

The normal build and test workflow is pure Go and does not require a C compiler.
The race detector is separate: on Linux, `go test -race ./...` requires
`CGO_ENABLED=1` and a supported C compiler such as GCC.
