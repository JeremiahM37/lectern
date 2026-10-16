package app

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/config"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/hooks"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/mcp"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui"
)

// mcpNameRE is what a server may be called. Tool names are
// mcp__<server>__<tool>, so the name cannot hold "__" or separators.
var mcpNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]*(_[A-Za-z0-9-]+)*$`)

// MCPServers implements tui.Backend. It lists every configured server with
// the definition the form edits, including disabled and untrusted ones.
func (b *scribeBackend) MCPServers() []tui.MCPServerSpec {
	a := b.current()
	if a.ext.mcp == nil {
		return nil
	}
	var out []tui.MCPServerSpec
	for _, s := range a.ext.mcp.Status() {
		out = append(out, mcpSpecFrom(s))
	}
	return out
}

// SaveMCPServer implements tui.Backend. It writes spec into the config file
// of its scope, replacing was (the definition being edited) when given, and
// then applies the config to the running servers. A server that changes
// scope is removed from the old scope's file.
func (b *scribeBackend) SaveMCPServer(spec tui.MCPServerSpec, was *tui.MCPServerSpec) error {
	a := b.current()
	cfg, err := mcpConfigFrom(spec)
	if err != nil {
		return err
	}
	path, err := a.mcpConfigPath(spec.Scope)
	if err != nil {
		return err
	}
	prev := ""
	if was != nil && was.Scope != spec.Scope {
		oldPath, err := a.mcpConfigPath(was.Scope)
		if err != nil {
			return err
		}
		oldName := was.Name
		if err := mcp.UpdateServers(oldPath, func(list []config.MCPServer) ([]config.MCPServer, error) {
			out, _ := mcp.RemoveServer(list, oldName)
			return out, nil
		}); err != nil {
			return err
		}
	} else if was != nil {
		prev = was.Name
	}
	if err := mcp.UpdateServers(path, func(list []config.MCPServer) ([]config.MCPServer, error) {
		return mcp.PutServer(list, prev, cfg), nil
	}); err != nil {
		return err
	}
	return a.reloadMCP()
}

// RemoveMCPServer implements tui.Backend. Only servers written in the scope's
// config file can be removed here. Entries from .mcp.json are edited in that
// file.
func (b *scribeBackend) RemoveMCPServer(name, scope string) error {
	a := b.current()
	path, err := a.mcpConfigPath(scope)
	if err != nil {
		return err
	}
	found := false
	if err := mcp.UpdateServers(path, func(list []config.MCPServer) ([]config.MCPServer, error) {
		out, ok := mcp.RemoveServer(list, name)
		found = ok
		if !ok {
			return list, nil
		}
		return out, nil
	}); err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("%s is not in the %s config. Servers from .mcp.json are removed from that file.", name, scope)
	}
	return a.reloadMCP()
}

// mcpConfigPath is the config file a scope writes to.
func (a *App) mcpConfigPath(scope string) (string, error) {
	switch scope {
	case "global":
		return config.GlobalPath(), nil
	case "project":
		return config.ProjectPath(a.Workdir), nil
	}
	return "", fmt.Errorf("unknown scope %q; use global or project", scope)
}

// reloadMCP re-reads the MCP servers from disk and applies them to the
// running manager. The manager is created on first use, for a session that
// started with no MCP config at all. Connections finish in the background.
func (a *App) reloadMCP() error {
	global, project, err := mcp.LoadServers(a.Workdir)
	if err != nil {
		return err
	}
	if a.ext.mcp == nil {
		a.ext.mcp = mcp.New(mcp.Options{ClientVersion: a.opts.Version})
		a.ext.mcp.RegisterTools(a.Tools)
	}
	a.ext.mcp.Reload(mcp.Options{Global: global, Project: project, ProjectTrusted: hooks.Trusted(a.Workdir)})
	return nil
}

// mcpSpecFrom converts a manager status into the form's view of a server.
func mcpSpecFrom(s mcp.ServerStatus) tui.MCPServerSpec {
	sp := s.Spec
	out := tui.MCPServerSpec{
		Name: s.Name, Scope: s.Source, Transport: "stdio",
		Command: sp.Command, Args: append([]string(nil), sp.Args...),
		URL: sp.URL, Disabled: sp.Disabled,
	}
	if sp.URL != "" {
		out.Transport = "http"
		out.Args = nil
		out.Headers = copyMap(sp.Headers)
	}
	out.Env = copyMap(sp.Env)
	return out
}

// mcpConfigFrom validates a form value and turns it into a config entry.
func mcpConfigFrom(spec tui.MCPServerSpec) (config.MCPServer, error) {
	name := strings.TrimSpace(spec.Name)
	if !mcpNameRE.MatchString(name) || len(name) > 40 {
		return config.MCPServer{}, errors.New("a server name uses letters, digits, '-' and single '_', up to 40 characters")
	}
	if spec.Scope != "global" && spec.Scope != "project" {
		return config.MCPServer{}, fmt.Errorf("unknown scope %q; use global or project", spec.Scope)
	}
	out := config.MCPServer{Name: name, Env: copyMap(spec.Env), Disabled: spec.Disabled}
	switch spec.Transport {
	case "stdio":
		if strings.TrimSpace(spec.Command) == "" {
			return config.MCPServer{}, errors.New("a stdio server needs a command")
		}
		out.Command = strings.TrimSpace(spec.Command)
		out.Args = append([]string(nil), spec.Args...)
	case "http":
		u := strings.TrimSpace(spec.URL)
		if !strings.HasPrefix(u, "https://") && !strings.HasPrefix(u, "http://") {
			return config.MCPServer{}, errors.New("an http server needs a URL starting with https:// or http://")
		}
		out.URL = u
		out.Headers = copyMap(spec.Headers)
	default:
		return config.MCPServer{}, fmt.Errorf("unknown transport %q; use stdio or http", spec.Transport)
	}
	return out, nil
}

func copyMap(m map[string]string) map[string]string {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
