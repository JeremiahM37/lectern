package lsp

import "path/filepath"

// Active reports whether a language server that is already running serves
// abs. Unlike Diagnostics it never starts a server, so callers that only want
// to check a file the editor just changed do not pay a cold start.
func (m *Manager) Active(abs string) bool {
	abs = m.abs(abs)
	info, ok := languageForPath(abs)
	if !ok {
		return false
	}
	spec := serverSpecs[info.key]
	key := spec.Language + "\x00" + m.rootFor(spec, filepath.Clean(abs))
	m.mu.Lock()
	s := m.sessions[key]
	m.mu.Unlock()
	return s != nil && s.alive()
}
