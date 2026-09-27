package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/accounts"
	"github.com/JeremiahM37/lectern/v2/internal/executor"
	"github.com/JeremiahM37/lectern/v2/internal/limits"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// Accounts (docs/accounts.md): several logins of one CLI on one target, which
// the swap limit policy moves limited work between. An account's directory
// is never sent anywhere — not to the web app, relay clients or the chat
// connector. Views carry the label, the limit Lectern last saw and the usage
// the CLI itself last reported.

type accountUsage struct {
	Rate5hPct   *int     `json:"rate_5h_pct,omitempty"`
	Rate5hReset *float64 `json:"rate_5h_reset,omitempty"`
	Rate7dPct   *int     `json:"rate_7d_pct,omitempty"`
	Rate7dReset *float64 `json:"rate_7d_reset,omitempty"`
	At          *float64 `json:"at,omitempty"`
}

type accountView struct {
	*store.Account
	TargetName string `json:"target_name"`
	Default    bool   `json:"default"`
	// SignedIn is whether the CLI's credential file exists in the account's
	// directory (nil when the target could not be checked).
	SignedIn *bool `json:"signed_in,omitempty"`
	// BlockedUntil is when the account is usable again, absent when it is
	// usable now.
	BlockedUntil *float64      `json:"blocked_until,omitempty"`
	Usage        *accountUsage `json:"usage,omitempty"`
	LiveSessions int           `json:"live_sessions"`
}

func (s *Server) accountView(a *store.Account, signedIn *bool) *accountView {
	v := &accountView{Account: a, Default: a.Default(), SignedIn: signedIn}
	if t, err := s.DB.Target(a.TargetID); err == nil {
		v.TargetName = t.Name
	}
	for _, c := range limits.Candidates(s.DB, a.TargetID, a.Agent) {
		if c.ID == a.ID {
			if until := c.BlockedUntil(time.Now()); !until.IsZero() {
				u := float64(until.Unix())
				v.BlockedUntil = &u
			}
		}
	}
	if sess, err := s.DB.LatestAccountUsage(a.TargetID, a.Agent, a.ID, a.Default()); err == nil && sess != nil {
		v.Usage = &accountUsage{Rate5hPct: sess.Rate5hPct, Rate5hReset: sess.Rate5hReset,
			Rate7dPct: sess.Rate7dPct, Rate7dReset: sess.Rate7dReset, At: sess.UsageAt}
	}
	where := "ended_at IS NULL AND target_id=? AND agent=? AND account_id=?"
	if a.Default() {
		where = "ended_at IS NULL AND target_id=? AND agent=? AND (account_id IS NULL OR account_id=?)"
	}
	v.LiveSessions, _ = s.DB.Count("sessions", where, a.TargetID, a.Agent, a.ID)
	return v
}

// signedIn checks every account's credential file, one command per target.
func (s *Server) signedIn(ctx context.Context, rows []*store.Account) map[int64]*bool {
	out := map[int64]*bool{}
	byTarget := map[int64][]*store.Account{}
	for _, a := range rows {
		byTarget[a.TargetID] = append(byTarget[a.TargetID], a)
	}
	for targetID, group := range byTarget {
		target, err := s.DB.Target(targetID)
		if err != nil {
			continue
		}
		ex, err := s.Reg.For(target)
		if err != nil {
			continue
		}
		parts := make([]string, 0, len(group))
		for _, a := range group {
			parts = append(parts, accounts.StatusCommand(a.Agent, a.Dir))
		}
		cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		r, err := ex.Run(cctx, strings.Join(parts, "; "), executor.RunOpts{Timeout: 10})
		cancel()
		if err != nil {
			continue
		}
		lines := strings.Fields(r.Stdout)
		if len(lines) != len(group) {
			continue
		}
		for i, a := range group {
			ok := lines[i] == "signed-in"
			out[a.ID] = &ok
		}
	}
	return out
}

func (s *Server) listAccounts(w http.ResponseWriter, r *http.Request) {
	rows, err := s.DB.Accounts()
	if err != nil {
		respondErr(w, err)
		return
	}
	status := s.signedIn(r.Context(), rows)
	out := make([]*accountView, 0, len(rows))
	for _, a := range rows {
		out = append(out, s.accountView(a, status[a.ID]))
	}
	writeJSON(w, 200, out)
}

type accountIn struct {
	TargetID int64  `json:"target_id"`
	Machine  string `json:"machine"`
	Agent    string `json:"agent"`
	Label    string `json:"label"`
	// Dir adopts an existing config directory on the target instead of
	// creating one under ~/.lectern/accounts. It is only ever written, never
	// returned.
	Dir string `json:"dir"`
}

// addAccount registers a login: it creates the account's private directory
// on the target (or adopts dir) and, the first time a CLI gets an account on
// a target, also registers the login the CLI already uses as "Default", so
// the rotation includes it.
func (s *Server) addAccount(w http.ResponseWriter, r *http.Request) {
	var in accountIn
	if err := decodeBody(r, &in); err != nil {
		httpError(w, 422, "%s", err.Error())
		return
	}
	in.Agent = strings.TrimSpace(in.Agent)
	in.Label = strings.TrimSpace(in.Label)
	in.Dir = strings.TrimSpace(in.Dir)
	if !accounts.Supported(in.Agent) {
		httpError(w, 422, "accounts are supported for %s", strings.Join(accounts.Agents(), ", "))
		return
	}
	if err := accounts.ValidLabel(in.Label); err != nil {
		httpError(w, 422, "%s", err.Error())
		return
	}
	if in.Dir != "" && !strings.HasPrefix(in.Dir, "/") {
		httpError(w, 422, "dir must be an absolute path on the target")
		return
	}
	var target *store.Target
	var err error
	switch {
	case in.TargetID != 0:
		target, err = s.DB.Target(in.TargetID)
	case strings.TrimSpace(in.Machine) != "":
		target, err = s.DB.TargetByName(strings.TrimSpace(in.Machine))
	default:
		target, err = s.DB.Target(s.defaultTargetID())
	}
	if err != nil {
		httpError(w, 422, "no such target")
		return
	}
	if target.Kind == "sandbox" {
		httpError(w, 422, "a sandbox target has no persistent logins")
		return
	}
	existing, _ := s.DB.AccountsFor(target.ID, in.Agent)
	for _, a := range existing {
		if strings.EqualFold(a.Label, in.Label) {
			httpError(w, 409, "%s already has an account called %q", in.Agent, a.Label)
			return
		}
	}
	ex, err := s.Reg.For(target)
	if err != nil {
		respondErr(w, err)
		return
	}
	res, err := ex.Run(r.Context(), accounts.CreateDirCommand(in.Agent, in.Label, in.Dir), executor.RunOpts{Timeout: 20})
	if err != nil {
		respondErr(w, err)
		return
	}
	dir := accounts.FirstLine(res.Stdout)
	if !res.OK() || !strings.HasPrefix(dir, "/") {
		httpError(w, 422, "could not create the account directory on %s", target.Name)
		return
	}
	for _, a := range existing {
		if a.Dir == dir {
			httpError(w, 409, "that directory is already account %q", a.Label)
			return
		}
	}
	if len(existing) == 0 {
		if _, err := s.DB.InsertAccount(&store.Account{TargetID: target.ID, Agent: in.Agent, Label: "Default"}); err != nil && !strings.Contains(err.Error(), "UNIQUE") {
			respondErr(w, err)
			return
		}
	}
	a, err := s.DB.InsertAccount(&store.Account{TargetID: target.ID, Agent: in.Agent, Label: in.Label, Dir: dir})
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			httpError(w, 409, "%s already has an account called %q", in.Agent, in.Label)
			return
		}
		respondErr(w, err)
		return
	}
	s.Log.Info("account added", "account", a.ID, "agent", a.Agent, "target", target.Name)
	writeJSON(w, 201, s.accountView(a, nil))
}

// deleteAccount forgets an account. Its directory, and the login in it, stay
// on the target: removing someone's credentials is theirs to do.
func (s *Server) deleteAccount(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		httpError(w, 404, "no such account")
		return
	}
	a, err := s.DB.Account(id)
	if err != nil {
		httpError(w, 404, "no such account")
		return
	}
	if !a.Default() {
		if n, _ := s.DB.Count("sessions", "ended_at IS NULL AND account_id=?", id); n > 0 {
			httpError(w, 409, "%d running session(s) use %q; stop or move them first", n, a.Label)
			return
		}
	}
	if err := s.DB.DeleteAccount(id); err != nil {
		respondErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// accountLogin opens a web terminal running the CLI's own sign-in with the
// account's directory in its environment.
func (s *Server) accountLogin(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		httpError(w, 404, "no such account")
		return
	}
	if _, err := s.DB.Account(id); err != nil {
		httpError(w, 404, "no such account")
		return
	}
	sess, err := s.Sessions.LaunchAccountLogin(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpError(w, 404, "no such account")
			return
		}
		httpError(w, 409, "%s", err.Error())
		return
	}
	writeJSON(w, 201, s.sessionView(sess))
}

// sessionAccount is the label a session card shows, only when its CLI has
// more than one login on that machine.
func (s *Server) sessionAccount(row *store.Session) string {
	if !accounts.Supported(row.Agent) {
		return ""
	}
	rows, err := s.DB.AccountsFor(row.TargetID, row.Agent)
	if err != nil || len(rows) == 0 {
		return ""
	}
	current := limits.Account(s.DB, row.TargetID, row.Agent, row.AccountID)
	count := len(rows)
	if dflt, err := s.DB.DefaultAccountFor(row.TargetID, row.Agent); err != nil || dflt == nil {
		count++ // the unregistered default login is one more
	}
	if count < 2 {
		return ""
	}
	if current == nil {
		if row.AccountID != nil {
			return "removed account"
		}
		return "Default"
	}
	return current.Label
}
