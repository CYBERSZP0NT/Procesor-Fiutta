# FREE-MODELS.md — keyless only (no Ollama, no Google/OpenRouter keys)

Everything below works with **zero API keys and zero local models**:
g4f (gpt4free) scrapes the corresponding provider's public frontend
(Perplexity, Meta AI, Groq web, HuggingFace, etc.) on your behalf.

**Count: 76 chat models**, requested as `g4f/<id>`.
Excluded from the count: 7 g4f image models (`dall-e-3`, `flux`, `flux-pro`,
`flux-kontext`, `gpt-image`, `sd-3.5-large`, `sdxl-turbo`), 1 TTS
(`gpt-4o-mini-tts`), and `agent-tools` (tool provider, not a model) —
those still work and are still keyless.

**Conditions** — same for every row: free, no account, no key, but
best-effort: per-IP rate limits, occasional upstream outages, and g4f may
need a fresh `pip install -U g4f` now and then. Fine as fallback and
hobby volume, not production traffic. (Several ids *also* exist under your
Google/OpenRouter keys in MODELS.md; the `g4f/` routes here are the
key-free copies of the same models.)

| id | id | id | id |
|---|---|---|---|
| aria | command-a | command-r | command-r-plus |
| command-r7b | deepseek-r1 | deepseek-r1-distill-llama-70b | deepseek-r1-distill-qwen-1.5b |
| deepseek-r1-distill-qwen-14b | deepseek-v3 | gemini-2.5-flash | gemini-2.5-pro |
| gemini-3-pro-preview | gemini-3.1-flash-lite | gemini-3.1-pro | gemini-3.5-flash |
| gemini-3.5-flash-lite | gemini-3.5-flash-thinking | gemini-3.5-flash-thinking-lite | gemini-3.6-flash |
| gemini-3.7-flash | gemini-3.8-flash | gemini-3.8-pro | gemini-auto |
| gemini-flash-lite | gpt-4 | gpt-4.1 | gpt-4.1-mini |
| gpt-4.1-nano | gpt-4.5 | gpt-4o | gpt-4o-mini |
| gpt-oss-120b | grok-2 | grok-3 | grok-3-r1 |
| hermes-2-dpo | kimi-k2 | llama-2-70b | llama-2-7b |
| llama-3-70b | llama-3-8b | llama-3.1-405b | llama-3.1-70b |
| llama-3.1-8b | llama-3.2-11b | llama-3.2-1b | llama-3.2-3b |
| llama-3.2-90b | llama-3.3-70b | llama-4-maverick | llama-4-scout |
| meta-ai | mistral-7b | mistral-nemo | mistral-small-24b |
| mistral-small-3.1-24b | mixtral-8x7b | nemotron-70b | o1 |
| o1-mini | o3-mini | o3-mini-high | o4-mini |
| o4-mini-high | phi-3.5-mini | qwen-2.5-coder-32b | qwen-2.5-vl-72b |
| qwen-3-235b | qwen-3-32b | qwq-32b | r1-1776 |
| sonar | sonar-pro | sonar-reasoning | sonar-reasoning-pro |
