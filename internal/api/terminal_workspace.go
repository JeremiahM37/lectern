package api

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/executor"
	"github.com/JeremiahM37/lectern/v2/internal/shellq"
	"github.com/JeremiahM37/lectern/v2/internal/terminal"
	"github.com/JeremiahM37/lectern/v2/web"
)

func (s *Server) terminalPage(w http.ResponseWriter, r *http.Request) {
	body, err := web.Assets.ReadFile("static/terminal.html")
	if err != nil {
		respondErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(body)
}

// Resolve a workspace from durable records; never accept a client-supplied root.
func (s *Server) terminalDirectory(kind, rawID string) (string, int64, error) {
	kind = strings.TrimSuffix(kind, "-shell")
	id, err := strconv.ParseInt(rawID, 10, 64)
	if err != nil {
		return "", 0, err
	}
	switch kind {
	case "session":
		row, err := s.DB.Session(id)
		if err != nil {
			return "", 0, err
		}
		return row.Workdir, row.TargetID, nil
	case "project":
		row, err := s.DB.Project(id)
		if err != nil {
			return "", 0, err
		}
		return row.RepoPath, row.TargetID, nil
	case "attempt":
		row, err := s.DB.Attempt(id)
		if err != nil {
			return "", 0, err
		}
		project, err := s.DB.ProjectForAttempt(id)
		if err != nil {
			return "", 0, err
		}
		return row.WorktreePath, project.TargetID, nil
	}
	return "", 0, fmt.Errorf("not a terminal")
}

func (s *Server) terminalInfo(w http.ResponseWriter, r *http.Request) {
	kind, id := r.PathValue("kind"), r.PathValue("id")
	att, target, err := s.resolveAttachment(kind, id)
	if err != nil {
		httpError(w, 404, "%s", err)
		return
	}
	argv, err := terminal.AttachArgv(att, target)
	if err != nil {
		httpError(w, 409, "%s", err)
		return
	}
	dir, _, _ := s.terminalDirectory(kind, id)
	shellURL := ""
	if kind == "session" || kind == "attempt" {
		shellURL = "/term/" + kind + "-shell/" + id
	}
	writeJSON(w, 200, map[string]any{"kind": kind, "id": id, "tmux_session": att.TmuxSession, "target": target.Name,
		"workdir": dir, "terminal_url": att.BasePath(), "shell_url": shellURL, "attach_argv": argv,
		"files_available": path.IsAbs(dir) && target.Kind != "sandbox",
		"desktop_uri":     "lectern://attach/" + kind + "/" + id,
		// The remote peer is a hosted control plane. Mark the command so a
		// newer CLI's no-API local default cannot start a second local database.
		"desktop_command": "ssh -t lectern /usr/local/bin/lectern --hosted-attach attach " + kind + " " + id})
}

// terminalActivity is the browser's explicit "a person just typed into this
// session" heartbeat (docs/agent-events.md section 3's alert-suppression
// rule) — see the doc comment on alerts.Activity for why this endpoint
// exists instead of snooping the ttyd proxy's bytes. It only means anything
// for a "session" attachment (task attempts and project shells are not
// alerted on), and it never fails loudly: a session that no longer exists,
// or has ended, simply has nothing to record and gets 204 either way — a
// heartbeat racing a session's teardown must never surface as a terminal
// error to whoever is just trying to type.
func (s *Server) terminalActivity(w http.ResponseWriter, r *http.Request) {
	if s.Activity == nil || r.PathValue("kind") != "session" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err == nil && id > 0 {
		s.Activity.Touch(id)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) terminalHistory(w http.ResponseWriter, r *http.Request) {
	att, target, err := s.resolveAttachment(r.PathValue("kind"), r.PathValue("id"))
	if err != nil {
		httpError(w, 404, "%s", err)
		return
	}
	ex, err := s.Reg.For(target)
	if err != nil {
		respondErr(w, err)
		return
	}
	if target.Kind == "sandbox" && att.SandboxVMID != "" {
		if ex, err = s.sandboxExec(target, att.SandboxVMID); err != nil {
			respondErr(w, err)
			return
		}
	}
	result, err := ex.Run(r.Context(), "tmux capture-pane -p -J -S -100000 -t "+shellq.Quote("="+att.TmuxSession+":"), executor.RunOpts{Timeout: 20})
	if err != nil || !result.OK() {
		httpError(w, 502, "could not read terminal history")
		return
	}
	truncated := false
	if len(result.Stdout) > 8<<20 {
		result.Stdout = result.Stdout[len(result.Stdout)-(8<<20):]
		truncated = true
	}
	writeJSON(w, 200, map[string]any{"text": result.Stdout, "truncated": truncated, "limit_lines": 100000})
}

func (s *Server) terminalUpload(w http.ResponseWriter, r *http.Request) {
	_, _, err := s.resolveAttachment(r.PathValue("kind"), r.PathValue("id"))
	if err != nil {
		httpError(w, 404, "%s", err)
		return
	}
	dir, targetID, err := s.terminalDirectory(r.PathValue("kind"), r.PathValue("id"))
	if err != nil {
		httpError(w, 404, "%s", err)
		return
	}
	s.uploadAttachment(w, r, targetID, dir)
}

// Workspace file reads (list, read, download) share workspace_files.go's
// resolution and the embedded target-side script with the editing endpoints.
func (s *Server) terminalFiles(w http.ResponseWriter, r *http.Request) {
	out, ok := s.workspaceFileAction(w, r, "list", r.URL.Query().Get("path"))
	if ok {
		writeJSON(w, 200, out)
	}
}

// terminalFile returns the exact bytes. The content hash rides along in a
// header so an editor can later save with conflict detection.
func (s *Server) terminalFile(w http.ResponseWriter, r *http.Request) {
	out, ok := s.workspaceFileAction(w, r, "read", r.URL.Query().Get("path"))
	if !ok {
		return
	}
	var encoded, sha string
	var mtime int64
	json.Unmarshal(out["data"], &encoded)
	json.Unmarshal(out["sha256"], &sha)
	json.Unmarshal(out["mtime"], &mtime)
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		respondErr(w, err)
		return
	}
	name := path.Base(r.URL.Query().Get("path"))
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Lectern-Sha256", sha)
	w.Header().Set("X-Lectern-Mtime", strconv.FormatInt(mtime, 10))
	w.Header().Set("ETag", `"`+sha+`"`)
	w.Write(data)
}
