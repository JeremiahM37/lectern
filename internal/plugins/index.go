package plugins

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/pluginpkg"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

var sourceNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)

// AddSource registers a marketplace after reading its index once, so a
// broken URL is refused rather than saved.
func (m *Manager) AddSource(ctx context.Context, name, url, ref string) (*Index, error) {
	if !sourceNameRe.MatchString(name) {
		return nil, fmt.Errorf("source name must be lowercase letters, digits and '-'")
	}
	idx, err := m.readIndex(ctx, url, ref)
	if err != nil {
		return nil, err
	}
	if err := m.DB.SavePluginSource(&store.PluginSource{Name: name, URL: url, Ref: ref}); err != nil {
		return nil, err
	}
	m.mu.Lock()
	m.indexes[name] = idx
	m.mu.Unlock()
	return idx, nil
}

// RemoveSource forgets a marketplace. Plugins installed from it stay.
func (m *Manager) RemoveSource(name string) error {
	if err := m.DB.DeletePluginSource(name); err != nil {
		return err
	}
	m.mu.Lock()
	delete(m.indexes, name)
	m.mu.Unlock()
	return nil
}

func (m *Manager) readIndex(ctx context.Context, url, ref string) (*Index, error) {
	f, err := fetchGit(ctx, url, ref, "", "")
	if err != nil {
		return nil, err
	}
	idx, err := parseIndex(pluginpkg.FileMap(f.Files), url, f.Commit)
	if err != nil {
		return nil, err
	}
	idx.Commit = f.Commit
	return idx, nil
}

// indexFor returns a source's index, reading it when it is not cached or
// when refresh asks for the current one.
func (m *Manager) indexFor(ctx context.Context, s *store.PluginSource, refresh bool) (*Index, error) {
	m.mu.Lock()
	idx := m.indexes[s.Name]
	m.mu.Unlock()
	if idx != nil && !refresh {
		return idx, nil
	}
	idx, err := m.readIndex(ctx, s.URL, s.Ref)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	m.indexes[s.Name] = idx
	m.mu.Unlock()
	return idx, nil
}

// Listing is one plugin available from a source.
type Listing struct {
	IndexEntry
	Index     string `json:"index"`
	Installed string `json:"installed,omitempty"` // installed status, if it is
}

// Search lists plugins from every source whose id, name or description
// contains query. refresh re-reads each index.
func (m *Manager) Search(ctx context.Context, query string, refresh bool) ([]Listing, []string, error) {
	sources, err := m.DB.PluginSources()
	if err != nil {
		return nil, nil, err
	}
	q := strings.ToLower(strings.TrimSpace(query))
	var out []Listing
	var problems []string
	for _, s := range sources {
		idx, err := m.indexFor(ctx, s, refresh)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: %v", s.Name, err))
			continue
		}
		for _, e := range idx.Plugins {
			hay := strings.ToLower(e.ID + " " + e.Name + " " + e.Description)
			if q != "" && !strings.Contains(hay, q) {
				continue
			}
			l := Listing{IndexEntry: e, Index: s.Name}
			if p, ok := m.Get(e.ID); ok {
				l.Installed = p.Status
			}
			out = append(out, l)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, problems, nil
}

// lookup finds an id in one named source, or in every source when index is
// empty. An id listed by two sources must be installed naming one of them.
func (m *Manager) lookup(ctx context.Context, index, id string) (IndexEntry, string, error) {
	sources, err := m.DB.PluginSources()
	if err != nil {
		return IndexEntry{}, "", err
	}
	var found []IndexEntry
	var where []string
	for _, s := range sources {
		if index != "" && s.Name != index {
			continue
		}
		// An install reads the index fresh: the pinned commit is what gets
		// installed, and it must be the one listed now.
		idx, err := m.indexFor(ctx, s, true)
		if err != nil {
			return IndexEntry{}, "", fmt.Errorf("source %s: %w", s.Name, err)
		}
		for _, e := range idx.Plugins {
			if e.ID == id {
				found = append(found, e)
				where = append(where, s.Name)
			}
		}
	}
	switch len(found) {
	case 0:
		if index != "" {
			return IndexEntry{}, "", fmt.Errorf("source %s does not list %s", index, id)
		}
		return IndexEntry{}, "", fmt.Errorf("no source lists %s; add one with `lectern plugin source add`", id)
	case 1:
		return found[0], where[0], nil
	}
	return IndexEntry{}, "", fmt.Errorf("%s is listed by %s; say which source to install from", id, strings.Join(where, " and "))
}

// Sources lists marketplaces with when their index was read.
func (m *Manager) Sources() ([]map[string]any, error) {
	rows, err := m.DB.PluginSources()
	if err != nil {
		return nil, err
	}
	out := []map[string]any{}
	for _, s := range rows {
		row := map[string]any{"name": s.Name, "url": s.URL, "ref": s.Ref, "added_at": s.AddedAt}
		m.mu.Lock()
		if idx := m.indexes[s.Name]; idx != nil {
			row["commit"], row["plugins"] = idx.Commit, len(idx.Plugins)
		}
		m.mu.Unlock()
		out = append(out, row)
	}
	return out, nil
}
