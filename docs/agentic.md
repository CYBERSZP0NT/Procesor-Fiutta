# Agentic task router

`model: "agent"` (or `"task"`) asks freellm-router to **pick the model per
request**. A fixed alias such as `auto` always walks the same chain. The task
router reads what the request asks for and sends it to the model that is
*just enough* for it. Tiny models label and extract. Mid models chat and
summarize. Strong models code, review and plan. The strongest ones handle only
the hardest tasks.

It decides by readable rules, not by asking a model. Every pick explains
itself, so you never need to guess why a request went where it did.

## Modes

| Mode | id | What it does |
|---|---|---|
| **[Rozkład Jazdy]** (default) | `rozklad-jazdy` | All in, whatever the task. The strongest available model goes first; among equals, the biggest context. Cost, speed and the tier caps of rules (`max_tier`, `min_tier`) are ignored, and every eligible model is in the chain, strongest first. |
| **Jako Tako** | `jako-tako` | Just enough. The cheapest model that reaches the tier the task needs goes first, stronger ones follow as escalation, weaker ones as a best effort. |

The hard filters apply in both modes, because they are physical limits rather
than thrift: tool calling, context size, `local`, and availability. In
[Rozkład Jazdy] a rejected non-stream answer moves on to the next strongest
model.

`[Rozkład Jazdy]` is the default everywhere: the embedded defaults, an empty
spec, the Go library and the server. It stays the default until you change it.
Ways to change it, from the narrowest scope to the widest:

| Scope | How |
|---|---|
| one request | `"model": "agent:jako-tako"`, header `X-Freellm-Task-Mode: Jako Tako`, `metadata.task_mode`, or `chat --mode` / `route --mode` |
| running server | `curl -X PUT localhost:8787/v1/route/mode -d '{"mode":"jako-tako"}'` (until restart; `GET` shows the current mode) |
| process | env `FREELLM_TASK_MODE=jako-tako` (beats the config file) |
| permanent | `freellm-router mode jako-tako` (writes `agentic.mode` to your config and keeps the rest of the file), or `agentic: {mode: jako-tako}` |

Any spelling works: `[Rozkład Jazdy]`, `rozklad jazdy`, `rozklad`, `max`,
`Jako Tako`, `jt`. Within one request, the model suffix beats the
`X-Freellm-Task-Mode` header, which beats metadata, which beats the default.
`/v1/models` also lists `agent:rozklad-jazdy` and `agent:jako-tako`, so UI
clients can pick a mode from their model picker.

```bash
freellm-router route "hi"                                   # preview, no upstream call
freellm-router route "refactor this service for concurrency" --all
freellm-router route --catalog                              # catalog + live availability
freellm-router chat --model agent --via "explain CRDTs"     # route for real
```

```bash
curl -si localhost:8787/v1/chat/completions -H 'content-type: application/json' \
  -d '{"model":"agent","messages":[{"role":"user","content":"hi"}]}' | grep -i x-freellm
# X-Freellm-Routed-Via: google/gemini-flash-latest
# X-Freellm-Task: mode=rozklad-jazdy; role=chat; complexity=1; tier=5; pick=google/gemini-flash-latest; source=heuristic; attempt=0
# X-Freellm-Task-Reason: [Rozkład Jazdy] chat c1 (heuristic) -> all in, strongest first [cost and tier caps ignored]; picked gemini-flash-latest (tier 4, context 1000000), 13 in chain
# with -H 'X-Freellm-Task-Mode: jako-tako':
# X-Freellm-Routed-Via: opencode_zen/llama-3.1-8b
# X-Freellm-Task-Reason: Jako Tako chat c1 (heuristic) -> tier 1 [chat 2 -1]; picked zen-llama-8b (tier 1, cost 0, good at chat), 13 in chain
```

## How a pick is made (Jako Tako)

In [Rozkład Jazdy], steps 2 and 4 reduce to "strongest first": the required
tier is 5, rule caps are ignored, and nothing is "below tier".

1. **Classify.** The classifier is a deterministic heuristic that reads the
   **last user message** and assigns a role and a complexity from 1 to 5.
   - **Roles:** `classify`, `extract`, `chat`, `summarize`, `translate`,
     `write`, `research`, `review`, `plan`, `code`, `reason`.
   - **Inputs:** English and Polish cue words, code fences, numbered steps,
     length, and "hard" or "easy" wording.
   - **Why only the last message:** agent clients such as Claude Code and
     OpenCode send huge system prompts and tool schemas, which say nothing
     about how hard *this* turn is.
   - **Prompt size:** the whole prompt is still measured. Models whose
     `context` is too small are dropped.
2. **Required tier.** The formula is `roles[role] + complexity[c]`, then the
   tier window of the matching rules (`min_tier` and `max_tier`), clamped to
   1–5.
3. **Filter.** An entry is skipped for any of these reasons:
   - it is disabled;
   - the request has `tools` and the entry has none (a hard filter: tools are
     never silently dropped);
   - its context is too small;
   - its kind is not allowed by the rule's `only_where` or by `local`;
   - its provider is unavailable: no key, the daemon is down, the local model
     is not pulled, or it is cooling down.
4. **Rank.** The lowest sufficient tier comes first. Ties are broken in this
   order: the rule's `prefer` list, `good_at` the role, then `cost`, `speed`
   and catalog order.
5. **Chain.** The chain is built in three layers:
   - the winner;
   - the other sufficient entries, which escalate to stronger models if
     providers fail;
   - weaker entries, strongest first, as a best effort
     (`below_tier_fallback`).

   The normal fallback engine (cooldowns, key rotation, retries) walks it.
6. **Quality gate.** This step applies to non-streaming requests only. The
   answer is rejected when it comes back empty (no text and no tool calls) or
   with `finish_reason` `content_filter` or `error`. The task is then picked
   again one tier higher, without the entry that failed, up to `escalations`
   times. Escalation may exceed a rule's `max_tier` but never goes below its
   `min_tier`. Streams cannot be taken back once the first byte is out, so
   they are not escalated.

## Forcing the classification

If several overrides are given, the strongest one wins:

| Where | Example |
|---|---|
| model suffix | `"model": "agent:code:4"`, `"agent:plan"`, `"agent:3"`, `"agent:code:local"` |
| header | `X-Freellm-Task: code:4` or `role=review, complexity=5, local` |
| metadata | `"metadata": {"task": "code:4"}` or `task_role` / `task_complexity` / `task_local` |
| heuristic | none of the above |

`local` keeps the request on this machine (local providers only).

## API

| | |
|---|---|
| `POST /v1/chat/completions` with `model: agent…` | routes; sets `X-Freellm-Task` and `X-Freellm-Task-Reason` |
| `POST /v1/route` | the same body as chat completions. Returns the decision as JSON (need, required tier, rules, winner, chain, every candidate with its rank and why it lost) **without** calling an upstream |
| `GET /v1/route` | the effective spec: mode, modes, aliases, roles, rules, and the catalog with live availability |
| `GET` / `PUT /v1/route/mode` | show or switch the default mode of the running server |
| `GET /v1/models` | lists the aliases (`owned_by: freellm-router/agentic`), so Open WebUI and others can select them |
| Go: `pkg/router` | `IsTaskAlias`, `PlanTask`, `TaskSpec`, `TaskAvailability`, `RouteOpts.Task`, `Sinks.Task` |

## Configuring it

The defaults live in [`defaults/agentic.yaml`](../defaults/agentic.yaml) and are
embedded in the binary. Your config's `agentic:` section is layered on top.
Catalog entries and rules replace defaults **by id** (new ids are appended),
`roles` and `complexity` merge per key, and scalars replace. Catalog entries
whose provider is not configured are skipped. `config validate` checks
everything.

```yaml
agentic:
  mode: rozklad-jazdy       # or jako-tako
  escalations: 1            # 0 = no quality-gate escalation
  below_tier_fallback: true # weaker models as a last resort instead of a 502
  roles: {write: 2}         # make writing cheaper
  catalog:
    # your own pulled Ollama models
    - {id: local-qwen3.8-27b, model: "ollama/qwen3.8:27b", tier: 3, cost: 0, speed: 1, context: 16384, tools: true, good_at: [write, code, chat]}
    - {id: local-gemma4,      model: "ollama/gemma4:e4b",  tier: 1, cost: 0, speed: 3, context: 8192,  good_at: [classify, extract, translate]}
    # turn a default off
    - {id: deepseek-reasoner, model: "deepseek/deepseek-reasoner", tier: 5, enabled: false}
  rules:
    - id: reviews-stay-strong
      when: {role: review}
      min_tier: 4
      prefer: [gemini-pro]
```

| Field | Meaning |
|---|---|
| `tier` | 1 tiny (≤8B) · 2 small/mid · 3 70B-class · 4 strong · 5 strongest |
| `cost` | 0 free/local · 1 free tier with quota · 2+ burns paid credits |
| `speed` | 1 slow · 2 normal · 3 fast |
| `context` | usable prompt tokens (0 = unknown, never filtered) |
| `tools` | supports OpenAI tool calling |
| `good_at` | roles it is known to be good at (tie-break) |
| `where` | provider kind (filled from the provider config) |
| rule `when` | `role` (string or list), `complexity_min`, `complexity_max`, `tools`, `local` |
| rule effects | `min_tier`, `max_tier`, `prefer` (entry ids), `only_where` (kinds) |

If a configured **route** has the same name as an alias (`routes: {agent: […]}`),
the route wins, and the alias's suffixed forms (`agent:code:4`) are no longer
task-routed either. Routes are explicit configuration, while aliases are a
convenience.

`gemini-pro` (Google's current Pro) is in the catalog but **off by default**:
its free-tier quota is 0, and `gemini-2.5-pro` is closed to new users. In
[Rozkład Jazdy] it would be tried first and fail on every request. If your key
has billing, turn it on with
`- {id: gemini-pro, model: "google/gemini-pro-latest", tier: 5, enabled: true}`.

Tiers, costs and context sizes are judgment calls, not measurements. Edit them
freely, then run `freellm-router route --catalog` to see the result.
