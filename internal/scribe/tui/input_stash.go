package tui

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/config"
)

// stashCap is how many drafts a directory keeps (spec 05 §3). The oldest
// is dropped first.
const stashCap = 50

// pushStash adds a draft as the newest stash, dropping the oldest drafts once
// the directory holds stashCap.
func (s *inputState) pushStash(text string) {
	s.stashes = append(s.stashes, text)
	if n := len(s.stashes); n > stashCap {
		s.stashes = append([]string(nil), s.stashes[n-stashCap:]...)
	}
}

// Stashes (Alt+S / Alt+P) are kept per working directory in stashes.json in the
// Scribe home, so a draft survives a restart. The file maps a directory to its
// stashes, oldest first.

// stashPath is the file holding every directory's stashes.
func stashPath() string { return filepath.Join(config.Home(), "stashes.json") }

// loadStashes returns the stashes for dir. A missing or unreadable file means none.
func loadStashes(dir string) []string {
	b, err := os.ReadFile(stashPath())
	if err != nil {
		return nil
	}
	all := map[string][]string{}
	if json.Unmarshal(b, &all) != nil {
		return nil
	}
	return all[dir]
}

// storeStashes writes dir's stashes, keeping every other directory's.
func storeStashes(dir string, list []string) error {
	all := map[string][]string{}
	if b, err := os.ReadFile(stashPath()); err == nil {
		_ = json.Unmarshal(b, &all)
	}
	if len(list) == 0 {
		delete(all, dir)
	} else {
		all[dir] = list
	}
	if err := os.MkdirAll(filepath.Dir(stashPath()), 0o755); err != nil {
		return err
	}
	b, err := json.Marshal(all)
	if err != nil {
		return err
	}
	return os.WriteFile(stashPath(), b, 0o644)
}

// mountStash loads the stashes for this tab's directory, once. When the input
// is empty the newest stash is restored and removed, so a draft comes back
// after a restart or a new tab in the same directory.
func (s *inputState) mountStash(dir string) {
	if s.mounted {
		return
	}
	s.mounted = true
	s.cwd = dir
	s.stashes = loadStashes(dir)
	if n := len(s.stashes); n > 0 && s.empty() {
		s.setValue(s.stashes[n-1])
		s.stashes = s.stashes[:n-1]
		_ = storeStashes(dir, s.stashes)
	}
}

// saveStashes writes this tab's stashes to disk.
func (s *inputState) saveStashes() error {
	return storeStashes(s.cwd, s.stashes)
}

// saveStashes reports a failed write to the error log; the stash still works
// for this session.
func (m *Model) saveStashes(t *tab) {
	if err := t.in.saveStashes(); err != nil {
		m.logError(t, "could not save stashed draft: "+err.Error())
	}
}
