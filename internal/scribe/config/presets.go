package config

// Presets are named partial configs. A preset is one JSON file whose keys
// are Config keys; it is merged over the loaded config for one run (or, from
// the /preset command, written to the scope the preset lives in).
//
// Locations: <Home>/presets/NAME.json (global) and <root>/.scribe/presets/
// NAME.json (project). When both define the same name the project file wins.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// MaxPresetBytes caps one preset file. Presets are settings, not data.
const MaxPresetBytes = 512 << 10

var presetName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// Preset is one loaded preset.
type Preset struct {
	Name    string
	Path    string
	Project bool   // lives in the project's .scribe/presets
	Config  Config // the partial config; unset fields leave the loaded value alone
}

// Scope names where the preset is defined: "global" or "project".
func (p Preset) Scope() string {
	if p.Project {
		return "project"
	}
	return "global"
}

// PresetDir is the global presets directory.
func PresetDir() string { return filepath.Join(Home(), "presets") }

// ProjectPresetDir is the project presets directory for root.
func ProjectPresetDir(root string) string { return filepath.Join(ProjectDir(root), "presets") }

// ListPresets returns every valid preset visible from root, sorted by name.
// Files that cannot be used are skipped and reported in the error slice, so
// one bad file never hides the others.
func ListPresets(root string) ([]Preset, []error) {
	var errs []error
	byName := map[string]Preset{}
	// Global first, then project, so the project file replaces a global one.
	for _, src := range []struct {
		dir     string
		project bool
	}{{PresetDir(), false}, {ProjectPresetDir(root), true}} {
		if root == "" && src.project {
			continue
		}
		ps, es := readPresetDir(src.dir, src.project)
		errs = append(errs, es...)
		for _, p := range ps {
			byName[p.Name] = p
		}
	}
	out := make([]Preset, 0, len(byName))
	for _, p := range byName {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, errs
}

// readPresetDir loads the *.json files in dir. A missing directory is fine.
func readPresetDir(dir string, project bool) ([]Preset, []error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, []error{err}
	}
	var out []Preset
	var errs []error
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		p, err := readPreset(path, project)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		out = append(out, p)
	}
	return out, errs
}

// readPreset reads one preset file, refusing symlinks, non-regular files and
// names the /preset command cannot address.
func readPreset(path string, project bool) (Preset, error) {
	name := strings.TrimSuffix(filepath.Base(path), ".json")
	if !presetName.MatchString(name) {
		return Preset{}, fmt.Errorf("preset %s: name must be lowercase letters, digits and dashes", path)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return Preset{}, err
	}
	if !info.Mode().IsRegular() {
		return Preset{}, fmt.Errorf("preset %s: not a regular file", path)
	}
	if info.Size() > MaxPresetBytes {
		return Preset{}, fmt.Errorf("preset %s: larger than %d KB", path, MaxPresetBytes>>10)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return Preset{}, err
	}
	var c Config
	b = expandPresetEnv(b)
	if len(strings.TrimSpace(string(b))) > 0 {
		if err := json.Unmarshal(b, &c); err != nil {
			return Preset{}, fmt.Errorf("preset %s: %w", path, err)
		}
	}
	return Preset{Name: name, Path: path, Project: project, Config: c}, nil
}

// presetEnvRef matches ${NAME} placeholders in a preset file.
var presetEnvRef = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// expandPresetEnv replaces ${NAME} with the environment variable NAME, so a
// preset can name a secret or a host without storing it. A missing variable
// is the empty string. Values are escaped for a JSON string, so placeholders
// belong inside string values (for example "base_url": "${HOST}/v1").
func expandPresetEnv(b []byte) []byte {
	return presetEnvRef.ReplaceAllFunc(b, func(m []byte) []byte {
		name := presetEnvRef.FindSubmatch(m)[1]
		esc, _ := json.Marshal(os.Getenv(string(name)))
		return esc[1 : len(esc)-1]
	})
}

// FindPreset returns the preset called name, or an error that lists the ones
// that exist.
func FindPreset(root, name string) (Preset, error) {
	all, _ := ListPresets(root)
	var names []string
	for _, p := range all {
		if p.Name == name {
			return p, nil
		}
		names = append(names, p.Name)
	}
	if len(names) == 0 {
		return Preset{}, fmt.Errorf("no preset %q (none are defined; add one to %s)", name, PresetDir())
	}
	return Preset{}, fmt.Errorf("no preset %q (have: %s)", name, strings.Join(names, ", "))
}

// RemovePreset deletes the file a preset was read from. It only removes a
// regular file that sits directly in the presets directory of its scope (the
// global one, or the project's when p.Project is set), so a preset value
// cannot point a removal anywhere else. Other presets of the same name, in
// the other scope, are left alone.
func RemovePreset(root string, p Preset) error {
	dir := PresetDir()
	if p.Project {
		if root == "" {
			return fmt.Errorf("preset %s is in a project, and no project is open", p.Name)
		}
		dir = ProjectPresetDir(root)
	}
	if !presetName.MatchString(p.Name) || filepath.Dir(p.Path) != dir || filepath.Base(p.Path) != p.Name+".json" {
		return fmt.Errorf("preset %s is not a preset file this command may remove", p.Name)
	}
	info, err := os.Lstat(p.Path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("preset %s: not a regular file", p.Path)
	}
	return os.Remove(p.Path)
}

// ApplyPreset merges p over base. Preset values win where set; everything
// else is kept, with the same rules as the global-over-project merge.
func ApplyPreset(base Config, p Preset) Config { return Merge(base, p.Config) }

// LoadWithPreset is Load followed by ApplyPreset. An empty name is plain Load.
func LoadWithPreset(root, name string) (Config, error) {
	c, err := Load(root)
	if err != nil || name == "" {
		return c, err
	}
	p, err := FindPreset(root, name)
	if err != nil {
		return Config{}, err
	}
	return ApplyPreset(c, p), nil
}
