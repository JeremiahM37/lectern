package app

import "github.com/JeremiahM37/lectern/v2/internal/scribe/tui"

// LSPSessions implements tui.LSPSessioner. It lists the language servers the
// workspace's LSP manager has started, is starting, or failed to start.
func (b *scribeBackend) LSPSessions() []tui.LSPSession {
	a := b.current()
	if a.ext.lsp == nil {
		return nil
	}
	var out []tui.LSPSession
	for _, s := range a.ext.lsp.Sessions() {
		out = append(out, tui.LSPSession{
			Language: s.Language, Command: s.Command, Root: s.Root, State: s.State,
			LastError: s.LastError, PID: s.PID, OpenDocs: s.OpenDocs, Since: s.Since,
		})
	}
	return out
}
