package pluginpkg

import (
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"sync"
)

// bundledFS holds the plugins that ship inside the binary: the agent catalog
// and the three workflows. The all: prefix keeps upstream files whose names
// start with '.' or '_'; they are part of the pinned source.
//
//go:embed all:bundled
var bundledFS embed.FS

var (
	bundledOnce sync.Once
	bundled     []*Package
	bundledErr  error
)

// Bundled returns the binary's own plugins, sorted by id. They are parsed
// and validated once; an invalid bundled plugin is a build defect, which the
// package tests catch before it ships.
func Bundled() ([]*Package, error) {
	bundledOnce.Do(func() { bundled, bundledErr = loadBundled() })
	return bundled, bundledErr
}

// BundledPackage returns one bundled plugin by id.
func BundledPackage(id string) (*Package, bool) {
	pkgs, err := Bundled()
	if err != nil {
		return nil, false
	}
	for _, p := range pkgs {
		if p.Manifest.ID == id {
			return p, true
		}
	}
	return nil, false
}

func loadBundled() ([]*Package, error) {
	dirs, err := fs.ReadDir(bundledFS, "bundled")
	if err != nil {
		return nil, err
	}
	var out []*Package
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		files, err := embeddedFiles(bundledFS, "bundled/"+d.Name())
		if err != nil {
			return nil, err
		}
		pkg, err := Load(files, true)
		if err != nil {
			return nil, fmt.Errorf("bundled plugin %s: %w", d.Name(), err)
		}
		if !pkg.Manifest.Bundled() {
			return nil, fmt.Errorf("bundled plugin %s must use the %q prefix", d.Name(), ReservedPrefix)
		}
		out = append(out, pkg)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Manifest.ID < out[j].Manifest.ID })
	return out, nil
}

func embeddedFiles(fsys fs.FS, root string) ([]File, error) {
	var out []File
	err := fs.WalkDir(fsys, root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("bundled file %q is not regular", p)
		}
		data, err := fs.ReadFile(fsys, p)
		if err != nil {
			return err
		}
		out = append(out, File{Path: strings.TrimPrefix(p, root+"/"), Mode: EmbeddedMode(p), Data: data})
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, err
}

// EmbeddedMode is the mode a bundled file gets. An embed does not keep the
// executable bit, and workflow helpers are run as programs, so the script
// formats that ship are made executable explicitly — the same rule the
// workflows package always used, so staged workflow digests are unchanged.
func EmbeddedMode(p string) uint32 {
	if strings.HasSuffix(p, ".py") || strings.HasSuffix(p, ".sh") {
		return 0o755
	}
	return 0o644
}
