// Package plugins installs plugins, keeps the consent that pins each one, and
// hands their contributions to the places Lectern already has: the agent
// catalog, project workflows, agent MCP config, lifecycle hooks and the UI.
// The package format itself is internal/pluginpkg; docs/plugins.md is the
// user-facing description.
//
// The rule everything here serves: nothing from a plugin is used unless a
// person consented to exactly the content that is on disk. Content lives in
// <Dir>/store/<sha256>/, is re-hashed on every load and before every hook
// run, and a mismatch stops the plugin until someone trusts it again.
package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/pluginpkg"
	"github.com/JeremiahM37/lectern/v2/internal/sessions"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// Status values of an installed plugin.
const (
	StatusActive       = "active"        // consented, verified, enabled
	StatusDisabled     = "disabled"      // consented and verified, switched off
	StatusModified     = "modified"      // files on disk differ from the consented hash
	StatusNeedsConsent = "needs_consent" // content changed through an update not yet consented
	StatusMissing      = "missing"       // the content store lost its files
)

// Plugin is an installed plugin as the rest of Lectern sees it.
type Plugin struct {
	ID         string             `json:"id"`
	Bundled    bool               `json:"bundled"`
	Status     string             `json:"status"`
	Problem    string             `json:"problem,omitempty"`
	ProjectIDs []int64            `json:"project_ids"`
	Row        *store.Plugin      `json:"install,omitempty"`
	Pkg        *pluginpkg.Package `json:"-"`
	// Manifest is the consented manifest, or nil when none can be read.
	Manifest *pluginpkg.Manifest `json:"manifest,omitempty"`
}

// Active reports whether contributions from p may be used at all.
func (p *Plugin) Active() bool { return p != nil && p.Status == StatusActive && p.Pkg != nil }

// InScope reports whether p applies to a project. Project 0 asks about
// contributions that are global by nature (agents, themes, palette).
func (p *Plugin) InScope(projectID int64) bool {
	if len(p.ProjectIDs) == 0 || projectID == 0 {
		return true
	}
	for _, id := range p.ProjectIDs {
		if id == projectID {
			return true
		}
	}
	return false
}

// Manager owns installed plugins.
type Manager struct {
	DB  *store.DB
	Dir string
	Log *slog.Logger
	// Version is this Lectern's version, checked against min_lectern.
	Version string

	mu       sync.Mutex
	plugins  map[string]*Plugin
	previews map[string]*Preview
	indexes  map[string]*Index
	staged   map[string]string
}

// New builds a manager over dir (normally lectern-plugins beside the
// database) and loads what is installed.
func New(db *store.DB, dir, version string, log *slog.Logger) *Manager {
	if log == nil {
		log = slog.Default()
	}
	m := &Manager{DB: db, Dir: dir, Log: log, Version: version,
		previews: map[string]*Preview{}, indexes: map[string]*Index{}, staged: map[string]string{}}
	m.Reload()
	return m
}

func (m *Manager) storePath(hash string) string { return filepath.Join(m.Dir, "store", hash) }

// Reload re-reads every plugin from the database and its content store,
// re-hashing each one.
func (m *Manager) Reload() {
	out := map[string]*Plugin{}
	rows, err := m.DB.Plugins()
	if err != nil {
		m.Log.Warn("plugins: could not read installed plugins", "err", err)
	}
	byID := map[string]*store.Plugin{}
	for _, r := range rows {
		byID[r.ID] = r
	}
	bundled, err := pluginpkg.Bundled()
	if err != nil {
		m.Log.Error("plugins: a bundled plugin is invalid", "err", err)
	}
	for _, pkg := range bundled {
		p := &Plugin{ID: pkg.Manifest.ID, Bundled: true, Pkg: pkg, Manifest: pkg.Manifest, Status: StatusActive, ProjectIDs: []int64{}}
		if r := byID[p.ID]; r != nil {
			p.Row = r
			p.ProjectIDs = projectIDs(r.ProjectIDsJSON)
			if r.Enabled == 0 {
				p.Status = StatusDisabled
			}
		}
		out[p.ID] = p
	}
	for _, r := range rows {
		if r.SourceKind == "bundled" {
			continue
		}
		out[r.ID] = m.load(r)
	}
	m.mu.Lock()
	m.plugins = out
	m.mu.Unlock()
}

// load reads and verifies one installed plugin.
func (m *Manager) load(r *store.Plugin) *Plugin {
	p := &Plugin{ID: r.ID, Row: r, ProjectIDs: projectIDs(r.ProjectIDsJSON)}
	files, err := pluginpkg.ReadDir(m.storePath(r.ContentHash))
	if err != nil || len(files) == 0 {
		p.Status, p.Problem = StatusMissing, "installed files are missing; reinstall or remove it"
		return p
	}
	if got := pluginpkg.ContentHash(files); got != r.ContentHash {
		p.Status, p.Problem = StatusModified, "installed files changed since you trusted them"
		return p
	}
	pkg, err := pluginpkg.Load(files, false)
	if err != nil {
		p.Status, p.Problem = StatusModified, err.Error()
		return p
	}
	p.Pkg, p.Manifest = pkg, pkg.Manifest
	switch {
	case r.ConsentedHash != r.ContentHash:
		p.Status, p.Problem = StatusNeedsConsent, "this version has not been consented to"
	case r.Enabled == 0:
		p.Status = StatusDisabled
	default:
		p.Status = StatusActive
	}
	return p
}

// Verify re-hashes an active plugin's files. Hooks call it before every run,
// so a file changed after load is caught before it executes.
func (m *Manager) Verify(p *Plugin) bool {
	if p.Bundled {
		return true
	}
	files, err := pluginpkg.ReadDir(m.storePath(p.Row.ContentHash))
	if err == nil && pluginpkg.ContentHash(files) == p.Row.ContentHash {
		return true
	}
	m.Log.Warn("plugins: files changed on disk; the plugin is stopped until trusted again", "plugin", p.ID)
	m.mu.Lock()
	if cur := m.plugins[p.ID]; cur != nil {
		cur.Status, cur.Problem = StatusModified, "installed files changed since you trusted them"
	}
	m.mu.Unlock()
	return false
}

// List returns every plugin, bundled first, then by id.
func (m *Manager) List() []*Plugin {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*Plugin, 0, len(m.plugins))
	for _, p := range m.plugins {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Bundled != out[j].Bundled {
			return out[i].Bundled
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// Get returns one plugin.
func (m *Manager) Get(id string) (*Plugin, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.plugins[id]
	return p, ok
}

// ActiveFor lists the active plugins that apply to a project (0 = global).
func (m *Manager) ActiveFor(projectID int64) []*Plugin {
	var out []*Plugin
	for _, p := range m.List() {
		if p.Active() && p.InScope(projectID) {
			out = append(out, p)
		}
	}
	return out
}

// ---- consent ------------------------------------------------------------------

// Preview is what a person reviews before consenting: the package, what it
// may do, and for an update what changed. Nothing of it runs.
type Preview struct {
	Hash          string                 `json:"hash"`
	Source        Source                 `json:"source"`
	Commit        string                 `json:"commit,omitempty"`
	Tree          string                 `json:"tree,omitempty"`
	Format        string                 `json:"format"`
	Skipped       []string               `json:"skipped,omitempty"`
	Manifest      *pluginpkg.Manifest    `json:"manifest"`
	Capabilities  []pluginpkg.Capability `json:"capabilities"`
	Accept        []string               `json:"accept"`
	Contributions map[string]int         `json:"contributions"`
	// Update fields: the installed version and what the new one adds.
	Installed    *store.Plugin `json:"installed,omitempty"`
	FromVersion  string        `json:"from_version,omitempty"`
	Grown        []string      `json:"grown,omitempty"`
	Unchanged    bool          `json:"unchanged,omitempty"`
	pkg          *pluginpkg.Package
	created      time.Time
	previousCaps []pluginpkg.Capability
}

// ErrUnknownPreview means the preview expired or never existed.
var ErrUnknownPreview = errors.New("that preview has expired; preview the plugin again")

// ConsentError is a consent that does not match what the preview showed.
type ConsentError struct{ Msg string }

func (e *ConsentError) Error() string { return e.Msg }

// Preview fetches a plugin and validates it without installing anything.
// The files go into the content store (they are addressed by their hash, so
// nothing refers to them until a consent does).
func (m *Manager) Preview(ctx context.Context, src Source) (*Preview, error) {
	src.Kind = strings.TrimSpace(src.Kind)
	var f *fetched
	var err error
	switch src.Kind {
	case "path":
		abs, aerr := filepath.Abs(src.Path)
		if aerr != nil || src.Path == "" {
			return nil, fmt.Errorf("a path is required")
		}
		src.Path = abs
		files, rerr := pluginpkg.ReadDir(abs)
		if rerr != nil {
			return nil, rerr
		}
		f = &fetched{Files: files}
	case "git":
		f, err = fetchGit(ctx, src.URL, src.Ref, src.Commit, src.Subdir)
	case "index":
		entry, idx, lerr := m.lookup(ctx, src.Index, src.ID)
		if lerr != nil {
			return nil, lerr
		}
		src = Source{Kind: "index", Index: idx, ID: entry.ID, URL: entry.Source, Commit: entry.Commit, Subdir: entry.Path}
		f, err = fetchGit(ctx, entry.Source, "", entry.Commit, entry.Path)
	default:
		return nil, fmt.Errorf("source kind must be path, git or index")
	}
	if err != nil {
		return nil, err
	}
	pkg, err := pluginpkg.Load(f.Files, false)
	if err != nil {
		return nil, err
	}
	man := pkg.Manifest
	if src.Kind == "index" && man.ID != src.ID && pkg.Format == "lectern" {
		return nil, fmt.Errorf("the index lists %s, but the plugin at that commit is %s", src.ID, man.ID)
	}
	if err := man.CheckVersion(m.Version); err != nil {
		return nil, err
	}
	if len(man.Contributes.Agents) > 0 {
		presets, err := sessions.CatalogFromRaw(man.Contributes.Agents)
		if err != nil {
			return nil, err
		}
		specs := make([]sessions.Spec, 0, len(presets))
		for _, p := range presets {
			specs = append(specs, p.Spec)
		}
		raw, _ := json.Marshal(specs)
		if err := sessions.ValidateSpecs(string(raw)); err != nil {
			return nil, err
		}
	}
	if err := pluginpkg.WriteDir(m.storePath(pkg.Hash), pkg.Files); err != nil {
		return nil, fmt.Errorf("could not store the plugin: %w", err)
	}
	caps := man.CapabilityList()
	pv := &Preview{Hash: pkg.Hash, Source: src, Commit: f.Commit, Tree: f.Tree, Format: pkg.Format,
		Skipped: pkg.Skipped, Manifest: man, Capabilities: caps, Accept: pluginpkg.CapabilityKeys(caps),
		Contributions: Summary(man), pkg: pkg, created: time.Now()}
	if cur, ok := m.Get(man.ID); ok {
		if cur.Bundled {
			return nil, fmt.Errorf("%s is bundled with Lectern", man.ID)
		}
		pv.Installed = cur.Row
		if cur.Manifest != nil {
			pv.FromVersion = cur.Manifest.Version
		}
		var prev []pluginpkg.Capability
		_ = json.Unmarshal([]byte(cur.Row.ConsentedCapsJSON), &prev)
		pv.previousCaps = prev
		pv.Grown = pluginpkg.Grown(prev, caps)
		pv.Unchanged = cur.Row.ConsentedHash == pkg.Hash
	}
	if pv.Accept == nil {
		pv.Accept = []string{}
	}
	m.mu.Lock()
	for k, old := range m.previews {
		if time.Since(old.created) > time.Hour {
			delete(m.previews, k)
		}
	}
	m.previews[pkg.Hash] = pv
	m.mu.Unlock()
	return pv, nil
}

// Consent installs, updates or re-trusts the previewed plugin. accept must
// be exactly the capability list the preview showed: a client cannot consent
// to one set and install another. by names the person.
func (m *Manager) Consent(hash string, accept []string, by string, projectIDs []int64) (*Plugin, error) {
	m.mu.Lock()
	pv := m.previews[hash]
	m.mu.Unlock()
	if pv == nil || time.Since(pv.created) > time.Hour {
		return nil, ErrUnknownPreview
	}
	want := append([]string{}, pv.Accept...)
	got := append([]string{}, accept...)
	sort.Strings(got)
	if strings.Join(want, "\n") != strings.Join(got, "\n") {
		return nil, &ConsentError{Msg: "the capabilities you accepted are not the ones this plugin asks for; review the preview again"}
	}
	// The stored files must still be the previewed ones.
	files, err := pluginpkg.ReadDir(m.storePath(hash))
	if err != nil || pluginpkg.ContentHash(files) != hash {
		return nil, fmt.Errorf("the previewed files changed before consent; preview again")
	}
	now := store.Now()
	row := &store.Plugin{ID: pv.Manifest.ID, Enabled: 1, ProjectIDsJSON: "[]", SecretsJSON: "{}"}
	if pv.Installed != nil {
		row = pv.Installed
	}
	row.SourceKind, row.Source, row.Ref, row.IndexName, row.Subdir = pv.Source.Kind, firstNonEmpty(pv.Source.Path, pv.Source.URL), pv.Source.Ref, pv.Source.Index, pv.Source.Subdir
	row.CommitSHA, row.TreeSHA, row.ContentHash, row.Format = pv.Commit, pv.Tree, hash, pv.Format
	row.ConsentedHash, row.ConsentedCapsJSON, row.ConsentedBy, row.ConsentedAt = hash, store.J(pv.Capabilities), by, &now
	if projectIDs != nil {
		row.ProjectIDsJSON = store.J(projectIDs)
	}
	if err := m.DB.SavePlugin(row); err != nil {
		return nil, err
	}
	m.mu.Lock()
	delete(m.previews, hash)
	m.mu.Unlock()
	m.Reload()
	p, _ := m.Get(row.ID)
	return p, nil
}

// UpdatePreview fetches the installed plugin's source again: the same path,
// the same git ref's current commit, or the commit its index lists now.
func (m *Manager) UpdatePreview(ctx context.Context, id, ref string) (*Preview, error) {
	p, ok := m.Get(id)
	if !ok || p.Row == nil || p.Bundled {
		return nil, fmt.Errorf("no installed plugin %s", id)
	}
	r := p.Row
	src := Source{Kind: r.SourceKind, Ref: r.Ref, Subdir: r.Subdir, Index: r.IndexName, ID: id}
	switch r.SourceKind {
	case "path":
		src.Path = r.Source
	case "git":
		src.URL = r.Source
		if ref != "" {
			src.Ref = ref
		}
	case "index":
	}
	return m.Preview(ctx, src)
}

// TrustPreview reads what is on disk now for a modified plugin and previews
// it, so a person can see what changed before trusting it.
func (m *Manager) TrustPreview(id string) (*Preview, error) {
	p, ok := m.Get(id)
	if !ok || p.Row == nil || p.Bundled {
		return nil, fmt.Errorf("no installed plugin %s", id)
	}
	src := Source{Kind: "path", Path: m.storePath(p.Row.ContentHash)}
	pv, err := m.Preview(context.Background(), src)
	if err != nil {
		return nil, err
	}
	pv.Source = Source{Kind: p.Row.SourceKind, Path: p.Row.Source, URL: p.Row.Source, Ref: p.Row.Ref, Subdir: p.Row.Subdir, Index: p.Row.IndexName}
	if p.Row.SourceKind != "path" {
		pv.Source.Path = ""
	} else {
		pv.Source.URL = ""
	}
	pv.Commit, pv.Tree = p.Row.CommitSHA, p.Row.TreeSHA
	return pv, nil
}

// SetEnabled switches a plugin on or off; scope, when non-nil, sets the
// projects it applies to (empty = everywhere).
func (m *Manager) SetEnabled(id string, enabled *bool, scope []int64) (*Plugin, error) {
	p, ok := m.Get(id)
	if !ok {
		return nil, fmt.Errorf("no plugin %s", id)
	}
	row := p.Row
	if row == nil { // a bundled plugin nobody changed yet
		row = &store.Plugin{ID: id, SourceKind: "bundled", Source: "bundled", Enabled: 1, ProjectIDsJSON: "[]", SecretsJSON: "{}"}
	}
	if enabled != nil {
		if *enabled && !p.Bundled && row.ConsentedHash != row.ContentHash {
			return nil, &ConsentError{Msg: "consent to this version before enabling it"}
		}
		row.Enabled = map[bool]int{true: 1, false: 0}[*enabled]
	}
	if scope != nil {
		row.ProjectIDsJSON = store.J(scope)
	}
	if err := m.DB.SavePlugin(row); err != nil {
		return nil, err
	}
	m.Reload()
	p, _ = m.Get(id)
	return p, nil
}

// Remove uninstalls a plugin and forgets its consent and secrets. Its files
// stay in the content store until Prune, since another install may share them.
func (m *Manager) Remove(id string) error {
	p, ok := m.Get(id)
	if !ok || p.Row == nil && !p.Bundled {
		return store.ErrNotFound
	}
	if p.Bundled {
		return fmt.Errorf("%s is bundled with Lectern; disable it instead", id)
	}
	if err := m.DB.DeletePlugin(id); err != nil {
		return err
	}
	m.Reload()
	m.Prune()
	return nil
}

// Prune deletes content-store directories no installed plugin refers to.
func (m *Manager) Prune() {
	keep := map[string]bool{}
	for _, p := range m.List() {
		if p.Row != nil {
			keep[p.Row.ContentHash] = true
		}
	}
	m.mu.Lock()
	for h := range m.previews {
		keep[h] = true
	}
	m.mu.Unlock()
	entries, _ := os.ReadDir(filepath.Join(m.Dir, "store"))
	for _, e := range entries {
		if e.IsDir() && !keep[e.Name()] && len(e.Name()) == 64 {
			dir := m.storePath(e.Name())
			_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
				if err == nil && d.IsDir() {
					_ = os.Chmod(p, 0o755)
				}
				return nil
			})
			_ = os.RemoveAll(dir)
		}
	}
}

// SetSecrets stores values for the plugin's declared secrets. An empty
// value removes one. Undeclared names are refused.
func (m *Manager) SetSecrets(id string, values map[string]string) error {
	p, ok := m.Get(id)
	if !ok || p.Row == nil || p.Manifest == nil {
		return fmt.Errorf("no installed plugin %s", id)
	}
	cur := map[string]string{}
	_ = json.Unmarshal([]byte(p.Row.SecretsJSON), &cur)
	for k, v := range values {
		declared := false
		for _, s := range p.Manifest.Capabilities.Secrets {
			declared = declared || s == k
		}
		if !declared {
			return fmt.Errorf("%s does not declare a secret named %s", id, k)
		}
		if v == "" {
			delete(cur, k)
		} else {
			cur[k] = v
		}
	}
	p.Row.SecretsJSON = store.J(cur)
	if err := m.DB.SavePlugin(p.Row); err != nil {
		return err
	}
	m.Reload()
	return nil
}

// SecretNames lists which declared secrets have a value.
func (p *Plugin) SecretNames() []string {
	out := []string{}
	if p.Row == nil {
		return out
	}
	cur := map[string]string{}
	_ = json.Unmarshal([]byte(p.Row.SecretsJSON), &cur)
	for k := range cur {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func (p *Plugin) secrets() map[string]string {
	cur := map[string]string{}
	if p.Row != nil {
		_ = json.Unmarshal([]byte(p.Row.SecretsJSON), &cur)
	}
	return cur
}

// Summary counts contributions by kind, for list views.
func Summary(man *pluginpkg.Manifest) map[string]int {
	c := man.Contributes
	out := map[string]int{}
	for k, n := range map[string]int{
		"agents": len(c.Agents), "mcp_servers": len(c.MCPServers), "skills": len(c.Skills),
		"workflows": len(c.Workflows), "hooks": len(c.Hooks), "sandbox_providers": len(c.SandboxProviders),
		"quick_commands": len(c.QuickCommands), "themes": len(c.Themes), "palette_commands": len(c.PaletteCommands),
	} {
		if n > 0 {
			out[k] = n
		}
	}
	return out
}

func projectIDs(raw string) []int64 {
	out := []int64{}
	_ = json.Unmarshal([]byte(raw), &out)
	return out
}

func firstNonEmpty(xs ...string) string {
	for _, x := range xs {
		if x != "" {
			return x
		}
	}
	return ""
}
