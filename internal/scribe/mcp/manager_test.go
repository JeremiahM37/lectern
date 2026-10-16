package mcp

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/config"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// fakeHost answers approvals from a queue and records what it was asked.
type fakeHost struct {
	mu        sync.Mutex
	decisions []tools.Decision
	asked     []tools.Approval
}

func (h *fakeHost) Approve(_ context.Context, a tools.Approval) tools.Decision {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.asked = append(h.asked, a)
	if len(h.decisions) == 0 {
		return tools.Deny
	}
	d := h.decisions[0]
	if len(h.decisions) > 1 {
		h.decisions = h.decisions[1:]
	}
	return d
}

func (h *fakeHost) Ask(context.Context, tools.Question) (string, error) {
	return "", context.Canceled
}

func (h *fakeHost) Progress(string, string) {}

func (h *fakeHost) approvals() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.asked)
}

func runTool(t *testing.T, reg *tools.Registry, name string, env *tools.Env, args string) tools.Result {
	t.Helper()
	tool, ok := reg.Get(name)
	if !ok {
		t.Fatalf("tool %s not registered; have %v", name, reg.Names())
	}
	return tool.Run(context.Background(), env, json.RawMessage(args))
}

func statusOf(m *Manager, name string) ServerStatus {
	for _, s := range m.Status() {
		if s.Name == name {
			return s
		}
	}
	return ServerStatus{}
}

func selfServer(t *testing.T, name string, env map[string]string) config.MCPServer {
	t.Helper()
	s := stdioSpec(t, env)
	s.Name = name
	return s
}

func TestManagerStartupStatesAndRegistry(t *testing.T) {
	srv, _ := newTestHTTPServer(t, "")
	dead := selfServer(t, "dead", map[string]string{"SCRIBE_MCP_TEST_MODE": "exit"})
	hang := selfServer(t, "hang", map[string]string{"SCRIBE_MCP_TEST_MODE": "hang"})
	off := selfServer(t, "off", nil)
	off.Disabled = true
	m := New(Options{
		Global: []config.MCPServer{
			selfServer(t, "ok", nil), dead, hang, off,
			{Name: "web", URL: srv.URL},
		},
		StartTimeout: 1500 * time.Millisecond,
	})
	reg := tools.NewRegistry()
	m.RegisterTools(reg)
	m.Start(context.Background())
	defer m.Close()

	if s := statusOf(m, "ok"); s.State != StateReady || len(s.ToolNames) != 6 {
		t.Fatalf("ok: %+v", s)
	}
	if s := statusOf(m, "dead"); s.State != StateFailed || !strings.Contains(s.Error, "fatal: missing API token") {
		t.Fatalf("dead: %+v", s)
	}
	if s := statusOf(m, "hang"); s.State != StateFailed || !strings.Contains(s.Error, "deadline") {
		t.Fatalf("hang: %+v", s)
	}
	if s := statusOf(m, "off"); s.State != StateDisabled {
		t.Fatalf("off: %+v", s)
	}
	if s := statusOf(m, "web"); s.State != StateReady || s.Transport != "http" {
		t.Fatalf("web: %+v", s)
	}
	for _, want := range []string{"mcp__ok__echo", "mcp__ok__read_only_info", "mcp__web__echo"} {
		if _, ok := reg.Get(want); !ok {
			t.Errorf("%s not registered; have %v", want, reg.Names())
		}
	}
	if _, ok := reg.Get("mcp__dead__echo"); ok {
		t.Error("failed server registered tools")
	}
	tool, _ := reg.Get("mcp__ok__read_only_info")
	if !tool.ReadOnly() || tool.Spec().Description != "[mcp:ok] read only" {
		t.Errorf("readOnly/description wrong: %v %q", tool.ReadOnly(), tool.Spec().Description)
	}
	echo, _ := reg.Get("mcp__ok__echo")
	if echo.ReadOnly() {
		t.Error("tool without readOnlyHint reported read-only")
	}
}

func TestProjectServersNeedTrust(t *testing.T) {
	proj := selfServer(t, "proj", nil)
	m := New(Options{Project: []config.MCPServer{proj}, ProjectTrusted: false, StartTimeout: 5 * time.Second})
	reg := tools.NewRegistry()
	m.RegisterTools(reg)
	m.Start(context.Background())
	defer m.Close()
	if s := statusOf(m, "proj"); s.State != StateUntrusted || s.Source != "project" {
		t.Fatalf("untrusted project server: %+v", s)
	}
	if len(reg.Names()) != 0 {
		t.Fatalf("untrusted server registered tools: %v", reg.Names())
	}

	trusted := New(Options{Project: []config.MCPServer{proj}, ProjectTrusted: true, StartTimeout: 10 * time.Second})
	trusted.Start(context.Background())
	defer trusted.Close()
	if s := statusOf(trusted, "proj"); s.State != StateReady {
		t.Fatalf("trusted project server: %+v", s)
	}
}

func TestProjectServerOverridesGlobalWhenTrusted(t *testing.T) {
	global := selfServer(t, "shared", map[string]string{"SCRIBE_MCP_TEST_MODE": "exit"})
	project := selfServer(t, "shared", nil)
	m := New(Options{Global: []config.MCPServer{global}, Project: []config.MCPServer{project}, ProjectTrusted: true, StartTimeout: 10 * time.Second})
	m.Start(context.Background())
	defer m.Close()
	if s := statusOf(m, "shared"); s.State != StateReady || s.Source != "project" {
		t.Fatalf("project definition should win: %+v", s)
	}
	// Untrusted: the global one keeps its name and runs.
	u := New(Options{Global: []config.MCPServer{project}, Project: []config.MCPServer{global}, ProjectTrusted: false, StartTimeout: 10 * time.Second})
	u.Start(context.Background())
	defer u.Close()
	if s := statusOf(u, "shared"); s.State != StateReady || s.Source != "global" {
		t.Fatalf("global should keep the name when the project is untrusted: %+v", s)
	}
}

func TestApprovalGatesNonReadOnlyTools(t *testing.T) {
	m := New(Options{Global: []config.MCPServer{selfServer(t, "ok", nil)}, StartTimeout: 10 * time.Second})
	reg := tools.NewRegistry()
	m.RegisterTools(reg)
	m.Start(context.Background())
	defer m.Close()

	host := &fakeHost{decisions: []tools.Decision{tools.Deny, tools.AllowSession}}
	env := &tools.Env{Workdir: t.TempDir(), Host: host, Mode: "default"}

	res := runTool(t, reg, "mcp__ok__echo", env, `{"text":"x"}`)
	if !res.IsError || !strings.Contains(res.Output, "denied") {
		t.Fatalf("denied call ran: %+v", res)
	}
	if host.asked[0].Kind != "mcp" || host.asked[0].Title != "Use ok tool echo" {
		t.Fatalf("approval = %+v", host.asked[0])
	}

	// AllowSession is remembered for the tool: the second call is not asked.
	if res := runTool(t, reg, "mcp__ok__echo", env, `{"text":"a"}`); res.IsError || res.Output != "a" {
		t.Fatalf("allowed call: %+v", res)
	}
	if res := runTool(t, reg, "mcp__ok__echo", env, `{"text":"b"}`); res.IsError || res.Output != "b" {
		t.Fatalf("remembered call: %+v", res)
	}
	if n := host.approvals(); n != 2 {
		t.Fatalf("approvals asked = %d, want 2 (deny, then allow-session)", n)
	}

	// Read-only tools never ask.
	if res := runTool(t, reg, "mcp__ok__read_only_info", env, `{}`); res.IsError {
		t.Fatalf("read-only tool: %+v", res)
	}
	// Auto mode skips the question.
	auto := &tools.Env{Workdir: t.TempDir(), Host: host, Mode: "auto"}
	if res := runTool(t, reg, "mcp__ok__fail", auto, `{}`); !res.IsError || res.Output != "boom" {
		t.Fatalf("auto-mode call: %+v", res)
	}
	if n := host.approvals(); n != 2 {
		t.Fatalf("auto mode asked: %d", n)
	}
	// With no way to ask, a gated tool refuses rather than running.
	if res := runTool(t, reg, "mcp__ok__image", &tools.Env{Workdir: t.TempDir()}, `{}`); !res.IsError {
		t.Fatalf("gated call with no host ran: %+v", res)
	}
}

func TestListChangedRefreshesRegistry(t *testing.T) {
	m := New(Options{Global: []config.MCPServer{selfServer(t, "ok", nil)}, StartTimeout: 10 * time.Second})
	reg := tools.NewRegistry()
	m.RegisterTools(reg)
	m.Start(context.Background())
	defer m.Close()

	if _, ok := reg.Get("mcp__ok__late"); ok {
		t.Fatal("late tool present before it exists")
	}
	env := &tools.Env{Workdir: t.TempDir(), Mode: "auto"}
	runTool(t, reg, "mcp__ok__add_tool", env, `{}`)
	waitFor(t, func() bool { _, ok := reg.Get("mcp__ok__late"); return ok })
	if res := runTool(t, reg, "mcp__ok__late", env, `{}`); res.IsError || res.Output != "late ok" {
		t.Fatalf("late tool: %+v", res)
	}
	if s := statusOf(m, "ok"); len(s.ToolNames) != 7 {
		t.Fatalf("status tool count = %d, want 7", len(s.ToolNames))
	}
}

func TestHTTPListChangedRefreshesRegistry(t *testing.T) {
	srv, _ := newTestHTTPServer(t, "")
	m := New(Options{Global: []config.MCPServer{{Name: "web", URL: srv.URL}}, StartTimeout: 10 * time.Second})
	reg := tools.NewRegistry()
	m.RegisterTools(reg)
	m.Start(context.Background())
	defer m.Close()
	if _, ok := reg.Get("mcp__web__late"); ok {
		t.Fatal("late tool present before the GET stream announced it")
	}
	waitFor(t, func() bool { _, ok := reg.Get("mcp__web__late"); return ok })
}

func TestRestartAndClose(t *testing.T) {
	m := New(Options{Global: []config.MCPServer{selfServer(t, "ok", nil)}, StartTimeout: 10 * time.Second})
	reg := tools.NewRegistry()
	m.RegisterTools(reg)
	m.Start(context.Background())

	if err := m.Restart(context.Background(), "ok"); err != nil {
		t.Fatal(err)
	}
	env := &tools.Env{Workdir: t.TempDir(), Mode: "auto"}
	if res := runTool(t, reg, "mcp__ok__echo", env, `{"text":"again"}`); res.Output != "again" {
		t.Fatalf("after restart: %+v", res)
	}
	if err := m.Restart(context.Background(), "nope"); err == nil {
		t.Fatal("restart of an unknown server succeeded")
	}

	m.Close()
	if s := statusOf(m, "ok"); s.State != StateStopped {
		t.Fatalf("after Close: %+v", s)
	}
	// Close unregisters the tools, so a new request no longer offers them.
	if _, ok := reg.Get("mcp__ok__echo"); ok {
		t.Fatal("tool still registered after Close")
	}
	// A call already in flight with the old tool value gets a clear error.
	stale := &remoteTool{m: m, server: "ok", remote: "echo", name: "mcp__ok__echo"}
	res := stale.Run(context.Background(), env, json.RawMessage(`{"text":"x"}`))
	if !res.IsError || !strings.Contains(res.Output, "not connected") {
		t.Fatalf("call after Close: %+v", res)
	}
}

func TestLoadServersReadsMCPJSON(t *testing.T) {
	t.Setenv("SCRIBE_HOME", t.TempDir())
	root := t.TempDir()
	if err := os.WriteFile(root+"/.mcp.json", []byte(`{"mcpServers":{
		"fs":{"command":"npx","args":["-y","fs-server"],"env":{"TOKEN":"${HOME}"}},
		"remote":{"type":"http","url":"https://example.invalid/mcp","headers":{"X":"1"}}
	}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	g, p, err := LoadServers(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(g) != 0 || len(p) != 2 || p[0].Name != "fs" || p[0].Command != "npx" || p[1].URL == "" {
		t.Fatalf("global=%v project=%+v", g, p)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("condition not met within 5s")
}
