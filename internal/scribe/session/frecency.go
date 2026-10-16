package session

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/config"
)

// Pick kinds. Each has its own ranking; a key is a file path, an agent name,
// a command name or a model id.
const (
	PickFile    = "file"
	PickAgent   = "agent"
	PickCommand = "command"
	PickModel   = "model"
)

// FrecencyCap bounds how many keys are remembered per kind. The least
// ranked keys are dropped first.
const FrecencyCap = 500

// frecencyNow is replaced in tests.
var frecencyNow = time.Now

type pickEntry struct {
	Kind  string    `json:"kind"`
	Key   string    `json:"key"`
	Count int       `json:"count"`
	Last  time.Time `json:"last"`
}

var frecencyMu sync.Mutex

func frecencyPath() string { return filepath.Join(config.Home(), "frecency.json") }

// frecencyScore ranks by how often a key was picked, discounted by how long
// ago it was last picked: count / (1 + days). A pick today counts in full; a
// pick a month ago counts for about a thirtieth as much as a fresh one.
func frecencyScore(count int, last, now time.Time) float64 {
	days := now.Sub(last).Hours() / 24
	if days < 0 {
		days = 0
	}
	return float64(count) / (1 + days)
}

// readPicks loads every entry. A missing file is empty; an unreadable file is
// an error so that a pick is not silently lost to a bad write.
func readPicks() ([]pickEntry, error) {
	b, err := os.ReadFile(frecencyPath())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []pickEntry
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// RecordPick notes that the person picked key of kind now. Blank keys are
// ignored.
func RecordPick(kind, key string) error {
	if key == "" {
		return nil
	}
	frecencyMu.Lock()
	defer frecencyMu.Unlock()
	now := frecencyNow().UTC()
	entries, err := readPicks()
	if err != nil {
		return err
	}
	found := false
	for i := range entries {
		if entries[i].Kind == kind && entries[i].Key == key {
			entries[i].Count++
			entries[i].Last = now
			found = true
			break
		}
	}
	if !found {
		entries = append(entries, pickEntry{Kind: kind, Key: key, Count: 1, Last: now})
	}
	entries = capKind(entries, kind, now)
	if err := os.MkdirAll(filepath.Dir(frecencyPath()), 0o700); err != nil {
		return err
	}
	out, err := json.Marshal(entries)
	if err != nil {
		return err
	}
	return atomicWrite(frecencyPath(), out, 0o600)
}

// capKind keeps at most FrecencyCap entries of kind, dropping the lowest
// scoring ones. Entries of other kinds are untouched.
func capKind(entries []pickEntry, kind string, now time.Time) []pickEntry {
	var same []pickEntry
	var rest []pickEntry
	for _, e := range entries {
		if e.Kind == kind {
			same = append(same, e)
		} else {
			rest = append(rest, e)
		}
	}
	if len(same) <= FrecencyCap {
		return entries
	}
	sort.SliceStable(same, func(i, j int) bool {
		return frecencyScore(same[i].Count, same[i].Last, now) > frecencyScore(same[j].Count, same[j].Last, now)
	})
	return append(rest, same[:FrecencyCap]...)
}

// TopPicks returns up to n keys of kind, best ranked first (n <= 0 means all).
// Ties are broken by key so the order is stable.
func TopPicks(kind string, n int) ([]string, error) {
	frecencyMu.Lock()
	entries, err := readPicks()
	frecencyMu.Unlock()
	if err != nil {
		return nil, err
	}
	now := frecencyNow().UTC()
	var same []pickEntry
	for _, e := range entries {
		if e.Kind == kind {
			same = append(same, e)
		}
	}
	sort.SliceStable(same, func(i, j int) bool {
		si := frecencyScore(same[i].Count, same[i].Last, now)
		sj := frecencyScore(same[j].Count, same[j].Last, now)
		if si != sj {
			return si > sj
		}
		return same[i].Key < same[j].Key
	})
	if n > 0 && len(same) > n {
		same = same[:n]
	}
	out := make([]string, 0, len(same))
	for _, e := range same {
		out = append(out, e.Key)
	}
	return out, nil
}
