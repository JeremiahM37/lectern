package plugins

import (
	"path"
	"slices"

	"github.com/JeremiahM37/lectern/v2/internal/pluginpkg"
)

// UIMod is one mod a surface loads (docs/mods.md). Script is the module
// already turned into the classic script both runtimes evaluate, from the
// consented, re-verified files; Hash pins it, so a runtime can tell that a
// mod changed and reload it.
type UIMod struct {
	Plugin string `json:"plugin"`
	ID     string `json:"id"`
	Name   string `json:"name"`
	API    string `json:"api,omitempty"`
	Hash   string `json:"hash"`
	Script string `json:"script,omitempty"`
	Error  string `json:"error,omitempty"`
}

// Mods lists the mods of every active plugin that run on surface ("web" or
// "cli"). A plugin whose files no longer match its consent contributes an
// error instead of code: nothing it ships runs until someone trusts it again.
func (m *Manager) Mods(surface string) []UIMod {
	out := []UIMod{}
	for _, p := range m.ActiveFor(0) {
		mf := p.Manifest
		if len(mf.Contributes.Mods) == 0 || !slices.Contains(mf.Capabilities.Mods, surface) {
			continue
		}
		verified := m.Verify(p)
		files := pluginpkg.FileMap(p.Pkg.Files)
		for _, md := range mf.Contributes.Mods {
			if !slices.Contains(md.SurfacesOf(), surface) {
				continue
			}
			hash := ""
			if p.Row != nil {
				hash = p.Row.ContentHash
			}
			mod := UIMod{Plugin: p.ID, ID: md.ID, Name: mf.Name, API: mf.Capabilities.API, Hash: hash}
			f, ok := files[path.Clean(md.Path)]
			switch {
			case !verified:
				mod.Error = "the plugin's files changed; trust it again in Settings → Plugins"
			case !ok:
				mod.Error = md.Path + " is not in the plugin"
			default:
				script, err := pluginpkg.ModScript(string(f.Data))
				if err != nil {
					mod.Error = err.Error()
				}
				mod.Script = script
			}
			out = append(out, mod)
		}
	}
	return out
}
