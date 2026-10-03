// Package pluginpkg is the plugin package format: the lectern-plugin.yaml
// manifest, its validation, the content hash consent is pinned to, the
// capability list a person reviews, and the plugins bundled into the binary.
// It is a leaf package — the agent catalog and the workflows read their
// bundled plugins through it — and it never runs anything. Installing,
// consent and hooks live in internal/plugins. See docs/plugins.md.
package pluginpkg

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// ManifestName is the file that makes a directory a Lectern plugin.
const ManifestName = "lectern-plugin.yaml"

// MaxManifestBytes bounds the manifest itself; the agent catalog, the largest
// real one, is under 60 KiB.
const MaxManifestBytes = 1 << 20

// Manifest is lectern-plugin.yaml.
type Manifest struct {
	ID           string        `json:"id" yaml:"id"`
	Name         string        `json:"name" yaml:"name"`
	Version      string        `json:"version" yaml:"version"`
	Description  string        `json:"description,omitempty" yaml:"description,omitempty"`
	Author       string        `json:"author,omitempty" yaml:"author,omitempty"`
	Homepage     string        `json:"homepage,omitempty" yaml:"homepage,omitempty"`
	License      string        `json:"license,omitempty" yaml:"license,omitempty"`
	MinLectern   string        `json:"min_lectern,omitempty" yaml:"min_lectern,omitempty"`
	Capabilities Capabilities  `json:"capabilities" yaml:"capabilities"`
	Contributes  Contributions `json:"contributes" yaml:"contributes"`
}

// Capabilities is what a plugin may do. Every contribution that needs one is
// checked against this list (Validate), so it is the enforced list, not a
// description the author could get wrong.
type Capabilities struct {
	// HostExec: hooks or sandbox providers run commands on the Lectern server.
	HostExec bool `json:"host_exec,omitempty" yaml:"host_exec,omitempty"`
	// TargetExec: hooks run on machines, or MCP servers start a program there.
	TargetExec bool `json:"target_exec,omitempty" yaml:"target_exec,omitempty"`
	// Network lists the hosts the plugin says it contacts. Declared for the
	// reader; Lectern cannot enforce it on a command it runs.
	Network []string `json:"network,omitempty" yaml:"network,omitempty"`
	// Secrets are names of values a person sets for this plugin, handed only
	// to its hooks and MCP servers.
	Secrets []string `json:"secrets,omitempty" yaml:"secrets,omitempty"`
	// MCPTools: adds MCP servers whose tools agents can call.
	MCPTools bool `json:"mcp_tools,omitempty" yaml:"mcp_tools,omitempty"`
	// Agents: adds agent presets to the catalog.
	Agents bool `json:"agents,omitempty" yaml:"agents,omitempty"`
	// Notify: a hook's result may send a notification.
	Notify bool `json:"notify,omitempty" yaml:"notify,omitempty"`
	// Mods lists where the plugin's mods run code: "web" (the browser) and
	// "cli" (the terminal console). docs/mods.md.
	Mods []string `json:"mods,omitempty" yaml:"mods,omitempty"`
	// API is what a mod may do through $.api: "read" or "write".
	API string `json:"api,omitempty" yaml:"api,omitempty"`
}

// Contributions are the extension points a plugin adds to.
type Contributions struct {
	// Agents are sessions.CatalogPreset objects; kept raw here because that
	// type lives above this package. internal/plugins validates them fully.
	Agents           AgentList                 `json:"agents,omitempty" yaml:"agents,omitempty"`
	MCPServers       map[string]map[string]any `json:"mcp_servers,omitempty" yaml:"mcp_servers,omitempty"`
	Skills           []Skill                   `json:"skills,omitempty" yaml:"skills,omitempty"`
	Workflows        []Workflow                `json:"workflows,omitempty" yaml:"workflows,omitempty"`
	Hooks            []Hook                    `json:"hooks,omitempty" yaml:"hooks,omitempty"`
	SandboxProviders []SandboxProvider         `json:"sandbox_providers,omitempty" yaml:"sandbox_providers,omitempty"`
	QuickCommands    []QuickCommand            `json:"quick_commands,omitempty" yaml:"quick_commands,omitempty"`
	Themes           []Theme                   `json:"themes,omitempty" yaml:"themes,omitempty"`
	PaletteCommands  []PaletteCommand          `json:"palette_commands,omitempty" yaml:"palette_commands,omitempty"`
	Mods             []Mod                     `json:"mods,omitempty" yaml:"mods,omitempty"`
}

// Mod is a JavaScript module that hooks the web app or the console
// (docs/mods.md).
type Mod struct {
	ID       string   `json:"id" yaml:"id"`
	Path     string   `json:"path" yaml:"path"`
	Surfaces []string `json:"surfaces,omitempty" yaml:"surfaces,omitempty"`
}

// ModSurfaces are where a mod can run.
var ModSurfaces = []string{"web", "cli"}

// MaxModBytes bounds one mod's source.
const MaxModBytes = 256 << 10

// SurfacesOf is where a mod runs: its own list, or every surface.
func (m Mod) SurfacesOf() []string {
	if len(m.Surfaces) == 0 {
		return append([]string(nil), ModSurfaces...)
	}
	return m.Surfaces
}

// Skill is a directory with SKILL.md that a project can turn on per agent.
type Skill struct {
	ID          string `json:"id" yaml:"id"`
	Name        string `json:"name,omitempty" yaml:"name,omitempty"`
	Description string `json:"description,omitempty" yaml:"description,omitempty"`
	Path        string `json:"path" yaml:"path"`
	// Entry is the skill's directory name in the project (.claude/skills/<entry>).
	Entry string `json:"entry,omitempty" yaml:"entry,omitempty"`
}

// Workflow is a skill with commands, shown in Project workflows.
type Workflow struct {
	ID          string   `json:"id" yaml:"id"`
	Name        string   `json:"name" yaml:"name"`
	Description string   `json:"description,omitempty" yaml:"description,omitempty"`
	Path        string   `json:"path" yaml:"path"`
	Entry       string   `json:"entry,omitempty" yaml:"entry,omitempty"`
	Version     string   `json:"version,omitempty" yaml:"version,omitempty"`
	UpstreamURL string   `json:"upstream_url,omitempty" yaml:"upstream_url,omitempty"`
	Commands    []string `json:"commands,omitempty" yaml:"commands,omitempty"`
}

// Hook runs a command on a lifecycle event.
type Hook struct {
	Event   string   `json:"event" yaml:"event"`
	Run     string   `json:"run" yaml:"run"`
	Command []string `json:"command" yaml:"command"`
	Timeout int      `json:"timeout,omitempty" yaml:"timeout,omitempty"`
}

// SandboxProvider is a lectern.sandbox.yaml a sandbox machine can use.
type SandboxProvider struct {
	ID          string `json:"id" yaml:"id"`
	Name        string `json:"name" yaml:"name"`
	Description string `json:"description,omitempty" yaml:"description,omitempty"`
	Path        string `json:"path" yaml:"path"`
}

// QuickCommand is text a person sends to a terminal with one tap.
type QuickCommand struct {
	ID    string `json:"id" yaml:"id"`
	Label string `json:"label" yaml:"label"`
	Text  string `json:"text" yaml:"text"`
	Enter *bool  `json:"enter,omitempty" yaml:"enter,omitempty"`
}

// Theme is a named set of colour tokens for Settings → Appearance.
type Theme struct {
	ID     string            `json:"id" yaml:"id"`
	Name   string            `json:"name" yaml:"name"`
	Accent string            `json:"accent,omitempty" yaml:"accent,omitempty"`
	Dark   map[string]string `json:"dark,omitempty" yaml:"dark,omitempty"`
	Light  map[string]string `json:"light,omitempty" yaml:"light,omitempty"`
}

// PaletteCommand opens a Lectern view ("#…") or an https link.
type PaletteCommand struct {
	ID    string `json:"id" yaml:"id"`
	Title string `json:"title" yaml:"title"`
	Href  string `json:"href" yaml:"href"`
}

// Hook events, in the order docs/plugins.md lists them.
var Events = []string{
	"session.start", "session.end", "task.dispatched", "task.finished",
	"approval.requested", "approval.decided", "ci.failed", "limit.hit",
}

// ThemeTokens are the colour tokens a theme may set (frontend app-theme.ts).
var ThemeTokens = []string{
	"bg", "bg-soft", "panel", "panel-2", "line", "line-2", "ink", "ink-dim", "ink-faint",
	"accent", "accent-soft", "accent-fill", "indigo", "cyan", "blue", "amber", "red", "green",
}

var (
	idRe        = regexp.MustCompile(`^([a-z0-9][a-z0-9-]{0,39}\.)?[a-z0-9][a-z0-9-]{0,62}$`)
	partRe      = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	agentNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}$`)
	mcpNameRe   = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	secretRe    = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)
	hostRe      = regexp.MustCompile(`^(\*\.)?[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)*(:[0-9]{1,5})?$`)
	colorRe     = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)
	versionRe   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.+_-]{0,63}$`)
	secretRef   = regexp.MustCompile(`\$\{secret:([A-Za-z0-9_]+)\}`)
)

// ReservedPrefix is the publisher only the binary's own plugins may use.
const ReservedPrefix = "lectern."

// ParseManifest decodes lectern-plugin.yaml strictly: an unknown field is an
// error, so a typo in a capability name cannot silently grant nothing. A
// scalar read into a text field keeps its text, so `version: 1.0` is "1.0".
func ParseManifest(data []byte) (*Manifest, error) {
	if len(data) > MaxManifestBytes {
		return nil, fmt.Errorf("%s is larger than %d bytes", ManifestName, MaxManifestBytes)
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var m Manifest
	if err := dec.Decode(&m); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("%s is empty", ManifestName)
		}
		return nil, fmt.Errorf("%s: %w", ManifestName, err)
	}
	return &m, nil
}

// AgentList holds agent presets as JSON: their type (sessions.CatalogPreset)
// lives above this package and is keyed by json tags, so each YAML entry is
// converted once here and decoded strictly by internal/plugins.
type AgentList []json.RawMessage

func (a *AgentList) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.SequenceNode {
		return fmt.Errorf("line %d: agents must be a list", node.Line)
	}
	for _, item := range node.Content {
		var v any
		if err := item.Decode(&v); err != nil {
			return err
		}
		raw, err := json.Marshal(v)
		if err != nil {
			return fmt.Errorf("line %d: %w", item.Line, err)
		}
		*a = append(*a, raw)
	}
	return nil
}

// Validate checks a manifest against itself and, when files is non-nil, the
// package's files (paths must exist). bundled permits the reserved prefix.
func (m *Manifest) Validate(files map[string]File, bundled bool) error {
	var errs []string
	fail := func(format string, args ...any) { errs = append(errs, fmt.Sprintf(format, args...)) }
	if !idRe.MatchString(m.ID) {
		fail("id %q must be lowercase letters, digits and '-', optionally with a 'publisher.' prefix", m.ID)
	}
	if strings.HasPrefix(m.ID, ReservedPrefix) && !bundled {
		fail("id %q uses the reserved %q publisher", m.ID, ReservedPrefix)
	}
	if strings.TrimSpace(m.Name) == "" || len(m.Name) > 80 {
		fail("name is required (at most 80 characters)")
	}
	if !versionRe.MatchString(m.Version) {
		fail("version %q is required: letters, digits and . + _ -", m.Version)
	}
	if len(m.Description) > 500 {
		fail("description is longer than 500 characters")
	}
	if m.Homepage != "" && !httpURL(m.Homepage) {
		fail("homepage must be an http(s) URL")
	}
	if m.MinLectern != "" {
		if _, ok := parseSemver(m.MinLectern); !ok {
			fail("min_lectern %q is not a version like 2.4.0", m.MinLectern)
		}
	}
	for _, h := range m.Capabilities.Network {
		if !hostRe.MatchString(h) {
			fail("capabilities.network: %q is not a host name", h)
		}
	}
	secrets := map[string]bool{}
	for _, s := range m.Capabilities.Secrets {
		if !secretRe.MatchString(s) {
			fail("capabilities.secrets: %q must be UPPER_SNAKE_CASE", s)
		}
		secrets[s] = true
	}

	c := m.Contributes
	for i, raw := range c.Agents {
		var a struct {
			Name    string `json:"name" yaml:"name"`
			Command string `json:"command" yaml:"command"`
		}
		if err := json.Unmarshal(raw, &a); err != nil {
			fail("agents[%d]: %v", i, err)
			continue
		}
		if !agentNameRe.MatchString(a.Name) {
			fail("agents[%d]: name %q must be lowercase letters, digits and . _ -", i, a.Name)
		}
		if strings.TrimSpace(a.Command) == "" {
			fail("agents[%d] (%s): command is required", i, a.Name)
		}
	}
	for _, name := range sortedKeys(c.MCPServers) {
		cfg := c.MCPServers[name]
		if !mcpNameRe.MatchString(name) {
			fail("mcp_servers: name %q must be letters, digits, '_' or '-'", name)
		}
		cmd, _ := cfg["command"].(string)
		u, _ := cfg["url"].(string)
		switch {
		case cmd != "" && u != "":
			fail("mcp_servers.%s: set command or url, not both", name)
		case cmd == "" && u == "":
			fail("mcp_servers.%s: needs a command or a url", name)
		case u != "" && !httpURL(u):
			fail("mcp_servers.%s: url must be http(s)", name)
		}
		raw, _ := json.Marshal(cfg)
		for _, ref := range secretRef.FindAllStringSubmatch(string(raw), -1) {
			if !secrets[ref[1]] {
				fail("mcp_servers.%s uses ${secret:%s}, which capabilities.secrets does not declare", name, ref[1])
			}
		}
	}
	ids := map[string]bool{}
	entries := map[string]bool{}
	skillDir := func(kind, id, p, entry string) {
		if !partRe.MatchString(id) {
			fail("%s: id %q must be lowercase letters, digits and '-'", kind, id)
		}
		if ids[id] {
			fail("%s: id %q is used twice", kind, id)
		}
		ids[id] = true
		if entry != "" && !partRe.MatchString(entry) {
			fail("%s.%s: entry %q must be lowercase letters, digits and '-'", kind, id, entry)
		}
		e := m.EntryFor(id, entry)
		if entries[e] {
			fail("%s.%s: entry %q is used twice", kind, id, e)
		}
		entries[e] = true
		if err := checkRel(p); err != nil {
			fail("%s.%s: path: %v", kind, id, err)
			return
		}
		if files != nil {
			if _, ok := files[path.Join(p, "SKILL.md")]; !ok {
				fail("%s.%s: %s/SKILL.md is missing", kind, id, p)
			}
		}
	}
	for _, s := range c.Skills {
		skillDir("skills", s.ID, s.Path, s.Entry)
		if len(s.Description) > 500 {
			fail("skills.%s: description is longer than 500 characters", s.ID)
		}
	}
	for _, w := range c.Workflows {
		skillDir("workflows", w.ID, w.Path, w.Entry)
		if strings.TrimSpace(w.Name) == "" {
			fail("workflows.%s: name is required", w.ID)
		}
		if w.UpstreamURL != "" && !httpURL(w.UpstreamURL) {
			fail("workflows.%s: upstream_url must be http(s)", w.ID)
		}
		if len(w.Commands) > 40 {
			fail("workflows.%s: at most 40 commands", w.ID)
		}
	}
	for i, h := range c.Hooks {
		if !contains(Events, h.Event) {
			fail("hooks[%d]: event %q is not one of %s", i, h.Event, strings.Join(Events, ", "))
		}
		if h.Run != "host" && h.Run != "target" {
			fail("hooks[%d]: run must be host or target", i)
		}
		if len(h.Command) == 0 || strings.TrimSpace(h.Command[0]) == "" {
			fail("hooks[%d]: command is required", i)
		} else if strings.Contains(h.Command[0], "/") {
			if err := checkRel(h.Command[0]); err != nil {
				fail("hooks[%d]: command[0] must be a program name or a path inside the plugin: %v", i, err)
			} else if files != nil {
				if _, ok := files[path.Clean(h.Command[0])]; !ok {
					fail("hooks[%d]: %s is not in the plugin", i, h.Command[0])
				}
			}
		}
		if h.Timeout < 0 || h.Timeout > 300 {
			fail("hooks[%d]: timeout must be 1–300 seconds", i)
		}
	}
	for _, p := range c.SandboxProviders {
		if !partRe.MatchString(p.ID) || strings.TrimSpace(p.Name) == "" {
			fail("sandbox_providers: %q needs an id (lowercase, digits, '-') and a name", p.ID)
		}
		if err := checkRel(p.Path); err != nil {
			fail("sandbox_providers.%s: path: %v", p.ID, err)
		} else if files != nil {
			if _, ok := files[path.Clean(p.Path)]; !ok {
				fail("sandbox_providers.%s: %s is not in the plugin", p.ID, p.Path)
			}
		}
	}
	seen := map[string]bool{}
	for _, q := range c.QuickCommands {
		if !partRe.MatchString(q.ID) || seen["q:"+q.ID] {
			fail("quick_commands: id %q must be unique lowercase letters, digits and '-'", q.ID)
		}
		seen["q:"+q.ID] = true
		if q.Text == "" || len(q.Text) > 2000 || len(q.Label) > 60 {
			fail("quick_commands.%s: text is required (at most 2000 characters), label at most 60", q.ID)
		}
	}
	for _, t := range c.Themes {
		if !partRe.MatchString(t.ID) || seen["t:"+t.ID] || strings.TrimSpace(t.Name) == "" {
			fail("themes: %q needs a unique id and a name", t.ID)
		}
		seen["t:"+t.ID] = true
		if t.Accent != "" && !colorRe.MatchString(t.Accent) {
			fail("themes.%s: accent must be #rrggbb", t.ID)
		}
		for mode, tokens := range map[string]map[string]string{"dark": t.Dark, "light": t.Light} {
			for _, k := range sortedKeys(tokens) {
				if !contains(ThemeTokens, k) {
					fail("themes.%s.%s: %q is not a theme token", t.ID, mode, k)
				} else if !colorRe.MatchString(tokens[k]) {
					fail("themes.%s.%s.%s must be #rrggbb", t.ID, mode, k)
				}
			}
		}
	}
	for _, p := range c.PaletteCommands {
		if !partRe.MatchString(p.ID) || seen["p:"+p.ID] || strings.TrimSpace(p.Title) == "" || len(p.Title) > 80 {
			fail("palette_commands: %q needs a unique id and a title (at most 80 characters)", p.ID)
		}
		seen["p:"+p.ID] = true
		if !(strings.HasPrefix(p.Href, "#") || strings.HasPrefix(p.Href, "https://") && httpURL(p.Href)) {
			fail("palette_commands.%s: href must be a Lectern view (#…) or an https link", p.ID)
		}
	}
	for _, md := range c.Mods {
		if !partRe.MatchString(md.ID) || seen["m:"+md.ID] {
			fail("mods: id %q must be unique lowercase letters, digits and '-'", md.ID)
		}
		seen["m:"+md.ID] = true
		for _, sf := range md.Surfaces {
			if !contains(ModSurfaces, sf) {
				fail("mods.%s: surface %q must be web or cli", md.ID, sf)
			}
		}
		clean := path.Clean(md.Path)
		if !(strings.HasSuffix(clean, ".js") || strings.HasSuffix(clean, ".mjs")) {
			fail("mods.%s: %s must be a .js or .mjs file", md.ID, md.Path)
			continue
		}
		f, ok := files[clean]
		if !ok {
			fail("mods.%s: %s is not in the plugin", md.ID, md.Path)
			continue
		}
		if len(f.Data) > MaxModBytes {
			fail("mods.%s: %s is larger than 256 KB", md.ID, md.Path)
			continue
		}
		if _, err := ModScript(string(f.Data)); err != nil {
			fail("mods.%s: %v", md.ID, err)
		}
	}
	if c := m.Capabilities.API; c != "" && c != "read" && c != "write" {
		fail("capabilities.api must be read or write")
	}
	for _, sf := range m.Capabilities.Mods {
		if !contains(ModSurfaces, sf) {
			fail("capabilities.mods: %q must be web or cli", sf)
		}
	}
	if missing := m.MissingCapabilities(); len(missing) > 0 {
		for _, miss := range missing {
			fail("%s", miss)
		}
	}
	if len(errs) > 0 {
		return &ValidationError{Problems: errs}
	}
	return nil
}

// ValidationError lists every problem at once, so an author fixes them in one
// pass rather than one per run.
type ValidationError struct{ Problems []string }

func (e *ValidationError) Error() string {
	if len(e.Problems) == 1 {
		return "invalid plugin: " + e.Problems[0]
	}
	return "invalid plugin:\n  - " + strings.Join(e.Problems, "\n  - ")
}

// MissingCapabilities says which contributions need a capability the
// manifest does not declare.
func (m *Manifest) MissingCapabilities() []string {
	var out []string
	c, caps := m.Contributes, m.Capabilities
	if len(c.Agents) > 0 && !caps.Agents {
		out = append(out, "agents need capabilities.agents")
	}
	if len(c.MCPServers) > 0 && !caps.MCPTools {
		out = append(out, "mcp_servers need capabilities.mcp_tools")
	}
	for _, name := range sortedKeys(c.MCPServers) {
		if cmd, _ := c.MCPServers[name]["command"].(string); cmd != "" && !caps.TargetExec {
			out = append(out, fmt.Sprintf("mcp_servers.%s starts a program where the agent runs: it needs capabilities.target_exec", name))
		}
	}
	for i, h := range c.Hooks {
		if h.Run == "host" && !caps.HostExec {
			out = append(out, fmt.Sprintf("hooks[%d] runs on the Lectern server: it needs capabilities.host_exec", i))
		}
		if h.Run == "target" && !caps.TargetExec {
			out = append(out, fmt.Sprintf("hooks[%d] runs on a machine: it needs capabilities.target_exec", i))
		}
	}
	for _, md := range c.Mods {
		for _, sf := range md.SurfacesOf() {
			if !contains(caps.Mods, sf) {
				out = append(out, fmt.Sprintf("mods.%s runs in the %s: it needs %q in capabilities.mods", md.ID, surfaceName(sf), sf))
			}
		}
	}
	if caps.API != "" && len(c.Mods) == 0 {
		out = append(out, "capabilities.api is only for mods, and this plugin has none")
	}
	if len(c.SandboxProviders) > 0 && !caps.HostExec {
		out = append(out, "sandbox_providers run their hooks on the Lectern server: they need capabilities.host_exec")
	}
	return out
}

// EntryFor is the skill directory name a skill or workflow materializes as.
// Bundled workflows declare theirs (lectern-spec-kit); a plugin's default is
// prefixed with its name so two plugins' "review" skills cannot collide.
func (m *Manifest) EntryFor(id, entry string) string {
	if entry != "" {
		return entry
	}
	name := m.ID
	if i := strings.LastIndex(name, "."); i >= 0 {
		name = name[i+1:]
	}
	if name == id {
		return id
	}
	return name + "-" + id
}

// Bundled reports whether the id belongs to the binary's own plugins.
func (m *Manifest) Bundled() bool { return strings.HasPrefix(m.ID, ReservedPrefix) }

// CheckVersion refuses a plugin that needs a newer Lectern than current.
// An unparseable current version (a dev build) accepts anything.
func (m *Manifest) CheckVersion(current string) error {
	if m.MinLectern == "" {
		return nil
	}
	want, _ := parseSemver(m.MinLectern)
	have, ok := parseSemver(current)
	if !ok {
		return nil
	}
	for i := range want {
		if have[i] != want[i] {
			if have[i] < want[i] {
				return fmt.Errorf("%s needs Lectern %s or newer (this is %s)", m.ID, m.MinLectern, current)
			}
			return nil
		}
	}
	return nil
}

func parseSemver(v string) ([3]int, bool) {
	var out [3]int
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	parts := strings.Split(v, ".")
	if len(parts) < 1 || len(parts) > 3 {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

func httpURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != ""
}

// checkRel accepts a clean relative path inside the plugin.
func checkRel(p string) error {
	if p == "" {
		return fmt.Errorf("required")
	}
	if strings.HasPrefix(p, "/") || strings.Contains(p, "\\") {
		return fmt.Errorf("%q must be relative", p)
	}
	clean := path.Clean(p)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return fmt.Errorf("%q leaves the plugin", p)
	}
	return nil
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
