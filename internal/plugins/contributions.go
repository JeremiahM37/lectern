package plugins

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/executor"
	"github.com/JeremiahM37/lectern/v2/internal/pluginpkg"
	"github.com/JeremiahM37/lectern/v2/internal/sessions"
	"github.com/JeremiahM37/lectern/v2/internal/workflows"
)

// AgentPreset is a catalog entry and the plugin it came from.
type AgentPreset struct {
	sessions.CatalogPreset
	Plugin string `json:"plugin"`
}

// AgentPresets lists catalog entries from active installed plugins. The
// bundled catalog is sessions.Catalog(); a disabled bundled catalog plugin
// hides it (Catalog reports whether it is active).
func (m *Manager) AgentPresets() []AgentPreset {
	var out []AgentPreset
	for _, p := range m.ActiveFor(0) {
		if p.Bundled || len(p.Manifest.Contributes.Agents) == 0 {
			continue
		}
		presets, err := sessions.CatalogFromRaw(p.Manifest.Contributes.Agents)
		if err != nil {
			continue
		}
		for _, preset := range presets {
			out = append(out, AgentPreset{CatalogPreset: preset, Plugin: p.ID})
		}
	}
	return out
}

// BundledCatalogActive reports whether the bundled agent catalog plugin is on.
func (m *Manager) BundledCatalogActive() bool {
	p, ok := m.Get(sessions.CatalogPluginID)
	return ok && p.Active()
}

// Workflows lists the workflows and skills active plugins offer a project.
func (m *Manager) Workflows(projectID int64) []workflows.Source {
	var out []workflows.Source
	for _, p := range m.ActiveFor(projectID) {
		srcs, err := workflows.FromPlugin(p.Pkg)
		if err != nil {
			m.Log.Warn("plugins: workflows unavailable", "plugin", p.ID, "err", err)
			continue
		}
		out = append(out, srcs...)
	}
	return out
}

// AllWorkflows lists every plugin's workflows whatever its state, so an
// attachment made while a plugin was on can still be found and turned off.
func (m *Manager) AllWorkflows() []workflows.Source {
	var out []workflows.Source
	for _, p := range m.List() {
		if p.Pkg == nil {
			continue
		}
		if srcs, err := workflows.FromPlugin(p.Pkg); err == nil {
			out = append(out, srcs...)
		}
	}
	return out
}

var (
	rootRef   = regexp.MustCompile(`\$\{LECTERN_PLUGIN_ROOT\}`)
	secretRef = regexp.MustCompile(`\$\{secret:([A-Za-z0-9_]+)\}`)
)

// MCPServers returns the MCP servers active plugins add to a launch on a
// project's target, keyed by server name. A server that refers to
// ${LECTERN_PLUGIN_ROOT} gets the plugin copied onto the target first. taken
// holds names the project already uses: the project's own server wins, and
// between two plugins the first by id does.
func (m *Manager) MCPServers(ctx context.Context, ex executor.Executor, targetID, projectID int64, taken map[string]bool) (map[string]any, error) {
	out := map[string]any{}
	for _, p := range m.ActiveFor(projectID) {
		servers := p.Manifest.Contributes.MCPServers
		if len(servers) == 0 {
			continue
		}
		if !m.Verify(p) {
			continue
		}
		secrets := p.secrets()
		root := ""
		for _, name := range sortedNames(servers) {
			if taken[name] || out[name] != nil {
				continue
			}
			raw, _ := json.Marshal(servers[name])
			text := string(raw)
			if rootRef.MatchString(text) {
				if root == "" {
					var err error
					if root, err = m.StageOnTarget(ctx, ex, targetID, p); err != nil {
						return nil, fmt.Errorf("plugin %s: %w", p.ID, err)
					}
				}
				rootJSON, _ := json.Marshal(root)
				text = rootRef.ReplaceAllString(text, strings.Trim(string(rootJSON), `"`))
			}
			missing := ""
			text = secretRef.ReplaceAllStringFunc(text, func(ref string) string {
				k := secretRef.FindStringSubmatch(ref)[1]
				v, ok := secrets[k]
				if !ok {
					missing = k
				}
				enc, _ := json.Marshal(v)
				return strings.Trim(string(enc), `"`)
			})
			if missing != "" {
				m.Log.Warn("plugins: MCP server skipped until its secret is set", "plugin", p.ID, "server", name, "secret", missing)
				continue
			}
			var cfg map[string]any
			if err := json.Unmarshal([]byte(text), &cfg); err != nil {
				continue
			}
			out[name] = cfg
		}
	}
	return out, nil
}

// StageOnTarget copies an active plugin onto a target, into
// ~/.lectern/plugins/<id>/<hash>, and returns that path. The copy is
// verified file by file against the consented content, never replaced.
func (m *Manager) StageOnTarget(ctx context.Context, ex executor.Executor, targetID int64, p *Plugin) (string, error) {
	key := fmt.Sprintf("%d/%s", targetID, p.Pkg.Hash)
	m.mu.Lock()
	root, ok := m.staged[key]
	m.mu.Unlock()
	if ok {
		return root, nil
	}
	home, err := workflows.TargetHome(ctx, ex)
	if err != nil {
		return "", err
	}
	root = path.Join(home, ".lectern", "plugins", p.ID, p.Pkg.Hash[:16])
	var files []workflows.File
	for _, f := range p.Pkg.Files {
		files = append(files, workflows.File{Path: strings.Split(f.Path, "/"), Data: f.Data, Mode: f.Mode})
	}
	if err := workflows.StageFiles(ctx, ex, root, files); err != nil {
		return "", err
	}
	m.mu.Lock()
	m.staged[key] = root
	m.mu.Unlock()
	return root, nil
}

// UIContributions are what the browser shows: quick commands (with the
// projects they apply to), themes and palette commands.
type UIContributions struct {
	QuickCommands   []UIQuickCommand   `json:"quick_commands"`
	Themes          []UITheme          `json:"themes"`
	PaletteCommands []UIPaletteCommand `json:"palette_commands"`
}

type UIQuickCommand struct {
	ID         string  `json:"id"`
	Plugin     string  `json:"plugin"`
	Label      string  `json:"label"`
	Text       string  `json:"text"`
	Enter      bool    `json:"enter"`
	ProjectIDs []int64 `json:"project_ids"`
}

type UITheme struct {
	ID     string            `json:"id"`
	Plugin string            `json:"plugin"`
	Name   string            `json:"name"`
	Accent string            `json:"accent,omitempty"`
	Dark   map[string]string `json:"dark,omitempty"`
	Light  map[string]string `json:"light,omitempty"`
}

type UIPaletteCommand struct {
	ID     string `json:"id"`
	Plugin string `json:"plugin"`
	Title  string `json:"title"`
	Href   string `json:"href"`
}

// UI collects the browser-side contributions of every active plugin. Ids
// are "<plugin>/<id>", so two plugins cannot collide.
func (m *Manager) UI() UIContributions {
	out := UIContributions{QuickCommands: []UIQuickCommand{}, Themes: []UITheme{}, PaletteCommands: []UIPaletteCommand{}}
	for _, p := range m.ActiveFor(0) {
		c := p.Manifest.Contributes
		for _, q := range c.QuickCommands {
			out.QuickCommands = append(out.QuickCommands, UIQuickCommand{ID: p.ID + "/" + q.ID, Plugin: p.ID,
				Label: q.Label, Text: q.Text, Enter: q.Enter == nil || *q.Enter, ProjectIDs: p.ProjectIDs})
		}
		for _, t := range c.Themes {
			out.Themes = append(out.Themes, UITheme{ID: p.ID + "/" + t.ID, Plugin: p.ID, Name: t.Name,
				Accent: t.Accent, Dark: t.Dark, Light: t.Light})
		}
		for _, pc := range c.PaletteCommands {
			out.PaletteCommands = append(out.PaletteCommands, UIPaletteCommand{ID: p.ID + "/" + pc.ID, Plugin: p.ID,
				Title: pc.Title, Href: pc.Href})
		}
	}
	return out
}

// SandboxProviderFile returns a plugin sandbox provider's hooks file, for a
// machine whose script provider names "plugin:<id>/<provider>". The bytes
// are the consented ones, so no separate trust is needed.
func (m *Manager) SandboxProviderFile(ref string) ([]byte, error) {
	rest, ok := strings.CutPrefix(ref, "plugin:")
	if !ok {
		return nil, fmt.Errorf("%q is not a plugin sandbox provider", ref)
	}
	id, provider, ok := strings.Cut(rest, "/")
	if !ok {
		return nil, fmt.Errorf("%q must be plugin:<id>/<provider>", ref)
	}
	p, found := m.Get(id)
	if !found || !p.Active() {
		return nil, fmt.Errorf("plugin %s is not installed and enabled", id)
	}
	if !m.Verify(p) {
		return nil, fmt.Errorf("plugin %s changed on disk; trust it again in Settings → Plugins", id)
	}
	for _, sp := range p.Manifest.Contributes.SandboxProviders {
		if sp.ID == provider {
			if f, ok := pluginpkg.FileMap(p.Pkg.Files)[path.Clean(sp.Path)]; ok {
				return f.Data, nil
			}
		}
	}
	return nil, fmt.Errorf("plugin %s has no sandbox provider %s", id, provider)
}

func sortedNames(m map[string]map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// MCPServerNames lists the servers active plugins would add to a project,
// without staging anything — for the launch preview.
func (m *Manager) MCPServerNames(projectID int64) []string {
	var out []string
	for _, p := range m.ActiveFor(projectID) {
		for name := range p.Manifest.Contributes.MCPServers {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}
