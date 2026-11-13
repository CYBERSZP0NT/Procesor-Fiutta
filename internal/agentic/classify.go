package agentic

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Msg is the slice of a chat message the classifier needs.
type Msg struct {
	Role    string
	Content string
}

// Need is what a task asks for.
type Need struct {
	Role       string   `json:"role"`
	Complexity int      `json:"complexity"`     // 1-5
	Mode       string   `json:"mode,omitempty"` // empty = the spec's mode
	Tools      bool     `json:"tools"`          // the request carries tool definitions
	Local      bool     `json:"local"`          // keep it on this machine
	Tokens     int      `json:"tokens"`         // rough prompt size (chars/4)
	Source     string   `json:"source"`         // heuristic | metadata | header | model
	Signals    []string `json:"signals,omitempty"`
}

// KnownRoles lists the roles the classifier can produce, cheapest first.
var KnownRoles = []string{"classify", "extract", "chat", "summarize", "translate", "write", "research", "review", "plan", "code", "reason"}

type roleCue struct {
	role  string
	words []string
}

// Cues are matched as lowercase substrings of the last user message
// (English + Polish stems). Each hit scores 1; code fences score 3.
var cues = []roleCue{
	{"code", []string{"code", "kod", "function", "funkcj", "bug", "debug", "stack trace", "traceback", "exception", "compile", "kompil", "refactor", "refaktor", "implement", "regex", "sql", "python", "golang", " go ", "javascript", "typescript", "rust", "bash", "script", "skrypt", "api ", "endpoint", "unit test", " tests", " test ", "segfault", "null pointer", "dockerfile", "makefile", "class "}},
	{"review", []string{"review", "recenz", "przejrzyj", "code review", "audit", "audyt", "critique", "oceń", "ocen ", "what's wrong with", "co jest nie tak"}},
	{"plan", []string{"plan", "architect", "architektur", "design a", "zaprojektuj", "roadmap", "strategy", "strategi", "milestone", "break down", "rozpisz"}},
	{"reason", []string{"prove", "udowodnij", "calculate", "oblicz", "solve", "rozwiąż", "rozwiaz", "equation", "równani", "rownani", "integral", "całk", "probability", "prawdopodob", "step by step", "krok po kroku", "logic puzzle", "zagadk"}},
	{"research", []string{"research", "compare", "porównaj", "porownaj", "pros and cons", "zalety i wady", "investigate", "zbadaj", "explain why", "wyjaśnij dlaczego", "difference between", "różnica między", "roznica miedzy", "alternatives", "alternatyw"}},
	{"write", []string{"write", "napisz", "essay", "esej", "article", "artykuł", "artykul", "email", "e-mail", "mail", "story", "opowiadani", "blog", "post ", "cover letter", "list motywacyjny", "rewrite", "przeredaguj", "poem", "wiersz", "copy"}},
	{"summarize", []string{"summarize", "summarise", "summary", "tl;dr", "tldr", "streść", "stresc", "podsumuj", "streszcz", "key points", "najważniejsze"}},
	{"translate", []string{"translate", "przetłumacz", "przetlumacz", "tłumacz", "tlumacz", "in english", "po polsku", "po angielsku", "into english", "into polish"}},
	{"extract", []string{"extract", "wyciągnij", "wyciagnij", "parse", "sparsuj", "pull out", "list all", "wypisz", "as json", "jako json", "fields"}},
	{"classify", []string{"classify", "categorize", "categorise", "sklasyfikuj", "kategoryz", "sentiment", "yes or no", "tak czy nie", "true or false", "label"}},
}

var (
	reFence    = regexp.MustCompile("```")
	reNumbered = regexp.MustCompile(`(?m)^\s*(\d+[.)]|[-*•])\s+\S`)
	hardWords  = []string{"complex", "złożon", "zlozon", "optimi", "optymaliz", "concurren", "współbież", "distributed", "rozproszon", "architecture", "security", "bezpiecze", "edge case", "production", "produkc", "scalab", "skalow", "race condition", "performance", "wydajno", "migrat", "proof", "dowód"}
	easyWords  = []string{"simple", "quick", "short", "brief", "one word", "one line", "prosty", "prost", "szybk", "krótk", "krotk", "jednym słowem", "jednym zdaniem"}
)

// Classify guesses role and complexity from the LAST user message only:
// agent clients (Claude Code, OpenCode, …) ship huge system prompts and
// tool schemas, which say nothing about how hard this turn is. The total
// prompt size is still measured (Tokens) — the picker uses it to drop
// models whose context window is too small.
func Classify(msgs []Msg, tools bool) Need {
	n := Need{Role: "chat", Complexity: 2, Tools: tools, Source: "heuristic"}
	total := 0
	last := ""
	for _, m := range msgs {
		total += len(m.Content)
		if strings.EqualFold(m.Role, "user") {
			last = m.Content
		}
	}
	if last == "" && len(msgs) > 0 {
		last = msgs[len(msgs)-1].Content
	}
	n.Tokens = total/4 + 1
	text := " " + strings.ToLower(last) + " "

	// Role: best cue score; on a tie the earlier cue wins (cues are listed
	// strongest role first, so ties lean towards "just enough", not less).
	scores := map[string]int{}
	fences := len(reFence.FindAllStringIndex(last, -1)) / 2
	if fences > 0 {
		scores["code"] += 3 * fences
		n.Signals = append(n.Signals, "code-fence")
	}
	for _, c := range cues {
		for _, w := range c.words {
			if strings.Contains(text, w) {
				scores[c.role]++
			}
		}
	}
	best := 0
	for _, c := range cues {
		if s := scores[c.role]; s > best {
			best, n.Role = s, c.role
		}
	}
	if best > 0 {
		n.Signals = append(n.Signals, "role:"+n.Role+"="+strconv.Itoa(best))
	}

	// Complexity: start at 2, move by size, structure and wording.
	c := 2
	switch l := len(last); {
	case l < 60 && fences == 0:
		c--
		n.Signals = append(n.Signals, "short")
	case l > 6000:
		c += 2
		n.Signals = append(n.Signals, "very-long")
	case l > 1500:
		c++
		n.Signals = append(n.Signals, "long")
	}
	if steps := len(reNumbered.FindAllString(last, -1)); steps >= 3 {
		c++
		n.Signals = append(n.Signals, "steps="+strconv.Itoa(steps))
	}
	hard := countAny(text, hardWords)
	if hard > 0 {
		c += min(hard, 2)
		n.Signals = append(n.Signals, "hard-words="+strconv.Itoa(hard))
	}
	if countAny(text, easyWords) > 0 {
		c--
		n.Signals = append(n.Signals, "easy-words")
	}
	if fences > 0 && c < 3 {
		c = 3 // pasted code is rarely trivial
	}
	n.Complexity = clamp(c, 1, 5)
	return n
}

func countAny(text string, words []string) int {
	k := 0
	for _, w := range words {
		if strings.Contains(text, w) {
			k++
		}
	}
	return k
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// Override is a forced role/complexity from the caller.
type Override struct {
	Mode       string
	Role       string
	Complexity int
	Local      bool
}

// ParseOverride reads "code", "4", "code:4", "code:4:local", "local",
// "jako-tako:code", "[Rozkład Jazdy]" or "mode=jako-tako, role=code,
// complexity=4, local". Unknown words are ignored.
func ParseOverride(s string) Override {
	var o Override
	s = joinModeWords(strings.ToLower(strings.TrimSpace(s)))
	if s == "" {
		return o
	}
	parts := strings.FieldsFunc(s, func(r rune) bool { return r == ':' || r == ',' || r == ';' || r == ' ' })
	for _, p := range parts {
		k, v, kv := strings.Cut(p, "=")
		if !kv {
			v = k
		}
		if m, ok := NormalizeMode(v); ok && (!kv || k == "mode") {
			o.Mode = m
			continue
		}
		switch {
		case v == "local" || (kv && k == "local" && (v == "1" || v == "true" || v == "yes")):
			o.Local = true
		case kv && k == "complexity", !kv && isDigit(v):
			if d, err := strconv.Atoi(v); err == nil {
				o.Complexity = clamp(d, 1, 5)
			}
		case kv && k == "role", !kv && isRole(v):
			if isRole(v) {
				o.Role = v
			}
		}
	}
	return o
}

func isDigit(s string) bool {
	_, err := strconv.Atoi(s)
	return err == nil
}

func isRole(s string) bool {
	for _, r := range KnownRoles {
		if r == s {
			return true
		}
	}
	return false
}

// Apply layers an override onto n and records where it came from.
func (n *Need) Apply(o Override, source string) {
	changed := false
	if o.Mode != "" {
		n.Mode = o.Mode // Source tracks role/complexity, not the mode
	}
	if o.Role != "" {
		n.Role, changed = o.Role, true
	}
	if o.Complexity != 0 {
		n.Complexity, changed = o.Complexity, true
	}
	if o.Local {
		n.Local, changed = true, true
	}
	if changed {
		n.Source = source
	}
}

// SplitAlias splits "agent:code:4" into ("agent", "code:4"). ok is false
// when name is not one of aliases.
func SplitAlias(name string, aliases []string) (alias, suffix string, ok bool) {
	head, rest, _ := strings.Cut(strings.TrimSpace(name), ":")
	for _, a := range aliases {
		if strings.EqualFold(head, a) {
			return a, rest, true
		}
	}
	return "", "", false
}

// sortedRoles returns roles in s sorted by tier then name (for display).
func sortedRoles(roles map[string]int) []string {
	out := make([]string, 0, len(roles))
	for r := range roles {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		if roles[out[i]] != roles[out[j]] {
			return roles[out[i]] < roles[out[j]]
		}
		return out[i] < out[j]
	})
	return out
}
