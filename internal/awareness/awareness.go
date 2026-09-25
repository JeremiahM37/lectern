// Package awareness gives interactive agent sessions visibility into what
// OTHER live sessions (and running task attempts) are doing in the same
// repository, so two agents working the same checkout — or two worktrees of
// it — don't duplicate or collide with each other's work. See
// docs/agent-events.md's "Cross-agent awareness" section for the full
// contract; this package is the store/logic half, called from the hook
// response path in internal/api/hooks_agentevents.go and exposed directly
// through GET /api/sessions/{id}/peers and the `active_work` MCP tool.
//
// It never blocks a hook response on a live git command: the one thing here
// that shells out (resolveSession) always runs on its own goroutine, kicked
// off by EnsureRepoKeyAsync and never awaited by the caller.
package awareness

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/executor"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// RepoKeyNone is the sentinel stored on a session once it is confirmed NOT
// to be a git repository, so resolution is not retried on every hook event
// for the lifetime of that session.
const RepoKeyNone = "none"

// Windows and limits from docs/agent-events.md's "Cross-agent awareness".
const (
	RecentEditsWindow  = 60 * time.Minute // peer summary: files edited in the last N
	EditWarnWindow     = 30 * time.Minute // PreToolUse warning: someone else touched this file in the last N
	BriefingTTL        = 30 * time.Minute // re-send a briefing even if unchanged after this long
	EditRetention      = 24 * time.Hour   // session_file_edits rows older than this are pruned
	BriefingMaxChars   = 1200
	PromptExcerptChars = 200
)

// TrackedTools are the file-mutating tools awareness follows for edit
// tracking and the overwrite/conflict warning.
var TrackedTools = map[string]bool{"Edit": true, "Write": true, "MultiEdit": true, "NotebookEdit": true}

// FilePathFromToolInput pulls the edited file's path out of a PreToolUse/
// PostToolUse tool_input for one of TrackedTools. NotebookEdit's schema
// carries notebook_path; every other tracked tool carries file_path.
// Returns ok=false for an untracked tool or a missing/blank path.
func FilePathFromToolInput(toolName string, input map[string]any) (string, bool) {
	if !TrackedTools[toolName] {
		return "", false
	}
	if toolName == "NotebookEdit" {
		if v, ok := input["notebook_path"].(string); ok && strings.TrimSpace(v) != "" {
			return v, true
		}
	}
	if v, ok := input["file_path"].(string); ok && strings.TrimSpace(v) != "" {
		return v, true
	}
	return "", false
}

// PromptExcerpt clips a UserPromptSubmit prompt to the ~200 chars a peer
// summary and card show — full enough to recognise the task, short enough
// to stay well under the briefing's own 1200-char budget.
func PromptExcerpt(prompt string) string {
	prompt = strings.TrimSpace(prompt)
	r := []rune(prompt)
	if len(r) <= PromptExcerptChars {
		return prompt
	}
	return strings.TrimSpace(string(r[:PromptExcerptChars])) + "…"
}

// ResolveRepoKey runs the one git command this whole package ever shells
// out for: `git -C workdir rev-parse --path-format=absolute
// --git-common-dir --show-toplevel`. Its two stdout lines are the git
// common dir (stable across every worktree of one repository) and the
// toplevel of THIS particular worktree (what rel_path is computed against).
// repoKey embeds targetID so two different hosts that happen to check out a
// repo at the same path never collide. ok is false for a non-git workdir, a
// dead target, or any other failure — never an error the caller must
// specially handle, since "not tracked" is always a safe fallback here.
func ResolveRepoKey(ctx context.Context, ex executor.Executor, targetID int64, workdir string) (repoKey, toplevel string, ok bool) {
	if ex == nil || strings.TrimSpace(workdir) == "" {
		return "", "", false
	}
	cmd := "git -C " + executor.ShellQuote(workdir) +
		" rev-parse --path-format=absolute --git-common-dir --show-toplevel"
	res, err := ex.Run(ctx, cmd, executor.RunOpts{Timeout: 15})
	if err != nil || res.RC != 0 {
		return "", "", false
	}
	lines := strings.Split(strings.TrimRight(res.Stdout, "\n"), "\n")
	if len(lines) < 2 {
		return "", "", false
	}
	commonDir := strings.TrimSpace(lines[0])
	top := strings.TrimSpace(lines[1])
	if commonDir == "" || top == "" {
		return "", "", false
	}
	return fmt.Sprintf("%d:%s", targetID, commonDir), top, true
}

// RelPath turns an edited file's absolute path into one relative to a
// session's repo_toplevel, so two worktrees of the same repository record
// (and compare) the same rel_path for the same logical file. ok is false
// for the toplevel directory itself or a path outside it (a symlinked
// context file, say) — awareness only tracks files that are genuinely part
// of the repo.
func RelPath(toplevel, abs string) (string, bool) {
	toplevel = strings.TrimRight(strings.TrimSpace(toplevel), "/")
	abs = strings.TrimSpace(abs)
	if toplevel == "" || abs == "" || abs == toplevel {
		return "", false
	}
	prefix := toplevel + "/"
	if !strings.HasPrefix(abs, prefix) {
		return "", false
	}
	return strings.TrimPrefix(abs, prefix), true
}

// Tracker owns awareness's DB/executor access and the in-flight resolution
// guard. One Tracker is shared process-wide (wired through internal/app,
// same as internal/checks.Runner).
type Tracker struct {
	DB  *store.DB
	Reg *executor.Registry
	Log *slog.Logger

	mu        sync.Mutex
	resolving map[int64]bool // session id currently resolving in the background
}

// New builds a Tracker.
func New(db *store.DB, reg *executor.Registry, log *slog.Logger) *Tracker {
	if log == nil {
		log = slog.Default()
	}
	return &Tracker{DB: db, Reg: reg, Log: log, resolving: map[int64]bool{}}
}

// EnsureRepoKeyAsync kicks off a background repo-key resolution for a
// session that does not have one yet, and returns immediately without
// waiting for it. This is the ONLY thing in this package allowed to shell
// out, and it must NEVER be called from inside a hook's HTTP response —
// see the package doc. A session that is already resolved (RepoKey set,
// even to RepoKeyNone) or already has a resolution in flight is a no-op, so
// calling this on every hook event is cheap and safe.
func (t *Tracker) EnsureRepoKeyAsync(sess *store.Session) {
	if sess == nil || sess.RepoKey != "" || strings.TrimSpace(sess.Workdir) == "" {
		return
	}
	t.mu.Lock()
	if t.resolving == nil {
		t.resolving = map[int64]bool{}
	}
	if t.resolving[sess.ID] {
		t.mu.Unlock()
		return
	}
	t.resolving[sess.ID] = true
	t.mu.Unlock()

	id, targetID, workdir, projectID := sess.ID, sess.TargetID, sess.Workdir, sess.ProjectID
	go func() {
		defer func() {
			t.mu.Lock()
			delete(t.resolving, id)
			t.mu.Unlock()
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		t.resolveSession(ctx, id, targetID, workdir, projectID)
	}()
}

// resolveSession is the background body EnsureRepoKeyAsync schedules. On
// success it also backfills the session's project (if any and not already
// resolved) with the SAME repo_key/toplevel, at no extra git cost — an
// attempt's worktree is always cut from that project's repository, so this
// one resolution is enough to make that project's running attempts visible
// as peers from here on (see Peers in peers.go).
func (t *Tracker) resolveSession(ctx context.Context, sessionID, targetID int64, workdir string, projectID *int64) {
	target, err := t.DB.Target(targetID)
	if err != nil {
		return
	}
	ex, err := t.Reg.For(target)
	if err != nil {
		return
	}
	key, top, ok := ResolveRepoKey(ctx, ex, targetID, workdir)
	if !ok {
		if err := t.DB.Update("sessions", sessionID, map[string]any{"repo_key": RepoKeyNone}); err != nil {
			t.Log.Warn("awareness: could not record non-git session", "session", sessionID, "err", err)
		}
		return
	}
	if err := t.DB.Update("sessions", sessionID, map[string]any{"repo_key": key, "repo_toplevel": top}); err != nil {
		t.Log.Warn("awareness: could not record repo key", "session", sessionID, "err", err)
		return
	}
	if projectID == nil {
		return
	}
	proj, err := t.DB.Project(*projectID)
	if err != nil || proj.RepoKey != "" {
		return
	}
	if err := t.DB.Update("projects", proj.ID, map[string]any{"repo_key": key, "repo_toplevel": top}); err != nil {
		t.Log.Warn("awareness: could not backfill project repo key", "project", proj.ID, "err", err)
	}
}

// ResolveNow synchronously resolves (and persists) a session's repo_key,
// unlike EnsureRepoKeyAsync — safe ONLY from a normal API handler, never a
// hook response path (see the package doc). internal/api/claims.go's
// session-scoped claim creation is the first caller: a claim needs its
// repo_key at the moment it is made, and creating one is an explicit request,
// not a hook Claude/Codex is waiting on a fast reply to.
func (t *Tracker) ResolveNow(ctx context.Context, sess *store.Session) (repoKey, toplevel string, ok bool) {
	if sess.RepoKey != "" {
		return sess.RepoKey, sess.RepoToplevel, sess.RepoKey != RepoKeyNone
	}
	target, err := t.DB.Target(sess.TargetID)
	if err != nil {
		return "", "", false
	}
	ex, err := t.Reg.For(target)
	if err != nil {
		return "", "", false
	}
	key, top, ok := ResolveRepoKey(ctx, ex, sess.TargetID, sess.Workdir)
	if !ok {
		_ = t.DB.Update("sessions", sess.ID, map[string]any{"repo_key": RepoKeyNone})
		return "", "", false
	}
	if err := t.DB.Update("sessions", sess.ID, map[string]any{"repo_key": key, "repo_toplevel": top}); err != nil {
		t.Log.Warn("awareness: could not record repo key", "session", sess.ID, "err", err)
		return "", "", false
	}
	sess.RepoKey, sess.RepoToplevel = key, top
	return key, top, true
}

// ResolveProjectRepoKey synchronously resolves (and persists) a PROJECT's
// repo_key directly from its own target/repo_path, independent of any
// session — for a caller (claim creation "for" a project, or a human's
// "claim for me") that has no session context to resolve through. Same
// hook-response restriction as ResolveNow.
func (t *Tracker) ResolveProjectRepoKey(ctx context.Context, proj *store.Project) (repoKey, toplevel string, ok bool) {
	if proj.RepoKey != "" {
		return proj.RepoKey, proj.RepoToplevel, true
	}
	target, err := t.DB.Target(proj.TargetID)
	if err != nil {
		return "", "", false
	}
	ex, err := t.Reg.For(target)
	if err != nil {
		return "", "", false
	}
	key, top, ok := ResolveRepoKey(ctx, ex, proj.TargetID, proj.RepoPath)
	if !ok {
		return "", "", false
	}
	if err := t.DB.Update("projects", proj.ID, map[string]any{"repo_key": key, "repo_toplevel": top}); err != nil {
		t.Log.Warn("awareness: could not record project repo key", "project", proj.ID, "err", err)
		return "", "", false
	}
	proj.RepoKey, proj.RepoToplevel = key, top
	return key, top, true
}

// RecordEdit stores the latest edit time for a tracked-tool file path
// against this session's repo, then opportunistically prunes rows older
// than EditRetention (docs/agent-events.md: "pruning rows older than 24h").
// A session whose repo_key is not resolved yet (still "" — see
// EnsureRepoKeyAsync) or confirmed non-git (RepoKeyNone) records nothing;
// there is nothing wrong in that — the very next hook event for this
// session tries again once resolution has had time to land.
func (t *Tracker) RecordEdit(sess *store.Session, absPath string) {
	if sess == nil || sess.RepoKey == "" || sess.RepoKey == RepoKeyNone || absPath == "" {
		return
	}
	rel, ok := RelPath(sess.RepoToplevel, absPath)
	if !ok {
		return
	}
	now := store.Now()
	if err := t.DB.UpsertSessionFileEdit(sess.ID, sess.RepoKey, rel, now); err != nil {
		t.Log.Warn("awareness: could not record file edit", "session", sess.ID, "err", err)
		return
	}
	_ = t.DB.PruneSessionFileEdits(now - EditRetention.Seconds())
}

// RecordPrompt stores the latest UserPromptSubmit excerpt on the session
// row — the "what is this session working on" signal peers.go reads.
func (t *Tracker) RecordPrompt(sess *store.Session, prompt string) {
	if sess == nil {
		return
	}
	excerpt := PromptExcerpt(prompt)
	now := store.Now()
	if err := t.DB.Update("sessions", sess.ID, map[string]any{
		"last_prompt_excerpt": excerpt, "last_prompt_at": now,
	}); err != nil {
		t.Log.Warn("awareness: could not record prompt", "session", sess.ID, "err", err)
		return
	}
	sess.LastPromptExcerpt = excerpt
	sess.LastPromptAt = &now
}
