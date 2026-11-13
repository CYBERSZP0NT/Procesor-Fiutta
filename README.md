# freellm-router

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
- Adding a new source is **a config line or a small plugin** — never a
  code change to the core. Two supported paths, ~minutes each:
  [docs/adding-a-source.md](docs/adding-a-source.md).

## 5-minute quickstart

```bash
# 1) get the binary (pick one)
make install                                   # go install
#   or: docker compose -f docker/docker-compose.yml up --build
#   or: download a static binary from a release tag (dist/ layout)

# 2) (optional) put your free-tier keys somewhere — all optional
cp .env.example .env                           # fill in the ones you have

# 3) point it at a fake/local provider first
freellm-router config init                     # writes ~/.config/freellm/config.yaml
freellm-router config validate                 # ✓ Config OK (43 providers, 35 routes)

# 4) talk to it — CLI
freellm-router chat "hi" --model free          # streams the first working provider
freellm-router models --tier                   # route aliases + free-tier notes
freellm-router probe --provider pollinations   # 'pong' ping, latency, preview

# 5) or run the OpenAI-compatible server
freellm-router serve --port 8787
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
model **per request**:

1. It classifies the last user message into a role (`chat`, `code`,
   `review`, `plan`, `summarize`, …) and a complexity from 1 to 5.
2. It works out the tier that is *just enough*.
3. It routes the cheapest sufficient model first, then stronger ones as
   escalation, then weaker ones as a best effort.

A tiny model says hi, a 70B writes the mail, and the strongest model you have
is kept for the hard refactor. Empty or filtered non-stream answers are
retried one tier up. Every pick explains itself:

```bash
freellm-router route "refactor this Go service for concurrency" --all   # preview + why each model lost
curl -si localhost:8787/v1/chat/completions -d '{"model":"agent","messages":[{"role":"user","content":"hi"}]}' | grep -i x-freellm-task
curl -s localhost:8787/v1/route -d '{"model":"agent:code:4","messages":[{"role":"user","content":"x"}]}'
```

You can force the classification with `agent:code:4`, the `X-Freellm-Task`
header or `metadata.task`. Tiers, rules and the catalog are data
([`defaults/agentic.yaml`](defaults/agentic.yaml) plus an `agentic:` section
in your config). Full guide: [docs/agentic.md](docs/agentic.md).

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
| **CLI** | `chat`, `route`, `models`, `probe`, `providers (list/add/remove/test)`, `notes`, `stats`, `config (path/show/validate/init)`, `serve` |
| **Server** | `POST /v1/chat/completions`, `GET /v1/models`, `GET /v1/health`, `GET /v1/notes`, `GET /v1/metrics` (Prometheus text format), `GET|POST /v1/route` (task router) — plus middleware: logging → CORS → auth (bearer, constant-time) → rate-limit → usage journal → panic-recover |
| **Go library** | `pkg/router` — embed the whole fallback engine in your Go service in < 30 lines; reference: [`example/embed.go`](example/embed.go) |
| **Any-language plugins** | `impl: plugin` — any subprocess speaking stdio JSON-RPC (spec: [docs/plugin-protocol.md](docs/plugin-protocol.md)); references in `plugins/python-example` and `plugins/go-example` |
| **Docker** | distroless image + compose examples (`docker/`) |

## Config

Three layers: embedded defaults → `~/.config/freellm/config.yaml` (or
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
(machine-readable data, not prose) and are exposed via `freellm-router
notes`, `models --tier`, and `GET /v1/notes`.

## Docs

| | |
|---|---|
| [docs/agentic.md](docs/agentic.md) | the agentic task router: classification, tiers, rules, overrides, config |
| [docs/integrations.md](docs/integrations.md) | copy-paste blocks: OpenAI py/JS SDK, curl, Claude Code, Open WebUI, LibreChat, LiteLLM, MCP, Go embed |
| [docs/adding-a-source.md](docs/adding-a-source.md) | the two supported onboarding paths (config line / plugin) |
| [docs/plugin-protocol.md](docs/plugin-protocol.md) | stdio JSON-RPC spec + minimal reference impl |
| [docs/config-reference.md](docs/config-reference.md) | full config schema, generated from `internal/config` |
| `defaults/providers.yaml` | the 43 known sources + free-tier notes (the "MODELS.md" replacement) |
| [AGENTS.md](AGENTS.md) | architecture contract + the 8-step build plan (steps 1–8 done) |

## Dev

```bash
make build      # → ./freellm-router
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
