# free-llm-router

One small Python router in front of **all** your free LLM capacity: vendor
free tiers (Google, Groq, OpenRouter, Cerebras, …), the keyless **g4f**
(gpt4free) endpoints, and local **Ollama** models. It exposes a standard
**OpenAI-compatible API**, so you can point *anything* — an OpenAI SDK, a Pi
harness, Claude Code, etc. — at it.

`freellm_router.py` (only dep: `httpx`; g4f and Ollama auto-detected) will:

1. Take a model name — a route alias like `auto`/`fast`/`free`, or an exact
   `provider/model` like `google/gemini-2.5-flash` or `g4f/gpt-4o-mini`.
2. Try the backends in that chain in order, skipping anything with no key or
   on cooldown, so a 429 on Google just silently continues to Groq.
3. Stream the answer back as normal OpenAI SSE.

## Setup

```bash
pip install httpx          # required
pip install g4f            # keyless fallback backends (installed)
cp .env.example .env       # then fill in the free keys you have (all optional)
```

## Use

```bash
python freellm_router.py chat "hello" --model auto      # one-shot, streams
python freellm_router.py models                        # live model list
python freellm_router.py probe                         # test every backend
python freellm_router.py serve --port 8787             # OpenAI server
```

```python
# from anything else (Python)
from openai import OpenAI
c = OpenAI(base_url="http://localhost:8787/v1", api_key="x")
c.chat.completions.create(model="auto", messages=[{"role":"user","content":"hi"}])
```

Route aliases: `auto` (balanced), `smart` (best quality), `fast`
(small+quick), `code` (coding), `free` (keyless: g4f + ollama only),
`local` (ollama first). All names/models and the free-tier conditions for
each are in **[MODELS.md](MODELS.md)**.
