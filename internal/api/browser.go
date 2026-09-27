package api

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/JeremiahM37/lectern/v2/internal/auth"
	"github.com/JeremiahM37/lectern/v2/internal/browser"
	"github.com/JeremiahM37/lectern/v2/internal/executor"
	"github.com/JeremiahM37/lectern/v2/internal/sessions"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// The Browser pane, Design Mode and the agent browser tools (docs/browser.md).
//
// Each session can have one shared browser: a real headless Chromium on the
// session's own machine, which the agent drives through MCP or `lectern
// browser` and the operator watches, and can take over, in the pane. Its
// frames and input travel over an ordinary API WebSocket, so the pane works
// wherever the rest of Lectern does, the relay and the Android app included.
//
// A session can also frame a dev server directly through a view (see
// internal/browser/proxy.go), which is where Design Mode injects its picker.

// Control says who may drive a session's browser: the agent, or nobody but the
// operator (who took over, or pressed Stop).
const (
	controlAgent   = "agent"
	controlUser    = "user"
	controlStopped = "stopped"
)

type sessionBrowser struct {
	tabs      *browser.Tabs
	proc      *browser.Process
	run       browser.Runner // where the browser runs
	where     string         // target | host
	binary    string
	profile   string // the full profile name; "" is a throwaway one
	stopOnce  sync.Once
	stopFn    func()
	keepAlive func()
	sessionID int64

	mu         sync.Mutex
	control    string
	lastAgent  time.Time
	lastAction string
	lastUsed   time.Time
	watchers   int
}

func (sb *sessionBrowser) stop() { sb.stopOnce.Do(sb.stopFn) }

func (sb *sessionBrowser) touch() {
	sb.mu.Lock()
	sb.lastUsed = time.Now()
	sb.mu.Unlock()
}

type browserView struct {
	view      *browser.View
	sessionID int64
}

// deskControl is computer use's state for one live desktop.
type deskControl struct {
	allowed    bool // the owner allowed agent control of this desktop in the pane
	stopped    bool
	lastAgent  time.Time
	lastAction string
}

type browserState struct {
	once     sync.Once
	owner    string
	mu       sync.Mutex
	sessions map[int64]*sessionBrowser
	starting map[int64]*sync.Mutex
	views    map[string]*browserView
	desks    map[int64]*deskControl // by live forward id
	stop     chan struct{}
	stopOnce sync.Once
}

// browserIdle stops a shared browser nobody has watched or driven for this long.
const browserIdle = 30 * time.Minute

func (s *Server) browsersInit() *browserState {
	st := &s.browsers
	st.once.Do(func() {
		st.owner = randomHex(8)
		st.sessions = map[int64]*sessionBrowser{}
		st.starting = map[int64]*sync.Mutex{}
		st.views = map[string]*browserView{}
		st.desks = map[int64]*deskControl{}
		st.stop = make(chan struct{})
		go func() {
			tick := time.NewTicker(30 * time.Second)
			defer tick.Stop()
			for {
				select {
				case <-st.stop:
					return
				case <-tick.C:
					s.reapBrowsers()
				}
			}
		}()
	})
	return st
}

func randomHex(n int) string {
	raw := make([]byte, n)
	if _, err := rand.Read(raw); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	return fmt.Sprintf("%x", raw)
}

func (s *Server) sessionGone(id int64) bool {
	sess, err := s.DB.Session(id)
	return err != nil || sess.EndedAt != nil || sess.Status == sessions.StatusDead
}

// reapBrowsers stops what belongs to ended sessions, and browsers left idle.
func (s *Server) reapBrowsers() {
	st := &s.browsers
	var stop []*sessionBrowser
	var closeViews []*browser.View
	st.mu.Lock()
	for id, sb := range st.sessions {
		sb.mu.Lock()
		idle := sb.watchers == 0 && time.Since(sb.lastUsed) > browserIdle
		sb.mu.Unlock()
		if idle || s.sessionGone(id) {
			stop = append(stop, sb)
			delete(st.sessions, id)
		}
	}
	for key, v := range st.views {
		if s.sessionGone(v.sessionID) || time.Since(time.Unix(int64(v.view.Created), 0)) > 24*time.Hour {
			closeViews = append(closeViews, v.view)
			delete(st.views, key)
		}
	}
	alive := map[*sessionBrowser]bool{}
	for _, sb := range st.sessions {
		alive[sb] = true
	}
	st.mu.Unlock()
	for _, sb := range stop {
		sb.stop()
	}
	for _, v := range closeViews {
		v.Close()
	}
	for sb := range alive {
		if sb.keepAlive != nil {
			go sb.keepAlive()
		}
	}
}

// browserShutdown stops every browser and view this server started.
func (s *Server) browserShutdown() {
	st := &s.browsers
	if st.sessions == nil {
		return
	}
	st.stopOnce.Do(func() { close(st.stop) })
	st.mu.Lock()
	all := st.sessions
	views := st.views
	st.sessions, st.views = map[int64]*sessionBrowser{}, map[string]*browserView{}
	st.mu.Unlock()
	for _, sb := range all {
		sb.stop()
	}
	for _, v := range views {
		v.view.Close()
	}
}

// ---- starting a session's browser ---------------------------------------------

func runnerFor(ex executor.Executor, timeout float64) browser.Runner {
	return func(ctx context.Context, script string) (string, error) {
		r, err := ex.Run(ctx, script, executor.RunOpts{Timeout: timeout})
		if err != nil {
			return "", err
		}
		if !r.OK() {
			return r.Stdout, fmt.Errorf("browser script failed: %s", strings.TrimSpace(r.Stderr))
		}
		return r.Stdout, nil
	}
}

func (s *Server) sessionTarget(sess *store.Session) (*store.Target, executor.Executor, executor.Dialer, error) {
	target, err := s.DB.Target(sess.TargetID)
	if err != nil {
		return nil, nil, nil, err
	}
	ex, err := s.Reg.For(target)
	if err != nil {
		return nil, nil, nil, err
	}
	dialer, ok := ex.(executor.Dialer)
	if !ok {
		return target, ex, nil, executor.ErrNoDial
	}
	return target, ex, dialer, nil
}

// browserFor returns the session's running browser, or nil.
func (s *Server) browserFor(id int64) *sessionBrowser {
	st := s.browsersInit()
	st.mu.Lock()
	defer st.mu.Unlock()
	sb := st.sessions[id]
	if sb != nil {
		select {
		case <-sb.tabs.Done():
			delete(st.sessions, id)
			go sb.stop()
			return nil
		default:
		}
	}
	return sb
}

// ensureBrowser starts the session's shared browser on its own machine, or,
// when that machine has none, on this one with the machine's localhost
// reachable through a loopback proxy.
func (s *Server) ensureBrowser(ctx context.Context, sess *store.Session, vp browser.Viewport, label string) (*sessionBrowser, error) {
	profile, err := profileFor(sess, label)
	if err != nil {
		return nil, err
	}
	if sb := s.browserFor(sess.ID); sb != nil {
		// Asking for another profile starts the browser again in it.
		if label == "" || sb.profile == profile {
			return sb, nil
		}
		s.closeSessionBrowser(sess.ID)
	}
	st := s.browsersInit()
	st.mu.Lock()
	gate := st.starting[sess.ID]
	if gate == nil {
		gate = &sync.Mutex{}
		st.starting[sess.ID] = gate
	}
	st.mu.Unlock()
	gate.Lock()
	defer gate.Unlock()
	if sb := s.browserFor(sess.ID); sb != nil && (label == "" || sb.profile == profile) {
		return sb, nil
	}
	if sess.EndedAt != nil || sess.Status == sessions.StatusDead {
		return nil, invalid("this session has ended")
	}
	target, ex, dialer, err := s.sessionTarget(sess)
	if err != nil {
		if errors.Is(err, executor.ErrNoDial) {
			return nil, fmt.Errorf("%w: a browser needs a local or SSH machine whose localhost Lectern can reach", err)
		}
		return nil, err
	}
	startCtx, cancel := context.WithTimeout(ctx, 150*time.Second)
	defer cancel()
	sb := &sessionBrowser{sessionID: sess.ID, control: controlAgent, lastUsed: time.Now(), profile: profile}
	run := runnerFor(ex, 120)
	proc, err := browser.Launch(startCtx, run, st.owner, browser.LaunchOptions{Width: vp.Width, Height: vp.Height, Profile: profile})
	var proxy *browser.LoopbackProxy
	cdpDial := browser.Dial(dialer.DialTarget)
	sb.where = "target"
	if errors.Is(err, browser.ErrNoBrowser) && target.Kind != "local" {
		// The control plane stands in; the target's localhost stays the
		// browser's localhost through the proxy.
		proxy, err = browser.StartLoopbackProxy(dialer.DialTarget)
		if err != nil {
			return nil, err
		}
		run = runnerFor(executor.NewLocal(), 120)
		proc, err = browser.Launch(startCtx, run, st.owner, browser.LaunchOptions{Width: vp.Width, Height: vp.Height,
			ProxyPort: proxy.Port, Profile: profile})
		cdpDial = func(ctx context.Context, addr string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "tcp", addr)
		}
		sb.where = "host"
	}
	if err != nil {
		if proxy != nil {
			proxy.Close()
		}
		return nil, err
	}
	// Downloads land in the workspace, beside the agent's work. A browser on
	// the control plane saves them there first and copies them over.
	dlDir := downloadsDir(sess.Workdir)
	if sb.where == "host" {
		dlDir = proc.Dir + "/downloads"
	}
	if dlDir != "" {
		if _, err := run(startCtx, "mkdir -p "+shellQuoteArg(dlDir)+" && chmod 700 "+shellQuoteArg(dlDir)); err != nil {
			dlDir = ""
		}
	}
	tabs, err := browser.OpenTabs(startCtx, cdpDial, proc, vp, dlDir)
	if err != nil {
		_ = browser.Stop(context.Background(), run, proc.Dir)
		if proxy != nil {
			proxy.Close()
		}
		return nil, err
	}
	tabs.OnDownload = s.downloadFinisher(sess, sb, run, ex)
	b := tabs
	sb.tabs, sb.proc, sb.run, sb.binary = tabs, proc, run, proc.Binary
	sb.keepAlive = func() {
		c, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_ = browser.KeepAlive(c, run, proc.Dir)
	}
	sb.stopFn = func() {
		c, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		quit, stop := context.WithTimeout(c, 10*time.Second)
		b.Quit(quit)
		stop()
		if err := browser.Stop(c, run, proc.Dir); err != nil {
			s.Log.Warn("browser: did not stop cleanly", "session", sess.ID, "err", err)
		}
		if proxy != nil {
			proxy.Close()
		}
	}
	st.mu.Lock()
	st.sessions[sess.ID] = sb
	st.mu.Unlock()
	s.Log.Info("browser: started", "session", sess.ID, "target", target.Name, "where", sb.where, "binary", proc.Binary)
	s.Bus.Publish("board", "browser", map[string]any{"session_id": sess.ID, "running": true})
	return sb, nil
}

func (s *Server) closeSessionBrowser(id int64) bool {
	st := s.browsersInit()
	st.mu.Lock()
	sb := st.sessions[id]
	delete(st.sessions, id)
	st.mu.Unlock()
	if sb == nil {
		return false
	}
	sb.stop()
	s.Bus.Publish("board", "browser", map[string]any{"session_id": id, "running": false})
	return true
}

// ---- status -------------------------------------------------------------------

type browserStatus struct {
	Running     bool               `json:"running"`
	Where       string             `json:"where,omitempty"`
	Binary      string             `json:"binary,omitempty"`
	State       *browser.State     `json:"state,omitempty"`
	Control     string             `json:"control"`
	AgentActive bool               `json:"agent_active"`
	LastAction  string             `json:"last_action,omitempty"`
	Watchers    int                `json:"watchers"`
	Views       []map[string]any   `json:"views"`
	ViewsOK     bool               `json:"views_enabled"`
	ViewsWhy    string             `json:"views_disabled_reason,omitempty"`
	Tabs        []browser.TabInfo  `json:"tabs"`
	Active      int                `json:"active_tab,omitempty"`
	Downloads   []browser.Download `json:"downloads"`
	// Profile is the profile's label as the pane shows it; "" is a
	// throwaway one.
	Profile string `json:"profile"`
}

// agentActiveFor is how long after an agent action the pane keeps saying the
// agent is in control.
const agentActiveFor = 6 * time.Second

func (s *Server) statusOf(id int64) browserStatus {
	out := browserStatus{Control: controlAgent, Views: []map[string]any{}}
	out.ViewsOK, out.ViewsWhy = s.viewsAllowed()
	out.Tabs, out.Downloads = []browser.TabInfo{}, []browser.Download{}
	if sb := s.browserFor(id); sb != nil {
		var st browser.State
		if b, active, err := sb.tabs.Get(0); err == nil {
			st, out.Active = b.State(), active
		}
		out.Tabs, out.Downloads, out.Profile = sb.tabs.List(), sb.tabs.Downloads(), profileLabel(sb.profile)
		sb.mu.Lock()
		out.Running, out.Where, out.Binary, out.State = true, sb.where, sb.binary, &st
		out.Control, out.LastAction, out.Watchers = sb.control, sb.lastAction, sb.watchers
		out.AgentActive = time.Since(sb.lastAgent) < agentActiveFor
		sb.mu.Unlock()
	}
	st := s.browsersInit()
	st.mu.Lock()
	for _, v := range st.views {
		if v.sessionID == id {
			out.Views = append(out.Views, viewJSON(v.view))
		}
	}
	st.mu.Unlock()
	return out
}

func viewJSON(v *browser.View) map[string]any {
	return map[string]any{"id": v.ID, "port": v.Port, "listen_port": v.ListenPort, "design": v.Design(), "tls": v.TLS}
}

func (s *Server) getSessionBrowser(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.sessionParam(w, r)
	if !ok {
		return
	}
	writeJSON(w, 200, s.statusOf(sess.ID))
}

// requireHuman is the pane's gate: the operator's own actions, which an agent
// on the same machine must not be able to take in their name. In no-auth mode
// everyone is the operator.
func (s *Server) requireHuman(w http.ResponseWriter, r *http.Request, what string) bool {
	principal, _ := auth.FromContext(r.Context())
	if s.Auth == nil || !s.Auth.CanDecide(principal) {
		httpError(w, 403, "%s needs a signed-in person (tailscale identity or access token)", what)
		return false
	}
	return true
}

// ---- actions ------------------------------------------------------------------

// browserArgs is every action's input; each action reads what it needs.
type browserArgs struct {
	Action     string  `json:"action"`
	URL        string  `json:"url"`
	Ref        int     `json:"ref"`
	Selector   string  `json:"selector"`
	Text       string  `json:"text"`
	Key        string  `json:"key"`
	Expression string  `json:"expression"`
	Width      int     `json:"width"`
	Height     int     `json:"height"`
	Mobile     bool    `json:"mobile"`
	Scale      float64 `json:"scale"`
	X          float64 `json:"x"`
	Y          float64 `json:"y"`
	Screenshot *bool   `json:"screenshot"`
	Limit      int     `json:"limit"`
	On         *bool   `json:"on"`
	Mode       string  `json:"mode"`
	// Tab is the tab to act on; 0 is the active one.
	Tab       int    `json:"tab"`
	Backwards bool   `json:"backwards"`
	Profile   string `json:"profile"`
	// Session resolution for an agent: an explicit id, or what its
	// environment says (LECTERN_SESSION_ID, its tmux session).
	SessionID   int64  `json:"session_id"`
	HintSession int64  `json:"hint_session_id"`
	TmuxSession string `json:"tmux_session"`
}

func (a browserArgs) viewport() browser.Viewport {
	return browser.Viewport{Width: a.Width, Height: a.Height, Mobile: a.Mobile, Scale: a.Scale}
}

var agentActions = map[string]bool{"tabs": true, "tab_new": true, "tab_select": true, "tab_close": true,
	"find": true, "downloads": true, "open": true, "navigate": true, "back": true, "forward": true, "reload": true,
	"snapshot": true, "click": true, "fill": true, "press": true, "evaluate": true, "console": true, "network": true,
	"screenshot": true, "resize": true, "status": true, "close": true}

// act runs one action on a session's browser, starting it first when the
// action needs a page.
func (s *Server) act(ctx context.Context, sess *store.Session, a browserArgs, agent bool) (map[string]any, error) {
	if a.Action == "status" {
		st := s.statusOf(sess.ID)
		return map[string]any{"status": st}, nil
	}
	if a.Action == "close" {
		return map[string]any{"closed": s.closeSessionBrowser(sess.ID)}, nil
	}
	sb := s.browserFor(sess.ID)
	if sb == nil {
		var err error
		if sb, err = s.ensureBrowser(ctx, sess, a.viewport(), a.Profile); err != nil {
			return nil, err
		}
	} else if !agent && a.Profile != "" && a.Action == "open" {
		var err error
		if sb, err = s.ensureBrowser(ctx, sess, a.viewport(), a.Profile); err != nil {
			return nil, err
		}
	}
	if agent {
		sb.mu.Lock()
		control := sb.control
		sb.mu.Unlock()
		switch control {
		case controlUser:
			return nil, &conflict{"the operator has taken over this browser; wait until they hand it back"}
		case controlStopped:
			return nil, &conflict{"the operator stopped agent control of this browser"}
		}
		sb.mu.Lock()
		sb.lastAgent, sb.lastAction = time.Now(), describeAction(a)
		sb.mu.Unlock()
	}
	sb.touch()
	out := map[string]any{}
	switch a.Action {
	case "tabs":
		out["tabs"] = sb.tabs.List()
		return out, nil
	case "tab_new":
		id, _, err := sb.tabs.New(ctx, a.URL)
		out["tab"], out["tabs"] = id, sb.tabs.List()
		return out, err
	case "tab_select":
		err := sb.tabs.Select(a.Tab)
		out["tabs"] = sb.tabs.List()
		return out, err
	case "tab_close":
		err := sb.tabs.CloseTab(ctx, a.Tab)
		out["tabs"] = sb.tabs.List()
		return out, err
	case "downloads":
		out["downloads"] = sb.tabs.Downloads()
		return out, nil
	case "resize":
		if err := sb.tabs.Resize(ctx, a.viewport()); err != nil {
			return nil, err
		}
	}
	b, tab, err := sb.tabs.Get(a.Tab)
	if err != nil {
		return nil, invalid("%s", err)
	}
	out["tab"] = tab
	var state browser.State
	switch a.Action {
	case "find":
		var r browser.FindResult
		r, err = b.Find(ctx, a.Text, a.Backwards)
		out["find"] = r
		state = b.State()
	case "open":
		state = b.State()
		if a.URL != "" {
			state, err = b.Navigate(ctx, a.URL)
		}
	case "navigate":
		state, err = b.Navigate(ctx, a.URL)
	case "back":
		state, err = b.History(ctx, -1)
	case "forward":
		state, err = b.History(ctx, 1)
	case "reload":
		state, err = b.Reload(ctx)
	case "resize":
		state = b.State()
	case "click":
		if a.Ref == 0 && a.Selector == "" && a.X == 0 && a.Y == 0 {
			return nil, invalid("name what to click: a ref from a snapshot, a selector, or x and y")
		}
		if a.Ref == 0 && a.Selector == "" {
			err = b.ClickAt(ctx, a.X, a.Y)
			state = b.State()
		} else {
			state, err = b.Click(ctx, browser.Target{Ref: a.Ref, Selector: a.Selector})
		}
		settle(ctx)
		state = b.State()
	case "fill":
		state, err = b.Fill(ctx, browser.Target{Ref: a.Ref, Selector: a.Selector}, a.Text)
	case "press":
		err = b.Press(ctx, a.Key)
		settle(ctx)
		state = b.State()
	case "snapshot":
		snap, e := b.Snapshot(ctx)
		if err = e; err == nil {
			out["tree"], out["refs"] = snap.Tree, snap.Refs
			state = b.State()
			if a.Screenshot == nil || *a.Screenshot {
				if png, e := b.Screenshot(ctx, nil); e == nil {
					out["screenshot"] = base64.StdEncoding.EncodeToString(png)
				}
			}
		}
	case "screenshot":
		var clip *browser.Clip
		if a.Selector != "" || a.Ref > 0 {
			sel := a.Selector
			if sel == "" {
				return nil, invalid("screenshot of one element takes a selector")
			}
			raw, e := b.DescribeSelector(ctx, sel)
			if e != nil {
				return nil, e
			}
			clip, err = clipOf(raw)
		}
		if err == nil {
			png, e := b.Screenshot(ctx, clip)
			if err = e; err == nil {
				out["screenshot"] = base64.StdEncoding.EncodeToString(png)
			}
		}
		state = b.State()
	case "evaluate":
		var v any
		v, err = b.Evaluate(ctx, a.Expression)
		out["value"] = v
		state = b.State()
	case "console":
		logs := b.Console()
		out["entries"] = lastN(logs, a.Limit)
		state = b.State()
	case "network":
		reqs := b.Network()
		out["entries"] = lastN(reqs, a.Limit)
		state = b.State()
	case "design":
		if agent {
			return nil, invalid("Design Mode is the operator's")
		}
		err = b.SetDesign(ctx, a.On != nil && *a.On)
		state = b.State()
	default:
		return nil, invalid("unknown browser action %q", a.Action)
	}
	if err != nil {
		return nil, err
	}
	out["url"], out["title"], out["viewport"] = state.URL, state.Title, state.Viewport
	return out, nil
}

// settle gives a click or key press a moment to start whatever it starts.
func settle(ctx context.Context) {
	t := time.NewTimer(300 * time.Millisecond)
	defer t.Stop()
	select {
	case <-t.C:
	case <-ctx.Done():
	}
}

func lastN[T any](xs []T, limit int) []T {
	if limit <= 0 || limit > 300 {
		limit = 100
	}
	if len(xs) > limit {
		return xs[len(xs)-limit:]
	}
	return xs
}

func describeAction(a browserArgs) string {
	switch a.Action {
	case "navigate", "open":
		return a.Action + " " + clip(a.URL, 80)
	case "click", "fill":
		if a.Ref > 0 {
			return fmt.Sprintf("%s ref %d", a.Action, a.Ref)
		}
		if a.Selector != "" {
			return a.Action + " " + clip(a.Selector, 60)
		}
		return fmt.Sprintf("%s at %.0f,%.0f", a.Action, a.X, a.Y)
	case "press":
		return "press " + a.Key
	}
	return a.Action
}

type conflict struct{ msg string }

func (c *conflict) Error() string { return c.msg }

func actError(w http.ResponseWriter, err error) {
	var c *conflict
	switch {
	case errors.As(err, &c):
		httpError(w, 409, "%s", c.msg)
	case errors.Is(err, browser.ErrNoBrowser), errors.Is(err, executor.ErrNoDial):
		httpError(w, 409, "%s", err)
	case errors.Is(err, browser.ErrNotFound):
		httpError(w, 404, "%s", err)
	case errors.Is(err, browser.ErrClosed):
		httpError(w, 410, "the browser has closed; the next action starts a new one")
	default:
		var ve *validationError
		if errors.As(err, &ve) {
			httpError(w, 422, "%s", ve.msg)
			return
		}
		// Most failures are the page's: a refused connection, a missing
		// element, an expression that threw. Say so plainly.
		httpError(w, 422, "%s", err)
	}
}

func clipOf(raw json.RawMessage) (*browser.Clip, error) {
	var d struct {
		Rect   browser.Clip `json:"rect"`
		Scroll struct {
			X float64 `json:"x"`
			Y float64 `json:"y"`
		} `json:"scroll"`
	}
	if err := json.Unmarshal(raw, &d); err != nil {
		return nil, err
	}
	return &browser.Clip{X: d.Rect.X + d.Scroll.X, Y: d.Rect.Y + d.Scroll.Y, Width: d.Rect.Width, Height: d.Rect.Height}, nil
}

// postSessionBrowser is the pane's own control: start, navigate, resize,
// Design Mode and who is in control.
func (s *Server) postSessionBrowser(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.sessionParam(w, r)
	if !ok {
		return
	}
	var a browserArgs
	if err := decodeBody(r, &a); err != nil {
		httpError(w, 422, "%s", err)
		return
	}
	if !s.requireHuman(w, r, "driving the session browser") {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 150*time.Second)
	defer cancel()
	if a.Action == "control" {
		sb := s.browserFor(sess.ID)
		if sb == nil {
			httpError(w, 409, "no browser is running for this session")
			return
		}
		if a.Mode != controlAgent && a.Mode != controlUser && a.Mode != controlStopped {
			httpError(w, 422, "mode must be agent, user or stopped")
			return
		}
		sb.mu.Lock()
		sb.control = a.Mode
		if a.Mode != controlAgent {
			sb.lastAgent = time.Time{}
		}
		sb.mu.Unlock()
		s.Log.Info("browser: control changed", "session", sess.ID, "control", a.Mode)
		writeJSON(w, 200, s.statusOf(sess.ID))
		return
	}
	if a.Action == "" {
		a.Action = "open"
	}
	out, err := s.act(ctx, sess, a, false)
	if err != nil {
		actError(w, err)
		return
	}
	out["status"] = s.statusOf(sess.ID)
	writeJSON(w, 200, out)
}

func (s *Server) deleteSessionBrowser(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.sessionParam(w, r)
	if !ok {
		return
	}
	s.closeSessionBrowser(sess.ID)
	w.WriteHeader(204)
}

// agentBrowser is the agent's door: the MCP browser tools and `lectern
// browser` post here, naming their session by id or by their environment.
func (s *Server) agentBrowser(w http.ResponseWriter, r *http.Request) {
	var a browserArgs
	if err := decodeBody(r, &a); err != nil {
		httpError(w, 422, "%s", err)
		return
	}
	if !agentActions[a.Action] {
		httpError(w, 422, "unknown browser action %q", a.Action)
		return
	}
	id, err := s.mediaSession(a.SessionID, a.TmuxSession, a.HintSession)
	if err != nil {
		httpError(w, 422, "%s", err)
		return
	}
	if id == nil {
		httpError(w, 422, "no Lectern session: run this inside a Lectern session, or pass session_id")
		return
	}
	sess, err := s.DB.Session(*id)
	if err != nil {
		httpError(w, 404, "no such session")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 150*time.Second)
	defer cancel()
	out, err := s.act(ctx, sess, a, true)
	if err != nil {
		actError(w, err)
		return
	}
	out["session_id"] = sess.ID
	writeJSON(w, 200, out)
}

// ---- the pane's live stream -----------------------------------------------------

// browserStream carries screencast frames (binary JPEG messages) and state
// (JSON text messages) to the pane, and the operator's input back.
func (s *Server) browserStream(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.sessionParam(w, r)
	if !ok {
		return
	}
	sb := s.browserFor(sess.ID)
	if sb == nil {
		httpError(w, 409, "no browser is running for this session")
		return
	}
	principal, _ := auth.FromContext(r.Context())
	canDrive := s.Auth != nil && s.Auth.CanDecide(principal)
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{CompressionMode: websocket.CompressionDisabled})
	if err != nil {
		return
	}
	defer c.CloseNow()
	c.SetReadLimit(64 << 10)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	// The pane watches the active tab, and follows it when another becomes
	// active.
	var watchMu sync.Mutex
	var frames <-chan browser.Frame
	var updates <-chan browser.Update
	unwatch := func() {}
	watching := 0
	watchActive := func() {
		b, id, err := sb.tabs.Get(0)
		if err != nil || id == watching {
			return
		}
		watchMu.Lock()
		unwatch()
		frames, updates, unwatch = b.Watch(ctx)
		watching = id
		watchMu.Unlock()
	}
	watchActive()
	defer func() { watchMu.Lock(); unwatch(); watchMu.Unlock() }()
	changes, stopChanges := sb.tabs.Changes()
	defer stopChanges()
	sb.mu.Lock()
	sb.watchers++
	sb.mu.Unlock()
	defer func() {
		sb.mu.Lock()
		sb.watchers--
		sb.lastUsed = time.Now()
		sb.mu.Unlock()
	}()
	var writeMu sync.Mutex
	send := func(typ websocket.MessageType, data []byte) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		wctx, wcancel := context.WithTimeout(ctx, 10*time.Second)
		defer wcancel()
		return c.Write(wctx, typ, data)
	}
	sendState := func() error {
		raw, _ := json.Marshal(map[string]any{"type": "state", "status": s.statusOf(sess.ID), "can_drive": canDrive})
		return send(websocket.MessageText, raw)
	}
	// The operator's input.
	go func() {
		defer cancel()
		for {
			_, data, err := c.Read(ctx)
			if err != nil {
				return
			}
			var msg struct {
				Type  string        `json:"type"`
				Event browser.Input `json:"event"`
				Mode  string        `json:"mode"`
			}
			if json.Unmarshal(data, &msg) != nil || !canDrive {
				continue
			}
			// Control changes travel on this socket too, so they apply in the
			// order the operator made them: a hand-back right after typing
			// must not be undone by keystrokes that were still in flight.
			if msg.Type == "control" {
				if msg.Mode == controlAgent || msg.Mode == controlUser || msg.Mode == controlStopped {
					sb.mu.Lock()
					sb.control = msg.Mode
					if msg.Mode != controlAgent {
						sb.lastAgent = time.Time{}
					}
					sb.mu.Unlock()
					_ = sendState()
				}
				continue
			}
			if msg.Type != "input" {
				continue
			}
			// Pressing, typing or scrolling takes over from the agent; merely
			// moving the pointer (which Design Mode hover needs) does not.
			if msg.Event.Type != "mousemove" && msg.Event.Type != "mouseup" && msg.Event.Type != "keyup" {
				sb.mu.Lock()
				took := sb.control == controlAgent
				if took {
					sb.control, sb.lastAgent = controlUser, time.Time{}
				}
				sb.mu.Unlock()
				if took {
					_ = sendState()
				}
			}
			sb.touch()
			if b, _, err := sb.tabs.Get(0); err == nil {
				ictx, icancel := context.WithTimeout(ctx, 10*time.Second)
				_ = b.Dispatch(ictx, msg.Event)
				icancel()
			}
		}
	}()
	if sendState() != nil {
		return
	}
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	last := ""
	pageScale := 1.0
	for {
		select {
		case <-ctx.Done():
			return
		case <-sb.tabs.Done():
			_ = c.Close(websocket.StatusNormalClosure, "the browser has closed")
			return
		case <-changes:
			watchActive()
			if sendState() != nil {
				return
			}
		case f := <-frames:
			if f.PageScale != pageScale {
				pageScale = f.PageScale
				raw, _ := json.Marshal(map[string]any{"type": "frame", "page_scale": pageScale})
				if send(websocket.MessageText, raw) != nil {
					return
				}
			}
			if send(websocket.MessageBinary, f.JPEG) != nil {
				return
			}
		case u := <-updates:
			if u.Kind == "design" {
				raw, _ := json.Marshal(map[string]any{"type": "design", "design": u.Design})
				if send(websocket.MessageText, raw) != nil {
					return
				}
				continue
			}
			if sendState() != nil {
				return
			}
		case <-tick.C:
			// Control and agent activity change without a page event.
			st := s.statusOf(sess.ID)
			key := fmt.Sprintf("%s|%v|%s", st.Control, st.AgentActive, st.LastAction)
			if key != last {
				last = key
				if sendState() != nil {
					return
				}
			}
		}
	}
}

// ---- dev server views -----------------------------------------------------------

func (s *Server) viewsAllowed() (bool, string) {
	if !s.Cfg.Live {
		return false, "Direct views open a listening port, so they are off unless LECTERN_LIVE=1 is set. The shared browser works without it."
	}
	return true, ""
}

func (s *Server) openBrowserView(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.sessionParam(w, r)
	if !ok {
		return
	}
	var in struct {
		Port         int    `json:"port"`
		Design       bool   `json:"design"`
		ParentOrigin string `json:"parent_origin"`
	}
	if err := decodeBody(r, &in); err != nil {
		httpError(w, 422, "%s", err)
		return
	}
	if !s.requireHuman(w, r, "opening a dev server view") {
		return
	}
	if ok, why := s.viewsAllowed(); !ok {
		httpError(w, 409, "%s", why)
		return
	}
	if !browser.ValidOrigin(in.ParentOrigin) {
		httpError(w, 422, "parent_origin must be the origin of the Lectern page")
		return
	}
	if s.sessionGone(sess.ID) {
		httpError(w, 409, "this session has ended")
		return
	}
	_, _, dialer, err := s.sessionTarget(sess)
	if err != nil {
		actError(w, err)
		return
	}
	st := s.browsersInit()
	st.mu.Lock()
	var found *browser.View
	for _, v := range st.views {
		if v.sessionID == sess.ID && v.view.Port == in.Port && v.view.ParentOrigin == in.ParentOrigin {
			found = v.view
		}
	}
	st.mu.Unlock()
	if found == nil {
		lo, hi := livePortRange()
		found, err = browser.OpenView(browser.Dial(dialer.DialTarget), in.Port, s.Cfg.Host, lo, hi, s.TLS, in.ParentOrigin)
		if err != nil {
			httpError(w, 409, "%s", err)
			return
		}
		st.mu.Lock()
		st.views[found.ID] = &browserView{view: found, sessionID: sess.ID}
		st.mu.Unlock()
		s.Log.Info("browser: view opened", "session", sess.ID, "port", in.Port, "listen", found.ListenPort)
	}
	found.SetDesign(in.Design)
	out := viewJSON(found)
	out["ticket"] = found.Ticket()
	out["ticket_param"] = browser.TicketParam
	writeJSON(w, 201, out)
}

func livePortRange() (int, int) {
	lo, hi := 19200, 19299
	if a, b, ok := strings.Cut(os.Getenv("LECTERN_LIVE_PORTS"), "-"); ok {
		if x, err1 := strconv.Atoi(a); err1 == nil {
			if y, err2 := strconv.Atoi(b); err2 == nil && x > 1023 && y >= x && y < 65536 {
				lo, hi = x, y
			}
		}
	}
	return lo, hi
}

func (s *Server) viewParam(w http.ResponseWriter, r *http.Request) (*browserView, bool) {
	st := s.browsersInit()
	st.mu.Lock()
	v := st.views[r.PathValue("view")]
	st.mu.Unlock()
	if v == nil {
		httpError(w, 404, "no such view")
		return nil, false
	}
	return v, true
}

func (s *Server) closeBrowserView(w http.ResponseWriter, r *http.Request) {
	v, ok := s.viewParam(w, r)
	if !ok {
		return
	}
	st := s.browsersInit()
	st.mu.Lock()
	delete(st.views, v.view.ID)
	st.mu.Unlock()
	v.view.Close()
	w.WriteHeader(204)
}

// ---- ports ----------------------------------------------------------------------

// portsScript lists listening TCP ports on the target's loopback or wildcard
// addresses, marks the ones a process in the session's workspace owns, and
// says which answer HTTP; those come first.
const portsScript = `import os,json,socket,sys
work=os.path.realpath(sys.argv[1]) if len(sys.argv)>1 and sys.argv[1] else ''
listen={}
for fn,v6 in (('/proc/net/tcp',False),('/proc/net/tcp6',True)):
    try: lines=open(fn).read().splitlines()[1:]
    except OSError: continue
    for l in lines:
        f=l.split()
        if len(f)<10 or f[3]!='0A': continue
        addr,port=f[1].split(':'); port=int(port,16)
        ok=addr in ('00000000000000000000000000000000','00000000000000000000000001000000','0000000000000000FFFF00000100007F') if v6 else (addr=='00000000' or addr.endswith('7F'))
        if ok: listen.setdefault(int(f[9]),port)
owner={}
for pid in os.listdir('/proc'):
    if not pid.isdigit(): continue
    try: fds=os.listdir('/proc/%s/fd'%pid)
    except OSError: continue
    for fd in fds:
        try: link=os.readlink('/proc/%s/fd/%s'%(pid,fd))
        except OSError: continue
        if link.startswith('socket:['):
            ino=int(link[8:-1])
            if ino in listen and ino not in owner: owner[ino]=pid
out={}
for ino,port in listen.items():
    pid=owner.get(ino); cwd=cmd=''
    if pid:
        try: cwd=os.readlink('/proc/%s/cwd'%pid)
        except OSError: pass
        try: cmd=open('/proc/%s/cmdline'%pid,'rb').read().replace(b'\0',b' ').decode('utf-8','replace').strip()[:160]
        except OSError: pass
    inws=bool(work and cwd and (cwd==work or cwd.startswith(work+'/')))
    if port in out and out[port]['in_workspace']: continue
    out[port]=dict(port=port,pid=int(pid) if pid else None,command=cmd,in_workspace=inws)
def probe(e):
    try:
        s=socket.create_connection(('127.0.0.1',e['port']),timeout=0.3); s.settimeout(0.6)
        s.sendall(b'HEAD / HTTP/1.0\r\nHost: localhost\r\n\r\n'); e['http']=s.recv(5)==b'HTTP/'; s.close()
    except Exception: e['http']=False
ports=list(out.values())[:300]
from concurrent.futures import ThreadPoolExecutor
with ThreadPoolExecutor(32) as pool: list(pool.map(probe,ports))
ports=sorted(ports,key=lambda e:(not e['in_workspace'],not e['http'],e['port']))[:80]
print(json.dumps(dict(ports=ports)))
`

func (s *Server) sessionPorts(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.sessionParam(w, r)
	if !ok {
		return
	}
	target, err := s.DB.Target(sess.TargetID)
	if err != nil {
		respondErr(w, err)
		return
	}
	ex, err := s.Reg.For(target)
	if err != nil {
		respondErr(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	res, err := ex.Run(ctx, "python3 -c "+shellQuoteArg(portsScript)+" "+shellQuoteArg(sess.Workdir), executor.RunOpts{Timeout: 20})
	if err != nil {
		respondErr(w, err)
		return
	}
	var out struct {
		Ports []map[string]any `json:"ports"`
	}
	if !res.OK() || json.Unmarshal([]byte(strings.TrimSpace(res.Stdout)), &out) != nil {
		httpError(w, 502, "could not list ports on %s: %s", target.Name, clip(strings.TrimSpace(res.Stderr), 300))
		return
	}
	if out.Ports == nil {
		out.Ports = []map[string]any{}
	}
	writeJSON(w, 200, map[string]any{"ports": out.Ports, "target": target.Name, "workdir": sess.Workdir})
}

func shellQuoteArg(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
