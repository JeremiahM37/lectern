package agent

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// The post-dispatch passes (agentFeatures.desloppify and verifyEdits). Both
// are plain subagents built by the same factory as the dispatch's own tasks,
// so the app wiring decides which model runs them. A pass that cannot start
// reports why instead of failing the dispatch.

// desloppifyTask is the cleanup instruction for the files code tasks edited.
const desloppifyTask = `Clean up the code these edits left behind. Edited files: %s

Remove: framework-behaviour tests, redundant type assertions, error handling for states that cannot happen, debug logging and dead code.
Keep: TODO markers, business logic and non-obvious comments.

Edit only the files listed. Read around each section before you change it. Then reply in under 200 words: what you removed and what you kept.`

// reviewTask is the instruction for the read-only reviewer.
const reviewTask = `Review the edits that code agents just made. Edited files: %s

Project check result:
%s

Read the changed files. Look for correctness bugs, broken call sites and problems the check would not catch. Do not edit files.

End with one line, exactly one of "VERDICT: PASS", "VERDICT: FAIL — file:line" or "VERDICT: PARTIAL", then the reasons in under 150 words.`

// lintFixTimeout bounds the deterministic lint fix after code tasks edit files
// (spec 06 section 9.5: 30 seconds).
const lintFixTimeout = 30 * time.Second

// lintFixer is the file-scoped lint fix the project tool offers. It is an
// optional interface on the tool: a registry without it skips the pass.
type lintFixer interface {
	LintFixFiles(ctx context.Context, env *tools.Env, files []string, timeout time.Duration) (string, bool)
}

// lintFix runs the project's lint auto-fix on the files code tasks changed.
// It runs once per dispatch, before the cleanup and the check, so both see the
// fixed files. It is deterministic and returns the report, or "" when the
// registry has no project tool that can scope a fix.
func lintFix(ctx context.Context, env *tools.Env, reg *tools.Registry, changed []string) string {
	if reg == nil || len(changed) == 0 {
		return ""
	}
	t, ok := reg.Get("project")
	if !ok {
		return ""
	}
	fx, ok := t.(lintFixer)
	if !ok {
		return ""
	}
	e := *env
	e.CallID = env.CallID + "/lintfix"
	report, _ := fx.LintFixFiles(ctx, &e, changed, lintFixTimeout)
	return report
}

// sideTask runs one post-dispatch subagent and counts its spend toward the
// dispatch. It does not run once the circuit breaker has opened.
func (r *dispatchRun) sideTask(ctx context.Context, role string, purpose Purpose, label, task string) attempt {
	if r.tripped() {
		return attempt{err: errBreakerOpen}
	}
	idx := r.board.add(role, label)
	res := r.attempt(ctx, idx, role, purpose, task)
	r.mu.Lock()
	r.total.Add(res.usage)
	r.cost += res.cost
	r.mu.Unlock()
	return res
}

// desloppify runs the cleanup pass over the edited files. It returns the
// report for the Cleanup section and the files the pass changed.
func (r *dispatchRun) desloppify(ctx context.Context, changed []string) (string, []string) {
	res := r.sideTask(ctx, "code", PurposeCleanup, "cleanup of edited files",
		fmt.Sprintf(desloppifyTask, strings.Join(changed, ", ")))
	if res.err != nil {
		return "not run: " + res.err.Error(), nil
	}
	report := strings.TrimSpace(res.report)
	if report == "" {
		report = "the cleanup agent reported nothing"
	}
	return report, res.edited
}

// review runs the read-only reviewer over the changed files and the project
// check. It returns the report and a verdict: "pass", "fail" or "unknown".
func (r *dispatchRun) review(ctx context.Context, changed []string, check string) (string, string) {
	task := fmt.Sprintf(reviewTask, strings.Join(changed, ", "), check)
	res := r.sideTask(ctx, "explore", PurposeReview, "review of the changed files", task)
	if res.err != nil {
		return "not run: " + res.err.Error(), "unknown"
	}
	report := strings.TrimSpace(res.report)
	return report, verdictOf(report)
}

// verdictOf reads the last "VERDICT:" line of a reviewer's reply. The verdict
// is the first word after the colon, so "FAIL — file:line" reads as a fail.
// Anything else is "unknown".
func verdictOf(report string) string {
	lines := strings.Split(report, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.ToUpper(strings.Trim(strings.TrimSpace(lines[i]), "*_` "))
		v, ok := strings.CutPrefix(line, "VERDICT:")
		if !ok {
			continue
		}
		word := strings.FieldsFunc(v, func(r rune) bool { return !unicode.IsLetter(r) })
		if len(word) == 0 {
			return "unknown"
		}
		switch word[0] {
		case "PASS":
			return "pass"
		case "FAIL":
			return "fail"
		case "PARTIAL":
			return "partial"
		}
		return "unknown"
	}
	return "unknown"
}

// renderReview is the body of the Review section: the verdict on its own
// line, then the reviewer's reasons.
func renderReview(report, verdict string) string {
	word := map[string]string{"pass": "PASS", "fail": "FAIL", "partial": "PARTIAL"}[verdict]
	if word == "" {
		word = "none stated (the reviewer did not end with a VERDICT line)"
	}
	return "Verdict: " + word + "\n" + report
}

// renderCleanup is the body of the Cleanup section (spec 06 §9.5): the
// deterministic lint fix that runs first, then the cleanup agent's report and
// the files that agent changed.
func renderCleanup(lint, cleanup string, edited []string) string {
	var b strings.Builder
	if lint != "" {
		fmt.Fprintf(&b, "Lint fix: %s\n", lint)
	}
	if cleanup != "" {
		fmt.Fprintf(&b, "Cleanup agent: %s\n", cleanup)
	}
	if len(edited) > 0 {
		fmt.Fprintf(&b, "Files edited by cleanup: %s\n", strings.Join(edited, ", "))
	}
	return b.String()
}

// cacheTally counts what the dispatch cache did over one dispatch, per
// subagent, for the Cache section of the output (spec 03 §3.7, §3.8). A miss
// is a read that went to disk and was stored, or stored over an older read of
// a changed file.
type cacheTally struct {
	mu    sync.Mutex
	order []string // subagent ids in the order they first had a cache note
	byID  map[string]*cacheCount
}

type cacheCount struct {
	hits, waits, misses int
	from                []string // subagents that supplied a hit or were waited on
}

// note records one cache outcome.
func (c *cacheTally) note(n CacheNote) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.byID == nil {
		c.byID = map[string]*cacheCount{}
	}
	cc := c.byID[n.Agent]
	if cc == nil {
		cc = &cacheCount{}
		c.byID[n.Agent] = cc
		c.order = append(c.order, n.Agent)
	}
	switch n.Outcome {
	case CacheHit:
		cc.hits++
	case CacheWait:
		cc.waits++
	case CacheStore, CacheInvalidate:
		cc.misses++
	}
	if (n.Outcome == CacheHit || n.Outcome == CacheWait) && n.From != "" && !slices.Contains(cc.from, n.From) {
		cc.from = append(cc.from, n.From)
	}
}

// render is the Cache section body: the totals, then one line per subagent
// that had a cache note, labelled by labels when it has one. It is "" when the
// cache did nothing (the cache is off, or no read was made).
func (c *cacheTally) render(labels map[string]string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.order) == 0 {
		return ""
	}
	var hits, waits, misses int
	for _, id := range c.order {
		cc := c.byID[id]
		hits += cc.hits
		waits += cc.waits
		misses += cc.misses
	}
	var b strings.Builder
	fmt.Fprintf(&b, "File reads: %s, %s, %s\n", countWord(hits, "hit", "hits"), countWord(waits, "wait", "waits"), countWord(misses, "miss", "misses"))
	for _, id := range c.order {
		cc := c.byID[id]
		var parts []string
		if cc.hits > 0 {
			parts = append(parts, countWord(cc.hits, "hit", "hits"))
		}
		if cc.waits > 0 {
			parts = append(parts, countWord(cc.waits, "wait", "waits"))
		}
		if cc.misses > 0 {
			parts = append(parts, countWord(cc.misses, "miss", "misses"))
		}
		line := strings.Join(parts, ", ")
		if len(cc.from) > 0 {
			line += " (from " + strings.Join(cc.from, ", ") + ")"
		}
		name := id
		if l := labels[id]; l != "" {
			name = id + " (" + l + ")"
		}
		fmt.Fprintf(&b, "- %s: %s\n", name, line)
	}
	return b.String()
}

// countWord is "1 hit" or "3 hits".
func countWord(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

// labels names each subagent of the dispatch for the output: its role and the
// start of its task, keyed by id.
func (b *board) labels() map[string]string {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make(map[string]string, len(b.infos))
	for _, info := range b.infos {
		out[info.ID] = info.Role + ": " + firstLine(clip(info.Task, 60))
	}
	return out
}
