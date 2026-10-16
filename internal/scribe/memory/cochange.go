package memory

import (
	"sort"
	"strings"
)

// Co-change neighbours (spec 05 §1.6): files that changed in the same git
// commits as the file being edited. A memory linked to a neighbour is
// recalled too, with a weaker score than a direct file hit.
const (
	cochangePerFile  = 2   // neighbours taken from each edited file
	cochangeMax      = 6   // neighbours across all edited files
	cochangeScore    = 0.1 // score of a memory found only through a neighbour
	cochangeMaxAdded = 5   // extra memories a recall may add this way
)

// neighbours returns up to cochangeMax co-change partners of files, excluding
// the files themselves, two per file at most. It reads the repo map's cached
// co-change graph (CoChange), so no git command runs per recall.
func (s *Stores) neighbours(files []string) []string {
	if s.CoChange == nil || len(files) == 0 {
		return nil
	}
	seen := map[string]bool{}
	for _, f := range files {
		seen[normPath(f)] = true
	}
	var out []string
	for _, f := range files {
		taken := 0
		for _, nb := range s.CoChange(f) {
			p := normPath(nb)
			if seen[p] {
				continue
			}
			seen[p] = true
			out = append(out, p)
			taken++
			if taken == cochangePerFile || len(out) == cochangeMax {
				break
			}
		}
		if len(out) == cochangeMax {
			break
		}
	}
	return out
}

// addCoChange appends memories linked to neighbour files that are not already
// in got. got is returned sorted by score.
func (s *Stores) addCoChange(got []Scored, neighbours []string) []Scored {
	if len(neighbours) == 0 {
		return got
	}
	have := map[string]bool{}
	for _, g := range got {
		have[g.ID] = true
	}
	added := 0
	for _, d := range s.readDBs() {
		if added >= cochangeMaxAdded {
			break
		}
		ids, err := d.idsForFiles(neighbours)
		if err != nil {
			continue
		}
		for _, id := range ids {
			if have[id] || added >= cochangeMaxAdded {
				continue
			}
			rec, err := d.Get(id)
			if err != nil || rec.Hidden || rec.SupersededBy != "" {
				continue
			}
			have[id] = true
			got = append(got, Scored{Record: rec, Score: cochangeScore, Why: []string{"co-change"}})
			added++
		}
	}
	sort.SliceStable(got, func(i, j int) bool { return got[i].Score > got[j].Score })
	return got
}

// idsForFiles returns the live memory ids linked to any of files, by path match.
func (d *DB) idsForFiles(files []string) ([]string, error) {
	rows, err := d.db.Query("SELECT memory_id, path FROM memory_files")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	seen := map[string]bool{}
	for rows.Next() {
		var id, p string
		if err := rows.Scan(&id, &p); err != nil {
			return nil, err
		}
		if seen[id] {
			continue
		}
		for _, f := range files {
			if matchFile(p, f) {
				seen[id] = true
				ids = append(ids, id)
				break
			}
		}
	}
	return ids, rows.Err()
}

// normPath is a slash-separated path with no leading "./", for comparisons.
func normPath(p string) string {
	return strings.TrimPrefix(strings.ReplaceAll(p, "\\", "/"), "./")
}
