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
)

// claimTTL is how long a file stays claimed after its last edit.
const claimTTL = 15 * time.Minute

// fileClaim records which Scribe session last edited a file.
type fileClaim struct {
	owner string // per App instance, not per session id (two tabs can share one)
	label string // shown to other sessions
	at    time.Time
}

// claimRegistry is process-wide: every App in this process shares one, so
// two Scribe tabs editing the same checkout can see each other's edits.
type claimRegistry struct {
	mu     sync.Mutex
	now    func() time.Time
	ttl    time.Duration
	claims map[string]fileClaim // absolute path -> claim
}

var fileClaims = newClaimRegistry(time.Now, claimTTL)

func newClaimRegistry(now func() time.Time, ttl time.Duration) *claimRegistry {
	return &claimRegistry{now: now, ttl: ttl, claims: map[string]fileClaim{}}
}

// claim records that owner just edited path.
func (r *claimRegistry) claim(path, owner, label string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.claims[path] = fileClaim{owner: owner, label: label, at: r.now()}
}

// heldByOther returns the live claim on path by someone other than owner.
func (r *claimRegistry) heldByOther(path, owner string) (fileClaim, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.claims[path]
	if !ok {
		return fileClaim{}, false
	}
	if r.now().Sub(c.at) > r.ttl {
		delete(r.claims, path)
		return fileClaim{}, false
	}
	if c.owner == owner {
		return fileClaim{}, false
	}
	return c, true
}

// otherClaims lists live claims held by owners other than owner, sorted.
func (r *claimRegistry) otherClaims(owner string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for p, c := range r.claims {
		if r.now().Sub(c.at) > r.ttl {
			delete(r.claims, p)
			continue
		}
		if c.owner != owner {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
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

// editTools are the tools whose "path" argument names a file they will write.
var editTools = map[string]bool{"edit_file": true, "multi_edit": true, "write_file": true, "ast_edit": true}

// claimHooks wraps the user hooks. It blocks edits to files another session
// has claimed and tells the model about other sessions' claims at the start
// of each turn. Everything else is delegated to inner (which may be nil).
type claimHooks struct {
	inner   agent.Hooks
	reg     *claimRegistry
	owner   string
	label   string
	workdir string
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
	return fmt.Sprintf("%s is being edited by %s (last edit %s ago). Another Scribe tab or session owns it: do not overwrite it. Work on other files, or tell the user and wait for them to coordinate. Read the file again before any later edit.",
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
	if others := h.reg.otherClaims(h.owner); len(others) > 0 {
		rels := make([]string, len(others))
		for i, p := range others {
			rels[i] = h.rel(p)
		}
		note := "Files claimed by other Scribe sessions in the last 15 minutes (they are editing these now; avoid them or coordinate with the user):\n- " +
			strings.Join(rels, "\n- ")
		if extra != "" {
			extra += "\n\n"
		}
		extra += note
	}
	return extra, block
}

func (h claimHooks) Stop(ctx context.Context, stats agent.TurnStats) {
	if h.inner != nil {
		h.inner.Stop(ctx, stats)
	}
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
		fileClaims.claim(filepath.Clean(abs), a.claimOwner, a.claimLabel)
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
