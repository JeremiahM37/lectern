package memory

import "sort"

// DuplicateGroups groups the live records of this store whose normalised
// titles are the same (spec 05 §1.9 findDuplicates, title key). Each group
// is ordered keeper first: pinned before unpinned, then most recently used.
// Only groups of two or more are returned. Nothing is changed.
func (d *DB) DuplicateGroups(limit int) ([][]Record, error) {
	recs, err := d.List(ListOptions{Limit: limit})
	if err != nil {
		return nil, err
	}
	byKey := map[string][]Record{}
	var keys []string
	for _, r := range recs {
		k := normTitle(r.Title)
		if k == "" {
			continue
		}
		if _, ok := byKey[k]; !ok {
			keys = append(keys, k)
		}
		byKey[k] = append(byKey[k], r)
	}
	sort.Strings(keys)
	var out [][]Record
	for _, k := range keys {
		g := byKey[k]
		if len(g) < 2 {
			continue
		}
		// List returns pinned first, then most recently used, so the first
		// record of each group is already the keeper.
		out = append(out, g)
	}
	return out, nil
}
