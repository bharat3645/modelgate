#!/usr/bin/env bash
# End-to-end smoke test: real binary, real Python stub upstreams, curl
# through the gateway, then audit-log assertions.
# Invoked as: GW_BIN=/path/to/modelgate bash ci/smoke.sh
set -euo pipefail

BIN=${GW_BIN:-/tmp/modelgate}
WORK=$(mktemp -d)
trap 'kill $(jobs -p) 2>/dev/null || true; rm -rf "$WORK"' EXIT
cd "$WORK"

wait_for() {
  for _ in $(seq 1 40); do
    curl -s -o /dev/null "$1" && return 0
    sleep 0.25
  done
  echo "never came up: $1"; exit 1
}

echo "--- starting stub upstreams ---"
python3 "$OLDPWD/ci/stub_upstream.py" 3901 503 &                # primary: always overloaded
python3 "$OLDPWD/ci/stub_upstream.py" 3902 200 111 22 &          # fallback: succeeds, 111+22 tokens
wait_for "http://127.0.0.1:3901/chat/completions" || true
wait_for "http://127.0.0.1:3902/chat/completions" || true
# The stubs 404 on GET (BaseHTTPRequestHandler default), so wait_for's
# curl above just needs *a* response, not 200 - give them a beat instead.
sleep 0.5

export PRIMARY_KEY="sk-primary-canary"
export FALLBACK_KEY="sk-fallback-canary"

cat > config.json <<EOF
{
  "listen": "127.0.0.1:8090",
  "audit": {"path": "$WORK/audit.jsonl"},
  "timeout_seconds": 5,
  "providers": [
    {"name": "primary", "base_url": "http://127.0.0.1:3901", "api_key_env": "PRIMARY_KEY",
     "pricing": {"prompt_per_1m_usd": 0.05, "completion_per_1m_usd": 0.08}},
    {"name": "fallback", "base_url": "http://127.0.0.1:3902", "api_key_env": "FALLBACK_KEY",
     "pricing": {"prompt_per_1m_usd": 0.15, "completion_per_1m_usd": 0.60}, "model_override": "stub-fallback-model"}
  ]
}
EOF

echo "--- check: valid config passes ---"
"$BIN" --config config.json --check
echo "check OK"

echo "--- starting modelgate ---"
"$BIN" --config config.json &
wait_for "http://127.0.0.1:8090/v1/chat/completions"

echo "--- request: primary overloaded, must fall back and succeed ---"
RESP=$(curl -s -X POST http://127.0.0.1:8090/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model":"generic","messages":[{"role":"user","content":"hi"}]}')
echo "$RESP"
echo "$RESP" | grep -q '"stub response"' || { echo "FAIL: expected the fallback's response body"; exit 1; }
echo "ok - client received the fallback's response"

echo "--- request: streaming rejected ---"
CODE=$(curl -s -o /dev/null -w '%{http_code}' -X POST http://127.0.0.1:8090/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model":"generic","messages":[],"stream":true}')
[ "$CODE" = "400" ] || { echo "FAIL: streaming request got $CODE, want 400"; exit 1; }
echo "ok - streaming request rejected with 400"

echo "--- audit log assertions ---"
# GNU stat (-c, Linux/CI) first: BSD stat (macOS, local dev) rejects -c
# outright (exit nonzero), so the fallback triggers correctly. The other
# order is the real bug this had: BSD `stat -f FORMAT` and GNU `stat -f`
# (a totally different flag, "show filesystem info") share a letter but
# not a meaning - GNU accepts -f with a bogus FORMAT arg and just prints
# default filesystem info instead of erroring, so a GNU-second fallback
# never fires and $MODE silently captures the wrong multi-line output.
MODE=$(stat -c '%a' "$WORK/audit.jsonl" 2>/dev/null || stat -f '%Lp' "$WORK/audit.jsonl")
[ "$MODE" = "600" ] || { echo "FAIL: audit.jsonl mode = $MODE, want 600"; exit 1; }
echo "ok - audit.jsonl is 0600"

LINES=$(wc -l < "$WORK/audit.jsonl" | tr -d ' ')
[ "$LINES" = "2" ] || { echo "FAIL: audit.jsonl has $LINES lines, want 2 (failed primary + succeeded fallback)"; exit 1; }
echo "ok - audit.jsonl has exactly 2 lines"

python3 - "$WORK/audit.jsonl" <<'PY'
import json, sys
lines = [json.loads(l) for l in open(sys.argv[1])]
failed, ok = lines
assert failed["provider"] == "primary", failed
assert failed["status_code"] == 503, failed
assert ok["provider"] == "fallback", ok
assert ok["status_code"] == 200, ok
assert ok["prompt_tokens"] == 111, ok
assert ok["completion_tokens"] == 22, ok
# 111/1e6*0.15 + 22/1e6*0.60 = 0.00001665 + 0.0000132 = 0.00002985
assert abs(ok["cost_usd"] - 0.00002985) < 1e-9, ok
assert ok["attempted_providers"] == ["primary", "fallback"], ok
print("ok - audit entries have the expected shape and cost math")
for entry in lines:
    blob = json.dumps(entry)
    assert "hi" not in blob, "prompt content leaked into audit log: %r" % blob
    assert "stub response" not in blob, "response content leaked into audit log: %r" % blob
print("ok - no prompt/response content in the audit log")
PY

echo "--- Authorization + model_override reached the real upstream ---"
grep -q "Bearer sk-fallback-canary stub-fallback-model" "$WORK/authz_log.txt" || {
  echo "FAIL: fallback did not see the right Authorization header + overridden model"
  cat "$WORK/authz_log.txt"; exit 1
}
echo "ok - fallback received Bearer sk-fallback-canary and model_override applied"

echo "SMOKE OK"
