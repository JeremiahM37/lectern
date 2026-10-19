package app

// SessionWorkdir is the project folder the current session was saved in. The
// TUI compares it with the folder the session is open in, to show the
// foreign-session header (tui/resume_origin.go).
func (b *scribeBackend) SessionWorkdir() string {
	a := b.current()
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.Meta.Workdir
}
