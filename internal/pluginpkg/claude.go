package pluginpkg

import (
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
)

// ClaudeManifest marks a Claude Code plugin.
const ClaudeManifest = ".claude-plugin/plugin.json"

var claudeNameClean = regexp.MustCompile(`[^a-z0-9-]+`)

// FromClaude reads a Claude Code plugin as a Lectern manifest: its skills
// (skills/<name>/SKILL.md) become skills and its MCP servers (.mcp.json or
// plugin.json mcpServers) become MCP servers, with ${CLAUDE_PLUGIN_ROOT}
// meaning the plugin's directory. Commands, subagents and hooks are
// Claude-specific — a Claude hook runs inside the agent, out of sight of
// Lectern's consent — so they are listed in skipped instead.
func FromClaude(files map[string]File) (*Manifest, []string, error) {
	var pj map[string]any
	if err := json.Unmarshal(files[ClaudeManifest].Data, &pj); err != nil {
		return nil, nil, fmt.Errorf("%s: %w", ClaudeManifest, err)
	}
	str := func(k string) string { s, _ := pj[k].(string); return strings.TrimSpace(s) }
	name := str("name")
	id := strings.Trim(claudeNameClean.ReplaceAllString(strings.ToLower(name), "-"), "-")
	if id == "" {
		return nil, nil, fmt.Errorf("%s has no name", ClaudeManifest)
	}
	if len(id) > 63 {
		id = id[:63]
	}
	m := &Manifest{
		ID: id, Name: name, Version: str("version"), Description: str("description"),
		Homepage: str("homepage"), License: str("license"),
	}
	if m.Version == "" {
		m.Version = "0.0.0"
	}
	if len(m.Description) > 500 {
		m.Description = m.Description[:500]
	}
	if m.Homepage == "" {
		if repo := str("repository"); httpURL(repo) {
			m.Homepage = repo
		}
	}
	switch a := pj["author"].(type) {
	case string:
		m.Author = a
	case map[string]any:
		m.Author, _ = a["name"].(string)
	}

	// Skills: every skills/<dir>/SKILL.md.
	var dirs []string
	for p := range files {
		parts := strings.Split(p, "/")
		if len(parts) == 3 && parts[0] == "skills" && parts[2] == "SKILL.md" {
			dirs = append(dirs, parts[1])
		}
	}
	sort.Strings(dirs)
	for _, d := range dirs {
		sid := strings.Trim(claudeNameClean.ReplaceAllString(strings.ToLower(d), "-"), "-")
		if sid == "" || len(sid) > 63 {
			continue
		}
		m.Contributes.Skills = append(m.Contributes.Skills, Skill{
			ID: sid, Name: d, Path: "skills/" + d, Entry: sid,
			Description: skillDescription(files["skills/"+d+"/SKILL.md"].Data),
		})
	}

	// MCP servers.
	var mcpDoc any
	switch v := pj["mcpServers"].(type) {
	case string:
		f, ok := files[path.Clean(strings.TrimPrefix(v, "./"))]
		if !ok {
			return nil, nil, fmt.Errorf("plugin.json mcpServers names %s, which is not in the plugin", v)
		}
		if err := json.Unmarshal(f.Data, &mcpDoc); err != nil {
			return nil, nil, fmt.Errorf("%s: %w", v, err)
		}
	case map[string]any:
		mcpDoc = v
	case nil:
		if f, ok := files[".mcp.json"]; ok {
			if err := json.Unmarshal(f.Data, &mcpDoc); err != nil {
				return nil, nil, fmt.Errorf(".mcp.json: %w", err)
			}
		}
	}
	if doc, ok := mcpDoc.(map[string]any); ok {
		if inner, ok := doc["mcpServers"].(map[string]any); ok {
			doc = inner
		}
		for name, raw := range doc {
			cfg, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			b, _ := json.Marshal(cfg)
			b = []byte(strings.ReplaceAll(string(b), "${CLAUDE_PLUGIN_ROOT}", "${LECTERN_PLUGIN_ROOT}"))
			var out map[string]any
			_ = json.Unmarshal(b, &out)
			if m.Contributes.MCPServers == nil {
				m.Contributes.MCPServers = map[string]map[string]any{}
			}
			m.Contributes.MCPServers[name] = out
			m.Capabilities.MCPTools = true
			if cmd, _ := out["command"].(string); cmd != "" {
				m.Capabilities.TargetExec = true
			}
		}
	}

	var skipped []string
	for _, dir := range []string{"commands", "agents", "hooks"} {
		for p := range files {
			if strings.HasPrefix(p, dir+"/") {
				skipped = append(skipped, dir+"/")
				break
			}
		}
	}
	for _, k := range []string{"commands", "agents", "hooks"} {
		if _, ok := pj[k]; ok && !containsPrefix(skipped, k+"/") {
			skipped = append(skipped, k+"/")
		}
	}
	sort.Strings(skipped)
	return m, skipped, nil
}

func containsPrefix(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

// skillDescription reads description: from SKILL.md's front matter.
func skillDescription(data []byte) string {
	lines := strings.Split(string(data), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return ""
	}
	for _, line := range lines[1:] {
		if strings.TrimSpace(line) == "---" {
			break
		}
		if k, v, ok := strings.Cut(line, ":"); ok && strings.TrimSpace(k) == "description" {
			v = strings.Trim(strings.TrimSpace(v), `"'`)
			if len(v) > 500 {
				v = v[:500]
			}
			return v
		}
	}
	return ""
}
