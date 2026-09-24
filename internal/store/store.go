// Package store is lectern's SQLite persistence layer: schema, typed row
// accessors, and the small JSON helpers the rest of the service shares.
package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// ErrNotFound is returned by the One* accessors when a row does not exist. It is
// deliberately distinct from sql.ErrNoRows so callers never have to import
// database/sql just to branch on "missing".
var ErrNotFound = errors.New("not found")

// DB wraps the connection pool.
type DB struct{ *sql.DB }

// Open creates (or migrates) the database at path.
//
// The pragmas go through modernc's `_pragma=` DSN parameter: the cgo driver's
// `_journal_mode=`/`_busy_timeout=` spelling is silently IGNORED here, which
// leaves the database in rollback-journal mode with no busy timeout — writes
// then fail under any concurrency instead of waiting.
func Open(path string) (*DB, error) {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}
	// The database includes explicitly configured credentials. Restrict it before
	// SQLite opens it, so newly created journal files inherit private permissions.
	if path != ":memory:" {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
		if err != nil {
			return nil, err
		}
		err = f.Chmod(0o600)
		f.Close()
		if err != nil {
			return nil, err
		}
		for _, suffix := range []string{"-wal", "-shm", "-journal"} {
			if err := os.Chmod(path+suffix, 0o600); err != nil && !os.IsNotExist(err) {
				return nil, err
			}
		}
	}

	dsn := path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(10000)" +
		"&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(1)"
	sqldb, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// One writer, one reader queue. The scheduler, the HTTP handlers and the hook
	// endpoints all write; serialising here is cheaper than debugging SQLITE_BUSY
	// at a scale where every query is sub-millisecond.
	sqldb.SetMaxOpenConns(1)
	db := &DB{sqldb}
	if _, err := db.Exec(Schema); err != nil {
		return nil, fmt.Errorf("schema: %w", err)
	}
	for _, m := range migrations {
		if _, err := db.Exec(m); err != nil && !strings.Contains(err.Error(), "duplicate column") {
			return nil, fmt.Errorf("migration %q: %w", m, err)
		}
	}
	// Not an ADD COLUMN, so it cannot live in the plain-string list above —
	// see migrate_approvals.go for why.
	if err := migrateApprovalsSessionColumn(db.DB); err != nil {
		return nil, fmt.Errorf("migrate approvals: %w", err)
	}
	return db, nil
}

// Now is the float epoch seconds every timestamp column stores.
func Now() float64 { return float64(time.Now().UnixNano()) / 1e9 }

// J marshals to a compact JSON string for a *_json column.
func J(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// UnjObj parses a *_json column into an object, tolerating empty and malformed
// values — a bad blob must degrade a card, never take down the board.
//
// It never returns nil, including for the literal "null", which unmarshals into
// a nil map without erroring. Callers write into the result (the scheduler adds
// an "error" key when finalising, and a "review" key when a reviewer gate
// lands), and writing to a nil map panics — inside the scheduler tick, which
// would stall every other running attempt.
func UnjObj(s string) map[string]any {
	out := map[string]any{}
	if s == "" {
		return out
	}
	if err := json.Unmarshal([]byte(s), &out); err != nil || out == nil {
		return map[string]any{}
	}
	return out
}

// UnjList parses a *_json column into a list. diff_stat_json defaults to '{}'
// before a diff is captured, which parses as an object — returning an empty list
// there is what keeps a running card renderable.
func UnjList(s string) []any {
	var out []any
	if s == "" {
		return []any{}
	}
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return []any{}
	}
	return out
}

// UnjStrings parses a *_json column into a string list (context paths, labels).
func UnjStrings(s string) []string {
	var out []string
	if s == "" {
		return nil
	}
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil
	}
	return out
}
