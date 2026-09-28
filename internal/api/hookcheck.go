package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// The hook round trip check (`lectern doctor`). Sessions call back to the
// hook base this server hands them, so the only honest test is to call that
// exact address and see whether it is this process that answers. A base that
// names another port reaches nothing, or another Lectern, and both look fine
// from the outside: status and approvals just never arrive.

func (s *Server) hookPingID() string {
	s.hookPingOnce.Do(func() {
		b := make([]byte, 16)
		_, _ = rand.Read(b)
		s.hookPing = hex.EncodeToString(b)
	})
	return s.hookPing
}

// hookPingHandler is GET /api/hook/ping, outside the auth gate like every
// /api/hook/ route: it names this process with a random value and nothing
// else.
func (s *Server) hookPingHandler(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"instance": s.hookPingID()})
}

func (s *Server) hookBase() string {
	if s.Sessions != nil && s.Sessions.HookBase != "" {
		return strings.TrimRight(s.Sessions.HookBase, "/")
	}
	if s.Cfg == nil {
		return ""
	}
	return strings.TrimRight(firstNonEmptyStr(s.Cfg.HookBase, s.Cfg.BaseURL), "/")
}

// hookDiagnostics is GET /api/diagnostics/hooks: the hook base new sessions
// get, and whether a request to it comes back to this same server.
func (s *Server) hookDiagnostics(w http.ResponseWriter, r *http.Request) {
	base := s.hookBase()
	ok, detail := checkHookBase(r.Context(), base, s.hookPingID())
	writeJSON(w, 200, map[string]any{"hook_base": base, "ok": ok, "detail": detail})
}

func checkHookBase(ctx context.Context, base, want string) (bool, string) {
	if base == "" {
		return false, "no hook base is configured"
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/hook/ping", nil)
	if err != nil {
		return false, err.Error()
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return false, fmt.Sprintf("nothing answers at %s: %v", base, err)
	}
	defer res.Body.Close()
	var got struct {
		Instance string `json:"instance"`
	}
	if res.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(res.Body, 4096)).Decode(&got) != nil {
		return false, fmt.Sprintf("%s answered %s, so it is not a Lectern this build can check (an older one, or another program)", base, res.Status)
	}
	if got.Instance != want {
		return false, fmt.Sprintf("%s is a different Lectern server than this one", base)
	}
	return true, "sessions call back to " + base + ", and it reaches this server"
}
