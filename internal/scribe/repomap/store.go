package repomap

import (
	"database/sql"
	"encoding/json"
	"fmt"

	_ "modernc.org/sqlite"
)

// schemaVersion invalidates the cache when the extraction format changes.
const schemaVersion = "repomap-2"

type cacheRow struct {
	mtime, size int64
	hash        string
	data        []byte
}

type upsert struct {
	rel string
	row cacheRow
}

func openStore(dsn string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	stmts := []string{
		`PRAGMA journal_mode=WAL`,
		`PRAGMA synchronous=NORMAL`,
		`CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT NOT NULL)`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			db.Close()
			return nil, fmt.Errorf("repomap cache: %w", err)
		}
	}
	var ver string
	_ = db.QueryRow(`SELECT value FROM meta WHERE key='schema'`).Scan(&ver)
	if ver != schemaVersion {
		if _, err := db.Exec(`DROP TABLE IF EXISTS files`); err != nil {
			db.Close()
			return nil, err
		}
		if _, err := db.Exec(`INSERT OR REPLACE INTO meta(key,value) VALUES('schema',?)`, schemaVersion); err != nil {
			db.Close()
			return nil, err
		}
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS files (path TEXT PRIMARY KEY, mtime INTEGER NOT NULL, size INTEGER NOT NULL, hash TEXT NOT NULL, data BLOB NOT NULL)`); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func loadRows(db *sql.DB) (map[string]cacheRow, error) {
	rows, err := db.Query(`SELECT path, mtime, size, hash, data FROM files`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]cacheRow{}
	for rows.Next() {
		var p string
		var r cacheRow
		if err := rows.Scan(&p, &r.mtime, &r.size, &r.hash, &r.data); err != nil {
			return nil, err
		}
		out[p] = r
	}
	return out, rows.Err()
}

func writeRows(db *sql.DB, ups []upsert, dels []string) error {
	if len(ups) == 0 && len(dels) == 0 {
		return nil
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	ins, err := tx.Prepare(`INSERT OR REPLACE INTO files(path,mtime,size,hash,data) VALUES(?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer ins.Close()
	for _, u := range ups {
		if _, err := ins.Exec(u.rel, u.row.mtime, u.row.size, u.row.hash, u.row.data); err != nil {
			return err
		}
	}
	del, err := tx.Prepare(`DELETE FROM files WHERE path=?`)
	if err != nil {
		return err
	}
	defer del.Close()
	for _, p := range dels {
		if _, err := del.Exec(p); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func encodeInfo(fi *fileInfo) ([]byte, error) { return json.Marshal(fi) }

func decodeInfo(b []byte) (*fileInfo, error) {
	var fi fileInfo
	if err := json.Unmarshal(b, &fi); err != nil {
		return nil, err
	}
	return &fi, nil
}
