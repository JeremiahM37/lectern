package store

// UI preferences are one person's view settings — app theme, keyboard
// shortcuts, saved workspace layouts, quick commands — kept on the server so
// they follow that person to every device. Rows are keyed by owner (the
// signed-in login, or "operator" when there is no identity) and a short key;
// the value is opaque JSON the web app owns.

// UIPref is one stored preference.
type UIPref struct {
	Key       string  `json:"key"`
	Value     string  `json:"value"`
	UpdatedAt float64 `json:"updated_at"`
}

// UIPrefs returns every preference the owner has stored.
func (db *DB) UIPrefs(owner string) ([]UIPref, error) {
	rows, err := db.Query(`SELECT key, value, updated_at FROM ui_prefs WHERE owner=? ORDER BY key`, owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []UIPref{}
	for rows.Next() {
		var p UIPref
		if err := rows.Scan(&p.Key, &p.Value, &p.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// UIPrefCount is how many keys the owner has, for the per-owner cap.
func (db *DB) UIPrefCount(owner string) (int, error) {
	var n int
	err := db.QueryRow(`SELECT count(*) FROM ui_prefs WHERE owner=?`, owner).Scan(&n)
	return n, err
}

// SetUIPref upserts one preference.
func (db *DB) SetUIPref(owner, key, value string) (float64, error) {
	at := Now()
	_, err := db.Exec(`INSERT INTO ui_prefs(owner, key, value, updated_at) VALUES(?,?,?,?)
		ON CONFLICT(owner, key) DO UPDATE SET value=excluded.value, updated_at=excluded.updated_at`,
		owner, key, value, at)
	return at, err
}

// DeleteUIPref removes one preference; a missing row is not an error.
func (db *DB) DeleteUIPref(owner, key string) error {
	_, err := db.Exec(`DELETE FROM ui_prefs WHERE owner=? AND key=?`, owner, key)
	return err
}
