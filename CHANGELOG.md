# Changelog

## [Unreleased]

### Added
- Launch demo (`demo/modelgate-demo.cast` + `demo/run_demo.sh`): a real
  asciinema recording of the fallback/cost-accounting story (groq-like
  primary returns 503, fireworks-like backup succeeds, client gets one
  response, audit log shows both attempts with real token counts and
  computed cost) followed by the promptproof-scanning story (a benign
  request is forwarded, an injected request is blocked with `403` before
  it ever reaches a provider, and the audit log is grepped live to prove
  the scanned content never leaked into it). Driven against the real
  compiled binary, real stub upstreams, and a real `promptproof` binary
  built from source — nothing synthetic or hand-edited in the cast.

## [0.2.0] - 2026-07-22

### Added
- **promptproof integration — scanning inbound request content for prompt
  injection.** Optional top-level `promptproof` config wires the
  [promptproof](https://github.com/bharat3645/promptproof) scanner into the
  request path: a chat-completion request's message content is scanned for
  injection / exfiltration signals **before** it is forwarded to any provider.
  Off by default — an absent `promptproof` block (or `enabled:false`) is
  byte-for-byte the old behavior. On a verdict at or above `threshold`
  (`suspicious`/`dangerous`) the gateway either **blocks** the request (`403`,
  never forwarded — the tainted content never reaches a model) or **flags** it
  (forwarded, `X-PromptProof-Verdict` header set, verdict audited). Configurable
  `action`, score cutoffs, and coprocess `pool` size.
- Detection is **not** reimplemented: the gateway runs a small pool of
  `promptproof serve` coprocesses (promptproof ≥ 0.2.0) and streams the message
  content through promptproof's length-prefixed framing. String message content
  and the text parts of array (vision-style) content are both scanned, and
  JSON-escaped hidden characters are decoded first so covert channels stay
  visible. Fail-open: a scanner error is audited and the request proceeds.
- Audit entries gain metadata-only `promptproof_verdict` / `promptproof_score` /
  `promptproof_categories` / `promptproof_blocked` / `promptproof_error` fields
  (never the scanned content).
- Real integration tests (`gateway/promptproof_test.go`) drive the actual
  promptproof binary through the gateway with malicious and benign payloads
  (block, flag, array content, hidden-char decode, threshold, concurrency,
  disabled-is-inert), and `ci/smoke.sh` blocks a real injected request
  end-to-end. CI installs the real promptproof (`cargo install --git`) and runs
  the tests with `PROMPTPROOF_REQUIRED=1` so the integration is never silently
  skipped.
- Benchmarks (`gateway/bench_test.go`): the added latency is ~33µs per request
  (~40µs → ~73µs through the gateway); ~26µs for the isolated scan round trip.

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
