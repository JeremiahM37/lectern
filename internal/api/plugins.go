package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/agents"
	"github.com/JeremiahM37/lectern/v2/internal/auth"
	"github.com/JeremiahM37/lectern/v2/internal/plugins"
	"github.com/JeremiahM37/lectern/v2/internal/sessions"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// Plugins (docs/plugins.md). Reading is open to any caller the API admits;
// everything that installs, consents, enables, scopes, stores a secret or
// adds a source needs a signed-in person, like deciding an approval — a
// plugin can run commands on this server and on every machine, so a process
// here, the MCP server or an agent must never be able to bring one in.

func (s *Server) pluginRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/plugins", s.listPlugins)
	mux.HandleFunc("GET /api/plugins/contributions", s.pluginContributions)
	mux.HandleFunc("GET /api/plugins/search", s.searchPlugins)
	mux.HandleFunc("POST /api/plugins/preview", s.previewPlugin)
	mux.HandleFunc("POST /api/plugins/install", s.installPlugin)
	mux.HandleFunc("GET /api/plugins/{id}", s.getPlugin)
	mux.HandleFunc("PUT /api/plugins/{id}", s.putPlugin)
	mux.HandleFunc("DELETE /api/plugins/{id}", s.deletePlugin)
	mux.HandleFunc("POST /api/plugins/{id}/update", s.updatePlugin)
	mux.HandleFunc("POST /api/plugins/{id}/trust", s.trustPlugin)
	mux.HandleFunc("PUT /api/plugins/{id}/secrets", s.putPluginSecrets)
	mux.HandleFunc("GET /api/plugin-sources", s.listPluginSources)
	mux.HandleFunc("POST /api/plugin-sources", s.addPluginSource)
	mux.HandleFunc("DELETE /api/plugin-sources/{name}", s.deletePluginSource)
}

func (s *Server) pluginsReady(w http.ResponseWriter) bool {
	if s.Plugins == nil {
		httpError(w, 503, "plugins are not available on this server")
		return false
	}
	return true
}

// pluginPerson gates a plugin change and names who made it.
func (s *Server) pluginPerson(w http.ResponseWriter, r *http.Request, what string) (string, bool) {
	if !s.pluginsReady(w) || !s.requireHuman(w, r, what) {
		return "", false
	}
	p, _ := auth.FromContext(r.Context())
	return firstNonEmptyStr(p.Login, p.Kind, "operator"), true
}

func pluginView(p *plugins.Plugin) map[string]any {
	v := map[string]any{"id": p.ID, "bundled": p.Bundled, "status": p.Status, "problem": p.Problem,
		"enabled": p.Status != plugins.StatusDisabled, "project_ids": p.ProjectIDs, "secrets_set": p.SecretNames()}
	if m := p.Manifest; m != nil {
		v["name"], v["version"], v["description"] = m.Name, m.Version, m.Description
		v["author"], v["homepage"], v["license"] = m.Author, m.Homepage, m.License
		v["contributions"] = plugins.Summary(m)
		v["capabilities"] = m.CapabilityList()
		v["secrets"] = m.Capabilities.Secrets
	} else {
		v["name"] = p.ID
	}
	if r := p.Row; r != nil && !p.Bundled {
		v["source"] = map[string]any{"kind": r.SourceKind, "source": r.Source, "ref": r.Ref, "index": r.IndexName,
			"subdir": r.Subdir, "commit": r.CommitSHA, "tree": r.TreeSHA}
		v["content_hash"], v["format"] = r.ContentHash, r.Format
		v["consented_by"], v["consented_at"] = r.ConsentedBy, r.ConsentedAt
	} else {
		v["source"] = map[string]any{"kind": "bundled"}
	}
	return v
}

// listPlugins is the Plugins page: installed plugins, plus the extension
// points configured outside any plugin (custom agents, project MCP servers),
// listed as local contributions so everything that extends Lectern is in
// one place.
func (s *Server) listPlugins(w http.ResponseWriter, r *http.Request) {
	if !s.pluginsReady(w) {
		return
	}
	out := []map[string]any{}
	for _, p := range s.Plugins.List() {
		out = append(out, pluginView(p))
	}
	localAgents := []string{}
	for _, spec := range sessions.ParseSpecs(s.DB.Setting("agents")) {
		if !spec.Builtin {
			localAgents = append(localAgents, spec.Name)
		}
	}
	localMCP := []map[string]any{}
	if projects, err := s.DB.Projects(); err == nil {
		for _, p := range projects {
			names := []string{}
			for name := range agentsServerNames(p.MCPJSON) {
				names = append(names, name)
			}
			if len(names) == 0 {
				continue
			}
			sort.Strings(names)
			localMCP = append(localMCP, map[string]any{"project_id": p.ID, "project": p.Name, "servers": names})
		}
	}
	writeJSON(w, 200, map[string]any{"plugins": out, "local": map[string]any{"agents": localAgents, "mcp_servers": localMCP}})
}

func (s *Server) pluginContributions(w http.ResponseWriter, r *http.Request) {
	if s.Plugins == nil {
		writeJSON(w, 200, plugins.UIContributions{QuickCommands: []plugins.UIQuickCommand{}, Themes: []plugins.UITheme{}, PaletteCommands: []plugins.UIPaletteCommand{}})
		return
	}
	writeJSON(w, 200, s.Plugins.UI())
}

func (s *Server) getPlugin(w http.ResponseWriter, r *http.Request) {
	if !s.pluginsReady(w) {
		return
	}
	p, ok := s.Plugins.Get(r.PathValue("id"))
	if !ok {
		httpError(w, 404, "no such plugin")
		return
	}
	v := pluginView(p)
	if p.Manifest != nil {
		c := p.Manifest.Contributes
		agents := []string{}
		for _, a := range c.Agents {
			var x struct {
				Name string `json:"name"`
			}
			_ = json.Unmarshal(a, &x)
			agents = append(agents, x.Name)
		}
		mcp := []string{}
		for name := range c.MCPServers {
			mcp = append(mcp, name)
		}
		sort.Strings(mcp)
		if len(agents) > 40 {
			v["agents_more"] = len(agents) - 40
			agents = agents[:40]
		}
		v["detail"] = map[string]any{"agents": agents, "mcp_servers": mcp, "skills": c.Skills, "workflows": c.Workflows,
			"hooks": c.Hooks, "sandbox_providers": c.SandboxProviders, "quick_commands": c.QuickCommands,
			"themes": c.Themes, "palette_commands": c.PaletteCommands}
	}
	runs, _ := s.DB.PluginHookRuns(p.ID, 20)
	v["hook_runs"] = runs
	writeJSON(w, 200, v)
}

type pluginSourceIn struct {
	Kind   string `json:"kind"`
	Path   string `json:"path"`
	URL    string `json:"url"`
	Ref    string `json:"ref"`
	Commit string `json:"commit"`
	Subdir string `json:"subdir"`
	Index  string `json:"index"`
	ID     string `json:"id"`
}

func (s *Server) previewPlugin(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.pluginPerson(w, r, "previewing a plugin"); !ok {
		return
	}
	var in pluginSourceIn
	if err := decodeBody(r, &in); err != nil {
		httpError(w, 422, "%s", err)
		return
	}
	pv, err := s.Plugins.Preview(r.Context(), plugins.Source(in))
	if err != nil {
		httpError(w, 422, "%s", err)
		return
	}
	writeJSON(w, 200, pv)
}

func (s *Server) installPlugin(w http.ResponseWriter, r *http.Request) {
	by, ok := s.pluginPerson(w, r, "installing a plugin")
	if !ok {
		return
	}
	var in struct {
		Hash       string   `json:"hash"`
		Accept     []string `json:"accept"`
		ProjectIDs []int64  `json:"project_ids"`
	}
	if err := decodeBody(r, &in); err != nil {
		httpError(w, 422, "%s", err)
		return
	}
	if in.Accept == nil {
		httpError(w, 422, "accept is required: the capability list you reviewed")
		return
	}
	p, err := s.Plugins.Consent(in.Hash, in.Accept, by, in.ProjectIDs)
	var ce *plugins.ConsentError
	switch {
	case errors.Is(err, plugins.ErrUnknownPreview), errors.As(err, &ce):
		httpError(w, 409, "%s", err)
		return
	case err != nil:
		respondErr(w, err)
		return
	}
	s.Bus.Publish("board", "plugins", map[string]any{"id": p.ID})
	writeJSON(w, 200, pluginView(p))
}

func (s *Server) updatePlugin(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.pluginPerson(w, r, "updating a plugin"); !ok {
		return
	}
	var in struct {
		Ref string `json:"ref"`
	}
	_ = decodeBody(r, &in)
	pv, err := s.Plugins.UpdatePreview(r.Context(), r.PathValue("id"), in.Ref)
	if err != nil {
		httpError(w, 422, "%s", err)
		return
	}
	writeJSON(w, 200, pv)
}

func (s *Server) trustPlugin(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.pluginPerson(w, r, "trusting a plugin"); !ok {
		return
	}
	pv, err := s.Plugins.TrustPreview(r.PathValue("id"))
	if err != nil {
		httpError(w, 422, "%s", err)
		return
	}
	writeJSON(w, 200, pv)
}

func (s *Server) putPlugin(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.pluginPerson(w, r, "changing a plugin"); !ok {
		return
	}
	var in struct {
		Enabled    *bool    `json:"enabled"`
		ProjectIDs *[]int64 `json:"project_ids"`
	}
	if err := decodeBody(r, &in); err != nil {
		httpError(w, 422, "%s", err)
		return
	}
	var scope []int64
	if in.ProjectIDs != nil {
		scope = append([]int64{}, (*in.ProjectIDs)...)
		for _, id := range scope {
			if _, err := s.DB.Project(id); err != nil {
				httpError(w, 422, "no project %d", id)
				return
			}
		}
	}
	p, err := s.Plugins.SetEnabled(r.PathValue("id"), in.Enabled, scope)
	var ce *plugins.ConsentError
	if errors.As(err, &ce) {
		httpError(w, 409, "%s", err)
		return
	}
	if err != nil {
		httpError(w, 404, "%s", err)
		return
	}
	s.Bus.Publish("board", "plugins", map[string]any{"id": p.ID})
	writeJSON(w, 200, pluginView(p))
}

func (s *Server) deletePlugin(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.pluginPerson(w, r, "removing a plugin"); !ok {
		return
	}
	id := r.PathValue("id")
	if err := s.Plugins.Remove(id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpError(w, 404, "no such plugin")
		} else {
			httpError(w, 409, "%s", err)
		}
		return
	}
	s.Bus.Publish("board", "plugins", map[string]any{"id": id})
	writeJSON(w, 200, map[string]any{"removed": id})
}

func (s *Server) putPluginSecrets(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.pluginPerson(w, r, "setting a plugin secret"); !ok {
		return
	}
	var in map[string]string
	if err := decodeBody(r, &in); err != nil {
		httpError(w, 422, "%s", err)
		return
	}
	if err := s.Plugins.SetSecrets(r.PathValue("id"), in); err != nil {
		httpError(w, 422, "%s", err)
		return
	}
	p, _ := s.Plugins.Get(r.PathValue("id"))
	writeJSON(w, 200, map[string]any{"secrets_set": p.SecretNames()})
}

func (s *Server) searchPlugins(w http.ResponseWriter, r *http.Request) {
	if !s.pluginsReady(w) {
		return
	}
	found, problems, err := s.Plugins.Search(r.Context(), r.URL.Query().Get("q"), r.URL.Query().Get("refresh") == "1")
	if err != nil {
		respondErr(w, err)
		return
	}
	if found == nil {
		found = []plugins.Listing{}
	}
	if problems == nil {
		problems = []string{}
	}
	writeJSON(w, 200, map[string]any{"plugins": found, "problems": problems})
}

func (s *Server) listPluginSources(w http.ResponseWriter, r *http.Request) {
	if !s.pluginsReady(w) {
		return
	}
	out, err := s.Plugins.Sources()
	if err != nil {
		respondErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"sources": out})
}

func (s *Server) addPluginSource(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.pluginPerson(w, r, "adding a plugin source"); !ok {
		return
	}
	var in struct {
		Name string `json:"name"`
		URL  string `json:"url"`
		Ref  string `json:"ref"`
	}
	if err := decodeBody(r, &in); err != nil {
		httpError(w, 422, "%s", err)
		return
	}
	idx, err := s.Plugins.AddSource(r.Context(), strings.TrimSpace(in.Name), strings.TrimSpace(in.URL), strings.TrimSpace(in.Ref))
	if err != nil {
		httpError(w, 422, "%s", err)
		return
	}
	writeJSON(w, 200, map[string]any{"name": in.Name, "plugins": len(idx.Plugins), "commit": idx.Commit})
}

func (s *Server) deletePluginSource(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.pluginPerson(w, r, "removing a plugin source"); !ok {
		return
	}
	if err := s.Plugins.RemoveSource(r.PathValue("name")); err != nil {
		respondErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"removed": r.PathValue("name")})
}

// pluginSandboxHooks reads a plugin sandbox provider's file for Settings.
func (s *Server) pluginSandboxHooks(ref string) ([]byte, error) {
	if s.Plugins == nil {
		return nil, errors.New("plugins are not available")
	}
	return s.Plugins.SandboxProviderFile(ref)
}

func agentsServerNames(raw string) map[string]bool {
	return agents.ProjectServerNames(store.UnjObj(raw))
}
