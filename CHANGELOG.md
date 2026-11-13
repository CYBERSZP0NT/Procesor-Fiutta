# Changelog

All notable changes to freellm-router. Format loosely follows Keep a
Changelog.

## [Unreleased]

### Added
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
- `cmd/freellm-router/main.go` was never committed: the bare
  `freellm-router` pattern in `.gitignore` matched the directory, so
  `make build`, CI and the Docker image could not build. Pattern anchored.
- Every chat request fetched `/models` from **every** provider (43
  sequential HTTP calls) to build a map the default policy never reads.
  It is now only built for policies that implement `KnownModelsPolicy`.
- `go.mod`: cobra marked as a direct dependency.

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
