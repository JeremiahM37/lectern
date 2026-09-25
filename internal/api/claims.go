package api

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/auth"
	"github.com/JeremiahM37/lectern/v2/internal/claims"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// listClaims is GET /api/claims — docs/claims.md point 6. Filters combine
// (AND): repo_key, project_id (resolved to that project's repo_key,
// synchronously if it is not cached yet), session_id, attempt_id. With no
// filter at all, every active claim on the board is returned — the Claims
// panel's board-wide view.
func (s *Server) listClaims(w http.ResponseWriter, r *http.Request) {
	if s.Claims == nil {
		writeJSON(w, 200, []store.Claim{})
		return
	}
	q := r.URL.Query()
	var list []*store.Claim
	var err error
	switch {
	case q.Get("session_id") != "":
		id, perr := strconv.ParseInt(q.Get("session_id"), 10, 64)
		if perr != nil {
			httpError(w, 400, "invalid session_id")
			return
		}
		list, err = s.DB.ActiveClaimsForSession(id, store.Now())
	case q.Get("attempt_id") != "":
		id, perr := strconv.ParseInt(q.Get("attempt_id"), 10, 64)
		if perr != nil {
			httpError(w, 400, "invalid attempt_id")
			return
		}
		list, err = s.DB.ActiveClaimsForAttempt(id, store.Now())
	case q.Get("project_id") != "":
		id, perr := strconv.ParseInt(q.Get("project_id"), 10, 64)
		if perr != nil {
			httpError(w, 400, "invalid project_id")
			return
		}
		repoKey, rerr := s.resolveProjectRepoKeyByID(r, id)
		if rerr != nil {
			respondErr(w, rerr)
			return
		}
		if repoKey == "" {
			list = nil
		} else {
			list, err = s.Claims.ActiveForRepo(repoKey)
		}
	case q.Get("repo_key") != "":
		list, err = s.Claims.ActiveForRepo(q.Get("repo_key"))
	default:
		list, err = s.Claims.All()
	}
	if err != nil {
		respondErr(w, err)
		return
	}
	if list == nil {
		list = []*store.Claim{}
	}
	writeJSON(w, 200, list)
}

// resolveProjectRepoKeyByID loads a project and resolves its repo_key,
// resolving synchronously (via internal/awareness) if it is not cached yet —
// safe here since this is an ordinary API handler, never a hook response.
func (s *Server) resolveProjectRepoKeyByID(r *http.Request, id int64) (string, error) {
	proj, err := s.DB.Project(id)
	if err != nil {
		return "", err
	}
	if proj.RepoKey != "" {
		return proj.RepoKey, nil
	}
	if s.Awareness == nil {
		return "", nil
	}
	key, _, ok := s.Awareness.ResolveProjectRepoKey(r.Context(), proj)
	if !ok {
		return "", nil
	}
	return key, nil
}

type claimIn struct {
	RepoKey    string   `json:"repo_key"`
	ProjectID  int64    `json:"project_id"`
	SessionID  int64    `json:"session_id"`
	AttemptID  int64    `json:"attempt_id"`
	ScopeKind  string   `json:"scope_kind"`
	Scope      string   `json:"scope"`
	Paths      []string `json:"paths"`
	Holder     string   `json:"holder"`
	Intent     string   `json:"intent"`
	TTLMinutes int      `json:"ttl_minutes"`
}

// createClaim is POST /api/claims. The caller identifies itself one of four
// ways, tried in this order: session_id (an interactive session — repo_key
// and holder/agent come from that session, resolved synchronously if not
// cached yet), attempt_id (a task attempt — repo_key from its project,
// holder is the task's title), project_id (a human claiming against a named
// project — repo_key from that project, holder is the signed-in principal or
// an explicit `holder`), or a bare repo_key (advanced/test use — holder must
// be given explicitly). This is also what the `claim_work` MCP tool and the
// Claims panel's "claim for me" action both go through.
func (s *Server) createClaim(w http.ResponseWriter, r *http.Request) {
	if s.Claims == nil {
		httpError(w, 503, "claims are not available")
		return
	}
	var body claimIn
	if err := decodeBody(r, &body); err != nil {
		httpError(w, 422, "%s", err.Error())
		return
	}
	in := claims.Input{
		ScopeKind: body.ScopeKind, Scope: body.Scope, Paths: body.Paths,
		Intent: body.Intent, TTLMinutes: body.TTLMinutes,
	}
	switch {
	case body.SessionID != 0:
		sess, err := s.DB.Session(body.SessionID)
		if err != nil {
			respondErr(w, err)
			return
		}
		repoKey := sess.RepoKey
		if repoKey == "" && s.Awareness != nil {
			repoKey, _, _ = s.Awareness.ResolveNow(r.Context(), sess)
		}
		if repoKey == "" || repoKey == "none" {
			httpError(w, 409, "this session's repository has not been identified yet — try again shortly")
			return
		}
		in.RepoKey = repoKey
		in.HolderKind = "session"
		in.SessionID = &sess.ID
		in.Agent = sess.Agent
		in.Holder = orDefaultClaims(body.Holder, sess.Name)
	case body.AttemptID != 0:
		att, err := s.DB.Attempt(body.AttemptID)
		if err != nil {
			respondErr(w, err)
			return
		}
		proj, err := s.DB.ProjectForAttempt(att.ID)
		if err != nil {
			respondErr(w, err)
			return
		}
		repoKey := proj.RepoKey
		if repoKey == "" && s.Awareness != nil {
			repoKey, _, _ = s.Awareness.ResolveProjectRepoKey(r.Context(), proj)
		}
		if repoKey == "" {
			httpError(w, 409, "this attempt's repository has not been identified yet — try again shortly")
			return
		}
		in.RepoKey = repoKey
		in.HolderKind = "attempt"
		in.AttemptID = &att.ID
		in.Agent = att.Agent
		holder := body.Holder
		if holder == "" {
			if task, terr := s.DB.Task(att.TaskID); terr == nil && task != nil {
				holder = task.Title
			} else {
				holder = fmt.Sprintf("attempt #%d", att.ID)
			}
		}
		in.Holder = holder
	case body.ProjectID != 0:
		repoKey, err := s.resolveProjectRepoKeyByID(r, body.ProjectID)
		if err != nil {
			respondErr(w, err)
			return
		}
		if repoKey == "" {
			httpError(w, 409, "this project's repository has not been identified yet — try again shortly")
			return
		}
		in.RepoKey = repoKey
		in.HolderKind = "human"
		in.Holder = orDefaultClaims(body.Holder, humanHolder(r))
	case body.RepoKey != "":
		in.RepoKey = body.RepoKey
		in.HolderKind = "human"
		in.Holder = orDefaultClaims(body.Holder, humanHolder(r))
	default:
		httpError(w, 422, "give one of session_id, attempt_id, project_id, or repo_key")
		return
	}
	c, err := s.Claims.Create(in)
	if err != nil {
		httpError(w, 422, "%s", err.Error())
		return
	}
	writeJSON(w, 200, c)
}

// humanHolder names the signed-in principal for a human-made claim, falling
// back to "operator" in auth mode none (where there is no identity at all).
func humanHolder(r *http.Request) string {
	principal, _ := auth.FromContext(r.Context())
	if principal.Login != "" {
		return principal.Login
	}
	return "operator"
}

func orDefaultClaims(v, def string) string {
	if strings.TrimSpace(v) != "" {
		return v
	}
	return def
}

// releaseClaim is DELETE /api/claims/{id}.
func (s *Server) releaseClaim(w http.ResponseWriter, r *http.Request) {
	if s.Claims == nil {
		httpError(w, 503, "claims are not available")
		return
	}
	id, err := pathID(r, "id")
	if err != nil {
		httpError(w, 404, "claim not found")
		return
	}
	if _, err := s.DB.Claim(id); err != nil {
		respondErr(w, err)
		return
	}
	if err := s.Claims.Release(id); err != nil {
		respondErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"released": true})
}

type extendIn struct {
	TTLMinutes int `json:"ttl_minutes"`
}

// extendClaim is POST /api/claims/{id}/extend — docs/claims.md's human
// "extend" panel action.
func (s *Server) extendClaim(w http.ResponseWriter, r *http.Request) {
	if s.Claims == nil {
		httpError(w, 503, "claims are not available")
		return
	}
	id, err := pathID(r, "id")
	if err != nil {
		httpError(w, 404, "claim not found")
		return
	}
	var body extendIn
	if err := decodeBody(r, &body); err != nil {
		httpError(w, 422, "%s", err.Error())
		return
	}
	c, err := s.Claims.Extend(id, body.TTLMinutes)
	if err != nil {
		httpError(w, 409, "%s", err.Error())
		return
	}
	writeJSON(w, 200, c)
}

// claimsTopicOverlap is GET /api/claims/topic-overlap?project_id=&text=
// (docs/claims.md point 4c): the check a new-session/new-task launch dialog
// makes BEFORE launching, so the operator sees a warning if the prompt looks
// like an active topic claim someone else already has. project_id resolves
// to that project's repo_key (synchronously, like the other endpoints in
// this file); repo_key may be given directly instead.
func (s *Server) claimsTopicOverlap(w http.ResponseWriter, r *http.Request) {
	if s.Claims == nil {
		writeJSON(w, 200, []store.Claim{})
		return
	}
	q := r.URL.Query()
	text := q.Get("text")
	repoKey := q.Get("repo_key")
	if repoKey == "" && q.Get("project_id") != "" {
		id, err := strconv.ParseInt(q.Get("project_id"), 10, 64)
		if err != nil {
			httpError(w, 400, "invalid project_id")
			return
		}
		resolved, err := s.resolveProjectRepoKeyByID(r, id)
		if err != nil {
			respondErr(w, err)
			return
		}
		repoKey = resolved
	}
	if repoKey == "" || text == "" {
		writeJSON(w, 200, []store.Claim{})
		return
	}
	overlaps, err := s.Claims.OverlappingTopicClaims(repoKey, text)
	if err != nil {
		respondErr(w, err)
		return
	}
	if overlaps == nil {
		overlaps = []*store.Claim{}
	}
	writeJSON(w, 200, overlaps)
}
