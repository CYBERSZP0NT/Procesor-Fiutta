# Configuration reference

Generated from the schema in `internal/config/config.go` + the embedded
defaults in `defaults/providers.yaml` / `defaults/routes.yaml`
(v1.0.0-rc1). If code and this page ever disagree, **the code wins**; the
canonical way to check is `szmelc-router config show` (full resolved YAML).

## Layers & resolution order

1. **Embedded defaults** — `defaults/providers.yaml` (43 sources) +
   `defaults/routes.yaml` (alias chains). Bundled in the binary; offline-safe.
2. **Config file** — `$FREELLM_CONFIG`, else `~/.config/szmelc/config.yaml`.
   `szmelc-router config init` writes a full commented default there.
3. **Environment overlay** — `FREELLM_HOST`, `FREELLM_PORT`,
   `FREELLM_LOG_LEVEL`, `FREELLM_LOG_FORMAT`, plus a loaded `.env` file.
4. **`${VAR}` / `${VAR:-default}` expansion** — applied to every string field
   after merging.

Validation runs after expansion: `szmelc-router config validate` is the fast
check (unknown `impl`, bad `kind`, missing `base_url` for HTTP impls,
duplicate names, routes referencing unknown providers, port range, etc.).

## Top-level shape

```yaml
routes:    # alias → ordered chain of "provider/model"
providers: # ordered list; every source is data, nothing hardcoded
server:
policies:
catalog:
logging:
http:
routes_acl:   # optional per-route bearer-key ACLs
```

## `routes` — `map[string][]string`

Route alias → ordered fallback chain. Entry form is always
`provider/model`. Built-in aliases: `auto`, `smart`, `fast`, `code`,
`local`, `free` (keyless), `keyless`, `credits`, plus a same-name pass-through
alias for every configured provider.

```yaml
routes:
  free: [g4f/gpt-4o-mini, pollinations/openai, ollama/llama3.2:3b]
```

## `providers` — `[]provider`

| Field | Type / values | Default | Notes |
|---|---|---|---|
| `name` | string (unique) | — | the label in chains, `via`, `providers …` commands |
| `impl` | `openailike` \| `ollama` \| `g4fbridge` \| `plugin` | — | transport plugin to dispatch to |
| `base_url` | URL string | — | required for HTTP impls; trailing `/v1` |
| `key_env` | env var name | `""` (keyless) | may be pipe-separated multi-var |
| `kind` | `keyless`\|`keyed`\|`local`\|`sidecar`\|`scraper`\|`plugin` | `keyless` | informational + SSRF-guard class |
| `weight` | int 0..10000 | 100 | tie-break when chains interleave same model |
| `header` | header name | `""` | non-standard auth header (e.g. `x-goog-api-key`) instead of Bearer |
| `notes` | string | — | free-tier notes; shown by `notes` / `/v1/notes` |
| `command` | `[]string` | — | `impl: plugin` only — spawn argv |
| `env` | `map[string]string` | — | `impl: plugin` only — plugin env (values may use `${VAR}`) |
| `allow_insecure_upstream` | bool | `false` | permit loopback `base_url` for non-local kinds (local dev / sidecars) |
| `max_parallel` | int 0..10000 | 0 (unlimited) | per-provider in-flight stream cap |
| `restart_policy` | `on-crash` \| `never` | `on-crash` | `impl: plugin` |
| `handshake_timeout` | int seconds | 10 | `impl: plugin` |
| `restart_max` | int | -1 (infinite) | `impl: plugin`; 0 = never retry after a crash |

Example (the "add a MORE.md source" path):

```yaml
providers:
  - name: novita
    impl: openailike
    base_url: https://api.novita.ai/v1
    key_env: NOVITA_API_KEY
    kind: keyed
    weight: 40
    notes: "free tier, ~1 req/s burst"
```

`keyless` + `impl: plugin`:

```yaml
  - name: myscraper
    impl: plugin
    kind: plugin
    command: ["python3", "/opt/myscraper/server.py"]
    env: {SCRAPER_KEY: "${SCRAPER_KEY:-}"}
    restart_policy: on-crash
    handshake_timeout: 10
```

## `server`

| Field | Type | Default | Notes |
|---|---|---|---|
| `host` | string | `0.0.0.0` | bind address; `allow_insecure_upstream` unrelated — this is the router's own bind |
| `port` | int 1..65535 | 8787 | |
| `auth.mode` | `none` \| `bearer` | `none` | |
| `auth.keys` | `[]string` | `[]` | bearer tokens to accept (constant-time compare) |
| `auth.names` | `map[string]string` | `{}` | optional human names → key (for per-route ACLs) |
| `once` | bool | `false` | serve exactly one request then exit (harness mode); also `serve --once` |
| `max_body_bytes` | int | 4 MiB | request body cap |
| `cert` / `key` | paths | — | enable TLS when both set (or `--cert`/`--key`) |
| `rate_limit_per_key_per_min` | int | 0 (off) | token bucket per bearer key |
| `usage_file` | path | `$XDG_DATA_HOME/szmelc/usage.jsonl` | JSONL journal file (empty = default) |
| `usage_max_bytes` | int | 10 MiB | per-file journal size before rotation |
| `usage_keep` | int | 3 | rotated files to keep (`.1`, `.2`, `.3`) |
| `middleware_off` | `[]string` | `[]` | disable default middlewares: `logging` \| `cors` \| `auth` \| `ratelimit` \| `usage` |

`serve` CLI flags (equivalent or override): `--host --port --cert --key
--max-body-bytes --once --grace-seconds`.

## `policies`

| Field | Type | Default | Notes |
|---|---|---|---|
| `cooldown_short_s` | int | 30 | cooldown after 429 / 408 / 5xx / conn error |
| `cooldown_long_s` | int | 3600 | cooldown after other 4xx (401/403/404/413/…) |
| `retry_same` | bool | false | one retry on the *same* provider after 429/5xx (key rotation included) |
| `max_chain_attempts` | int | 8 | total provider-attempts budget per request |
| `backoff` | `map[cls]spec` | — | override per status class: `conn` \| `429` \| `408` \| `4xx` \| `auth` \| `5xx` |

`backoff.<cls>`:

| Field | Type | Default |
|---|---|---|
| `base_s` | int seconds | class default (short/long per class) |
| `max_s` | int seconds | 0 = no cap |
| `jitter` | float 0..1 | class default (~0.25) |

## `catalog`

| Field | Type | Default | Notes |
|---|---|---|---|
| `sources` | `[]string` | `[providers, embedded]` | `providers` = live `/models`; `embedded` = bundled notes; remote feeds: `freellmapi`, `awesome-llm-apis`, `free-ai-models` |
| `refresh_s` | int | 3600 | cache TTL (seconds) for remote feeds |
| `cache_file` | path | `$XDG_CACHE_HOME/freellm/catalog.json` | on-disk catalog cache |
| `source_urls` | `map[string]string` | — | override a remote feed endpoint (mirror, proxy) |

## `logging`

| Field | Type | Default |
|---|---|---|
| `level` | `debug`\|`info`\|`warn`\|`error` | `info` |
| `format` | `json`\|`pretty` | `pretty` |
| `file` | path | `""` (stderr) |

Secrets (API keys, bearer tokens) are redacted in all log levels — enforced
by a test.

## `http` (outbound transport tuning)

| Field | Type | Default |
|---|---|---|
| `max_idle_conns` | int | 100 |
| `max_idle_conns_per_host` | int | 20 |
| `max_conns_per_host` | int | 0 (runtime default) |

## `routes_acl` — `map[string][]string`

Optional per-route access control (bearer keys only). Value accepts a key
*name* (from `server.auth.names`) or a raw key. Omitted = all keys allowed on
all routes:

```yaml
server:
  auth:
    mode: bearer
    names:
      alice: alice-secret
      bob:   bob-secret
routes_acl:
  code: [alice]     # only alice may use the "code" chain
```

## `agentic` — task router overlay

Layered on top of the embedded [`defaults/agentic.yaml`](../defaults/agentic.yaml).
Catalog entries and rules replace defaults by `id` (new ids are appended),
`roles` and `complexity` merge per key, and scalars replace. Full guide:
[agentic.md](agentic.md).

| Key | Type | Default | Meaning |
|---|---|---|---|
| `enabled` | bool | `true` | turn the task router on or off |
| `mode` | string | `rozklad-jazdy` | default mode: `rozklad-jazdy` ([Rozkład Jazdy], all in) or `jako-tako` (Jako Tako, just enough). Env `FREELLM_TASK_MODE` overrides it; `szmelc-router mode <id>` writes it |
| `aliases` | []string | `[agent, task]` | model names that trigger it (`agent:code:4` suffixes force role and complexity) |
| `below_tier_fallback` | bool | `true` | append weaker models at the end of the chain as a last resort |
| `escalations` | int | `1` | times an empty or filtered non-stream answer is retried one tier up |
| `roles` | map[string]int | see defaults | role → base tier (1–5) |
| `complexity` | map[int]int | `1:-1 2:0 3:0 4:+1 5:+2` | complexity → tier adjustment |
| `catalog[]` | entry | 25 entries | `id`, `model` (`provider/model`), `tier`, `cost`, `speed`, `context`, `tools`, `good_at`, `enabled`, `note` |
| `rules[]` | rule | 4 rules | `id`, `when` (`role`, `complexity_min`, `complexity_max`, `tools`, `local`), `min_tier`, `max_tier`, `prefer`, `only_where` |

`config validate` reports duplicate ids, tiers outside 1–5, rules that
prefer unknown entries, and catalog entries for providers that are not
configured.

## `limits` — `map[string]limits`

Quotas you declare, keyed by provider name. A provider entry may carry its
own `limits:` too; the top-level map wins. Windows are rolling. Nothing is
shipped by default: free tiers change too often to guess.

| Key | Type | Meaning |
|---|---|---|
| `rpm` | int | requests per minute |
| `rpd` | int | requests per 24 hours |
| `tpm` | int | tokens per minute |
| `tpd` | int | tokens per 24 hours |
| `note` | string | free text shown by `usage` |

The limits are shown by `usage`, `GET /v1/usage` and the TUI. They are
enforced as a budget when `control.enforce_limits` is in effect.

## `ledger` — event log

| Key | Type | Default | Meaning |
|---|---|---|---|
| `file` | string | `$XDG_DATA_HOME/szmelc/usage.jsonl` | ledger path (`server.usage_file` is honoured when this is empty) |
| `disabled` | bool | `false` | no event logging anywhere |
| `content` | string | `preview` | prompt/answer capture: `off`, `preview` (200 chars), `full` |
| `max_bytes` | int | 10 MiB | server rotation size |
| `keep` | int | `3` | rotated files kept |

## `control` — control plane

Each set key overrides the profile preset. See
[control-and-observability.md](control-and-observability.md).

| Key | Type | Default | Meaning |
|---|---|---|---|
| `profile` | string | `balanced` | `auto` (∞ infinite & full auto), `control` (⛭ strict), `balanced` |
| `paused` | bool | `false` | refuse every call |
| `disabled` | []string | — | providers taken out of routing |
| `allow` | []string | — | when non-empty: only these providers |
| `fallback` | bool | per profile | walk the fallback chain |
| `forever` | bool | per profile | wait out cooldowns and retry until answered |
| `enforce_limits` | bool | per profile | treat `limits:` as a budget |
| `max_attempts` | int | `0` | upstream calls per request (`0` = `policies.max_chain_attempts`) |
| `max_wait_s` | int | `0` | forever mode: total waiting cap (`0` = until the request is cancelled) |
| `escalate` | string | `auto` in the auto profile | route tried once the chain is exhausted (`""` = none) |
| `hooks.pre_call` | []string | — | command that may veto or rewrite each call (fails closed). Config file only: the API refuses hook changes |
| `hooks.post_call` | []string | — | command run after each call (background) |
| `hooks.timeout_s` | int | `10` | per-hook timeout |

## Free-tier notes

Machine-readable, nothing hardcoded: every provider entry's `notes:` field is
surfaced by `szmelc-router notes`, `szmelc-router models --tier`,
`GET /v1/models` (per-model tier notes), and `GET /v1/notes`. The canonical
data lives in `defaults/providers.yaml`; user config `notes` overrides it.
