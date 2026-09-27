package store

import (
	"database/sql"
	"errors"
)

// TrackerConnection is how a project reaches an external tracker for the
// Tasks hub (docs/trackers.md): a Linear workspace or a Jira site with its
// credentials, or an override of the GitHub/GitLab repository Lectern
// otherwise detects from the clone's origin remote.
type TrackerConnection struct {
	ID        int64  `json:"id"`
	ProjectID int64  `json:"project_id"`
	Kind      string `json:"kind"` // github | gitlab | linear | jira
	Name      string `json:"name"`
	// ConfigJSON is the non-secret settings; the API returns it parsed.
	ConfigJSON string `json:"-"`
	// SecretsJSON holds the API key/token. It is never serialized: the API
	// reports only which fields are set, exactly like trigger sources.
	SecretsJSON string  `json:"-"`
	CreatedAt   float64 `json:"created_at"`
	UpdatedAt   float64 `json:"updated_at"`
}

const trackerConnCols = `id, project_id, kind, name, config_json, secrets_json, created_at, updated_at`

func scanTrackerConn(sc interface{ Scan(...any) error }) (*TrackerConnection, error) {
	var c TrackerConnection
	err := sc.Scan(&c.ID, &c.ProjectID, &c.Kind, &c.Name, &c.ConfigJSON, &c.SecretsJSON, &c.CreatedAt, &c.UpdatedAt)
	return &c, err
}

// TrackerConnections lists a project's connections, oldest first.
func (db *DB) TrackerConnections(projectID int64) ([]*TrackerConnection, error) {
	rows, err := db.Query(`SELECT `+trackerConnCols+` FROM tracker_connections WHERE project_id=? ORDER BY id`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*TrackerConnection{}
	for rows.Next() {
		c, err := scanTrackerConn(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// TrackerConnection reads one connection.
func (db *DB) TrackerConnection(id int64) (*TrackerConnection, error) {
	c, err := scanTrackerConn(db.QueryRow(`SELECT `+trackerConnCols+` FROM tracker_connections WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return c, err
}

// InsertTrackerConnection saves a new connection.
func (db *DB) InsertTrackerConnection(c *TrackerConnection) (*TrackerConnection, error) {
	now := Now()
	res, err := db.Exec(`INSERT INTO tracker_connections(project_id, kind, name, config_json, secrets_json, created_at, updated_at)
		VALUES(?,?,?,?,?,?,?)`, c.ProjectID, c.Kind, c.Name, nz(c.ConfigJSON, "{}"), nz(c.SecretsJSON, "{}"), now, now)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return db.TrackerConnection(id)
}

// DeleteTrackerConnection removes one connection.
func (db *DB) DeleteTrackerConnection(id int64) error {
	_, err := db.Exec(`DELETE FROM tracker_connections WHERE id=?`, id)
	return err
}
