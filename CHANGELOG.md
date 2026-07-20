# Changelog

## [0.1.0] - 2026-07-20

Initial release.

### Added
- Single-binary gateway for OpenAI-compatible `/v1/chat/completions`:
  ordered provider list, automatic fallback on `429`/`5xx`/network
  error/timeout, immediate passthrough on non-retryable `4xx`.
- Per-request cost estimation from the real response `usage` object
  (`prompt_tokens`/`completion_tokens`) against per-provider,
  per-1M-token pricing.
- `model_override`: rewrite the client's `model` field per provider, so
  one client request can target providers with different model naming
  schemes for the same logical model.
- Metadata-only JSONL audit trail (`0600`): provider, model, status,
  token counts, cost, duration, attempted-providers list - never prompt
  or completion content. Sink failures are counted, not fatal.
- CLI: `--config`, `--check` (validate and exit), `--version`, graceful
  shutdown on `SIGINT`/`SIGTERM`.
- Streaming requests (`"stream": true`) rejected with a clear `400`
  rather than silently mishandled - OpenAI-compatible providers omit
  `usage` by default on streamed responses unless the client opts in via
  `stream_options.include_usage`, and support for that flag is
  inconsistent across providers; correctly handling both the SSE
  passthrough and usage extraction is real scope, tracked on the
  roadmap.

### Evidence
- 29 unit/integration tests (`go test ./... -race`), including real
  `httptest` upstream servers for the fallback/timeout/non-retryable/
  model-override/all-providers-failed paths.
- Real end-to-end smoke test (`ci/smoke.sh`): the actual compiled binary
  against real Python stub upstreams (one returning 503, one returning
  200 with real token counts), verifying the client receives the
  fallback's response, the audit log has exactly the right two entries
  with correct cost math (`111 prompt + 22 completion tokens @
  $0.15/$0.60 per 1M = $0.00002985`, checked to 1e-9), 0600 permissions,
  no prompt/response content anywhere in the log, and that the fallback
  provider actually received the rewritten `Authorization` header and
  `model_override`.
- Example config's providers (Groq, Fireworks AI) and their pricing were
  verified against each provider's current public pricing/docs pages
  before being published, not guessed.
