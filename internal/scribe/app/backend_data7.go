package app

// Backend methods for the memory settings and soft delete, the session
// binding that keeps recalled memories from repeating, and the other
// wave 7 data the modals ask for.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/memory"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui"
	"github.com/JeremiahM37/lectern/v2/internal/version"
)

// releaseListURL is the newest-release endpoint. LECTERN_RELEASE_BASE points
// it elsewhere, as it does for lectern update.
const releaseListURL = "https://api.github.com/repos/JeremiahM37/lectern/releases/latest"

// Free space below these sizes is a warning and a failure in Diagnose.
const (
	diskWarnBytes = 1 << 30
	diskFailBytes = 200 << 20
)

// bindMemorySession tells memory which session this App is, so the records
// it saves carry the session id and the memories already injected in it stay
// in one place. An ephemeral session has no directory to keep that in.
func (a *App) bindMemorySession() {
	if a.Memory == nil {
		return
	}
	dir := ""
	if !a.opts.Ephemeral && a.Store != nil {
		dir = a.Store.Dir(a.Meta.ID)
	}
	_ = a.Memory.SetSession(a.Meta.ID, dir)
}

// HiddenMemories implements tui.MemoryHider: the soft-deleted memories of
// both stores.
func (b *scribeBackend) HiddenMemories() []tui.MemoryItem {
	a := b.current()
	if a.Memory == nil {
		return nil
	}
	var out []tui.MemoryItem
	for _, db := range a.Memory.DBs() {
		recs, err := db.ListHidden(memoryCleanupLimit)
		if err != nil {
			continue
		}
		for _, r := range recs {
			out = append(out, memoryItem(r))
		}
	}
	return out
}

// HideMemory implements tui.MemoryHider.
func (b *scribeBackend) HideMemory(id string) error {
	return b.withMemoryDB(id, func(db *memory.DB) error { return db.Hide(id) })
}

// RestoreMemory implements tui.MemoryHider.
func (b *scribeBackend) RestoreMemory(id string) error {
	return b.withMemoryDB(id, func(db *memory.DB) error { return db.Restore(id) })
}

// withMemoryDB runs fn on the store that holds id.
func (b *scribeBackend) withMemoryDB(id string, fn func(*memory.DB) error) error {
	a := b.current()
	if a.Memory == nil {
		return errors.New("memory is off for this session")
	}
	for _, db := range a.Memory.DBs() {
		if _, err := db.Get(id); err == nil {
			return fn(db)
		}
	}
	return fmt.Errorf("no memory %q", id)
}

// MemoryPolicy implements tui.MemoryPolicyBackend: the effective settings.
func (b *scribeBackend) MemoryPolicy() tui.MemoryPolicy {
	a := b.current()
	if a.Memory == nil {
		return tui.MemoryPolicy{}
	}
	return tui.MemoryPolicy{
		WriteScope:  a.Memory.Policy.WriteScope,
		ReadScope:   a.Memory.Policy.ReadScope,
		ProjectOpen: a.Memory.Project != nil,
	}
}

// SaveMemoryPolicy implements tui.MemoryPolicyBackend. It writes the settings
// to the file for saveTo ("project" or "global") and applies the result to the
// open stores now, so the next recall or save already follows it.
func (b *scribeBackend) SaveMemoryPolicy(p tui.MemoryPolicy, saveTo string) error {
	a := b.current()
	if a.Memory == nil {
		return errors.New("memory is off for this session")
	}
	path, err := memory.PolicyPath(saveTo, a.Workdir)
	if err != nil {
		return err
	}
	if err := memory.SavePolicy(path, memory.Policy{WriteScope: p.WriteScope, ReadScope: p.ReadScope}); err != nil {
		return err
	}
	a.Memory.SetPolicy(memory.LoadPolicy(a.Workdir))
	return nil
}

// PingMCP implements tui.MCPPinger.
func (b *scribeBackend) PingMCP(name string) (time.Duration, error) {
	a := b.current()
	if a.ext.mcp == nil {
		return 0, errors.New("no MCP servers are configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return a.ext.mcp.Ping(ctx, name)
}

// Diagnose implements tui.Diagnoser. Providers are checked by key, and the
// local Ollama server by a short request to its address. Language and MCP
// servers report the state the managers already hold. Git and disk are
// read from the project. Nothing here spends model tokens.
func (b *scribeBackend) Diagnose() []tui.DiagnoseCheck {
	a := b.current()
	var out []tui.DiagnoseCheck
	for _, p := range b.Providers() {
		c := tui.DiagnoseCheck{Area: "Providers", Name: p.ID, State: "info"}
		switch {
		case p.ID == "ollama":
			if ollamaReachable(a.Registry) {
				c.State, c.Detail = "ok", "answering on its address"
			} else {
				c.State, c.Detail = "warn", "not answering on its address"
				c.Fix = "start Ollama, or set OLLAMA_HOST to a server that is running"
			}
		case p.Local:
			c.Detail = "local, no key needed"
		case p.HasKey:
			c.State, c.Detail = "ok", "key set"
			if p.KeySource != "" {
				c.Detail = "key from " + p.KeySource
			}
		default:
			c.Detail = "no key set"
		}
		out = append(out, c)
	}
	for _, s := range b.LSPSessions() {
		c := tui.DiagnoseCheck{Area: "Language servers", Name: lspNameOf(s), State: "ok", Detail: s.State}
		switch {
		case s.State == "failed" || s.LastError != "":
			c.State, c.Detail = "fail", oneLineStr(s.LastError)
			c.Fix = "install or fix the server named in the detail, then run /diagnose again"
		case s.Command == "":
			c.State, c.Detail = "warn", "server binary not found on PATH"
			c.Fix = "install the server, then run /diagnose again"
		case s.State == "starting":
			c.State = "info"
		}
		out = append(out, c)
	}
	out = append(out, lspGapChecks(a, b.LSPSessions())...)
	for _, s := range b.Servers() {
		if s.Kind != "mcp" {
			continue
		}
		c := tui.DiagnoseCheck{Area: "MCP servers", Name: s.Name, State: "info", Detail: s.State}
		switch s.State {
		case "ready":
			c.State, c.Detail = "ok", fmt.Sprintf("%d tools", s.Tools)
		case "failed":
			c.State, c.Detail = "fail", oneLineStr(s.Error)
			c.Fix = "check its command and env in the config, then restart it from /mcp"
		}
		out = append(out, c)
	}
	out = append(out, gitCheck(b.Git(), a.Workdir))
	out = append(out, diskCheck(a.Workdir))
	out = append(out, homeCheck())
	out = append(out, hooksChecks(a.Workdir)...)
	return out
}

// gitCheck says whether git is installed and what the project's branch is.
func gitCheck(g tui.GitStatus, dir string) tui.DiagnoseCheck {
	c := tui.DiagnoseCheck{Area: "Git", Name: "repository", State: "ok"}
	if _, err := exec.LookPath("git"); err != nil {
		return tui.DiagnoseCheck{Area: "Git", Name: "git", State: "fail", Detail: "not found on PATH",
			Fix: "install git and put it on PATH"}
	}
	if g.Branch == "" {
		c.State, c.Detail = "info", "not a git repository: "+dir
		return c
	}
	c.Detail = "branch " + g.Branch
	if g.Dirty {
		c.State, c.Detail = "warn", c.Detail+", uncommitted changes"
		c.Fix = "commit or stash when you want a clean baseline for /diff"
	}
	return c
}

// diskCheck reports the space left on the project's filesystem.
func diskCheck(dir string) tui.DiagnoseCheck {
	c := tui.DiagnoseCheck{Area: "Disk", Name: "project filesystem", State: "ok"}
	free, err := freeBytes(dir)
	if err != nil {
		c.State, c.Detail = "info", err.Error()
		return c
	}
	c.Detail = fmt.Sprintf("%.1f GB free", float64(free)/(1<<30))
	switch {
	case free < diskFailBytes:
		c.State, c.Fix = "fail", "free space: sessions and checkpoints are written to this disk"
	case free < diskWarnBytes:
		c.State, c.Fix = "warn", "free space: sessions and checkpoints are written to this disk"
	}
	return c
}

// CheckUpdate implements tui.UpdateChecker: the newest release against this
// build. It waits at most eight seconds for the release list.
func (b *scribeBackend) CheckUpdate() tui.UpdateStatus {
	st := tui.UpdateStatus{Current: b.Version()}
	url := releaseListURL
	if base := os.Getenv("LECTERN_RELEASE_BASE"); base != "" {
		url = strings.TrimRight(base, "/") + "/latest.json"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	rel, err := fetchRelease(ctx, &http.Client{Timeout: 8 * time.Second}, url)
	if err != nil {
		st.Err = err.Error()
		return st
	}
	tag := rel.Tag
	st.Latest = tag
	st.Notes = releaseNotes(rel.Body)
	st.URL = rel.HTMLURL
	// The same test lectern update uses: the same version with or without a
	// leading v is current, whatever the comparison says.
	if strings.TrimPrefix(st.Current, "v") == strings.TrimPrefix(tag, "v") {
		st.Known = true
		return st
	}
	older, known := version.Older(version.Info{Version: st.Current}, version.Info{Version: tag})
	st.Known = known
	st.Newer = known && older
	return st
}

func oneLineStr(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// lspNameOf is the row name the servers view gives a language server.
func lspNameOf(s tui.LSPSession) string {
	return s.Language + " " + filepath.Base(s.Root)
}
