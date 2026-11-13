# MODELS.md — everything the router can give you, and under what conditions

> **DEPRECATED (v1.0.0-rc1):** this file is frozen as a historical
> reference. Model + free-tier data now lives in `defaults/providers.yaml`
> (embedded, machine-readable) and is exposed live via
> `szmelc-router models --tier`, `szmelc-router notes`, and
> `GET /v1/notes` + `GET /v1/models`. New models come from the catalog
> feeds + live provider `/models` — the static list below will go stale.
>
> How to request: `model` = an **alias** from §1, or an exact
`provider/slugged-id` (e.g. `g4f/gpt-4o-mini`, `ollama/llama3.2:3b`,
`openrouter/deepseek/deepseek-r1:free`). Run `python freellm_router.py models`
for the live list on this machine.

Nothing here is "unlimited free compute" — vendor free tiers are capped per
day/month, g4f is best-effort scraping, Ollama is your own hardware.
FreeLLMAPI's marketing figure for stacking all vendor free tiers is
**~7.4B tokens/month**; our router exposes the same pool.

## 1. Aliases (fallback chains, best-first)

| Alias | Chain (order = fallback) | Use for |
|---|---|---|
| `auto` | google/gemini-2.5-flash → groq/llama-3.3-70b → openrouter/deepseek-r1:free → g4f/gpt-4o-mini → ollama | default |
| `smart` | google/gemini-2.5-pro → openrouter free 70B → groq → g4f/o3-mini → ollama | quality |
| `fast` | groq/llama-3.1-8b-instant → google/flash-lite → cerebras/llama3.1-8b → openrouter free 8B → g4f → ollama | low latency / tokens |
| `code` | groq 70B → openrouter qwen-coder:free → g4f/gpt-4o → ollama qwen2.5-coder | coding |
| `free` | g4f/gpt-4o-mini → g4f/llama-3.1-8b → ollama | **no keys at all** |
| `local` | ollama → g4f | offline-ish / privacy |

Overridable in `router_config.json` (`"aliases"` section).

## 2. Key-based free tiers (need the key in `.env`; caps published by vendor)

All free under each vendor's ToS for the free tier; limits per key, per day.

| Provider (env key) | Free cap (vendor-published) | Models worth routing |
|---|---|---|
| **Google AI Studio** `GOOGLE_API_KEY` | ~10–15 req/min, ~250 req/day (varies by model) | `gemini-2.5-flash`, `gemini-2.5-flash-lite`, `gemini-2.5-pro`, `gemini-2.0-flash`, newer `gemini-3-*` previews |
| **Groq** `GROQ_API_KEY` | ~14,400 req/day, 1M tokens/day (per-model caps) | `llama-3.3-70b-versatile`, `llama-3.1-8b-instant`, `gemma2-9b-it`, `qwen-qwq-32b`, `deepseek-r1-distill-llama-70b` |
| **OpenRouter** `OPENROUTER_API_KEY` | models tagged `:free`: 50 req/day, 1,000 req/mo (2,000/mo after $10 lifetime credits) | any `<vendor>/<model>:free` on openrouter.ai (DeepSeek, Qwen, Llama, GLM, Kimi …) |
| **Cerebras** `CEREBRAS_API_KEY` | ~3,000 req/day free dev tier | `llama3.1-8b`, `llama-3.3-70b` (when available), `qwen3-*` |
| **SambaNova** `SAMBANOVA_API_KEY` | 750M tokens/mo, 1,000 req/min | `DeepSeek-V3`, `Meta-Llama-3.1-70B-Instruct`, `Gemma3-27B` |
| **Mistral** `MISTRAL_API_KEY` | 500k tokens/mo per free model | `mistral-small-latest`, `codestral-latest`, `open-mistral-nemo` |
| **Z.ai / Zhipu** `ZAI_API_KEY` | daily request + RPM caps (GLM free tier) | `glm-4-flash`, `glm-4-air`, `glm-4-plus` (trial) |
| **NVIDIA NIM** `NVIDIA_API_KEY` | 1,000 free req **per model** (one-time) | `meta/llama-3.1-70b-instruct`, `deepseek-ai/deepseek-r1`, `nvidia/nemotron-*` |
| **GitHub Models** `GITHUB_TOKEN` | 15 req/min, 150 req/day | `gpt-4.1`, `gpt-4o`, `claude-3.5-sonnet`, `o4-mini`, `phi-4` |
| **Cohere trial** `COHERE_API_KEY` | 10 req/min, 100 req/day | `command-r`, `command-r-plus` (trial key) |
| **Cloudflare Workers AI** `CLOUDFLARE_API_KEY` | 10,000 neurons/day | `@cf/meta/llama-3.1-8b-instruct`, `@cf/deepseek/deepseek-r1-distill-qllama-8b` |

Sidecars (optional; the router also treats these as upstreams):
`freellmapi/freellmapi/auto` (tashfeenahmed's 34-vendor router if you run it)
and `llmcycle/<model>` (llmcycle server if you run it).

## 3. g4f keyless models (`g4f/<id>`) — no key, no account

Full id → route as `g4f/<id>` (e.g. `g4f/gpt-4o-mini`, `g4f/o3-mini`,
`g4f/deepseek-r1`). These are scraped via g4f's providers; which upstream
backs each id depends on what's working at the moment.

**Condition:** free, unlimited-in-practice *if* g4f's provider(s) for that
model are up; real-world throughput is low, it rate-limits per IP, and
providers come and go without notice. Not for production; fine as fallback.

Text models (85 in g4f 8.8.x registry; verified-working subset marked ✓ on
this box today):

| id | id | id |
|---|---|---|
| ✓ `gpt-4o-mini` | `gpt-4o` | `gpt-4.1` |
| `gpt-4.1-mini` | `gpt-4.1-nano` | `gpt-4.5` |
| `gpt-4` | `gpt-oss-120b` | ✓ `o3-mini` |
| `o3-mini-high` | `o4-mini` | `o4-mini-high` |
| `o1` | `o1-mini` | `r1-1776` |
| ✓ `deepseek-r1` | `deepseek-v3` | `deepseek-r1-distill-llama-70b` |
| `deepseek-r1-distill-qwen-14b` | `deepseek-r1-distill-qwen-1.5b` | |
| `llama-4-maverick` | `llama-4-scout` | ✓ `llama-3.3-70b` |
| `llama-3.1-405b` | `llama-3.1-70b` | `llama-3.1-8b` |
| `llama-3.2-90b` | `llama-3.2-11b` | `llama-3.2-3b` |
| `llama-3.2-1b` | `llama-3-8b` | `llama-3-70b` |
| `llama-2-7b` | `llama-2-70b` | |
| `mistral-7b` | `mistral-nemo` | `mistral-small-24b` |
| ✓ `mistral-small-3.1-24b` | `mixtral-8x7b` | `phi-3.5-mini` |
| `hermes-2-dpo` | `nemotron-70b` | |
| ✓ `gemini-2.5-flash` | `gemini-2.5-pro` | `gemini-3-pro-preview` |
| `gemini-3.1-pro` | `gemini-3.1-flash-lite` | `gemini-3.5-flash` |
| `gemini-3.5-flash-lite` | `gemini-3.5-flash-thinking` | `gemini-3.5-flash-thinking-lite` |
| `gemini-3.6-flash` | `gemini-3.7-flash` | `gemini-3.8-flash` |
| `gemini-3.8-pro` | `gemini-flash-lite` | `gemini-auto` |
| `qwen-3-235b` | `qwen-3-32b` | `qwq-32b` |
| `qwen-2.5-coder-32b` | `qwen-2.5-vl-72b` (vision) | |
| ✓ `sonar` (Perplexity) | `sonar-pro` | `sonar-reasoning` |
| `sonar-reasoning-pro` | | |
| `kimi-k2` | `grok-2` | `grok-3` |
| `grok-3-r1` | `meta-ai` (Meta Llama via meta.ai) | `aria` (Opera) |
| `command-r` / `command-r7b` / `command-r-plus` (Cohere) | `command-a` | |

Media models (g4f also does these via `g4f/`): `dall-e-3`, `flux`,
`flux-kontext`, `flux-pro`, `gpt-image`, `sd-3.5-large`, `sdxl-turbo`,
`gpt-4o-mini-tts`.
Also `agent-tools` (g4f's built-in web/tool provider).

## 4. Ollama local models (`ollama/<name>`) — currently installed here

**Condition:** free & unlimited while the box is on; quality/speed bound by
your hardware (on a Pi, `qwen3:0.6b`–`3b` class models are realistic).

`gpt-oss:20b`, `qwen3.8:27b`, `qwen3.5:4b`, `qwen3.5:0.8b`, `qwen3:0.6b`,
`qwen3-abliterated:32b` (huihui_ai), `qwen3-vl-abliterated:8b`
(huihui_ai), `qwen3-embedding:0.6b`, `gemma4:e2b`, `gemma4:e4b`,
`gemma-4-12b-it` (unsloth Q8), `BieliK-4.5B-v3` (speakleash GGUF),
`Kev-4B` (ggml GGUF), `lfm3`, `lfm2.5-2.6b` (oamazonasgabriel q4),
`sollm2:135m`, `dolphin-mixtral:8x7b`, `Qwen3.8-27B-Uncensored:q6_K`
(orcarouter), `Qwen3.8-Uncensored:q4_K_M` (srchmnmichael), `q3.8-shell`.

Add more any time with `ollama pull <model>` — the router picks new models
up automatically.

## 5. Rules of thumb

- **Default to `auto`.** Let the router pay the free pools; you never call a
  vendor directly, so you're never stuck at one cap.
- **Burst → `fast`; hard problem → `smart`; offline / no keys → `free`.**
- Keyless `g4f` + `ollama` mean the service **never fully dies** even with
  zero keys or a wiped free quota — just slower/smoother.
- Respecting caps: the router puts a failing target on 30 s (429/5xx) or 1 h
  (auth error) cooldown, so it naturally spends each free tier slowly rather
  than burning one in a minute.
