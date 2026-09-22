package store

import "database/sql"

// ProjectSkill is a desired project attachment. The source is deliberately
// target-local: it is never copied into the control plane or another user's
// home directory.
type ProjectSkill struct {
	ID            int64   `json:"id"`
	ProjectID     int64   `json:"project_id"`
	TargetID      int64   `json:"target_id"`
	Agent         string  `json:"agent"`
	SkillID       string  `json:"skill_id"`
	SourceID      string  `json:"source_id"`
	SourcePath    string  `json:"source_path"`
	EntryName     string  `json:"entry_name"`
	TargetRel     string  `json:"target_rel"`
	SourceDigest  string  `json:"source_digest"`
	ExcludeMarker string  `json:"exclude_marker"`
	CreatedAt     float64 `json:"created_at"`
}

type SkillMaterialization struct {
	ID           int64  `json:"id"`
	AttachmentID int64  `json:"attachment_id"`
	TargetID     int64  `json:"target_id"`
	WorktreePath string `json:"worktree_path"`
	TargetPath   string `json:"target_path"`
	SourcePath   string `json:"source_path"`
	TargetRel    string `json:"target_rel"`
	// State records the filesystem fact separately from desired attachment
	// intent. pending means the link has not been proven; owned means Lectern
	// created it; preexisting means the destination was native and must never
	// be removed during detach.
	State     string  `json:"state"`
	CreatedAt float64 `json:"created_at"`
}

const projectSkillCols = `id, project_id, target_id, agent, skill_id, source_id,
 source_path, entry_name, target_rel, source_digest, exclude_marker, created_at`

func scanProjectSkill(s interface{ Scan(...any) error }) (*ProjectSkill, error) {
	var x ProjectSkill
	err := s.Scan(&x.ID, &x.ProjectID, &x.TargetID, &x.Agent, &x.SkillID,
		&x.SourceID, &x.SourcePath, &x.EntryName, &x.TargetRel, &x.SourceDigest,
		&x.ExcludeMarker, &x.CreatedAt)
	return &x, err
}

func (db *DB) ProjectSkills(projectID int64, agent string) ([]*ProjectSkill, error) {
	q := `SELECT ` + projectSkillCols + ` FROM project_skills WHERE project_id=?`
	args := []any{projectID}
	if agent != "" {
		q += ` AND agent=?`
		args = append(args, agent)
	}
	q += ` ORDER BY agent, skill_id`
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*ProjectSkill{}
	for rows.Next() {
		x, err := scanProjectSkill(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

func (db *DB) ProjectSkill(id int64) (*ProjectSkill, error) {
	x, err := scanProjectSkill(db.QueryRow(`SELECT `+projectSkillCols+` FROM project_skills WHERE id=?`, id))
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	return x, err
}

func (db *DB) InsertProjectSkill(x *ProjectSkill) (*ProjectSkill, error) {
	r, err := db.Exec(`INSERT INTO project_skills(project_id,target_id,agent,skill_id,source_id,source_path,entry_name,target_rel,source_digest,exclude_marker,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
		x.ProjectID, x.TargetID, x.Agent, x.SkillID, x.SourceID, x.SourcePath, x.EntryName, x.TargetRel, x.SourceDigest, x.ExcludeMarker, Now())
	if err != nil {
		return nil, err
	}
	id, _ := r.LastInsertId()
	return db.ProjectSkill(id)
}

func (db *DB) DeleteProjectSkill(id int64) error {
	_, err := db.Exec(`DELETE FROM project_skills WHERE id=?`, id)
	return err
}

const materializationCols = `id, attachment_id, target_id, worktree_path, target_path, source_path, target_rel, state, created_at`

func scanMaterialization(s interface{ Scan(...any) error }) (*SkillMaterialization, error) {
	var x SkillMaterialization
	err := s.Scan(&x.ID, &x.AttachmentID, &x.TargetID, &x.WorktreePath, &x.TargetPath, &x.SourcePath, &x.TargetRel, &x.State, &x.CreatedAt)
	if x.State == "" {
		x.State = "pending"
	}
	return &x, err
}
func (db *DB) Materializations(attachmentID int64) ([]*SkillMaterialization, error) {
	rows, err := db.Query(`SELECT `+materializationCols+` FROM skill_materializations WHERE attachment_id=?`, attachmentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*SkillMaterialization{}
	for rows.Next() {
		x, e := scanMaterialization(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, x)
	}
	return out, rows.Err()
}
func (db *DB) MaterializationsAt(targetID int64, path string) ([]*SkillMaterialization, error) {
	rows, err := db.Query(`SELECT `+materializationCols+` FROM skill_materializations WHERE target_id=? AND worktree_path=?`, targetID, path)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*SkillMaterialization{}
	for rows.Next() {
		x, e := scanMaterialization(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, x)
	}
	return out, rows.Err()
}
func (db *DB) UpsertMaterialization(x *SkillMaterialization) error {
	state := x.State
	if state == "" {
		state = "pending"
	}
	_, err := db.Exec(`INSERT INTO skill_materializations(attachment_id,target_id,worktree_path,target_path,source_path,target_rel,state,created_at) VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(attachment_id,worktree_path) DO UPDATE SET target_id=excluded.target_id,target_path=excluded.target_path,source_path=excluded.source_path,target_rel=excluded.target_rel,state=excluded.state`, x.AttachmentID, x.TargetID, x.WorktreePath, x.TargetPath, x.SourcePath, x.TargetRel, state, Now())
	return err
}
func (db *DB) DeleteMaterialization(id int64) error {
	_, err := db.Exec(`DELETE FROM skill_materializations WHERE id=?`, id)
	return err
}
