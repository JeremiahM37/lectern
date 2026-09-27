package store

import (
	"database/sql"
	"errors"
)

// Plugin is one row of plugins: an installed plugin and the consent that
// pins it (docs/plugins.md). ContentHash is what is installed; ConsentedHash
// is what a person agreed to. They differ only while a change waits for
// consent, and nothing of the plugin runs then.
type Plugin struct {
	ID                string   `json:"id"`
	SourceKind        string   `json:"source_kind"` // bundled | path | git | index
	Source            string   `json:"source"`
	Ref               string   `json:"ref"`
	IndexName         string   `json:"index_name"`
	Subdir            string   `json:"subdir"`
	CommitSHA         string   `json:"commit"`
	TreeSHA           string   `json:"tree"`
	ContentHash       string   `json:"content_hash"`
	Format            string   `json:"format"`
	ConsentedHash     string   `json:"consented_hash"`
	ConsentedCapsJSON string   `json:"-"`
	ConsentedBy       string   `json:"consented_by"`
	ConsentedAt       *float64 `json:"consented_at"`
	Enabled           int      `json:"enabled"`
	ProjectIDsJSON    string   `json:"-"`
	SecretsJSON       string   `json:"-"`
	InstalledAt       float64  `json:"installed_at"`
	UpdatedAt         float64  `json:"updated_at"`
}

const pluginCols = `id, source_kind, source, ref, index_name, subdir, commit_sha, tree_sha, content_hash,
	format, consented_hash, consented_caps_json, consented_by, consented_at, enabled,
	project_ids_json, secrets_json, installed_at, updated_at`

func scanPlugin(sc interface{ Scan(...any) error }) (*Plugin, error) {
	var p Plugin
	err := sc.Scan(&p.ID, &p.SourceKind, &p.Source, &p.Ref, &p.IndexName, &p.Subdir, &p.CommitSHA, &p.TreeSHA,
		&p.ContentHash, &p.Format, &p.ConsentedHash, &p.ConsentedCapsJSON, &p.ConsentedBy, &p.ConsentedAt,
		&p.Enabled, &p.ProjectIDsJSON, &p.SecretsJSON, &p.InstalledAt, &p.UpdatedAt)
	return &p, err
}

// Plugins lists every plugin row.
func (db *DB) Plugins() ([]*Plugin, error) {
	rows, err := db.Query(`SELECT ` + pluginCols + ` FROM plugins ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Plugin{}
	for rows.Next() {
		p, err := scanPlugin(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// Plugin returns one row, or ErrNotFound.
func (db *DB) Plugin(id string) (*Plugin, error) {
	p, err := scanPlugin(db.QueryRow(`SELECT `+pluginCols+` FROM plugins WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return p, err
}

// SavePlugin inserts or replaces a row whole.
func (db *DB) SavePlugin(p *Plugin) error {
	now := Now()
	if p.InstalledAt == 0 {
		p.InstalledAt = now
	}
	p.UpdatedAt = now
	_, err := db.Exec(`INSERT INTO plugins(`+pluginCols+`) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET source_kind=excluded.source_kind, source=excluded.source, ref=excluded.ref,
		index_name=excluded.index_name, subdir=excluded.subdir, commit_sha=excluded.commit_sha, tree_sha=excluded.tree_sha,
		content_hash=excluded.content_hash, format=excluded.format, consented_hash=excluded.consented_hash,
		consented_caps_json=excluded.consented_caps_json, consented_by=excluded.consented_by,
		consented_at=excluded.consented_at, enabled=excluded.enabled, project_ids_json=excluded.project_ids_json,
		secrets_json=excluded.secrets_json, updated_at=excluded.updated_at`,
		p.ID, p.SourceKind, p.Source, p.Ref, p.IndexName, p.Subdir, p.CommitSHA, p.TreeSHA, p.ContentHash,
		nz(p.Format, "lectern"), p.ConsentedHash, nz(p.ConsentedCapsJSON, "[]"), p.ConsentedBy, p.ConsentedAt,
		p.Enabled, nz(p.ProjectIDsJSON, "[]"), nz(p.SecretsJSON, "{}"), p.InstalledAt, p.UpdatedAt)
	return err
}

// DeletePlugin removes a row, its consent and its hook history.
func (db *DB) DeletePlugin(id string) error {
	if _, err := db.Exec(`DELETE FROM plugin_hook_runs WHERE plugin_id=?`, id); err != nil {
		return err
	}
	_, err := db.Exec(`DELETE FROM plugins WHERE id=?`, id)
	return err
}

// PluginSource is a marketplace: a git repository with an index file.
type PluginSource struct {
	Name    string  `json:"name"`
	URL     string  `json:"url"`
	Ref     string  `json:"ref"`
	AddedAt float64 `json:"added_at"`
}

func (db *DB) PluginSources() ([]*PluginSource, error) {
	rows, err := db.Query(`SELECT name, url, ref, added_at FROM plugin_sources ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*PluginSource{}
	for rows.Next() {
		var s PluginSource
		if err := rows.Scan(&s.Name, &s.URL, &s.Ref, &s.AddedAt); err != nil {
			return nil, err
		}
		out = append(out, &s)
	}
	return out, rows.Err()
}

func (db *DB) SavePluginSource(s *PluginSource) error {
	if s.AddedAt == 0 {
		s.AddedAt = Now()
	}
	_, err := db.Exec(`INSERT INTO plugin_sources(name,url,ref,added_at) VALUES(?,?,?,?)
		ON CONFLICT(name) DO UPDATE SET url=excluded.url, ref=excluded.ref`, s.Name, s.URL, s.Ref, s.AddedAt)
	return err
}

func (db *DB) DeletePluginSource(name string) error {
	res, err := db.Exec(`DELETE FROM plugin_sources WHERE name=?`, name)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// PluginHookRun is one execution of a plugin hook.
type PluginHookRun struct {
	ID         int64   `json:"id"`
	PluginID   string  `json:"plugin_id"`
	Event      string  `json:"event"`
	Run        string  `json:"run"`
	ProjectID  *int64  `json:"project_id"`
	StartedAt  float64 `json:"started_at"`
	DurationMS int64   `json:"duration_ms"`
	OK         bool    `json:"ok"`
	Message    string  `json:"message"`
	Error      string  `json:"error"`
}

func (db *DB) InsertPluginHookRun(r *PluginHookRun) error {
	res, err := db.Exec(`INSERT INTO plugin_hook_runs(plugin_id,event,run,project_id,started_at,duration_ms,ok,message,error)
		VALUES(?,?,?,?,?,?,?,?,?)`, r.PluginID, r.Event, r.Run, r.ProjectID, r.StartedAt, r.DurationMS, r.OK, r.Message, r.Error)
	if err != nil {
		return err
	}
	r.ID, _ = res.LastInsertId()
	// Keep the most recent 200 per plugin: it is a log to look at, not an archive.
	_, _ = db.Exec(`DELETE FROM plugin_hook_runs WHERE plugin_id=? AND id NOT IN
		(SELECT id FROM plugin_hook_runs WHERE plugin_id=? ORDER BY id DESC LIMIT 200)`, r.PluginID, r.PluginID)
	return nil
}

func (db *DB) PluginHookRuns(pluginID string, limit int) ([]*PluginHookRun, error) {
	rows, err := db.Query(`SELECT id,plugin_id,event,run,project_id,started_at,duration_ms,ok,message,error
		FROM plugin_hook_runs WHERE plugin_id=? ORDER BY id DESC LIMIT ?`, pluginID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*PluginHookRun{}
	for rows.Next() {
		var r PluginHookRun
		if err := rows.Scan(&r.ID, &r.PluginID, &r.Event, &r.Run, &r.ProjectID, &r.StartedAt, &r.DurationMS, &r.OK, &r.Message, &r.Error); err != nil {
			return nil, err
		}
		out = append(out, &r)
	}
	return out, rows.Err()
}
