package app

// Backend methods behind the wave 9 commands: /claim, /storage, /hooks and
// /privacy session. They are optional interfaces in package tui, so the
// Backend interface itself is unchanged.

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/config"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/hooks"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui"
)

// claimPath resolves a path the user typed (relative to the workdir, or
// absolute) and refuses paths outside it.
func (a *App) claimPath(p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" {
		return "", fmt.Errorf("give a file path")
	}
	abs := p
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(a.Workdir, abs)
	}
	abs = filepath.Clean(abs)
	if rel, err := filepath.Rel(a.Workdir, abs); err != nil || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("%s is outside the project", p)
	}
	return abs, nil
}

// relPath shows a claimed path relative to the workdir.
func (a *App) relPath(abs string) string {
	if r, err := filepath.Rel(a.Workdir, abs); err == nil {
		return filepath.ToSlash(r)
	}
	return abs
}

// Claims lists the file claims held by every tab in this process.
func (a *App) Claims() []tui.ClaimRow {
	rows := fileClaims.snapshot(a.claimOwner)
	out := make([]tui.ClaimRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, tui.ClaimRow{Path: a.relPath(r.Path), Label: r.Label, Age: r.Age, Mine: r.Mine})
	}
	return out
}

// ForceClaim takes a file from whichever tab holds it. It returns the label
// of the tab that lost it, or "" when nobody held it.
func (a *App) ForceClaim(path string) (string, error) {
	abs, err := a.claimPath(path)
	if err != nil {
		return "", err
	}
	prev, displaced := fileClaims.forceClaim(abs, a.claimOwner, a.claimLabel)
	if !displaced {
		return "", nil
	}
	return prev.label, nil
}

// ReleaseClaim drops this tab's claim on one file. It never touches another
// tab's claim; false means this tab did not hold the file.
func (a *App) ReleaseClaim(path string) (bool, error) {
	abs, err := a.claimPath(path)
	if err != nil {
		return false, err
	}
	return fileClaims.releaseFile(abs, a.claimOwner), nil
}

// ReleaseAllClaims drops every claim this tab holds and reports how many.
func (a *App) ReleaseAllClaims() int {
	n := 0
	for _, r := range fileClaims.snapshot(a.claimOwner) {
		if r.Mine && fileClaims.releaseFile(r.Path, a.claimOwner) {
			n++
		}
	}
	return n
}

// storageDirs names the directories Scribe owns for this project. Only
// these are ever scanned or cleaned; the user's own files are never listed.
func (a *App) storageDirs() []tui.StorageItem {
	pd := config.ProjectDir(a.Workdir)
	return []tui.StorageItem{
		{Key: "cache", Label: "Code map and search cache", Path: filepath.Join(pd, "cache")},
		{Key: "plans", Label: "Saved plans", Path: filepath.Join(pd, "plans")},
		{Key: "sessions", Label: "Saved sessions (conversation history)", Path: filepath.Join(pd, "sessions")},
	}
}

// StorageReport sizes Scribe's own data for this project.
func (a *App) StorageReport() []tui.StorageItem {
	items := a.storageDirs()
	for i := range items {
		items[i].Bytes, items[i].Files = dirSize(items[i].Path)
	}
	return items
}

// dirSize totals the regular files under dir. A missing directory is empty.
func dirSize(dir string) (int64, int) {
	var bytes int64
	var files int
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if !d.IsDir() {
			if info, e := d.Info(); e == nil {
				bytes += info.Size()
				files++
			}
		}
		return nil
	})
	return bytes, files
}

// CleanStorage deletes the contents of the named storage areas (the
// directories themselves stay) and reports the bytes freed. The current
// session's folder is always kept.
func (a *App) CleanStorage(keys []string) (int64, error) {
	var freed int64
	for _, want := range keys {
		var dir string
		for _, it := range a.storageDirs() {
			if it.Key == want {
				dir = it.Path
			}
		}
		if dir == "" {
			return freed, fmt.Errorf("unknown storage area %q", want)
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue // nothing stored yet
		}
		for _, e := range entries {
			if want == "sessions" && e.Name() == a.Meta.ID {
				continue
			}
			p := filepath.Join(dir, e.Name())
			b, _ := dirSize(p)
			if info, err := os.Stat(p); err == nil && !info.IsDir() {
				b = info.Size()
			}
			if err := os.RemoveAll(p); err != nil {
				return freed, fmt.Errorf("remove %s: %w", p, err)
			}
			freed += b
		}
	}
	return freed, nil
}

// HookEvents lists the hook events this session runs, with their switch state.
func (a *App) HookEvents() []tui.HookEvent {
	r, ok := a.ext.hooks.(*hooks.Runner)
	if !ok || r == nil {
		return nil
	}
	var out []tui.HookEvent
	for _, c := range r.EventCounts() {
		out = append(out, tui.HookEvent{Event: c.Event, Hooks: c.Hooks, Off: c.Off})
	}
	return out
}

// SetHookEventOff switches one event's hooks off or on for this session only.
func (a *App) SetHookEventOff(event string, off bool) error {
	r, ok := a.ext.hooks.(*hooks.Runner)
	if !ok || r == nil {
		return fmt.Errorf("no hooks are loaded")
	}
	for _, c := range r.EventCounts() {
		if c.Event == event {
			r.SetEventOff(event, off)
			return nil
		}
	}
	return fmt.Errorf("no hooks use the %s event", event)
}

// SessionForbidden lists the forbidden patterns added for this session.
func (a *App) SessionForbidden() []string { return config.SessionForbidden() }

// AddSessionForbidden forbids a pattern until Scribe exits.
func (a *App) AddSessionForbidden(pat string) bool { return config.AddSessionForbidden(pat) }

// RemoveSessionForbidden lifts a session-only pattern.
func (a *App) RemoveSessionForbidden(pat string) bool { return config.RemoveSessionForbidden(pat) }

// Compile-time checks that App provides the optional wave 9 interfaces.
var (
	_ tui.FileClaimer      = (*App)(nil)
	_ tui.StorageInspector = (*App)(nil)
	_ tui.HookSwitcher     = (*App)(nil)
	_ tui.SessionPrivacy   = (*App)(nil)
)

// The tab backend forwards the wave 9 capabilities to its current app; the
// TUI checks for them on the backend (see backend_optional.go).

func (b *scribeBackend) Claims() []tui.ClaimRow                 { return b.current().Claims() }
func (b *scribeBackend) ForceClaim(path string) (string, error) { return b.current().ForceClaim(path) }
func (b *scribeBackend) ReleaseClaim(path string) (bool, error) {
	return b.current().ReleaseClaim(path)
}
func (b *scribeBackend) ReleaseAllClaims() int            { return b.current().ReleaseAllClaims() }
func (b *scribeBackend) StorageReport() []tui.StorageItem { return b.current().StorageReport() }
func (b *scribeBackend) CleanStorage(keys []string) (int64, error) {
	return b.current().CleanStorage(keys)
}
func (b *scribeBackend) HookEvents() []tui.HookEvent { return b.current().HookEvents() }
func (b *scribeBackend) SetHookEventOff(event string, off bool) error {
	return b.current().SetHookEventOff(event, off)
}
func (b *scribeBackend) SessionForbidden() []string { return b.current().SessionForbidden() }
func (b *scribeBackend) AddSessionForbidden(pat string) bool {
	return b.current().AddSessionForbidden(pat)
}
func (b *scribeBackend) RemoveSessionForbidden(pat string) bool {
	return b.current().RemoveSessionForbidden(pat)
}
