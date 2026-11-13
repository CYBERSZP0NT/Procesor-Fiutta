#!/usr/bin/env python3
# =============================================================================
# LEGACY — DEPRECATED (freellm-router v1.0.0-rc1, 2026-11-02)
#
# This file is kept ONLY as a behavioral reference. The live implementation
# is the Go binary `szmelc-router` (see README.md). If you are reading this
# for how something behaves, it is still a decent spec — but run the Go
# router, not this script.
#
#   make install          # or: curl installer / docker compose (see README)
#   szmelc-router serve   # OpenAI-compatible API on :8787
# =============================================================================
"""
freellm_router.py — one router over your entire free-LLM estate.
=================================================================

Integrates:
  1. Free TIER  — key-based free endpoints (OpenAI-compatible):
       Google AI Studio, Groq, Cerebras, SambaNova, Mistral, Z.ai,
       OpenRouter ":free" models, NVIDIA NIM, GitHub Models, Cloudflare,
       Cohere.  Free to use within each vendor's published free-tier
       limits (see FREE_TIER_NOTES below / README.md).
  2. gpt4free (g4f) — keyless/scraped endpoints.  Free, no account
       needed, but rate-limited and occasionally flaky (community
       project scraping public chat UIs — fine for low-volume use).
  3. ollama — local models, free & unlimited while the box is on.
  4. Aggregators as optional upstreams — if you run freellmapi
     (tashfeenahmed's, 34 providers) or llmcycle as a sidecar server,
     point this router at them like any other OpenAI-compatible node.

Surface (pick what you need):
  * OpenAI-compatible HTTP server  -> http://host:PORT/v1  (point any
    OpenAI SDK, Claude Code, OpenCode, a Pi "harness", etc. here)
  * Python API                    -> from freellm_router import chat, models
  * CLI                           -> python freellm_router.py chat "hi"

Dependencies:  httpx is the only hard dep.  `g4f` and `ollama` backends
are optional and auto-detected.

    pip install httpx
    pip install g4f          # optional: enables keyless "g4f:..." models

Run:
    python freellm_router.py serve --port 8787
    python freellm_router.py chat "explain RAG in one line" --model auto
    python freellm_router.py models
    python freellm_router.py probe        # test every configured backend
"""

from __future__ import annotations

import argparse
import asyncio
import json
import os
import sys
import threading
import time
import uuid
from dataclasses import dataclass
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from typing import Any, AsyncIterator, Dict, List, Optional, Tuple

try:
    import httpx
except ImportError:
    print("httpx is required:  pip install httpx", file=sys.stderr)
    raise

# --------------------------------------------------------------------------
# Free-tier quick reference (the "what's free, under what conditions")
# --------------------------------------------------------------------------
FREE_TIER_NOTES = {
    "google":    "Gemini free tier via AI Studio key: ~10-15 req/min, "
                 "~250 req/day (varies by model).  Free while model stays in free tier.",
    "groq":      "Free tier: 14,400 req/day, 1M tokens/day (per-model limits).",
    "cerebras":  "Free developer tier: ~3,000 req/day.",
    "sambanova": "Free tier: 1,000 req/min, 750M tokens/month.",
    "mistral":   "La Plateforme free tier: 500k tokens/month per model.",
    "zai":       "Z.ai (GLM) free tier: daily request caps (RPM-limited).",
    "openrouter":"Free ':free' models: 50 req/day, 1,000 req/month "
                 "(2,000/mo once you have $10 lifetime credits).",
    "nvidia":    "NVIDIA NIM: 1,000 free requests per model (build.nvidia.com).",
    "github":    "GitHub Models: 15 req/min, 150 req/day free (free GitHub account).",
    "cloudflare":"Workers AI free: 10,000 neurons/day.",
    "cohere":    "Free trial key: 10 req/min, 100 req/day.",
    "g4f":       "Keyless scraped/public endpoints (gpt4free). Free, no account, "
                 "but rate-limited & best-effort; not for production traffic.",
    "ollama":    "Local inference. Unlimited while running; costs electricity only.",
    "aggregator":"A sidecar router (freellmapi or llmcycle) you run yourself; "
                 "its limits are the sum of your keys.",
}

# --------------------------------------------------------------------------
# Backends
# --------------------------------------------------------------------------

@dataclass
class Backend:
    """One routable (provider, model) target."""
    provider: str            # google | groq | g4f | ollama | openrouter | custom...
    model: str               # concrete model id sent upstream
    label: str = ""          # human label, e.g. "google/gemini-2.5-flash"
    base_url: str = ""       # OpenAI-compatible base url (for keyless providers)
    key: str = ""            # api key (empty ok for free/g4f/ollama)
    timeout: float = 120.0

    def __post_init__(self):
        if not self.label:
            self.label = f"{self.provider}/{self.model}"

    async def stream(self, messages: List[dict], **kw) -> AsyncIterator[dict]:
        """Yield {"type":"text","text":...} then {"type":"done","full":str}."""
        if self.provider == "g4f":
            async for ev in self._g4f(messages, kw.get("max_tokens")):
                yield ev
        else:
            async for ev in self._openai(messages, kw):
                yield ev

    async def _openai(self, messages: List[dict], kw: dict) -> AsyncIterator[dict]:
        base = (self.base_url or DEFAULT_BASE_URLS.get(self.provider, "")).rstrip("/")
        if not base:
            raise RuntimeError(f"no base_url known for provider '{self.provider}'")
        payload: dict = {"model": self.model, "messages": messages, "stream": True}
        for key in ("max_tokens", "temperature", "top_p", "tools", "tool_choice"):
            if kw.get(key) is not None:
                payload[key] = kw[key]
        headers = {"Content-Type": "application/json"}
        if self.key:
            headers["Authorization"] = f"Bearer {self.key}"
        if self.provider == "google":
            headers["x-goog-api-key"] = self.key
        full = ""
        async with httpx.AsyncClient(timeout=httpx.Timeout(self.timeout)) as client:
            async with client.stream("POST", f"{base}/chat/completions",
                                     headers=headers, json=payload) as r:
                if r.status_code != 200:
                    body = (await r.aread()).decode()[:400]
                    raise UpstreamError(r.status_code, self.label, body)
                async for line in r.aiter_lines():
                    if not line or not line.startswith("data:"):
                        continue
                    data = line[5:].strip()
                    if data == "[DONE]":
                        break
                    try:
                        chunk = json.loads(data)
                    except json.JSONDecodeError:
                        continue
                    choices = chunk.get("choices") or []
                    if not choices:
                        continue
                    delta = choices[0].get("delta") or {}
                    if choices[0].get("finish_reason") == "tool_calls":
                        yield {"type": "tool_calls", "data": choices[0]}
                    piece = delta.get("content") or delta.get("reasoning") or ""
                    if piece:
                        full += piece
                        yield {"type": "text", "text": piece}
        yield {"type": "done", "full": full}

    async def _g4f(self, messages: List[dict],
                   max_tokens: Optional[int]) -> AsyncIterator[dict]:
        try:
            import g4f  # type: ignore
        except ImportError:
            raise RuntimeError(
                "`g4f` backend used but gpt4free is not installed: pip install g4f")
        _g4f_browser_setup()
        full = ""
        try:
            result = g4f.ChatCompletion.create(
                model=self.model, messages=messages,
                max_tokens=max_tokens, streaming=True)
            if isinstance(result, str):
                # some providers collapse the stream to a final string
                if result:
                    full = result
                    yield {"type": "text", "text": result}
            else:
                for chunk in result:
                    text = getattr(chunk, "message", None) or ""
                    if text:
                        full += text
                        yield {"type": "text", "text": text}
        except UpstreamError:
            raise
        except RuntimeError as e:
            # g4f raises RuntimeError("No working providers...")
            raise UpstreamError(503, self.label, str(e)[:400])
        if not full:
            raise UpstreamError(503, self.label, "no text produced by g4f")
        yield {"type": "done", "full": full}


class UpstreamError(Exception):
    def __init__(self, status: int, label: str, body: str):
        super().__init__(f"[{label}] HTTP {status}: {body}")
        self.status, self.label, self.body = status, label, body


# --------------------------------------------------------------------------
# Router: aliases -> fallback chains, key rotation, cooldowns
# --------------------------------------------------------------------------

DEFAULT_BASE_URLS: Dict[str, str] = {
    "openai":      "https://api.openai.com/v1",
    "google":      "https://generativelanguage.googleapis.com/v1beta/openai",
    "groq":        "https://api.groq.com/openai/v1",
    "cerebras":    "https://api.cerebras.ai/v1",
    "sambanova":   "https://api.sambanova.ai/v1",
    "mistral":     "https://api.mistral.ai/v1",
    "zai":         "https://open.bigmodel.cn/api/paas/v4",
    "openrouter":  "https://openrouter.ai/api/v1",
    "nvidia":      "https://integrate.api.nvidia.com/v1",
    "github_models":"https://models.inference.ai.azure.com",
    "cohere":      "https://api.cohere.com/v1",
    "ollama":      os.environ.get("OLLAMA_BASE_URL", "http://localhost:11434/v1"),
    "llmcycle":    os.environ.get("LLMCYCLE_URL", "http://localhost:8000"),
    "freellmapi":  os.environ.get("FREELLMAPI_URL", "http://localhost:5174"),
}

# Route names you can request. Order inside a list = fallback priority.
DEFAULT_ALIASES: Dict[str, List[Tuple[str, str]]] = {
    "auto": [
        ("google",     "gemini-2.5-flash"),
        ("groq",       "llama-3.3-70b-versatile"),
        ("openrouter", "deepseek/deepseek-r1:free"),
        ("g4f",        "gpt-4o-mini"),
        ("ollama",     "llama3.2:3b"),
    ],
    "smart": [
        ("google",     "gemini-2.5-pro"),
        ("openrouter", "meta-llama/llama-3.3-70b-instruct:free"),
        ("groq",       "llama-3.3-70b-versatile"),
        ("g4f",        "o3-mini"),
        ("ollama",     "llama3.2:3b"),
    ],
    "fast": [
        ("groq",       "llama-3.1-8b-instant"),
        ("google",     "gemini-2.5-flash-lite"),
        ("cerebras",   "llama3.1-8b"),
        ("openrouter", "meta-llama/llama-3.1-8b-instruct:free"),
        ("g4f",        "gpt-4o-mini"),
        ("ollama",     "llama3.2:1b"),
    ],
    "code": [
        ("groq",       "llama-3.3-70b-versatile"),
        ("openrouter", "qwen/qwen-2.5-coder-32b-instruct:free"),
        ("g4f",        "gpt-4o"),
        ("ollama",     "qwen2.5-coder:7b"),
    ],
    "local": [
        ("ollama", "llama3.2:3b"),
        ("g4f",    "gpt-4o-mini"),
    ],
    "free": [  # pure keyless: no accounts, no keys
        ("g4f",    "gpt-4o-mini"),
        ("g4f",    "meta-llama/Meta-Llama-3.1-8B-Instruct"),
        ("ollama", "llama3.2:3b"),
    ],
    "g4f":    [("g4f", "gpt-4o-mini")],
    "ollama": [("ollama", "llama3.2:3b")],
}

# provider -> env vars holding its key(s); comma-separate for multi-key rotation
KEY_ENVS: Dict[str, str] = {
    "google":        "GOOGLE_API_KEY|GEMINI_API_KEY",
    "groq":          "GROQ_API_KEY",
    "cerebras":      "CEREBRAS_API_KEY",
    "sambanova":     "SAMBANOVA_API_KEY",
    "mistral":       "MISTRAL_API_KEY",
    "zai":           "ZAI_API_KEY|ZHIPU_API_KEY",
    "openrouter":    "OPENROUTER_API_KEY",
    "nvidia":        "NVIDIA_API_KEY",
    "github_models": "GITHUB_TOKEN",
    "cohere":        "COHERE_API_KEY",
    "llmcycle":      "LLMCYCLE_API_KEY",
    "freellmapi":    "FREELLMAPI_API_KEY",
}

_lock = threading.Lock()


def load_dotenv(path: Path) -> None:
    if not path.exists():
        return
    for line in path.read_text().splitlines():
        line = line.strip()
        if not line or line.startswith("#") or "=" not in line:
            continue
        k, _, v = line.partition("=")
        os.environ.setdefault(k.strip(), v.strip().strip('"').strip("'"))


class Router:
    """Alias -> chain-of-backends with round-robin keys and 429/5xx cooldowns."""

    def __init__(self, config: Optional[dict] = None):
        config = config or {}
        self.aliases: Dict[str, List[Tuple[str, str]]] = dict(DEFAULT_ALIASES)
        for name, chain in (config.get("aliases") or {}).items():
            if isinstance(chain, list):
                self.aliases[name] = [(c[0], c[1]) for c in chain if isinstance(c, (list, tuple)) and len(c) == 2]
        # "providers": [["provider/model", "base_url", "key"], ...] for anything
        # not built-in (custom OpenAI-compatible endpoints, self-hosted, etc.)
        self.custom: Dict[str, Backend] = {}
        for b in config.get("providers") or []:
            label, base, key = (list(b) + ["", ""])[:3]
            if "/" in label:
                provider, model = label.split("/", 1)
            else:
                provider, model = "custom", label
            self.custom[label] = Backend(provider=provider, model=model,
                                         base_url=base, key=key)
        self._cooldown: Dict[str, float] = {}
        self._rr: Dict[str, int] = {}

    @staticmethod
    def _keys_for(provider: str) -> List[str]:
        keys: List[str] = []
        for var in KEY_ENVS.get(provider, "").split("|"):
            keys.extend(k.strip() for k in os.environ.get(var, "").split(",") if k.strip())
        return keys

    def _pick_key(self, provider: str) -> str:
        keys = self._keys_for(provider)
        if not keys:
            return ""
        with _lock:
            i = self._rr.get(provider, 0)
            self._rr[provider] = (i + 1) % len(keys)
        return keys[i]

    def _backend(self, provider: str, model: str) -> Backend:
        provider = provider.lower()
        if f"{provider}/{model}" in self.custom:
            return self.custom[f"{provider}/{model}"]
        return Backend(provider=provider, model=model, key=self._pick_key(provider))

    def resolve(self, model: str) -> List[Backend]:
        """Expand a requested model string into an ordered backend chain."""
        name = model.strip()
        if name in self.aliases:
            chain = self.aliases[name]
        elif name in self.custom:
            return [self.custom[name]]
        elif "/" in name:                       # explicit "provider/model"
            provider, m = name.split("/", 1)
            chain = [(provider.lower(), m)]
            # add the generic auto chain as a safety net
            chain += [c for c in self.aliases.get("auto", []) if c not in chain]
        elif name in DEFAULT_BASE_URLS:         # bare provider name
            chain = list(self.aliases.get("auto", []))
        else:
            # bare model name: try the auto chain, then g4f for that model
            chain = list(self.aliases.get("auto", [])) + [("g4f", name)]
        out = []
        for provider, m in chain:
            out.append(self._backend(provider, m))
        # dedupe preserving order
        seen, uniq = set(), []
        for b in out:
            if b.label not in seen:
                seen.add(b.label)
                uniq.append(b)
        return uniq

    def available(self, b: Backend) -> bool:
        if time.time() < self._cooldown.get(b.label, 0):
            return False
        if b.provider == "g4f":
            return _g4f_installed()
        if b.provider == "ollama":
            return _ollama_alive()
        if b.provider in ("freellmapi", "llmcycle"):
            # no key env required; reachable check happens at request time
            return True
        return bool(self._keys_for(b.provider)) or bool(b.base_url)

    def set_cooldown(self, label: str, seconds: int) -> None:
        with _lock:
            self._cooldown[label] = time.time() + seconds

    async def complete(self, model: str, messages: List[dict],
                       on_text=None, **kw) -> Tuple[str, str]:
        """Walk the fallback chain until one works. Returns (text, label)."""
        backends = [b for b in self.resolve(model) if self.available(b)]
        if not backends:
            raise UpstreamError(503, model,
                                "no available backends — add keys to .env, "
                                "install g4f, or use a keyless route like 'free'")
        last: Optional[Exception] = None
        for b in backends:
            try:
                full = ""
                async for ev in b.stream(messages, **kw):
                    if ev["type"] == "text" and ev["text"]:
                        full += ev["text"]
                        if on_text:
                            on_text(ev["text"])
                if not full:
                    raise UpstreamError(599, b.label, "empty response")
                return full, b.label
            except (UpstreamError, httpx.HTTPError, RuntimeError) as e:
                last = e
                status = getattr(e, "status", None)
                if status is not None and 400 <= status < 500 and status not in (408, 429):
                    self.set_cooldown(b.label, 3600)   # auth/config problem — back off hard
                else:
                    self.set_cooldown(b.label, 30)     # rate limit / transient — brief back-off
                continue
        raise UpstreamError(503, model, f"all backends failed; last error: {last}")


# --------------------------------------------------------------------------
# Helpers / module-level state
# --------------------------------------------------------------------------

DEFAULT_ROUTER: Optional[Router] = None


def get_router(config_path: Optional[str] = None) -> Router:
    global DEFAULT_ROUTER
    if DEFAULT_ROUTER is None:
        cfg: dict = {}
        load_dotenv(Path(__file__).with_name(".env"))
        p = Path(config_path or os.environ.get(
            "FREELLM_CONFIG", Path(__file__).with_name("router_config.json")))
        if p.exists():
            try:
                cfg = json.loads(p.read_text())
            except json.JSONDecodeError as e:
                print(f"warning: bad config {p}: {e}", file=sys.stderr)
        DEFAULT_ROUTER = Router(cfg)
    return DEFAULT_ROUTER


def _g4f_installed() -> bool:
    try:
        import g4f  # type: ignore
        return True
    except ImportError:
        return False


def _g4f_browser_setup() -> None:
    """Point g4f at a working browser (env G4F_BROWSER_PATH takes priority).
    Chrome's CDP providers fall back to Chromium when the system
    google-chrome binary is incomplete."""
    import g4f  # type: ignore
    import g4f.requests as greq  # type: ignore
    for cand in [os.environ.get("G4F_BROWSER_PATH") or "",
                 "/usr/bin/chromium", "/usr/bin/chromium-browser",
                 "/usr/bin/google-chrome", "/usr/bin/google-chrome-stable"]:
        if cand and os.path.exists(cand):
            try:
                greq.set_browser_executable_path(cand)  # type: ignore[attr-defined]
            except Exception:
                pass
            return


def _g4f_models() -> List[str]:
    if not _g4f_installed():
        return []
    try:
        from g4f.models import ModelRegistry
        names = [getattr(m, "name", None)
                 for m in ModelRegistry.all_models().values()]
        return sorted(n for n in names if n)
    except Exception:
        return []


def _ollama_base() -> str:
    return os.environ.get("OLLAMA_BASE_URL", "http://localhost:11434").rstrip("/")


def _ollama_alive() -> bool:
    try:
        return httpx.get(f"{_ollama_base()}/api/tags", timeout=2.0).status_code == 200
    except Exception:
        return False


def _ollama_models() -> List[str]:
    try:
        data = httpx.get(f"{_ollama_base()}/api/tags", timeout=3.0).json()
        return [m["name"] for m in data.get("models", [])]
    except Exception:
        return []


# --------------------------------------------------------------------------
# Public Python API
# --------------------------------------------------------------------------

def chat(prompt: str, model: str = "auto", system: str = "", stream: bool = True, **kw) -> str:
    """One-shot. Prints streamed text to stdout when stream=True. Returns full text."""
    msgs: List[dict] = []
    if system:
        msgs.append({"role": "system", "content": system})
    msgs.append({"role": "user", "content": prompt})
    r = get_router()
    full, _ = asyncio.run(r.complete(model, msgs,
                                     on_text=(lambda t: print(t, end="", flush=True)
                                              if stream else None), **kw))
    if stream:
        print()
    return full


def models() -> List[str]:
    """List route names plus live upstream model ids (for configured keys)."""
    out: List[str] = []
    r = get_router()
    out.append(f"# route aliases: {', '.join(sorted(r.aliases))}")
    for label, b in sorted(r.custom.items()):
        out.append(f"# custom provider: {label}")
    for provider in sorted(DEFAULT_BASE_URLS):
        keys = r._keys_for(provider)
        if not keys:
            continue
        base = DEFAULT_BASE_URLS[provider].rstrip("/")
        try:
            resp = httpx.get(f"{base}/models",
                             headers={"Authorization": f"Bearer {keys[0]}"}, timeout=8.0)
            if resp.status_code == 200:
                ids = resp.json()
                data = ids.get("data") or ids.get("models") or []
                for m in data:
                    mid = m.get("id", m) if isinstance(m, dict) else m
                    out.append(f"{provider}/{mid}")
        except Exception:
            out.append(f"# {provider}: /models fetch failed (still routable)")
    if _g4f_installed():
        out.append(f"# g4f available; models: {', '.join(_g4f_models()[:25])}...")
    if _ollama_alive():
        out.append(f"# ollama running; models: {', '.join(_ollama_models())}")
    return out


# --------------------------------------------------------------------------
# OpenAI-compatible HTTP server  (point any OpenAI client / Pi harness here)
# --------------------------------------------------------------------------

class _Handler(BaseHTTPRequestHandler):
    router: Router = None  # type: ignore  # set in serve()

    def log_message(self, *a):
        pass

    def _json(self, code: int, obj: dict):
        body = json.dumps(obj).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        path = self.path.rstrip("/")
        if path in ("/v1/models", "/models"):
            data = [{"id": n, "object": "model", "owned_by": "freellm-router"}
                    for n in sorted(self.router.aliases)]
            for prefix, ms in (("g4f", _g4f_models()), ("ollama", _ollama_models())):
                data += [{"id": f"{prefix}/{m}", "object": "model",
                          "owned_by": "freellm-router"} for m in ms]
            self._json(200, {"object": "list", "data": data})
        elif path in ("/v1/health", "/health"):
            self._json(200, {"ok": True,
                             "ollama": _ollama_alive(),
                             "g4f": _g4f_installed(),
                             "keys": {p: bool(self.router._keys_for(p)) for p in KEY_ENVS}})
        else:
            self._json(404, {"error": "not found"})

    def do_POST(self):
        if not self.path.rstrip("/").endswith("/chat/completions"):
            return self._json(404, {"error": "not found"})
        try:
            body = json.loads(self.rfile.read(int(self.headers.get("Content-Length", 0))) or b"{}")
        except json.JSONDecodeError:
            return self._json(400, {"error": "bad json"})

        model = body.get("model") or "auto"
        messages: List[dict] = body.get("messages") or []
        kw = {k: body[k] for k in ("max_tokens", "temperature", "top_p") if k in body}
        if isinstance(body.get("tools"), list):
            kw["tools"] = body["tools"]
        if body.get("tool_choice") is not None:
            kw["tool_choice"] = body["tool_choice"]
        stream = bool(body.get("stream"))
        r = self.router

        if stream:
            self.send_response(200)
            self.send_header("Content-Type", "text/event-stream")
            self.send_header("Cache-Control", "no-cache")
            self.end_headers()
            cid = f"chatcmpl-{uuid.uuid4().hex[:24]}"
            started = int(time.time())

            def emit(delta: dict, finish: Optional[str] = None):
                payload = {"id": cid, "object": "chat.completion.chunk",
                           "created": started, "model": model,
                           "choices": [{"index": 0, "delta": delta,
                                        "finish_reason": finish}]}
                try:
                    self.wfile.write(b"data: " + json.dumps(payload).encode() + b"\n\n")
                    self.wfile.flush()
                except Exception:
                    pass

            def on_text(t: str):
                emit({"role": "assistant", "content": t})

            try:
                full, _v = asyncio.run(r.complete(model, messages, on_text=on_text, **kw))
                if not full:
                    emit({"content": full})
                emit({}, finish="stop")
                self.wfile.write(b"data: [DONE]\n\n")
            except UpstreamError as e:
                self.wfile.write(b"data: " + json.dumps({"error": str(e)}).encode() + b"\n\n")
                self.wfile.write(b"data: [DONE]\n\n")
            return

        try:
            full, label = asyncio.run(r.complete(model, messages, None, **kw))
        except UpstreamError as e:
            return self._json(502, {"error": {"message": str(e), "type": "upstream_error"}})
        self._json(200, {
            "id": f"chatcmpl-{uuid.uuid4().hex[:24]}",
            "object": "chat.completion", "created": int(time.time()),
            "model": model,
            "choices": [{"index": 0,
                         "message": {"role": "assistant", "content": full},
                         "finish_reason": "stop"}],
            "usage": {"prompt_tokens": -1, "completion_tokens": -1, "total_tokens": -1},
            "_routed_via": label,
        })


def serve(port: int = 8787, host: str = "0.0.0.0"):
    r = get_router()
    Handler = type("BoundHandler", (_Handler,), {"router": r})
    httpd = ThreadingHTTPServer((host, port), Handler)
    print(f"freellm-router listening on http://{host}:{port}")
    print(f"  OpenAI base_url -> http://{host}:{port}/v1   (model: auto|smart|fast|code|free|...)")
    print(f"  keys configured: {sorted(p for p in KEY_ENVS if r._keys_for(p)) or 'none'}")
    print(f"  g4f: {'installed' if _g4f_installed() else 'not installed (pip install g4f for keyless)'}")
    print(f"  ollama: {', '.join(_ollama_models()) if _ollama_alive() else 'not reachable'}")
    try:
        httpd.serve_forever()
    except KeyboardInterrupt:
        pass


# --------------------------------------------------------------------------
# CLI
# --------------------------------------------------------------------------

def main():
    ap = argparse.ArgumentParser(
        description="freellm-router: one router over free LLM endpoints.",
        formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--config", help="path to router_config.json")
    sub = ap.add_subparsers(dest="cmd")

    c = sub.add_parser("chat", help="one-shot chat (streams to stdout)")
    c.add_argument("prompt")
    c.add_argument("--model", default="auto")
    c.add_argument("--system", default="")
    c.add_argument("--no-stream", action="store_true")
    c.add_argument("--max-tokens", type=int)

    sub.add_parser("models", help="list route aliases + live upstream models")
    sub.add_parser("probe", help="test every backend in the default chains")
    s = sub.add_parser("serve", help="run the OpenAI-compatible server")
    s.add_argument("--port", type=int, default=8787)
    s.add_argument("--host", default="0.0.0.0")

    args = ap.parse_args()
    if args.config:
        os.environ["FREELLM_CONFIG"] = args.config
    if not args.cmd:
        print("freellm-router — commands: chat | models | probe | serve")
        print("\nfree-tier notes (what's free & under what conditions):")
        for k, v in FREE_TIER_NOTES.items():
            print(f"  {k:11s} {v}")
        return

    if args.cmd == "chat":
        return chat(args.prompt, args.model, args.system,
                    stream=not args.no_stream,
                    **({"max_tokens": args.max_tokens} if args.max_tokens else {}))

    if args.cmd == "models":
        for line in models():
            print(line)
        return

    if args.cmd == "probe":
        r = get_router()
        seen = set()
        for alias in sorted(r.aliases):
            for provider, m in r.aliases[alias]:
                b = r._backend(provider, m)
                if b.label in seen:
                    continue
                seen.add(b.label)
                if not r.available(b):
                    print(f"  skip  {b.label:55s} (no key installed / optional dep missing)")
                    continue
                t0 = time.time()
                try:
                    full, _ = asyncio.run(r.complete(
                        b.label, [{"role": "user", "content": "Reply with exactly: pong"}]))
                    print(f"  ok    {b.label:55s} {time.time()-t0:5.1f}s  {full.strip()[:40]!r}")
                    r.set_cooldown(b.label, 0)
                except Exception as e:
                    print(f"  fail  {b.label:55s} {str(e)[:110]}")
        return

    if args.cmd == "serve":
        return serve(args.port, args.host)


if __name__ == "__main__":
    main()
