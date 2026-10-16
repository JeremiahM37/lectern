package remote

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// Allowlist is the set of chats allowed to talk to Scribe on one surface.
// Static ids come from config and are never written. Ids added by pairing
// are saved to a state file (mode 0600) so a pairing survives a restart.
// Surfaces default-deny: an empty allowlist admits no chat.
type Allowlist struct {
	mu     sync.Mutex
	path   string
	static map[string]bool
	paired map[string]bool
}

type allowlistFile struct {
	Chats []string `json:"chats"`
}

// NewAllowlist loads the paired ids from path (a missing file is empty) and
// merges static. An empty path keeps pairings in memory only.
func NewAllowlist(path string, static []string) (*Allowlist, error) {
	a := &Allowlist{path: path, static: map[string]bool{}, paired: map[string]bool{}}
	for _, id := range static {
		if id = strings.TrimSpace(id); id != "" {
			a.static[id] = true
		}
	}
	if path == "" {
		return a, nil
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return a, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read chat allowlist: %w", err)
	}
	if len(b) == 0 {
		return a, nil
	}
	var f allowlistFile
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("chat allowlist %s is not valid JSON: %w", path, err)
	}
	for _, id := range f.Chats {
		a.paired[id] = true
	}
	return a, nil
}

// Allowed reports whether chat may use the surface.
func (a *Allowlist) Allowed(chat string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.static[chat] || a.paired[chat]
}

// Add pairs chat and saves the pairing. Adding a chat twice is fine.
func (a *Allowlist) Add(chat string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.paired[chat] = true
	return a.saveLocked()
}

// Members returns every allowed chat id, sorted.
func (a *Allowlist) Members() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	seen := map[string]bool{}
	var out []string
	for id := range a.static {
		seen[id] = true
	}
	for id := range a.paired {
		seen[id] = true
	}
	for id := range seen {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

func (a *Allowlist) saveLocked() error {
	if a.path == "" {
		return nil
	}
	ids := make([]string, 0, len(a.paired))
	for id := range a.paired {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	b, err := json.MarshalIndent(allowlistFile{Chats: ids}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(a.path), 0o700); err != nil {
		return err
	}
	tmp := a.path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, a.path)
}
