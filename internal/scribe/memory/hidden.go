package memory

// Hide soft-deletes the record idOrPrefix: it stays on disk, but recall,
// search, list and dedupe no longer see it until Restore. Hiding a record
// that is already hidden is a no-op.
func (d *DB) Hide(idOrPrefix string) error {
	return d.setHidden(idOrPrefix, true)
}

// Restore brings a hidden record back into recall, search and list.
func (d *DB) Restore(idOrPrefix string) error {
	return d.setHidden(idOrPrefix, false)
}

func (d *DB) setHidden(idOrPrefix string, hidden bool) error {
	id, err := resolveID(d.db, idOrPrefix)
	if err != nil {
		return err
	}
	_, err = d.db.Exec("UPDATE memories SET hidden = ?, updated = ? WHERE id = ?", boolInt(hidden), fmtTime(d.now()), id)
	return err
}

// ListHidden returns the hidden records of this store, most recently
// updated first. Superseded records are not included.
func (d *DB) ListHidden(limit int) ([]Record, error) {
	if limit <= 0 {
		limit = 100
	}
	return d.queryRecords("SELECT "+cols+" FROM memories WHERE hidden = 1 AND superseded_by = '' ORDER BY updated DESC, id LIMIT ?", limit)
}

// SetSession stamps records saved from now on with id. An empty id stops
// stamping. It does not touch existing records.
func (d *DB) SetSession(id string) { d.session = id }

// SetSession binds both stores to the session id and, when dir is not "",
// loads and persists the set of memories already surfaced in that session
// (see Inject). Switching to a different session forgets the previous set.
func (s *Stores) SetSession(id, dir string) error {
	for _, d := range s.DBs() {
		d.SetSession(id)
	}
	s.session = id
	s.surfMu.Lock()
	defer s.surfMu.Unlock()
	s.surfacedDir = dir
	s.surfaced = map[string]bool{}
	if dir == "" {
		return nil
	}
	ids, err := readSurfaced(dir)
	if err != nil {
		return err
	}
	for _, x := range ids {
		s.surfaced[x] = true
	}
	return nil
}

// Session is the session id set by SetSession, or "".
func (s *Stores) Session() string { return s.session }

// ResetSurfaced forgets which memories were surfaced in this session, so the
// next matching prompt may inject them again. Call it when the model no
// longer has the earlier injections in its context, such as after compaction.
func (s *Stores) ResetSurfaced() error {
	s.surfMu.Lock()
	defer s.surfMu.Unlock()
	s.surfaced = map[string]bool{}
	if s.surfacedDir == "" {
		return nil
	}
	return writeSurfaced(s.surfacedDir, nil)
}

// Surfaced reports whether id was already injected in this session.
func (s *Stores) Surfaced(id string) bool {
	s.surfMu.Lock()
	defer s.surfMu.Unlock()
	return s.surfaced[id]
}
