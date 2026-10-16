package app

import (
	"context"
	"path/filepath"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/agent"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/lsp"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/memory"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/skills"
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
}

// wire registers every tool and starts optional subsystems.
func (a *App) wire(ctx context.Context) error {
	a.ext.undo = fs.NewUndoStack()
	fs.RegisterWithUndo(a.Tools, a.ext.undo)
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
	a.Subscribe(a.onFilesChanged)
	return nil
}

// onFilesChanged keeps language servers (and the code map) in sync with
// files the agent edits.
func (a *App) onFilesChanged(e agent.Event) {
	if e.Kind != agent.EvToolResult || e.Result == nil || e.Result.IsError || len(e.Result.Files) == 0 {
		return
	}
	for _, f := range e.Result.Files {
		abs := f
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(a.Workdir, f)
		}
		if a.ext.lsp != nil {
			a.ext.lsp.FileChanged(abs)
		}
	}
}

func (e *extensions) sessionStart(ctx context.Context, a *App) {}

func (e *extensions) afterTurn(a *App, s agent.TurnStats) {}

func (e *extensions) close(a *App) {
	if e.jobs != nil {
		e.jobs.KillAll()
	}
	if e.lsp != nil {
		e.lsp.Shutdown()
	}
}
