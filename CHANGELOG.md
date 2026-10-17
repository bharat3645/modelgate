# Changelog

## [Unreleased]

### Fixed
- **`Scanner.Close` could panic on shutdown if a scan was still in
  flight.** `Close` unconditionally closed the coprocess pool channel;
  a `Scan` call that had already checked a coprocess out and was still
  waiting on a slow or wedged `promptproof serve` process would, on
  finishing, try to return it with `s.pool <- p` — sending on a channel
  `Close` had since closed, panicking the whole gateway. Real reachable
  path: `cmd/modelgate`'s shutdown handler calls `srv.Shutdown` with a 5s
  deadline and then unconditionally runs the deferred `Gateway.Close`; if
  that deadline elapses while a request's scan is still running, `Close`
  and the in-flight `Scan` race and modelgate crashes on shutdown instead
  of exiting cleanly. Fixed by never closing the pool channel — `Close`
  now sets a `closed` flag and only drains/shuts-down coprocesses
  currently idle in the pool; an in-flight `Scan` that finishes afterwards
  sees `closed` and shuts its own coprocess down directly instead of
  sending it back. Regression test reproduces the panic against the
  unfixed code with a scripted slow coprocess stub (no real `promptproof`
  binary needed) and stays green, clean under `-race`, against the fix.
- **`model_override` silently corrupted large integer fields in the
  request body.** `overrideModel` decoded the whole body into
  `map[string]any` to replace the top-level `model` field, which
  unmarshals every JSON number as `float64` — losing precision on any
  integer outside float64's exactly-representable range (2^53), e.g. a
  client-supplied `seed`. A provider with `model_override` set (and only
  that provider — others receive the body untouched) could receive a
  silently different `seed` (or any other large-integer field) than the
  client sent, contradicting the function's own "preserving every other
  field exactly" contract. Fixed by decoding with `json.Decoder.UseNumber`
  so every other field's numbers round-trip through their original text.
  Regression tests added at both the unit (`overrideModel`) and gateway
  (real upstream, real HTTP round trip) level.

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
