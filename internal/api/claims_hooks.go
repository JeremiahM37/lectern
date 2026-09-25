package api

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/agentevents"
	"github.com/JeremiahM37/lectern/v2/internal/awareness"
	"github.com/JeremiahM37/lectern/v2/internal/claims"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// claimsAdditionalContext is the Claim board's half of the hook response
// (docs/claims.md point 4): SessionStart/UserPromptSubmit list active claims
// in the repo, PreToolUse warns about an overlapping claim (and pushes an
// alert the first time it sees a given (claim, editor) pair), and every
// activity-bearing event renews the session's own claims so a working
// session's claims never lapse under it. It never shells out and is safe on
// a hook response path, same as awarenessAdditionalContext.
func (s *Server) claimsAdditionalContext(sess *store.Session, event string, body []byte) string {
	if s.Claims == nil {
		return ""
	}
	var in awarenessHookInput
	if len(body) > 0 {
		_ = json.Unmarshal(body, &in)
	}
	if in.ToolInput == nil {
		in.ToolInput = map[string]any{}
	}
	switch event {
	case agentevents.EventSessionStart, agentevents.EventUserPromptSubmit:
		if !s.awarenessSettingOn("claims_briefing") {
			return ""
		}
		if sess.RepoKey == "" || sess.RepoKey == awareness.RepoKeyNone {
			return ""
		}
		active, err := s.Claims.ActiveForRepo(sess.RepoKey)
		if err != nil {
			return ""
		}
		return claims.BriefingSection(active)
	case agentevents.EventPreToolUse:
		s.Claims.RenewActivity(sess.ID)
		text := s.claimsEditWarning(sess, in)
		return text
	case agentevents.EventPostToolUse:
		s.Claims.RenewActivity(sess.ID)
		if absPath, ok := awareness.FilePathFromToolInput(in.ToolName, in.ToolInput); ok {
			s.maybeAutoClaimPath(sess, absPath)
		}
		return ""
	default:
		return ""
	}
}

// claimsEditWarning renders the PreToolUse advisory for every active paths
// claim that overlaps the file about to be edited, and pushes a one-time (per
// claim, per editor) alert to the operator (docs/claims.md point 5).
func (s *Server) claimsEditWarning(sess *store.Session, in awarenessHookInput) string {
	if !s.awarenessSettingOn("claims_edit_warning") {
		return ""
	}
	if sess.RepoKey == "" || sess.RepoKey == awareness.RepoKeyNone {
		return ""
	}
	absPath, ok := awareness.FilePathFromToolInput(in.ToolName, in.ToolInput)
	if !ok {
		return ""
	}
	rel, ok := awareness.RelPath(sess.RepoToplevel, absPath)
	if !ok {
		return ""
	}
	overlaps, err := s.Claims.OverlappingPathClaims(sess.RepoKey, rel, sess.ID)
	if err != nil || len(overlaps) == 0 {
		return ""
	}
	texts := make([]string, 0, len(overlaps))
	for _, c := range overlaps {
		texts = append(texts, claims.EditWarning(c))
		if s.Claims.ShouldNotifyOverlap(c.ID, sess.ID) {
			s.notifyClaimOverlap(sess, c, rel)
		}
	}
	return strings.Join(texts, "\n")
}

// notifyClaimOverlap pushes the operator alert docs/claims.md point 5 asks
// for: an agent started editing inside someone else's active claim.
func (s *Server) notifyClaimOverlap(editor *store.Session, c *store.Claim, rel string) {
	if s.Notifier == nil {
		return
	}
	title := fmt.Sprintf("%s editing inside %s's claim", editor.Name, c.Holder)
	body := fmt.Sprintf("%s — claimed by %s", rel, c.Holder)
	if c.Intent != "" {
		body += ": " + c.Intent
	}
	s.Notifier.Notify(title, body, "/#session/"+strconv.FormatInt(editor.ID, 10), nil)
}

// autoClaimTaskScope is docs/claims.md point 3's other automatic claim:
// "tasks automatically claim their task scope." Called right after a task
// attempt is created (internal/api/tasks.go's queueTask) — best-effort, like
// every awareness/claims side effect: a project whose repo_key cannot be
// resolved yet just does not get a task claim this attempt, exactly the
// awareness pattern for an unresolved repo.
func (s *Server) autoClaimTaskScope(task *store.Task, att *store.Attempt) {
	if s.Claims == nil || att == nil {
		return
	}
	proj, err := s.DB.Project(task.ProjectID)
	if err != nil {
		return
	}
	repoKey := proj.RepoKey
	if repoKey == "" && s.Awareness != nil {
		repoKey, _, _ = s.Awareness.ResolveProjectRepoKey(context.Background(), proj)
	}
	if repoKey == "" {
		return
	}
	if _, err := s.Claims.Create(claims.Input{
		RepoKey: repoKey, ScopeKind: claims.ScopeTask, Scope: strconv.FormatInt(task.ID, 10),
		Holder: task.Title, HolderKind: "attempt", AttemptID: &att.ID, Agent: att.Agent,
		Intent: task.Title, TTLMinutes: int(claims.MaxTTL.Minutes()), Auto: true,
	}); err != nil {
		s.Log.Warn("claims: could not create automatic task-scope claim", "task", task.ID, "attempt", att.ID, "err", err)
	}
}

// maybeAutoClaimPath is docs/claims.md point 3: the first PostToolUse edit a
// session makes gets it an implicit, lightweight paths claim on that file's
// directory. "First" is enforced by checking the session does not already
// hold an automatic paths claim — later edits to other directories do not
// each get their own, keeping this a single soft "this session is active
// around here" signal rather than a growing pile of claims.
func (s *Server) maybeAutoClaimPath(sess *store.Session, absPath string) {
	if !s.awarenessSettingOn("claims_auto_paths") {
		return
	}
	if sess.RepoKey == "" || sess.RepoKey == awareness.RepoKeyNone {
		return
	}
	rel, ok := awareness.RelPath(sess.RepoToplevel, absPath)
	if !ok {
		return
	}
	existing, err := s.DB.ActiveClaimsForSession(sess.ID, store.Now())
	if err != nil {
		return
	}
	for _, c := range existing {
		if c.Auto && c.ScopeKind == claims.ScopePaths {
			return
		}
	}
	glob := claims.AutoClaimGlob(rel)
	if _, err := s.Claims.Create(claims.Input{
		RepoKey: sess.RepoKey, ScopeKind: claims.ScopePaths, Paths: []string{glob},
		Holder: sess.Name, HolderKind: "session", SessionID: &sess.ID, Agent: sess.Agent,
		Intent: sess.LastPromptExcerpt, TTLMinutes: int(claims.DefaultTTL.Minutes()), Auto: true,
	}); err != nil {
		s.Log.Warn("claims: could not create automatic paths claim", "session", sess.ID, "err", err)
	}
}
