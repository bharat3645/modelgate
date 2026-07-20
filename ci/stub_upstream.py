#!/usr/bin/env python3
"""Minimal OpenAI-compatible /chat/completions stub for smoke testing.

Usage: stub_upstream.py PORT STATUS [PROMPT_TOKENS COMPLETION_TOKENS]
  PORT   port to listen on
  STATUS HTTP status to return for every request
  PROMPT_TOKENS / COMPLETION_TOKENS  usage fields for a 200 response
    (ignored for non-200 STATUS)

Records every Authorization header it receives to authz_log.txt in the
current directory, one per line - the smoke test uses this to verify
modelgate rewrote the Authorization header per-provider correctly.
"""
import http.server
import json
import sys

PORT = int(sys.argv[1])
STATUS = int(sys.argv[2])
PROMPT_TOKENS = int(sys.argv[3]) if len(sys.argv) > 3 else 0
COMPLETION_TOKENS = int(sys.argv[4]) if len(sys.argv) > 4 else 0


class Handler(http.server.BaseHTTPRequestHandler):
    def log_message(self, fmt, *args):
        pass  # keep smoke output readable

    def do_POST(self):
        length = int(self.headers.get("Content-Length", 0))
        body = self.rfile.read(length)
        try:
            req = json.loads(body) if body else {}
        except json.JSONDecodeError:
            req = {}

        with open("authz_log.txt", "a") as f:
            f.write(self.headers.get("Authorization", "") + " " + str(req.get("model")) + "\n")

        self.send_response(STATUS)
        self.send_header("Content-Type", "application/json")
        self.end_headers()
        if STATUS == 200:
            resp = {
                "id": "chatcmpl-stub",
                "choices": [{"message": {"role": "assistant", "content": "stub response"}}],
                "usage": {
                    "prompt_tokens": PROMPT_TOKENS,
                    "completion_tokens": COMPLETION_TOKENS,
                    "total_tokens": PROMPT_TOKENS + COMPLETION_TOKENS,
                },
            }
        else:
            resp = {"error": {"message": "stub failure", "code": STATUS}}
        self.wfile.write(json.dumps(resp).encode())


if __name__ == "__main__":
    http.server.HTTPServer(("127.0.0.1", PORT), Handler).serve_forever()
