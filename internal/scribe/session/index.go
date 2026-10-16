package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/config"
)

// indexMu serialises changes to this process's copy of the global index. Two
// processes writing at once can still lose an entry; the index is a lookup
// aid, and a lost entry only means --resume needs the project directory.
var indexMu sync.Mutex

func indexPath() string { return filepath.Join(config.Home(), "sessions.json") }

// readIndex returns id -> project root. A missing or unreadable index is
// treated as empty.
func readIndex() map[string]string {
	idx := map[string]string{}
	b, err := os.ReadFile(indexPath())
	if err != nil || len(b) == 0 {
		return idx
	}
	_ = json.Unmarshal(b, &idx)
	return idx
}

func writeIndex(idx map[string]string) error {
	b, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(indexPath(), append(b, '\n'), 0o600)
}

func registerGlobal(id, root string) error {
	indexMu.Lock()
	defer indexMu.Unlock()
	idx := readIndex()
	idx[id] = root
	return writeIndex(idx)
}

func unregisterGlobal(id string) error {
	indexMu.Lock()
	defer indexMu.Unlock()
	idx := readIndex()
	if _, ok := idx[id]; !ok {
		return nil
	}
	delete(idx, id)
	return writeIndex(idx)
}

// FindGlobal resolves an id or unique prefix across all projects using the
// global index. It returns the project's Store and the session's Meta, so
// `lectern code --resume <id>` works from any directory.
func FindGlobal(idPrefix string) (*Store, Meta, error) {
	prefix := strings.ToLower(strings.TrimSpace(idPrefix))
	if prefix == "" {
		return nil, Meta{}, fmt.Errorf("%w: empty id", ErrNotFound)
	}
	idx := readIndex()
	var hits []string
	for id := range idx {
		l := strings.ToLower(id)
		if l == prefix {
			hits = []string{id}
			break
		}
		if strings.HasPrefix(l, prefix) {
			hits = append(hits, id)
		}
	}
	switch len(hits) {
	case 0:
		return nil, Meta{}, fmt.Errorf("%w: %s", ErrNotFound, prefix)
	case 1:
		root := idx[hits[0]]
		st := NewStore(root)
		m, err := st.readMeta(hits[0])
		if err != nil {
			return nil, Meta{}, err
		}
		return st, m, nil
	default:
		sort.Strings(hits)
		return nil, Meta{}, fmt.Errorf("%w: %q matches %d sessions (%s)", ErrAmbiguous, prefix, len(hits), strings.Join(hits, ", "))
	}
}

// ListAll returns sessions from every project in the global index, newest
// first. Projects whose directory or session has gone are skipped.
func ListAll() []Meta {
	var out []Meta
	idx := readIndex()
	roots := map[string]bool{}
	for _, root := range idx {
		roots[root] = true
	}
	for root := range roots {
		for _, m := range NewStore(root).List() {
			if idx[m.ID] == root {
				out = append(out, m)
			}
		}
	}
	sortMeta(out)
	return out
}
