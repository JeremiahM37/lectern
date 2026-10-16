package app

import (
	"context"
	"path/filepath"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/config"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/hooks"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/mcp"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/repomap"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/agent"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/lsp"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/memory"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/skills"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/symedit"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools/exec"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools/fs"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools/web"
)

// extensions holds the optional subsystems. Each is nil when unavailable.
type extensions struct {
	hooks agent.Hooks
	undo  *fs.UndoStack
	jobs  *exec.Jobs
	lsp   *lsp.Manager
	hookR *hooks.Runner
	mcp   *mcp.Manager
	cmap  *repomap.Map
	mapOK chan struct{} // closed when the first code map build finishes
}

// wire registers every tool and starts optional subsystems.
func (a *App) wire(ctx context.Context) error {
	a.ext.undo = fs.NewUndoStack()
	fs.RegisterWithUndo(a.Tools, a.ext.undo)
	symedit.Register(a.Tools)
	a.ext.jobs = exec.NewJobs(exec.Options{Allow: a.Cfg.Allow, Deny: a.Cfg.Deny})
	exec.Register(a.Tools, a.ext.jobs)
	a.ext.lsp = lsp.NewManager(a.Workdir)
	lsp.Register(a.Tools, a.ext.lsp)
	web.Register(a.Tools, web.Options{SearchURL: a.Cfg.SearchURL})
	if a.Memory != nil {
		memory.Register(a.Tools, a.Memory)
	}
	if a.Skills != nil {
		skills.Register(a.Tools)
	}
	if r, err := hooks.NewFromDisk(a.Workdir, a.Meta.ID); err == nil {
		a.ext.hookR = r
		a.ext.hooks = r
	}
	if m, err := mcp.NewFromDisk(a.Workdir, hooks.Trusted(a.Workdir), a.opts.Version); err == nil {
		a.ext.mcp = m
		go func() {
			m.Start(context.WithoutCancel(ctx))
			m.RegisterTools(a.Tools)
		}()
	}
	a.ext.mapOK = make(chan struct{})
	if config.Bool(a.Cfg.RepoMap, true) && !a.opts.NoRepoMap {
		if m, err := repomap.Open(a.Workdir, repomap.Options{}); err == nil {
			a.ext.cmap = m
			repomap.Register(a.Tools, m)
			go func() {
				defer close(a.ext.mapOK)
				if m.Build(context.WithoutCancel(ctx), nil) == nil {
					a.mu.Lock()
					a.codeMap = m.Render(2500, nil)
					a.mu.Unlock()
				}
			}()
			// A small repo maps in well under a second; give it a moment so
			// the first prompt already carries the map.
			select {
			case <-a.ext.mapOK:
			case <-time.After(3 * time.Second):
			}
		} else {
			close(a.ext.mapOK)
		}
	} else {
		close(a.ext.mapOK)
	}
	a.Subscribe(a.onFilesChanged)
	return nil
}

// onFilesChanged keeps language servers (and the code map) in sync with
// files the agent edits.
func (a *App) onFilesChanged(e agent.Event) {
	if e.Kind != agent.EvToolResult || e.Result == nil || e.Result.IsError || len(e.Result.Files) == 0 {
		return
	}
	var changed []string
	defer func() {
		if a.ext.cmap != nil && len(changed) > 0 {
			go a.ext.cmap.Update(changed)
		}
	}()
	for _, f := range e.Result.Files {
		changed = append(changed, f)
		abs := f
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(a.Workdir, f)
		}
		if a.ext.lsp != nil {
			a.ext.lsp.FileChanged(abs)
		}
	}
}

func (e *extensions) sessionStart(ctx context.Context, a *App) {
	if e.hookR != nil {
		e.hookR.SessionStart(ctx)
	}
}

func (e *extensions) afterTurn(a *App, s agent.TurnStats) {}

func (e *extensions) close(a *App) {
	if e.hookR != nil {
		e.hookR.SessionEnd(context.Background())
	}
	if e.mcp != nil {
		_ = e.mcp.Close()
	}
	if e.cmap != nil {
		<-e.mapOK
		_ = e.cmap.Close()
	}
	if e.jobs != nil {
		e.jobs.KillAll()
	}
	if e.lsp != nil {
		e.lsp.Shutdown()
	}
}
