# MORE.md — Free Inference / Free-AI landscape research

**Date:** 2026-11-02 · **Method:** deep + wide search — ~60 "free AI"-meaning phrases
run through the GitHub search API, README-scraping of 4 major curated
directories, and per-repo verification (star count, last push, description).
All links below were checked live; star counts as of today.

Scope: "free inference like those already integrated" — i.e. keyless scraping
engines (g4f), free-tier aggregators/routers (freellmapi, llmcycle), free
cloud inference providers, and local engines (Ollama) — plus everything found
in between.

---

## 1. Routers / gateways / aggregators (run as sidecars — like freellmapi & llmcycle)

Already referenced in this repo:
- https://github.com/tashfeenahmed/freellmapi (45.8k★) — **integrated as `freellmapi` upstream.** 34 free providers, 635 free endpoints, one `/v1`, per-key cap tracking, signed self-updating model catalog.
- https://github.com/llmcycle (sidecar `llmcycle` upstream) — free-LLM cycling server.

Found (not yet integrated here):
- https://github.com/cheaper-inference/OmniRoute (83.7k★) — Free MIT AI gateway: one endpoint, 375 providers (150+ free), 1,400+ models (Kimi, Claude, GPT, Gemini, GLM, DeepSeek, MiniMax).
- https://github.com/Alishahryar1/free-claude-code (65.8k★) — Run Claude Code, Codex, Antigravity, VSCode, Pi, OpenCode (6+ harnesses) on free tiers (1.3B+ free tokens).
- https://github.com/router-for-me/CLIProxyAPI (57.2k★) — Wrap Antigravity / ChatGPT Codex / Claude Code / Grok Build / Devin as an OpenAI/Gemini/Claude/Codex-compatible API service.
- https://github.com/BerriAI/litellm (62.5k★) — AI gateway, 100+ LLMs in OpenAI format, load balancing, cost tracking; can proxy all free tiers.
- https://github.com/QuantumNous/new-api (51.2k★) — Self-hosted LLM gateway: OpenAI/Claude/Gemini-compatible API, routing, access control, usage & cost accounting.
- https://github.com/songquanpeng/one-api (37.3k★) — LLM API management & distribution (Chinese ecosystem): key management, secondary distribution, many providers.
- https://github.com/decolua/9router (31.8k★) — Free AI coding: connect Claude Code/Codex/Cursor/Cline/Copilot/Antigravity to free Claude/GPT/Gemini via 40+ providers, auto-fallback.
- https://github.com/mnfst/llm-gateway (7.6k★) — Open-source LLM gateway.
- https://github.com/justlovemaki/AIClient2API (8.9k★) — Self-hosted multi-protocol proxy for Antigravity, Codex, Grok, Kiro, OpenAI, Claude + custom.
- https://github.com/BlockRunAI/ClawRouter (6.7k★) — Agent-native LLM router; wallet-based x402 payments + free pools.
- https://github.com/vllm-project/semantic-router (6.4k★) — Programmable decision layer routing to models/compute.
- https://github.com/lm-sys/RouteLLM (5.7k★) — Serve/evaluate LLM routers to cut costs without quality loss.
- https://github.com/VoAPI/VoAPI (1.1k★) — AI model API aggregation/distribution gateway (zh).
- https://github.com/Mirrowel/LLM-API-Key-Proxy (559★) — Universal LLM gateway: OpenAI/Anthropic-compatible, multi-provider translation, load balancing.
- https://github.com/NadirRouter/NadirClaw (661★) — LLM router + cost optimizer: simple prompts → cheap/local, complex → premium.
- https://github.com/easychen/openai-api-proxy (1.6k★) — One-line Docker OpenAI API proxy (zh).
- https://github.com/zhalice2011/ProxyLLM (408★) — Local Electron app: captures browser sessions to unify LLM web sites into OpenAI-compatible API (zh).
- https://github.com/ihsan-ramadhan/bansos-router (225★) — Keyless free coding models routed to every coding harness via one local endpoint.
- https://github.com/Quorinex/Freebuff2API (748★) + https://github.com/pingmike2/freebuff2api-wokers (397★) — Freebuff/Codebuff free models behind OpenAI-compatible API, token rotation, Docker-first.
- https://github.com/OEvortex/llm4free (377★) — Python toolkit: 40+ free providers, web search, AI interaction.

## 2. Keyless scraping / reverse-engineered web APIs (the g4f family)

Already integrated here via `pip install g4f`:
- https://github.com/xtekky/gpt4free (67k★) — **the g4f library** (successor repo of Aristex/gpt4free). GPT-5.3, Opus 4.6, Kimi 2.5, DeepSeek, Gemini 3, etc., no key.
- https://github.com/xiangsx/gpt4free-ts (7.6k★) — TypeScript port of gpt4free.
- https://github.com/gpt4free/g4f.dev (189★) — g4f.dev: ready-to-use free endpoints (hosted, for direct app use).
- https://github.com/XInTheDark/raycast-g4f (1.1k★) — Raycast extension exposing GPT/Claude/Llama via g4f.
- https://github.com/gptforfree/gpt4free (360★) — "uncensored" gpt4free fork.

Found (not integrated):
- https://github.com/chatanywhere/GPT_API_free (45.2k★) — Free model API: GPT/DeepSeek etc., 10,000 free credits refreshed daily (zh).
- https://github.com/nashsu/FreeAskInternet (8.7k★) — Free, private, local search aggregator + answer engine using multiple LLMs, no GPU.
- https://github.com/ayaka14732/ChatGPTAPIFree (3.3k★) — Open-source proxy: ChatGPT API for free.
- https://github.com/missuo/FreeGPT35 (3.2k★) — Login-free ChatGPT Web → GPT-3.5 API.
- https://github.com/aurora-develop/aurora (2.6k★) — free chatgpt api.
- https://github.com/ading2210/poe-api (2.5k★) / https://github.com/snowby666/poe-api-wrapper (1.1k★) — Reverse-engineered Poe.com API: free GPT-4, Claude, Llama, Gemini, Mistral (first is unmaintained).
- https://github.com/jwadow/kiro-gateway (2.3k★) — Proxy for Kiro IDE & CLI (Amazon Q): free Claude models through any client.
- https://github.com/Amm1rr/WebAI-to-API (1.4k★) — generic "webchat → API" bridge.
- https://github.com/RockChinQ/free-one-api (939★) — Reverse-engineered endpoints (ChatGPT/g4f/Bard/Claude/HuggingChat/Qwen) managed behind standard OpenAI API.
- https://github.com/NIyueeE/ds-free-api (854★) — DeepSeek web-UI API proxy, OpenAI & Anthropic compatible.
- https://github.com/aledipa/Free-GPT4-WEB-API (727★) — Self-hosted, unlimited free web API: Gemini, DeepSeek, Claude, GPT.
- https://github.com/ramon-victor/freegpt-webui (5.6k★) — GPT-3.5/4 web UI, no API key.
- https://github.com/IntelligenzaArtificiale/Free-Auto-GPT (2.5k★) — Auto-GPT on free APIs.
- https://github.com/GetGoAPI/Free-GPT-Grok-Gemini-Claude-API (218★) — GPT-5/o4/Claude 4.5/Gemini 3 via one API (CN-friendly "best OpenRouter alternative").
- https://github.com/mfoud444/ollamafreeapi (335★) — Free public gateway to managed Ollama servers: 50+ models, zero-config.
- https://github.com/y13sint/FreeQwenApi (339★) — Qwen web API proxy with context/session management.
- https://github.com/ChatTeach/FreeGPT (271★) — GPT-3.5/4, no key.
- https://github.com/eaveszen/Gemini-Free-API (58★) / https://github.com/kodelyx/free-gemini-api (41★) — Free Gemini APIs, OpenAI-compatible.
- https://github.com/callbacked/keyless-gpt-wrapper-api (102★) — [deprecated] GPT-4o/Claude via DuckDuckGo AI Chat, keyless.

## 3. Hosted services — zero key, zero signup (drop-in `base_url`, no auth)

- https://pollinations.ai (+ https://github.com/pollinations/pollinations, 5.3k★) — Friendly open-source Gen-AI platform: free LLM + image APIs, no auth.
- https://developer.puter.com/tutorials/free-llm-api/ (Puter) — Free LLM API in the browser, no signup (puter.js).
- https://aihorde.net (+ https://github.com/Haidra-Org/AI-Horde, 1.7k★) — Crowdsourced distributed cluster for AI text/image generation; public API, free tier.
- https://freeinference.org (FreeInference) — Directory/terminal of free inference providers (used by YoannDev90's list).
- https://clawlabs.ai / https://github.com/ClawLabsAI/free-ai-models (252★) — Daily-updated ranked list of free LLMs with live status **plus** one OpenAI-compatible endpoint routing to the best available.
- https://gratisfy.xyz — Unified access point + benchmarks over the free inference providers.
- https://ollama.com/settings/keys (Ollama Cloud) — 17 free cloud models beyond local Ollama.

## 4. Free-tier providers NOT yet in this router's `DEFAULT_BASE_URLS`

(Already integrated: Google, Groq, Cerebras, SambaNova, Mistral, Z.ai,
OpenRouter, NVIDIA NIM, GitHub Models, Cloudflare, Cohere.)

Verified against https://github.com/mnfst/awesome-free-llm-apis (11.9k★) provider directory (data refreshed daily, live at https://freellm.net):

| Provider | Free offering | Base URL |
|---|---|---|
| OpenCode Zen — https://opencode.ai | 34 free models (audio, reasoning, vision), no CC | `https://opencode.ai/zen/v1` |
| LLM7.io — https://llm7.io | 24 free models, no CC | `https://api.llm7.io/v1` |
| Kilo Code — https://kilo.ai | 23 free models, no CC | `https://api.kilo.ai/api/gateway` |
| Aion Labs — https://aionlabs.ai | 11 free models | `https://api.aionlabs.ai/v1` |
| Hugging Face Inference — https://huggingface.co | 9 free models (router) | `https://router.huggingface.co/v1` |
| Agnes AI — https://agnes-ai.com | 5 free models | `https://apihub.agnes-ai.com/v1` |
| ModelScope — https://modelscope.cn | 61 free models | `https://api-inference.modelscope.cn/v1` |
| Alibaba Cloud Model Studio (Bailian) — https://bailian.console.aliyun.com | 5 free models | `https://dashscope-intl.aliyuncs.com/compatible-mode/v1` |
| SiliconFlow — https://cloud.siliconflow.cn | 4 free models | `https://api.siliconflow.cn/v1` |
| OVHcloud AI Endpoints — https://www.ovhcloud.com | 15 free models | `https://oai.endpoints.kepler.ai.cloud.ovh.net/v1` |
| xAI / Grok free — https://console.x.ai | 2–3 free models | `https://api.x.ai/v1` |
| DeepSeek free — https://platform.deepseek.com | 2 free models | `https://api.deepseek.com/v1` |
| Chutes.ai — https://chutes.ai | 2 free models | `https://api.chutes.ai/v1` |
| Glhf.chat — https://glhf.chat | 2 free models | `https://glhf.chat/api/openai/v1` |
| AI21 Studio — https://studio.ai21.com | Jamba free trial | `https://api.ai21.com/studio/v1` |
| Nebius — https://studio.nebius.com | free trial | `https://api.studio.nebius.com/v1` |
| Nscale — https://console.nscale.com | 2 free models | `https://inference.api.nscale.com/v1` |

Startup-credit providers (free credits, not recurring — one-shot capacity):
- https://together.ai — $5 free credits, 100+ OSS models.
- https://fireworks.ai — $1 free credits, fast OSS inference.
- https://replicate.com — $5 free credits (no card), any model incl. Flux.
- https://deepinfra.com — free sign-up credits, serverless inference.
- https://novita.ai — $0.50 free credits.
- https://hyperbolic.ai — free tier, OSS model hosting.
- https://aimlapi.com — 200+ models, free tier included.
- https://stability.ai — 25 free credits/mo (images).
- https://huggingface.co — PRO trial credits path.

Free relay/cascade gateways found in
https://github.com/YoannDev90/awesome-free-ai-api (82 APIs, tested daily) —
lower trust tier, useful as deep fallback:
FreeTheAI (freetheai.xyz), DGrid AI, Electron Hub, NagaAI (naga.ac),
AIHubMix (aihubmix.com), AI Pooled (ai.pooled.dev), SiliconFlow, LiteRouter,
LLM-Gateway.io (free filter), Vercel AI Gateway (freeTier=true),
Requesty (free models), Coze, AwanLLM, BazaarLink, TokenReply, UnoRouter,
SwiftRouter, Resurge, StudioLM, Pixazo, Ofox, OhMyGPT, SubNP, ZenLLM,
LLM7, Logfare, MegaNova, Nexusify, Atessa, EvolveX, Flowbar AI, Yingsuan.ai,
CoderPlan, A4F, AIchixia, AnyAPI, Apertis, AquaDevs, BlazeAI, Completions,
Crax GPT, DLab Pulse, Hubs02225, Inference.net, Inception Labs,
IBM watsonx.ai (free tier), MNN AI, MincAPI, Mixedbread, NaraRouter, Navy AI,
Onomeo, PaxSenix, Pydantic AI Gateway, Rout.my, Routeway, SixFingerAPI,
Subaxis, TokenHarbor, ValorGPT, Void AI.

## 5. Local / self-hosted engines (Ollama-class, for the `local` route)

Already integrated: Ollama (https://ollama.com).

Found (all OpenAI-compatible servers → candidates for `local` fallback or
sidecar):
- https://github.com/ggml-org/llama.cpp (133k★) — LLM inference in C/C++; the reference local runtime.
- https://github.com/vllm-project/vllm (96k★) — high-throughput inference/serving engine; plus https://github.com/GeeeekExplorer/nano-vllm (16k★) minimal implementation.
- https://github.com/Mintplex-Labs/anything-llm (67.8k★) — local-first agent workspace, runs over local engines.
- https://github.com/FlashML-org/FreeToken (15.5k★) — datacenter-scale model serving on desktop hardware.
- https://github.com/Arthur-Ficial/apfel (6.5k★) — Apple Intelligence on your Mac as CLI + OpenAI-compatible server, no keys/cloud.
- https://github.com/AlexsJones/llmfit (38.6k★) — one command to find which models run on your hardware.
- https://github.com/nomic-ai/gpt4all (77k★) — run local LLMs on any device.
- https://github.com/mudler/LocalAI (50k★) — open-source drop-in "AI engine": LLMs, vision, voice, image, video without GPU.
- https://github.com/janhq/jan (45k★) — 100% offline ChatGPT alternative.
- https://github.com/sgl-project/sglang (38k★) — high-performance serving framework.
- https://github.com/mozilla-ai/llamafile (26.5k★) — run LLMs from a single distributable file.
- https://github.com/LostRuins/koboldcpp (12k★) — single-binary GGUF inference (KoboldAI).
- https://github.com/FlashML-org/FreeVideo (2.6k★) — run MiniMax H3 video model in 8 GB VRAM.
- https://github.com/sqliteai/warp (2.5k★) — runs 2.78T-param Kimi K3 / DeepSeek V4.1 / GLM-5.3 beyond RAM by streaming weights from NVMe.
- https://github.com/bentoml/OpenLLM (12.6k★) — any OSS LLM as OpenAI-compatible endpoint.
- https://github.com/xorbitsai/inference (9.6k★, Xinference) — run OSS/speech/multimodal models, one-line GPT swap.
- https://github.com/intel/ipex-llm (8.8k★) — accelerate local LLM inference on Intel CPU/iGPU.
- https://github.com/mlc-ai/web-llm-chat (1.1k★) — LLM in the browser (WebLLM).

Front-ends worth pointing at these (Open WebUI ↔ Ollama is the canonical pair):
- https://github.com/open-webui/open-webui (157k★) — AI interface supporting Ollama, OpenAI API, etc.
- https://github.com/LibreChat-AI/LibreChat (46.6k★) — ChatGPT clone with agents/MCP, multi-provider.
- https://github.com/Open-LLM-VTuber/Open-LLM-VTuber (14.5k★) — hands-free voice + Live2D over any LLM.

## 6. Curated free-LLM lists & directories (meta, for ongoing discovery)

- https://github.com/mnfst/awesome-free-llm-apis (11.9k★) — **best maintained**: permanent free LLM APIs/keys, 513+ APIs, daily-refreshed data, live at freellm.net.
- https://github.com/open-free-llm-api/awesome-freellm-apis (5k★) — provider directory + base URLs + one-click configs (feeds the table in §4).
- https://github.com/12britz/awesome-free-models (2.6k★) — free AI models/APIs/tools, zero cost.
- https://github.com/zebbern/no-cost-ai (2.4k★) — 80+ free AI services (chat, image, video, voice, APIs).
- https://github.com/zukixa/cool-ai-stuff (1.2k★) — free AI APIs & sites list.
- https://github.com/ShaikhWarsi/free-ai-tools (1.1k★) — free/low-cost AI tools, LLM APIs, IDEs, agents.
- https://github.com/CelaDaniel/free-ai-resources-x (984★) — free AI tools/APIs/datasets/learning resources.
- https://github.com/nejib1/Free-LLM (892★) — 34+ free LLM APIs, permanent tiers + trial credits, synced.
- https://github.com/OuterSpacee/free-ai-apis (375★) — 100+ free AI APIs across 15 categories (LLM, images, STT, TTS, embeddings…), auth type per API.
- https://github.com/guihuashaoxiang/FreeLLM-API-KeyHub (285★) — China-focused free LLM API resources (Bailian, Zhipu, …) (zh).
- https://github.com/wdhdev/free-for-life (1.8k★) — huge general "free stuff" list (non-AI included).
- https://github.com/YoannDev90/awesome-free-ai-api (120★) — 82 free APIs with test status (feeds §4 relays).
- https://github.com/vava-nessa/free-coding-models (2.9k★) — CLI to find/benchmark/install 170+ FREE coding models across 15+ providers.
- https://github.com/ClawLabsAI/free-ai-models (252★) — daily ranked free-model list w/ live status + routing endpoint.
- https://freellm.net / https://freellmapi.co/models — live catalogs behind the two above lists.

## 7. Other useful finds (adjacent)

- https://github.com/vava-nessa/free-coding-models (2.9k★) — (see lists) benchmark free coding models.
- https://github.com/travisvn/openai-edge-tts (2.1k★) — free high-quality TTS API endpoint (replaces OpenAI/Azure/ElevenLabs).
- https://github.com/simonw/llm (12.7k★) — CLI for LLMs; works with free providers.
- https://github.com/opencode-ai/opencode (13.8k★) — terminal AI coding agent (its `zen` gateway is a free provider itself).
- https://github.com/Shubhamsaboo/awesome-llm-apps (143.7k★) — 100+ free open-source LLM apps (for inspiration, not capacity).
- https://github.com/Hannibal046/Awesome-LLM (27.5k★) — large curated LLM resource list.
- https://github.com/AiHubCN/Awesome-Chinese-LLM (22.8k★) — open Chinese LLMs (zh).
- https://github.com/IntelligenzaArtificiale/Free-Auto-GPT (2.5k★) — Auto-GPT on free APIs.

---

## 8. Recommendations for this router (quick wins)

1. **Keyless, no-auth providers (highest value — zero signup):**
   Pollinations (`https://text.pollinations.ai/openai`), Puter, AI Horde,
   OpenCode Zen, LLM7.io, Kilo Code, Aion Labs, Ollama Cloud — all
   OpenAI-compatible, can be added as `DEFAULT_BASE_URLS` + `KEY_ENVS` entries
   (some take no key at all → `available()` should treat empty-key as OK).
2. **Key-based new free tiers:** ModelScope, SiliconFlow, xAI, DeepSeek,
   Together, Fireworks, Replicate, Nebius, HF router — same pattern as
   existing `DEFAULT_BASE_URLS` entries; caps listed in §4.
3. **Sidecars already speaking the protocol:** OmniRoute, 9router,
   CLIProxyAPI, free-claude-code, one-api/new-api — add as optional upstreams
   next to `freellmapi`/`llmcycle`.
4. **Keyless scraping fallbacks** for when g4f is down:
   Gemini-Free-API variants, FreeQwenApi, ds-free-api, ChatGPTAPIFree,
   free-one-api — all one-file FastAPI/Go services, easily run as sidecars.
5. **Continuous discovery:** subscribe to `mnfst/awesome-free-llm-apis`
   (daily data refresh) + `ClawLabsAI/free-ai-models` (daily status) rather
   than re-researching manually.
