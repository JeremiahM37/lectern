package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/auth"
	"github.com/JeremiahM37/lectern/v2/internal/clipboard"
	"github.com/JeremiahM37/lectern/v2/internal/executor"
)

// Clipboard bridge (docs/clipboard.md). Four endpoints:
//
//	GET  /api/clipboard/listen                  a client says it can answer, and waits (SSE)
//	POST /api/clipboard/active                  that client's person just typed
//	POST /api/clipboard/respond/{req}           the client's answer to one request
//	POST /api/hook/session/{id}/clipboard       the wl-paste/xclip shim in a session asks
//
// The first three are the person's: they need a signed-in human, so an agent
// on the host (who is KindLocal and not human unless auth is off entirely)
// cannot register as a clipboard or answer for one. The last is the agent's
// and is authenticated, like every /api/hook/session route, by that one
// session's hook token, so a session can ask only about itself.

func (s *Server) clipboardBroker() *clipboard.Broker {
	s.clipboardInit.Do(func() {
		if s.Clipboard == nil {
			s.Clipboard = clipboard.NewBroker()
		}
		if s.Clipboard.Audit == nil {
			s.Clipboard.Audit = func(f map[string]any) {
				args := make([]any, 0, len(f)*2)
				for k, v := range f {
					args = append(args, k, v)
				}
				if s.Log != nil {
					s.Log.Info("clipboard request", args...)
				}
			}
		}
	})
	return s.Clipboard
}

var clientIDPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{4,64}$`)

func ownerKey(p auth.Principal) string { return p.Kind + ":" + p.Login }

func (s *Server) clipboardListen(w http.ResponseWriter, r *http.Request) {
	if !s.requireHuman(w, r, "Offering a clipboard") {
		return
	}
	principal, _ := auth.FromContext(r.Context())
	q := r.URL.Query()
	id := q.Get("client")
	if !clientIDPattern.MatchString(id) {
		httpError(w, 400, "client must be 4-64 letters, digits, dots, dashes or underscores")
		return
	}
	var session int64
	if v := q.Get("session"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n <= 0 {
			httpError(w, 400, "bad session")
			return
		}
		if _, err := s.DB.Session(n); err != nil {
			httpError(w, 404, "no such session")
			return
		}
		session = n
	}
	kind := q.Get("kind")
	if len(kind) > 16 || kind == "" {
		kind = "client"
	}
	draining := s.streamDone()
	flusher, ok := w.(http.Flusher)
	if !ok {
		httpError(w, 500, "streaming unsupported")
		return
	}
	b := s.clipboardBroker()
	prov, unregister := b.Register(clipboard.Provider{
		ID: id, Session: session, Kind: kind, Login: ownerKey(principal),
		CanRead: q.Get("can_read") != "0", Always: q.Get("always") == "1",
	})
	defer unregister()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(200)
	io.WriteString(w, ": connected\n\n")
	flusher.Flush()
	keepalive := newTicker(15)
	defer keepalive.Stop()
	for {
		select {
		case <-draining:
			return
		case <-r.Context().Done():
			return
		case req, ok := <-prov.Requests():
			if !ok {
				return
			}
			fmt.Fprintf(w, "event: request\ndata: {\"id\":%q,\"op\":%q,\"type\":%q}\n\n", req.ID, req.Op, req.Type)
			flusher.Flush()
		case <-keepalive.C:
			io.WriteString(w, ": keepalive\n\n")
			flusher.Flush()
		}
	}
}

func (s *Server) clipboardActive(w http.ResponseWriter, r *http.Request) {
	if !s.requireHuman(w, r, "Reporting activity") {
		return
	}
	principal, _ := auth.FromContext(r.Context())
	if err := s.clipboardBroker().Touch(r.URL.Query().Get("client"), ownerKey(principal)); err != nil {
		httpError(w, 404, "no such client")
		return
	}
	w.WriteHeader(204)
}

func (s *Server) clipboardRespond(w http.ResponseWriter, r *http.Request) {
	if !s.requireHuman(w, r, "Answering a clipboard request") {
		return
	}
	principal, _ := auth.FromContext(r.Context())
	b := s.clipboardBroker()
	res := clipboard.Result{}
	if r.Header.Get("X-Clipboard-Status") != "unavailable" {
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, int64(b.MaxBytes)+1))
		if err != nil {
			var tooBig *http.MaxBytesError
			if !errors.As(err, &tooBig) {
				httpError(w, 400, "could not read the answer")
				return
			}
			body = nil // oversize: answered as unavailable
		}
		res.Data = body
		res.Type = r.Header.Get("Content-Type")
		for _, t := range strings.Split(r.Header.Get("X-Clipboard-Types"), ",") {
			if t = strings.TrimSpace(t); t != "" {
				res.Types = append(res.Types, t)
			}
		}
		res.OK = len(body) > 0 || len(res.Types) > 0
	}
	if err := b.Deliver(r.PathValue("req"), r.URL.Query().Get("client"), ownerKey(principal), res); err != nil {
		httpError(w, 404, "no such request")
		return
	}
	w.WriteHeader(204)
}

// hookSessionClipboard is the shim's call. The body names what it wants;
// the answer is the raw bytes (or the type list) of the clipboard.
func (s *Server) hookSessionClipboard(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.sessionFromHookAuth(w, r)
	if !ok {
		return
	}
	var req struct {
		Op   string `json:"op"`
		Type string `json:"type"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req); err != nil {
		httpError(w, 400, "bad request")
		return
	}
	req.Type = strings.ToLower(req.Type)
	res, err := s.clipboardBroker().Ask(r.Context(), sess.ID, req.Op, req.Type)
	if err != nil {
		w.Header().Set("X-Clipboard-Status", "unavailable")
		writeJSON(w, http.StatusNotFound, map[string]any{"detail": err.Error()})
		return
	}
	if req.Op == clipboard.OpList {
		w.Header().Set("X-Clipboard-Types", strings.Join(res.Types, ","))
		w.Header().Set("Content-Type", "text/plain")
		io.WriteString(w, strings.Join(res.Types, "\n"))
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("X-Clipboard-Type", req.Type)
	w.Write(res.Data)
}

// mirrorExecutor is the machine a session runs on.
func (s *Server) mirrorExecutor(w http.ResponseWriter, r *http.Request) (executor.Executor, bool) {
	n, err := strconv.ParseInt(r.URL.Query().Get("session"), 10, 64)
	if err != nil || n <= 0 {
		httpError(w, 400, "session is required")
		return nil, false
	}
	sess, err := s.DB.Session(n)
	if err != nil {
		httpError(w, 404, "no such session")
		return nil, false
	}
	target, err := s.DB.Target(sess.TargetID)
	if err != nil || target.Kind == "sandbox" {
		httpError(w, 409, "this session's machine has no clipboard bridge")
		return nil, false
	}
	ex, err := s.Reg.For(target)
	if err != nil {
		respondErr(w, err)
		return nil, false
	}
	return ex, true
}

var mirrorTypes = map[string]bool{"image/png": true, "image/jpeg": true, "image/gif": true, "image/webp": true, "image/bmp": true, "text/plain": true}

// clipboardMirror is PUT /api/clipboard/mirror?session=ID: the person's client
// copying what is on its clipboard to the headless clipboard of the machine
// the session runs on, so a program that reads the clipboard natively finds it
// there. Only a signed-in person may; the item expires on its own; it is
// readable only by programs of the OS user the session runs as.
func (s *Server) clipboardMirror(w http.ResponseWriter, r *http.Request) {
	if !s.requireHuman(w, r, "Copying to a session's clipboard") {
		return
	}
	mime := strings.ToLower(strings.TrimSpace(strings.Split(r.Header.Get("Content-Type"), ";")[0]))
	if !mirrorTypes[mime] {
		httpError(w, 415, "only png, jpeg, gif, webp and bmp images, and plain text, can be mirrored")
		return
	}
	ex, ok := s.mirrorExecutor(w, r)
	if !ok {
		return
	}
	b := s.clipboardBroker()
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, int64(b.MaxBytes)+1))
	if err != nil || len(body) == 0 {
		httpError(w, 413, "the clipboard item is empty or larger than %d MB", b.MaxBytes>>20)
		return
	}
	s.uploadMu.Lock()
	if s.uploadCount >= 4 {
		s.uploadMu.Unlock()
		httpError(w, 429, "busy; try again shortly")
		return
	}
	s.uploadCount++
	s.uploadMu.Unlock()
	defer func() { s.uploadMu.Lock(); s.uploadCount--; s.uploadMu.Unlock() }()
	principal, _ := auth.FromContext(r.Context())
	err = clipboard.Mirror(r.Context(), ex, s.ClipboardBin, mime, body)
	if s.Log != nil {
		s.Log.Info("clipboard mirror", "session", r.URL.Query().Get("session"), "type", mime, "bytes", len(body), "login", ownerKey(principal), "err", err)
	}
	if err != nil {
		httpError(w, 502, "could not reach the machine's clipboard: %s", err)
		return
	}
	w.WriteHeader(204)
}

func (s *Server) clipboardMirrorClear(w http.ResponseWriter, r *http.Request) {
	if !s.requireHuman(w, r, "Clearing a session's clipboard") {
		return
	}
	ex, ok := s.mirrorExecutor(w, r)
	if !ok {
		return
	}
	if err := clipboard.Clear(r.Context(), ex, s.ClipboardBin); err != nil {
		httpError(w, 502, "%s", err)
		return
	}
	w.WriteHeader(204)
}
