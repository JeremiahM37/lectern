package app

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/config"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/hooks"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm/registry"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/mcp"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/nvim"
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
	// postEdit formats edited files and reports new diagnostics; nil when
	// format_on_edit is off.
	postEdit *postEditor
	hookR    *hooks.Runner
	mcp      *mcp.Manager
	cmap     *repomap.Map
	mapOK    chan struct{} // closed when the first code map build finishes
}

// wire registers every tool and starts optional subsystems.
func (a *App) wire(ctx context.Context) error {
	a.ext.undo = fs.NewUndoStack()
	fs.RegisterWithUndo(a.Tools, a.ext.undo)
	symedit.Register(a.Tools)
	a.ext.jobs = exec.NewJobs(exec.Options{Allow: a.Cfg.Allow, Deny: a.Cfg.Deny, ToolTimeout: a.toolTimeout()})
	exec.Register(a.Tools, a.ext.jobs)
	a.ext.lsp = lsp.NewManager(a.Workdir)
	lsp.Register(a.Tools, a.ext.lsp)
	// The editor tool answers from the editor the user has open; the write
	// hook tells the code map and language server about the user's saves.
	nvim.Register(a.Tools)
	nvim.SetWriteHook(func(abs string) {
		if rel, err := filepath.Rel(a.Workdir, abs); err == nil && !strings.HasPrefix(rel, "..") && a.ext.cmap != nil {
			go a.ext.cmap.Update([]string{rel})
		}
		if a.ext.lsp != nil {
			go a.ext.lsp.FileChanged(abs)
		}
	})
	if config.Bool(a.Cfg.FormatOnEdit, true) {
		a.ext.postEdit = newPostEditor(a.Workdir, a.ext.lsp)
	}
	web.Register(a.Tools, web.Options{SearchURL: a.Cfg.SearchURL, BraveAPIKey: a.braveKey()})
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
			a.services["repomap"] = m
			repomap.Register(a.Tools, m)
			go func() {
				defer close(a.ext.mapOK)
				if m.Build(context.WithoutCancel(ctx), nil) == nil {
					a.mu.Lock()
					a.codeMap = m.Render(repomap.Budget(0), nil)
					a.mu.Unlock()
					// The post-edit pass checks an edited file's importers and
					// gives clone hints from the built map.
					a.ext.postEdit.SetDependents(m)
				}
				if w := m.Warning(); w != "" {
					a.emit(agent.Event{Kind: agent.EvNotice, Text: w})
				}
				a.installSummarizer(m)
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
	a.Subscribe(a.onFileClaims)
	a.Subscribe(a.onCommandRan)
	return nil
}

// braveKey returns the Brave Search key: the variable named by
// search.brave_key_env (default BRAVE_SEARCH_API_KEY), or a key stored in the
// registry. It is handed to the web tool only and never shown to the model.
func (a *App) braveKey() string {
	if a.Registry == nil {
		return ""
	}
	v, _ := a.Registry.Secret(registry.BraveKeyID, a.Cfg.Search.BraveEnv())
	return v
}

// toolTimeout is the default limit for shell and project commands, from
// config tool_timeout (config.ClampToolTimeout). Zero config means no limit.
func (a *App) toolTimeout() time.Duration {
	if d := config.ClampToolTimeout(a.Cfg.ToolTimeout); d > 0 {
		return d
	}
	return exec.NoTimeout
}

// RenderMap returns the code map sized for a conversation of
// conversationTokens tokens (repomap.Budget), focused on focus when given.
// It is "" until the first build has finished or when the map is off.
func (a *App) RenderMap(conversationTokens int, focus []string) string {
	if a.ext.cmap == nil {
		return ""
	}
	<-a.ext.mapOK
	return a.ext.cmap.Render(repomap.Budget(conversationTokens), focus)
}

// shellTools are the tools that can rewrite files the edit tools never
// reported, so the code map must re-check its files after them.
var shellTools = map[string]bool{"shell": true, "project": true, "git": true}

// onCommandRan re-checks the code map after a command that may have changed
// files. Recheck compares modification times, so it costs a stat per indexed
// file and nothing when nothing moved. It runs in the background so the
// next model step does not wait for it.
func (a *App) onCommandRan(e agent.Event) {
	if e.Kind != agent.EvToolResult || !shellTools[e.Tool] || a.ext.cmap == nil {
		return
	}
	m := a.ext.cmap
	go func() {
		<-a.ext.mapOK
		_, _ = m.Recheck()
	}()
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
		// The open editor shows what the agent changed. When the user has
		// unsaved edits in that file the buffer keeps them, so it no longer
		// matches the disk: say so rather than let it pass silently.
		rel := f
		nvim.FollowThen(abs, 0, func(res string) {
			if res == "modified" {
				a.emit(agent.Event{Kind: agent.EvWarning, At: time.Now(), Text: fmt.Sprintf(
					"%s changed on disk, but your editor has unsaved edits in it and still shows your version. "+
						"Saving there overwrites the agent's change; :e! loads it instead.", rel)})
			}
		})
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
