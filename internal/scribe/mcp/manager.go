package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"sync"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/config"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// State is a server's connection state, as shown in the UI.
type State string

const (
	StateDisabled   State = "disabled"   // turned off in config
	StateUntrusted  State = "untrusted"  // a project server, and the project is not trusted
	StateConnecting State = "connecting" // startup in progress
	StateReady      State = "ready"
	StateFailed     State = "failed"
	StateStopped    State = "stopped" // Close was called
)

// ServerStatus is what the UI shows for one server.
type ServerStatus struct {
	Name      string
	Transport string // "stdio" or "http"
	Source    string // "global" or "project"
	State     State
	Error     string
	ToolNames []string // registered tool names (mcp__server__tool)
	Spec      config.MCPServer
}

// Options configures a Manager. Use NewFromDisk for the normal case.
type Options struct {
	// Global servers come from the user's Scribe config and always run.
	Global []config.MCPServer
	// Project servers come from the project config and .mcp.json. They run
	// only when ProjectTrusted is set. A project server with the same name
	// as a global one replaces it when trusted.
	Project        []config.MCPServer
	ProjectTrusted bool

	ClientVersion string        // reported to servers, default "dev"
	StartTimeout  time.Duration // per server, connect through tool list, default 20s
	CallTimeout   time.Duration // per tool call, default 60s
}

type server struct {
	spec   config.MCPServer
	source string
	state  State
	err    string
	client *Client
	tools  []ToolInfo
	// gen changes whenever the server is (re)connected or stopped, so a
	// late notification or connect from an old attempt is ignored.
	gen int
}

// Manager owns the MCP connections and publishes their tools to a registry.
type Manager struct {
	opts Options

	mu      sync.Mutex
	order   []string
	servers map[string]*server
	reg     *tools.Registry
	allowed map[string]bool // tools the user allowed for this session
	// published is the set of tool names currently in reg, so a tool that
	// goes away can be unregistered.
	published map[string]bool
}

// New builds a Manager. Nothing connects until Start.
func New(o Options) *Manager {
	if o.ClientVersion == "" {
		o.ClientVersion = "dev"
	}
	if o.StartTimeout == 0 {
		o.StartTimeout = 20 * time.Second
	}
	if o.CallTimeout == 0 {
		o.CallTimeout = 60 * time.Second
	}
	m := &Manager{opts: o, servers: map[string]*server{}, allowed: map[string]bool{}, published: map[string]bool{}}
	for _, s := range m.plan(o) {
		m.servers[s.spec.Name] = s
		m.order = append(m.order, s.spec.Name)
	}
	return m
}

// plan turns the server lists in o into fresh, unconnected servers in config
// order. A name appears once: a trusted project server replaces a global one
// of the same name, and an untrusted project server never shadows a global.
func (m *Manager) plan(o Options) []*server {
	var out []*server
	seen := map[string]bool{}
	add := func(s config.MCPServer, source string, state State) {
		if seen[s.Name] || s.Name == "" {
			return
		}
		seen[s.Name] = true
		out = append(out, &server{spec: s, source: source, state: state})
	}
	projectNames := map[string]bool{}
	for _, s := range o.Project {
		if s.Name != "" {
			projectNames[s.Name] = true
		}
	}
	for _, s := range o.Global {
		if o.ProjectTrusted && projectNames[s.Name] {
			continue // the project's definition wins
		}
		add(s, "global", initialState(s, true))
	}
	for _, s := range o.Project {
		if o.ProjectTrusted {
			add(s, "project", initialState(s, true))
		} else if !seen[s.Name] {
			add(s, "project", StateUntrusted)
		}
	}
	return out
}

// Reload applies new server lists to a running Manager, typically after the
// config files changed. Servers that disappeared disconnect and lose their
// tools. Servers whose definition or trust changed reconnect. New enabled
// servers start. Unchanged servers keep their connection. Connections run in
// the background, and the registry follows as each one finishes.
func (m *Manager) Reload(o Options) {
	fresh := m.plan(o)

	m.mu.Lock()
	var closing []*Client
	var starts []string
	keep := make(map[string]*server, len(fresh))
	order := make([]string, 0, len(fresh))
	for _, f := range fresh {
		name := f.spec.Name
		order = append(order, name)
		old, had := m.servers[name]
		if had && old.source == f.source && reflect.DeepEqual(old.spec, f.spec) &&
			(old.state == StateUntrusted) == (f.state == StateUntrusted) {
			keep[name] = old
			continue
		}
		if had {
			old.gen++
			if old.client != nil {
				closing = append(closing, old.client)
				old.client = nil
			}
			old.tools = nil
		}
		keep[name] = f
		if f.state == StateConnecting {
			starts = append(starts, name)
		}
	}
	for name, old := range m.servers {
		if _, ok := keep[name]; ok {
			continue
		}
		old.gen++
		if old.client != nil {
			closing = append(closing, old.client)
			old.client = nil
		}
		old.tools = nil
	}
	m.servers = keep
	m.order = order
	m.syncRegistryLocked()
	m.mu.Unlock()

	for _, c := range closing {
		c.Close()
	}
	for _, name := range starts {
		go func(name string) {
			m.connect(context.Background(), name)
			m.syncRegistry()
		}(name)
	}
}

func initialState(s config.MCPServer, trusted bool) State {
	if s.Disabled {
		return StateDisabled
	}
	if !trusted {
		return StateUntrusted
	}
	return StateConnecting
}

// LoadServers reads the MCP servers for a project: global from the Scribe
// config, and project from the project config and <root>/.mcp.json
// (Claude Code's format). Use the result with New.
func LoadServers(root string) (global, project []config.MCPServer, err error) {
	g, err := readConfigMCP(config.GlobalPath())
	if err != nil {
		return nil, nil, err
	}
	if root == "" {
		return g, nil, nil
	}
	p, err := readConfigMCP(config.ProjectPath(root))
	if err != nil {
		return nil, nil, err
	}
	have := map[string]bool{}
	for _, s := range p {
		have[s.Name] = true
	}
	mcpJSON, err := readMCPJSON(filepath.Join(root, ".mcp.json"))
	if err != nil {
		return nil, nil, err
	}
	for _, s := range mcpJSON {
		if !have[s.Name] {
			p = append(p, s)
		}
	}
	return g, p, nil
}

// NewFromDisk loads servers for root and builds a Manager. trusted comes
// from hooks.Trusted(root).
func NewFromDisk(root string, trusted bool, clientVersion string) (*Manager, error) {
	g, p, err := LoadServers(root)
	if err != nil {
		return nil, err
	}
	return New(Options{Global: g, Project: p, ProjectTrusted: trusted, ClientVersion: clientVersion}), nil
}

func readConfigMCP(path string) ([]config.MCPServer, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) || (err == nil && len(b) == 0) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var c config.Config
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return c.MCP, nil
}

// readMCPJSON parses Claude Code's .mcp.json: {"mcpServers": {name: {...}}}.
func readMCPJSON(path string) ([]config.MCPServer, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) || (err == nil && len(b) == 0) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var f struct {
		MCPServers map[string]struct {
			Command  string            `json:"command"`
			Args     []string          `json:"args"`
			Env      map[string]string `json:"env"`
			URL      string            `json:"url"`
			Headers  map[string]string `json:"headers"`
			Disabled bool              `json:"disabled"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	names := make([]string, 0, len(f.MCPServers))
	for n := range f.MCPServers {
		names = append(names, n)
	}
	sort.Strings(names)
	var out []config.MCPServer
	for _, n := range names {
		s := f.MCPServers[n]
		out = append(out, config.MCPServer{
			Name: n, Command: s.Command, Args: s.Args, Env: s.Env,
			URL: s.URL, Headers: s.Headers, Disabled: s.Disabled,
		})
	}
	return out, nil
}

// Start connects every enabled, trusted server in parallel. Each attempt
// is bounded by StartTimeout. It returns when all attempts have finished.
func (m *Manager) Start(ctx context.Context) {
	m.mu.Lock()
	var names []string
	for _, n := range m.order {
		if s := m.servers[n]; s.state == StateConnecting {
			names = append(names, n)
		}
	}
	m.mu.Unlock()

	var wg sync.WaitGroup
	for _, n := range names {
		wg.Add(1)
		go func(n string) {
			defer wg.Done()
			m.connect(ctx, n)
		}(n)
	}
	wg.Wait()
	m.syncRegistry()
}

// connect (re)connects one server and records the outcome.
func (m *Manager) connect(parent context.Context, name string) {
	m.mu.Lock()
	s := m.servers[name]
	if s == nil {
		// Removed by Reload after the attempt was queued.
		m.mu.Unlock()
		return
	}
	s.gen++
	gen := s.gen
	s.state = StateConnecting
	s.err = ""
	old := s.client
	s.client = nil
	spec := s.spec
	m.mu.Unlock()
	if old != nil {
		old.Close()
	}

	ctx, cancel := context.WithTimeout(parent, m.opts.StartTimeout)
	defer cancel()
	c, list, err := connect(ctx, name, spec, m.opts.ClientVersion, func(method string) {
		if method == "notifications/tools/list_changed" {
			go m.refresh(name, gen)
		}
	})

	m.mu.Lock()
	defer m.mu.Unlock()
	if s.gen != gen {
		// Superseded by a restart or Close while connecting.
		if c != nil {
			c.Close()
		}
		return
	}
	if err != nil {
		s.state = StateFailed
		s.err = err.Error()
		s.tools = nil
		return
	}
	s.state = StateReady
	s.client = c
	s.tools = list
}

// refresh re-lists a server's tools after it announced a change.
func (m *Manager) refresh(name string, gen int) {
	m.mu.Lock()
	s := m.servers[name]
	if s == nil || s.gen != gen || s.client == nil {
		m.mu.Unlock()
		return
	}
	c := s.client
	m.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), m.opts.StartTimeout)
	defer cancel()
	list, err := c.listTools(ctx)

	m.mu.Lock()
	if s.gen == gen && s.client == c && err == nil {
		s.tools = list
	}
	m.mu.Unlock()
	m.syncRegistry()
}

// Restart reconnects one server, for example after the user fixes its
// config or the process died. It blocks for up to StartTimeout.
func (m *Manager) Restart(ctx context.Context, name string) error {
	m.mu.Lock()
	s, ok := m.servers[name]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("no MCP server named %q", name)
	}
	state := s.state
	m.mu.Unlock()
	if state == StateDisabled || state == StateUntrusted {
		return fmt.Errorf("MCP server %q is %s and cannot be restarted", name, state)
	}
	m.connect(ctx, name)
	m.syncRegistry()
	m.mu.Lock()
	defer m.mu.Unlock()
	if s.state == StateFailed {
		return errors.New(s.err)
	}
	return nil
}

// Close disconnects every server. The Manager can still be used for Status.
func (m *Manager) Close() error {
	m.mu.Lock()
	var clients []*Client
	for _, s := range m.servers {
		s.gen++
		if s.client != nil {
			clients = append(clients, s.client)
			s.client = nil
		}
		if s.state != StateDisabled && s.state != StateUntrusted {
			s.state = StateStopped
		}
		s.tools = nil
	}
	m.mu.Unlock()
	for _, c := range clients {
		c.Close()
	}
	m.syncRegistry()
	return nil
}

// Status returns every configured server in config order.
func (m *Manager) Status() []ServerStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]ServerStatus, 0, len(m.order))
	for _, n := range m.order {
		s := m.servers[n]
		st := ServerStatus{Name: n, Transport: "stdio", Source: s.source, State: s.state, Error: s.err, Spec: s.spec}
		if s.spec.URL != "" {
			st.Transport = "http"
		}
		if s.state == StateReady {
			for _, t := range s.tools {
				st.ToolNames = append(st.ToolNames, toolName(n, t.Name))
			}
		}
		out = append(out, st)
	}
	return out
}

// RegisterTools publishes the tools of ready servers to r now and whenever
// the set changes. A tool whose server stops, is disabled or is removed is
// unregistered, so the next model request no longer offers it.
func (m *Manager) RegisterTools(r *tools.Registry) {
	m.mu.Lock()
	m.reg = r
	m.mu.Unlock()
	m.syncRegistry()
}

// syncRegistry makes the registry hold exactly the tools of ready servers.
func (m *Manager) syncRegistry() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.syncRegistryLocked()
}

// syncRegistryLocked is syncRegistry with m.mu already held. Registry changes
// happen under the lock so two syncs cannot interleave and leave a stale tool.
func (m *Manager) syncRegistryLocked() {
	reg := m.reg
	if reg == nil {
		return
	}
	now := map[string]bool{}
	for _, n := range m.order {
		s := m.servers[n]
		if s == nil || s.state != StateReady {
			continue
		}
		for _, t := range s.tools {
			rt := &remoteTool{
				m: m, server: n, remote: t.Name, name: toolName(n, t.Name),
				desc:     fmt.Sprintf("[mcp:%s] %s", n, t.Description),
				schema:   schemaOrEmpty(t.Schema),
				readOnly: t.ReadOnly,
			}
			now[rt.name] = true
			reg.Register(rt)
		}
	}
	for name := range m.published {
		if !now[name] {
			reg.Unregister(name)
		}
	}
	m.published = now
}

func schemaOrEmpty(s json.RawMessage) json.RawMessage {
	if len(s) == 0 || string(s) == "null" {
		return json.RawMessage(`{"type":"object","properties":{}}`)
	}
	return s
}

func (m *Manager) sessionAllowed(name string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.allowed[name]
}

func (m *Manager) allowSession(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.allowed[name] = true
}

// call runs one tool on a ready server, bounded by CallTimeout.
func (m *Manager) call(ctx context.Context, server, tool string, args json.RawMessage) tools.Result {
	m.mu.Lock()
	var c *Client
	state := StateStopped
	if s := m.servers[server]; s != nil {
		state = s.state
		if s.state == StateReady {
			c = s.client
		}
	}
	m.mu.Unlock()
	if c == nil {
		return tools.Errorf("MCP server %q is not connected (%s). Use the tool only after it is ready.", server, state)
	}
	cctx, cancel := context.WithTimeout(ctx, m.opts.CallTimeout)
	defer cancel()
	res, err := c.callTool(cctx, tool, args)
	if err != nil {
		return tools.Errorf("%s on %s failed: %v", tool, server, err)
	}
	out := formatContent(res.Content)
	if out == "" && len(res.StructuredContent) > 0 {
		out = truncate(string(res.StructuredContent), maxOutput)
	}
	return tools.Result{Output: out, IsError: res.IsError}
}
