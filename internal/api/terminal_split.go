package api

import (
	"context"
	"fmt"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/executor"
	"github.com/JeremiahM37/lectern/v2/internal/sessions/backend"
	"github.com/JeremiahM37/lectern/v2/internal/store"
	"github.com/JeremiahM37/lectern/v2/internal/terminal"
)

// terminalSplit opens what a split of a native attachment shows beside the
// agent (docs/terminal-client.md): a new tracked shell session on the
// session's own target, in the directory the agent's pane is in now (or, with
// ?dir=workdir or when the pane cannot be asked, the session's workdir). It
// answers with the shell session and the command that attaches it.
func (s *Server) terminalSplit(w http.ResponseWriter, r *http.Request) {
	kind, id := r.PathValue("kind"), r.PathValue("id")
	att, target, err := s.resolveAttachment(kind, id)
	if err != nil {
		httpError(w, 404, "%s", err)
		return
	}
	if target.Kind == "sandbox" {
		httpError(w, 409, "a sandbox has no shell beside its agent")
		return
	}
	dir, _, err := s.terminalDirectory(kind, id)
	if err != nil {
		httpError(w, 404, "%s", err)
		return
	}
	switch r.URL.Query().Get("dir") {
	case "", "agent":
		if current := s.paneDirectory(r.Context(), att, target); current != "" {
			dir = current
		}
	case "workdir":
	default:
		httpError(w, 400, "dir is agent or workdir")
		return
	}
	if !path.IsAbs(dir) {
		httpError(w, 409, "this terminal has no directory to open a shell in")
		return
	}
	name := "Shell · " + target.Name
	var projectID *int64
	if n, err := strconv.ParseInt(id, 10, 64); err == nil {
		switch strings.TrimSuffix(kind, "-shell") {
		case "session":
			if row, err := s.DB.Session(n); err == nil {
				name, projectID = "Shell · "+row.Name, row.ProjectID
			}
		case "project":
			projectID = &n
		case "attempt":
			if project, err := s.DB.ProjectForAttempt(n); err == nil {
				projectID = &project.ID
			}
		}
	}
	shell, err := s.Sessions.LaunchShellIn(r.Context(), target.ID, dir, name, projectID)
	if err != nil {
		httpError(w, 409, "%s", err)
		return
	}
	argv, err := terminal.AttachArgv(terminal.Attachment{Key: fmt.Sprintf("session:%d", shell.ID), TmuxSession: shell.TmuxSession,
		Backend: s.targetBackend(target)}, target)
	if err != nil {
		httpError(w, 409, "%s", err)
		return
	}
	writeJSON(w, 201, map[string]any{"dir": dir, "target": target.Name, "session": s.sessionView(shell), "attach_argv": argv})
}

// paneDirectory is the current directory of the attachment's active pane,
// asked of tmux on the target, or "" when it cannot be known.
func (s *Server) paneDirectory(ctx context.Context, att terminal.Attachment, target *store.Target) string {
	if target.Kind == "sandbox" || att.TmuxSession == "" {
		return ""
	}
	ex, err := s.Reg.For(target)
	if err != nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	be := att.Backend
	if be == nil {
		be = backend.For(ex)
	}
	out, err := ex.Run(ctx, be.Display(backend.Pane(att.TmuxSession), "#{pane_current_path}"), executor.RunOpts{Timeout: 15})
	if err != nil || out.RC != 0 {
		return ""
	}
	dir := strings.TrimSpace(out.Stdout)
	if !path.IsAbs(dir) || strings.ContainsAny(dir, "\n\r") {
		return ""
	}
	return dir
}
