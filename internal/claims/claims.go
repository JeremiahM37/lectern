// Package claims is the Claim board's logic half: a shared, vendor-neutral
// record of who is doing what in a repository, so agents from different
// vendors (Claude Code, Codex, ACP agents) and humans coordinate instead of
// duplicating work on the same checkout. See docs/claims.md for the full
// contract; this package is called from the hook response path in
// internal/api/hooks_agentevents.go, the REST surface in
// internal/api/claims.go, and the `claim_work`/`release_work`/`list_claims`
// MCP tools.
//
// Nothing here shells out or blocks on anything slower than a SQLite query —
// unlike internal/awareness's repo-key resolution, a claim always already
// knows its repo_key (resolved via internal/awareness beforehand), so this
// package is safe to call from a hook's HTTP response as well as an ordinary
// API handler.
package claims

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"path"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// Scope kinds (docs/claims.md).
const (
	ScopeTask  = "task"
	ScopePaths = "paths"
	ScopeTopic = "topic"
)

// TTL bounds. DefaultTTL is docs/claims.md's "default 2h". A claim's TTL is
// also what a renewal (RenewActivity) extends it by, so these bounds apply at
// creation time only.
const (
	DefaultTTL = 2 * time.Hour
	MinTTL     = 5 * time.Minute
	MaxTTL     = 24 * time.Hour
)

// TopicOverlapThreshold is the Jaccard word-overlap score at or above which a
// new prompt is considered a likely duplicate of an active topic claim
// (docs/claims.md point 4c) — the same 0.5 threshold
// internal/awareness.DuplicatePrompts already uses for session-to-session
// duplicate detection.
const TopicOverlapThreshold = 0.5

// EditWarnWindow bounds how "recent" a claim's own creation needs to be
// treated as informational rather than stale — claims already carry their
// own expiry, so this only guards the briefing text's phrasing, not whether a
// claim counts as active at all (that is released_at/expires_at).
const BriefingMaxChars = 1200

func ValidScopeKind(k string) bool {
	return k == ScopeTask || k == ScopePaths || k == ScopeTopic
}

// EncodePaths/DecodePaths are the paths scope_kind's payload encoding: a JSON
// array of glob patterns, relative to the repository toplevel (the same
// rel_path space internal/awareness already computes edits in).
func EncodePaths(globs []string) string {
	b, err := json.Marshal(globs)
	if err != nil {
		return "[]"
	}
	return string(b)
}

func DecodePaths(scope string) []string {
	var out []string
	if err := json.Unmarshal([]byte(scope), &out); err != nil {
		// Tolerate a bare comma-separated string too, for a caller (or a
		// hand-typed test fixture) that didn't bother with JSON.
		scope = strings.TrimSpace(scope)
		if scope == "" {
			return nil
		}
		for _, p := range strings.Split(scope, ",") {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
	}
	return out
}

// AutoClaimGlob computes the "that file's directory" glob docs/claims.md's
// automatic-claim rule refers to: a file at the repo toplevel gets "*"
// (root-level files only), anything else gets "<dir>/**".
func AutoClaimGlob(relPath string) string {
	dir := path.Dir(strings.TrimSpace(relPath))
	if dir == "." || dir == "/" || dir == "" {
		return "*"
	}
	return dir + "/**"
}

// ---- glob matching ----------------------------------------------------------

// MatchGlob reports whether relPath matches pattern, where "*" matches any
// run of characters EXCEPT '/' and "**" matches any run of characters
// including '/' — the same two wildcards docs/claims.md's examples use
// ("frontend/src/sessions/**"). Go's stdlib path/filepath.Match has no `**`,
// so this translates the pattern to a small anchored regexp instead.
func MatchGlob(pattern, relPath string) bool {
	re, ok := compileGlob(pattern)
	if !ok {
		return false
	}
	return re.MatchString(relPath)
}

var globCache sync.Map // pattern string -> *regexp.Regexp

func compileGlob(pattern string) (*regexp.Regexp, bool) {
	if cached, ok := globCache.Load(pattern); ok {
		re, ok := cached.(*regexp.Regexp)
		return re, ok
	}
	var b strings.Builder
	b.WriteString("^")
	i := 0
	for i < len(pattern) {
		switch {
		case strings.HasPrefix(pattern[i:], "**"):
			b.WriteString(".*")
			i += 2
		case pattern[i] == '*':
			b.WriteString("[^/]*")
			i++
		case pattern[i] == '?':
			b.WriteString("[^/]")
			i++
		default:
			b.WriteString(regexp.QuoteMeta(string(pattern[i])))
			i++
		}
	}
	b.WriteString("$")
	re, err := regexp.Compile(b.String())
	if err != nil {
		globCache.Store(pattern, (*regexp.Regexp)(nil))
		return nil, false
	}
	globCache.Store(pattern, re)
	return re, true
}

// GlobBase strips a pattern's trailing wildcard segment to get the directory
// it is rooted at — "frontend/src/sessions/**" -> "frontend/src/sessions",
// "*.go" -> "". Used by PathsOverlap to decide whether two glob sets could
// ever match the same file without needing full glob-vs-glob intersection.
func GlobBase(pattern string) string {
	pattern = strings.TrimSuffix(pattern, "**")
	pattern = strings.TrimSuffix(pattern, "/")
	if strings.ContainsAny(pattern, "*?") {
		// A wildcard mid-pattern (e.g. "src/*/gen") has no clean prefix;
		// only the directory portion before the first wildcard is safe.
		if idx := strings.IndexAny(pattern, "*?"); idx >= 0 {
			pattern = pattern[:idx]
		}
	}
	return strings.TrimSuffix(pattern, "/")
}

// PathsOverlap reports whether any glob in a could ever match a file also
// matched by some glob in b — a directory-prefix heuristic (one base is a
// prefix of the other, or they're identical) rather than true glob-vs-glob
// intersection, which is undecidable in general once "**" is involved. Good
// enough for advisory coordination text, which is all this ever drives.
func PathsOverlap(a, b []string) bool {
	for _, ga := range a {
		ba := GlobBase(ga)
		for _, gb := range b {
			bb := GlobBase(gb)
			if ga == gb || strings.HasPrefix(ba+"/", bb+"/") || strings.HasPrefix(bb+"/", ba+"/") {
				return true
			}
		}
	}
	return false
}

// ---- topic word overlap ------------------------------------------------------

// TopicScore is the Jaccard similarity over normalised words between two
// short texts — the same measure internal/awareness.DuplicatePrompts uses for
// session-to-session duplicate detection, reimplemented here (rather than
// exported from that package) to keep the two features decoupled.
func TopicScore(a, b string) float64 {
	return jaccard(wordSet(a), wordSet(b))
}

func wordSet(s string) map[string]bool {
	fields := strings.Fields(strings.ToLower(s))
	out := make(map[string]bool, len(fields))
	for _, f := range fields {
		f = strings.Trim(f, ".,!?;:\"'()[]{}")
		if f != "" {
			out[f] = true
		}
	}
	return out
}

func jaccard(a, b map[string]bool) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	inter := 0
	for w := range a {
		if b[w] {
			inter++
		}
	}
	union := len(a) + len(b) - inter
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}

// ---- Tracker ------------------------------------------------------------

// Tracker owns claim creation, release, renewal and overlap queries. One
// Tracker is shared process-wide, wired through internal/app like
// internal/awareness.Tracker and internal/checks.Runner.
type Tracker struct {
	DB  *store.DB
	Log *slog.Logger

	notifyMu   sync.Mutex
	lastNotify map[string]float64 // "claimID:editorSessionID" -> last notified at
}

// New builds a Tracker.
func New(db *store.DB, log *slog.Logger) *Tracker {
	if log == nil {
		log = slog.Default()
	}
	return &Tracker{DB: db, Log: log, lastNotify: map[string]float64{}}
}

// Input is what a new claim needs. Exactly the fields docs/claims.md's model
// describes, plus the structured holder identity (session_id/attempt_id) that
// makes overlap/release queries a join instead of a string parse.
type Input struct {
	RepoKey    string
	ScopeKind  string
	Scope      string   // task id or topic text
	Paths      []string // glob list, only for ScopeKind==ScopePaths
	Holder     string
	HolderKind string // session | attempt | human
	SessionID  *int64
	AttemptID  *int64
	Agent      string
	Intent     string
	TTLMinutes int
	Auto       bool
}

// Create validates and inserts a new claim.
func (t *Tracker) Create(in Input) (*store.Claim, error) {
	if strings.TrimSpace(in.RepoKey) == "" {
		return nil, fmt.Errorf("repo_key is required")
	}
	if !ValidScopeKind(in.ScopeKind) {
		return nil, fmt.Errorf("scope_kind must be one of task, paths, topic")
	}
	if strings.TrimSpace(in.Holder) == "" {
		return nil, fmt.Errorf("holder is required")
	}
	scope := strings.TrimSpace(in.Scope)
	if in.ScopeKind == ScopePaths {
		globs := make([]string, 0, len(in.Paths))
		for _, p := range in.Paths {
			if p = strings.TrimSpace(p); p != "" {
				globs = append(globs, p)
			}
		}
		if len(globs) == 0 {
			return nil, fmt.Errorf("paths claims need at least one glob")
		}
		scope = EncodePaths(globs)
	}
	if scope == "" {
		return nil, fmt.Errorf("scope is required")
	}
	ttl := time.Duration(in.TTLMinutes) * time.Minute
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	if ttl < MinTTL {
		ttl = MinTTL
	}
	if ttl > MaxTTL {
		ttl = MaxTTL
	}
	now := store.Now()
	c := &store.Claim{
		RepoKey: in.RepoKey, ScopeKind: in.ScopeKind, Scope: scope,
		Holder: in.Holder, HolderKind: in.HolderKind, SessionID: in.SessionID, AttemptID: in.AttemptID,
		Agent: in.Agent, Intent: strings.TrimSpace(in.Intent), Auto: in.Auto,
		TTLSeconds: ttl.Seconds(), CreatedAt: now, ExpiresAt: now + ttl.Seconds(),
	}
	return t.DB.InsertClaim(c)
}

// Release ends one claim.
func (t *Tracker) Release(id int64) error {
	return t.DB.ReleaseClaim(id, store.Now())
}

// ReleaseAllForHolder releases every active claim a session holds — used by
// release_work's `all:true` and by session-end handling.
func (t *Tracker) ReleaseAllForSession(sessionID int64) (int64, error) {
	return t.DB.ReleaseClaimsForSession(sessionID, store.Now())
}

// ReleaseAllForAttempt releases every active claim a task attempt holds.
func (t *Tracker) ReleaseAllForAttempt(attemptID int64) (int64, error) {
	return t.DB.ReleaseClaimsForAttempt(attemptID, store.Now())
}

// Extend renews one claim by a fresh TTL (docs/claims.md's human "extend"
// action) — ttlMinutes<=0 reuses the claim's own original TTL.
func (t *Tracker) Extend(id int64, ttlMinutes int) (*store.Claim, error) {
	c, err := t.DB.Claim(id)
	if err != nil {
		return nil, err
	}
	if c.ReleasedAt != nil {
		return nil, fmt.Errorf("claim #%d was already released", id)
	}
	ttl := time.Duration(ttlMinutes) * time.Minute
	if ttl <= 0 {
		ttl = time.Duration(c.TTLSeconds) * time.Second
	}
	if ttl < MinTTL {
		ttl = MinTTL
	}
	if ttl > MaxTTL {
		ttl = MaxTTL
	}
	now := store.Now()
	if err := t.DB.RenewClaim(id, now+ttl.Seconds()); err != nil {
		return nil, err
	}
	return t.DB.Claim(id)
}

// RenewActivity extends every active claim a session holds by its own TTL —
// docs/claims.md's "renewed by activity" clause. Called from every hook event
// that already touches a session (PreToolUse/PostToolUse/UserPromptSubmit),
// so a claim never lapses under a session that is genuinely still working.
func (t *Tracker) RenewActivity(sessionID int64) {
	if sessionID == 0 {
		return
	}
	if _, err := t.DB.RenewClaimsForSession(sessionID, store.Now()); err != nil {
		t.Log.Warn("claims: could not renew activity", "session", sessionID, "err", err)
	}
}

// ActiveForRepo lists a repository's active claims, oldest first.
func (t *Tracker) ActiveForRepo(repoKey string) ([]*store.Claim, error) {
	if strings.TrimSpace(repoKey) == "" {
		return nil, nil
	}
	return t.DB.ActiveClaimsForRepo(repoKey, store.Now())
}

// All lists every active claim across every repository — the board-wide
// Claims panel's starting point.
func (t *Tracker) All() ([]*store.Claim, error) {
	return t.DB.ActiveClaims(store.Now())
}

// Sweep releases: expired-TTL claims, claims of a finished task attempt, and
// claims of a session that has ended — docs/claims.md's auto-release clause
// for the two cases that are not already handled synchronously
// (RenewActivity keeps a working session's claims alive; the hook handler
// releases a SessionEnd session immediately). Run every scheduler tick
// (internal/scheduler.Scheduler.Claims), the same "no dedicated goroutine"
// pattern internal/budget.Checker.Tick uses.
func (t *Tracker) Sweep() (released int64, err error) {
	now := store.Now()
	n1, err := t.DB.ExpireClaims(now)
	if err != nil {
		return released, err
	}
	n2, err := t.DB.ReleaseFinishedAttemptClaims(now)
	if err != nil {
		return released, err
	}
	n3, err := t.DB.ReleaseDeadSessionClaims(now)
	if err != nil {
		return released, err
	}
	return n1 + n2 + n3, nil
}

// ---- overlap detection -------------------------------------------------------

// OverlappingPathClaims returns every OTHER holder's active paths claim in
// repoKey whose glob set matches relPath — the PreToolUse edit-warning source
// (docs/claims.md point 4b).
func (t *Tracker) OverlappingPathClaims(repoKey, relPath string, excludeSessionID int64) ([]*store.Claim, error) {
	active, err := t.ActiveForRepo(repoKey)
	if err != nil {
		return nil, err
	}
	var out []*store.Claim
	for _, c := range active {
		if c.ScopeKind != ScopePaths {
			continue
		}
		if excludeSessionID != 0 && c.SessionID != nil && *c.SessionID == excludeSessionID {
			continue
		}
		for _, g := range DecodePaths(c.Scope) {
			if MatchGlob(g, relPath) {
				out = append(out, c)
				break
			}
		}
	}
	return out, nil
}

// OverlappingTopicClaims returns every active topic claim in repoKey whose
// scope text has >= TopicOverlapThreshold Jaccard word-overlap with text —
// what a new session/task launch dialog warns about before it ever starts
// (docs/claims.md point 4c).
func (t *Tracker) OverlappingTopicClaims(repoKey, text string) ([]*store.Claim, error) {
	if strings.TrimSpace(text) == "" {
		return nil, nil
	}
	active, err := t.ActiveForRepo(repoKey)
	if err != nil {
		return nil, err
	}
	var out []*store.Claim
	for _, c := range active {
		if c.ScopeKind != ScopeTopic {
			continue
		}
		if TopicScore(text, c.Scope) >= TopicOverlapThreshold {
			out = append(out, c)
		}
	}
	return out, nil
}

// ShouldNotifyOverlap is a 10-minute per-(claim, editor session) cooldown so
// the same overlap does not push-notify on every keystroke's PreToolUse —
// in-memory only, like internal/awareness.Tracker's resolution guard; losing
// it on a restart just means one extra notification, never a missed one.
func (t *Tracker) ShouldNotifyOverlap(claimID, editorSessionID int64) bool {
	key := fmt.Sprintf("%d:%d", claimID, editorSessionID)
	now := store.Now()
	t.notifyMu.Lock()
	defer t.notifyMu.Unlock()
	if t.lastNotify == nil {
		t.lastNotify = map[string]float64{}
	}
	if last, ok := t.lastNotify[key]; ok && now-last < 600 {
		return false
	}
	t.lastNotify[key] = now
	return true
}

// ---- rendering ----------------------------------------------------------

// BriefingSection renders the "active claims in this repo" block the
// SessionStart/UserPromptSubmit hook response appends alongside
// internal/awareness's own briefing (docs/claims.md point 4a). Empty when
// there is nothing to say.
func BriefingSection(cs []*store.Claim) string {
	if len(cs) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Active claims in this repository —\n")
	now := store.Now()
	for _, c := range cs {
		b.WriteString(fmt.Sprintf("• %s claimed %s %s ago", holderLabel(c), scopeLabel(c), roundAge(now-c.CreatedAt)))
		if c.Intent != "" {
			b.WriteString(fmt.Sprintf(": %q", c.Intent))
		}
		b.WriteString("\n")
	}
	b.WriteString("Coordinate before starting overlapping work: check the claim or ask the operator.")
	return clip(b.String(), BriefingMaxChars)
}

// EditWarning renders the PreToolUse advisory for one overlapping claim —
// docs/claims.md's own example wording: "session #143 (Codex) claimed
// frontend/src/sessions/** 12 min ago: 'rename button'".
func EditWarning(c *store.Claim) string {
	now := store.Now()
	msg := fmt.Sprintf("Claim board: %s claimed %s %s ago", holderLabel(c), scopeLabel(c), roundAge(now-c.CreatedAt))
	if c.Intent != "" {
		msg += fmt.Sprintf(": %q", c.Intent)
	}
	return msg + ". Coordinate with them before proceeding, or ask the operator."
}

func holderLabel(c *store.Claim) string {
	switch c.HolderKind {
	case "session":
		agent := c.Agent
		if agent == "" {
			agent = "agent"
		}
		id := int64(0)
		if c.SessionID != nil {
			id = *c.SessionID
		}
		return fmt.Sprintf("session #%d (%s) %q", id, agent, c.Holder)
	case "attempt":
		id := int64(0)
		if c.AttemptID != nil {
			id = *c.AttemptID
		}
		return fmt.Sprintf("task attempt #%d %q", id, c.Holder)
	default:
		return c.Holder
	}
}

func scopeLabel(c *store.Claim) string {
	switch c.ScopeKind {
	case ScopePaths:
		globs := DecodePaths(c.Scope)
		sort.Strings(globs)
		if len(globs) == 1 {
			return globs[0]
		}
		return strings.Join(globs, ", ")
	case ScopeTask:
		return "task #" + c.Scope
	default:
		return c.Scope
	}
}

func roundAge(seconds float64) string {
	m := int(seconds / 60)
	if m < 1 {
		return "<1m"
	}
	if m < 60 {
		return fmt.Sprintf("%dm", m)
	}
	return fmt.Sprintf("%dh%dm", m/60, m%60)
}

func clip(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return strings.TrimSpace(string(r[:max-1])) + "…"
}
