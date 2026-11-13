# AGENTS.md — szmelc-router (Go rewrite)

Goal: replace `freellm_router.py` with a single, statically-friendly **Go binary**
(`router` / `szmelc-router`) that routes across every free/cheap LLM source we
have, is fully **config/data-driven (no hardcoded providers, routes or models)**,
exposes a **standard OpenAI-compatible API**, a **friendly CLI**, and a **library
surface** — and makes adding new token/inference sources (see `MORE.md`) a
**config line or a small plugin**, never a code change to the core.

The plan below is split into **sequential steps**. Each step is written to be
executable in a fresh context using only this file + the repo state. Steps are
cumulative: later steps build on earlier ones. Do not skip ordering; do not
rework earlier steps' public contracts unless a step explicitly says so.

---

## Ground rules (apply to every step)

- **Language:** Go 1.23+, stdlib-first. Allowed third-party deps (keep minimal):
  `github.com/spf13/cobra` (CLI), `gopkg.in/yaml.v3` (config), `github.com/pterm/pterm`
  (optional, only for pretty CLI output). Everything else stdlib
  (`net/http`, `encoding/json`, `log/slog`, `net/http/httptest`, …).
- **No hardcoded providers/models/routes in core.** Core knows *kinds* of
  providers (transport plugins); *which* providers exist, their base URLs, keys,
  and routes come from **configuration + embedded defaults** (defaults are data
  files, swappable at build time via `go:embed`, not scattered constants).
- **Everything is a plugin:** providers, route policies, key sources, cooldown
  stores, middlewares, catalog sources. Core = interfaces + composition.
- **Context:** every core function takes `context.Context` first.
- **Errors:** wrap with `fmt.Errorf("%w")`; one typed error
  `*UpstreamErr{Status int, Provider, Model, Msg string}` in the core package.
- **Testing:** every step ships unit tests; HTTP-facing code tested with
  `httptest` (mock upstreams). `golangci-lint` (if available) must pass;
  `go vet` always.
- **Docs:** keep `README.md` current; new user-visible behavior gets a
  `docs/` note. CLI `--help` text is user-facing — write it properly.
- **Repo layout is fixed from step 1** (below). Steps add files, they don't
  restructure.

---

## Architecture (the contract — all steps agree on this)

```
cmd/szmelc-router/        main.go (thin: wires plugins + calls core)
internal/core/             domain: types, resolver, router (fallback engine),
                           keyring, cooldown store, catalog, config
internal/config/           config loading: defaults < file < env overlay
internal/providers/        provider *implementations* (plugins)
  openailike/              one provider covers ANY OpenAI-compatible HTTP API
                           (google, groq, openrouter, pollinations, sidecars, custom…)
  ollama/                  native /api/* + /v1
  g4fbridge/               (optional) talks to a gpt4free sidecar (py/g4f.dev/hosted)
  pluginproc/              EXTERNAL plugins: JSON-RPC over stdio (see below)
internal/server/           OpenAI-compatible HTTP server (chat completions,
                           models, health, metrics) + middleware chain
internal/cli/              cobra commands
internal/keys/             key sources: env, file, (later: keyring, vault)
internal/agentic/          task router: classifier, tier rules, picker (data in defaults/agentic.yaml)
pkg/router/                small public Go API (for embedding)
defaults/                  embedded YAML: default providers, default routes,
                           free-tier notes (data, not code)
plugins/                   example external plugins (one Go, one shell/python)
docs/                      design notes, plugin protocol spec
```

### Core interfaces (`internal/core`)

```go
// Provider = one routable inference source. THE extension point.
type Provider interface {
    Name() string                                  // "groq", "pollinations", …
    Models(ctx context.Context) ([]Model, error)   // catalog (may be empty)
    Stream(ctx context.Context, req *ChatRequest, out *StreamOut) (*StreamDone, error)
    Available(ctx context.Context) *Availability   // key present? up? cooldown?
    Kinds() []Kind                                 // keyless | keyed | local | scraper | sidecar
}

type Model struct { ID string; Name string; Kinds []Kind; Tier TierInfo } // Tier = free-tier notes

// ChatRequest: Messages, Model, MaxTokens, Temperature, TopP, Tools, ToolChoice, Stream…
// StreamOut: TextSink func(string), ToolSink func(ToolChunk), ReasoningSink func(string)
// StreamDone: FinishReason, PromptTokens, CompletionTokens (when upstream reports)

// RoutePolicy expands a requested model string ("auto" | "groq/…" | bare id)
// into an ordered []provider.Model chain. Default policy = route tables;
// pluggable (e.g. semantic routing later).
type RoutePolicy interface { Resolve(req ModelSelector, known map[string]Model) []provider.Model }

// KeySource supplies credentials per provider; multi-key => rotation.
type KeySource interface { Keys(ctx context.Context, provider string) []string }

// CooldownStore: set/has/get with TTL (default: in-mem; pluggable: file/redis).
type CooldownStore interface { ... }

// CatalogSource: extra model/tier metadata (default provider /models,
// embedded notes, remote catalog from freellmapi etc).
type CatalogSource interface { Fetch(ctx context.Context) []ModelNote }
```

### The Router (heartbeat)

`Router.Route(ctx, sel ModelSelector, req, policy RoutePolicy) (*stream, label)` —
resolve chain → filter by `Available()` + cooldown → walk in order with
retry-once policy → on 4xx (except 429/408) long cooldown (1h), on 429/5xx
short cooldown (30s, pluggable policy) → return `(fullText, via)`.
Key rotation: round-robin over `KeySource.Keys()` per provider.

### External plugin protocol (`pluginproc`)

So *anything* (python, node, a g4f wrapper, a scraper CLI) can be a provider
**without Go code or recompiling**: a subprocess speaks JSON-RPC 2.0 over stdio
(LSP-style): methods `initialize`, `models`, `available`, `stream`
(stream = server sends `text_delta` notifications then `done` result).
Declared in config:

```yaml
providers:
  - name: myscraper
    kind: plugin
    command: ["python3", "/opt/myscraper/server.py"]
    env: {SCAPER_KEY: "${SCRAPER_KEY}"}
```

Spec lives in `docs/plugin-protocol.md`; a reference Go plugin + a reference
python plugin ship in `plugins/`. This is the "add a NEW source in the future
without hassle" guarantee: **(a) if it's OpenAI-compatible → 3 lines of config**,
**(b) otherwise → any-l-language plugin via stdio JSON-RPC**.

### Configuration (single source of truth)

`~/.config/szmelc/config.yaml` (or `$FREELLM_CONFIG`), layered over embedded
defaults, then `${ENV_VAR}` expansion + a `.env` loader.

```yaml
routes:                      # alias -> ordered chain (data, mirrors old DEFAULT_ALIASES)
  auto:  [google/gemini-2.5-flash, groq/llama-3.3-70b-versatile, …]
  free:  [g4fbridge/gpt-4o-mini, ollama/llama3.2:3b]
  …
providers:                   # every source is a config entry (none hardcoded)
  - {name: groq,        impl: openailike, base_url: https://api.groq.com/openai/v1, key_env: GROQ_API_KEY, kind: keyed,  weight: 10}
  - {name: pollinations,impl: openailike, base_url: https://text.pollinations.ai/openai, kind: keyless}
  - {name: ollama,      impl: ollama,     base_url: ${OLLAMA_BASE_URL:-http://localhost:11434}, kind: local}
  - {name: g4fbridge,   impl: g4fbridge,  base_url: https://api.g4f.dev/v1, kind: keyless}
server: {host: 0.0.0.0, port: 8787, auth: {mode: none|bearer, keys: []}}
policies: {cooldown_short_s: 30, cooldown_long_s: 3600, retry_same: false, max_chain_attempts: 6}
catalog: {sources: [providers, embedded, {remote: https://freellmapi.co/models}]}
logging: {level: info, format: json|pretty, file: ""}
```

---

## STEP 1 — Skeleton, config, domain types (foundation)

**Goal:** repo compiles, config system + core types exist, zero HTTP yet.

- `go mod init github.com/<you>/szmelc-router`; directory layout above;
  `Makefile` (build/test/lint/release), `.gitignore` update, gofmt/vet green.
- `internal/config`: schema structs matching the YAML above; loader with
  3-layer merge (embedded defaults in `defaults/*.yaml` via `go:embed` →
  config file → env + `.env`); `${VAR:-default}` expansion; `Validate()` with
  human-readable errors (unknown `impl` names, bad routes, port range…).
- `internal/core`: all types + interfaces from the contract section
  (`Provider`, `Model`, `ChatRequest`, `StreamOut`/`StreamDone`, `RoutePolicy`,
  `KeySource`, `CooldownStore`, `CatalogSource`, `*UpstreamErr`); in-memory
  `CooldownStore` + env/file `KeySource` implementations.
- **Move all provider data out of the python file** into `defaults/providers.yaml`
  (google, groq, cerebras, sambanova, mistral, zai, openrouter, nvidia,
  github_models, cohere, ollama, llmcycle, freellmapi) and
  `defaults/routes.yaml` (auto/smart/fast/code/local/free + provider passthrough
  aliases), and **the §4 providers from MORE.md** (pollinations, puter-ish,
  opencode zen, llm7, kilo, aionlabs, hf router, modelscope, siliconflow, xai,
  deepseek, chutes, glhf, agnes, alibaba/bailian, ovh, ai21, nebius, nscale,
  together, fireworks, replicate, deepinfra, novita, hyperbolic, aimlapi,
  ollama cloud, ai horde) — each with kind + base_url + key env + free-tier
  notes (moved from `FREE_TIER_NOTES`/MODELS.md/MORE.md). This proves the
  "no hardcoded sources" rule from day one (MORE.md §8 quick wins become data).
- Tests: config merge/overlay/expansion/validate; cooldown TTL; key rotation;
  env-var redaction in error strings.

**Exit criteria:** `go build ./... && go test ./...` green; new provider
addable by YAML only; no provider name appears as a constant in `internal/core`.

---

## STEP 2 — OpenAI-like provider + router engine

**Goal:** the heartbeat works end-to-end in-process (no server/CLI yet):
`pkg/router` lets a test (or embedded caller) send a prompt through a real
fallback chain against a `httptest` fake upstream.

- `internal/providers/openailike`: full SSE client (chunked `data:`/`[DONE]`),
  headers per provider (Bearer, `x-goog-api-key`, none), `tools`/`tool_choice`
  pass-through, `max_tokens/temperature/top_p` opts, per-request timeout,
  `/models` fetch, `Available()` (key present / keyless / reachability probe
  optional), error mapping → `*UpstreamErr`.
- `internal/core/router`: `Route()` per contract — chain resolution via
  `RoutePolicy` (default policy: named route → chain; `provider/model` → that
  + `auto` safety net; bare model → `auto` + that model on any provider that
  lists it; provider name → `auto`; dedupe), availability filter, cooldown
  enforcement, key rotation, retry policy, `via` label, empty-response handling
  (599-equivalent).
- `RoutePolicy` default implementation + `SemanticRouteHook` interface (unused
  now — extension point for future "route by prompt" plugins).
- Tests: fallback across 3 fake upstreams (200 / 429-then-200 / 500), long vs
  short cooldown, key rotation order, tool-call streaming, `auto` safety net,
  unknown-model behavior. A `testdata/fakes.go` reusable mock upstream.

**Exit criteria:** `go test ./internal/...` green; a `cmd`-free Go test can
`pkg/router.New(cfg).Route(...)` and stream; adding a fake provider = config +
mock, no core edits.

---

## STEP 3 — CLI (cobra)

**Goal:** the daily-driver surface, parity + polish over the python CLI.

- `internal/cli` + `cmd/szmelc-router/main.go`; global flags `--config`, `--provider`,
  `--model`, `--timeout`, `--json`; subcommands:
  - `chat "prompt" [--system …] [--no-stream] [--max-tokens N] [--temperature f] [--via label]`
    (stream to stdout, TTY colorize with pterm when pretty logging on,
    `--via` appends the routing path used).
  - `models [--provider X] [--kind keyless|keyed|local] [--json] [--tier]` —
    route aliases + per-provider live catalog + free-tier notes table.
  - `probe [--provider X] [--model M] [--concurrency N] [--timeout s]` —
    ping every configured (provider, chain member) with a `pong` prompt,
    table output: ok/fail/skip, latency, response preview. (Upgraded over python:
    concurrency + filters.)
  - `providers` list/add/remove/test (edit the config file safely; `add` takes
    name/base_url/key_env/kind → **this is the "add a source from MORE.md" UX**).
  - `notes` — human free-tier cheat sheet (from catalog data, nothing hardcoded).
  - `config path|show|validate|init` — config lifecycle.
  - `serve …` — registered as a cobra stub printing "added in step 4"; the
    real implementation lands in step 4 (same command name/flags, no drift).
- `--version` with build info (`-ldflags` commit/date).
- Tests: golden outputs for `models --json`, `config validate` failures,
  `chat` against a `httptest` fake (set config to point at it), `probe` filtering.

**Exit criteria:** `szmelc-router chat "hi" --model free` works against fakes;
all `--help` texts complete; `make install` gives a working binary;
`providers add` with a MORE.md entry routes through it.

---

## STEP 4 — OpenAI-compatible server + middleware pipeline

**Goal:** point *anything* (OpenAI SDK, Claude Code, OpenCode, pi, scripts) at it.

- `internal/server`: `net/http` server
  - `POST /v1/chat/completions` (+ `POST /chat/completions`) — streaming &
    non-streaming exact OpenAI shapes (chunk `chat.completion.chunk`, `[DONE]`,
    non-stream `chat.completion` with `usage` when upstream provides tokens,
    `-1` otherwise), tool-call chunks, error envelope on failure.
    Extra non-breaking header `X-Freellm-Routed-Via: groq/llama-3.3-70b-versatile`.
  - `GET /v1/models` (aliases + live catalogs, `owned_by` per provider).
  - `GET /v1/health` + `/healthz` (per-provider availability — upgraded:
    include latency + last error, not just booleans).
  - `GET /v1/notes` (free-tier notes as JSON — machine-readable cheat sheet).
  - `GET /v1/metrics` — lightweight self-made Prometheus text format
    (requests_total, upstream_errors by provider/status, latency histograms,
    active streams). No client dep; format-compatible.
- **Middleware chain (plugin-shaped):** ordered, composable:
  logging (slog, structured, per-request id) → CORS → auth
  (`none | bearer` keys from config) → rate-limit (per-key, token bucket,
  pluggable) → usage accounting (in-memory + append-jsonl file, pluggable) →
  panic recover → handler. `internal/server/middleware.go` defines
  `type Middleware func(http.Handler) http.Handler`; defaults registered in one
  place; user can disable via config.
- Graceful shutdown (SIGINT/SIGTERM, drain in-flight streams), optional
  TLS (`--cert/--key`), `--once` mode (handle one request then exit — useful
  as a drop-in for harnesses), `max_body_bytes`, request-id propagation.
- Tests: httptest end-to-end (streaming bytes match OpenAI SDK expectations —
  include one recorded real SDK conversation replay), auth/ratelimit, drain on
  shutdown, metrics emission.

**Exit criteria:** a real `openai` python or JS client with
`base_url=http://host:8787/v1, model=auto` completes a streaming chat with
fallback across two providers; `curl /v1/health` shows per-provider detail;
middleware order asserted in tests.

---

## STEP 5 — Native + external provider plugins (the "anything" step)

**Goal:** more source *impls*, and the external plugin protocol, so new
sources never require core changes.

- `internal/providers/ollama`: native `/api/chat` (stream) + `/api/tags`,
  `Available()` = reachability. (Replaces python's ollama handling.)
- `internal/providers/g4fbridge`: talks to ANY gpt4free-class sidecar —
  upstream `g4f` python server, `g4f.dev` hosted, or your own FastAPI/Go
  wrapper — behind one OpenAI-ish or g4f-native adapter (config picks which).
  Docs: how to stand up each sidecar.
- `internal/providers/pluginproc` + `docs/plugin-protocol.md`:
  stdio JSON-RPC protocol (initialize/models/available/stream-notification
  protocol), client with handshake timeout, stream demux, crash → cooldown of
  whole provider, auto-restart with backoff, log capture (plugin stderr → slog).
- `plugins/go-example/` (a ~150-line "echo/rotating" provider using the small
  `internal/pluginhost` package — expose it as `pkg/pluginhost` so others can
  build Go plugins with one import) and `plugins/python-example/` (python
  g4f sidecar wrapped in the same protocol) — both pass `probe`.
- Config: `plugin:` provider entries gain `restart_policy`, `handshake_timeout`,
  `env`. Validate step-3's `providers add --kind plugin` path here.
- Tests: protocol encode/decode fuzz-ish tests, plugin crash/restart, a fake
  python subprocess in CI (guard: skip if no python3), stream ordering.

**Exit criteria:** `probe --provider go-example` and `--provider python-example`
pass WITHOUT touching core; a brand-new source added purely via config +
protocol in < 5 min (write this walkthrough into `docs/adding-a-source.md`).

---

## STEP 6 — Catalog, discovery & MORE.md ingestion

**Goal:** model knowledge stops being static; the router *discovers* sources
and their free tiers, keeping the alias chains honest.

- `internal/core/catalog`: merge `CatalogSource`s (embedded defaults →
  per-provider live `/models` → optional remote sources) with de-dup,
  tier-note precedence (local file > remote > builtin), and a `catalog.json`
  on-disk cache with TTL (`catalog.refresh_s` config).
- Remote catalog adapters (each its own small `CatalogSource` impl — pluggable):
  - freellmapi signed catalog (`https://freellmapi.co/models`),
  - `mnfst/awesome-free-llm-apis` daily JSON (raw on jsDelivr),
  - `ClawLabsAI/free-ai-models` daily status.
  Each adapter = ~60 lines, config-enabled, tolerant of absence.
- New CLI wiring: `models` uses merged catalog (tier notes shown); `notes`
  reads merged notes; new command `discover --source X [--apply]`:
  fetch a source's provider list, diff against config, print (or with
  `--apply`, append) ready-to-paste `providers:` entries and suggested
  `routes` additions. **This is the "MORE.md tomorrow" workflow institutionalized.**
- `doctor`: config validity + per-provider reachability + key sanity +
  plugin handshake status, one clean screen (the ops entry point).
- Tests: adapter parsing against recorded fixtures (commit JSON fixtures for
  each remote source), cache TTL, `discover --apply` idempotency (run twice).

**Exit criteria:** kill-network scenario: everything works offline from
embedded + cache; online: `discover --apply --source awesome-llm-apis` adds the
delta and `probe` routes through a newly added row end-to-end.

---

## STEP 7 — Observability, resilience, security polish

**Goal:** production-grade behavior on top of a working product.

- Structured usage journal (per-request: t, route, via, tokens, latency,
  status) as append-only JSONL with rotation + `stats [--date]` CLI command
  (top providers, error rates by status, per-key consumption).
- Backoff policy object (per status class), jittered, config-driven;
  provider `weight:` honored in chain tiebreaks.
- Auth hardening: bearer keys with constant-time compare (server side),
  optional per-route ACL (`routes[].allow: [keyname]`), TLS cert reload.
- Concurrency: shared `*http.Client` transport (pool-sized), per-provider
  in-flight cap (config `max_parallel`), stream cancel on client disconnect
  (verify upstream conns actually close).
- `fuzz` targets: SSE parser, JSON-RPC plugin stream demux, config expansion.
- Security: body size limits, JSON depth limits, SSRF guard for sidecar
  base_urls (no localhost unless `allow_insecure_upstream: true`), no secrets
  in logs/metrics (assert in tests).
- Tests incl. a 200-stream concurrency soak against fakes.

**Exit criteria:** 8h loop of `chat` hammering against fakes: no goroutine
leaks (`-test.timeout` + `runtime.NumGoroutine` check), no leaked conns,
zero secret leakage in logs/metrics/journal.

---

## STEP 8 — Distribution, embedding, docs (ship it)

**Goal:** one artifact, every consumer shape, zero-friction onboarding.

- `Makefile release`: cross-compile (linux/amd64+arm64, darwin, windows),
  static, `-ldflags` version info; `docker/` (distroless image) + compose
  example with sidecars; `plugins/python-example` baked compose variant
  (g4f server + router) as the "zero-key" stack.
- `pkg/router` promoted to a stable, documented public API with godoc
  examples + `example/embed.go` (embed the router in your own Go service
  in < 30 lines) — "integrable with anything in Go".
- Non-Go clients: OpenAI-compat already covered; add
  `docs/integrations.md` with copy-paste blocks: OpenAI python/JS SDK,
  Claude Code / OpenCode / pi / Open WebUI / LibreChat / any LiteLLM front,
  plain curl (stream + sync), MCP server example pointing at `/v1`.
- Final `README.md` (replace the python one): features, 5-minute quickstart,
  config reference (generated from schema, `docs/config-reference.md`),
  `docs/adding-a-source.md`, `docs/plugin-protocol.md`, free-tier notes link
  to data files. Deprecate `freellm_router.py` (mark as legacy reference,
  keep in repo with a banner comment) and `MODELS.md` (merged into catalog).
- CHANGELOG + tag `v1.0.0-rc1`. Test matrix runs on Linux/macOS in CI
  (`.github/workflows/ci.yml`) including the python-plugin step (with
  python3 setup action).

**Exit criteria:** fresh machine: one command (curl-installer or
`make install`) → `szmelc-router serve` → an OpenAI SDK chat succeeds with
`model=free` (keyless stack, docker compose) — the headline demo.

---

## Per-step working protocol (for whoever runs a step)

1. Read this file top→bottom; read the 1-2 files the step touches.
2. Only step N's scope; if you need to change an earlier contract, note it
   in this file (append under the affected section, dated) before coding.
3. Leave `go build ./... / go vet ./... / go test ./...` green.
4. Append a dated one-paragraph "state of the repo" note to `docs/PROGRESS.md`
   (create if missing) so the next context picks up cold with zero loss.
5. Do not delete the python files until step 8.

## Reference material in repo

- `freellm_router.py` — behavioral spec for parity (fallback/cooldown/keys/SSE shapes).
- `FREE-MODELS.md`, `MODELS.md`, `RESEARCH.md`, `MORE.md` — source data;
  MORE.md §4 + §8 are the primary "add these providers" list; §5 = local engines;
  §6 = remote catalog feeds (step 6).
- `.env.example` — key env names to preserve for compatibility.
