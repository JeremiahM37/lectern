package api

import (
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/sessions"
	"github.com/JeremiahM37/lectern/v2/internal/store"
	"github.com/JeremiahM37/lectern/v2/internal/terminal"
)

// termProxy serves an attached terminal on lectern's own origin.
//
// The URL names the attachment ("/term/session/24"), never the port. ttyd runs
// on the control plane, on loopback, on a port from a small range — and that
// port is not stable: a terminal is retired when the range fills, and every one
// of them dies when this service restarts. A page holding a port URL is then
// pointed at nothing for good, which is exactly what left ttyd's own reconnect
// retrying forever with no way to succeed.
//
// So the terminal is resolved, and respawned if it is gone, on every request.
// Reconnecting from a stale tab therefore just works: it lands on a fresh ttyd
// attached to the same tmux session, which is still exactly where it was.
func (s *Server) termProxy(w http.ResponseWriter, r *http.Request) {
	kind, id := r.PathValue("kind"), r.PathValue("id")
	att, target, err := s.resolveAttachment(kind, id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	// Attach reuses a live terminal for this attachment and starts one when
	// there is none, so a reconnect after a restart heals itself
	port, err := s.Terminals.Attach(r.Context(), att, target)
	if err != nil {
		s.Log.Info("terminal could not be started", "attachment", att.Key, "err", err)
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}

	target2, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", port))
	proxy := &httputil.ReverseProxy{
		// ttyd is mounted with --base-path, so it expects the prefix to arrive
		// intact; the path is passed through rather than stripped.
		Director: func(req *http.Request) {
			req.URL.Scheme = target2.Scheme
			req.URL.Host = target2.Host
			req.Host = target2.Host
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			s.Log.Info("terminal proxy ended", "attachment", att.Key, "err", err)
			http.Error(w, "the terminal has closed", http.StatusGone)
		},
	}
	// ReverseProxy carries the websocket upgrade through on its own
	proxy.ServeHTTP(w, r)
}

// resolveAttachment turns a URL like /term/session/24 into the tmux session it
// names, refusing anything that is not a live session or attempt of this board.
func (s *Server) resolveAttachment(kind, rawID string) (terminal.Attachment, *store.Target, error) {
	if strings.HasSuffix(kind, "-shell") {
		base := strings.TrimSuffix(kind, "-shell")
		if base != "session" && base != "attempt" {
			return terminal.Attachment{}, nil, fmt.Errorf("not a terminal")
		}
		att, target, err := s.resolveAttachment(base, rawID)
		if err != nil {
			return att, target, err
		}
		dir, _, err := s.terminalDirectory(base, rawID)
		if err != nil {
			return att, target, err
		}
		att.Key = kind + ":" + rawID
		att.TmuxSession = "lec-companion-" + base + "-" + rawID
		att.Workdir = dir
		return att, target, nil
	}
	id, err := strconv.ParseInt(rawID, 10, 64)
	if err != nil || id <= 0 {
		return terminal.Attachment{}, nil, fmt.Errorf("not a terminal")
	}
	switch kind {
	case "session":
		row, err := s.DB.Session(id)
		if err != nil {
			return terminal.Attachment{}, nil, fmt.Errorf("no such session")
		}
		if row.Status == sessions.StatusDead || row.EndedAt != nil {
			return terminal.Attachment{}, nil, fmt.Errorf("this session is ended or untracked; restore tracking first")
		}
		target, err := s.DB.Target(row.TargetID)
		if err != nil {
			return terminal.Attachment{}, nil, err
		}
		return terminal.Attachment{
			Key:         fmt.Sprintf("session:%d", row.ID),
			TmuxSession: row.TmuxSession,
		}, target, nil
	case "project":
		// a plain shell where the project's code lives, for reading something or
		// making a change by hand without asking an agent to do it
		proj, err := s.DB.Project(id)
		if err != nil {
			return terminal.Attachment{}, nil, fmt.Errorf("no such project")
		}
		if proj.RepoPath == "" {
			return terminal.Attachment{}, nil, fmt.Errorf("this project has no directory")
		}
		target, err := s.DB.Target(proj.TargetID)
		if err != nil {
			return terminal.Attachment{}, nil, err
		}
		return terminal.Attachment{
			Key:         fmt.Sprintf("project:%d", proj.ID),
			TmuxSession: fmt.Sprintf("lec-sh%d", proj.ID),
			Workdir:     proj.RepoPath,
		}, target, nil
	case "attempt":
		att, err := s.DB.Attempt(id)
		if err != nil {
			return terminal.Attachment{}, nil, fmt.Errorf("no such attempt")
		}
		if att.TmuxSession == "" {
			return terminal.Attachment{}, nil, fmt.Errorf("no running tmux session to attach")
		}
		proj, err := s.DB.ProjectForAttempt(att.ID)
		if err != nil {
			return terminal.Attachment{}, nil, err
		}
		target, err := s.DB.Target(proj.TargetID)
		if err != nil {
			return terminal.Attachment{}, nil, err
		}
		return terminal.Attachment{
			Key:         fmt.Sprintf("attempt:%d", att.ID),
			TmuxSession: att.TmuxSession, SandboxVMID: att.SandboxVMID,
		}, target, nil
	}
	return terminal.Attachment{}, nil, fmt.Errorf("not a terminal")
}
