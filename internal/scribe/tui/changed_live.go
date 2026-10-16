package tui

// changedNow is the files this tab's turns changed that git still reports as
// changed: created first, then modified, as changedFiles orders them. The
// transcript remembers every file a turn touched, so a file committed since
// then is dropped here. Without git (no repository, or the status has not
// been read) the transcript's list stands. It reads only the cached status,
// so it costs a slice lookup per row and nothing on disk.
func (t *tab) changedNow() (created, modified []string) {
	created, modified = t.chat.ChangedFiles()
	if t.git.Branch == "" {
		return created, modified
	}
	live := make(map[string]bool, len(t.git.Changed))
	for _, f := range t.git.Changed {
		live[f] = true
	}
	return keepLive(created, live), keepLive(modified, live)
}

// keepLive returns the paths in files that are in live, in order.
func keepLive(files []string, live map[string]bool) []string {
	var out []string
	for _, f := range files {
		if live[f] {
			out = append(out, f)
		}
	}
	return out
}
