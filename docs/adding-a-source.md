# Adding a new source — the two supported paths

Adding a model/inference source to szmelc-router is **never a core code
change**. You have exactly two paths.

## Path A — it (or a sidecar of it) is OpenAI-compatible → 3 lines of config

Most free tiers (Groq, Google, Cerebras, Together, OpenRouter, DeepSeek, …)
speak OpenAI's `/chat/completions` + `/models`. That's all the router needs:

```yaml
# ~/.config/szmelc/config.yaml (or append in your FREELLM_CONFIG file)
providers:
  - name: mynewsource          # the "provider" label in chains / via
    impl: openailike           # dispatch to the OpenAI-compatible adapter
    base_url: https://api.example.com/v1   # trailing /v1, not /
    key_env: MYNEW_API_KEY     # env var holding the key ("" = keyless)
    kind: keyed                # keyless | keyed | local | sidecar | scraper
    weight: 30                 # tie-break in chain ordering
routes:
  free: [mynewsource/some-model]   # or: auto: [mynewsource/some-model, …]
```

Then:

```bash
export MYNEW_API_KEY=…
szmelc-router config validate          # catches bad fields fast
szmelc-router providers test mynewsource
szmelc-router chat "hi" --model mynewsource/some-model
```

Or the same thing as a one-liner CLI:

```bash
szmelc-router providers add \
  --name mynewsource --impl openailike --kind keyed \
  --base-url https://api.example.com/v1 --key-env MYNEW_API_KEY
```

**Non-standard auth?** `impl: openailike` supports a `header:` field (e.g.
`x-goog-api-key`) and fully keyless bases (no `key_env`). Different wire
protocols → Path B (or a new ~100-line adapter like `ollama`/`g4fbridge`
if you want to upstream it).

## Path B — it's not OpenAI-compatible → any-language plugin (stdio JSON-RPC)

Anything: a scraper CLI, a g4f wrapper, a node service, a shell script.
Write ~50 lines in whatever language you like speaking the stdio JSON-RPC
protocol (spec: [plugin-protocol.md](plugin-protocol.md)), then:

```yaml
providers:
  - name: myscraper
    impl: plugin
    kind: plugin
    command: ["python3", "/opt/myscraper/server.py"]
    env: {SCRAPER_KEY: "${SCRAPER_KEY:-}"}

routes:
  free: [myscraper/page-model]
```

Worked examples:

- `plugins/python-example` — a full reference python plugin (models,
  availability, streaming deltas, shutdown). Zero dependencies,
  `python3 plugin.py`.
- `plugins/go-example` — same protocol via `pkg/pluginhost` (one import).

The router handles spawning, handshake timeouts, crash → cooldown +
restart-with-backoff, stderr → log capture. `probe` and `/v1/health` see it
exactly like any other provider.

## Timeline

- OpenAI-compatible source → **< 5 minutes** (copy the YAML, set the key,
  `providers test`).
- New protocol → **one afternoon** (follow plugin-protocol.md, use a
  reference plugin as a checklist).

That asymmetry is the design goal: the long tail of sources is either already
OpenAI-compatible or fits one small process boundary.
