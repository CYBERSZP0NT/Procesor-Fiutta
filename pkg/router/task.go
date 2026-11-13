package router

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/silverx/freellm-router/internal/agentic"
	"github.com/silverx/freellm-router/internal/core"
)

// TaskSpec is the agentic task-router configuration (catalog, roles, rules).
type TaskSpec = agentic.Spec

// TaskDecision explains one task-router pick: the need, the required tier,
// the chain, a one-line reason and every candidate with why it lost.
type TaskDecision = agentic.Decision

// TaskCandidate is a catalog entry as one decision saw it (rank, why it lost).
type TaskCandidate = agentic.Candidate

// TaskModeInfo describes a task-router mode ([Rozkład Jazdy], Jako Tako).
type TaskModeInfo = agentic.ModeInfo

// TaskEntry is one catalog entry of the task router.
type TaskEntry = agentic.Entry

// TaskHints carries caller overrides for the task router. Precedence,
// strongest first: model suffix ("agent:code:4") > Override > Metadata >
// the heuristic classifier.
type TaskHints struct {
	// Override is e.g. the X-Freellm-Task header: "code:4", "role=plan", "local".
	Override string
	// Metadata is the OpenAI "metadata" object: "task" ("code:4"), or
	// "task_role", "task_complexity", "task_local", "task_mode".
	Metadata map[string]string
	// Mode is e.g. the X-Freellm-Task-Mode header ("jako-tako",
	// "[Rozkład Jazdy]"); it beats metadata, the model suffix beats it.
	Mode string
	// OnDecision, when set, receives the decisions like Sinks.Task does
	// (for Route/RouteWithOptions callers, which have no Sinks).
	OnDecision func(*TaskDecision)
}

// taskRouter is the compiled task-router state of a Router.
type taskRouter struct {
	spec   agentic.Spec
	routes map[string]bool // configured route names win over task aliases
	mode   atomic.Value    // string: current default mode (SetTaskMode)
}

func newTaskRouter(user *agentic.Spec, kinds map[string]string, routes map[string][]string) (*taskRouter, error) {
	base, err := agentic.Default()
	if err != nil {
		return nil, err
	}
	spec := *base
	if user != nil {
		spec = spec.Merge(*user)
	}
	// Keep only entries whose provider is configured; fill their kind.
	kept := spec.Catalog[:0:0]
	for _, e := range spec.Catalog {
		kind, ok := kinds[strings.ToLower(e.Provider())]
		if !ok {
			slog.Debug("agentic: catalog entry skipped, provider not configured", "id", e.ID, "provider", e.Provider())
			continue
		}
		if e.Where == "" {
			e.Where = kind
		}
		kept = append(kept, e)
	}
	spec.Catalog = kept
	rs := make(map[string]bool, len(routes))
	for name := range routes {
		rs[strings.ToLower(name)] = true
	}
	tr := &taskRouter{spec: spec, routes: rs}
	tr.mode.Store(spec.EffectiveMode())
	return tr, nil
}

// TaskMode returns the current default mode id ("rozklad-jazdy" unless
// configured or switched).
func (r *Router) TaskMode() string {
	if r.task == nil {
		return agentic.DefaultMode
	}
	return r.task.mode.Load().(string)
}

// SetTaskMode switches the default mode for every following request (any
// spelling NormalizeMode accepts). It returns the normalized id. Requests
// can still pick their own mode.
func (r *Router) SetTaskMode(mode string) (string, error) {
	id, ok := agentic.NormalizeMode(mode)
	if !ok {
		return "", fmt.Errorf("unknown task-router mode %q (rozklad-jazdy | jako-tako)", mode)
	}
	if r.task == nil {
		return "", fmt.Errorf("agentic task router is disabled")
	}
	r.task.mode.Store(id)
	return id, nil
}

// TaskModes lists the available modes, default first.
func TaskModes() []TaskModeInfo { return agentic.Modes }

// IsTaskAlias reports whether selector goes through the task router
// ("agent", "task", "agent:code:4", …). A configured route with the same
// name wins: routes are explicit, aliases are a convenience.
func (r *Router) IsTaskAlias(selector string) bool {
	if r.task == nil || !r.task.spec.IsEnabled() {
		return false
	}
	alias, _, ok := agentic.SplitAlias(selector, r.task.spec.AliasNames())
	return ok && !r.task.routes[strings.ToLower(alias)]
}

// TaskAliases returns the model names that trigger task routing (empty
// when the task router is disabled).
func (r *Router) TaskAliases() []string {
	if r.task == nil || !r.task.spec.IsEnabled() {
		return nil
	}
	var out []string
	for _, a := range r.task.spec.AliasNames() {
		if !r.task.routes[strings.ToLower(a)] {
			out = append(out, a)
		}
	}
	return out
}

// TaskSpec returns the effective task-router spec (defaults + config,
// catalog limited to configured providers).
func (r *Router) TaskSpec() TaskSpec {
	if r.task == nil {
		return TaskSpec{}
	}
	return r.task.spec
}

// TaskAvailability reports, per catalog entry id, whether it can be tried
// right now and why not.
func (r *Router) TaskAvailability(ctx context.Context) map[string]string {
	out := map[string]string{}
	if r.task == nil {
		return out
	}
	avail := r.availFunc(ctx)
	for _, e := range r.task.spec.Catalog {
		if ok, why := avail(e); ok {
			out[e.ID] = "ok"
		} else {
			out[e.ID] = why
		}
	}
	return out
}

// PlanTask previews what the task router would do for messages, without
// calling any upstream. selector may carry a suffix ("agent:code:4").
func (r *Router) PlanTask(ctx context.Context, selector string, messages []Message, tools bool, h *TaskHints) (*TaskDecision, error) {
	if r.task == nil || !r.task.spec.IsEnabled() {
		return nil, fmt.Errorf("agentic task router is disabled")
	}
	if selector == "" {
		selector = r.task.spec.AliasNames()[0]
	}
	msgs := make([]core.Message, 0, len(messages))
	for _, m := range messages {
		msgs = append(msgs, core.Message{Role: core.Role(m.Role), Content: m.Content})
	}
	need := r.taskNeed(selector, msgs, tools, h)
	d := r.task.spec.Pick(need, 0, nil, r.availFunc(ctx))
	return &d, nil
}

func (r *Router) taskNeed(selector string, msgs []core.Message, tools bool, h *TaskHints) agentic.Need {
	in := make([]agentic.Msg, 0, len(msgs))
	for _, m := range msgs {
		in = append(in, agentic.Msg{Role: string(m.Role), Content: m.Content})
	}
	n := agentic.Classify(in, tools)
	if h != nil {
		if md := h.Metadata; len(md) > 0 {
			o := agentic.ParseOverride(md["task"])
			if v := md["task_role"]; v != "" {
				o.Role = agentic.ParseOverride(v).Role
			}
			if v, err := strconv.Atoi(md["task_complexity"]); err == nil {
				o.Complexity = agentic.ParseOverride(strconv.Itoa(v)).Complexity
			}
			if v := strings.ToLower(md["task_local"]); v == "1" || v == "true" || v == "yes" {
				o.Local = true
			}
			if m, ok := agentic.NormalizeMode(md["task_mode"]); ok {
				o.Mode = m
			}
			n.Apply(o, "metadata")
		}
		n.Apply(agentic.ParseOverride(h.Override), "header")
		if m, ok := agentic.NormalizeMode(h.Mode); ok {
			n.Mode = m
		}
	}
	if _, suffix, ok := agentic.SplitAlias(selector, r.task.spec.AliasNames()); ok {
		n.Apply(agentic.ParseOverride(suffix), "model")
	}
	if n.Mode == "" {
		n.Mode = r.TaskMode()
	}
	return n
}

// availFunc checks provider availability (cached per provider for this
// call — ollama probes the daemon) and per-model cooldown.
func (r *Router) availFunc(ctx context.Context) agentic.AvailFunc {
	var mu sync.Mutex
	cache := map[string]*core.Availability{}
	local := map[string]map[string]bool{} // provider → pulled model ids (nil = unknown)
	return func(e agentic.Entry) (bool, string) {
		name := e.Provider()
		p, ok := r.core.Provider(name)
		if !ok {
			return false, "provider not registered"
		}
		mu.Lock()
		a, seen := cache[name]
		mu.Unlock()
		if !seen {
			a = p.Available(ctx)
			mu.Lock()
			cache[name] = a
			mu.Unlock()
		}
		if a != nil && !a.OK {
			return false, a.Reason
		}
		// A running local daemon says nothing about whether this model is
		// pulled; ask its (local, cheap) model list instead of letting the
		// chain hit a 404 and a long cooldown.
		if e.Where == "local" {
			mu.Lock()
			ids, listed := local[name]
			mu.Unlock()
			if !listed {
				if ms, err := p.Models(ctx); err == nil {
					ids = map[string]bool{}
					for _, m := range ms {
						ids[strings.TrimSuffix(m.ID, ":latest")] = true
					}
				}
				mu.Lock()
				local[name] = ids
				mu.Unlock()
			}
			if ids != nil && !ids[strings.TrimSuffix(e.ModelID(), ":latest")] {
				return false, "model not pulled (ollama pull " + e.ModelID() + ")"
			}
		}
		if g := core.CallOptionsFrom(ctx).Gate; g != nil {
			if ok, why := g(ctx, name, e.ModelID()); !ok {
				return false, why
			}
		}
		if cd := r.core.Cooldown(); cd.Has(ctx, e.Model) {
			return false, "cooldown " + cd.Remaining(ctx, e.Model).Round(1e9).String()
		}
		return true, ""
	}
}

// dispatchOnce routes req once: through the task router when selector is a
// task alias, through the route policy otherwise.
func (r *Router) dispatchOnce(ctx context.Context, selector string, req *core.ChatRequest, out *core.StreamOut, h *TaskHints, onDecision func(*TaskDecision)) (*core.RouteResult, error) {
	if !r.IsTaskAlias(selector) {
		return r.core.Route(ctx, core.ModelSelector{Name: selector}, req, out)
	}
	res, _, err := r.routeTask(ctx, selector, req, out, h, onDecision)
	return res, err
}

// routeTask picks a chain for the task and walks it. A non-streaming answer
// that comes back empty (no text, no tool calls) or filtered is rejected
// and the task is re-picked one tier higher, without the entry that
// produced it — up to Spec.Escalations times. Streams cannot be taken back
// once the first byte is out, so they are not escalated.
func (r *Router) routeTask(ctx context.Context, selector string, req *core.ChatRequest, out *core.StreamOut, h *TaskHints, onDecision func(*TaskDecision)) (*core.RouteResult, *TaskDecision, error) {
	need := r.taskNeed(selector, req.Messages, len(req.Tools) > 0, h)
	maxEsc := r.task.spec.MaxEscalations()
	if req.Stream {
		maxEsc = 0
	}
	exclude := map[string]bool{}
	avail := r.availFunc(ctx)
	for attempt := 0; ; attempt++ {
		d := r.task.spec.Pick(need, attempt, exclude, avail)
		if len(d.Chain) == 0 {
			return nil, &d, fmt.Errorf("agentic: %s", d.Reason)
		}
		if onDecision != nil {
			onDecision(&d)
		}
		chain := make([]core.Model, 0, len(d.Chain))
		for _, m := range d.Chain {
			e := agentic.Entry{Model: m}
			chain = append(chain, core.Model{Provider: e.Provider(), ID: e.ModelID()})
		}
		sel := core.ModelSelector{Name: selector, Chain: chain, Context: map[string]string{
			"task.role":       need.Role,
			"task.complexity": strconv.Itoa(need.Complexity),
			"task.tier":       strconv.Itoa(d.Required),
		}}

		canEscalate := attempt < maxEsc
		callOut := out
		var buf *bufferedOut
		if canEscalate && out != nil {
			buf = newBufferedOut(out)
			callOut = buf.stream()
		}
		res, err := r.core.Route(ctx, sel, req, callOut)
		if err != nil {
			return nil, &d, err
		}
		d.Via = res.Via
		if canEscalate && ctx.Err() == nil && rejected(res, buf) {
			slog.Info("agentic: answer rejected, escalating", "via", res.Via, "finish", res.Done.FinishReason, "attempt", attempt+1)
			exclude[res.Via] = true
			continue
		}
		if buf != nil {
			buf.flush(res.Via)
		}
		if onDecision != nil {
			onDecision(&d)
		}
		return res, &d, nil
	}
}

// rejected is the quality gate: deliberately narrow (no "I can't" keyword
// matching — that would escalate legitimate answers).
func rejected(res *core.RouteResult, buf *bufferedOut) bool {
	if f := res.Done.FinishReason; f == "content_filter" || f == "error" {
		return true
	}
	if buf != nil {
		return strings.TrimSpace(buf.text.String()) == "" && len(buf.tools) == 0
	}
	return strings.TrimSpace(res.FullText) == ""
}

// bufferedOut holds one attempt's output until the gate accepts it.
type bufferedOut struct {
	dst       *core.StreamOut
	text      strings.Builder
	reasoning strings.Builder
	tools     []core.ToolCall
}

func newBufferedOut(dst *core.StreamOut) *bufferedOut { return &bufferedOut{dst: dst} }

func (b *bufferedOut) stream() *core.StreamOut {
	return &core.StreamOut{
		TextSink:      func(s string) { b.text.WriteString(s) },
		ToolSink:      func(tc core.ToolCall) { b.tools = append(b.tools, tc) },
		ReasoningSink: func(s string) { b.reasoning.WriteString(s) },
		ErrSink:       b.dst.ErrSink,
	}
}

func (b *bufferedOut) flush(via string) {
	if b.dst.Serving != nil {
		b.dst.Serving(via)
	}
	if b.dst.ReasoningSink != nil && b.reasoning.Len() > 0 {
		b.dst.ReasoningSink(b.reasoning.String())
	}
	if b.dst.TextSink != nil && b.text.Len() > 0 {
		b.dst.TextSink(b.text.String())
	}
	if b.dst.ToolSink != nil {
		for _, tc := range b.tools {
			b.dst.ToolSink(tc)
		}
	}
}
