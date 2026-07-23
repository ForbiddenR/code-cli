# code-cli

Go implementation of the core model-streaming API boundary used by the Claude
Code query flow corresponding to `queryModelWithStreaming` in
`claude-code-source/src/services/api/claude.ts`.

## Retained packages

- `internal/core` — normalized message, content, model, usage, retry, error, and
  API configuration contracts
- `internal/anthropicapi` — official Anthropic SDK integration for model
  requests, streaming events, response conversion, token counting, retries,
  and API error normalization

The retained streaming path covers the API-facing responsibilities of
`queryModelWithStreaming`:

1. convert normalized conversation messages and request options into Anthropic
   SDK parameters
2. create and stream a Messages API request
3. convert SDK stream events and final responses into normalized Go contracts
4. retry eligible setup/transient failures without replaying a partially
   delivered stream
5. expose normalized API errors and usage, including prompt-cache accounting

UI rendering, tool orchestration, permissions, session transport, OAuth,
control-plane endpoints, Files API behavior, telemetry, and repository helpers
are intentionally excluded from this reduced module.

## Development

Tests use deterministic fixtures and do not require live Claude API credentials.

```bash
go mod tidy
go fmt ./...
go test ./...
go build ./...
git diff --check
```
