package store

import (
	"database/sql"
	"errors"
)

// LaunchProfile holds explicit overrides for future interactive sessions.
// Environment values are configuration data and must not enter session events.
// Description and Instructions are the human-facing parts: Instructions is a
// launch briefing typed to the agent when a session starts, and never a
// configuration or privilege change.
type LaunchProfile struct {
	ID           int64  `json:"id"`
	Name         string `json:"name"`
	Agent        string `json:"agent"`
	Command      string `json:"command"`
	Model        string `json:"model"`
	EnvJSON      string `json:"env_json"`
	Description  string `json:"description"`
	Instructions string `json:"instructions"`
}

const launchProfileCols = `id,name,agent,command,model,env_json,description,instructions`

func scanLaunchProfile(row interface{ Scan(...any) error }) (*LaunchProfile, error) {
	p := &LaunchProfile{}
	err := row.Scan(&p.ID, &p.Name, &p.Agent, &p.Command, &p.Model, &p.EnvJSON, &p.Description, &p.Instructions)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return p, err
}

func (db *DB) LaunchProfile(id int64) (*LaunchProfile, error) {
	return scanLaunchProfile(db.QueryRow(`SELECT `+launchProfileCols+` FROM launch_profiles WHERE id=?`, id))
}

func (db *DB) LaunchProfiles() ([]*LaunchProfile, error) {
	rows, err := db.Query(`SELECT ` + launchProfileCols + ` FROM launch_profiles ORDER BY name,id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*LaunchProfile{}
	for rows.Next() {
		p, err := scanLaunchProfile(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (db *DB) SaveLaunchProfile(p *LaunchProfile) (*LaunchProfile, error) {
	if p.ID == 0 {
		result, err := db.Exec(`INSERT INTO launch_profiles(name,agent,command,model,env_json,description,instructions) VALUES(?,?,?,?,?,?,?)`, p.Name, p.Agent, p.Command, p.Model, p.EnvJSON, p.Description, p.Instructions)
		if err != nil {
			return nil, err
		}
		id, err := result.LastInsertId()
		if err != nil {
			return nil, err
		}
		return db.LaunchProfile(id)
	}
	result, err := db.Exec(`UPDATE launch_profiles SET name=?,agent=?,command=?,model=?,env_json=?,description=?,instructions=? WHERE id=?`, p.Name, p.Agent, p.Command, p.Model, p.EnvJSON, p.Description, p.Instructions, p.ID)
	if err != nil {
		return nil, err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return nil, ErrNotFound
	}
	return db.LaunchProfile(p.ID)
}

func (db *DB) DeleteLaunchProfile(id int64) error {
	result, err := db.Exec(`DELETE FROM launch_profiles WHERE id=?`, id)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
