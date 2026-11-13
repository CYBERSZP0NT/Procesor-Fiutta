# szmelc-router

[![CI](https://github.com/CYBERSZP0NT/szmelc-router/actions/workflows/ci.yml/badge.svg)](https://github.com/CYBERSZP0NT/szmelc-router/actions/workflows/ci.yml)
![Go](https://img.shields.io/github/go-mod/go-version/CYBERSZP0NT/szmelc-router)
![OpenAI-compatible](https://img.shields.io/badge/API-OpenAI--compatible-412991)
![Task router](https://img.shields.io/badge/routing-agentic%20task%20router-2ea44f)
![TUI](https://img.shields.io/badge/TUI-interactive-A78BFA)
![Control](https://img.shields.io/badge/control-%E2%88%9E%20auto%20%E2%87%84%20%E2%9B%AD%20control-orange)

**One static Go binary** in front of *all* your free and low-cost LLM
capacity: vendor free tiers (Google, Groq, Cerebras, Together, DeepSeek, …),
keyless endpoints (pollinations, g4f, AI Horde, …), local **Ollama**, sidecars
(llmcycle, freellmapi, g4f.dev), and **any-language plugins** via stdio
JSON-RPC. Fully **config/data-driven** — 43 known sources ship as embedded
YAML, nothing is hardcoded — and exposes a standard **OpenAI-compatible API**
so you can point *anything* (OpenAI SDK, Claude Code, OpenCode, LiteLLM,
Open WebUI, LibreChat, scripts) at it.

- `freellm_router.py` is the **legacy reference implementation** (kept for
  behavior comparison). The Go binary is the product.
- Formerly **freellm-router**, in the repo formerly named `Procesor-Fiutta`
  (old GitHub URLs redirect). The old name still works: `make install` and
  the Docker image add a `freellm-router` symlink, and an existing
  `~/.config/freellm/` (or `~/.local/share/freellm/`, `~/.cache/freellm/`)
  is used until a `szmelc/` one exists. `FREELLM_*` env vars and
  `X-Freellm-*` headers are unchanged.
- Adding a new source is **a config line or a small plugin** — never a
  code change to the core. Two supported paths, ~minutes each:
  [docs/adding-a-source.md](docs/adding-a-source.md).

## 5-minute quickstart

```bash
# 1) get the binary (pick one)
make install                                   # go install
#   or: go install github.com/CYBERSZP0NT/szmelc-router/cmd/szmelc-router@latest
#   or: docker compose -f docker/docker-compose.yml up --build
#   or: download a static binary from a release tag (dist/ layout)

# 2) (optional) put your free-tier keys somewhere — all optional
cp .env.example .env                           # fill in the ones you have

# 3) point it at a fake/local provider first
szmelc-router config init                     # writes ~/.config/szmelc/config.yaml
szmelc-router config validate                 # ✓ Config OK (43 providers, 35 routes)

# 4) talk to it — CLI
szmelc-router chat "hi" --model free          # streams the first working provider
szmelc-router models --tier                   # route aliases + free-tier notes
szmelc-router probe --provider pollinations   # 'pong' ping, latency, preview

# 5) or run the OpenAI-compatible server
szmelc-router serve --port 8787
```

```bash
curl -s http://localhost:8787/v1/chat/completions \
  -H 'content-type: application/json' \
  -d '{"model":"free","messages":[{"role":"user","content":"hi"}]}'
```

```python
from openai import OpenAI
c = OpenAI(base_url="http://localhost:8787/v1", api_key="x")
print(c.chat.completions.create(
    model="auto",   # route alias — or "groq/llama-3.3-70b-versatile", a bare id, …
    messages=[{"role": "user", "content": "hi"}]).choices[0].message.content)
```

## What it does

1. Takes a `model` — route alias (`auto`/`free`/`code`/…), exact
   `provider/model`, or a bare model id.
2. Resolves it to an ordered fallback chain from **data** (embedded defaults +
   your config), filters by availability (key present? cooldown? local up?),
   and walks the chain with key rotation, a retry-once policy, and cooldowns
   (short 30 s for 429/5xx, long 1 h for other 4xx).
3. Streams the answer back as **exact OpenAI SSE** (`chat.completion.chunk`
   + `[DONE]`), with token usage when the upstream reports it, plus an
   `X-Freellm-Routed-Via` header saying which provider served.

If provider A 429s, provider B just takes over — the client sees one
`model=free` request, as intended.

## Agentic task router — `model: "agent"`

Built in, no extra service. Instead of a fixed chain, `agent` picks the
model **per request**. There are two modes:

- **[Rozkład Jazdy]** is the default. It always goes all in: the strongest
  available model with the biggest context, whatever the task, at any cost.
- **Jako Tako** is "just enough":

1. It classifies the last user message into a role (`chat`, `code`,
   `review`, `plan`, `summarize`, …) and a complexity from 1 to 5.
2. It works out the tier that is *just enough*.
3. It routes the cheapest sufficient model first, then stronger ones as
   escalation, then weaker ones as a best effort.

In Jako Tako, a tiny model says hi, a 70B writes the mail, and the strongest
model you have is kept for the hard refactor. Switch the mode with
`szmelc-router mode jako-tako`, `PUT /v1/route/mode`, `FREELLM_TASK_MODE`, or
per request with `agent:jako-tako`. Empty or filtered non-stream answers are
retried one tier up. Every pick explains itself:

```bash
szmelc-router route "refactor this Go service for concurrency" --all   # preview + why each model lost
curl -si localhost:8787/v1/chat/completions -d '{"model":"agent","messages":[{"role":"user","content":"hi"}]}' | grep -i x-freellm-task
curl -s localhost:8787/v1/route -d '{"model":"agent:code:4","messages":[{"role":"user","content":"x"}]}'
```

You can force the classification with `agent:code:4`, the `X-Freellm-Task`
header or `metadata.task`. Tiers, rules and the catalog are data
([`defaults/agentic.yaml`](defaults/agentic.yaml) plus an `agentic:` section
in your config). Full guide: [docs/agentic.md](docs/agentic.md).

## FreeLLMAPI live — `model: "live"`, by richness

Have a [FreeLLMAPI](https://github.com/tashfeenahmed/freellmapi) Premium key
(`fla_…`, the "live" plan)? Export it, and the router checks it, pulls the
signed live catalog (Ed25519-verified) and makes **`live`** the default mode:
all in, richest first (size class, then context, then free daily quota).
Without a key nothing changes and no request is made.

```bash
export FREELLM_API_KEY=fla_…
szmelc-router live          # ✓ license active, live catalog v…, N entries, mode Live
szmelc-router chat "hi"     # no --model → "live"
```

It works with a self-hosted FreeLLMAPI sidecar too (`freellmapi-…` key). An
explicit `agentic.mode` or `FREELLM_TASK_MODE` always wins. Guide:
[docs/freellmapi-live.md](docs/freellmapi-live.md).

## Observability, control & integrations

Everything is logged to one **ledger**: every request, every upstream call
with the provider's own `x-ratelimit-*` snapshot, every task decision,
session and operator change. You can read it from the CLI, from the
interactive TUI, or over HTTP. Full guide:
[docs/control-and-observability.md](docs/control-and-observability.md).

```bash
szmelc-router tui                       # interactive: dashboard · usage · history · sessions · chat · control
szmelc-router usage --models            # usage & limits across all providers/models (live or from the ledger)
szmelc-router history -f                # full log: requests, attempts, tasks, sessions — live
szmelc-router history sessions          # per-session totals; history show <id> for one request

szmelc-router ctl profile auto          # ∞ infinite & full auto: fallback, wait out cooldowns, retry until answered
szmelc-router ctl profile control       # ⛭ 120% control: no fallback, no silent retries, budgets, veto hooks
szmelc-router ctl disable groq · pause · forever on · max-wait 10m · hook pre ./guard.sh · cooldowns clear

git diff | szmelc-router exec -m code "review this"   # scripts: stdin in, answer out, exit 0/1/2/3
eval "$(szmelc-router env --session nightly)"          # any OpenAI-compatible tool → the router
szmelc-router wrap --profile auto -- python agent.py   # any harness, tracked as one session
```

Declare the quotas you know (they are never guessed), and the router
tracks them, and in `auto`/`control` also enforces them as a budget:

```yaml
limits:
  groq:       {rpm: 30, rpd: 14400}
  openrouter: {rpm: 20, rpd: 50}
```

## Zero-key stack (Docker)

```bash
docker compose -f docker/docker-compose.zerokey.yml up --build
# → g4f sidecar + python-example plugin + router, no accounts, no keys.
curl -s http://localhost:8787/v1/chat/completions \
  -d '{"model":"free","messages":[{"role":"user","content":"hi"}]}'
```

## Surface

| Shape | What |
|---|---|
| **CLI** | `chat`, `exec`/`ask`, `route`, `models`, `probe`, `providers (list/add/remove/test)`, `notes`, `usage`/`limits`, `history`/`log` (`sessions`, `show`, `export`), `stats`, `ctl`, `env`, `wrap`, `tui`, `mode`, `config (path/show/validate/init)`, `serve` |
| **TUI** | `szmelc-router tui` — dashboard, usage & limits, history, sessions, chat, control plane (live or local) |
| **Server** | `POST /v1/chat/completions`, `GET /v1/models`, `GET /v1/health`, `GET /v1/notes`, `GET /v1/metrics` (Prometheus text format), `GET|POST /v1/route` (task router), `GET /v1/usage`, `GET /v1/history` (`?follow=1` SSE), `GET /v1/sessions`, `GET|PUT|PATCH /v1/control`, `DELETE /v1/cooldowns`, `GET /v1/ping`, session base URL `/s/<id>/v1` — plus middleware: logging → CORS → auth (bearer, constant-time) → rate-limit → usage journal → panic-recover |
| **Go library** | `pkg/router` — embed the whole fallback engine in your Go service in < 30 lines; reference: [`example/embed.go`](example/embed.go) |
| **Any-language plugins** | `impl: plugin` — any subprocess speaking stdio JSON-RPC (spec: [docs/plugin-protocol.md](docs/plugin-protocol.md)); references in `plugins/python-example` and `plugins/go-example` |
| **Docker** | distroless image + compose examples (`docker/`) |

## Config

Three layers: embedded defaults → `~/.config/szmelc/config.yaml` (or
`$FREELLM_CONFIG`) → env + `.env`, with `${VAR:-default}` expansion. Full
reference: [docs/config-reference.md](docs/config-reference.md).

```yaml
routes:
  free: [g4f/gpt-4o-mini, pollinations/openai, ollama/llama3.2:3b]
providers:
  - {name: groq,      impl: openailike, base_url: https://api.groq.com/openai/v1, key_env: GROQ_API_KEY, kind: keyed}
  - {name: pollinations, impl: openailike, base_url: https://text.pollinations.ai/openai, kind: keyless}
  - {name: ollama,    impl: ollama,     base_url: ${OLLAMA_BASE_URL:-http://localhost:11434}, kind: local}
  - {name: myscraper, impl: plugin, command: ["python3", "/opt/myscraper/server.py"], kind: plugin}
server: {host: 0.0.0.0, port: 8787, auth: {mode: none}}
```

Free-tier notes for every source live in `defaults/providers.yaml`
(machine-readable data, not prose) and are exposed via `szmelc-router
notes`, `models --tier`, and `GET /v1/notes`.

## Docs

| | |
|---|---|
| [docs/agentic.md](docs/agentic.md) | the agentic task router: classification, tiers, rules, overrides, config |
| [docs/control-and-observability.md](docs/control-and-observability.md) | ledger, usage & limits, history, sessions, control profiles (∞ auto / ⛭ control), hooks, `exec`/`env`/`wrap`, TUI |
| [docs/integrations.md](docs/integrations.md) | copy-paste blocks: OpenAI py/JS SDK, curl, Claude Code, Open WebUI, LibreChat, LiteLLM, MCP, Go embed |
| [docs/adding-a-source.md](docs/adding-a-source.md) | the two supported onboarding paths (config line / plugin) |
| [docs/plugin-protocol.md](docs/plugin-protocol.md) | stdio JSON-RPC spec + minimal reference impl |
| [docs/config-reference.md](docs/config-reference.md) | full config schema, generated from `internal/config` |
| `defaults/providers.yaml` | the 43 known sources + free-tier notes (the "MODELS.md" replacement) |
| [AGENTS.md](AGENTS.md) | architecture contract + the 8-step build plan (steps 1–8 done) |

## Dev

```bash
make build      # → ./szmelc-router
make test       # go test -race ./...
make lint       # go vet (+ golangci-lint if present)
make release    # cross-compile static binaries → dist/ (+ SHA256SUMS)
go run ./plugins/go-example      # the Go reference plugin
python3 plugins/python-example/plugin.py   # the Python reference plugin
```

Repo layout: `cmd/` (thin main) · `internal/` (core, config, providers,
server, cli, catalog, journal) · `pkg/router` (public API) · `pkg/pluginhost`
(plugin-embedding helper) · `defaults/` (embedded data) · `plugins/`
(reference plugins) · `docker/` (images + compose stacks) · `example/`
(embedding example).

## Status

This codebase is v1.0.0-rc1: steps 1–8 of the plan in AGENTS.md are complete
(skeleton, provider+router, CLI, server, plugins, catalog, resilience,
distribution). See [CHANGELOG.md](CHANGELOG.md) and
[docs/PROGRESS.md](docs/PROGRESS.md) for the step log.
