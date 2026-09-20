package api

import (
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/JeremiahM37/agentdeck/internal/store"
)

// Media is the channel from an agent back to the operator: "here is the feature
// working", as a recording, a screenshot, a report or a link to the server it
// started. Files are copied into the control plane's own store, because the
// worktree that produced them may be gone by the time anyone looks.

type mediaLinkIn struct {
	URL         string `json:"url"`
	Title       string `json:"title"`
	Note        string `json:"note"`
	SessionID   int64  `json:"session_id"`
	HintSession int64  `json:"hint_session_id"`
	TmuxSession string `json:"tmux_session"`
	Source      string `json:"source"`
}

// mediaSession resolves who is posting. An explicit id must exist. A hint — the
// id or tmux name the poster inherited from its environment — is only evidence:
// it may have been inherited from a session of some other AgentDeck, and an
// unknown one still posts, unattributed, because losing the evidence is worse
// than losing its label.
func (s *Server) mediaSession(id int64, tmux string, hints ...int64) (*int64, error) {
	if id > 0 {
		if _, err := s.DB.Session(id); err != nil {
			return nil, invalid("no session %d", id)
		}
		return &id, nil
	}
	for _, hint := range hints {
		if hint > 0 {
			if _, err := s.DB.Session(hint); err == nil {
				return &hint, nil
			}
		}
	}
	if tmux = strings.TrimSpace(tmux); tmux != "" {
		if found, err := s.DB.LiveSessionByTmux(tmux); err == nil {
			return &found, nil
		}
	}
	return nil, nil
}

func mediaSource(v string) string {
	if v == "mcp" || v == "cli" {
		return v
	}
	return ""
}

func (s *Server) listMedia(w http.ResponseWriter, r *http.Request) {
	sessionID, _ := strconv.ParseInt(r.URL.Query().Get("session_id"), 10, 64)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	rows, err := s.DB.MediaList(sessionID, limit)
	if err != nil {
		respondErr(w, err)
		return
	}
	writeJSON(w, 200, rows)
}

// postMedia takes a link as JSON, or a file as the raw request body with its
// metadata in the query. A raw body streams to disk: a demo recording is far
// past what the multipart attachment path is willing to hold in memory.
func (s *Server) postMedia(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		s.postMediaLink(w, r)
		return
	}
	q := r.URL.Query()
	name := filepath.Base(strings.ReplaceAll(clip(trimSpace(q.Get("name")), 180), "\\", "/"))
	if name == "" || name == "." || name == "/" {
		httpError(w, 422, "name is required: the file's base name, with its extension")
		return
	}
	explicit, _ := strconv.ParseInt(q.Get("session_id"), 10, 64)
	hint, _ := strconv.ParseInt(q.Get("hint_session_id"), 10, 64)
	sessionID, err := s.mediaSession(explicit, q.Get("tmux_session"), hint)
	if err != nil {
		respondErr(w, err)
		return
	}
	dir := s.Cfg.MediaDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		httpError(w, 500, "media store: %s", err)
		return
	}
	part, err := os.CreateTemp(dir, "upload-*.part")
	if err != nil {
		httpError(w, 500, "media store: %s", err)
		return
	}
	defer os.Remove(part.Name()) // a no-op once the file has been renamed into place
	limit := s.Cfg.MediaLimit()
	size, err := io.Copy(part, io.LimitReader(r.Body, limit+1))
	closeErr := part.Close()
	switch {
	case err != nil:
		httpError(w, 400, "upload interrupted: %s", err)
		return
	case closeErr != nil:
		httpError(w, 500, "media store: %s", closeErr)
		return
	case size > limit:
		httpError(w, 413, "file exceeds the %d MiB media limit (AGENTDECK_MEDIA_MAX_MB)", limit>>20)
		return
	case size == 0:
		httpError(w, 422, "the file is empty")
		return
	}
	row, err := s.DB.InsertMedia(&store.Media{SessionID: sessionID, Kind: "file",
		Title: clip(trimSpace(q.Get("title")), 200), Note: clip(trimSpace(q.Get("note")), 4000), Name: name,
		Mime: mediaMime(name, part.Name()), Size: size, Source: mediaSource(q.Get("source"))})
	if err != nil {
		respondErr(w, err)
		return
	}
	// The blob is named by row id alone: nothing a poster chose reaches the
	// filesystem, so a hostile name cannot steer where the bytes land.
	blob := fmt.Sprintf("media-%d", row.ID)
	if err := os.Rename(part.Name(), filepath.Join(dir, blob)); err != nil {
		_ = s.DB.DeleteMedia(row.ID)
		httpError(w, 500, "media store: %s", err)
		return
	}
	if err := s.DB.Update("media", row.ID, map[string]any{"blob": blob}); err != nil {
		respondErr(w, err)
		return
	}
	row.Blob = blob
	s.Bus.Publish("board", "media", row)
	writeJSON(w, 201, row)
}

func (s *Server) postMediaLink(w http.ResponseWriter, r *http.Request) {
	var in mediaLinkIn
	if err := decodeBody(r, &in); err != nil {
		httpError(w, 400, "%s", err)
		return
	}
	parsed, err := url.Parse(strings.TrimSpace(in.URL))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		httpError(w, 422, "url must be an http(s) address")
		return
	}
	sessionID, err := s.mediaSession(in.SessionID, in.TmuxSession, in.HintSession)
	if err != nil {
		respondErr(w, err)
		return
	}
	row, err := s.DB.InsertMedia(&store.Media{SessionID: sessionID, Kind: "link",
		Title: clip(trimSpace(in.Title), 200), Note: clip(trimSpace(in.Note), 4000), URL: parsed.String(),
		Source: mediaSource(in.Source)})
	if err != nil {
		respondErr(w, err)
		return
	}
	s.Bus.Publish("board", "media", row)
	writeJSON(w, 201, row)
}

// mediaMime trusts the extension first — it is what the poster meant — and
// sniffs only when the extension says nothing.
func mediaMime(name, path string) string {
	if t := mime.TypeByExtension(strings.ToLower(filepath.Ext(name))); t != "" {
		return t
	}
	f, err := os.Open(path)
	if err != nil {
		return "application/octet-stream"
	}
	defer f.Close()
	head := make([]byte, 512)
	n, _ := f.Read(head)
	return http.DetectContentType(head[:n])
}

func (s *Server) mediaParam(w http.ResponseWriter, r *http.Request) (*store.Media, bool) {
	id, err := pathID(r, "id")
	if err != nil {
		httpError(w, 404, "no such media")
		return nil, false
	}
	row, err := s.DB.MediaByID(id)
	if err != nil {
		respondErr(w, err)
		return nil, false
	}
	return row, true
}

// mediaContent serves the bytes. ServeContent answers range requests, which is
// what lets a browser seek inside a recording instead of downloading all of it.
func (s *Server) mediaContent(w http.ResponseWriter, r *http.Request) {
	row, ok := s.mediaParam(w, r)
	if !ok {
		return
	}
	if row.Kind != "file" || row.Blob == "" {
		httpError(w, 404, "this entry has no file")
		return
	}
	f, err := os.Open(filepath.Join(s.Cfg.MediaDir(), filepath.Base(row.Blob)))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			httpError(w, 404, "the file is no longer in the media store")
			return
		}
		httpError(w, 500, "%s", err)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		httpError(w, 500, "%s", err)
		return
	}
	// Posted content is whatever an agent produced, HTML reports included. The
	// sandbox gives it an opaque origin, so a page can render and run its own
	// scripts without being able to act as the AgentDeck origin it is served from.
	w.Header().Set("Content-Security-Policy", "sandbox allow-scripts")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Type", row.Mime)
	disposition := "inline"
	if r.URL.Query().Get("download") == "1" {
		disposition = "attachment"
	}
	w.Header().Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": row.Name}))
	http.ServeContent(w, r, "", st.ModTime(), f)
}

func (s *Server) deleteMedia(w http.ResponseWriter, r *http.Request) {
	row, ok := s.mediaParam(w, r)
	if !ok {
		return
	}
	if err := s.DB.DeleteMedia(row.ID); err != nil {
		respondErr(w, err)
		return
	}
	if row.Blob != "" {
		_ = os.Remove(filepath.Join(s.Cfg.MediaDir(), filepath.Base(row.Blob)))
	}
	s.Bus.Publish("board", "media_deleted", map[string]any{"id": row.ID})
	w.WriteHeader(204)
}
