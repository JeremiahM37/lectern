package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/JeremiahM37/lectern/v2/internal/limits"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// Usage-limit continuity (internal/limits, docs/rate-limits.md): the holds,
// the one-tap choices and the policy editor.

// limitView is a hold plus what its card needs to offer: the handoff
// destination the effective policy would use, and where that policy is set.
type limitView struct {
	*store.LimitHold
	Fallback    string `json:"fallback,omitempty"`
	PolicyScope string `json:"policy_scope"`
}

func (s *Server) limitView(h *store.LimitHold) *limitView {
	if h == nil {
		return nil
	}
	var sessionID int64
	var projectID *int64
	if h.SessionID != nil {
		sessionID = *h.SessionID
		if sess, err := s.DB.Session(sessionID); err == nil {
			projectID = sess.ProjectID
		}
	} else if h.TaskID != nil {
		if task, err := s.DB.Task(*h.TaskID); err == nil {
			projectID = &task.ProjectID
		}
	}
	p, scope := limits.Effective(s.DB, sessionID, projectID)
	return &limitView{LimitHold: h, Fallback: p.Fallback(), PolicyScope: scope}
}

// sessionLimit is a session's open hold, for its card.
func (s *Server) sessionLimit(id int64) *limitView {
	h, err := s.DB.OpenLimitHoldForSession(id)
	if err != nil {
		return nil
	}
	return s.limitView(h)
}

// taskLimit is the task's newest hold while it still matters to the card:
// open, or resolved into an attempt that is still held in the queue.
func (s *Server) taskLimit(taskID int64) *limitView {
	h, err := s.DB.LatestLimitHoldForTask(taskID)
	if err != nil {
		return nil
	}
	if !h.Open() {
		if h.SuccessorID == nil {
			return nil
		}
		next, err := s.DB.Attempt(*h.SuccessorID)
		if err != nil || next.Status != "queued" {
			return nil
		}
	}
	return s.limitView(h)
}

func (s *Server) listLimits(w http.ResponseWriter, r *http.Request) {
	var holds []*store.LimitHold
	var err error
	if r.URL.Query().Get("all") == "true" {
		holds, err = s.DB.RecentLimitHolds(100)
	} else {
		holds, err = s.DB.OpenLimitHolds()
	}
	if err != nil {
		respondErr(w, err)
		return
	}
	out := make([]*limitView, 0, len(holds))
	for _, h := range holds {
		out = append(out, s.limitView(h))
	}
	writeJSON(w, 200, out)
}

type chooseLimitIn struct {
	Action    string `json:"action"`
	Agent     string `json:"agent"`
	Model     string `json:"model"`
	ProfileID int64  `json:"profile_id"`
}

// chooseLimit is the one-tap answer from a card or a push: resume at the
// reset, resume now, hand off, leave it to me, or dismiss.
func (s *Server) chooseLimit(w http.ResponseWriter, r *http.Request) {
	if s.Limits == nil {
		httpError(w, 503, "usage-limit tracking is not enabled")
		return
	}
	id, err := pathID(r, "id")
	if err != nil {
		httpError(w, 404, "no such limit")
		return
	}
	var in chooseLimitIn
	if err := decodeBody(r, &in); err != nil {
		httpError(w, 422, "%s", err.Error())
		return
	}
	if !oneOf(in.Action, limits.ActionWait, limits.ActionResumeNow, limits.ActionHandoff,
		limits.ActionNotify, limits.ActionDismiss) {
		httpError(w, 422, "action must be wait, resume_now, handoff, notify or dismiss")
		return
	}
	var override *limits.Policy
	if in.Agent != "" || in.ProfileID != 0 {
		if in.Agent != "" && !s.knownAgent(in.Agent) {
			httpError(w, 422, "unknown agent %q", in.Agent)
			return
		}
		override = &limits.Policy{FallbackAgent: in.Agent, FallbackModel: in.Model, FallbackProfileID: in.ProfileID}
	}
	h, err := s.Limits.Choose(r.Context(), id, in.Action, override)
	switch {
	case errors.Is(err, store.ErrNotFound):
		httpError(w, 404, "no such limit")
		return
	case errors.Is(err, limits.ErrConflict):
		httpError(w, 409, "%s", err.Error())
		return
	case err != nil:
		httpError(w, 422, "%s", err.Error())
		return
	}
	if h.TaskID != nil {
		if task, err := s.DB.Task(*h.TaskID); err == nil {
			s.Bus.Publish("board", "task", task)
		}
	}
	writeJSON(w, 200, s.limitView(h))
}

// limitScope reads which policy a request is about: ?session_id=, then
// ?project_id=, else the global default.
func limitScope(get func(string) string) (string, int64, error) {
	for _, pair := range [][2]string{{"session_id", limits.ScopeSession}, {"project_id", limits.ScopeProject}} {
		if raw := get(pair[0]); raw != "" {
			id, err := strconv.ParseInt(raw, 10, 64)
			if err != nil || id <= 0 {
				return "", 0, invalid("%s must be a positive number", pair[0])
			}
			return pair[1], id, nil
		}
	}
	return limits.ScopeGlobal, 0, nil
}

type limitPolicyOut struct {
	Scope          string         `json:"scope"`
	ScopeID        int64          `json:"scope_id"`
	Own            *limits.Policy `json:"own"`
	Effective      limits.Policy  `json:"effective"`
	EffectiveScope string         `json:"effective_scope"`
}

func (s *Server) limitPolicyOut(scope string, id int64) limitPolicyOut {
	out := limitPolicyOut{Scope: scope, ScopeID: id}
	if p, ok := limits.ParsePolicy(s.DB.LimitPolicyJSON(scope, id)); ok {
		out.Own = &p
	}
	var sessionID int64
	var projectID *int64
	switch scope {
	case limits.ScopeSession:
		sessionID = id
		if sess, err := s.DB.Session(id); err == nil {
			projectID = sess.ProjectID
		}
	case limits.ScopeProject:
		projectID = &id
	}
	out.Effective, out.EffectiveScope = limits.Effective(s.DB, sessionID, projectID)
	return out
}

func (s *Server) getLimitPolicy(w http.ResponseWriter, r *http.Request) {
	scope, id, err := limitScope(r.URL.Query().Get)
	if err != nil {
		respondErr(w, err)
		return
	}
	writeJSON(w, 200, s.limitPolicyOut(scope, id))
}

type putLimitPolicyIn struct {
	SessionID int64           `json:"session_id"`
	ProjectID int64           `json:"project_id"`
	Policy    json.RawMessage `json:"policy"`
}

// putLimitPolicy sets (or, with "policy": null, clears) one scope's policy.
func (s *Server) putLimitPolicy(w http.ResponseWriter, r *http.Request) {
	var in putLimitPolicyIn
	if err := decodeBody(r, &in); err != nil {
		httpError(w, 422, "%s", err.Error())
		return
	}
	scope, id := limits.ScopeGlobal, int64(0)
	switch {
	case in.SessionID != 0:
		if _, err := s.DB.Session(in.SessionID); err != nil {
			httpError(w, 404, "no such session")
			return
		}
		scope, id = limits.ScopeSession, in.SessionID
	case in.ProjectID != 0:
		if _, err := s.DB.Project(in.ProjectID); err != nil {
			httpError(w, 404, "no such project")
			return
		}
		scope, id = limits.ScopeProject, in.ProjectID
	}
	raw := ""
	if len(in.Policy) > 0 && string(in.Policy) != "null" {
		var p limits.Policy
		if err := json.Unmarshal(in.Policy, &p); err != nil {
			httpError(w, 422, "policy must be an object")
			return
		}
		if err := p.Validate(); err != nil {
			httpError(w, 422, "%s", err.Error())
			return
		}
		if p.FallbackAgent != "" && !s.knownAgent(p.FallbackAgent) {
			httpError(w, 422, "unknown fallback agent %q", p.FallbackAgent)
			return
		}
		if p.FallbackProfileID != 0 {
			if _, err := s.DB.LaunchProfile(p.FallbackProfileID); err != nil {
				httpError(w, 422, "no such launch profile")
				return
			}
		}
		b, _ := json.Marshal(p)
		raw = string(b)
	}
	if err := s.DB.SetLimitPolicyJSON(scope, id, raw); err != nil {
		respondErr(w, err)
		return
	}
	writeJSON(w, 200, s.limitPolicyOut(scope, id))
}
