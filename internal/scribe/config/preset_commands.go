package config

// A preset can carry hooks and MCP servers. Both run commands on this machine:
// a hook runs a shell command on agent events, and an MCP server starts a
// process or connects to a URL. A preset therefore never adds them silently.
// The /preset command shows what would be added and asks first. This file
// works out what a preset would add to its scope's config file and writes
// exactly that once the user has said yes.

import (
	"encoding/json"
	"fmt"
)

// PresetCommands is what a preset adds to its scope's config file: hooks and
// MCP servers that are not there yet, or an MCP server that differs from the
// one there under the same name.
type PresetCommands struct {
	Hooks []Hook
	MCP   []MCPServer
	// Replaced names the MCP servers in MCP that overwrite an entry of the
	// same name already in the file.
	Replaced map[string]bool
}

// Empty reports whether the preset would add nothing.
func (c PresetCommands) Empty() bool { return len(c.Hooks) == 0 && len(c.MCP) == 0 }

// PendingPresetCommands reports what applying p would add to the config file
// of p's scope. It writes nothing. Hooks and servers that are already in that
// file exactly as the preset has them are left out, so reapplying a preset
// asks nothing new.
func PendingPresetCommands(root string, p Preset) (PresetCommands, error) {
	_, add, err := presetCommandsFor(root, p)
	return add, err
}

// SavePresetCommands writes what PendingPresetCommands reports into p's scope
// config file and returns it. Hooks are appended. An MCP server replaces the
// entry of the same name, as Merge does for presets. Other keys in the file
// are kept by SaveGlobal and SaveProject.
func SavePresetCommands(root string, p Preset) (PresetCommands, error) {
	cur, add, err := presetCommandsFor(root, p)
	if err != nil || add.Empty() {
		return add, err
	}
	cur.Hooks = append(cur.Hooks, add.Hooks...)
	cur.MCP = mergeBy(cur.MCP, add.MCP, func(m MCPServer) string { return m.Name })
	if p.Project {
		err = SaveProject(root, cur)
	} else {
		err = SaveGlobal(cur)
	}
	return add, err
}

// presetCommandsFor loads the config file p's scope writes to, and the part
// of p's hooks and MCP servers that file does not have yet.
func presetCommandsFor(root string, p Preset) (Config, PresetCommands, error) {
	if err := checkPresetCommands(p); err != nil {
		return Config{}, PresetCommands{}, err
	}
	path := GlobalPath()
	if p.Project {
		if root == "" {
			return Config{}, PresetCommands{}, fmt.Errorf("preset %s is in a project, and no project is open", p.Name)
		}
		path = ProjectPath(root)
	}
	cur, err := LoadFile(path)
	if err != nil {
		return Config{}, PresetCommands{}, err
	}
	return cur, diffPresetCommands(cur, p.Config), nil
}

// diffPresetCommands returns the hooks and MCP servers of want that cur does
// not already have. A duplicate hook in want is counted once.
func diffPresetCommands(cur, want Config) PresetCommands {
	out := PresetCommands{Replaced: map[string]bool{}}
	for _, h := range want.Hooks {
		if !containsHook(cur.Hooks, h) && !containsHook(out.Hooks, h) {
			out.Hooks = append(out.Hooks, h)
		}
	}
	for _, m := range want.MCP {
		old, ok := findMCP(cur.MCP, m.Name)
		switch {
		case !ok:
			out.MCP = append(out.MCP, m)
		case !sameMCP(old, m):
			out.MCP = append(out.MCP, m)
			out.Replaced[m.Name] = true
		}
	}
	return out
}

// checkPresetCommands refuses hooks and servers that could not run, before
// anything is written. Each MCP server needs a name that is not used twice
// in the preset, and exactly one of a command (stdio) or a URL (http).
func checkPresetCommands(p Preset) error {
	for _, h := range p.Config.Hooks {
		if h.Event == "" || h.Command == "" {
			return fmt.Errorf("preset %s: a hook needs an event and a command", p.Name)
		}
	}
	seen := map[string]bool{}
	for _, m := range p.Config.MCP {
		if m.Name == "" {
			return fmt.Errorf("preset %s: an MCP server needs a name", p.Name)
		}
		if seen[m.Name] {
			return fmt.Errorf("preset %s: MCP server %s is listed twice", p.Name, m.Name)
		}
		seen[m.Name] = true
		if (m.Command == "") == (m.URL == "") {
			return fmt.Errorf("preset %s: MCP server %s needs either a command or a URL", p.Name, m.Name)
		}
	}
	return nil
}

func containsHook(list []Hook, h Hook) bool {
	for _, x := range list {
		if x == h {
			return true
		}
	}
	return false
}

func findMCP(list []MCPServer, name string) (MCPServer, bool) {
	for _, m := range list {
		if m.Name == name {
			return m, true
		}
	}
	return MCPServer{}, false
}

// sameMCP compares two server definitions as they would be written to disk.
// Encoding both makes a nil map and an empty one equal, as they are on disk.
func sameMCP(a, b MCPServer) bool {
	ja, errA := json.Marshal(a)
	jb, errB := json.Marshal(b)
	if errA != nil || errB != nil {
		return false
	}
	return string(ja) == string(jb)
}
