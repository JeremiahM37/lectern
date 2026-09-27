package api

import (
	"encoding/base64"
	"encoding/json"
	"mime"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/auth"
)

// Terminal links (docs/files.md): a path an agent prints is a link only if it
// names something. workspaceExists answers that for a workspace path without
// reading the file.
func (s *Server) workspaceExists(w http.ResponseWriter, r *http.Request) {
	if out, ok := s.workspaceFileAction(w, r, "exists", r.URL.Query().Get("path")); ok {
		writeJSON(w, 200, out)
	}
}

// An absolute or ~/ path outside the workspace (a report an agent wrote to
// ~/.formwork, say) opens read-only, on the session's own machine through its
// executor, with the same 25 MiB cap. Only a person may read outside the
// workspace: an agent on this machine already has its own shell, and the
// browser API must not become a way for one session to read what another
// can reach. Nothing here writes.
func (s *Server) outsideReader(w http.ResponseWriter, r *http.Request) (string, bool) {
	principal, _ := auth.FromContext(r.Context())
	if s.Auth == nil || !s.Auth.CanDecide(principal) {
		httpError(w, 403, "opening files outside the workspace requires your signed-in identity, not an automated caller")
		return "", false
	}
	p := r.URL.Query().Get("path")
	if !strings.HasPrefix(p, "/") && !strings.HasPrefix(p, "~/") {
		httpError(w, 400, "give an absolute or ~/ path")
		return "", false
	}
	return p, true
}

func (s *Server) externalStat(w http.ResponseWriter, r *http.Request) {
	p, ok := s.outsideReader(w, r)
	if !ok {
		return
	}
	if out, ok := s.workspaceFileAction(w, r, "ext_stat", p); ok {
		writeJSON(w, 200, out)
	}
}

func (s *Server) externalFile(w http.ResponseWriter, r *http.Request) {
	p, ok := s.outsideReader(w, r)
	if !ok {
		return
	}
	out, ok := s.workspaceFileAction(w, r, "ext_read", p)
	if !ok {
		return
	}
	var encoded, sha, real string
	var mtime int64
	json.Unmarshal(out["data"], &encoded)
	json.Unmarshal(out["sha256"], &sha)
	json.Unmarshal(out["mtime"], &mtime)
	json.Unmarshal(out["path"], &real)
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		respondErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": path.Base(real)}))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Lectern-Sha256", sha)
	w.Header().Set("X-Lectern-Mtime", strconv.FormatInt(mtime, 10))
	// Header values must be ASCII-safe; the real path is percent-encoded.
	w.Header().Set("X-Lectern-Path", (&url.URL{Path: real}).EscapedPath())
	w.Write(data)
}
