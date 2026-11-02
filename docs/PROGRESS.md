# docs/PROGRESS.md — freellm-router Go rewrite

Running log. Each entry is one step's outcome + handover for the next.
Newest at the bottom.

---

### STEP 1 — 2025-07-14 — ✅ ok

**Scope:** Skeleton, config system, domain types (foundation). No HTTP yet.

**What was built:**

- **`go.mod`** — module `github.com/silverx/freellm-router`, Go 1.27, single
  third-party dep (`gopkg.in/yaml.v3`).
- **Directory layout** created per AGENTS.md specification:
  `cmd/freellm-router/`, `internal/core/`, `internal/config/`,
  `defaults/`, `docs/`, `plugins/`, `pkg/router/` (empty stubs ready for
  later steps).
- **`defaults/providers.yaml`** — 43 provider entries moved from the Python
  file + MORE.md §4/§8. Covers all keyless (pollinations, opencode_zen,
  llm7, kilo, aionlabs, hf_router, agnes, ovh, puter, aihorde), key-based
  free tiers (google, groq, cerebras, sambanova, mistral, zai, openrouter,
  nvidia, github_models, cohere, cloudflare, modelscope, alibaba, siliconflow,
  xai, deepseek, chutes, glhf, ai21, nebius, nscale, together, fireworks,
  replicate, deepinfra, novita, hyperbolic, aimlapi), local (ollama),
  sidecars (llmcycle, freellmapi), g4f.
  No provider name appears as a Go constant in `internal/core`.
- **`defaults/routes.yaml`** — 35 route entries: `auto`, `smart`, `fast`,
  `code`, `local`, `free`, `keyless`, `credits` + provider pass-through
  aliases for every provider in the list.
- **`internal/core/types.go`** — complete domain contract: `Provider`,
  `Model`, `Kind` (keyless|keyed|local|scraper|sidecar|plugin), `TierInfo`,
  `ChatRequest`, `Message`, `StreamOut`, `StreamDone`, `RoutePolicy`,
  `SemanticRouteHook`, `KeySource`, `CooldownStore`, `CatalogSource`,
  `UpstreamErr` (typed, `errors.Is`-compatible, `Unwrap` chain),
  `ModelSelector`, `Availability`, `ToolDef`/`ToolCall`/`FunctionDef`.
  All core functions take `context.Context` first.
- **`internal/core/cooldown.go`** — `InMemCooldown` (RWMutex-guarded,
  `Set`/`Has`/`Remaining`/`Forget`/`ForgetAll`/`Labels`). Race-detector safe.
- **`internal/core/keys.go`** — `EnvKeySource` (pipe-separated multi-env-var,
  comma-separated multi-key, case-insensitive provider names) +
  `FileKeySource` (`#comment`, `k=v`, `k: v` line format). Both satisfy
  `KeySource` interface. `RedactedKeys` available for error messages.
- **`internal/config/config.go`** — full schema structs (`Config`, `Provider`,
  `ServerConfig`, `AuthConfig`, `PolicyConfig`, `CatalogConfig`,
  `LoggingConfig`), 3-layer merge (embedded defaults → user file → env
  overlay), `${VAR:-default}` expansion on all string fields, `Validate()`
  catching: unknown impl, unknown kind, missing base_url, duplicate provider
  names, unknown provider refs in routes, bad port, bad auth mode, bad log
  level, bad `max_chain_attempts`.
- **`internal/config/expand.go`** — `${VAR}` / `${VAR:-default}` regex
  expansion.
- **`internal/config/config_test.go`** — 22 test cases: embedded defaults
  loading, user override, user add-provider, env overlay, expansion (all
  patterns), 3-layer merge ordering, validation failures (unknown impl,
  bad port, unknown kind, duplicate provider, unknown provider in route),
  key-env map, route name sorting, case-insensitive lookup.
- **`internal/core/cooldown_test.go`** — TTL expiry, remaining time,
  ForgetAll, multi-entry isolation, sorted labels, UpstreamErr formatting
  and `errors.Is` chain, Kind constants, ChatRequest/ToolDef structure.
- **`internal/core/keys_test.go`** — single key, multi-key (comma),
  pipe-separated env vars, unset/empty, case-insensitivity, redaction
  (long and short keys), FileKeySource (basic, multi, bad path),
  interface satisfaction, Availability/StreamDone structs.
- **`cmd/freellm-router/main.go`** — thin entry: loads config, validates,
  prints summary; `--version` flag works. (Cobra CLI arrives in STEP 3.)
- **`Makefile`** — `build`, `test`, `test-verbose`, `lint`, `fmt`, `clean`,
  `install`, `release` (cross-compile to linux/amd64+arm64, darwin/amd64+arm64,
  windows/amd64) with `-ldflags` version injection.
- **`.gitignore`** — Go artifacts (`freellm-router`, `dist/`, `*.exe`).

**Files touched:** all listed above (new files; `go.mod`, `go.sum` created).

**Test results:**
```
go build  ./...   → OK
go vet    ./...   → OK
go test   -race   → all pass (55 test cases, 0 fail)
go run    ./cmd/freellm-router
  → 43 providers, 35 routes, config validated OK
```

**Exit criteria:**
| Criterion | Status |
|---|---|
| `go build ./... && go test ./...` green | ✅ met |
| New provider addable by YAML only | ✅ met (add one entry to defaults/providers.yaml or user config) |
| No provider name as a constant in `internal/core` | ✅ met (only Kind & Role constants exist there) |

**Deviation note:** None. The contract as written in AGENTS.md is followed.

**Next step (STEP 2) — verify first:**
1. `internal/providers/openailike` should use `config.Provider.BaseURL`,
   `config.Provider.KeyEnv`, `config.Provider.Header` (x-goog-api-key) to
   set auth headers. Confirm the `header` field is respected.
2. `core.RoutePolicy.Resolve` needs to use `config.RouteChain(name)` to
   look up the chain, then `config.ProviderByName` to fill `Model` structs.
3. The `UpstreamErr` type and `errors.Is` chain are already in place —
   the openailike provider should wrap all HTTP errors into it.

3-line handover for STEP 2:
> The domain types (`Provider`, `ChatRequest`, `StreamOut`, `StreamDone`,
> `UpstreamErr`, `CooldownStore`, `KeySource`) and the config system are
> complete and tested. A provider implementation (openailike) needs to
> implement the `core.Provider` interface — `Stream` is the main method,
> it should call `upstream /chat/completions` with SSE streaming and return
> `*StreamDone`. Keys come from `core.KeySource`; cooldowns from 
> `core.InMemCooldown`. No new types are needed in `internal/core` for 
> STEP 2 unless a new `Kind` or `Tier` field is required.

---

### STEP 2 — 2026-11-02 — ✅ ok

**Scope:** OpenAI-like provider + router engine. Heartbeat works end-to-end
in-process via `pkg/router`.

**What was built:**

- **`internal/providers/openailike/provider.go`** — full `core.Provider`
  implementation: SSE streaming (chunked `data:`/`[DONE]`), non-streaming JSON,
  auth (Bearer, custom header like `x-goog-api-key`, keyless), `tools`/
  `tool_choice` pass-through, `max_tokens`/`temperature`/`top_p`, per-request
  timeout, `/models` fetch, `Available()` check, error mapping →
  `*core.UpstreamErr`. Key rotation (round-robin) per provider with
  `currentKey` (read-only, for catalog) vs `nextKey` (advancing, for usage).
- **`internal/providers/openailike/sse.go`** — minimal SSE reader
  (`data:`/blank-line protocol). 64 KB bufio buffer.
- **`internal/providers/openailike/sse_types.go`** — SSE JSON chunk types.
- **`internal/core/router.go`** — Router engine: `Route()` resolves chain
  via `RoutePolicy` → dedup → filter (availability + cooldown) → walk in
  order with retry policy → on 4xx (non-429/408) long cooldown (1h),
  on 429/5xx short cooldown (30s) → returns `(fullText, via)`. Configurable
  `max_chain_attempts`, `retry_same` (retry-once on 429/5xx).
- **`internal/core/default_policy.go`** — `DefaultRoutePolicy`: resolves
  route aliases, `provider/model` (+ auto safety net), provider-by-name,
  bare model ID → auto fallback. All data-driven from config routes.
- **`internal/core/types.go`** — added `Provider string` field to `Model`
  (minimal contract change: enables provider-bound route entries).
- **`internal/core/testdata/fakes.go`** — reusable `FakeUpstream` httptest
  mock (SSE + JSON, status queue, call counters).
- **`pkg/router/router.go`** — public Go API: `router.New(cfg)` → `r.Route(ctx,
  selector, messages, model, sink)`. Thin wrapper: instantiates providers,
  builds policy + router, exposes minimal type surface.

**Files touched:** (all new unless noted)
| File | Action |
|------|--------|
| `internal/core/types.go` | modified (added `Provider` field to `Model`) |
| `internal/core/router.go` | new |
| `internal/core/default_policy.go` | new |
| `internal/core/router_test.go` | new (14 tests) |
| `internal/core/testdata/fakes.go` | new |
| `internal/providers/openailike/provider.go` | new |
| `internal/providers/openailike/sse.go` | new |
| `internal/providers/openailike/sse_types.go` | new |
| `internal/providers/openailike/provider_test.go` | new (20 tests) |
| `pkg/router/router.go` | new |
| `pkg/router/router_test.go` | new (7 tests) |

**Test results:**
```
go build  ./...   → OK
go vet    ./...   → OK
go test   -race   → all pass (84 test cases, 0 fail, 0 data races)
```

**Exit criteria:**
| Criterion | Status |
|---|---|
| `go test ./internal/...` green | ✅ met (all pass, race-clean) |
| A `cmd`-free Go test can `pkg/router.New(cfg).Route(...)` and stream | ✅ met (`TestPkgRouterBasic`, `TestPkgRouterStreaming`, `TestPkgRouterFallback`) |
| Adding a fake provider = config + mock, no core edits | ✅ met (all router tests are pure config + httptest, zero core changes) |

**Deviation note:** Added `Provider string` field to `core.Model` (one new
field). The AGENTS.md contract reference `[]provider.Model` implies this
association; the existing `Model` type from STEP 1 lacked it. Minimal change.

**Next step (STEP 3) — verify first:**
1. CLI commands should instantiate providers via `pkg/router.New()` (or the
   internal `core.NewRouter`) and call `Route()` — the public API is already
   there and tested.
2. `--json` output for `models` can call `Provider.Models(ctx)` which is
   implemented in openailike (GET /models).
3. The `chat` command's streaming output should use `sink` callback → buffer
   → stdout. The TTY detection + pterm wrapping is new code for step 3.

3-line handover for STEP 3:
> The heartbeat (resolve chain → stream → fallback → cooldown → retry) is
> fully working and tested. `pkg/router.New(cfg).Route(ctx, selector,
> messages, model, sink)` is the entry point the CLI and server will use.
> Adding a provider = one `ProviderConfig` entry + `routes` map update.
> The Cobra CLI should build on `pkg/router` without touching core internals.

---

### STEP 3 — 2026-11-02 — ✅ ok

**Scope:** CLI (cobra) — the daily-driver surface, parity + polish over the
python CLI.

**What was built:**

- **`internal/cli/root.go`** — Global flags (`--config`, `--provider`,
  `--model`, `--timeout`, `--json`), `Execute()` entry point,
  `SetVersionInfo()` for ldflags wiring.
- **`internal/cli/chat.go`** — `chat "prompt"` subcommand: streaming to
  stdout, SIGINT handling, `--system`, `--no-stream`, `--max-tokens`,
  `--temperature`, `--via`, `--json` output.
- **`internal/cli/models.go`** — `models` subcommand: route aliases +
  provider table, `--provider` filter, `--kind` filter, `--tier`,
  `--json` (structured JSON with routes + providers).
- **`internal/cli/probe.go`** — `probe` subcommand: concurrent pings of
  chain members, `--concurrency`, `--pro-timeout`, `--provider` filter,
  ok/fail status + latency + preview in table or JSON.
- **`internal/cli/providers.go`** — `providers` subcommand tree:
  `list` (table + JSON), `add` (writes to config file, supports
  name/base_url/key_env/kind/impl/weight/notes), `remove`, `test`
  (single-provider connectivity check).
- **`internal/cli/notes.go`** — `notes` subcommand: human free-tier cheat
  sheet from provider `notes` field, filterable by `--provider`.
- **`internal/cli/configcmd.go`** — `config` subcommand tree:
  `path`, `show` (full YAML dump), `validate`, `init` (writes default
  config to `~/.config/freellm/config.yaml`).
- **`internal/cli/serve.go`** — `serve` stub: prints "added in step 4"
  with endpoint list; flags registered for continuity.
- **`internal/cli/helpers.go`** — `buildRouter()` bridge that converts
  `config.Config` → `router.Config` and instantiates the `pkg/router` handle.
- **`cmd/freellm-router/main.go`** — Rewritten as thin entry (3 lines):
  calls `cli.SetVersionInfo()` + `cli.Execute(args)`.
- **`internal/cli/cli_test.go`** — 20 test cases covering:
  chat against httptest fake (stream + non-stream + `--via` + `--system`),
  models JSON structure/shapes (routes present, provider filter, kind
  filter, tier notes), config validate/path/show, serve stub output,
  providers list/add, notes cheat-sheet, probe JSON/filter,
  chat error handling (connection refused), providers test, `--version`.
- **`go.mod`** — Added `github.com/spf13/cobra` dependency (v1.10.2).

**Files touched:**
| File | Action |
|------|--------|
| `internal/cli/root.go` | new |
| `internal/cli/chat.go` | new |
| `internal/cli/models.go` | new |
| `internal/cli/probe.go` | new |
| `internal/cli/providers.go` | new |
| `internal/cli/notes.go` | new |
| `internal/cli/configcmd.go` | new |
| `internal/cli/serve.go` | new |
| `internal/cli/helpers.go` | new |
| `internal/cli/cli_test.go` | new (20 tests) |
| `cmd/freellm-router/main.go` | rewritten (thin wrapper) |
| `go.mod` / `go.sum` | added cobra dep |

**Test results:**
```
go build  ./...   → OK
go vet    ./...   → OK
go test   -race   → all pass (104 test cases across all packages, 0 fail, 0 data races)

freellm-router chat "hi" --model free       → works against httptest fake
freellm-router models --json --tier        → structured JSON output
freellm-router probe --provider fake       → ok status + latency
freellm-router providers add --name …      → appends to config
freellm-router providers test fake         → OK response
freellm-router notes                       → shows cheat sheet
freellm-router config validate             → "Config OK (43 providers, 35 routes)"
freellm-router serve                       → "added in step 4" stub
freellm-router --version                   → version commit date
```

**Exit criteria:**
| Criterion | Status |
|---|---|
| `freellm-router chat "hi" --model free` works against fakes | ✅ met |
| All `--help` texts complete | ✅ met (all subcommands have Long text + Args) |
| `make install` gives a working binary | ✅ met |
| `providers add` with a MORE.md entry routes through it | ✅ met (tested with deepseek) |

**Deviation note:** None. The contract is followed exactly.

**Next step (STEP 4) — verify first:**
1. `internal/server` should call `buildRouter()` (or directly
   `pkg/router.New()`) and expose `POST /v1/chat/completions`,
   `GET /v1/models`, etc. The `serve.go` flags are already registered
   so the command name/flags won't drift.
2. The middleware chain should compose around the handlers defined in
   step 4. Use `internal/server/middleware.go` per AGENTS.md.
3. `providers test` already works end-to-end with `pkg/router` — the
   server can reuse the same `Router.RouteWithOptions` call path.

3-line handover for STEP 4:
> The Cobra CLI is complete and tested. `buildRouter(cfg, timeout)` in
> `internal/cli/helpers.go` is the reference for how to wire
> `config.Config` → `pkg/router.Router`. The `serve` command is
> registered and ready to accept the real implementation in step 4 —
> just replace the `Run` function with the actual server loop.

---

### STEP 8 — 2026-11-02 — ✅ ok

**Scope:** Distribution, embedding, docs (ship it). One artifact, every
consumer shape, zero-friction onboarding.

**What was built:**

- **`docker/`** — `Dockerfile` (multi-stage → `gcr.io/distroless/static:nonroot`,
  non-root, healthcheck, config at `/config`, journal under `/data`),
  `docker-compose.yml` (router + g4f sidecar + ollama), and
  `docker-compose.zerokey.yml` (the headline "zero-key" stack: g4f server +
  python-example plugin + router). Compose files + CI workflow YAML all
  parser-validated (no docker daemon on this machine to actually `docker
  build` — see deviations).
- **`example/embed.go`** — < 30-line `package main` showing the Go-embed
  API (`router.New` + `Route` with a streaming sink); verified to run
  end-to-end against the live keyless pollinations endpoint (`via=poll/openai
  finish="stop" text="Hello"`).
- **`pkg/router`** promoted to a **stable public API**: package doc now
  carries a SemVer stability guarantee + godoc example; stale "impls arrive
  in later steps" note removed (all 4 impls now supported: openailike,
  ollama, g4fbridge, plugin).
- **`docs/integrations.md`** — copy-paste blocks: OpenAI python/JS SDK,
  plain curl (stream + sync + other endpoints), Claude Code, OpenCode/pi,
  Open WebUI, LibreChat, LiteLLM front, MCP server (two bridging options),
  Go embed.
- **`docs/config-reference.md`** — full schema reference (providers, server,
  policies incl. backoff, catalog, logging, http, routes_acl), generated
  from `internal/config` structs + defaults; cross-checked against
  `config show` / a live `config init`.
- **`docs/plugin-protocol.md`** — the stdio JSON-RPC spec (framing, all
  methods, streaming notifications, error codes, router-side lifecycle:
  handshake, at-least-once stream re-send, stderr capture) + a ~40-line
  reference impl. Written from the actual `pluginproc`/`pluginhost` code and
  both reference plugins (handshake + stream verified live).
- **`docs/adding-a-source.md`** — the two supported onboarding paths
  (3-line OpenAI-compatible config / any-language plugin) with worked
  examples.
- **`README.md`** — rewritten for the Go product: 5-minute quickstart,
  zero-key docker stack, surface table (CLI / server / Go library / plugins /
  docker), config overview, docs index, status = v1.0.0-rc1.
- **`CHANGELOG.md`** — full step-by-step history + this release's fixes.
- **`.github/workflows/ci.yml`** — Linux+macOS matrix (go build/vet/test,
  `-race` on Linux), a smoke step (config validate + serve + `/v1/models` +
  `/v1/health`), and a plugins job that handshakes both the Go (built via
  `go build -C plugins/go-example`) and Python (setup-python 3.12) reference
  plugins over real stdio JSON-RPC. All three YAML files parser-validated.
- **`Makefile`** — `release` now also emits `dist/SHA256SUMS`
  (sha256sum, shasum fallback). Verified: 5 static binaries build.
- **Deprecations** — `freellm_router.py` carries a LEGACY banner (kept,
  per step-8 rule "do not delete the python files until step 8"; we're at
  8 but keeping + marking, which the plan's wording prefers); `MODELS.md`
  carries a DEPRECATED banner pointing at `defaults/providers.yaml` +
  `models --tier` / `/v1/notes` / `/v1/models`.
- **`gofmt`** — normalized the whole repo (pre-existing drift vs. Go 1.27
  gofmt rules).
- **Tag + commit** — `v1.0.0-rc1` (see commit below).

**Earlier-contract fixes (minimal, required to get the step green):**

1. **Journal data-loss on rotation** (`internal/journal`): a line larger
   than the post-rotation space was silently dropped. `AppendLine` now never
   rotates an already-empty active file, so a fresh file always holds at
   least one line. `TestConcurrentAppend` was also fixed: it asked for
   *zero* retention loss with a capacity that mathematically can't hold the
   written volume (rotation semantics are covered by the other two tests);
   it now uses a capacity that fits 50 lines while still exercising several
   concurrent rotations.
2. **SSRF-guard vs. loopback fixtures**: `defaults/providers.yaml` entries
   `llmcycle`/`freellmapi` (local sidecars by design) and the
   loopback-pointing test fixtures (`internal/cli/cli_test.go` `fake`,
   `internal/config/config_test.go` `custom_provider`/`mysidecar`) now set
   `allow_insecure_upstream: true` — the guard (STEP 7) stayed intact.
3. **Alias → per-provider model ID was lost** (`internal/core/router.go`):
   when a caller selected a route alias (or a provider name) — the normal
   `model=free` flow — the provider was called with the *caller's* model
   string (alias or empty), not the chain entry's concrete model. Fixed at
   the single Stream call site: if `req.Model` is empty or equals the
   selector, the entry's `modelID` is used per provider; a concrete model id
   given by the caller is still honored as-is (bare-model rule 4). Verified
   live: `example/embed.go` now succeeds via `poll/openai`.
4. **`config init`** no longer fails when `$FREELLM_CONFIG` points at a
   not-yet-existing file (it now loads pure embedded defaults via new
   `config.Defaults()`), and it honors `$FREELLM_CONFIG` as the target path
   (previously always wrote the XDG path).
5. **`stats` CLI command was built (STEP 7) but never registered** in
   `rootCmd.AddCommand` — registered now; `stats` shows "no usage records"
   cleanly. (The `discover`/`doctor` CLIs advertised in the AGENTS.md STEP 6
   bullet were never implemented as commands — the catalog machinery they
   would use is present and consumed by `models`/`notes`; noted in
   CHANGELOG as deferred, not built here = kept out of STEP 8 scope.)

**Files touched (this step):**

| File | Action |
|---|---|
| `docker/Dockerfile`, `docker/docker-compose.yml`, `docker/docker-compose.zerokey.yml` | new |
| `example/embed.go` | new |
| `pkg/router/router.go` | modified (stable-API docs) |
| `docs/config-reference.md`, `docs/integrations.md`, `docs/plugin-protocol.md`, `docs/adding-a-source.md` | new (plugin-protocol + adding-a-source backfill STEP 5/6 doc debt) |
| `README.md` | rewritten |
| `CHANGELOG.md` | new |
| `.github/workflows/ci.yml` | new |
| `Makefile` | modified (release SHA256SUMS) |
| `freellm_router.py`, `MODELS.md`, `.gitignore` | modified (banners / bin/) |
| `internal/core/router.go` | modified (per-entry model fix + its test-suite now passes) |
| `internal/journal/journal.go`, `internal/journal/journal_test.go` | modified (rotation fix) |
| `internal/config/config.go`, `internal/config/config_test.go`, `internal/cli/cli_test.go`, `defaults/providers.yaml` | modified (SSRF-guard fixture opts-in) |
| `internal/cli/configcmd.go`, `internal/cli/root.go` | modified (config init + stats registration) |
| `docs/PROGRESS.md` | this entry |

**Test results:**

```
go build ./...   → OK
go vet   ./...   → OK
gofmt -l .       → (empty)
go test -race -count=1 ./... → all packages pass (9 test packages, 0 failures)

make release                 → 5 static binaries + dist/SHA256SUMS (linux amd64/arm64,
                               darwin amd64/arm64, windows amd64)

go run ./example             → via=poll/openai finish="stop" text="Hello"   (live)
go build -C plugins/go-example . + JSON-RPC initialize handshake           → OK
python3 plugins/python-example/plugin.py + initialize/models/stream        → OK
freellm-router config init (fresh $FREELLM_CONFIG) → writes + validates OK
freellm-router serve → /v1/health, /v1/models, /v1/notes all 200 (checked live)

docker build / compose up    → NOT RUN here (no docker daemon on this box);
                               Dockerfile + compose files are YAML/structure-validated
```

**Exit criteria (AGENTS.md STEP 8):**

| Criterion | Status |
|---|---|
| `Makefile release` cross-compile (linux/darwin/windows, static, ldflags) | ✅ met (verified locally; SHA256SUMS added) |
| `docker/` distroless image + compose example + zero-key (g4f+router) variant | ⚠️ files complete & validated; actual `docker build`/`up` deferred to a machine with a daemon (see handover note) |
| `pkg/router` stable public API + godoc + `< 30-line` `example/embed.go` | ✅ met (example runs live) |
| `docs/integrations.md` copy-paste blocks (OpenAI py/JS, Claude Code, Open WebUI, LibreChat, LiteLLM, curl, MCP) | ✅ met |
| Final README (replace the python one) + config reference from schema + adding-a-source + plugin-protocol + free-tier notes link to data | ✅ met |
| `freellm_router.py` + `MODELS.md` deprecated (banner, keep in repo) | ✅ met |
| `CHANGELOG` + tag `v1.0.0-rc1` | ✅ met (tag present) |
| Test matrix on Linux/macOS in CI incl. python-plugin step | ✅ met (workflow written + all non-docker steps reproduce locally; actual GHA run happens on push) |
| Headline demo: fresh machine → `serve` → OpenAI SDK chat `model=free` (keyless, docker compose) | ⚠️ code path verified via CLI/curl/live example against the keyless chain; the *docker-compose* flavor of the demo awaits a docker-enabled CI runner (the `plugins` job in CI exercises the g4f/plugin path without docker) |

**Deviation notes:**

- Steps 5–7 landed in the repo without a PROGRESS.md entry (the file ends at
  the STEP 3 handover) — the code is present and tested, so this entry
  treats them as prior state and only records what STEP 8 itself changed,
  with the earlier-contract fixes listed above.
- The `discover`/`doctor` CLIs from the STEP 6 bullet were not built by
  STEP 8 (out of scope); the underlying catalog machinery is present and
  consumed by `models`/`notes`. CHANGELOG records them as deferred.
- `docs/plugin-protocol.md` + `docs/adding-a-source.md` are written here
  (STEP 5/6 scope) because neither existed in the repo at step-8 start.

**Handover for STEP 9 / a maintainer:**

1. On a docker-enabled machine, run
   `docker compose -f docker/docker-compose.zerokey.yml up --build`, then hit
   `http://localhost:8787/v1/chat/completions` with `model=free` and confirm
   the g4f+plugin fallback works end-to-end (that is the one exit-criterion
   branch not yet exercised on real hardware).
2. If the router 404s on a specific provider model, the alias→per-entry
   model fix in `internal/core/router.go` is the most recent change to
   that area; `example/embed.go` is the fastest live repro path.
3. The `stats` CLI is now registered; if you add `discover`/`doctor` as
   dedicated commands later, the catalog machinery in `internal/cli/catalog.go`
   (`resolveCatalog`, `liveProviderModels`) is the intended building block.
