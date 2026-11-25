package api

// Updating this Lectern from its own UI (docs/sessions-restart.md, "Update
// and restart"). GET /api/update says whether a newer release exists and
// whether this server can install it; POST /api/update installs it (the same
// download, checksum and test run `lectern update` does), restarts the
// server in place, and the new server then restarts every session with its
// own conversation, so the whole board is on the new build without losing
// anyone's place.

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/auth"
	"github.com/JeremiahM37/lectern/v2/internal/version"
)

// SelfUpdater installs releases over the running program. The command
// package provides it; a server without one (the private local runtime, a
// test) cannot update itself.
type SelfUpdater interface {
	// Latest is the newest release's tag, such as v2.10.6.
	Latest(ctx context.Context) (string, error)
	// Blocked says why this program cannot be replaced from here ("" when it can).
	Blocked() string
	// Install puts tag in place of the running program.
	Install(ctx context.Context, tag string) error
}

type updateState struct {
	mu       sync.Mutex
	latest   string
	checked  time.Time
	checkErr string
	phase    string // "", "installing", "restarting", "failed"
	target   string
	err      string
}

// UpdateView is GET /api/update.
type UpdateView struct {
	Current   string `json:"current"`
	Latest    string `json:"latest,omitempty"`
	Available bool   `json:"available"`
	CanUpdate bool   `json:"can_update"`
	Reason    string `json:"reason,omitempty"`
	Phase     string `json:"phase,omitempty"`
	Target    string `json:"target,omitempty"`
	Error     string `json:"error,omitempty"`
	// SessionsRestart is "scheduled" right after an update restarted the
	// server, until the restart of every session has been started.
	SessionsRestart string `json:"sessions_restart,omitempty"`
}

func (s *Server) updateView(ctx context.Context, refresh bool) UpdateView {
	st := &s.update
	view := UpdateView{Current: version.Version}
	if s.Updater == nil {
		view.Reason = "this Lectern cannot update itself; run lectern update where it is installed"
		return view
	}
	st.mu.Lock()
	stale := refresh || st.checked.IsZero() || time.Since(st.checked) > 30*time.Minute
	st.mu.Unlock()
	if stale {
		check, cancel := context.WithTimeout(ctx, 20*time.Second)
		latest, err := s.Updater.Latest(check)
		cancel()
		st.mu.Lock()
		st.checked = time.Now()
		if err != nil {
			st.checkErr = "could not reach the release list: " + err.Error()
		} else {
			st.latest, st.checkErr = latest, ""
		}
		st.mu.Unlock()
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	view.Latest, view.Phase, view.Target, view.Error = st.latest, st.phase, st.target, st.err
	if st.checkErr != "" && view.Error == "" {
		view.Error = st.checkErr
	}
	if st.latest != "" {
		view.Available = releaseNewer(view.Current, st.latest)
	}
	view.Reason = s.Updater.Blocked()
	view.CanUpdate = view.Reason == ""
	if s.sessionsRestartScheduled.Load() {
		view.SessionsRestart = "scheduled"
	}
	return view
}

// releaseNewer says whether tag is a later release than current. The same
// release with build metadata ("2.10.6+abc") is not newer, and neither is
// anything that cannot be compared.
func releaseNewer(current, tag string) bool {
	older, known := version.Older(version.Info{Version: current}, version.Info{Version: tag})
	return known && older
}

func (s *Server) getUpdate(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.updateView(r.Context(), r.URL.Query().Get("refresh") == "1"))
}

// postUpdate installs the latest release (or {"version"}), answers, and then
// restarts the server; with restart_sessions (the default) the new server
// restarts every session with its own conversation.
func (s *Server) postUpdate(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())
	if s.Auth == nil || !s.Auth.CanDecide(p) {
		httpError(w, 403, "updating Lectern requires your signed-in identity, not an automated caller")
		return
	}
	if s.Updater == nil || s.Restart == nil {
		httpError(w, 501, "this Lectern cannot update itself; run lectern update where it is installed")
		return
	}
	var req struct {
		Version         string `json:"version"`
		RestartSessions *bool  `json:"restart_sessions"`
	}
	if err := decodeBody(r, &req); err != nil {
		httpError(w, 400, "%s", err)
		return
	}
	if reason := s.Updater.Blocked(); reason != "" {
		httpError(w, 409, "%s", reason)
		return
	}
	st := &s.update
	st.mu.Lock()
	if st.phase == "installing" || st.phase == "restarting" {
		st.mu.Unlock()
		httpError(w, 409, "an update is already running")
		return
	}
	st.phase, st.err, st.target = "installing", "", ""
	st.mu.Unlock()

	fail := func(code int, message string) {
		st.mu.Lock()
		st.phase, st.err = "failed", message
		st.mu.Unlock()
		httpError(w, code, "%s", message)
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Minute)
	defer cancel()
	tag := strings.TrimSpace(req.Version)
	if tag == "" {
		latest, err := s.Updater.Latest(ctx)
		if err != nil {
			fail(502, "could not reach the release list: "+err.Error())
			return
		}
		tag = latest
		if !releaseNewer(version.Version, tag) {
			st.mu.Lock()
			st.phase = ""
			st.mu.Unlock()
			httpError(w, 409, "Lectern %s is already the latest release", version.Version)
			return
		}
	}
	if !strings.HasPrefix(tag, "v") {
		tag = "v" + tag
	}
	st.mu.Lock()
	st.target = tag
	st.mu.Unlock()
	if err := s.Updater.Install(ctx, tag); err != nil {
		fail(502, err.Error())
		return
	}
	restartSessions := req.RestartSessions == nil || *req.RestartSessions
	st.mu.Lock()
	st.phase = "restarting"
	st.mu.Unlock()
	s.Log.Info("installed update; restarting", "from", version.Version, "to", tag, "restart_sessions", restartSessions)
	writeJSON(w, 202, map[string]any{"from": version.Version, "to": tag, "restart_sessions": restartSessions})
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	go func() {
		// Let the answer reach the browser first.
		time.Sleep(500 * time.Millisecond)
		s.Restart(restartSessions)
	}()
}

// RestartSessionsAfterUpdate runs once a server started by an update is
// ready: every session restarts with its own conversation (idle ones now,
// busy ones once they wait for you), as POST /api/restart-all does.
func (s *Server) RestartSessionsAfterUpdate(delay time.Duration) {
	s.sessionsRestartScheduled.Store(true)
	go func() {
		defer s.sessionsRestartScheduled.Store(false)
		time.Sleep(delay)
		out, err := s.Sessions.RestartAll(context.Background())
		if err != nil {
			s.Log.Error("restarting sessions after the update failed", "err", err)
			return
		}
		s.Log.Info("restarting sessions after the update", "sessions", len(out))
	}()
}
