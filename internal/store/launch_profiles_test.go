package store

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
)

func TestLaunchProfilesMigratePersistAndDeleteIndependently(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	old, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.Exec(earlySchema + `INSERT INTO settings VALUES('agents','retained');`); err != nil {
		t.Fatal(err)
	}
	old.Close()
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	p, err := db.SaveLaunchProfile(&LaunchProfile{Name: "Work", Agent: "codex", EnvJSON: `{"CODEX_HOME":"/private/home"}`})
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	got, err := db.LaunchProfile(p.ID)
	if err != nil || got.EnvJSON != p.EnvJSON || db.Setting("agents") != "retained" {
		t.Fatal("migration or reopen lost configuration")
	}
	p.Name = "Personal"
	if _, err := db.SaveLaunchProfile(p); err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteLaunchProfile(p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SaveLaunchProfile(p); !errors.Is(err, ErrNotFound) {
		t.Fatal("updating a deleted profile recreated it", err)
	}
	if _, err := db.LaunchProfile(p.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	replacement, err := db.SaveLaunchProfile(&LaunchProfile{Name: "Replacement", Agent: "codex", EnvJSON: `{}`})
	if err != nil || replacement.ID == p.ID {
		t.Fatal("deleted profile ID was reused; a stale selection could launch different settings", err)
	}
	if db.Setting("agents") != "retained" {
		t.Fatal("profile deletion removed other settings")
	}
}

// legacyLaunchProfilesSchema is the launch_profiles shape before descriptions
// and launch briefings existed. Reproduced here so the migration test states
// exactly what an older database is missing.
const legacyLaunchProfilesSchema = `
CREATE TABLE launch_profiles(
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL COLLATE NOCASE UNIQUE,
  agent TEXT NOT NULL,
  command TEXT NOT NULL DEFAULT '',
  model TEXT NOT NULL DEFAULT '',
  env_json TEXT NOT NULL DEFAULT '{}'
);
INSERT INTO launch_profiles(name,agent,command,model,env_json) VALUES('Legacy','codex','legacy-codex','','{"KEEP":"1"}');
`

func TestLaunchProfileBriefingColumnsMigrateAndPersist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy-profiles.db")
	old, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.Exec(legacyLaunchProfilesSchema); err != nil {
		t.Fatal(err)
	}
	old.Close()
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := db.LaunchProfiles()
	if err != nil || len(legacy) != 1 {
		t.Fatalf("legacy rows lost: %v %v", legacy, err)
	}
	if legacy[0].Description != "" || legacy[0].Instructions != "" || legacy[0].EnvJSON != `{"KEEP":"1"}` {
		t.Fatalf("migrated profile has wrong defaults: %+v", legacy[0])
	}
	legacy[0].Description = "A focused builder"
	legacy[0].Instructions = "First line.\nSecond line."
	if _, err := db.SaveLaunchProfile(legacy[0]); err != nil {
		t.Fatal(err)
	}
	db.Close()
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	got, err := db.LaunchProfile(legacy[0].ID)
	if err != nil || got.Description != "A focused builder" || got.Instructions != "First line.\nSecond line." {
		t.Fatalf("briefing fields did not survive reopen: %+v %v", got, err)
	}
}
