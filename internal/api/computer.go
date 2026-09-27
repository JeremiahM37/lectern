package api

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/desktop"
	"github.com/JeremiahM37/lectern/v2/internal/forward"
)

// Computer use (docs/browser.md): an agent operates a live desktop — the one
// `lectern live` / open_live_view started for its session — by screenshot,
// click, type and key. It is off unless a person turns it on, for the project
// or for that one desktop, and the operator can stop it at any moment.

type computerIn struct {
	desktop.Action
	LiveID      int64  `json:"live_id"`
	SessionID   int64  `json:"session_id"`
	HintSession int64  `json:"hint_session_id"`
	TmuxSession string `json:"tmux_session"`
	Screenshot  bool   `json:"screenshot"`
}

type liveDesktop struct {
	view *forward.Forward
	d    *desktop.Desktop
}

// desktopsOf lists the live desktops a session started, newest first.
func (s *Server) desktopsOf(sessionID int64) []liveDesktop {
	if s.live.forwards == nil {
		return nil
	}
	var out []liveDesktop
	views := s.live.forwards.List()
	for i := len(views) - 1; i >= 0; i-- {
		v := views[i]
		if v.Kind != "desktop" || v.SessionID == nil || *v.SessionID != sessionID {
			continue
		}
		s.live.mu.Lock()
		d := s.live.desktops[v.ID]
		s.live.mu.Unlock()
		if d != nil {
			out = append(out, liveDesktop{view: v, d: d})
		}
	}
	return out
}

func (s *Server) liveDesktopByID(id int64) (liveDesktop, bool) {
	if s.live.forwards == nil {
		return liveDesktop{}, false
	}
	for _, v := range s.live.forwards.List() {
		if v.ID == id && v.Kind == "desktop" {
			s.live.mu.Lock()
			d := s.live.desktops[id]
			s.live.mu.Unlock()
			return liveDesktop{view: v, d: d}, d != nil
		}
	}
	return liveDesktop{}, false
}

func (s *Server) deskState(id int64) *deskControl {
	st := s.browsersInit()
	st.mu.Lock()
	defer st.mu.Unlock()
	c := st.desks[id]
	if c == nil {
		c = &deskControl{}
		st.desks[id] = c
	}
	return c
}

// computerAllowed says whether agents may operate this desktop, and if not, why.
func (s *Server) computerAllowed(ld liveDesktop) (bool, string) {
	c := s.deskState(ld.view.ID)
	s.browsers.mu.Lock()
	allowed, stopped := c.allowed, c.stopped
	s.browsers.mu.Unlock()
	if stopped {
		return false, "the operator stopped agent control of this desktop"
	}
	if allowed {
		return true, ""
	}
	if ld.view.SessionID != nil {
		if sess, err := s.DB.Session(*ld.view.SessionID); err == nil && sess.ProjectID != nil {
			if p, err := s.DB.Project(*sess.ProjectID); err == nil && p.ComputerUse == 1 {
				return true, ""
			}
		}
	}
	return false, "computer use is off: a person has to allow it, in the project's settings or on this desktop in the Browser pane"
}

func (s *Server) deskRunner(ld liveDesktop) (desktop.Runner, error) {
	target, err := s.DB.Target(ld.view.TargetID)
	if err != nil {
		return nil, err
	}
	ex, err := s.Reg.For(target)
	if err != nil {
		return nil, err
	}
	return liveRunner(ex), nil
}

func (s *Server) agentComputer(w http.ResponseWriter, r *http.Request) {
	var in computerIn
	if err := decodeBody(r, &in); err != nil {
		httpError(w, 422, "%s", err)
		return
	}
	id, err := s.mediaSession(in.SessionID, in.TmuxSession, in.HintSession)
	if err != nil || id == nil {
		httpError(w, 422, "no Lectern session: run this inside a Lectern session, or pass session_id")
		return
	}
	desks := s.desktopsOf(*id)
	var ld liveDesktop
	for _, d := range desks {
		if in.LiveID == 0 || d.view.ID == in.LiveID {
			ld = d
			break
		}
	}
	if ld.d == nil {
		httpError(w, 409, "this session has no live desktop; start one with open_live_view (or `lectern live`) first")
		return
	}
	if in.Type == "status" {
		ok, why := s.computerAllowed(ld)
		writeJSON(w, 200, map[string]any{"live_id": ld.view.ID, "display": ld.view.Detail["display"], "allowed": ok,
			"reason": why, "width": ld.d.Width, "height": ld.d.Height})
		return
	}
	if ok, why := s.computerAllowed(ld); !ok {
		httpError(w, 403, "%s", why)
		return
	}
	run, err := s.deskRunner(ld)
	if err != nil {
		respondErr(w, err)
		return
	}
	c := s.deskState(ld.view.ID)
	s.browsers.mu.Lock()
	c.lastAgent, c.lastAction = time.Now(), in.Type
	s.browsers.mu.Unlock()
	s.Bus.Publish("board", "computer", map[string]any{"live_id": ld.view.ID, "action": in.Type})
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	out := map[string]any{"live_id": ld.view.ID, "width": ld.d.Width, "height": ld.d.Height}
	if in.Type == "windows" {
		wins, err := desktop.Windows(ctx, run, ld.d)
		if err != nil {
			deskError(w, err)
			return
		}
		out["windows"] = wins
		writeJSON(w, 200, out)
		return
	}
	if in.Type != "screenshot" {
		if err := desktop.Act(ctx, run, ld.d, in.Action); err != nil {
			deskError(w, err)
			return
		}
		out["done"] = in.Type
	}
	if in.Type == "screenshot" || in.Screenshot {
		png, err := desktop.Screenshot(ctx, run, ld.d)
		if err != nil {
			deskError(w, err)
			return
		}
		out["screenshot"] = base64.StdEncoding.EncodeToString(png)
	}
	writeJSON(w, 200, out)
}

func deskError(w http.ResponseWriter, err error) {
	var missing *desktop.MissingError
	if errors.As(err, &missing) {
		httpError(w, 409, "%s", err)
		return
	}
	httpError(w, 422, "%s", err)
}

// liveComputer reports, and lets the operator change, whether agents may
// operate one desktop.
func (s *Server) liveComputer(w http.ResponseWriter, r *http.Request) {
	id, ok := s.liveParam(w, r)
	if !ok {
		return
	}
	ld, found := s.liveDesktopByID(id)
	if !found {
		httpError(w, 404, "no such desktop")
		return
	}
	c := s.deskState(id)
	if r.Method == http.MethodPost {
		var in struct {
			Allow *bool `json:"allow"`
			Stop  *bool `json:"stop"`
		}
		if err := decodeBody(r, &in); err != nil {
			httpError(w, 422, "%s", err)
			return
		}
		// Stopping only takes power away, so anyone may; granting it is a
		// person's decision.
		if (in.Allow != nil && *in.Allow) || (in.Stop != nil && !*in.Stop) {
			if !s.requireHuman(w, r, "allowing agent control of a desktop") {
				return
			}
		}
		s.browsers.mu.Lock()
		if in.Allow != nil {
			c.allowed = *in.Allow
			if *in.Allow {
				c.stopped = false
			}
		}
		if in.Stop != nil {
			c.stopped = *in.Stop
			if *in.Stop {
				c.lastAgent = time.Time{}
			}
		}
		s.browsers.mu.Unlock()
		s.Log.Info("computer use: control changed", "live", id, "allowed", c.allowed, "stopped", c.stopped)
	}
	allowed, why := s.computerAllowed(ld)
	s.browsers.mu.Lock()
	out := map[string]any{"live_id": id, "allowed": allowed, "reason": why, "allowed_here": c.allowed,
		"stopped": c.stopped, "agent_active": time.Since(c.lastAgent) < agentActiveFor, "last_action": c.lastAction,
		"width": ld.d.Width, "height": ld.d.Height, "display": ld.view.Detail["display"]}
	s.browsers.mu.Unlock()
	writeJSON(w, 200, out)
}

// liveScreenshot is a still of a desktop, for watching one where its noVNC
// port cannot be reached (over the relay, or from a secure page).
func (s *Server) liveScreenshot(w http.ResponseWriter, r *http.Request) {
	id, ok := s.liveParam(w, r)
	if !ok {
		return
	}
	ld, found := s.liveDesktopByID(id)
	if !found {
		httpError(w, 404, "no such desktop")
		return
	}
	run, err := s.deskRunner(ld)
	if err != nil {
		respondErr(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	png, err := desktop.Screenshot(ctx, run, ld.d)
	if err != nil {
		deskError(w, err)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(png)
}
