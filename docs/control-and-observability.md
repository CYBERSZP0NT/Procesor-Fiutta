# Observability & control

Everything the router does lands in one **ledger**. You read it with
`usage`, `history` and `tui`. You steer the router with the **control
plane**, which runs between two ends: **∞ auto** (infinite and fully
automatic) and **⛭ control** (strict, nothing happens that you did not
allow). You can plug the router into any script or harness with `exec`,
`env` and `wrap`.

```
freellm-router tui                 # the interactive UI over all of it
freellm-router usage               # usage & limits, every provider/model
freellm-router history -f          # every request / call / task, live
freellm-router ctl profile auto    # ∞  infinite & full auto
freellm-router ctl profile control # ⛭  120% control
freellm-router wrap -- <harness>   # run anything against the router as a session
```

## The ledger

The ledger is one append-only JSONL file:
`~/.local/share/freellm/usage.jsonl`, or `$XDG_DATA_HOME/freellm/…`, or
`ledger.file` in the config. The server and every CLI process (`chat`,
`exec`, `wrap`, `tui`) append to it. Only the server rotates it (10 MiB × 3
files by default), so short-lived processes never race on a rename. Lines
written by the old usage journal are still read: a line with no `kind` is
treated as a request.

| kind | one line per… | key fields |
|---|---|---|
| `request` | client call (HTTP, `chat`, `exec`, TUI chat), **failures included** | `model`, `route`, `via`, `status`, `error`, `tokens`, `attempts`, `rounds`, `waited_s`, `trail`, `task`, `profile`, `session`, `prompt`/`answer` |
| `attempt` | upstream call made while serving a request | `via`, `attempt`, `status`, `latency_ms`, `tokens`, `ratelimit` (the provider's `x-ratelimit-*` / `retry-after`), `cooldown_s` |
| `task` | task-router pick (`model: agent`) | `task.mode`, `role`, `tier`, `chain`, `reason` |
| `session` | session start / end (`wrap`, …) | `action`, `note`, `status` (exit code) |
| `control` | operator change (`ctl`, TUI, `PUT /v1/control`) | `action`, `note` |
| `http` | other server calls (`/v1/models`, `/v1/route`, …). Polling of the observability endpoints is not logged. | `path`, `method`, `status` |

A request and all of its attempts share `request_id`. `history show <id>`
prints them together.

```yaml
ledger:
  file: /var/log/freellm/ledger.jsonl   # default: ~/.local/share/freellm/usage.jsonl
  content: preview    # off | preview (first 200 chars of prompt/answer, default) | full
                      # previews are stored on disk — use "off" for sensitive work
  max_bytes: 10485760 # server rotation
  keep: 3
  # disabled: true    # no event logging at all
```

## Usage & limits

```
freellm-router usage            # providers you used, limited or blocked
freellm-router usage --all --models
freellm-router usage --watch 2s
freellm-router limits --json
```

Every number says where it came from:

- **Observed usage** is counted from the ledger's upstream attempts over
  rolling windows: 1 minute, 1 hour, 24 hours, plus today and all time.
  A 429 or a transport error is not counted against a quota.
- **Limits** are what *you* declare. The router never ships guessed numbers,
  because free tiers change too often.
- **Upstream says** is the provider's own last `x-ratelimit-*` snapshot,
  shown with the time it was seen.
- **Cooldowns, availability and operator switches** come from a running
  server. Without a server the report is offline and built from the ledger
  alone.

```yaml
limits:                       # by provider name; overrides providers[].limits
  groq:       {rpm: 30, rpd: 14400, tpm: 6000}
  openrouter: {rpm: 20, rpd: 50}
  gemini:     {rpm: 10, rpd: 250, note: "flash free tier, re-check monthly"}
```

With `control.enforce_limits` on (it is on in both `auto` and `control`),
declared limits become a **client-side budget**. A provider that has used up
its budget, or that reported `remaining-requests: 0` before its reset time, is
skipped instead of being called and earning a 429.

Status values: `ok`, `cooldown`, `limit`, `disabled`, `unavailable`, `no key`.

## History

```
freellm-router history                          # last 50 events
freellm-router log -f                           # follow live
freellm-router history --since 2h --errors
freellm-router history -k request,attempt --provider groq -n 0
freellm-router history --session build-42 --grep timeout
freellm-router history show 3f9a                # a request + its attempts
freellm-router history sessions                 # per-session totals
freellm-router history export --format csv -o all.csv   # jsonl | json | csv
freellm-router --json history --since today     # JSONL for jq
```

`--since` and `--until` accept `30m`, `2h`, `7d`, `today`, `yesterday`,
`2026-11-01` or RFC 3339.

## Sessions

A session groups every call one tool or run makes. You can tag calls with a
session in several ways:

- base URL `http://host:8787/s/<session>/v1`. This works with any OpenAI
  client and needs no custom headers.
- header `X-Freellm-Session: <id>`, or `metadata.session` in the request body
- `exec --session <id>`, or `$FREELLM_SESSION` (read by `chat` and `exec`)
- `wrap --session <id> -- cmd` (it sets all of the above for the child process)

## The control plane

| | **∞ auto** | **⛭ control** | balanced (default) |
|---|---|---|---|
| fallback chain | yes | **no**, only the entry you asked for | yes |
| when every entry fails | **wait for the soonest cooldown and retry**, until the request is cancelled or `max_wait_s` | fail | fail |
| when the chain is exhausted | escalate once to `auto` | — | — |
| quota budgets (`limits:`) | enforced | enforced | not enforced |
| pre/post hooks | run | run | run |

Explicit settings override the profile's preset:

```yaml
control:
  profile: auto          # auto | control | balanced
  paused: false          # true: refuse every call (403 / exit 3)
  disabled: [mistral]    # providers taken out of routing
  allow: []              # non-empty: only these providers
  fallback: true         # override the preset
  forever: true
  enforce_limits: true
  max_attempts: 0        # upstream calls per request (0 = policies default)
  max_wait_s: 900        # forever mode: stop waiting after 15 min (0 = until cancelled)
  escalate: auto         # route tried once the chain is exhausted ("" = none)
  hooks:
    pre_call:  [/usr/local/bin/llm-guard]   # veto / rewrite every call
    post_call: [/usr/local/bin/llm-audit]   # fire-and-forget audit
    timeout_s: 10
```

Forever mode is bounded on purpose. It stops when the request context is
cancelled (client disconnect, Ctrl-C, `exec --deadline`), or when
`max_wait_s` is reached. It never retries a stream that has already sent its
first byte, because those bytes cannot be taken back. It waits for the
soonest cooldown, clamped to 1–60 s per round.

### Changing it

```
freellm-router ctl                       # status (live server, else config)
freellm-router ctl profile auto          # live on the running server
freellm-router ctl profile control --save   # …and persisted to the config
freellm-router ctl --local forever on    # config only
freellm-router ctl pause | resume
freellm-router ctl disable groq mistral | enable groq | enable all
freellm-router ctl allow ollama | allow --clear
freellm-router ctl fallback off | forever on | limits on   # or "default"
freellm-router ctl max-attempts 3 | max-wait 10m | escalate free | escalate none
freellm-router ctl hook pre ./guard.sh | hook pre --clear   # config file only; restart to apply
freellm-router ctl cooldowns | cooldowns clear [provider/model]
```

The same is available over HTTP. `GET /v1/control` returns the settings and
the effective behaviour. `PUT` replaces the settings. `PATCH` merges a
partial object, where `null` clears a field. `DELETE /v1/cooldowns[?via=…]`
clears cooldowns.

> **Security.**
> - **Hooks are config-file only.** They run commands on the router's
>   machine, so the API refuses to change them (HTTP 400). `ctl hook`
>   writes them to the config, and they take effect when `serve` or `wrap`
>   restarts.
> - **Some endpoints are loopback-only without auth.** While
>   `server.auth.mode` is `none`, the endpoints that change behaviour or
>   return logged content answer `127.0.0.1`/`::1` only:
>   `PUT/PATCH /v1/control`, `DELETE /v1/cooldowns`, `/v1/history` and
>   `/v1/sessions`. The default bind is `0.0.0.0`. To use them from another
>   host, enable bearer auth. Behind a reverse proxy every client looks like
>   loopback, so put auth in front of the proxy.

You can also override per request. Overrides do not touch the operator-wide
state (pause, disabled, allow, hooks):

| header | effect |
|---|---|
| `X-Freellm-Profile: auto\|control\|balanced` | profile for this call (also `metadata.profile`) |
| `X-Freellm-Fallback: off` | no fallback |
| `X-Freellm-Forever: on` | retry until answered |
| `X-Freellm-Max-Attempts: 3` | attempt budget |
| `X-Freellm-Tags: team=a,job=42` | free-form tags in the ledger and hook payloads |

Responses carry `X-Freellm-Request-Id`, `X-Freellm-Session` and
`X-Freellm-Routed-Via`.

### Hooks

`pre_call` runs before routing and receives the call as JSON on stdin:

```json
{"request_id":"rid-…","session":"build-42","source":"server","model":"auto",
 "profile":"control","messages":3,"prompt":"last user message…","tools":false,"stream":true}
```

- Exit 0 allows the call. Any other exit vetoes it, and stderr/stdout
  becomes the reason (HTTP 403 `vetoed`, `exec` exit 3).
- Stdout may be `{"allow": false, "reason": "…"}` or
  `{"allow": true, "model": "free"}`. The second form **rewrites** the
  route.
- A hook that cannot run, or that times out, **vetoes** the call. The hook
  fails closed: a guard that silently stops guarding is worse than a refused
  call.

`post_call` receives the outcome: `ok`, `via`, `error`, `attempts`,
`rounds`, `waited_s`, `latency_ms`, tokens and `trail`. It runs in the
background.

## Integrating with anything

### Scripts: `exec` (aliases `ask`, `run`)

```bash
answer=$(freellm-router exec -q "one word: capital of France") || exit $?
git diff | freellm-router ask -m code "review this diff"
freellm-router exec -f spec.md -f main.go "find the bug" --json | jq -r .text
freellm-router exec --profile auto --deadline 1h "…"        # never give up within 1h
freellm-router exec --profile control -m groq/llama-3.3-70b-versatile "…"
```

`exec` reads piped stdin as context. If a harness keeps a stdin pipe open
without writing to it, or you call `exec` inside a `while read` loop, pass
`--no-stdin` or set `FREELLM_NO_STDIN=1`; otherwise `exec` waits on stdin.

`exec` prints the answer on stdout and diagnostics on stderr (set
`FREELLM_VERBOSE=1` to see the route taken). The exit codes are stable:
`0` answered · `1` usage/config error · `2` every provider failed (or
forever mode gave up) · `3` vetoed (paused, or a hook refused).

### Any OpenAI-compatible tool: `env`

```bash
eval "$(freellm-router env --session nightly)"   # OPENAI_BASE_URL, OPENAI_API_BASE, OPENAI_API_KEY, FREELLM_URL, FREELLM_SESSION
freellm-router env --shell fish | source
freellm-router env --shell dotenv >> .env         # also: powershell, json
```

### Harnesses / agents: `wrap`

```bash
freellm-router wrap --session refactor-42 --profile auto -- aider
freellm-router wrap --profile control -- ./my-harness.sh --task x
```

`wrap` uses the running server, or starts a private server on a free
loopback port for the lifetime of the command. It points the command at the
server through the environment and records session start and end with the
exit code. When the command finishes it prints a session summary, and it
returns the command's exit code. `--profile` applies only to a private
server. On a shared server, use `ctl` or headers.

### HTTP API

| endpoint | |
|---|---|
| `GET /v1/ping` | cheap liveness: pid, uptime, in-flight, ledger path, profile |
| `GET /v1/usage` | the usage & limits report (JSON of `freellm-router usage`) |
| `GET /v1/history?since=&until=&kind=&session=&provider=&id=&source=&errors=1&q=&limit=` | ledger events (`limit` defaults to 200). Loopback-only without auth |
| `GET /v1/history?follow=1&…` | backlog, then live events as Server-Sent Events |
| `GET /v1/sessions[?limit=]` | per-session summaries |
| `GET/PUT/PATCH /v1/control` | control plane |
| `DELETE /v1/cooldowns[?via=]` | clear cooldowns |

### Go

`pkg/router` exposes the same hooks: `SetControl`, `SetLimiter`,
`SetOnAttempt`, `SetOnTask`, `WithOverride`, `Cooldowns`,
`ClearCooldowns`, `Unavailable`, `KeyCount`.

## The TUI

`freellm-router tui` (aliases `ui`, `top`) has six tabs:

1. **Dashboard**: totals, a requests-per-minute sparkline, top providers and
   recent failures.
2. **Usage**: limit gauges and the provider's own quota; `enter` shows
   per-model detail, `d` disables or enables a provider, `c` clears its
   cooldowns, `a` lists all providers.
3. **History**: `/` searches, `e` shows errors only, `t` cycles the event
   kind, `f` follows new events, `enter` shows the full event with every
   attempt of its request.
4. **Sessions**: `enter` opens the session's history, `c` chats inside it.
5. **Chat**: answers stream in; `/model`, `/session`, `/system` and `/new`
   are commands.
6. **Control**: switches the profile, pause, fallback, forever, limits, max
   attempts and wait, and escalation; toggles providers on/off and allow-list
   membership; clears cooldowns; `s` saves to the config.

When a server is running, the TUI is live and its changes apply to that
server. Without one it reads the ledger and routes chat in-process.
`--local` forces local mode.
