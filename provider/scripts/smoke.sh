#!/usr/bin/env bash
# Local end-to-end check of the agy-provider helper against a stub host.
# Exercises the parts no unit test reaches: the detached proxy, the inherited
# listener, the launch environment and the streaming path end to end.
set -Eeuo pipefail

root="$(cd "$(dirname "$0")/../.." && pwd)"
work="$(mktemp -d)"
trap 'set +e; "$bin" stop >/dev/null 2>&1; [[ -n "${stub_pid:-}" ]] && kill "$stub_pid" 2>/dev/null; rm -rf "$work"' EXIT

export AGY_PROVIDER_HOME="$work/home"
export AGY_CLI_SETTINGS="$work/settings.json"
export AGY_CLI_SKILLS="$work/skills"
export NO_COLOR=1

bin="$work/agy-provider"
say() { printf '\n\033[36m== %s\033[0m\n' "$*"; }

say "building the helper for this host"
(cd "$root/provider" && go build -o "$bin" ./cmd/agy-provider)

say "starting a stub OpenAI-shaped host"
python3 - "$work/port" <<'PY' &
import http.server, json, sys, threading

class Handler(http.server.BaseHTTPRequestHandler):
    def log_message(self, *a): pass

    def do_GET(self):
        if self.path.endswith("/models"):
            self.send(200, {"data": [{"id": "demo"}, {"id": "demo-mini"}]})
        else:
            self.send(404, {"error": {"message": "no"}})

    def do_POST(self):
        length = int(self.headers.get("content-length", 0))
        body = json.loads(self.rfile.read(length) or b"{}")
        key = self.headers.get("authorization", "")
        # A first key that is always rejected proves rotation happens.
        if key == "Bearer sk-dead":
            self.send(429, {"error": {"message": "rate limited"}})
            return
        if body.get("stream"):
            self.send_response(200)
            self.send_header("content-type", "text/event-stream")
            self.end_headers()
            for chunk in (
                '{"choices":[{"delta":{"content":"streamed "}}]}',
                '{"choices":[{"delta":{"content":"reply"}}]}',
                '{"choices":[{"delta":{},"finish_reason":"stop"}]}',
                '{"choices":[],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}',
                "[DONE]",
            ):
                self.wfile.write(f"data: {chunk}\n\n".encode())
                self.wfile.flush()
            return
        self.send(200, {"id": "c1", "model": body.get("model"), "choices": [
            {"index": 0, "message": {"role": "assistant", "content": "whole reply"},
             "finish_reason": "stop"}],
            "usage": {"prompt_tokens": 3, "completion_tokens": 2, "total_tokens": 5}})

    def send(self, status, payload):
        raw = json.dumps(payload).encode()
        self.send_response(status)
        self.send_header("content-type", "application/json")
        self.send_header("content-length", str(len(raw)))
        self.end_headers()
        self.wfile.write(raw)

server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Handler)
with open(sys.argv[1], "w") as fh:
    fh.write(str(server.server_address[1]))
server.serve_forever()
PY
stub_pid=$!

for _ in $(seq 50); do [[ -s "$work/port" ]] && break; sleep 0.1; done
stub_port="$(cat "$work/port")"
echo "stub listening on 127.0.0.1:$stub_port"

say "adding a provider with two keys, the first of them dead"
"$bin" add --no-prompt --name stub --kind openai \
  --base-url "http://127.0.0.1:$stub_port/v1" \
  --key sk-dead --key sk-live --strategy rotate --model demo \
  --header "X-Tenant=acme"

say "list"
"$bin" list
"$bin" list | grep -F "stub" >/dev/null
"$bin" list | grep -F "via local proxy" >/dev/null

say "the CLI setting was written"
grep -F '"modelProvider": "gemini"' "$AGY_CLI_SETTINGS"

say "model discovery"
"$bin" models stub </dev/null | grep -F "demo-mini" >/dev/null
echo "discovered models listed"

say "key check"
"$bin" test stub || echo "(a dead key is expected to fail)"

say "launch environment"
env_lines="$("$bin" up)"
printf '%s\n' "$env_lines"
token="$(printf '%s\n' "$env_lines" | sed -n 's/^GEMINI_API_KEY=//p')"
base="$(printf '%s\n' "$env_lines" | sed -n 's/^GOOGLE_GEMINI_BASE_URL=//p')"
[[ -n "$token" && -n "$base" ]] || { echo "no environment produced" >&2; exit 1; }

say "the detached proxy is alive"
curl -fsS "$base/__agy/health"; echo
"$bin" status | grep -F "running" >/dev/null

say "a whole reply, translated both ways"
request='{"contents":[{"role":"user","parts":[{"text":"hello"}]}],
  "systemInstruction":{"parts":[{"text":"be brief"}]},
  "generationConfig":{"maxOutputTokens":64}}'
whole="$(curl -fsS -H "x-goog-api-key: $token" -H 'content-type: application/json' \
  -d "$request" "$base/v1beta/models/gemini-3-pro:generateContent")"
printf '%s\n' "$whole"
printf '%s\n' "$whole" | grep -F '"text":"whole reply"' >/dev/null

say "a streamed reply"
streamed="$(curl -fsS -N -H "x-goog-api-key: $token" -H 'content-type: application/json' \
  -d "$request" "$base/v1beta/models/gemini-3-pro:streamGenerateContent?alt=sse")"
printf '%s\n' "$streamed"
printf '%s\n' "$streamed" | grep -F 'streamed ' >/dev/null
printf '%s\n' "$streamed" | grep -F '"finishReason":"STOP"' >/dev/null

say "the dead key was rotated past, and is resting"
# The proxy keeps key health in memory and flushes it on a timer, so give it
# long enough to land before reading it from another process.
sleep 3
"$bin" status
"$bin" status | grep -F "resting" >/dev/null || {
  echo "the rejected key should be resting after a 429" >&2
  exit 1
}

say "an unauthorized caller is refused"
code="$(curl -s -o /dev/null -w '%{http_code}' -H 'x-goog-api-key: wrong' \
  -d "$request" "$base/v1beta/models/gemini-3-pro:generateContent")"
[[ "$code" == "401" ]] || { echo "expected 401, got $code" >&2; exit 1; }
echo "401 as expected"

say "the slash command skill"
"$bin" install-skill
grep -F "name: provider" "$AGY_CLI_SKILLS/provider.md" >/dev/null

say "switching off"
"$bin" use none
[[ -z "$("$bin" up)" ]] || { echo "nothing should be exported once off" >&2; exit 1; }
if grep -F '"modelProvider"' "$AGY_CLI_SETTINGS"; then
  echo "the setting should be gone" >&2
  exit 1
fi

printf '\n\033[32m== every check passed\033[0m\n'
