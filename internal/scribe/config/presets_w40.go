package config

// A preset that adds hooks or MCP servers writes them into its scope's config
// file. Removing the preset must take them back out, but the file alone does
// not say which entries came from the preset: a hook the user wrote by hand
// looks the same. So each apply records what it wrote, one file per preset and
// scope, and removal reverts exactly those entries.
//
// The record lives beside the config, not in the presets directory, so a
// preset listing never reads it.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// appliedDirName is the record directory, under the global home or the
// project's .scribe directory.
const appliedDirName = "preset-applied"

// AppliedPresetCommands is what one preset has written to one config file.
type AppliedPresetCommands struct {
	// Hooks are the hooks the preset added, exactly as written.
	Hooks []Hook `json:"hooks,omitempty"`
	// MCP are the server definitions the preset wrote, one per name.
	MCP []MCPServer `json:"mcp,omitempty"`
	// Replaced are the entries the preset overwrote, by name. Removal puts
	// them back instead of deleting the name.
	Replaced []MCPServer `json:"replaced,omitempty"`
}

// LoadPresetScope returns the config file p's scope writes to, as it is on
// disk now. It is what RecordPresetCommands compares against.
func LoadPresetScope(root string, p Preset) (Config, error) {
	path, err := presetScopePath(root, p)
	if err != nil {
		return Config{}, err
	}
	return LoadFile(path)
}

// presetScopePath is the config file of p's scope.
func presetScopePath(root string, p Preset) (string, error) {
	if !p.Project {
		return GlobalPath(), nil
	}
	if root == "" {
		return "", fmt.Errorf("preset %s is in a project, and no project is open", p.Name)
	}
	return ProjectPath(root), nil
}

// appliedPath is the record file for p in its scope.
func appliedPath(root string, p Preset) (string, error) {
	if !presetName.MatchString(p.Name) {
		return "", fmt.Errorf("preset %s is not a preset name this command may record", p.Name)
	}
	dir := filepath.Join(Home(), appliedDirName)
	if p.Project {
		if root == "" {
			return "", fmt.Errorf("preset %s is in a project, and no project is open", p.Name)
		}
		dir = filepath.Join(ProjectDir(root), appliedDirName)
	}
	return filepath.Join(dir, p.Name+".json"), nil
}

// loadApplied reads the record for p. A missing record is empty.
func loadApplied(path string) (AppliedPresetCommands, error) {
	var rec AppliedPresetCommands
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return rec, nil
	}
	if err != nil {
		return rec, err
	}
	if err := json.Unmarshal(b, &rec); err != nil {
		return rec, fmt.Errorf("preset record %s: %w", path, err)
	}
	return rec, nil
}

// RecordPresetCommands adds what a preset just wrote (added, as returned by
// SavePresetCommands) to its record. before is the scope file as it was
// before that write, so an overwritten server can be restored later. A
// second apply adds to the record rather than replacing it, and keeps the
// first original of an overwritten server.
func RecordPresetCommands(root string, p Preset, before Config, added PresetCommands) error {
	if added.Empty() {
		return nil
	}
	path, err := appliedPath(root, p)
	if err != nil {
		return err
	}
	rec, err := loadApplied(path)
	if err != nil {
		return err
	}
	for _, h := range added.Hooks {
		if !containsHook(rec.Hooks, h) {
			rec.Hooks = append(rec.Hooks, h)
		}
	}
	for _, m := range added.MCP {
		rec.MCP = replaceMCPByName(rec.MCP, m)
		if !added.Replaced[m.Name] {
			continue
		}
		if _, ok := findMCP(rec.Replaced, m.Name); ok {
			continue
		}
		if orig, ok := findMCP(before.MCP, m.Name); ok {
			rec.Replaced = append(rec.Replaced, orig)
		}
	}
	return writeApplied(path, rec)
}

// RevertPresetCommands takes back what p wrote into its scope's config file:
// recorded hooks are removed, recorded servers are removed (or restored, when
// they replaced one), and an entry the user has edited since is left alone.
// The record is deleted afterwards. With no record it does nothing.
func RevertPresetCommands(root string, p Preset) (PresetCommands, error) {
	out := PresetCommands{Replaced: map[string]bool{}}
	path, err := appliedPath(root, p)
	if err != nil {
		return out, err
	}
	rec, err := loadApplied(path)
	if err != nil {
		return out, err
	}
	if len(rec.Hooks) == 0 && len(rec.MCP) == 0 {
		return out, removeApplied(path)
	}
	cur, err := LoadPresetScope(root, p)
	if err != nil {
		return out, err
	}
	for _, h := range rec.Hooks {
		if i := indexHook(cur.Hooks, h); i >= 0 {
			cur.Hooks = append(cur.Hooks[:i], cur.Hooks[i+1:]...)
			out.Hooks = append(out.Hooks, h)
		}
	}
	for _, m := range rec.MCP {
		i := indexMCP(cur.MCP, m.Name)
		if i < 0 || !sameMCP(cur.MCP[i], m) {
			continue
		}
		if orig, ok := findMCP(rec.Replaced, m.Name); ok {
			cur.MCP[i] = orig
			out.Replaced[m.Name] = true
		} else {
			cur.MCP = append(cur.MCP[:i], cur.MCP[i+1:]...)
		}
		out.MCP = append(out.MCP, m)
	}
	if len(out.Hooks) > 0 || len(out.MCP) > 0 {
		if p.Project {
			err = SaveProject(root, cur)
		} else {
			err = SaveGlobal(cur)
		}
		if err != nil {
			return out, err
		}
	}
	return out, removeApplied(path)
}

// replaceMCPByName returns list with the entry of m's name replaced by m, or
// m appended when the name is new.
func replaceMCPByName(list []MCPServer, m MCPServer) []MCPServer {
	if i := indexMCP(list, m.Name); i >= 0 {
		list[i] = m
		return list
	}
	return append(list, m)
}

func indexHook(list []Hook, h Hook) int {
	for i, x := range list {
		if x == h {
			return i
		}
	}
	return -1
}

func indexMCP(list []MCPServer, name string) int {
	for i, m := range list {
		if m.Name == name {
			return i
		}
	}
	return -1
}

// writeApplied saves a record, creating its directory privately.
func writeApplied(path string, rec AppliedPresetCommands) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o600)
}

// removeApplied deletes a record. A record that is already gone is fine.
func removeApplied(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
