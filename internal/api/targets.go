package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/JeremiahM37/lectern/internal/agents"
	"github.com/JeremiahM37/lectern/internal/creds"
	"github.com/JeremiahM37/lectern/internal/executor"
	"github.com/JeremiahM37/lectern/internal/store"
)

var targetKinds = []string{"local", "ssh", "pct", "sandbox", "mock"}

type targetIn struct {
	Name          string  `json:"name"`
	Kind          *string `json:"kind"`
	Host          string  `json:"host"`
	Port          *int    `json:"port"`
	User          *string `json:"user"`
	KeyPath       string  `json:"key_path"`
	Workroot      string  `json:"workroot"`
	MaxConcurrent *int    `json:"max_concurrent"`
	Sandbox       bool    `json:"sandbox"`
	// ContextPaths are control-plane paths (globs ok) staged into every worktree
	// on this target.
	ContextPaths []string `json:"context_paths"`
	// MemoryDir opts in to sharing one Claude Code memory store across attempts.
	// It depends on the CLI's internal ~/.claude/projects layout, hence off
	// unless you set it.
	MemoryDir string `json:"memory_dir"`
	// CommandPrefix wraps every command, for hosts whose SSH lands somewhere
	// other than the work (a Windows box with its toolchain in WSL).
	CommandPrefix string `json:"command_prefix"`
}

func (s *Server) listTargets(w http.ResponseWriter, r *http.Request) {
	rows, err := s.DB.Targets()
	if err != nil {
		respondErr(w, err)
		return
	}
	writeJSON(w, 200, rows)
}

func (s *Server) createTarget(w http.ResponseWriter, r *http.Request) {
	var in targetIn
	if err := decodeBody(r, &in); err != nil {
		httpError(w, 422, "%s", err.Error())
		return
	}
	if in.Name == "" {
		httpError(w, 422, "name is required")
		return
	}
	kind := "ssh"
	if in.Kind != nil {
		kind = *in.Kind
	}
	if !oneOf(kind, targetKinds...) {
		httpError(w, 422, "kind must be one of %v", targetKinds)
		return
	}
	if _, err := s.DB.TargetByName(in.Name); err == nil {
		httpError(w, 409, "target name exists")
		return
	}
	t := &store.Target{
		Name: in.Name, Kind: kind, Host: in.Host, Port: valOr(in.Port, 22),
		User: strOr(in.User, "root"), KeyPath: in.KeyPath, Workroot: in.Workroot,
		MaxConcurrent: valOr(in.MaxConcurrent, 4), Sandbox: boolInt(in.Sandbox),
		ContextJSON: store.J(orEmpty(in.ContextPaths)), MemoryDir: in.MemoryDir,
		CommandPrefix: in.CommandPrefix,
	}
	out, err := s.DB.InsertTarget(t)
	if err != nil {
		respondErr(w, err)
		return
	}
	writeJSON(w, 201, out)
}

type targetPatch struct {
	ContextPaths  *[]string `json:"context_paths"`
	MemoryDir     *string   `json:"memory_dir"`
	Workroot      *string   `json:"workroot"`
	MaxConcurrent *int      `json:"max_concurrent"`
	// Connection details are patchable because machines move. Without this, a
	// target that changed address had to be deleted and recreated — which is
	// blocked while it has projects, so the only way out was to re-point every
	// project by hand. A LAN renumber should not cost that.
	Host          *string `json:"host"`
	CommandPrefix *string `json:"command_prefix"`
	User          *string `json:"user"`
	Port          *int    `json:"port"`
	KeyPath       *string `json:"key_path"`
	Name          *string `json:"name"`
}

func (s *Server) patchTarget(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		httpError(w, 404, "no such target")
		return
	}
	if _, err := s.DB.Target(id); err != nil {
		httpError(w, 404, "no such target")
		return
	}
	var p targetPatch
	if err := decodeBody(r, &p); err != nil {
		httpError(w, 422, "%s", err.Error())
		return
	}
	fields := map[string]any{}
	if p.ContextPaths != nil {
		fields["context_json"] = store.J(orEmpty(*p.ContextPaths))
	}
	if p.MemoryDir != nil {
		fields["memory_dir"] = *p.MemoryDir
	}
	if p.Workroot != nil {
		fields["workroot"] = *p.Workroot
	}
	if p.MaxConcurrent != nil {
		fields["max_concurrent"] = *p.MaxConcurrent
	}
	setStr(fields, "host", p.Host)
	setStr(fields, "command_prefix", p.CommandPrefix)
	setStr(fields, "user", p.User)
	setStr(fields, "key_path", p.KeyPath)
	if p.Port != nil {
		fields["port"] = *p.Port
	}
	if p.Name != nil && strings.TrimSpace(*p.Name) != "" {
		if existing, err := s.DB.TargetByName(strings.TrimSpace(*p.Name)); err == nil &&
			existing.ID != id {
			httpError(w, 409, "target name exists")
			return
		}
		fields["name"] = strings.TrimSpace(*p.Name)
	}
	if len(fields) > 0 {
		if err := s.DB.Update("targets", id, fields); err != nil {
			respondErr(w, err)
			return
		}
		// connection details may have changed under the cached executor
		s.Reg.Reset()
	}
	out, err := s.DB.Target(id)
	if err != nil {
		respondErr(w, err)
		return
	}
	writeJSON(w, 200, out)
}

// checkTarget probes a target's capabilities. With ?deep=true it also does a real
// one-token auth round-trip, which both TESTS and HEALS the exact path a
// dispatch uses.
func (s *Server) checkTarget(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		httpError(w, 404, "no such target")
		return
	}
	t, err := s.DB.Target(id)
	if err != nil {
		httpError(w, 404, "no such target")
		return
	}
	ctx := r.Context()
	status := "offline"
	var info map[string]any

	ex, exErr := s.Reg.For(t)
	if exErr == nil {
		if mock, ok := ex.(*executor.Mock); ok {
			info = mock.Probe()
		} else {
			info, exErr = executor.Probe(ctx, ex)
		}
	}
	switch {
	case exErr != nil:
		info = map[string]any{"error": exErr.Error()}
	default:
		status = "degraded"
		if info["git"] != nil && info["tmux"] != nil {
			status = "online"
		}
		if r.URL.Query().Get("deep") == "true" && info["claude"] != nil {
			// provision current auth FIRST, so the probe exercises the same code
			// path a dispatch does — fresh OAuth creds, or the API key
			s.Creds().Provision(ctx, ex, t.Kind, t.Name, "claude")
			prefix, _ := agents.EnvPrefix(s.Creds().BaseAgentEnv(), false)
			// </dev/null: `claude -p` reads stdin to EOF and an ssh exec channel
			// never EOFs, so without the redirect the probe hangs until timeout
			res, err := ex.Run(ctx, prefix+
				`claude -p "Reply with exactly: ok" --model haiku < /dev/null`,
				executor.RunOpts{Timeout: 120})
			if err == nil && res.OK() {
				info["claude_auth"] = "ok"
			} else {
				detail := ""
				if err != nil {
					detail = err.Error()
				} else {
					detail = clipEnd(res.Stdout+res.Stderr, 300)
				}
				info["claude_auth"] = "FAILED: " + detail
				status = "degraded"
			}
		}
	}
	raw, _ := json.Marshal(info)
	s.DB.Update("targets", id, map[string]any{"status": status, "info_json": string(raw)})
	out, err := s.DB.Target(id)
	if err != nil {
		respondErr(w, err)
		return
	}
	writeJSON(w, 200, out)
}

func (s *Server) deleteTarget(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		httpError(w, 404, "no such target")
		return
	}
	if s.DB.Exists("projects", "target_id=?", id) {
		httpError(w, 409, "target has projects")
		return
	}
	if _, err := s.DB.Exec(`DELETE FROM targets WHERE id=?`, id); err != nil {
		respondErr(w, err)
		return
	}
	s.Reg.Reset()
	w.WriteHeader(204)
}

// Creds exposes the provisioner the scheduler owns, so the deep probe and a
// dispatch can never drift apart.
func (s *Server) Creds() *creds.Provisioner { return s.Sched.Creds }

func valOr(p *int, def int) int {
	if p == nil || *p == 0 {
		return def
	}
	return *p
}

func strOr(p *string, def string) string {
	if p == nil || *p == "" {
		return def
	}
	return *p
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func orEmpty(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

func clipEnd(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
