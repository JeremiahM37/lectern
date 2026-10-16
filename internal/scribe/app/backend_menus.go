package app

import "github.com/JeremiahM37/lectern/v2/internal/scribe/tui"

// The optional capabilities the TUI menus check for by type assertion (see
// tui/backend.go). Each method is implemented in its backend_*.go file; these
// assertions make a rename a compile error here instead of a silently missing
// menu entry.
var (
	_ tui.MarkdownExporter = (*scribeBackend)(nil)
	_ tui.GitRepoOps       = (*scribeBackend)(nil)
	_ tui.StagingCommitter = (*scribeBackend)(nil)
	_ tui.ContextClearer   = (*scribeBackend)(nil)
	_ tui.ConfigMover      = (*scribeBackend)(nil)
)
