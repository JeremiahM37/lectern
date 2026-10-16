package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// Deferred tools keep the per-step tool schema small. Only the core set and
// the tools the model has asked for with request_tools are sent to the
// model; everything else is listed by name and one-line description in the
// system prompt (see DeferredCatalog) until it is requested.
//
// The offered set only ever grows during a session (until release_tools),
// and schemas are emitted in registration order, so the tools block stays
// byte-identical across steps whenever the set does not change.

// Deferred is the per-session set of tools the model has activated. Create
// it with NewDeferred, pass it in Options.Deferred, and register the
// request_tools and release_tools tools with RegisterDeferred.
type Deferred struct {
	mu     sync.Mutex
	core   map[string]bool
	active map[string]bool
}

// metaTools are always offered: they are how deferred tools are managed.
var metaTools = []string{"request_tools", "release_tools"}

// NewDeferred returns a set whose core names are always offered.
func NewDeferred(core []string) *Deferred {
	d := &Deferred{core: map[string]bool{}, active: map[string]bool{}}
	for _, n := range core {
		d.core[n] = true
	}
	for _, n := range metaTools {
		d.core[n] = true
	}
	return d
}

// IsCore reports whether name is always offered.
func (d *Deferred) IsCore(name string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.core[name]
}

// names returns core plus activated names.
func (d *Deferred) names() map[string]bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make(map[string]bool, len(d.core)+len(d.active))
	for n := range d.core {
		out[n] = true
	}
	for n := range d.active {
		out[n] = true
	}
	return out
}

// Active returns the activated (non-core) names, sorted.
func (d *Deferred) Active() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]string, 0, len(d.active))
	for n := range d.active {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// offered intersects the deferred set with base (the user's disabled-tool
// filter, nil = everything). A nil result means "all".
func (d *Deferred) offered(base map[string]bool) map[string]bool {
	names := d.names()
	if base == nil {
		return names
	}
	for n := range names {
		if !base[n] {
			delete(names, n)
		}
	}
	return names
}

// RegisterDeferred adds request_tools and release_tools to r.
func RegisterDeferred(r *tools.Registry, d *Deferred) {
	r.Register(&requestTool{reg: r, d: d, release: false})
	r.Register(&requestTool{reg: r, d: d, release: true})
}

type requestTool struct {
	reg     *tools.Registry
	d       *Deferred
	release bool
}

func (t *requestTool) ReadOnly() bool { return true }

func (t *requestTool) Spec() llm.ToolSpec {
	if t.release {
		return llm.ToolSpec{
			Name:        "release_tools",
			Description: "Unload deferred tools you no longer need, to keep the tool list short. Core tools cannot be released.",
			Schema: tools.Schema(map[string]any{
				"tools": tools.Arr("Tool names to release", map[string]any{"type": "string"}),
			}, "tools"),
		}
	}
	return llm.ToolSpec{
		Name: "request_tools",
		Description: "Load deferred tools by name so you can call them from the next step on. The system prompt lists " +
			"every deferred tool with a one-line description. Loaded tools stay available for the rest of the session.",
		Schema: tools.Schema(map[string]any{
			"tools": tools.Arr("Tool names to load, exactly as listed", map[string]any{"type": "string"}),
		}, "tools"),
	}
}

func (t *requestTool) Run(_ context.Context, _ *tools.Env, args json.RawMessage) tools.Result {
	var in struct {
		Tools []string `json:"tools"`
	}
	if err := tools.Decode(args, &in); err != nil {
		return tools.Errorf("%v", err)
	}
	if len(in.Tools) == 0 {
		return tools.Errorf("pass at least one tool name in tools")
	}
	if t.release {
		return t.doRelease(in.Tools)
	}
	return t.doRequest(in.Tools)
}

func (t *requestTool) doRequest(names []string) tools.Result {
	var activated, already, unknown []string
	t.d.mu.Lock()
	for _, raw := range names {
		n := strings.TrimSpace(raw)
		if n == "" {
			continue
		}
		if _, ok := t.reg.Get(n); !ok {
			unknown = append(unknown, n)
			continue
		}
		if t.d.core[n] || t.d.active[n] {
			already = append(already, n)
			continue
		}
		t.d.active[n] = true
		activated = append(activated, n)
	}
	t.d.mu.Unlock()

	var lines []string
	if len(activated) > 0 {
		lines = append(lines, "Activated: "+strings.Join(activated, ", ")+". They can be called from the next step.")
	}
	if len(already) > 0 {
		lines = append(lines, "Already active: "+strings.Join(already, ", "))
	}
	var errs []string
	for _, n := range unknown {
		if s := t.suggest(n); s != "" {
			errs = append(errs, fmt.Sprintf("Unknown: %s (did you mean %s?)", n, s))
		} else {
			errs = append(errs, "Unknown: "+n)
		}
	}
	if len(lines) == 0 && len(errs) == 0 {
		return tools.Errorf("no tool names given")
	}
	out := strings.Join(append(lines, errs...), "\n")
	return tools.Result{Output: out, IsError: len(activated) == 0 && len(already) == 0, Summary: fmt.Sprintf("%d loaded", len(activated))}
}

func (t *requestTool) doRelease(names []string) tools.Result {
	var released, notActive, core []string
	t.d.mu.Lock()
	for _, raw := range names {
		n := strings.TrimSpace(raw)
		switch {
		case n == "":
		case t.d.core[n]:
			core = append(core, n)
		case t.d.active[n]:
			delete(t.d.active, n)
			released = append(released, n)
		default:
			notActive = append(notActive, n)
		}
	}
	t.d.mu.Unlock()
	var lines []string
	if len(released) > 0 {
		lines = append(lines, "Released: "+strings.Join(released, ", "))
	}
	if len(notActive) > 0 {
		lines = append(lines, "Not active: "+strings.Join(notActive, ", "))
	}
	if len(core) > 0 {
		lines = append(lines, "Core tools cannot be released: "+strings.Join(core, ", ")+" (core)")
	}
	if len(lines) == 0 {
		return tools.Errorf("no tool names given")
	}
	return tools.Result{Output: strings.Join(lines, "\n"), Summary: fmt.Sprintf("%d released", len(released))}
}

// suggest returns the closest deferred or registered tool name to n, or "".
func (t *requestTool) suggest(n string) string {
	best, bestD := "", 4
	lower := strings.ToLower(n)
	for _, cand := range t.reg.Names() {
		d := levenshtein(lower, strings.ToLower(cand))
		if strings.Contains(cand, lower) || strings.Contains(lower, cand) {
			d = 1
		}
		if d < bestD {
			best, bestD = cand, d
		}
	}
	return best
}

func levenshtein(a, b string) int {
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}

// DeferredCatalog renders one line per deferred tool ("- name — description")
// for the system prompt, in registration order. core names are skipped.
// Descriptions are cut to their first sentence and 160 characters.
func DeferredCatalog(r *tools.Registry, core func(string) bool) string {
	var b strings.Builder
	for _, n := range r.Names() {
		if core(n) {
			continue
		}
		t, ok := r.Get(n)
		if !ok {
			continue
		}
		fmt.Fprintf(&b, "- %s — %s\n", n, oneLine(t.Spec().Description))
	}
	return strings.TrimRight(b.String(), "\n")
}

func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if i := strings.Index(s, ". "); i > 0 {
		s = s[:i+1]
	}
	if r := []rune(s); len(r) > 160 {
		s = string(r[:157]) + "..."
	}
	return s
}
