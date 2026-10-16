package tui

// Fake implementations of the MCP and skill-loading methods of Backend, so
// the server editor and the skill browser have data in tests and the demo.

import (
	"errors"
	"path"
	"sort"
	"strings"
)

// seedMCP fills the fake's MCP list the first time it is read.
func (f *FakeBackend) seedMCP() {
	if f.mcpSpecs == nil {
		f.mcpSpecs = []MCPServerSpec{
			{Name: "github", Scope: "global", Transport: "stdio", Command: "npx", Args: []string{"-y", "@modelcontextprotocol/server-github"}, Env: map[string]string{"GITHUB_TOKEN": "set"}},
			{Name: "postgres", Scope: "project", Transport: "http", URL: "https://db.example.invalid/mcp", Headers: map[string]string{"X-Team": "data"}},
		}
	}
}

// MCPServers implements Backend.
func (f *FakeBackend) MCPServers() []MCPServerSpec {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seedMCP()
	return append([]MCPServerSpec(nil), f.mcpSpecs...)
}

// SaveMCPServer implements Backend. A new server with a name already in use
// replaces that entry, as the real config writer does.
func (f *FakeBackend) SaveMCPServer(spec MCPServerSpec, was *MCPServerSpec) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seedMCP()
	if strings.TrimSpace(spec.Name) == "" {
		return errors.New("a server name is needed")
	}
	var out []MCPServerSpec
	placed := false
	for _, s := range f.mcpSpecs {
		editing := was != nil && s.Name == was.Name && s.Scope == was.Scope
		if editing || s.Name == spec.Name {
			if !placed {
				out = append(out, spec)
				placed = true
			}
			continue
		}
		out = append(out, s)
	}
	if !placed {
		out = append(out, spec)
	}
	f.mcpSpecs = out
	return nil
}

// RemoveMCPServer implements Backend.
func (f *FakeBackend) RemoveMCPServer(name, scope string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seedMCP()
	for i, s := range f.mcpSpecs {
		if s.Name == name && s.Scope == scope {
			f.mcpSpecs = append(f.mcpSpecs[:i], f.mcpSpecs[i+1:]...)
			return nil
		}
	}
	return errors.New(name + " is not in the " + scope + " config")
}

// InstallSkill implements Backend. The name is taken from the last path
// element of src, as an installed skill directory is named.
func (f *FakeBackend) InstallSkill(src, scope string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if strings.TrimSpace(src) == "" {
		return "", errors.New("install needs a source: a local directory or a git URL")
	}
	name := strings.TrimSuffix(path.Base(strings.SplitN(src, "#", 2)[0]), ".git")
	f.installed = append(f.installed, name)
	return name, nil
}

// ActiveSkills implements Backend.
func (f *FakeBackend) ActiveSkills() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.activeSkills...)
}

// SetSkillActive implements Backend.
func (f *FakeBackend) SetSkillActive(name string, on bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	have := false
	for _, n := range f.activeSkills {
		if n == name {
			have = true
			if !on {
				continue
			}
		}
		out = append(out, n)
	}
	if on && !have {
		out = append(out, name)
	}
	sort.Strings(out)
	f.activeSkills = out
	return nil
}
