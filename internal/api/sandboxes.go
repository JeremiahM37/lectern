package api

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/executor"
	"github.com/JeremiahM37/lectern/v2/internal/sandbox"
	"github.com/JeremiahM37/lectern/v2/internal/store"
	"github.com/JeremiahM37/lectern/v2/internal/terminal"
)

// Sandboxes (docs/sandboxes.md): the provider a sandbox target uses, the
// trusted hooks file of a script provider, and the lifecycle of every
// sandbox Lectern made. Each of these starts, stops or runs things on
// machines, so each needs a signed-in person.

type sandboxConfigIn = sandbox.Config

var envNameRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func validEnvName(k string) bool { return envNameRE.MatchString(k) }

// validSandboxConfig checks a config a person is saving and renders it.
func (s *Server) validSandboxConfig(w http.ResponseWriter, r *http.Request, cfg sandbox.Config) (string, error) {
	if !s.requireHuman(w, r, "choosing a sandbox provider") {
		return "", errors.New("forbidden")
	}
	if cfg.Provider == "" {
		cfg.Provider = "proxmox"
	}
	if err := cfg.Validate(); err != nil {
		httpError(w, 422, "%s", err)
		return "", err
	}
	if cfg.Machine != "" {
		m, err := s.DB.TargetByName(cfg.Machine)
		if err != nil || m.Kind == "sandbox" {
			httpError(w, 422, "machine %q must be an existing machine that is not itself a sandbox", cfg.Machine)
			return "", errors.New("bad machine")
		}
	}
	// Trust is only ever granted through the trust endpoint.
	cfg.Trusted = nil
	return cfg.JSON(), nil
}

func (s *Server) sandboxTarget(w http.ResponseWriter, r *http.Request) (*store.Target, bool) {
	t, ok := s.targetParam(w, r)
	if !ok {
		return nil, false
	}
	if t.Kind != "sandbox" {
		httpError(w, 409, "%s is not a sandbox machine", t.Name)
		return nil, false
	}
	return t, true
}

// putTargetSandbox replaces a sandbox target's provider config, keeping
// trust already granted to hooks files.
func (s *Server) putTargetSandbox(w http.ResponseWriter, r *http.Request) {
	t, ok := s.sandboxTarget(w, r)
	if !ok {
		return
	}
	var in sandbox.Config
	if err := decodeBody(r, &in); err != nil {
		httpError(w, 422, "%s", err)
		return
	}
	raw, err := s.validSandboxConfig(w, r, in)
	if err != nil {
		return
	}
	cfg := sandbox.ParseConfig(raw)
	cfg.Trusted = sandbox.ParseConfig(t.SandboxJSON).Trusted
	if err := s.DB.Update("targets", t.ID, map[string]any{"sandbox_json": cfg.JSON()}); err != nil {
		respondErr(w, err)
		return
	}
	out, _ := s.DB.Target(t.ID)
	writeJSON(w, 200, out)
}

// hooksPath resolves the script provider's file for a project (or the
// target's first project).
func (s *Server) hooksPath(t *store.Target, projectID int64) (string, string) {
	cfg := sandbox.ParseConfig(t.SandboxJSON)
	repo := ""
	if projectID != 0 {
		if p, err := s.DB.Project(projectID); err == nil && p.TargetID == t.ID {
			repo = p.RepoPath
		}
	} else if ps, err := s.DB.Projects(); err == nil {
		for _, p := range ps {
			if p.TargetID == t.ID {
				repo = p.RepoPath
				break
			}
		}
	}
	return cfg.ResolvePath(repo), repo
}

// sandboxHooks shows a script provider's hooks file, its hash and whether
// that exact content is trusted — what a person reviews before trusting.
func (s *Server) sandboxHooks(w http.ResponseWriter, r *http.Request) {
	t, ok := s.sandboxTarget(w, r)
	if !ok || !s.requireHuman(w, r, "reading sandbox hooks") {
		return
	}
	cfg := sandbox.ParseConfig(t.SandboxJSON)
	if cfg.Provider != "script" {
		httpError(w, 409, "this machine does not use the script provider")
		return
	}
	pid, _ := int64Query(r, "project_id")
	p, _ := s.hooksPath(t, pid)
	if sandbox.IsPluginProvider(p) {
		// Trusted through the plugin's own consent (Settings → Plugins).
		out := map[string]any{"path": p, "plugin": true}
		data, err := s.pluginSandboxHooks(p)
		if err != nil {
			out["error"], out["trusted"] = err.Error(), false
		} else {
			out["content"], out["sha256"], out["trusted"] = string(data), sandbox.Hash(data), true
		}
		writeJSON(w, 200, out)
		return
	}
	host, err := s.Reg.For(t)
	if err != nil {
		respondErr(w, err)
		return
	}
	data, err := host.ReadFile(r.Context(), p, 0)
	if err != nil {
		httpError(w, 502, "%s", err)
		return
	}
	out := map[string]any{"path": p, "content": string(data), "sha256": sandbox.Hash(data),
		"trusted": len(data) > 0 && cfg.Trusted[p] == sandbox.Hash(data)}
	if len(data) == 0 {
		out["error"] = "the file is missing or empty"
	} else if _, err := sandbox.ParseHooks(data); err != nil {
		out["error"] = err.Error()
	}
	writeJSON(w, 200, out)
}

// trustSandboxHooks records the hash of the content the person reviewed.
// It must still be the file's content, or nothing is trusted.
func (s *Server) trustSandboxHooks(w http.ResponseWriter, r *http.Request) {
	t, ok := s.sandboxTarget(w, r)
	if !ok || !s.requireHuman(w, r, "trusting sandbox hooks") {
		return
	}
	var in struct {
		Path   string `json:"path"`
		SHA256 string `json:"sha256"`
	}
	if err := decodeBody(r, &in); err != nil {
		httpError(w, 422, "%s", err)
		return
	}
	host, err := s.Reg.For(t)
	if err != nil {
		respondErr(w, err)
		return
	}
	data, err := host.ReadFile(r.Context(), in.Path, 0)
	if err != nil {
		httpError(w, 502, "%s", err)
		return
	}
	if len(data) == 0 || sandbox.Hash(data) != in.SHA256 {
		httpError(w, 409, "the file changed since you read it; review it again")
		return
	}
	if _, err := sandbox.ParseHooks(data); err != nil {
		httpError(w, 422, "%s", err)
		return
	}
	cfg := sandbox.ParseConfig(t.SandboxJSON)
	if cfg.Trusted == nil {
		cfg.Trusted = map[string]string{}
	}
	cfg.Trusted[in.Path] = in.SHA256
	if err := s.DB.Update("targets", t.ID, map[string]any{"sandbox_json": cfg.JSON()}); err != nil {
		respondErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"trusted": true, "path": in.Path})
}

type sandboxView struct {
	*store.Sandbox
	TargetName string          `json:"target_name"`
	TaskID     int64           `json:"task_id,omitempty"`
	TaskTitle  string          `json:"task_title,omitempty"`
	AttemptN   int             `json:"attempt_n,omitempty"`
	Can        map[string]bool `json:"can"`
}

func (s *Server) sandboxView(ctx context.Context, b *store.Sandbox) *sandboxView {
	v := &sandboxView{Sandbox: b, Can: map[string]bool{}}
	t, err := s.DB.Target(b.TargetID)
	if err != nil {
		return v
	}
	v.TargetName = t.Name
	if b.AttemptID != nil {
		if a, err := s.DB.Attempt(*b.AttemptID); err == nil {
			v.AttemptN = a.N
			if task, err := s.DB.Task(a.TaskID); err == nil {
				v.TaskID, v.TaskTitle = task.ID, task.Title
			}
		}
	}
	if b.DestroyedAt == nil {
		if p, err := s.Sched.SandboxProvider(ctx, t, b.Note); err == nil {
			v.Can["suspend"] = p.Can("suspend") && b.Status != "suspended"
			v.Can["resume"] = p.Can("resume") && b.Status == "suspended"
			v.Can["destroy"] = true
		} else {
			v.Can["destroy"] = true
		}
	}
	return v
}

func (s *Server) listSandboxes(w http.ResponseWriter, r *http.Request) {
	rows, err := s.DB.Sandboxes(r.URL.Query().Get("all") == "1")
	if err != nil {
		respondErr(w, err)
		return
	}
	out := make([]*sandboxView, 0, len(rows))
	for _, b := range rows {
		out = append(out, s.sandboxView(r.Context(), b))
	}
	writeJSON(w, 200, out)
}

// createSandbox makes one by hand, not for an attempt: to warm one, try a
// provider, or get a scratch environment. It is listed and destroyed like
// any other.
func (s *Server) createSandbox(w http.ResponseWriter, r *http.Request) {
	if !s.requireHuman(w, r, "creating a sandbox") {
		return
	}
	var in struct {
		TargetID  int64             `json:"target_id"`
		ProjectID int64             `json:"project_id"`
		Env       map[string]string `json:"env"`
	}
	if err := decodeBody(r, &in); err != nil {
		httpError(w, 422, "%s", err)
		return
	}
	t, err := s.DB.Target(in.TargetID)
	if err != nil || t.Kind != "sandbox" {
		httpError(w, 422, "target_id must be a sandbox machine")
		return
	}
	for k := range in.Env {
		if !validEnvName(k) {
			httpError(w, 422, "env name %q is not a variable name", k)
			return
		}
	}
	_, repo := s.hooksPath(t, in.ProjectID)
	p, err := s.Sched.SandboxProvider(r.Context(), t, repo)
	if err != nil {
		httpError(w, 409, "%s", err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Minute)
	defer cancel()
	name := "lec-sb-manual-" + time.Now().Format("150405")
	id, err := p.Create(ctx, sandbox.CreateRequest{Name: name, Env: in.Env})
	if err != nil {
		httpError(w, 502, "%s", err)
		return
	}
	rec, err := s.DB.InsertSandbox(&store.Sandbox{TargetID: t.ID, Provider: p.Name(), ExtID: id, Note: repo})
	if err != nil {
		respondErr(w, err)
		return
	}
	s.Bus.Publish("board", "sandbox", rec)
	writeJSON(w, 201, s.sandboxView(r.Context(), rec))
}

// sandboxAction suspends, resumes or destroys one.
func (s *Server) sandboxAction(w http.ResponseWriter, r *http.Request) {
	if !s.requireHuman(w, r, "changing a sandbox") {
		return
	}
	id, err := pathID(r, "id")
	if err != nil {
		httpError(w, 404, "no such sandbox")
		return
	}
	b, err := s.DB.Sandbox(id)
	if err != nil {
		httpError(w, 404, "no such sandbox")
		return
	}
	if b.DestroyedAt != nil {
		httpError(w, 409, "this sandbox is already destroyed")
		return
	}
	t, err := s.DB.Target(b.TargetID)
	if err != nil {
		respondErr(w, err)
		return
	}
	p, err := s.Sched.SandboxProvider(r.Context(), t, b.Note)
	if err != nil {
		httpError(w, 409, "%s", err)
		return
	}
	action := r.PathValue("action")
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	var status string
	switch action {
	case "suspend":
		err, status = p.Suspend(ctx, b.ExtID), "suspended"
	case "resume":
		err, status = p.Resume(ctx, b.ExtID), "running"
	case "destroy":
		err, status = p.Destroy(ctx, b.ExtID), "destroyed"
	default:
		httpError(w, 404, "unknown action %q", action)
		return
	}
	if err != nil {
		httpError(w, 502, "%s", err)
		return
	}
	s.DB.SetSandboxStatus(b.ID, status)
	fresh, _ := s.DB.Sandbox(b.ID)
	s.Bus.Publish("board", "sandbox", fresh)
	writeJSON(w, 200, s.sandboxView(r.Context(), fresh))
}

// sandboxExec runs commands inside a sandbox, by its provider's id.
func (s *Server) sandboxExec(target *store.Target, id string) (executor.Executor, error) {
	if s.Cfg.Mock {
		return s.Reg.For(target)
	}
	if sandbox.ParseConfig(target.SandboxJSON).Provider == "proxmox" {
		return executor.NewPct(id), nil
	}
	repo := ""
	_ = s.DB.QueryRow(`SELECT note FROM sandboxes WHERE target_id=? AND ext_id=? ORDER BY id DESC LIMIT 1`, target.ID, id).Scan(&repo)
	p, err := s.Sched.SandboxProvider(context.Background(), target, repo)
	if err != nil {
		return nil, err
	}
	return p.Exec(id), nil
}

// sandboxAttach gives a non-Proxmox sandbox attempt its terminal argv.
func (s *Server) sandboxAttach(att *store.Attempt, target *store.Target) func(string) ([]string, error) {
	if target.Kind != "sandbox" || att.SandboxVMID == "" || s.Cfg.Mock {
		return nil
	}
	cfg := sandbox.ParseConfig(target.SandboxJSON)
	if cfg.Provider == "proxmox" {
		return nil
	}
	return func(inner string) ([]string, error) {
		repo := ""
		if task, err := s.DB.Task(att.TaskID); err == nil {
			if p, err := s.DB.Project(task.ProjectID); err == nil {
				repo = p.RepoPath
			}
		}
		p, err := s.Sched.SandboxProvider(context.Background(), target, repo)
		if err != nil {
			return nil, err
		}
		if d, ok := p.(*sandbox.Docker); ok && cfg.Machine != "" {
			m, err := s.DB.TargetByName(cfg.Machine)
			if err != nil {
				return nil, err
			}
			if m.Kind == "ssh" {
				d.Remote = terminal.SSHPrefix(m)
			} else if m.Kind != "local" {
				return nil, executor.Errf("terminals into docker sandboxes need the docker machine to be local or SSH")
			}
		}
		return p.Attach(att.SandboxVMID, inner)
	}
}

func int64Query(r *http.Request, key string) (int64, bool) {
	v := strings.TrimSpace(r.URL.Query().Get(key))
	if v == "" {
		return 0, false
	}
	var n int64
	for _, c := range v {
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + int64(c-'0')
	}
	return n, true
}
