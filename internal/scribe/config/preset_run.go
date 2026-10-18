package config

// Presets for one run. --plugin NAME, --preset NAME and SCRIBE_PRESETS=a,b
// apply presets to this run only: their values are merged over the loaded
// config in memory, and no config file is written. Hooks and MCP servers are
// left out of a run-only application. They run commands on this machine, and
// a preset from a cloned repository could carry them, so only /preset adds
// them, after a confirmation (preset_commands.go).

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
)

// PresetEnv names the presets to apply at start, separated by commas.
const PresetEnv = "SCRIBE_PRESETS"

// SplitPresetNames splits a list of preset names. Commas and spaces both
// separate names, so "a,b" and "a b" mean the same. Empty items are dropped,
// and a repeated name keeps its first place.
func SplitPresetNames(s string) []string {
	var out []string
	seen := map[string]bool{}
	for _, n := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' }) {
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	return out
}

// RunPresets is the set of presets one run applies, in apply order.
type RunPresets struct {
	Names    []string // SCRIBE_PRESETS first, then --plugin, then --preset
	Warnings []string // SCRIBE_PRESETS names that do not resolve; they are skipped
}

// ResolveRunPresets works out the presets for a run in root. A name in
// SCRIBE_PRESETS that does not resolve is skipped with a warning, so a stale
// variable never stops Scribe starting. A --plugin or --preset name that does
// not resolve is an error, because the user asked for it by name.
func ResolveRunPresets(root string, plugins []string, preset string) (RunPresets, error) {
	var rp RunPresets
	seen := map[string]bool{}
	add := func(name string) {
		if !seen[name] {
			seen[name] = true
			rp.Names = append(rp.Names, name)
		}
	}
	for _, name := range SplitPresetNames(os.Getenv(PresetEnv)) {
		if _, err := FindPreset(root, name); err != nil {
			rp.Warnings = append(rp.Warnings, fmt.Sprintf("%s: %v (skipped)", PresetEnv, err))
			continue
		}
		add(name)
	}
	asked := append(append([]string(nil), plugins...), preset)
	for _, v := range asked {
		for _, name := range SplitPresetNames(v) {
			if _, err := FindPreset(root, name); err != nil {
				return RunPresets{}, err
			}
			add(name)
		}
	}
	return rp, nil
}

// composePresets folds several presets into one, later names winning where
// they set a value. It backs a comma list given to FindPreset. The result has
// no single file, so Path is empty and Project is false.
func composePresets(root string, names []string) (Preset, error) {
	out := Preset{Name: strings.Join(names, ",")}
	for _, n := range names {
		p, err := FindPreset(root, n)
		if err != nil {
			return Preset{}, err
		}
		out.Config = Merge(out.Config, p.Config)
	}
	return out, nil
}

// PresetChange says what one preset does to a run's config.
type PresetChange struct {
	Name string
	// Keys are the config keys whose value the preset changes, sorted.
	Keys []string
	// Skipped names the parts of the preset a run does not apply ("hooks",
	// "mcp servers"), so the report can say why they are missing.
	Skipped []string
}

// DescribePresets applies names in order to base, as a run does, and reports
// what each preset changes. Values are not printed: a preset can carry a URL
// or a provider setting, and the key names are enough to see what changed.
func DescribePresets(root string, base Config, names []string) ([]PresetChange, error) {
	cur := base
	var out []PresetChange
	for _, n := range names {
		p, err := FindPreset(root, n)
		if err != nil {
			return nil, err
		}
		next := ApplyPreset(cur, p)
		keys, err := changedKeys(cur, next)
		if err != nil {
			return nil, err
		}
		c := PresetChange{Name: n, Keys: keys}
		if len(p.Config.Hooks) > 0 {
			c.Skipped = append(c.Skipped, "hooks")
		}
		if len(p.Config.MCP) > 0 {
			c.Skipped = append(c.Skipped, "mcp servers")
		}
		out = append(out, c)
		cur = next
	}
	return out, nil
}

// changedKeys compares the top-level JSON keys of two configs.
func changedKeys(a, b Config) ([]string, error) {
	ma, err := configObject(a)
	if err != nil {
		return nil, err
	}
	mb, err := configObject(b)
	if err != nil {
		return nil, err
	}
	var keys []string
	for k := range mb {
		if string(ma[k]) != string(mb[k]) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return keys, nil
}

// configObject encodes c as its top-level JSON members.
func configObject(c Config) (map[string]json.RawMessage, error) {
	b, err := json.Marshal(c)
	if err != nil {
		return nil, err
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	return m, nil
}

// runOnly is the part of a preset a run applies: everything except hooks and
// MCP servers, which only /preset may add.
func runOnly(c Config) Config {
	c.Hooks = nil
	c.MCP = nil
	return c
}

// RunPresetReport gives one line per applied preset saying what it changes,
// for --verbose-presets. Callers add their own prefix.
func RunPresetReport(root string, names []string) ([]string, error) {
	if len(names) == 0 {
		return []string{"no presets apply to this run"}, nil
	}
	base, err := Load(root)
	if err != nil {
		return nil, err
	}
	changes, err := DescribePresets(root, base, names)
	if err != nil {
		return nil, err
	}
	lines := make([]string, 0, len(changes))
	for _, c := range changes {
		line := "changes " + strings.Join(c.Keys, ", ")
		if len(c.Keys) == 0 {
			line = "changes nothing"
		}
		if len(c.Skipped) > 0 {
			line += "; not applied for a run: " + strings.Join(c.Skipped, ", ") + " (only /preset adds them, after a confirmation)"
		}
		lines = append(lines, fmt.Sprintf("preset %s: %s", c.Name, line))
	}
	return lines, nil
}
