package app

import (
	"fmt"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui"
)

// StaleMemories implements tui.Backend. It counts the memories the cleanup
// would review, across the project and global stores.
func (b *scribeBackend) StaleMemories() int {
	a := b.current()
	if a.Memory == nil {
		return 0
	}
	return a.Memory.CleanupHintCount()
}

// BackgroundErrors implements tui.Backend. It checks MCP and language servers
// for failures that are standing, posts the new ones, and returns everything
// queued since the last call.
func (b *scribeBackend) BackgroundErrors() []tui.BackgroundError {
	a := b.current()
	if a.ext.mcp != nil {
		for _, s := range a.ext.mcp.Status() {
			failed := string(s.State) == "failed"
			text := fmt.Sprintf("%s did not start: %s", s.Name, s.Error)
			if s.Error == "" {
				text = s.Name + " stopped responding"
			}
			b.errs.Sync("mcp:"+s.Name, "mcp", text, failed)
		}
	}
	if a.ext.lsp != nil {
		for _, s := range a.ext.lsp.Sessions() {
			failed := s.State == "failed"
			text := fmt.Sprintf("%s language server failed: %s", s.Language, s.LastError)
			b.errs.Sync("lsp:"+s.Language+":"+s.Root, "lsp", text, failed)
		}
	}
	return b.errs.Drain()
}

// noteTurnContext records the context fill at the end of the turn on its
// checkpoint, so browsing that checkpoint can show it. Called after every
// turn, once the checkpoint is closed.
func (a *App) noteTurnContext() {
	if a.Checks == nil {
		return
	}
	a.mu.Lock()
	turn := a.turn
	a.mu.Unlock()
	used, window := a.Agent.ContextUsed()
	_ = a.Checks.SetContext(turn, used, window)
}
