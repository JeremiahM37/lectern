package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"path"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/executor"
	"github.com/JeremiahM37/lectern/v2/internal/nativeidentity"
	"github.com/JeremiahM37/lectern/v2/internal/sessions"
	"github.com/JeremiahM37/lectern/v2/internal/shellq"
	"github.com/JeremiahM37/lectern/v2/internal/store"
	"github.com/JeremiahM37/lectern/v2/internal/worktree"
)

// Kept as a package alias for native search, which shares the record decoder.
var nativeRecordsScript = nativeidentity.RecordsScript

func (s *Server) nativeConversationData(r *http.Request, row *store.Session, cid string) (map[string]json.RawMessage, error) {
	if row.Agent != "codex" && row.Agent != "claude" {
		return nil, fmt.Errorf("native history is supported for Claude and Codex; use terminal history for this agent")
	}
	if !path.IsAbs(row.Workdir) {
		return nil, fmt.Errorf("workspace unavailable")
	}
	target, err := s.DB.Target(row.TargetID)
	if err != nil {
		return nil, err
	}
	if target.Kind == "sandbox" {
		return nil, fmt.Errorf("native history unavailable for this sandbox session")
	}
	ex, err := s.Reg.For(target)
	if err != nil {
		return nil, err
	}
	config, err := s.Sessions.SessionLaunchConfiguration(row)
	if err != nil {
		return nil, err
	}
	spec := config.Spec
	prefix, err := sessions.EnvPrefix(spec.Env)
	if err != nil {
		return nil, err
	}
	name, identity := "", ""
	if row.EndedAt == nil && row.ArchivedAt == nil {
		name, identity = row.TmuxSession, row.TrackingIdentity
	}
	cmd := prefix + "python3 -c " + shellq.Quote(nativeidentity.RecordsScript+"\n"+nativeidentity.IdentityScript+"\n"+nativeidentity.ConversationsScript) + " " + shellq.Quote(row.Agent) + " " + shellq.Quote(row.Workdir) + " " + shellq.Quote(cid) + " " + shellq.Quote(r.URL.Query().Get("before")) + " " + shellq.Quote(name) + " " + shellq.Quote(identity)
	result, err := ex.Run(r.Context(), cmd, executor.RunOpts{Timeout: 30})
	var out map[string]json.RawMessage
	if err != nil || json.Unmarshal([]byte(result.Stdout), &out) != nil {
		return nil, fmt.Errorf("could not read native history on this target")
	}
	if !result.OK() {
		var message string
		json.Unmarshal(out["error"], &message)
		return nil, fmt.Errorf("%s", message)
	}
	return out, nil
}
func (s *Server) nativeConversations(w http.ResponseWriter, r *http.Request) {
	row, ok := s.sessionParam(w, r)
	if !ok {
		return
	}
	out, err := s.nativeConversationData(r, row, r.PathValue("conversation"))
	if err != nil {
		httpError(w, 409, "%s", err)
		return
	}
	config, err := s.Sessions.SessionLaunchConfiguration(row)
	if err != nil {
		httpError(w, 409, "%s", err)
		return
	}
	spec := config.Spec
	out["fork_supported"], _ = json.Marshal(len(spec.ForkArgs) > 0)
	out["resume_supported"], _ = json.Marshal(len(spec.ResumeIDArgs) > 0 && row.EndedAt != nil && row.ArchivedAt == nil)
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, out)
}
func (s *Server) forkConversation(w http.ResponseWriter, r *http.Request) {
	row, ok := s.sessionParam(w, r)
	if !ok {
		return
	}
	var in struct {
		Background     bool                         `json:"background"`
		ConversationID string                       `json:"conversation_id"`
		Name           string                       `json:"name"`
		Worktree       *worktree.InteractiveOptions `json:"worktree"`
	}
	if err := decodeBody(r, &in); err != nil || in.ConversationID == "" {
		httpError(w, 422, "choose the exact saved conversation to fork")
		return
	}
	config, err := s.Sessions.SessionLaunchConfiguration(row)
	if err != nil {
		httpError(w, 409, "%s", err)
		return
	}
	spec := config.Spec
	if len(spec.ForkArgs) == 0 {
		httpError(w, 409, "forking is not configured for this agent")
		return
	}
	// Re-resolve on the target at mutation time. Never accept a path, --last, or
	// an ID belonging to another workspace just because it came from a picker.
	if _, err := s.nativeConversationData(r, row, in.ConversationID); err != nil {
		httpError(w, 409, "%s", err)
		return
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		name = row.Name + " · fork"
	}
	if len(name) > 160 {
		httpError(w, 422, "name exceeds 160 bytes")
		return
	}
	launch := s.Sessions.Launch
	status := http.StatusCreated
	if in.Background && in.Worktree != nil {
		launch = s.Sessions.LaunchBackground
		status = http.StatusAccepted
	}
	next, err := launch(r.Context(), sessions.LaunchOpts{Worktree: in.Worktree, Configuration: config, GroupPath: row.GroupPath, ProjectID: row.ProjectID, TargetID: row.TargetID, Name: name, Agent: row.Agent, Model: row.Model, Workdir: row.Workdir, ForkID: in.ConversationID})
	if err != nil {
		httpError(w, 502, "%s", err)
		return
	}
	writeJSON(w, status, s.sessionView(next))
}

// resumeConversation continues one explicitly selected native history. It never
// guesses from recency or falls back to starting a fresh conversation.
func (s *Server) resumeConversation(w http.ResponseWriter, r *http.Request) {
	row, ok := s.sessionParam(w, r)
	if !ok {
		return
	}
	var in struct {
		ConversationID string `json:"conversation_id"`
		Name           string `json:"name"`
	}
	if err := decodeBody(r, &in); err != nil || in.ConversationID == "" {
		httpError(w, 422, "choose the exact saved conversation to resume")
		return
	}
	name := strings.TrimSpace(in.Name)
	if len(name) > 160 {
		httpError(w, 422, "name exceeds 160 bytes")
		return
	}
	if _, err := s.nativeConversationData(r, row, in.ConversationID); err != nil {
		httpError(w, 409, "%s", err)
		return
	}
	next, err := s.Sessions.ResumeConversation(r.Context(), row.ID, in.ConversationID, name)
	if err != nil {
		httpError(w, 409, "%s", err)
		return
	}
	writeJSON(w, 201, s.sessionView(next))
}

// boundNativeCID returns the conversation bound to this session, after
// validating that exact transcript on the target. Directory mtime is never a
// session identity: a newer unrelated conversation in the same workspace must
// not be selected. NativeRecoveryCID is the fresh process checkpoint and has
// priority over the legacy ResumeID; the latter is accepted only after the
// exact-history reader proves it still exists.
func (s *Server) boundNativeCID(r *http.Request, row *store.Session) (string, error) {
	if row.NativeRecoveryCID != "" {
		if _, err := s.nativeConversationData(r, row, row.NativeRecoveryCID); err == nil {
			return row.NativeRecoveryCID, nil
		}
		// A present checkpoint is authoritative. Falling back to an older ID after
		// it fails validation could resume the wrong generation of the session.
		return "", fmt.Errorf("the saved native conversation is unavailable; choose one from the history picker")
	}
	if row.ResumeID != "" {
		if _, err := s.nativeConversationData(r, row, row.ResumeID); err == nil {
			return row.ResumeID, nil
		}
	}
	return "", fmt.Errorf("no saved native conversation is bound to this session; choose one from the history picker")
}

// resumeRecentConversation is the convenience path for the Recent sessions
// view. It still validates the target history before launching and refuses when
// no exact session binding can be established.
func (s *Server) resumeRecentConversation(w http.ResponseWriter, r *http.Request) {
	row, ok := s.sessionParam(w, r)
	if !ok {
		return
	}
	if row.EndedAt == nil || row.ArchivedAt != nil {
		httpError(w, http.StatusConflict, "only an ended, non-archived session can be resumed from Recent")
		return
	}
	var in struct {
		Name string `json:"name"`
	}
	if err := decodeBody(r, &in); err != nil {
		httpError(w, http.StatusUnprocessableEntity, "%s", err)
		return
	}
	name := strings.TrimSpace(in.Name)
	if len(name) > 160 {
		httpError(w, http.StatusUnprocessableEntity, "name exceeds 160 bytes")
		return
	}
	cid, err := s.boundNativeCID(r, row)
	if err != nil {
		httpError(w, http.StatusConflict, "%s", err)
		return
	}
	if name == "" {
		name = row.Name + " · resumed"
	}
	next, err := s.Sessions.ResumeConversation(r.Context(), row.ID, cid, name)
	if err != nil {
		httpError(w, http.StatusConflict, "%s", err)
		return
	}
	writeJSON(w, http.StatusCreated, s.sessionView(next))
}
