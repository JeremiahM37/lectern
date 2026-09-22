package api

import (
	"net/http"

	"github.com/JeremiahM37/lectern/internal/memory"
)

// projectMemoryLink reports where a project's memory is read from, how that was
// decided, and whether anything is there. For a project without a managed
// topic the answer used to be assumed: the paths are derived from its name, and
// when no note is filed under that name retrieval quietly finds nothing.
func (s *Server) projectMemoryLink(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		httpError(w, 404, "no such project")
		return
	}
	project, err := s.DB.Project(id)
	if err != nil {
		respondErr(w, err)
		return
	}
	out := map[string]any{"provider": "none", "status": "disabled", "topic": project.MemoryTopic,
		"link": memory.Link{Mode: "off", Basis: "off", Paths: []memory.LinkedPath{}}}
	provider, ok := s.Memory.(memory.LinkProvider)
	if s.Memory == nil || !ok {
		writeJSON(w, 200, out)
		return
	}
	out["provider"] = s.Memory.Name()
	link, err := provider.ProjectLink(r.Context(), project.Name, project.MemoryTopic)
	out["link"] = link
	switch {
	case err != nil:
		out["status"], out["message"] = "unavailable", err.Error()
	case link.Linked:
		out["status"] = "linked"
	default:
		out["status"] = "unlinked"
	}
	writeJSON(w, 200, out)
}
