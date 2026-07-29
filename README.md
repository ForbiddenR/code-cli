# code-cli

Go implementation of the core model-streaming API boundary used by the Claude
Code query flow corresponding to `queryModelWithStreaming` in
`claude-code-source/src/services/api/claude.ts`.

## Retained packages

- `internal/core` — normalized message, content, model, usage, retry, error, and
  API configuration contracts
- `internal/systemprompt` — deterministic retained coding-agent prompt builder
  with tool-sensitive policy, explicit environment context, bounded Skill summaries,
  and a stable/dynamic prompt-cache boundary
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
- `internal/skills` — standalone Skill domain with strict and source-aware
  discovery, legacy commands, conditional activation, bundled registration,
  deterministic expansion, immutable snapshots, and Linux-safe reference extraction
- `internal/tools/skill` — model-facing `Skill` adapter over an immutable
  `internal/skills` snapshot or a shared refreshable manager
- `internal/tools` — concrete built-in registry and raw JSON dispatch layer for
  Bash, Grep, WebFetch, WebSearch, SendUserMessage (with `Brief` as a
  compatibility alias), and Skill
- `internal/query` — canonical conversation history, model streaming, explicit
  terminal outcomes, active cancellation, and a bounded generic tool-use loop
- `internal/session` — UI-independent visible transcript and first-message summary
  state for the interactive TUI
- `internal/tui` — Bubble Tea v2 source-style flowing header and transcript,
  growing multiline composer, adaptive color handling, inline resize handling,
  streamed assistant presentation, and active Ctrl+C cancellation using native
  terminal scrollback
- `cmd/code-cli` — model-backed executable composition with concrete tools disabled
  and denied by default

The concrete registry exposes a stable six-tool order, defensive definition
copies, exact case-sensitive canonical and alias lookup, strict raw JSON
execution, typed host outputs, and normalized `core.ContentBlock` tool results.
`Brief` resolves to `SendUserMessage` but is not advertised as a separate model
definition. `All`, `Definitions`, and `Lookup` are exhaustive, while `Enabled`
and `EnabledDefinitions` apply registry policy. WebSearch reuses
`websearch.IsEnabled` with the configured provider and model; an omitted policy
defaults to first-party and the core default model.

Each registry entry also exposes the retained non-UI portions of the shared
Claude Code tool contract: enabled state, strict parser-backed input
classification, and the TypeScript-compatible model-result size limit. Bash is
classified conservatively as neither concurrency-safe nor read-only; Skill uses
those same conservative defaults. Grep, WebFetch, WebSearch, and
SendUserMessage are concurrency-safe and read-only.
These values are scheduling and descriptive metadata only. They do not grant
permission, authorize shell commands, or provide sandboxing.

Bash and Grep advertise the Anthropic custom-tool top-level `strict: true`
metadata. This API field is distinct from local strict JSON parsing and from
`input_schema.additionalProperties: false`; the remaining four definitions
retain closed schemas without advertising top-level strict mode.

`WebSearch` is a locally dispatched outer tool that internally declares
Anthropic's hosted `web_search` server tool. The hosted declaration remains
separate and is not registered as another concrete local tool.

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
permission rules and UI, plugin-cache exclusions, and generic oversized-result
persistence are not implemented.

The `skills` domain has two loading modes. `LoadStrict` preserves the original
compatibility behavior for explicit `<root>/<name>/SKILL.md` roots: every root
must exist, malformed candidates fail atomically, first-root precedence applies,
and canonical files must remain inside their configured root. A `Manager` adds
source-aware discovery from bundled, managed, user, project, additional, and
legacy-command roots. Project roots are searched from the working directory
outward; lower-precedence name, alias, and canonical-file collisions are skipped
with diagnostics. Legacy `.claude/commands` Markdown is loaded recursively with
colon-separated names such as `release:deploy`; a directory containing
`SKILL.md` is treated as one Skill and suppresses sibling command Markdown.

The manager exposes immutable snapshots through atomic replacement. `Refresh`
rescans configured and previously discovered roots. `ObservePaths` discovers
nested `.claude/skills` roots below the working directory, consults an injected
Git-ignore checker, and activates `paths` metadata with sticky session semantics.
`ResetSession` clears dynamic roots and path activations. There is deliberately
no filesystem watcher or polling. `Registry.Skills()` reads the current manager
snapshot, so active model-visible summaries change after these explicit calls.

Skill frontmatter supports `name` as display metadata while retaining the
filesystem or legacy namespace as the invocation name, plus `description`,
`when_to_use`, `argument-hint`, `arguments`, `version`, `allowed-tools`, `model`,
`effort`, `disable-model-invocation`, `user-invocable`, `paths`, `context`,
`agent`, `hooks`, and `shell`. Known fields are type-checked and unknown fields
are ignored. Omitted `allowed-tools` means no requested tool-context change;
an explicitly empty value requests a restriction to no tools. Neither form
grants authorization.

Prompt expansion supports `$ARGUMENTS`, indexed forms, declared named
arguments, `${CLAUDE_SKILL_DIR}`, and an explicitly supplied
`${CLAUDE_SESSION_ID}`. Registry hosts pass the latter through
`ExecuteOptions.SessionID`; the compatibility `Tool.Call` method leaves it empty.
Arguments expand before Skill-directory and session variables. Local trusted
content may use an injected shell expander, but no shell
runs by default and bundled or other non-local content cannot use that adapter.
Fork/agent context, hooks, and shell metadata fail atomically with typed
unsupported-capability errors when the host has no corresponding adapter.

`BundledRegistry` lets a host register self-contained static or callback-built
Skills, aliases, enablement predicates, metadata, and optional reference files.
No product-dependent Claude Code Skill is registered by default. On Linux,
reference files are extracted lazily and concurrently safely beneath a random
process-private cache root using no-follow and exclusive creation: directories
use mode `0700`, files use `0600`, and absolute, malformed, traversal, and
symlink-attack paths are rejected.

A successful model invocation returns the short ordinary tool result
`Launching skill: <name>`, then a separate meta user message containing expanded
instructions, plus optional declarative allowed-tool/model/effort effects. The
host must preserve that ordering, intersect requested tools with tools it has
already enabled and authorized, and scope any applied effects appropriately.
Configured Skill Markdown remains trusted prompt content. Plugin, MCP, remote,
browser, memory, hooks-runtime, forked-agent, UI, telemetry, feature-flag,
session-compaction, and query-loop implementations remain intentionally outside
this standalone subsystem.

The `systemprompt` package builds API-ready `[]core.SystemBlock` values from
explicit host inputs. A host supplies observed environment facts together with
`registry.EnabledDefinitions()` and `registry.Skills()`, then assigns the result
to `anthropicapi.MessageRequest.System`. The builder performs no filesystem,
Git, clock, process-environment, or network discovery. Its first block contains
stable coding and retained-tool policy; its second contains dynamic environment
and available-Skill context. When prompt caching is enabled, only the stable
block receives ephemeral cache control.

Skill names and descriptions rendered by `systemprompt` are bounded and sorted,
but remain trusted prompt content loaded from configured roots. The prompt layer
does not grant permissions or authorize execution. Custom, appended, overridden,
coordinator, and agent prompt composition; `CLAUDE.md` and memory discovery;
current-date and Git collection; language and output-style settings; permissions
UI; hooks; MCP; scratchpads; session globals; feature flags; telemetry; and
global or organization cache scopes are intentionally excluded.

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
generic output-file persistence, PTY or interactive stdin forwarding,
query-loop integration, telemetry, and feature flags.

Full permission UI, concrete registry enablement, session-global storage,
proxy/mTLS application wiring, generic oversized-result persistence, concrete
Brief UI and upload transport, feature gating, conversation recovery, and other
session-transport, OAuth, control-plane, telemetry, and repository helpers are
intentionally excluded from this reduced module.

## Interactive model-backed TUI

The repository includes a Bubble Tea v2 application that follows the source default
non-fullscreen layout: a Claude Code identity header, visible transcript, prompt
composer, and shortcuts footer rendered sequentially in the terminal:

```bash
go run ./cmd/code-cli
```

At runtime, the executable loads the top-level `env` object from the user settings
file at `~/.claude/settings.json` and overlays those values onto the inherited
process environment before reading runtime configuration or constructing the
official Anthropic SDK client. Settings values are literal: `$VAR`, `${VAR}`, `~`,
and shell syntax are not expanded. Only this user settings source and its `env`
field are implemented; project, local, managed, and command-line settings sources
are not yet supported.

The executable then resolves the model from `ANTHROPIC_MODEL`, falling back to
`core.DefaultModel`, and streams Messages API output into the TUI. Authentication
follows the SDK's environment behavior, including credentials supplied through the
user settings environment; credentials are not copied into the system prompt,
transcript, or visible TUI configuration. Running the executable therefore requires
network access and a credential source accepted by the SDK.

- Enter submits the current message; Shift+Enter inserts a newline.
- The composer grows with multiline and wrapped input without the fullscreen-only
  half-screen cap; larger content pushes earlier output into terminal scrollback.
- Bubble Tea runs inline rather than in the alternate screen. The visible title,
  transcript, composer, and footer flow through native terminal scrollback instead
  of occupying fixed screen regions.
- The open-sided prompt border, header, transcript, and footer use source-derived
  colors that adapt to light/dark terminal backgrounds and color capabilities.
- User and assistant rows use the source-style `❯` and `●` markers. Assistant text
  appears incrementally as model stream deltas arrive.
- `internal/query.Engine` owns canonical API history, including assistant content
  and any hidden tool-result or injected messages. `internal/session` separately
  owns only the user-visible transcript and first-message window-title summary.
- A submission ends with an explicit outcome such as `end_turn`, `max_tokens`,
  `stop_sequence`, `pause_turn`, `refusal`, `canceled`, `tool_turn_limit`, or
  `failed`.
- Ctrl+C cancels an active request and keeps the TUI open; when no request is active,
  Ctrl+C exits.
- The generic query engine supports a bounded tool-use loop, with eight tool turns
  by default. The executable deliberately supplies `query.NoTools` and
  `query.DenyAll`: it does not advertise, authorize, or execute the concrete Bash,
  Grep, WebFetch, WebSearch, SendUserMessage, or Skill registry.
- Persistence, permission prompts, generated conversation titles, and concrete tool
  enablement remain outside this executable composition.

## Development

Tests use injected deterministic clients and fixtures: they make no live Claude
requests, require no API credentials, and are expected to run offline. This differs
from `go run ./cmd/code-cli`, whose model-backed runtime performs Anthropic API
network requests.

```bash
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go mod tidy
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go fix ./...
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go fmt ./...
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go vet ./...
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go test ./...
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build ./...
git diff --check
```

The normal build and test workflow is pure Go and does not require a C compiler.
The race detector is separate: on Linux, `go test -race ./...` requires
`CGO_ENABLED=1` and a supported C compiler such as GCC.
