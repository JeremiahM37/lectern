package memory

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// surfacedFile holds the ids injected into one session, next to its
// messages, so a resumed session does not inject them again.
const surfacedFile = "surfaced-memory.json"

// readSurfaced loads the surfaced ids of a session directory. A missing file
// is an empty set; a corrupt one is treated as empty rather than blocking the
// session.
func readSurfaced(dir string) ([]string, error) {
	b, err := os.ReadFile(filepath.Join(dir, surfacedFile))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var ids []string
	if json.Unmarshal(b, &ids) != nil {
		return nil, nil
	}
	return ids, nil
}

// writeSurfaced replaces the surfaced ids of a session directory atomically.
func writeSurfaced(dir string, ids []string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(ids)
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, surfacedFile+".tmp")
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, surfacedFile))
}

// rememberSurfaced adds ids to the set and persists it when a directory is
// bound. Persistence failures are ignored: the set still holds for this
// process, and a memory injected twice is only a cost in tokens.
func (s *Stores) rememberSurfaced(ids []string) {
	if len(ids) == 0 {
		return
	}
	s.surfMu.Lock()
	defer s.surfMu.Unlock()
	if s.surfaced == nil {
		s.surfaced = map[string]bool{}
	}
	for _, id := range ids {
		s.surfaced[id] = true
	}
	if s.surfacedDir == "" {
		return
	}
	all := make([]string, 0, len(s.surfaced))
	for id := range s.surfaced {
		all = append(all, id)
	}
	_ = writeSurfaced(s.surfacedDir, all)
}
