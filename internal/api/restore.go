package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/sessions"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// Restore is one list of everything that can come back — sessions someone
// closed, archived, that exited on their own, or that a restart interrupted —
// and one action that does the right thing for each. The older per-case paths
// (resume-recent, restore tracking, unarchive, the history picker) remain; this
// chooses between them so nobody has to know which one applies.

// restorePlan is what reopening a record would do, decided from local state
// only; POST /reopen repeats the target-side checks before acting.
type restorePlan struct {
	Reason      string `json:"reason"`
	ReasonLabel string `json:"reason_label"`
	Action      string `json:"action"` // resume|track|relaunch|shell|handoff|history|fresh
	ActionLabel string `json:"action_label"`
	Note        string `json:"note"`
	// LikelyMatch marks a resume of an adopted session's conversation that
	// was matched by folder, agent and time rather than bound at launch.
	LikelyMatch bool `json:"likely_match,omitempty"`
}

type restorableView struct {
	*sessionView
	restorePlan
	Preview      string `json:"preview"`
	PreviewKind  string `json:"preview_kind,omitempty"` // prompt|screen
	SupersededBy int64  `json:"superseded_by,omitempty"`
	ReopenURL    string `json:"reopen_url"`
}

func (s *Server) latestWrap(id int64) *store.Wrap {
	wraps, err := s.DB.SessionWraps(id)
	if err != nil || len(wraps) == 0 {
		return nil
	}
	return wraps[0]
}

func endReason(row *store.Session) (string, string) {
	if row.EndedAt == nil {
		return "restart", "Interrupted by a restart"
	}
	reason := row.EndReason
	if reason == "" && row.Origin == "discovered" && row.Status != sessions.StatusDead {
		reason = sessions.EndReleased
	}
	switch {
	case row.ArchivedAt != nil:
		return "archived", "Archived"
	case reason == sessions.EndReleased:
		return reason, "Tracking stopped; its terminal kept running"
	case reason == sessions.EndStopped:
		return reason, "Ended from Lectern"
	case reason == sessions.EndExited:
		return reason, "Exited on its own"
	case reason == sessions.EndRestart:
		return reason, "Lost to a restart"
	case reason == sessions.EndHandedOff:
		return reason, "Handed off"
	case reason == sessions.EndDismissed:
		return reason, "Dismissed"
	}
	return "closed", "Closed"
}

func (s *Server) planRestore(row *store.Session) restorePlan {
	p := restorePlan{}
	p.Reason, p.ReasonLabel = endReason(row)
	bound := row.NativeRecoveryCID != "" || row.ResumeID != ""
	resumable, pickable := false, row.Agent == "claude" || row.Agent == "codex"
	if config, err := s.Sessions.SessionLaunchConfiguration(row); err == nil {
		resumable = bound && len(config.Spec.ResumeIDArgs) > 0 && config.Spec.ExactConversations()
		// A catalog agent whose saved conversations Lectern can list gets the
		// same history picker as Claude and Codex.
		pickable = pickable || (config.Spec.Sessions != nil && len(config.Spec.ResumeIDArgs) > 0)
	}
	wrap := s.latestWrap(row.ID) != nil
	switch {
	case row.EndedAt == nil && row.Agent == "shell":
		p.Action, p.ActionLabel, p.Note = "shell", "New shell here", "Opens a new shell in the same folder. The old scrollback was lost with the restart."
	case row.EndedAt == nil && resumable:
		p.Action, p.ActionLabel, p.Note = "resume", "Resume", "Relaunches its saved conversation."
	case row.EndedAt == nil && wrap:
		p.Action, p.ActionLabel, p.Note = "relaunch", "Relaunch", "No saved conversation is bound; starts fresh in the same folder, primed with its last handoff."
	case row.EndedAt == nil:
		p.Action, p.ActionLabel, p.Note = "relaunch", "Relaunch", "No saved conversation is bound; starts fresh in the same folder."
	case row.Origin == "discovered" && row.Status != sessions.StatusDead && row.TrackingIdentity != "":
		p.Action, p.ActionLabel, p.Note = "track", "Track again", "Its terminal is still running; Lectern watches it again. Nothing is restarted."
	case row.Agent == "shell":
		p.Action, p.ActionLabel, p.Note = "shell", "New shell here", "Opens a new shell in the same folder. Shell scrollback is not restored."
	case resumable && !(row.Origin == "discovered" && row.Status != sessions.StatusDead):
		p.Action, p.ActionLabel, p.Note = "resume", "Resume", "Continues the saved conversation. Its terminal scrollback is not restored."
	case row.ResumeGuess != "" && row.Origin == "discovered" && row.Status == sessions.StatusDead:
		p.Action, p.ActionLabel, p.Note = "resume", "Resume", "Continues the one conversation in its folder that was last written when this session was last active. If it is not the right one, pick another from Saved conversations."
		p.LikelyMatch = true
	case wrap:
		p.Action, p.ActionLabel, p.Note = "handoff", "Continue from handoff", "Starts a new session primed with its last handoff."
	case pickable:
		p.Action, p.ActionLabel, p.Note = "history", "Choose history", "No conversation is bound to this record; pick one from its folder's saved conversations."
	default:
		p.Action, p.ActionLabel, p.Note = "fresh", "Start fresh here", "Nothing was saved to resume; starts a new session in the same folder."
	}
	if row.ArchivedAt != nil {
		p.Note = "Unarchives it. " + p.Note
	}
	return p
}

// sessionPreview is the last thing said in a session, from the hook-recorded
// prompt when there is one, otherwise the last meaningful screen line.
func sessionPreview(row *store.Session) (string, string) {
	if text := strings.TrimSpace(row.LastPromptExcerpt); text != "" {
		return clipRunes(oneLineText(text), 200), "prompt"
	}
	lines := strings.Split(row.PaneTail, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" || strings.Trim(line, ">❯$%#·─━│┃╭╮╰╯-=_ ") == "" {
			continue
		}
		line = strings.TrimSpace(strings.TrimLeft(line, ">❯$% "))
		return clipRunes(line, 200), "screen"
	}
	return "", ""
}

func oneLineText(text string) string { return strings.Join(strings.Fields(text), " ") }

func clipRunes(text string, n int) string {
	r := []rune(text)
	if len(r) <= n {
		return text
	}
	return string(r[:n-1]) + "…"
}

// supersededBy maps a record to the newer session that already continued it:
// one resuming the same native conversation, or the successor of its wrap.
func (s *Server) supersededBy(rows []*store.Session) map[int64]int64 {
	out, err := s.DB.ReopenedAs()
	if err != nil {
		out = map[int64]int64{}
	}
	refs, err := s.DB.ConversationRefs()
	if err != nil {
		return out
	}
	byID := map[int64]store.ConversationRef{}
	for _, ref := range refs {
		byID[ref.ID] = ref
	}
	for _, row := range rows {
		mine, ok := byID[row.ID]
		if ok && out[row.ID] == 0 {
			for _, other := range refs {
				if other.ID <= row.ID || other.TargetID != row.TargetID || other.Agent != row.Agent || out[row.ID] > other.ID {
					continue
				}
				for _, a := range mine.CIDs {
					for _, b := range other.CIDs {
						if a == b {
							out[row.ID] = other.ID
						}
					}
				}
			}
		}
		if out[row.ID] == 0 {
			if wraps, err := s.DB.SessionWraps(row.ID); err == nil {
				for _, wrap := range wraps {
					if wrap.NextSessionID != nil && *wrap.NextSessionID > row.ID {
						out[row.ID] = *wrap.NextSessionID
						break
					}
				}
			}
		}
	}
	return out
}

func (s *Server) restorableView(row *store.Session, superseded int64) *restorableView {
	preview, kind := sessionPreview(row)
	return &restorableView{sessionView: s.sessionView(row), restorePlan: s.planRestore(row),
		Preview: preview, PreviewKind: kind, SupersededBy: superseded,
		ReopenURL: fmt.Sprintf("/api/sessions/%d/reopen", row.ID)}
}

// GET /api/sessions/restorable?q=&limit=&all=true
//
// Newest first. Records a later session already continued are left out unless
// all=true. q matches every word against name, project, agent, machine,
// folder, group, reason and the last message.
func (s *Server) restorableSessions(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > 200 {
			httpError(w, http.StatusBadRequest, "limit must be between 1 and 200")
			return
		}
		limit = value
	}
	all := r.URL.Query().Get("all") == "true"
	terms := strings.Fields(strings.ToLower(r.URL.Query().Get("q")))
	rows, err := s.DB.RestorableSessions(500)
	if err != nil {
		respondErr(w, err)
		return
	}
	superseded := s.supersededBy(rows)
	out := []*restorableView{}
	for _, row := range rows {
		if superseded[row.ID] != 0 && !all {
			continue
		}
		view := s.restorableView(row, superseded[row.ID])
		if len(terms) > 0 {
			haystack := strings.ToLower(strings.Join([]string{"#" + strconv.FormatInt(row.ID, 10), row.Name, row.ProjectName,
				row.Agent, row.Model, row.TargetName, row.Workdir, row.GroupPath, view.ReasonLabel, view.Preview}, " "))
			match := true
			for _, term := range terms {
				if !strings.Contains(haystack, term) {
					match = false
					break
				}
			}
			if !match {
				continue
			}
		}
		out = append(out, view)
		if len(out) >= limit {
			break
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, out)
}

type reopenIn struct {
	// Agent, Model or ProfileID continue the session somewhere else: a new
	// session with that agent/model/profile, primed with the last handoff or
	// the recent conversation. Empty reopens it as it was.
	Agent     string `json:"agent"`
	Model     string `json:"model"`
	ProfileID int64  `json:"profile_id"`
	Name      string `json:"name"`
}

type reopenOut struct {
	Session  *sessionView `json:"session"`
	SourceID int64        `json:"source_id"`
	Action   string       `json:"action"`
	Message  string       `json:"message"`
}

func needsHistory(w http.ResponseWriter, format string, args ...any) {
	writeJSON(w, http.StatusConflict, map[string]any{"detail": fmt.Sprintf(format, args...), "needs_history": true})
}

// POST /api/sessions/{id}/reopen — one tap to bring a record back.
func (s *Server) reopenSession(w http.ResponseWriter, r *http.Request) {
	row, ok := s.sessionParam(w, r)
	if !ok {
		return
	}
	var in reopenIn
	if err := decodeBody(r, &in); err != nil {
		httpError(w, http.StatusUnprocessableEntity, "%s", err)
		return
	}
	name := strings.TrimSpace(in.Name)
	if len(name) > 160 {
		httpError(w, http.StatusUnprocessableEntity, "name exceeds 160 bytes")
		return
	}
	if row.EndedAt == nil && row.Status != sessions.StatusInterrupted {
		httpError(w, http.StatusConflict, "this session is still running; open it instead")
		return
	}
	if row.SetupState == "failed" || row.EndReason == sessions.EndFailed {
		httpError(w, http.StatusConflict, "this launch never started; start a new session instead")
		return
	}
	if in.Agent != "" {
		if _, ok := sessions.Find(s.agentSpecs(), in.Agent); !ok {
			httpError(w, http.StatusUnprocessableEntity, "unknown agent %q — define it in /api/agents", in.Agent)
			return
		}
	}
	elsewhere := (in.Agent != "" && in.Agent != row.Agent) || (in.Model != "" && in.Model != row.Model) || in.ProfileID != 0
	if elsewhere {
		s.reopenElsewhere(w, r, row, in, name)
		return
	}
	plan := s.planRestore(row)
	if plan.Action == "history" && row.Origin == "discovered" {
		// A lost adopted session may still be matched now, if it ended before
		// matching existed or the target was unreachable at the time.
		if cid := s.Sessions.MatchLostAdopted(r.Context(), row.ID); cid != "" {
			row.ResumeGuess = cid
			plan = s.planRestore(row)
		}
	}
	if plan.Action == "history" {
		needsHistory(w, "no conversation is bound to this record; choose one from its saved conversations")
		return
	}
	// Unarchive first, and put the archive back if reopening then fails, so a
	// failed attempt does not quietly move the record.
	archivedAt := row.ArchivedAt
	if archivedAt != nil {
		if _, err := s.Sessions.Unarchive(row.ID); err != nil {
			httpError(w, http.StatusConflict, "%s", err)
			return
		}
		row.ArchivedAt = nil
	}
	fail := func(status int, history bool, err error) {
		if archivedAt != nil {
			_ = s.DB.Update("sessions", row.ID, map[string]any{"archived_at": *archivedAt})
		}
		if history {
			needsHistory(w, "%s", err)
			return
		}
		httpError(w, status, "%s", err)
	}
	var next *store.Session
	var err error
	message := ""
	switch plan.Action {
	case "track":
		next, err = s.Sessions.Restore(r.Context(), row.ID)
		message = "Tracking restored; its terminal kept running."
	case "resume":
		cid, bindErr := s.boundNativeCID(r, row)
		if bindErr != nil && plan.LikelyMatch {
			if _, err := s.nativeConversationData(r, row, row.ResumeGuess); err == nil {
				cid, bindErr = row.ResumeGuess, nil
			}
		}
		if bindErr != nil {
			if row.EndedAt == nil {
				// The restart took the terminal and the transcript cannot be
				// found: end the record so the history picker can continue it.
				if _, retireErr := s.Sessions.Retire(r.Context(), row.ID); retireErr != nil {
					fail(http.StatusConflict, false, retireErr)
					return
				}
			}
			fail(http.StatusConflict, true, bindErr)
			return
		}
		if row.EndedAt == nil {
			next, err = s.Sessions.Relaunch(r.Context(), row.ID, "")
		} else {
			next, err = s.Sessions.ResumeConversation(r.Context(), row.ID, cid, firstNonEmptyStr(name, row.Name))
		}
		message = "Resumed its saved conversation."
		if plan.LikelyMatch {
			message = "Resumed the conversation matched to it by folder and time. If it is the wrong one, pick another from Saved conversations."
		}
	case "relaunch":
		prime := ""
		message = "Started again in the same folder. No conversation was saved, so it starts fresh."
		if wrap := s.latestWrap(row.ID); wrap != nil {
			prime = sessions.ResumePrompt(row.ProjectName, wrap.Summary, "")
			message = "Started again in the same folder, primed with its last handoff."
		}
		next, err = s.Sessions.Relaunch(r.Context(), row.ID, prime)
	case "shell":
		next, err = s.Sessions.ReopenShell(r.Context(), row.ID)
		message = "Opened a new shell in the same folder. The old scrollback is not restored."
	case "handoff":
		wrap := s.latestWrap(row.ID)
		next, err = s.Sessions.Continue(r.Context(), row.ID, sessions.ContinueOpts{Name: firstNonEmptyStr(name, row.Name), Context: wrap.Summary, WrapID: wrap.ID})
		message = "Started a new session primed with its last handoff."
	default:
		next, err = s.Sessions.Continue(r.Context(), row.ID, sessions.ContinueOpts{Name: firstNonEmptyStr(name, row.Name)})
		message = "Started a new session in the same folder. Nothing was saved to resume."
	}
	if err != nil {
		fail(http.StatusConflict, false, err)
		return
	}
	s.markReopened(row.ID, next.ID)
	writeJSON(w, http.StatusCreated, reopenOut{Session: s.sessionView(next), SourceID: row.ID, Action: plan.Action, Message: message})
}

// reopenElsewhere continues a closed session with another agent, model or
// launch profile. Native conversation formats are not interchangeable, so the
// successor gets context, not the conversation: the last handoff if there is
// one, else the end of the saved conversation, else the last screen.
func (s *Server) reopenElsewhere(w http.ResponseWriter, r *http.Request, row *store.Session, in reopenIn, name string) {
	if row.Agent == "shell" {
		httpError(w, http.StatusUnprocessableEntity, "a shell has no conversation to continue in an agent")
		return
	}
	if row.EndedAt != nil && row.Origin == "discovered" && row.Status != sessions.StatusDead {
		httpError(w, http.StatusConflict, "its terminal is still running untracked; track it again, then use Switch")
		return
	}
	brief, kind, wrapID := s.reopenContext(r, row)
	next, err := s.Sessions.Continue(r.Context(), row.ID, sessions.ContinueOpts{
		Agent: in.Agent, Model: in.Model, ProfileID: in.ProfileID, Name: name, Context: brief, WrapID: wrapID, Excerpt: wrapID == 0})
	if err != nil {
		httpError(w, http.StatusConflict, "%s", err)
		return
	}
	s.markReopened(row.ID, next.ID)
	message := "Started " + next.Agent + " fresh in the same folder; nothing was saved to carry over."
	if kind != "" {
		message = "Started " + next.Agent + " primed with " + kind + ". Its native conversation is not carried over."
	}
	writeJSON(w, http.StatusCreated, reopenOut{Session: s.sessionView(next), SourceID: row.ID, Action: "continue", Message: message})
}

// markReopened records the replacement so Restore stops offering the record
// it replaced. Reopening in place (tracking, a relaunch) has nothing to mark.
func (s *Server) markReopened(sourceID, nextID int64) {
	if nextID != sourceID {
		_ = s.DB.Update("sessions", sourceID, map[string]any{"reopened_as": nextID})
	}
}

const transcriptBudget = 12000

// reopenContext picks the best context a closed session left behind.
func (s *Server) reopenContext(r *http.Request, row *store.Session) (string, string, int64) {
	if wrap := s.latestWrap(row.ID); wrap != nil {
		return wrap.Summary, "its last handoff", wrap.ID
	}
	if cid := firstNonEmptyStr(row.NativeRecoveryCID, row.ResumeID); cid != "" {
		if data, err := s.nativeConversationData(r, row, cid); err == nil {
			var messages []struct{ Role, Text string }
			if json.Unmarshal(data["messages"], &messages) == nil {
				if excerpt := transcriptExcerpt(messages); excerpt != "" {
					return excerpt, "the end of its conversation", 0
				}
			}
		}
	}
	if tail := strings.TrimSpace(row.PaneTail); tail != "" {
		return "Last screen of the previous " + row.Agent + " session:\n\n" + tail, "its last screen", 0
	}
	return "", "", 0
}

// transcriptExcerpt keeps the newest user and assistant messages that fit the
// budget, oldest first, labelled as an excerpt rather than a handoff.
func transcriptExcerpt(messages []struct{ Role, Text string }) string {
	var kept []string
	used := 0
	for i := len(messages) - 1; i >= 0; i-- {
		m := messages[i]
		if m.Role != "user" && m.Role != "assistant" {
			continue
		}
		text := strings.TrimSpace(m.Text)
		if text == "" {
			continue
		}
		text = clipRunes(text, 2000)
		entry := strings.ToUpper(m.Role[:1]) + m.Role[1:] + ": " + text
		if used+len(entry) > transcriptBudget && len(kept) > 0 {
			break
		}
		kept = append(kept, entry)
		used += len(entry)
	}
	if len(kept) == 0 {
		return ""
	}
	for i, j := 0, len(kept)-1; i < j; i, j = i+1, j-1 {
		kept[i], kept[j] = kept[j], kept[i]
	}
	return "Last messages of the previous conversation, oldest first:\n\n" + strings.Join(kept, "\n\n")
}

// Sessions restart recovery relaunched on its own, for the "Relaunched N
// sessions after restart" notice. Dismissing hides the current ones; a later
// restart shows its own.
const relaunchDismissedKey = "relaunch_notice_dismissed_at"

// GET /api/sessions/relaunched
func (s *Server) relaunchedSessions(w http.ResponseWriter, r *http.Request) {
	since, _ := strconv.ParseFloat(s.DB.Setting(relaunchDismissedKey), 64)
	if week := store.Now() - 7*24*3600; since < week {
		since = week
	}
	rows, err := s.DB.RelaunchedSessions(since)
	if err != nil {
		respondErr(w, err)
		return
	}
	out := make([]*sessionView, 0, len(rows))
	for _, row := range rows {
		out = append(out, s.sessionView(row))
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, out)
}

// POST /api/sessions/relaunched/dismiss
func (s *Server) dismissRelaunched(w http.ResponseWriter, r *http.Request) {
	if err := s.DB.SetSetting(relaunchDismissedKey, strconv.FormatFloat(store.Now(), 'f', 3, 64)); err != nil {
		respondErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"dismissed": true})
}
