package helpers

// SQLite as Python's sqlite3 module used it: one connection, explicit
// transactions, a busy timeout, and error text that is sqlite3_errmsg.
// modernc.org/sqlite is the pure-Go SQLite the server already links, so this
// adds nothing to the binary and cross-compiles everywhere.

import (
	"context"
	"database/sql"
	"regexp"
	"strconv"

	_ "modernc.org/sqlite"
)

type sqliteDB struct {
	db   *sql.DB
	conn *sql.Conn
}

// openSQLite opens dsn on a single connection with sqlite3.connect's
// timeout.
func openSQLite(dsn string, timeoutMs int) (*sqliteDB, error) {
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, sqliteError(err)
	}
	db.SetMaxOpenConns(1)
	conn, err := db.Conn(context.Background())
	if err != nil {
		db.Close()
		return nil, sqliteError(err)
	}
	s := &sqliteDB{db, conn}
	if _, err := conn.ExecContext(context.Background(), "PRAGMA busy_timeout="+strconv.Itoa(timeoutMs)); err != nil {
		s.Close()
		return nil, sqliteError(err)
	}
	return s, nil
}

func (s *sqliteDB) Close() {
	s.conn.Close()
	s.db.Close()
}

func (s *sqliteDB) exec(query string, args ...any) (sql.Result, error) {
	r, err := s.conn.ExecContext(context.Background(), query, args...)
	return r, sqliteError(err)
}

func (s *sqliteDB) query(query string, args ...any) (*sql.Rows, error) {
	r, err := s.conn.QueryContext(context.Background(), query, args...)
	return r, sqliteError(err)
}

// queryRow returns the first row as column name -> value, or nil.
func (s *sqliteDB) queryRow(query string, args ...any) (map[string]any, error) {
	rows, err := s.queryAll(query, 1, args...)
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	return rows[0], nil
}

// queryAll returns up to limit rows (limit < 0: all) as maps.
func (s *sqliteDB) queryAll(query string, limit int, args ...any) ([]map[string]any, error) {
	rows, err := s.query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	names, err := rows.Columns()
	if err != nil {
		return nil, sqliteError(err)
	}
	var out []map[string]any
	for (limit < 0 || len(out) < limit) && rows.Next() {
		vals := make([]any, len(names))
		ptrs := make([]any, len(names))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, sqliteError(err)
		}
		row := map[string]any{}
		for i, n := range names {
			row[n] = vals[i]
		}
		out = append(out, row)
	}
	return out, sqliteError(rows.Err())
}

var sqliteSuffix = regexp.MustCompile(` \(\d+\)( \(SQLITE_BUSY\))?$`)

// sqliteError turns a driver error into sqlite3.Error's text: modernc
// renders "errstr: errmsg (code)" where Python shows only errmsg.
func sqliteError(err error) error {
	if err == nil {
		return nil
	}
	if _, ok := err.(*pyErr); ok {
		return err
	}
	msg := sqliteSuffix.ReplaceAllString(err.Error(), "")
	if i := indexColonSpace(msg); i >= 0 {
		msg = msg[i+2:]
	}
	return pyError("sqlite3.Error", msg)
}

func indexColonSpace(s string) int {
	for i := 0; i+1 < len(s); i++ {
		if s[i] == ':' && s[i+1] == ' ' {
			return i
		}
	}
	return -1
}

func asInt64(v any) int64 {
	switch x := v.(type) {
	case int64:
		return x
	case float64:
		return int64(x)
	}
	return 0
}

func asFloat(v any) float64 {
	switch x := v.(type) {
	case int64:
		return float64(x)
	case float64:
		return x
	}
	return 0
}

func asString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case []byte:
		return string(x)
	}
	return ""
}
