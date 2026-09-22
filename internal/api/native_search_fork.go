package api

import (
	"net/http"
	"strings"

	"github.com/JeremiahM37/lectern/internal/sessions"
	"github.com/JeremiahM37/lectern/internal/worktree"
)

func (s *Server) forkConversationSearchResult(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Background    bool                         `json:"background"`
		Configuration string                       `json:"configuration_id"`
		Name          string                       `json:"name"`
		Worktree      *worktree.InteractiveOptions `json:"worktree"`
	}
	if decodeBody(r, &in) != nil || in.Configuration == "" {
		httpError(w, 422, "choose launch settings for the fork")
		return
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		name = "Conversation fork"
	}
	if len(name) > 160 {
		httpError(w, 422, "name exceeds 160 bytes")
		return
	}
	job := s.conversationSearchJob(w, r)
	if job == nil {
		return
	}
	chosen, match := lookupSearchMatch(job, r.PathValue("result"))
	if chosen == nil {
		httpError(w, 404, "search result changed; refresh the search")
		return
	}
	if !s.searchTargetUnchanged(chosen.target) {
		httpError(w, 409, "target connection changed; run the search again")
		return
	}
	var launch *searchLaunchChoice
	for _, choice := range searchForkOptions(job, chosen) {
		if choice.ID == in.Configuration {
			copy := choice
			launch = &copy
			break
		}
	}
	if launch == nil {
		httpError(w, 422, "launch settings do not belong to this native profile")
		return
	}
	if !launch.Supported {
		httpError(w, 409, "native forking is not configured for these settings")
		return
	}
	// Revalidate using the chosen launch environment too: two aliases that pointed
	// to one profile during indexing might now resolve to different directories.
	prefix, err := sessions.EnvPrefix(launch.configuration.Spec.Env)
	if err != nil {
		httpError(w, 409, "launch environment is unavailable")
		return
	}
	chosen.prefix = prefix
	if _, status, err := s.readSearchMatch(r.Context(), job, chosen, match, "match", ""); err != nil {
		httpError(w, status, "%s", err)
		return
	}
	start := s.Sessions.Launch
	status := http.StatusCreated
	if in.Background && in.Worktree != nil {
		start = s.Sessions.LaunchBackground
		status = http.StatusAccepted
	}
	next, err := start(r.Context(), sessions.LaunchOpts{Configuration: launch.configuration, TargetID: chosen.TargetID, Name: name, Agent: chosen.Agent, Model: launch.Model, Workdir: match.Cwd, ForkID: match.CID, Worktree: in.Worktree})
	if err != nil {
		httpError(w, 502, "%s", err)
		return
	}
	writeJSON(w, status, s.sessionView(next))
}
