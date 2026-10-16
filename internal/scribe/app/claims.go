package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/agent"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools/exec"
)

// claimTTL is how long a file stays claimed after its last edit (spec 03
// 3.11: stale claims go after 5 minutes without edits).
const claimTTL = 5 * time.Minute

// claimIdleRelease is how long a tab keeps its claims after its turn ends
// (spec 03 3.11: released 5 seconds after the prompt finishes).
const claimIdleRelease = 5 * time.Second

// fileClaim records which Scribe session last edited a file.
type fileClaim struct {
	owner string // per App instance, not per session id (two tabs can share one)
	label string // shown to other sessions
	at    time.Time
}

// claimRow is one live claim, as /claim shows it.
type claimRow struct {
	Path, Label string
	Age         time.Duration
	Mine        bool
}

// claimRegistry is process-wide: every App in this process shares one, so
// two Scribe tabs editing the same checkout can see each other's edits.
type claimRegistry struct {
	mu     sync.Mutex
	now    func() time.Time
	ttl    time.Duration
	claims map[string]fileClaim // absolute path -> claim

	turnGen     map[string]uint64 // owner -> bumped when a main turn starts
	dispatching map[string]int    // owner -> dispatch agents running now
	dispLabel   map[string]string // owner -> label, for the git refusal
}

var fileClaims = newClaimRegistry(time.Now, claimTTL)

func newClaimRegistry(now func() time.Time, ttl time.Duration) *claimRegistry {
	return &claimRegistry{
		now: now, ttl: ttl, claims: map[string]fileClaim{},
		turnGen: map[string]uint64{}, dispatching: map[string]int{}, dispLabel: map[string]string{},
	}
}

// liveLocked returns the claim on path if it has not gone stale. The caller
// holds r.mu.
func (r *claimRegistry) liveLocked(path string) (fileClaim, bool) {
	c, ok := r.claims[path]
	if !ok {
		return fileClaim{}, false
	}
	if r.now().Sub(c.at) > r.ttl {
		delete(r.claims, path)
		return fileClaim{}, false
	}
	return c, true
}

// claim records that owner just edited path.
func (r *claimRegistry) claim(path, owner, label string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.claims[path] = fileClaim{owner: owner, label: label, at: r.now()}
}

// forceClaim takes path for owner whatever its holder, and returns the claim
// it displaced (ok false when nobody else held it).
func (r *claimRegistry) forceClaim(path, owner, label string) (fileClaim, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	prev, ok := r.liveLocked(path)
	r.claims[path] = fileClaim{owner: owner, label: label, at: r.now()}
	if ok && prev.owner == owner {
		ok = false
	}
	return prev, ok
}

// heldByOther returns the live claim on path by someone other than owner.
func (r *claimRegistry) heldByOther(path, owner string) (fileClaim, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.liveLocked(path)
	if !ok || c.owner == owner {
		return fileClaim{}, false
	}
	return c, true
}

// otherClaims lists live claims held by owners other than owner, sorted.
func (r *claimRegistry) otherClaims(owner string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for p := range r.claims {
		if c, ok := r.liveLocked(p); ok && c.owner != owner {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}

// snapshot lists every live claim, sorted by path, marking owner's own.
func (r *claimRegistry) snapshot(owner string) []claimRow {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []claimRow
	now := r.now()
	for p := range r.claims {
		if c, ok := r.liveLocked(p); ok {
			out = append(out, claimRow{Path: p, Label: c.label, Age: now.Sub(c.at).Round(time.Second), Mine: c.owner == owner})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// releaseFile drops owner's claim on path. It reports whether one was dropped;
// another owner's claim is never touched.
func (r *claimRegistry) releaseFile(path, owner string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.claims[path]
	if !ok || c.owner != owner {
		return false
	}
	delete(r.claims, path)
	return true
}

// release drops every claim held by owner (session closed).
func (r *claimRegistry) release(owner string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for p, c := range r.claims {
		if c.owner == owner {
			delete(r.claims, p)
		}
	}
}

// turnStarted records that owner began a main turn. It cancels any idle
// release still waiting from the previous turn.
func (r *claimRegistry) turnStarted(owner string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.turnGen[owner]++
}

// turnGeneration returns owner's current turn generation.
func (r *claimRegistry) turnGeneration(owner string) uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.turnGen[owner]
}

// releaseIfIdle drops owner's claims, unless a new turn started since gen was
// read or a dispatch is still running for that owner.
func (r *claimRegistry) releaseIfIdle(owner string, gen uint64) bool {
	r.mu.Lock()
	idle := r.turnGen[owner] == gen && r.dispatching[owner] == 0
	r.mu.Unlock()
	if !idle {
		return false
	}
	r.release(owner)
	return true
}

// dispatchStarted and dispatchEnded bracket one dispatch subagent of owner.
func (r *claimRegistry) dispatchStarted(owner, label string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.dispatching[owner]++
	r.dispLabel[owner] = label
}

func (r *claimRegistry) dispatchEnded(owner string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.dispatching[owner] > 1 {
		r.dispatching[owner]--
		return
	}
	delete(r.dispatching, owner)
	delete(r.dispLabel, owner)
}

// otherDispatch reports whether a tab other than owner has dispatch agents
// running, and that tab's label.
func (r *claimRegistry) otherDispatch(owner string) (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for o, n := range r.dispatching {
		if o != owner && n > 0 {
			return r.dispLabel[o], true
		}
	}
	return "", false
}

// editTools are the tools whose "path" argument names a file they will write.
var editTools = map[string]bool{"edit_file": true, "multi_edit": true, "write_file": true, "ast_edit": true}

// claimHooks wraps the user hooks. It blocks edits to files another session
// has claimed, refuses git changes while another tab runs dispatch agents,
// and tells the model about other sessions' claims at the start of each
// turn. Everything else is delegated to inner (which may be nil).
type claimHooks struct {
	inner    agent.Hooks
	reg      *claimRegistry
	owner    string
	label    string
	workdir  string
	dispatch bool // this hook serves a dispatch subagent, not the tab's own turn
}

var _ agent.Hooks = claimHooks{}

func (h claimHooks) abs(p string) string {
	if !filepath.IsAbs(p) {
		p = filepath.Join(h.workdir, p)
	}
	return filepath.Clean(p)
}

func (h claimHooks) PreTool(ctx context.Context, tool string, args json.RawMessage) string {
	if h.inner != nil {
		if block := h.inner.PreTool(ctx, tool, args); block != "" {
			return block
		}
	}
	return h.claimBlock(tool, args)
}

// inputRewriter is a hook chain whose PreToolUse can replace the tool input
// (hookSpecificOutput.updatedInput). The agent asks for it in preference to
// PreTool, so the wrapper must offer it too or updatedInput is lost.
type inputRewriter interface {
	PreToolUpdate(ctx context.Context, tool string, args json.RawMessage) (json.RawMessage, string)
}

// PreToolUpdate runs the inner hooks with input rewriting, then the claim
// check on the input the call will actually run with.
func (h claimHooks) PreToolUpdate(ctx context.Context, tool string, args json.RawMessage) (json.RawMessage, string) {
	if h.inner != nil {
		if rw, ok := h.inner.(inputRewriter); ok {
			var block string
			if args, block = rw.PreToolUpdate(ctx, tool, args); block != "" {
				return args, block
			}
		} else if block := h.inner.PreTool(ctx, tool, args); block != "" {
			return args, block
		}
	}
	return args, h.claimBlock(tool, args)
}

// claimBlock is the cross-tab check: a reason when the call must not run
// because another tab owns the file or is dispatching, else "".
func (h claimHooks) claimBlock(tool string, args json.RawMessage) string {
	if exec.MutatesRepo(tool, args) {
		if label, busy := h.reg.otherDispatch(h.owner); busy {
			// Terminal wording: the model must stop and report, not retry.
			return fmt.Sprintf("BLOCKED: %s is running dispatch subagents in this project, so git changes are refused until they finish. Do not retry this command. Tell the user and stop.", label)
		}
	}
	if !editTools[tool] {
		return ""
	}
	var in struct {
		Path string `json:"path"`
	}
	if json.Unmarshal(args, &in) != nil || in.Path == "" {
		return ""
	}
	abs := h.abs(in.Path)
	c, ok := h.reg.heldByOther(abs, h.owner)
	if !ok {
		return ""
	}
	ago := h.reg.now().Sub(c.at).Round(time.Second)
	return fmt.Sprintf("CONTENTION: File %s was modified by %s (last edit %s ago). Your edit is based on stale content, so it was not applied. Do not retry it: read the file again, work on other files, or tell the user and wait for them to coordinate.",
		h.rel(abs), c.label, ago)
}

func (h claimHooks) PostTool(ctx context.Context, tool string, args json.RawMessage, res tools.Result) string {
	if h.inner != nil {
		return h.inner.PostTool(ctx, tool, args, res)
	}
	return ""
}

func (h claimHooks) Prompt(ctx context.Context, prompt string) (string, string) {
	var extra, block string
	if h.inner != nil {
		extra, block = h.inner.Prompt(ctx, prompt)
	}
	if h.dispatch {
		h.reg.dispatchStarted(h.owner, h.label)
	} else {
		h.reg.turnStarted(h.owner)
	}
	if others := h.reg.otherClaims(h.owner); len(others) > 0 {
		rels := make([]string, len(others))
		for i, p := range others {
			rels[i] = h.rel(p)
		}
		note := "Files claimed by other Scribe sessions in the last 5 minutes (they are editing these now; avoid them or coordinate with the user):\n- " +
			strings.Join(rels, "\n- ")
		if extra != "" {
			extra += "\n\n"
		}
		extra += note
	}
	return extra, block
}

// Stop ends a turn. A dispatch subagent only leaves the dispatch count. A
// tab's own turn schedules its idle release: the claims go unless another
// turn starts first.
func (h claimHooks) Stop(ctx context.Context, stats agent.TurnStats) {
	if h.inner != nil {
		h.inner.Stop(ctx, stats)
	}
	if h.dispatch {
		h.reg.dispatchEnded(h.owner)
		return
	}
	gen := h.reg.turnGeneration(h.owner)
	reg, owner := h.reg, h.owner
	time.AfterFunc(claimIdleRelease, func() { reg.releaseIfIdle(owner, gen) })
}

func (h claimHooks) rel(abs string) string {
	if r, err := filepath.Rel(h.workdir, abs); err == nil {
		return filepath.ToSlash(r)
	}
	return abs
}

// claimHooks returns the hook chain for an agent of this app.
func (a *App) claimHooks(inner agent.Hooks) agent.Hooks {
	return claimHooks{inner: inner, reg: fileClaims, owner: a.claimOwner, label: a.claimLabel, workdir: a.Workdir}
}

// subagentClaimHooks is claimHooks for a dispatch subagent.
func (a *App) subagentClaimHooks(inner agent.Hooks) agent.Hooks {
	h := claimHooks{inner: inner, reg: fileClaims, owner: a.claimOwner, label: a.claimLabel, workdir: a.Workdir, dispatch: true}
	return h
}

// onFileClaims claims every file this app's edits changed.
func (a *App) onFileClaims(e agent.Event) {
	if e.Kind != agent.EvToolResult || e.Result == nil || e.Result.IsError {
		return
	}
	for _, f := range e.Result.Files {
		abs := f
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(a.Workdir, f)
		}
		abs = filepath.Clean(abs)
		fileClaims.claim(abs, a.claimOwner, a.claimLabel)
		if rel, err := filepath.Rel(a.Workdir, abs); err == nil && !strings.HasPrefix(rel, "..") {
			a.noteEdited(rel)
		}
	}
}

// newClaimOwner returns a random identity for one App instance.
func newClaimOwner() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprint(time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

// claimLabelFor names an app for other sessions' messages.
func claimLabelFor(sessionID string) string {
	if len(sessionID) >= 8 {
		return "Scribe session " + sessionID[:8]
	}
	return "another Scribe tab"
}
