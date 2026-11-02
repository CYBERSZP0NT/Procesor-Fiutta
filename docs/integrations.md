# Integrations — copy-paste blocks

Point any OpenAI-compatible client at freellm-router. The router listens on
`http://<host>:8787/v1`, speaks exact OpenAI shapes (streaming `SSE` +
non-streaming), and the `model` field accepts a route alias (`auto`, `free`,
…), an exact `provider/model`, or a bare model id.

Base URL used below: `http://localhost:8787/v1`.

## OpenAI — Python SDK

```python
from openai import OpenAI

client = OpenAI(base_url="http://localhost:8787/v1", api_key="x")  # any string if auth.mode=none

# Streaming
stream = client.chat.completions.create(
    model="auto",   # or "free", "groq/llama-3.3-70b-versatile", …
    messages=[{"role": "user", "content": "hello"}],
    stream=True,
)
for chunk in stream:
    print(chunk.choices[0].delta.content or "", end="", flush=True)

# Non-streaming (usage is populated when upstream reports it; -1 otherwise)
resp = client.chat.completions.create(model="free", messages=[{"role": "user", "content": "hi"}])
print(resp.choices[0].message.content, resp.usage)
```

## OpenAI — JS / TS SDK

```ts
import OpenAI from "openai";

const client = new OpenAI({ baseURL: "http://localhost:8787/v1", apiKey: "x" });

// streaming
const s = await client.chat.completions.create({
  model: "auto",
  messages: [{ role: "user", content: "hello" }],
  stream: true,
});
for await (const chunk of s) process.stdout.write(chunk.choices[0]?.delta?.content ?? "");

// sync
const r = await client.chat.completions.create({
  model: "free",
  messages: [{ role: "user", content: "hi" }],
});
console.log(r.choices[0].message.content);
```

## Plain curl

```bash
# Sync
curl -s http://localhost:8787/v1/chat/completions \
  -H 'content-type: application/json' \
  -d '{"model":"free","messages":[{"role":"user","content":"hi"}]}'

# Streaming (SSE; watch chunked text_delta events + [DONE])
curl -sN http://localhost:8787/v1/chat/completions \
  -H 'content-type: application/json' \
  -d '{"model":"auto","stream":true,"messages":[{"role":"user","content":"hi"}]}'

# Other endpoints
curl -s http://localhost:8787/v1/models     # aliases + live per-provider catalog
curl -s http://localhost:8787/v1/health     # per-provider availability (latency, keys, cooldown)
curl -s http://localhost:8787/v1/notes      # free-tier cheat sheet, as JSON
curl -s http://localhost:8787/metrics       # Prometheus text format
```

> With `server.auth.mode: bearer`, add `-H "authorization: Bearer <key>"`.

## Claude Code

```bash
# via a small OpenAI-compat proxy (claude-code-proxy, LiteLLM, or LiteLLM
# pass-through — any of these work) pointing upstream at the router
export ANTHROPIC_BASE_URL=https://your-litellm-proxy/
export ANTHROPIC_API_KEY=<proxy-credential>

# then
claude --model free       # the provider-agnostic alias travels through
```

Simplest zero-proxy route: run LiteLLM ([below](#litellm)) in front of
`/v1` and have Claude Code talk to it.

## OpenCode / pi (OpenAI-compatible harnesses)

Any harness that can be pointed at a custom OpenAI base URL takes:

```jsonc
// e.g. .opencode/config.jsonc or the harness's provider config
{
  "provider": {
    "name": "freellm",
    "base_url": "http://localhost:8787/v1",
    "api_key": "x",
    "models": ["auto", "free", "code"]   // route aliases behave as model ids
  }
}
```

`pi`: `pi serve`/agent config → add a model provider with `base_url` =
`http://localhost:8787/v1`, `api_key: x`, and use `free`/`auto` as the model
id. (See your harness's provider docs — the router side needs nothing special.)

## Open WebUI

1. Admin panel → **Settings → Pipelines / Custom Models** (or
   **Connections → OpenAI API** in newer builds).
2. OpenAI API Base URL: `http://localhost:8787/v1` (use the router's
   host:port — Open WebUI is usually in a container, so use the docker
   network name).
3. API Key: `x` (or your bearer key).
4. Custom models: `auto`, `free`, `code`, … — Open WebUI will list them; set
   context length generously (upstream decides).

## LibreChat

`config.yaml` (LibreChat):

```yaml
endpoints:
  custom:
    - name: Free LLM Router
      baseUrl: http://localhost:8787/v1
      apiKey: x
      models:
        default: [auto, free, code]
        fetch: true      # pulls /v1/models
```

## LiteLLM front (for Anthropic-shaped clients, Bedrock, etc.)

`litellm_config.yaml`:

```yaml
model_list:
  - model_name: auto
    litellm_params: {model: openai/auto, api_base: http://localhost:8787/v1, api_key: x}
  - model_name: free
    litellm_params: {model: openai/free, api_base: http://localhost:8787/v1, api_key: x}
```

## MCP server

freellm-router exposes an OpenAI endpoint, not MCP. Two clean ways to bridge
for MCP clients (Zed, Claude, VS Code MCP) — either one is ~10 lines:

**Option 1 (recommended): a tiny MCP shim** wrapping the router:

```python
# mcp-freellm.py — one tool: chat(model, messages) → text. stdio MCP server.
import json, sys, urllib.request

RO = "http://localhost:8787/v1/chat/completions"

def tool_chat(args):
    body = json.dumps({
        "model": args.get("model", "free"),
        "messages": args["messages"],
    }).encode()
    req = urllib.request.Request(RO, data=body,
                                 headers={"content-type": "application/json"})
    with urllib.request.urlopen(req) as r:
        return {"content": [{"type": "text",
                             "text": json.load(r)["choices"][0]["message"]["content"]}]}

# …(wrap in your MCP framework of choice; the HTTP call above is the whole
#  integration surface — sync, non-streaming works everywhere)
```

**Option 2:** run the router behind any generic "OpenAI-as-MCP" adapter
(`openai-mcp`, etc.) with `base_url=http://localhost:8787/v1` and expose model
ids `free`/`auto`.

## Go (embed the core, no server)

```go
import "github.com/silverx/freellm-router/pkg/router"

r, _ := router.New(router.Config{
    Providers: []router.ProviderConfig{
        {Name: "groq", BaseURL: "https://api.groq.com/openai/v1", KeyEnv: "GROQ_API_KEY", Kind: "keyed"},
        {Name: "poll", BaseURL: "https://text.pollinations.ai/openai", Kind: "keyless"},
    },
    Routes: map[string][]string{"auto": {"groq/llama-3.3-70b-versatile", "poll/llama"}},
})

var out string
via, done, err := r.Route(ctx, "auto",
    []router.Message{{Role: "user", Content: "Say hi."}}, "",
    func(s string) { out += s })
// via  = "groq/llama-3.3-70b-versatile" (the provider that served)
// done = finish info + token counts when the upstream reports them
// (full runnable version: example/embed.go)
```
