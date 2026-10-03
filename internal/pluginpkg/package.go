package pluginpkg

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// Limits on one plugin, as Orca and most registries set them: enough for a
// vendored upstream workflow (Spec Kit is 25 files, 400 KiB), small enough
// that hashing on every hook run costs nothing.
const (
	MaxFiles = 2000
	MaxBytes = 50 << 20
)

// File is one regular file of a package. Mode is 0o755 or 0o644: only the
// executable bit survives, because that is all git and an embed keep.
type File struct {
	Path string `json:"path"`
	Mode uint32 `json:"mode"`
	Data []byte `json:"-"`
}

// Package is a validated plugin: its manifest, files and content hash.
type Package struct {
	Manifest *Manifest `json:"manifest"`
	Files    []File    `json:"-"`
	Hash     string    `json:"hash"`
	// Format is "lectern", or "claude" for a Claude Code plugin read through
	// FromClaude; Skipped then says what of it was not imported.
	Format  string   `json:"format"`
	Skipped []string `json:"skipped,omitempty"`
}

// ContentHash is what consent is pinned to: sha256 over every file's path,
// mode and bytes, in path order. Any edit, rename or chmod changes it.
func ContentHash(files []File) string {
	sorted := append([]File(nil), files...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Path < sorted[j].Path })
	h := sha256.New()
	for _, f := range sorted {
		fmt.Fprintf(h, "%s\x00%o\x00%d\x00", f.Path, f.Mode, len(f.Data))
		h.Write(f.Data)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// FileMap indexes files by path.
func FileMap(files []File) map[string]File {
	out := make(map[string]File, len(files))
	for _, f := range files {
		out[f.Path] = f
	}
	return out
}

// Load builds a package from its files: a lectern-plugin.yaml, or failing
// that a Claude Code plugin. bundled permits the reserved id prefix.
func Load(files []File, bundled bool) (*Package, error) {
	if err := checkFiles(files); err != nil {
		return nil, err
	}
	byPath := FileMap(files)
	var (
		m       *Manifest
		format  = "lectern"
		skipped []string
		err     error
	)
	if mf, ok := byPath[ManifestName]; ok {
		m, err = ParseManifest(mf.Data)
	} else if _, ok := byPath[ClaudeManifest]; ok {
		format = "claude"
		m, skipped, err = FromClaude(byPath)
	} else {
		return nil, fmt.Errorf("no %s (or .claude-plugin/plugin.json) at the plugin root", ManifestName)
	}
	if err != nil {
		return nil, err
	}
	if err := m.Validate(byPath, bundled); err != nil {
		return nil, err
	}
	return &Package{Manifest: m, Files: files, Hash: ContentHash(files), Format: format, Skipped: skipped}, nil
}

func checkFiles(files []File) error {
	if len(files) > MaxFiles {
		return fmt.Errorf("plugin has %d files; the limit is %d", len(files), MaxFiles)
	}
	total := 0
	seen := map[string]bool{}
	for _, f := range files {
		total += len(f.Data)
		if err := checkRel(f.Path); err != nil || path.Clean(f.Path) != f.Path {
			return fmt.Errorf("unsafe file path %q in plugin", f.Path)
		}
		if seen[f.Path] {
			return fmt.Errorf("file %q appears twice", f.Path)
		}
		seen[f.Path] = true
		if f.Mode != 0o644 && f.Mode != 0o755 {
			return fmt.Errorf("file %q has mode %o; only 644 and 755 are kept", f.Path, f.Mode)
		}
	}
	if total > MaxBytes {
		return fmt.Errorf("plugin is %d bytes; the limit is %d", total, MaxBytes)
	}
	return nil
}

// ReadDir reads a plugin directory. Symlinks, devices and anything else that
// is not a regular file or directory are refused rather than followed; a
// top-level .git is skipped (a git source records its commit separately).
func ReadDir(root string) ([]File, error) {
	var out []File
	total := 0
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if rel == "." {
			return nil
		}
		if d.IsDir() {
			if rel == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return fmt.Errorf("%s is a symlink; plugins may not contain symlinks", rel)
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("%s is not a regular file", rel)
		}
		if len(out) >= MaxFiles {
			return fmt.Errorf("plugin has more than %d files", MaxFiles)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		total += int(info.Size())
		if total > MaxBytes {
			return fmt.Errorf("plugin is larger than %d bytes", MaxBytes)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		mode := uint32(0o644)
		if info.Mode().Perm()&0o111 != 0 {
			mode = 0o755
		}
		out = append(out, File{Path: rel, Mode: mode, Data: data})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

// WriteDir writes files under root, which must not exist yet. The copy is
// read-only: nothing is meant to edit an installed plugin in place.
func WriteDir(root string, files []File) error {
	if err := os.MkdirAll(filepath.Dir(root), 0o755); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(filepath.Dir(root), ".staging-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	for _, f := range files {
		dst := filepath.Join(tmp, filepath.FromSlash(f.Path))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		mode := os.FileMode(0o444)
		if f.Mode&0o111 != 0 {
			mode = 0o555
		}
		if err := os.WriteFile(dst, f.Data, mode); err != nil {
			return err
		}
	}
	if err := os.Rename(tmp, root); err != nil {
		if _, statErr := os.Stat(root); statErr == nil {
			return nil // another install of the same content won the race
		}
		return err
	}
	return nil
}

// SubFiles returns the files under dir, with paths relative to it.
func SubFiles(files []File, dir string) []File {
	prefix := strings.TrimSuffix(dir, "/") + "/"
	var out []File
	for _, f := range files {
		if strings.HasPrefix(f.Path, prefix) {
			out = append(out, File{Path: strings.TrimPrefix(f.Path, prefix), Mode: f.Mode, Data: f.Data})
		}
	}
	return out
}

// Capability is one line of the consent preview: a capability and exactly
// what in the plugin uses it.
type Capability struct {
	Key    string   `json:"key"`
	Detail []string `json:"detail,omitempty"`
}

// CapabilityList is what a person reviews and consents to, derived from the
// declared capabilities and the contributions that use them. It is sorted
// and deterministic, so a consent can be compared with a later preview.
func (m *Manifest) CapabilityList() []Capability {
	c, caps := m.Contributes, m.Capabilities
	var out []Capability
	add := func(key string, on bool, detail []string) {
		if on {
			out = append(out, Capability{Key: key, Detail: detail})
		}
	}
	var host, target, mcp []string
	for _, h := range c.Hooks {
		line := h.Event + ": " + strings.Join(h.Command, " ")
		if h.Run == "host" {
			host = append(host, line)
		} else {
			target = append(target, line)
		}
	}
	for _, p := range c.SandboxProviders {
		host = append(host, "sandbox provider "+p.Name)
	}
	for _, name := range sortedKeys(c.MCPServers) {
		cfg := c.MCPServers[name]
		if cmd, _ := cfg["command"].(string); cmd != "" {
			args, _ := json.Marshal(cfg["args"])
			line := name + ": " + cmd
			if string(args) != "null" {
				line += " " + string(args)
			}
			target = append(target, "MCP server "+line)
			mcp = append(mcp, line)
		} else {
			u, _ := cfg["url"].(string)
			mcp = append(mcp, name+": "+u)
		}
	}
	var agents []string
	for _, raw := range c.Agents {
		var a struct{ Name, Command string }
		_ = json.Unmarshal(raw, &a)
		agents = append(agents, a.Name+" ("+a.Command+")")
	}
	add("host_exec", caps.HostExec, host)
	add("target_exec", caps.TargetExec, target)
	add("mcp_tools", caps.MCPTools, mcp)
	add("agents", caps.Agents, agents)
	var mods []string
	for _, md := range c.Mods {
		mods = append(mods, md.ID+" ("+md.Path+") in the "+strings.Join(md.SurfacesOf(), " and "))
	}
	add("mods", len(caps.Mods) > 0, mods)
	add("api", caps.API != "", []string{caps.API})
	add("network", len(caps.Network) > 0, append([]string(nil), caps.Network...))
	add("secrets", len(caps.Secrets) > 0, append([]string(nil), caps.Secrets...))
	add("notify", caps.Notify, nil)
	return out
}

// CapabilityKeys is the set a consent compares: key plus sorted detail.
func CapabilityKeys(list []Capability) []string {
	var out []string
	for _, c := range list {
		if len(c.Detail) == 0 {
			out = append(out, c.Key)
		}
		for _, d := range c.Detail {
			out = append(out, c.Key+": "+d)
		}
	}
	sort.Strings(out)
	return out
}

// Grown lists what next grants that prev did not.
func Grown(prev, next []Capability) []string {
	had := map[string]bool{}
	for _, k := range CapabilityKeys(prev) {
		had[k] = true
	}
	var out []string
	for _, k := range CapabilityKeys(next) {
		if !had[k] {
			out = append(out, k)
		}
	}
	return out
}
