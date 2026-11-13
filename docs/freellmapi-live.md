# FreeLLMAPI live — routing by richness

[FreeLLMAPI](https://github.com/tashfeenahmed/freellmapi) keeps a signed model
catalog of the free-tier landscape: 87 providers, over 1,300 free endpoints,
with a size class, context window and rate limits for every model. Free
installs get a monthly snapshot of it. The **Premium "live" plan**
(`fla_…` key, [freellmapi.co](https://freellmapi.co/#pricing)) gets the same
catalog the day it changes.

szmelc-router uses that key directly. Put it in `FREELLM_API_KEY` and the
router checks it. When it works, the task router's default mode becomes
**`live`**: all in, ranked **by richness** over the live catalog.

```bash
export FREELLM_API_KEY=fla_…            # your Premium license key
szmelc-router live                      # ✓ live  license fla_…abcd active (lifetime), live catalog v2026.11.13, …
szmelc-router chat "hi"                 # no --model: goes to "live" now
szmelc-router route "refactor this" --all   # see the ranking and why each model lost
```

## What "by richness" means

Every eligible model goes into the chain, like in [Rozkład Jazdy]. The order
differs:

1. **Size class**, from the catalog's cross-provider `sizeLabel`:
   Frontier (tier 5) > Large (4) > Medium (3) > Small (2).
2. **Context window**: the bigger one first.
3. **Free daily capacity**: `limits.tpd`, else `monthlyTokenBudget / 30`.
   Unknown budgets count as zero. They are never guessed.
4. **Intelligence rank**: the catalog ranks models within each provider,
   so this only breaks ties that are otherwise exact.
5. **Speed** (`speedRank`).

The hard filters still apply: tool calling, context size, `local`, and
availability (key set, not cooling down). Rule preferences still win between
models of the same tier. Models that are `enabled: false` in the catalog, or
that are not text models, never enter.

Live entries are added to the task router's catalog with ids like
`live:google/gemini-3-pro`. The 25 hand-tuned entries of
`defaults/agentic.yaml` stay. When both list the same `provider/model`, the
hand-tuned entry wins. At most `live.max_models` live entries are added (300 by
default, richest first). A catalog platform only counts when szmelc-router has
a provider for it. Most names are the same. These are mapped:

| FreeLLMAPI platform | szmelc-router provider |
|---|---|
| `github` | `github_models` |
| `zhipu` | `zai` |
| `huggingface` | `hf_router` |
| `opencode` | `opencode_zen` |
| `aion` | `aionlabs` |
| `ollama` (Ollama **Cloud**) | `ollama_cloud` (never the local `ollama`) |

Platforms that only FreeLLMAPI knows, such as electronhub, aclide or xkiro,
are reachable through the [sidecar](#the-sidecar-key-freellmapi).

## Which key is which

Two different credentials are both called "the freellmapi key". The router
tells them apart by prefix and never sends one to the other's host:

| Prefix | What it is | How it is checked |
|---|---|---|
| `fla_…` | Premium license, the "live" plan | `GET https://api.freellmapi.co/v1/license/check` (Bearer), then the live catalog from `GET /v1/latest` (Bearer) |
| `freellmapi-…` | unified key of a FreeLLMAPI server you run | `GET $FREELLMAPI_URL/v1/models` (Bearer) |

The router looks for keys in this order. Duplicates are dropped, and licenses
are tried before sidecar keys:

1. `live.key` in the config file (`${VAR}` works)
2. `FREELLM_API_KEY`, `FREELLMAPI_LICENSE_KEY`, `FREELLMAPI_PREMIUM_KEY`,
   `FREELLMAPI_LICENSE`. A value without a known prefix is tried as a license.
3. `FREELLMAPI_API_KEY`, `FREELLMAPI_KEY`. A value without a known prefix is
   taken as a sidecar key.
4. The `license` field of FreeLLMAPI's own declarative config
   (`FREEAPI_CONFIG_JSON` or the file in `FREEAPI_CONFIG_PATH`).

`FREELLM_API_KEY` has another use: it is the bearer key that `szmelc-router env`
and the CLI client send to *your* szmelc-router server. A value that starts
with `fla_` or `freellmapi-` is not used for that. Values in
`server.auth.keys` are never sent anywhere.

The license check never activates anything: `/v1/license/activate` is the
dashboard's path and is not called. A key counts as **live** only when the
license is valid **and** the catalog service serves it the `live` tier. A key
that only gets the `monthly` tier is not live.

## Trust, cache and outages

- Every catalog is verified with **Ed25519 over the exact bytes received**,
  against the key FreeLLMAPI pins (`MCowBQYDK2VwAyEAq9yv4+…`). A response that
  is unsigned or tampered with is thrown away. `CATALOG_PUBKEY` or
  `live.pubkey` override the key, for a self-hosted catalog only.
- The verdict is cached in the user cache dir (`~/.cache/szmelc/live.json`
  and `live-catalog.json`, mode 0600, bound to a hash of the key, never the
  key itself). The license is checked again after 6 h. The catalog is
  fetched again after 12 h, which is how often FreeLLMAPI polls it; a 304
  keeps the cached copy. A cached catalog is verified again each time it is
  loaded.
- Without a key, nothing is read or written and no request is made.
- An unreachable service never turns live off. A "valid" verdict is kept for
  up to 7 days of outage, with a warning. A network error never fails
  `config.Load`. The worst case is that live stays off for that run.

## When live becomes the default

A working key makes `live` the default task mode **only when no mode is set**:

| You have … | Result |
|---|---|
| no `agentic.mode` in the config, no `FREELLM_TASK_MODE` | **live is the default**. `chat`/`exec` with no `--model` use `live` |
| `agentic.mode: …`, `szmelc-router mode …`, or `FREELLM_TASK_MODE` | your mode stays. The live entries still feed the catalog, and `live` is still one request away |
| `live.keep_mode: true` | same as above, without setting a mode |
| `live.enabled: off` or `FREELLM_LIVE=off` | the router never looks for a key |

Per request, a working key is not needed: `"model": "live"` (the same as
`agent:live`), the header `X-Freellm-Task-Mode: live`, or `metadata.task_mode`.
`live` also works without a key. It then ranks the built-in catalog by size
and context.

Mode spellings: `live`, `Live`, `po bogatości`, `bogaty`, `rich`, `freellmapi`.

## Commands and endpoints

```bash
szmelc-router live            # status from the cache: keys (masked), license, catalog, entries, mode
szmelc-router live check      # check the license again now
szmelc-router live sync       # check again and download the catalog again
szmelc-router live --json
szmelc-router mode live       # make live the permanent default, with or without a key
curl -s localhost:8787/v1/live | jq    # what the server found at startup
```

`serve` logs one line at startup: `freellmapi live active …`. When a key was
found but did not work, it logs a warning with the reason.

## The sidecar key (`freellmapi-…`)

If you also run FreeLLMAPI yourself (`curl -fsSL https://freellmapi.co/install.sh | bash`,
dashboard on `http://localhost:3001`), it is a whole router over your own
provider keys. Give szmelc-router its unified key:

```bash
export FREELLMAPI_URL=http://localhost:3001     # without /v1
export FREELLMAPI_API_KEY=freellmapi-…          # dashboard → Keys
```

A working sidecar adds two entries to live: `freellmapi/auto:smart` (tier 5,
FreeLLMAPI's "smartest" strategy) and `freellmapi/auto` (tier 4, "balanced").
They have no known context window, so frontier models with one rank first.
On its own, a sidecar key also makes live the default. A sidecar key found in
`FREELLM_API_KEY` is passed on to the `freellmapi` provider as well.

The `fla_` license also belongs in the sidecar, to give it the live feed:
dashboard → **Premium**, or `"license": "fla_…"` in its
`FREEAPI_CONFIG_JSON`. szmelc-router reads that field too.

## Config

```yaml
live:
  enabled: auto        # auto (default) | offline (cache only, no network) | off
  key: ${FREELLM_API_KEY}   # optional, discovered when empty
  max_models: 300      # live entries given to the task router, richest first
  keep_mode: false     # true: use the live catalog but keep your agentic.mode
  # catalog_url: https://api.freellmapi.co   # or $CATALOG_BASE_URL
  # pubkey: "-----BEGIN PUBLIC KEY-----…"    # or $CATALOG_PUBKEY (self-hosted catalog only)
```

| Env | Meaning |
|---|---|
| `FREELLM_API_KEY` | the key (license `fla_…` or sidecar `freellmapi-…`) |
| `FREELLM_LIVE` | `auto` \| `offline` \| `off`, beats `live.enabled` |
| `FREELLM_LIVE_CACHE` | cache directory (default: user cache dir `szmelc/`) |
| `CATALOG_BASE_URL`, `CATALOG_PUBKEY` | catalog service and signing key, the same names FreeLLMAPI uses |
| `FREELLMAPI_URL`, `FREELLMAPI_API_KEY` | sidecar base URL and unified key |

## Testing it

The unit tests use `httptest` and a freshly generated Ed25519 key pair. They
cover a valid key, a tampered signature, the wrong pinned key, 401, the
monthly tier, 304, an outage with and without the cache, a sidecar key, and
the platform mapping. Under `go test`, `config.Load` leaves live off unless
`FREELLM_LIVE` is set, so an exported key never makes the test suite hit the
network. To check the real service and the pinned production key:

```bash
FREELLM_LIVE_INTEGRATION=1 go test ./internal/live -run Real -v
```

That pulls the public monthly catalog without a key. With `FREELLM_API_KEY`
set, it pulls the live catalog.
