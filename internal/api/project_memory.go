package api

import (
	"context"
	"net/http"

	"github.com/JeremiahM37/lectern/internal/memory"
	"github.com/JeremiahM37/lectern/internal/store"
)

func (s *Server) provisionProjectMemory(ctx context.Context, project *store.Project) {
	status := "disabled"
	if provider, ok := s.Memory.(memory.ProjectProvider); ok && project.MemoryTopic != "" {
		var err error
		status, err = provider.Provision(ctx, project.Name, project.MemoryTopic)
		if err != nil {
			s.Log.Warn("project memory provisioning failed", "project", project.ID, "err", err)
		}
	}
	if err := s.DB.Update("projects", project.ID, map[string]any{"memory_status": status}); err != nil {
		s.Log.Warn("project memory status could not be saved", "project", project.ID, "err", err)
		return
	}
	project.MemoryStatus = status
}

func (s *Server) retryProjectMemory(w http.ResponseWriter, r *http.Request) {
	identity, err := pathID(r, "id")
	if err != nil {
		httpError(w, 404, "no such project")
		return
	}
	project, err := s.DB.Project(identity)
	if err != nil {
		httpError(w, 404, "no such project")
		return
	}
	if project.MemoryTopic == "" {
		httpError(w, 409, "existing project uses configured legacy memory paths")
		return
	}
	s.provisionProjectMemory(r.Context(), project)
	writeJSON(w, 200, project)
}
