# External plugin protocol (stdio JSON-RPC)

**Version:** 1.0 (v1.0.0-rc1)
**Status:** stable — implemented in `internal/providers/pluginproc`, hosted in
`pkg/pluginhost`, referenced by `plugins/go-example` and `plugins/python-example`.

This protocol lets **any process in any language** be a freellm-router
provider without Go code or recompiling. A plugin is a subprocess that speaks
JSON-RPC 2.0 over stdio (LSP-style): requests on stdin, responses + stream
notifications on stdout, logs on stderr.

```
┌────────────┐  stdin: JSON-RPC requests   ┌────────────┐
│  router    │ ──────────────────────────▶ │  plugin    │
│ (launcher) │ ◀────────────────────────── │ (any lang) │
│            │  stdout: responses         │            │
│            │  stdout: text_delta notif. │            │
└────────────┘  stderr: logs (→ slog)     └────────────┘
```

## Wiring a plugin in config

```yaml
providers:
  - name: myscraper
    impl: plugin
    kind: plugin
    command: ["python3", "/opt/myscraper/server.py"]
    env: {SCRAPER_KEY: "${SCRAPER_KEY:-}"}
    handshake_timeout: 10        # seconds (default 10)
    restart_policy: on-crash     # "on-crash" (default) | "never"
    restart_max: -1              # max restarts before giving up; -1 = infinite, 0 = never retry

routes:
  free: [myscraper/my-model, …]
```

Declaration via CLI: `freellm-router providers add --name myscraper --impl
plugin --kind plugin --command "python3 /opt/myscraper/server.py"`.

## Framing

One JSON object per line (newline-delimited JSON). No Content-Length headers,
no nesting of messages, UTF-8. stdout is **exclusively** protocol output —
plugins must log to stderr (the router forwards stderr to its own logger).

## Methods (router → plugin)

| Method | Purpose | Result |
|---|---|---|
| `initialize` | handshake (session-level; happens once, before any other call) | `{"name":…, "version":…, "models": …}` any object |
| `models` | model catalogue | `[{"id": "hello"}, …]` |
| `available` | availability probe (used by `probe`, `/v1/health`, router filter) | `{"available": true, "reason": "optional text"}` |
| `stream` | one chat request | see below (streaming) |
| `shutdown` | graceful stop (optional to implement) | `null` |

Request shape: `{"jsonrpc":"2.0","id":<int>,"method":"stream","params":{…}}`
Notification shape: `{"jsonrpc":"2.0","method":"…","params":{…}}` (no `id`).
Response shape: same `id`, with either `result` or `error`.

### `stream` params

```json
{
  "model": "my-model",
  "messages": [{"role": "user", "content": "hi"}],
  "stream": true,
  "max_tokens": 256
}
```

### Streaming responses

For `stream` the plugin sends **zero or more notifications** and then exactly
one response:

1. `{"jsonrpc":"2.0","method":"notification/streams/text_delta","params":{"text":"chun"}}`
   — zero or more, in order; the router forwards each to the caller.
2. `{"jsonrpc":"2.0","id":7,"result":{"finish_reason":"stop","prompt_tokens":-1,"completion_tokens":-1}}`

Token counts are optional (`-1`/`0` = "upstream didn't report"). Any other
JSON in `result` is tolerated; unknown fields are ignored.

### Plugin → router errors

`{"jsonrpc":"2.0","id":7,"error":{"code":-32000,"message":"boom"}}` — the
router maps this to an `UpstreamErr` and continues down the fallback chain.
Standard codes: `-32000` application error, `-32601` method not found.

## Router-side lifecycle (what the plugin can rely on)

- **One process per provider**, spawned lazily on first use, handshake with
  `handshake_timeout` (default 10 s). A second handshake is *not* sent after
  a crash-restart — the re-sent request is the first line the fresh process
  sees.
- **Crash handling:** request/response cycle serializes I/O; if the process
  exits mid-stream, the router kills it, restarts (if `restart_policy` allows
  and budget remains), and re-sends the request **exactly once**. Plugins
  should therefore treat `stream` as at-least-once and be idempotent in
  their *effect on the caller* (a caller may occasionally see repeated
  deltas on a crash-boundary — prefer small delta chunks).
- **stderr → slog** at debug level, prefixed with the provider name.
- **EOF/exit without `shutdown`** = crash, handled as above.

## Minimal reference implementation (~40 lines, any language)

```python
import json, sys

def line():
    ln = sys.stdin.readline()
    return json.loads(ln) if ln else None

def out(o): sys.stdout.write(json.dumps(o) + "\n"); sys.stdout.flush()

while True:
    m = line()
    if m is None: break
    p = m.get("params") or {}
    if m["method"] == "initialize":
        out({"jsonrpc":"2.0","id":m["id"],"result":{"name":"mini","version":"1"}})
    elif m["method"] == "models":
        out({"jsonrpc":"2.0","id":m["id"],"result":[{"id":"echo"}]})
    elif m["method"] == "available":
        out({"jsonrpc":"2.0","id":m["id"],"result":{"available":True}})
    elif m["method"] == "stream":
        txt = "echo: " + (p["messages"][-1].get("content") or "")
        out({"jsonrpc":"2.0","method":"notification/streams/text_delta","params":{"text":txt}})
        out({"jsonrpc":"2.0","id":m["id"],"result":{"finish_reason":"stop","prompt_tokens":-1,"completion_tokens":-1}})
    elif m["method"] == "shutdown":
        if m.get("id") is not None:
            out({"jsonrpc":"2.0","id":m["id"],"result":None})
        break
```

Go plugins: import `github.com/silverx/freellm-router/pkg/pluginhost` — one
import, register `Models` / `Available` / `Stream`, call `Run()` (see
`plugins/go-example`).

## Compatibility notes

- `text_delta` is the stable stream method; do not change its name or param
  shape without bumping this spec's version.
- The router currently does not send tool-call arguments to plugins (plugins
  return plain text); a `tool_delta` slot is reserved.
