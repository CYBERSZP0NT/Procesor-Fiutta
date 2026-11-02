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
