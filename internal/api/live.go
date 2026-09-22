package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/JeremiahM37/lectern/internal/desktop"
	"github.com/JeremiahM37/lectern/internal/executor"
	"github.com/JeremiahM37/lectern/internal/forward"
	"github.com/JeremiahM37/lectern/internal/store"
)

// Live is how the operator reaches what a target keeps on its own localhost: a
// forwarded port for a service an agent started, or a desktop for anything that
// draws a window. Both widen what is reachable, so both are explicit, listed in
// one place, and gone when their session ends, their time runs out, or the
// server restarts.

// liveState is built on first use, so a server that never forwards anything
// never listens on anything extra.
type liveState struct {
	once     sync.Once
	forwards *forward.Manager
	owner    string
	mu       sync.Mutex
	desktops map[int64]*desktop.Desktop // by forward id
	stop     chan struct{}
	stopOnce sync.Once
}

func (s *Server) liveInit() *liveState {
	l := &s.live
	l.once.Do(func() {
		lo, hi := 19200, 19299
		if a, b, ok := strings.Cut(os.Getenv("LECTERN_LIVE_PORTS"), "-"); ok {
			if x, err1 := strconv.Atoi(a); err1 == nil {
				if y, err2 := strconv.Atoi(b); err2 == nil && x > 1023 && y >= x && y < 65536 {
					lo, hi = x, y
				}
			}
		}
		raw := make([]byte, 8)
		_, _ = rand.Read(raw)
		l.owner = hex.EncodeToString(raw)
		l.forwards = forward.New(s.Cfg.Host, lo, hi)
		l.desktops = map[int64]*desktop.Desktop{}
		l.stop = make(chan struct{})
		go func() {
			tick := time.NewTicker(30 * time.Second)
			defer tick.Stop()
			for {
				select {
				case <-l.stop:
					return
				case <-tick.C:
					gone := l.forwards.Reap(store.Now(), func(id int64) bool {
						sess, err := s.DB.Session(id)
						return err != nil || sess.EndedAt != nil
					})
					if len(gone) > 0 {
						s.Bus.Publish("board", "live", map[string]any{"closed": gone})
					}
				}
			}
		}()
	})
	return l
}

// liveShutdown closes every forward, which stops every desktop with it.
func (s *Server) liveShutdown() {
	if s.live.forwards == nil {
		return
	}
	s.live.stopOnce.Do(func() { close(s.live.stop) })
	s.live.forwards.CloseAll()
}

// liveAllowed refuses unless the operator turned live views on, and again when
// the API is token-protected. A forwarded port is raw TCP and cannot check a
// bearer token, so opening one would put an unguarded door beside a guarded
// one. An operator who wants that says so.
func (s *Server) liveAllowed(w http.ResponseWriter) bool {
	if !s.Cfg.Live {
		httpError(w, 409, "live views are off on this server. They open extra listening ports and let an agent "+
			"start a desktop that can be driven from the network, so they are opt-in: set LECTERN_LIVE=1 and restart")
		return false
	}
	if s.Cfg.AuthToken != "" && os.Getenv("LECTERN_LIVE_UNAUTHENTICATED") != "1" {
		httpError(w, 409, "this server requires a token, and a forwarded port cannot check one; "+
			"set LECTERN_LIVE_UNAUTHENTICATED=1 to allow forwards anyone who can reach the server may use")
		return false
	}
	return true
}

type liveIn struct {
	SessionID   int64  `json:"session_id"`
	HintSession int64  `json:"hint_session_id"`
	TmuxSession string `json:"tmux_session"`
	TargetID    int64  `json:"target_id"`
	Title       string `json:"title"`
	Port        int    `json:"port"`
	URL         string `json:"url"`
	Width       int    `json:"width"`
	Height      int    `json:"height"`
	TTLMinutes  int    `json:"ttl_minutes"`
}

// liveTarget resolves where to look: the poster's session, a named machine, or
// the default one. It returns the executor as a dialer, or the reason it cannot
// be one.
func (s *Server) liveTarget(in liveIn) (*store.Target, *int64, executor.Executor, executor.Dialer, error) {
	sessionID, err := s.mediaSession(in.SessionID, in.TmuxSession, in.HintSession)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	targetID := in.TargetID
	if sessionID != nil {
		sess, err := s.DB.Session(*sessionID)
		if err != nil {
			return nil, nil, nil, nil, err
		}
		if targetID != 0 && targetID != sess.TargetID {
			return nil, nil, nil, nil, invalid("session %d does not run on target %d", sess.ID, targetID)
		}
		targetID = sess.TargetID
	}
	if targetID == 0 {
		targetID = s.defaultTargetID()
	}
	target, err := s.DB.Target(targetID)
	if err != nil {
		return nil, nil, nil, nil, invalid("no such target")
	}
	ex, err := s.Reg.For(target)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	dialer, ok := ex.(executor.Dialer)
	if !ok {
		return nil, nil, nil, nil, executor.ErrNoDial
	}
	return target, sessionID, ex, dialer, nil
}

func liveTTL(minutes int) time.Duration {
	if minutes <= 0 {
		minutes = 240
	}
	if minutes > 1440 {
		minutes = 1440
	}
	return time.Duration(minutes) * time.Minute
}

func liveError(w http.ResponseWriter, err error) {
	var missing *desktop.MissingError
	switch {
	case errors.Is(err, executor.ErrNoDial), errors.Is(err, forward.ErrFull), errors.As(err, &missing):
		httpError(w, 409, "%s", err)
	default:
		respondErr(w, err)
	}
}

// listLive also says whether the feature is on, so a client offers it only
// where it would work.
func (s *Server) listLive(w http.ResponseWriter, r *http.Request) {
	views := []*forward.Forward{}
	if s.live.forwards != nil {
		views = s.live.forwards.List()
	}
	writeJSON(w, 200, map[string]any{"enabled": s.Cfg.Live, "views": views})
}

// openLivePort forwards one port on a target's localhost.
func (s *Server) openLivePort(w http.ResponseWriter, r *http.Request) {
	var in liveIn
	if err := decodeBody(r, &in); err != nil {
		httpError(w, 422, "%s", err)
		return
	}
	if !s.liveAllowed(w) {
		return
	}
	target, sessionID, _, dialer, err := s.liveTarget(in)
	if err != nil {
		liveError(w, err)
		return
	}
	title := clip(trimSpace(in.Title), 120)
	if title == "" {
		title = fmt.Sprintf("localhost:%d", in.Port)
	}
	f, err := s.liveInit().forwards.Open(dialer.DialTarget, forward.Spec{Kind: "port", Title: title,
		TargetID: target.ID, TargetName: target.Name, SessionID: sessionID, Port: in.Port, TTL: liveTTL(in.TTLMinutes)})
	if err != nil {
		if errors.Is(err, forward.ErrFull) {
			liveError(w, err)
		} else {
			httpError(w, 422, "%s", err)
		}
		return
	}
	s.Log.Info("live: port forwarded", "target", target.Name, "port", in.Port, "listen", f.ListenPort)
	s.Bus.Publish("board", "live", f)
	writeJSON(w, 201, f)
}

func liveRunner(ex executor.Executor) desktop.Runner {
	return func(ctx context.Context, script string) (string, error) {
		r, err := ex.Run(ctx, script, executor.RunOpts{Timeout: 60})
		if err != nil {
			return "", err
		}
		if !r.OK() {
			return r.Stdout, fmt.Errorf("desktop script failed: %s", strings.TrimSpace(r.Stderr))
		}
		return r.Stdout, nil
	}
}

// openLiveDesktop starts a display on the target and forwards its web client.
func (s *Server) openLiveDesktop(w http.ResponseWriter, r *http.Request) {
	var in liveIn
	if err := decodeBody(r, &in); err != nil {
		httpError(w, 422, "%s", err)
		return
	}
	if !s.liveAllowed(w) {
		return
	}
	target, sessionID, ex, dialer, err := s.liveTarget(in)
	if err != nil {
		liveError(w, err)
		return
	}
	l := s.liveInit()
	run := liveRunner(ex)
	d, err := desktop.Start(r.Context(), run, l.owner, in.Width, in.Height)
	if err != nil {
		liveError(w, err)
		return
	}
	title := clip(trimSpace(in.Title), 120)
	if title == "" {
		title = "Live desktop"
	}
	detail := map[string]string{"display": fmt.Sprintf(":%d", d.Display),
		"width": strconv.Itoa(d.Width), "height": strconv.Itoa(d.Height)}
	// Settled before the forward exists: once it is listed, its detail is read
	// by other requests and must not change underneath them.
	if address := trimSpace(in.URL); address != "" {
		if name, err := desktop.OpenBrowser(r.Context(), run, d, address); err != nil {
			detail["browser_error"] = err.Error()
		} else {
			detail["browser"] = name
		}
	}
	var id int64
	f, err := l.forwards.Open(dialer.DialTarget, forward.Spec{Kind: "desktop", Title: title,
		TargetID: target.ID, TargetName: target.Name, SessionID: sessionID, Port: d.Port,
		TTL: liveTTL(in.TTLMinutes), Detail: detail,
		OnClose: func() {
			// The request that started it is long gone by the time this runs.
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := desktop.Stop(ctx, run, d.Dir); err != nil {
				s.Log.Warn("live: desktop did not stop cleanly", "target", target.Name, "dir", d.Dir, "err", err)
			}
			l.mu.Lock()
			delete(l.desktops, id)
			l.mu.Unlock()
		}})
	if err != nil {
		_ = desktop.Stop(context.Background(), run, d.Dir)
		liveError(w, err)
		return
	}
	id = f.ID
	l.mu.Lock()
	l.desktops[f.ID] = d
	l.mu.Unlock()
	s.Log.Info("live: desktop started", "target", target.Name, "display", d.Display, "listen", f.ListenPort)
	s.Bus.Publish("board", "live", f)
	writeJSON(w, 201, f)
}

func (s *Server) liveParam(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := pathID(r, "id")
	if err != nil || s.live.forwards == nil {
		httpError(w, 404, "no such live view")
		return 0, false
	}
	return id, true
}

// liveBrowser opens an address in a running desktop's browser.
func (s *Server) liveBrowser(w http.ResponseWriter, r *http.Request) {
	id, ok := s.liveParam(w, r)
	if !ok {
		return
	}
	var in liveIn
	if err := decodeBody(r, &in); err != nil {
		httpError(w, 422, "%s", err)
		return
	}
	s.live.mu.Lock()
	d := s.live.desktops[id]
	s.live.mu.Unlock()
	if d == nil {
		httpError(w, 404, "no such desktop")
		return
	}
	var owner *forward.Forward
	for _, f := range s.live.forwards.List() {
		if f.ID == id {
			owner = f
		}
	}
	if owner == nil {
		httpError(w, 404, "no such desktop")
		return
	}
	target, err := s.DB.Target(owner.TargetID)
	if err != nil {
		respondErr(w, err)
		return
	}
	ex, err := s.Reg.For(target)
	if err != nil {
		respondErr(w, err)
		return
	}
	name, err := desktop.OpenBrowser(r.Context(), liveRunner(ex), d, in.URL)
	if err != nil {
		httpError(w, 409, "%s", err)
		return
	}
	writeJSON(w, 200, map[string]any{"browser": name})
}

func (s *Server) closeLive(w http.ResponseWriter, r *http.Request) {
	id, ok := s.liveParam(w, r)
	if !ok {
		return
	}
	if !s.live.forwards.Close(id) {
		httpError(w, 404, "no such live view")
		return
	}
	s.Bus.Publish("board", "live", map[string]any{"closed": []int64{id}})
	w.WriteHeader(204)
}
