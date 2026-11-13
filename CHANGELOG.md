# Changelog

All notable changes to freellm-router. Format loosely follows Keep a
Changelog.

## [Unreleased]

### Added
- **Event ledger.** One JSONL log of every request (failures included),
  every upstream attempt (status, latency, tokens, the provider's
  `x-ratelimit-*` snapshot, cooldown applied), every task decision,
  session and control change. It is written by the server (rotated) and by
  every CLI process. Old usage journals still read. Config: `ledger:`
  (`file`, `content: off|preview|full`, rotation, `disabled`). **Note:**
  the default `content: preview` stores the first 200 characters of each
  prompt and answer on disk, which the old journal never did. Set
  `ledger.content: off` to keep them out.
- **Usage & limits**: `freellm-router usage` (`limits`, `quota`;
  `--models --all --watch --json`) and `GET /v1/usage`. Shows rolling
  1m/1h/24h/today windows per provider and model, measured against limits
  you declare (`limits:` per provider, never guessed), plus the provider's
  own last quota snapshot, live cooldowns and a status for each provider.
- **History**: `freellm-router history` (`log`). Filters: `--since/--until`,
  `--kind`, `--session`, `--provider`, `--errors`, `--grep`, `-n`. Also
  `--follow`, `history sessions`, `history show <id>`, and
  `history export --format jsonl|json|csv`. Over HTTP: `GET /v1/history`
  (`?follow=1` streams Server-Sent Events) and `GET /v1/sessions`.
- **Control plane** (`control:` config, `freellm-router ctl`,
  `GET/PUT/PATCH /v1/control`, `DELETE /v1/cooldowns`):
  - **∞ auto** profile: fallback, then wait out cooldowns and retry until
    answered, bounded by request cancellation and `max_wait_s`. It escalates
    once to `auto` and enforces budgets.
  - **⛭ control** profile: no fallback, no silent retries, budgets enforced.
  - Other controls: pause/resume, disable providers, allow-list, max
    attempts, escalation route.
  - **Pre-call hooks** can veto or rewrite a call. They fail closed: a hook
    that cannot run vetoes the call. **Post-call hooks** run for auditing.
    Hooks come from the config file only; the API refuses to set them.
  - Without auth, the mutating endpoints and the history/sessions
    endpoints only answer loopback clients.
  - Per-request overrides: `X-Freellm-Profile`, `-Fallback`, `-Forever`,
    `-Max-Attempts`, `-Tags`.
- **Integrations**:
  - `exec` (`ask`, `run`): a one-shot call for scripts. It reads context
    from stdin and `--file`, takes `--profile`, `--forever` and
    `--deadline`, prints `--json`, and has stable exit codes (0 ok, 1 error,
    2 all failed, 3 vetoed). `--no-stdin` / `FREELLM_NO_STDIN=1` for
    harnesses that keep stdin open.
  - `env`: prints `OPENAI_BASE_URL` and the related variables for sh, fish,
    powershell, dotenv or json.
  - `wrap -- <cmd>`: runs any harness as one tracked session. It uses the
    running server or starts a private one, and passes through the exit
    code.
  - **Sessions** can be set with the base URL `/s/<id>/v1`,
    `X-Freellm-Session`, `metadata.session`, `--session` or
    `$FREELLM_SESSION`.
- **Interactive TUI** (`freellm-router tui`, aliases `ui`, `top`), built on
  bubbletea. Tabs: dashboard (sparkline, top providers, failures), usage,
  history (search/filter/follow/detail), sessions, streaming chat, and a
  control panel that can save its settings to the config. It works live
  against a server or locally from the ledger.
- `GET /v1/ping`. Response headers `X-Freellm-Request-Id` and
  `X-Freellm-Session`. Global `-m` shorthand for `--model` and `--url` flag.
- `pkg/router`: `SetControl`, `SetLimiter`, `SetOnAttempt`, `SetOnTask`,
  `WithOverride`, `Resolve`, `Cooldowns`, `ClearCooldowns`, `Unavailable`,
  `KeyCount`. `internal/core`: per-attempt observer, `StreamOut.Meta`
  (status and rate-limit headers), per-call options (gate, maximum entries
  and attempts), typed `ChainError`.

### Fixed
- The server wrote two usage records per successful chat and none for
  failed ones. It now writes exactly one record per request, errors
  included.
- `serve` wrote no usage journal unless `server.usage_file` was set, while
  `stats` read the default path. Both now use the same default ledger.
- `stats` panicked on a `via` without `/`, and on keyed records without
  token counts.
- The in-flight request counter was not atomic.
- **Task-router modes.** **[Rozkład Jazdy]** (`rozklad-jazdy`) is the new
  default everywhere. It goes all in: the strongest available model first,
  then the biggest context, with cost, speed and rule tier caps ignored, and
  every eligible model in the chain. **Jako Tako** (`jako-tako`) is the
  previous "just enough" behavior. You can switch with:
  - `agentic.mode` in the config, or `freellm-router mode <id>`, which writes
    only that key;
  - `FREELLM_TASK_MODE`;
  - `GET`/`PUT /v1/route/mode` on a running server;
  - per request: `agent:<mode>`, `X-Freellm-Task-Mode`, `metadata.task_mode`,
    or `chat` / `route --mode`.

  `/v1/models` lists `agent:<mode>` variants. Decisions and headers carry the
  mode.
- **Agentic task router** (`internal/agentic`, `defaults/agentic.yaml`):
  `model: "agent"` / `"task"` classifies the last user message (role +
  complexity 1-5, EN+PL cues), computes the "just enough" tier, and routes
  the cheapest sufficient model first → stronger (escalation) → weaker
  (best effort). Hard filters: tool calling, context size, local-only,
  availability (keys, daemon, *pulled* Ollama models, cooldown). Non-stream
  quality gate re-picks one tier up on empty/filtered answers. Overrides:
  `agent:code:4`, `X-Freellm-Task`, `metadata.task`. Every pick carries a
  reason and every candidate's "why it lost".
- `GET /v1/route` (catalog + availability), `POST /v1/route` (decision
  preview, no upstream call), `X-Freellm-Task*` response headers, task
  aliases in `/v1/models`.
- CLI `route` (preview, `--all`, `--catalog`, `--role/--complexity/--tools/--local`);
  `chat --via` also prints the task reason.
- `pkg/router`: `IsTaskAlias`, `PlanTask`, `TaskSpec`, `TaskAvailability`,
  `TaskAliases`, `RouteOpts.Task`, `Sinks.Task`, `Config.Agentic`.
- `agentic:` config section, validated by `config validate`.
- `internal/server` tests (httptest).

### Fixed
- Gemini: `gemini-2.5-pro` now returns 404 for new users, and Pro's free-tier
  quota is 0. The `smart` route and the catalog moved to the `-latest`
  aliases (`gemini-flash-latest`, `gemini-flash-lite-latest`). The task
  router's `gemini-pro` entry (`gemini-pro-latest`) is off by default, and
  `gemini-flash-latest` is the tier-4 entry.
- `chat --no-stream --via` printed an empty route (a shadowed variable).
  `chat --via` with `agent` now reports the actual decision, and the
  fallback that served it, instead of re-planning after the fact.
- Non-streaming `/v1/chat/completions` wrote SSE chunks (and a
  `text/event-stream` content type) before the JSON body, so non-stream
  OpenAI clients got an unparseable response. Text and tool deltas are now
  only buffered when `stream` is false.
- `cmd/freellm-router/main.go` was never committed: the bare
  `freellm-router` pattern in `.gitignore` matched the directory, so
  `make build`, CI and the Docker image could not build. Pattern anchored.
- Every chat request fetched `/models` from **every** provider (43
  sequential HTTP calls) to build a map the default policy never reads.
  It is now only built for policies that implement `KnownModelsPolicy`.
- `go.mod`: cobra marked as a direct dependency.
- Plugin tests ran a committed Linux `bin/go-example` binary (`exec format
  error` on macOS CI); they now build the plugin for the host platform.
- CORS: `X-Freellm-Task` allowed as a request header; routing headers
  exposed to browser clients.

## [v1.0.0-rc1] — 2026-11-02

### Added
- **Step 1** — Go rewrite: module + layout, 3-layer config loader
  (embedded defaults → file → env/.env, `${VAR:-default}` expansion),
  full domain model (provider, catalog, requests, errors, cooldown,
  keys), 43 known sources + 35 route aliases moved out of the python
  file into data (`defaults/*.yaml`).
- **Step 2** — OpenAI-compatible provider (SSE, tools pass-through,
  multiple auth styles) + fallback router (chain resolution, cooldown
  short/long, key rotation, retry budget, "via" label) and the public
  `pkg/router` API.
- **Step 3** — Cobra CLI: `chat`, `models`, `probe`, `providers
  list|add|remove|test`, `notes`, `config path|show|validate|init`,
  global `--config/--provider/--model/--timeout/--json`.
- **Step 4** — OpenAI-compatible HTTP server: `/v1/chat/completions`
  (streaming + sync, exact OpenAI shapes), `/v1/models`, `/v1/health`,
  `/v1/notes`, `/v1/metrics`; middleware chain (logging, CORS, bearer
  auth, per-key rate limit, usage journal, panic recover); TLS,
  graceful shutdown, `--once` mode.
- **Step 5** — Native adapters for Ollama and g4f-class sidecars; the
  **external plugin protocol** (stdio JSON-RPC 2.0) with crash →
  cooldown + restart; `pkg/pluginhost` for Go plugins; reference Go
  and Python plugins.
- **Step 6** — Catalog: merged sources (live provider `/models`,
  embedded free-tier notes, remote feeds — freellmapi,
  awesome-llm-apis, free-ai-models), on-disk cache with TTL
  (the `discover`/`doctor` CLIs planned for this step landed as the
  catalog package + `models`/`notes` consumption; dedicated commands
  deferred).
- **Step 7** — Usage JSONL journal with rotation + `stats` CLI;
  config-driven jittered backoff per status class; constant-time
  bearer compare; per-provider in-flight cap; shared HTTP transport
  pool; secret redaction in logs/journal; SSRF guard for loopback
  sidecars (`allow_insecure_upstream: true`); fuzzable SSE parser;
  200-stream concurrency soak against fakes.
- **Step 8** (this release):
  - static cross-compiled binaries (linux/darwin × amd64/arm64,
    windows/amd64) via `make release` + SHA256SUMS;
  - `docker/` — distroless image, standard compose stack (router +
    g4f + ollama), and a **zero-key** compose (g4f sidecar +
    python-example plugin + router);
  - `pkg/router` promoted to a **stable public API** with godoc +
    `example/embed.go` (< 30 lines);
  - docs: `docs/integrations.md` (OpenAI py/JS, curl, Claude Code,
    Open WebUI, LibreChat, LiteLLM, MCP, Go embed),
    `docs/config-reference.md` (generated from schema),
    `docs/plugin-protocol.md`, `docs/adding-a-source.md`;
  - `freellm_router.py` and `MODELS.md` deprecated (kept in-repo with
    banners); fresh README;
  - GitHub Actions CI (Linux + macOS, python-plugin step).

### Fixed
- Journal rotation could lose a line that didn't fit the fresh file
  (append-after-rotate now guarantees one-line space).
- `config init` now works with a not-yet-existing `$FREELLM_CONFIG`
  and honors that variable (previously always wrote the XDG path).
- Default sidecar entries (llmcycle, freellmapi) opt in to the
  loopback SSRF guard; test fixtures that target loopback fakes do
  the same.

[unreleased]: #
[v1.0.0-rc1]: #v100-rc1
