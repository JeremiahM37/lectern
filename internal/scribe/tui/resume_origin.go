package tui

import "github.com/JeremiahM37/lectern/v2/internal/scribe/tui/chat"

// SessionOriginer is implemented by backends that can say which project
// folder a session was saved in. A resumed session from another folder gets
// a header saying so (spec 06 changelog 2.17.0: sessions show headers only
// when foreign or post-compaction).
type SessionOriginer interface {
	SessionWorkdir() string
}

// applyForeignHeader sets the foreign-session header on t's transcript after
// a replay. It only sets a non-empty header, so a post-compaction header from
// chat.View.Restore is kept when the session is in the current folder.
func applyForeignHeader(t *tab) {
	o, ok := t.be.(SessionOriginer)
	if !ok {
		return
	}
	if h := chat.ForeignSessionHeader(o.SessionWorkdir(), t.be.Workdir()); h != "" {
		t.chat.SetSessionHeader(h)
	}
}
