package scheduler

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/JeremiahM37/lectern/v2/internal/executor"
	"github.com/JeremiahM37/lectern/v2/internal/sandbox"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// SandboxProvider builds a sandbox target's provider (docs/sandboxes.md).
// repo is the project's repo path, for a script provider's {repo}.
func (s *Scheduler) SandboxProvider(ctx context.Context, target *store.Target, repo string) (sandbox.Provider, error) {
	cfg := sandbox.ParseConfig(target.SandboxJSON)
	host, err := s.Reg.For(target)
	if err != nil {
		return nil, err
	}
	switch cfg.Provider {
	case "docker":
		if cfg.Machine != "" && !s.Cfg.Mock {
			m, err := s.DB.TargetByName(cfg.Machine)
			if err != nil {
				return nil, executor.Errf("docker machine %q is not a Lectern machine", cfg.Machine)
			}
			if m.Kind == "sandbox" {
				return nil, executor.Errf("a sandbox cannot run on another sandbox machine")
			}
			if host, err = s.Reg.For(m); err != nil {
				return nil, err
			}
		}
		return &sandbox.Docker{Host: host, Cfg: cfg}, nil
	case "script":
		return sandbox.LoadScript(ctx, host, cfg, repo)
	default:
		return &sandbox.Proxmox{Host: host, Template: target.Host, Log: s.Log}, nil
	}
}

// sandboxEnv is what a new sandbox is given: the project's environment, the
// dispatch's own (per workspace) and Lectern's identifiers.
func (s *Scheduler) sandboxEnv(att *store.Attempt, c *runCtx) map[string]string {
	env := projectEnv(c.Project)
	for k, v := range s.attemptEnv(att.ID) {
		env[k] = v
	}
	env["LECTERN_TASK_ID"] = fmt.Sprint(att.TaskID)
	env["LECTERN_ATTEMPT_ID"] = fmt.Sprint(att.ID)
	env["LECTERN_PROJECT"] = c.Project.Name
	return env
}

// attemptEnv is the environment a dispatch set for this attempt.
func (s *Scheduler) attemptEnv(attemptID int64) map[string]string {
	out := map[string]string{}
	var raw map[string]any
	if json.Unmarshal([]byte(s.DB.AttemptEnv(attemptID)), &raw) == nil {
		for k, v := range raw {
			out[k] = fmt.Sprint(v)
		}
	}
	return out
}

// sandboxExecutor runs commands inside an attempt's sandbox, whichever
// provider made it.
func (s *Scheduler) sandboxExecutor(att *store.Attempt, target *store.Target) (executor.Executor, error) {
	cfg := sandbox.ParseConfig(target.SandboxJSON)
	if cfg.Provider == "proxmox" {
		return executor.NewPct(att.SandboxVMID), nil
	}
	repo := ""
	if c, err := s.contextFor(att); err == nil {
		repo = c.Project.RepoPath
	}
	p, err := s.SandboxProvider(context.Background(), target, repo)
	if err != nil {
		return nil, err
	}
	return p.Exec(att.SandboxVMID), nil
}

// AttemptExecutor is attemptExecutor for the API: review, terminals and
// workspace views of a sandbox attempt run inside its sandbox.
func (s *Scheduler) AttemptExecutor(att *store.Attempt, target *store.Target) (executor.Executor, error) {
	return s.attemptExecutor(att, target)
}

// finishSandbox applies the target's on-finish rule to an attempt's
// sandbox: destroy (the default), suspend or keep.
func (s *Scheduler) finishSandbox(ctx context.Context, att *store.Attempt, c *runCtx, id string, cancelled bool) {
	cfg := sandbox.ParseConfig(c.Target.SandboxJSON)
	rec, _ := s.DB.SandboxForAttempt(att.ID)
	p, err := s.SandboxProvider(ctx, c.Target, c.Project.RepoPath)
	if err != nil {
		s.Log.Warn("sandbox provider unavailable at finish", "attempt", att.ID, "err", err)
		return
	}
	mode := cfg.OnFinish
	if cancelled || mode == "" {
		mode = "destroy"
	}
	switch mode {
	case "keep":
		if rec != nil {
			s.DB.SetSandboxStatus(rec.ID, "kept")
		}
		return
	case "suspend":
		if err := p.Suspend(ctx, id); err == nil {
			if rec != nil {
				s.DB.SetSandboxStatus(rec.ID, "suspended")
			}
			return
		} else {
			s.Log.Warn("sandbox suspend failed, destroying", "id", id, "err", err)
		}
	}
	if err := p.Destroy(ctx, id); err != nil {
		s.Log.Warn("sandbox destroy failed", "id", id, "err", err)
		return
	}
	if rec != nil {
		s.DB.SetSandboxStatus(rec.ID, "destroyed")
	}
	s.DB.Update("attempts", att.ID, map[string]any{"worktree_path": ""})
}
