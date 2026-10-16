// Package memory is Scribe's durable memory: short records the agent saves
// so later sessions can recall them. Records live in two SQLite databases:
// a project store at <root>/.scribe/memory.db and a global store at
// config.Home()/memory.db. Full-text search uses FTS5 (unicode61); recall
// blends BM25, file overlap, a hashed bag-of-words embedding and a usage
// decay (see recall.go).
package memory

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	_ "modernc.org/sqlite"
)

// Categories a record may carry.
const (
	CatDecision   = "decision"
	CatConvention = "convention"
	CatBug        = "bug"
	CatPreference = "preference"
	CatFact       = "fact"
	CatTodo       = "todo"
)

// Scopes a DB can hold.
const (
	ScopeProject = "project"
	ScopeGlobal  = "global"
)

// Categories lists every valid category.
var Categories = []string{CatDecision, CatConvention, CatBug, CatPreference, CatFact, CatTodo}

// ErrNotFound is returned when no record matches an id.
var ErrNotFound = errors.New("memory not found")

// Record is one memory.
type Record struct {
	ID       string    `json:"id"`
	Scope    string    `json:"scope"`
	Category string    `json:"category"`
	Title    string    `json:"title"`
	Body     string    `json:"body"`
	Tags     []string  `json:"tags,omitempty"`
	Files    []string  `json:"files,omitempty"`
	Created  time.Time `json:"created"`
	Updated  time.Time `json:"updated"`
	Uses     int       `json:"uses"`
	LastUsed time.Time `json:"last_used"`
	Pinned   bool      `json:"pinned"`
}

// Scored is a recalled record with its relevance score and the signals
// that produced it ("text", "file", "similar", "pinned").
type Scored struct {
	Record
	Score float64  `json:"score"`
	Why   []string `json:"why,omitempty"`
}

// DB is one memory database.
type DB struct {
	db    *sql.DB
	scope string
	path  string
	now   func() time.Time
}

const schema = `
CREATE TABLE IF NOT EXISTS memories (
	id         TEXT PRIMARY KEY,
	category   TEXT NOT NULL,
	title      TEXT NOT NULL,
	body       TEXT NOT NULL,
	norm_title TEXT NOT NULL,
	tags       TEXT NOT NULL DEFAULT '[]',
	created    TEXT NOT NULL,
	updated    TEXT NOT NULL,
	uses       INTEGER NOT NULL DEFAULT 0,
	last_used  TEXT NOT NULL,
	pinned     INTEGER NOT NULL DEFAULT 0,
	embedding  BLOB
);
CREATE INDEX IF NOT EXISTS memories_pinned ON memories(pinned);
CREATE TABLE IF NOT EXISTS memory_files (
	memory_id TEXT NOT NULL,
	path      TEXT NOT NULL,
	PRIMARY KEY (memory_id, path)
);
CREATE INDEX IF NOT EXISTS memory_files_path ON memory_files(path);
CREATE VIRTUAL TABLE IF NOT EXISTS memories_fts USING fts5(
	id UNINDEXED, title, body, tags,
	tokenize = 'unicode61 remove_diacritics 2'
);
`

// OpenDB opens (creating if needed) the database file at path for scope.
func OpenDB(path, scope string) (*DB, error) {
	if scope != ScopeProject && scope != ScopeGlobal {
		return nil, fmt.Errorf("memory: unknown scope %q", scope)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	dsn := "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)"
	sdb, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// One connection per DB: writes serialise in-process and busy_timeout
	// handles sibling processes.
	sdb.SetMaxOpenConns(1)
	d := &DB{db: sdb, scope: scope, path: path, now: time.Now}
	if err := d.init(); err != nil {
		sdb.Close()
		return nil, fmt.Errorf("memory: open %s: %w", path, err)
	}
	restrictFiles(path)
	return d, nil
}

func (d *DB) init() error {
	if _, err := d.db.Exec("SELECT 1"); err != nil {
		return err
	}
	for _, stmt := range splitStatements(schema) {
		if _, err := d.db.Exec(stmt); err != nil {
			return err
		}
	}
	return nil
}

// restrictFiles applies 0600 to the database and its WAL side files.
func restrictFiles(path string) {
	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		_ = os.Chmod(p, 0o600)
	}
}

// splitStatements splits the schema on ';' at end of statement. The schema
// has no semicolons inside string literals, so this is exact for it.
func splitStatements(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ";") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// Close closes the database.
func (d *DB) Close() error { return d.db.Close() }

// Path is the database file.
func (d *DB) Path() string { return d.path }

// Scope is "project" or "global".
func (d *DB) Scope() string { return d.scope }

type execer interface {
	Exec(query string, args ...any) (sql.Result, error)
	Query(query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
}

const cols = "id, category, title, body, tags, created, updated, uses, last_used, pinned"

type scanner interface {
	Scan(dest ...any) error
}

func scanRecord(sc scanner) (Record, error) {
	var r Record
	var tags, created, updated, lastUsed string
	var pinned int
	if err := sc.Scan(&r.ID, &r.Category, &r.Title, &r.Body, &tags, &created, &updated, &r.Uses, &lastUsed, &pinned); err != nil {
		return Record{}, err
	}
	_ = json.Unmarshal([]byte(tags), &r.Tags)
	r.Created = parseTime(created)
	r.Updated = parseTime(updated)
	r.LastUsed = parseTime(lastUsed)
	r.Pinned = pinned != 0
	return r, nil
}

func parseTime(s string) time.Time {
	t, _ := time.Parse(time.RFC3339Nano, s)
	return t
}

func loadFiles(q execer, id string) ([]string, error) {
	rows, err := q.Query("SELECT path FROM memory_files WHERE memory_id = ? ORDER BY path", id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// loadOne reads a full record by exact id.
func loadOne(q execer, id string) (Record, error) {
	row := q.QueryRow("SELECT "+cols+" FROM memories WHERE id = ?", id)
	r, err := scanRecord(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Record{}, ErrNotFound
	}
	if err != nil {
		return Record{}, err
	}
	r.Files, err = loadFiles(q, id)
	return r, err
}

// resolveID maps an exact id or a unique id prefix to the full id.
func resolveID(q execer, idOrPrefix string) (string, error) {
	idOrPrefix = strings.TrimSpace(idOrPrefix)
	if idOrPrefix == "" {
		return "", ErrNotFound
	}
	var id string
	err := q.QueryRow("SELECT id FROM memories WHERE id = ?", idOrPrefix).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	rows, err := q.Query("SELECT id FROM memories WHERE substr(id, 1, ?) = ? LIMIT 3", len(idOrPrefix), idOrPrefix)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return "", err
		}
		ids = append(ids, s)
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	switch len(ids) {
	case 0:
		return "", ErrNotFound
	case 1:
		return ids[0], nil
	default:
		return "", fmt.Errorf("id prefix %q is ambiguous; use more characters", idOrPrefix)
	}
}

// writeRecord upserts the row, its file refs and its FTS row.
func writeRecord(q execer, r Record) error {
	tags, _ := json.Marshal(r.Tags)
	if r.Tags == nil {
		tags = []byte("[]")
	}
	emb := encodeVec(embed(embedText(r.Title, r.Body, r.Tags)))
	if _, err := q.Exec(`INSERT INTO memories
		(id, category, title, body, norm_title, tags, created, updated, uses, last_used, pinned, embedding)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
		category = excluded.category, title = excluded.title, body = excluded.body,
		norm_title = excluded.norm_title, tags = excluded.tags, updated = excluded.updated,
		uses = excluded.uses, last_used = excluded.last_used, pinned = excluded.pinned,
		embedding = excluded.embedding`,
		r.ID, r.Category, r.Title, r.Body, normTitle(r.Title), string(tags),
		fmtTime(r.Created), fmtTime(r.Updated), r.Uses, fmtTime(r.LastUsed), boolInt(r.Pinned), emb); err != nil {
		return err
	}
	if _, err := q.Exec("DELETE FROM memory_files WHERE memory_id = ?", r.ID); err != nil {
		return err
	}
	for _, f := range r.Files {
		if _, err := q.Exec("INSERT OR IGNORE INTO memory_files (memory_id, path) VALUES (?, ?)", r.ID, f); err != nil {
			return err
		}
	}
	if _, err := q.Exec("DELETE FROM memories_fts WHERE id = ?", r.ID); err != nil {
		return err
	}
	_, err := q.Exec("INSERT INTO memories_fts (id, title, body, tags) VALUES (?, ?, ?, ?)",
		r.ID, r.Title, r.Body, strings.Join(r.Tags, " "))
	return err
}

func removeRecord(q execer, id string) error {
	for _, stmt := range []string{
		"DELETE FROM memories_fts WHERE id = ?",
		"DELETE FROM memory_files WHERE memory_id = ?",
		"DELETE FROM memories WHERE id = ?",
	} {
		if _, err := q.Exec(stmt, id); err != nil {
			return err
		}
	}
	return nil
}

func fmtTime(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func validate(r *Record) error {
	r.Title = strings.TrimSpace(r.Title)
	r.Body = strings.TrimSpace(r.Body)
	if r.Title == "" {
		return errors.New("title is required")
	}
	if r.Body == "" {
		return errors.New("body is required")
	}
	if len(r.Title) > 200 {
		return errors.New("title is longer than 200 characters; shorten it and put detail in body")
	}
	if r.Category == "" {
		r.Category = CatFact
	}
	if !validCategory(r.Category) {
		return fmt.Errorf("unknown category %q; use one of %s", r.Category, strings.Join(Categories, ", "))
	}
	r.Tags = cleanTags(r.Tags)
	r.Files = normalizeFiles(r.Files)
	return nil
}

func validCategory(c string) bool {
	for _, k := range Categories {
		if k == c {
			return true
		}
	}
	return false
}

func cleanTags(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, t := range in {
		t = strings.ToLower(strings.TrimSpace(t))
		if t == "" || seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	return out
}

// normalizeFiles cleans paths to slash-separated relative form and drops
// duplicates.
func normalizeFiles(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, f := range in {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		f = filepath.ToSlash(filepath.Clean(f))
		if seen[f] {
			continue
		}
		seen[f] = true
		out = append(out, f)
	}
	return out
}

func union(a, b []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range append(append([]string(nil), a...), b...) {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// normTitle lowercases and collapses whitespace for title comparison.
func normTitle(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(s)), " ")
}

// Add stores r. If an existing record has the same normalized title or a
// body with token Jaccard similarity >= 0.8, that record is updated instead
// and deduped is true. Otherwise a new record is inserted.
func (d *DB) Add(r Record) (rec Record, deduped bool, err error) {
	explicitCategory := r.Category != ""
	if err := validate(&r); err != nil {
		return Record{}, false, err
	}
	now := d.now()
	tx, err := d.db.Begin()
	if err != nil {
		return Record{}, false, err
	}
	defer tx.Rollback()

	dupID, err := findDuplicate(tx, r.Title, r.Body)
	if err != nil {
		return Record{}, false, err
	}
	if dupID != "" {
		cur, err := loadOne(tx, dupID)
		if err != nil {
			return Record{}, false, err
		}
		cur.Title = r.Title
		cur.Body = r.Body
		if explicitCategory {
			cur.Category = r.Category
		}
		cur.Tags = union(cur.Tags, r.Tags)
		cur.Files = union(cur.Files, r.Files)
		cur.Pinned = cur.Pinned || r.Pinned
		cur.Updated = now
		cur.LastUsed = now
		if err := writeRecord(tx, cur); err != nil {
			return Record{}, false, err
		}
		if err := tx.Commit(); err != nil {
			return Record{}, false, err
		}
		cur.Scope = d.scope
		return cur, true, nil
	}

	r.ID = uuid.NewString()
	r.Scope = d.scope
	r.Created, r.Updated, r.LastUsed = now, now, now
	r.Uses = 0
	if err := writeRecord(tx, r); err != nil {
		return Record{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return Record{}, false, err
	}
	return r, false, nil
}

// findDuplicate returns the id of a record that is a near-duplicate of
// (title, body), or "".
func findDuplicate(q execer, title, body string) (string, error) {
	nt := normTitle(title)
	bt := tokenSet(body)
	rows, err := q.Query("SELECT id, norm_title, body FROM memories")
	if err != nil {
		return "", err
	}
	defer rows.Close()
	best, bestSim := "", 0.0
	for rows.Next() {
		var id, t, b string
		if err := rows.Scan(&id, &t, &b); err != nil {
			return "", err
		}
		if t == nt {
			return id, nil
		}
		if sim := jaccard(bt, tokenSet(b)); sim >= 0.8 && sim > bestSim {
			best, bestSim = id, sim
		}
	}
	return best, rows.Err()
}

func tokenSet(s string) map[string]bool {
	m := map[string]bool{}
	for _, w := range wordTokens(s) {
		m[w] = true
	}
	return m
}

func jaccard(a, b map[string]bool) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	inter := 0
	for k := range a {
		if b[k] {
			inter++
		}
	}
	union := len(a) + len(b) - inter
	return float64(inter) / float64(union)
}

// Update overwrites title, body, category, tags and files of the record
// with r.ID. Uses, pinned state and created time are kept.
func (d *DB) Update(r Record) error {
	if r.ID == "" {
		return errors.New("update needs an id")
	}
	if err := validate(&r); err != nil {
		return err
	}
	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	id, err := resolveID(tx, r.ID)
	if err != nil {
		return err
	}
	cur, err := loadOne(tx, id)
	if err != nil {
		return err
	}
	cur.Title, cur.Body, cur.Category = r.Title, r.Body, r.Category
	cur.Tags, cur.Files = r.Tags, r.Files
	cur.Updated = d.now()
	if err := writeRecord(tx, cur); err != nil {
		return err
	}
	return tx.Commit()
}

// Get returns the record with id or a unique id prefix.
func (d *DB) Get(idOrPrefix string) (Record, error) {
	id, err := resolveID(d.db, idOrPrefix)
	if err != nil {
		return Record{}, err
	}
	r, err := loadOne(d.db, id)
	if err != nil {
		return Record{}, err
	}
	r.Scope = d.scope
	return r, nil
}

// Delete removes the record with id or a unique id prefix.
func (d *DB) Delete(idOrPrefix string) error {
	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	id, err := resolveID(tx, idOrPrefix)
	if err != nil {
		return err
	}
	if err := removeRecord(tx, id); err != nil {
		return err
	}
	return tx.Commit()
}

// Pin sets or clears the pinned flag.
func (d *DB) Pin(idOrPrefix string, pinned bool) error {
	id, err := resolveID(d.db, idOrPrefix)
	if err != nil {
		return err
	}
	_, err = d.db.Exec("UPDATE memories SET pinned = ?, updated = ? WHERE id = ?", boolInt(pinned), fmtTime(d.now()), id)
	return err
}

// ListOptions filters List.
type ListOptions struct {
	Category   string
	PinnedOnly bool
	Limit      int // default 100
}

// List returns records, pinned first, then most recently used.
func (d *DB) List(o ListOptions) ([]Record, error) {
	if o.Limit <= 0 {
		o.Limit = 100
	}
	q := "SELECT " + cols + " FROM memories WHERE 1=1"
	var args []any
	if o.Category != "" {
		q += " AND category = ?"
		args = append(args, o.Category)
	}
	if o.PinnedOnly {
		q += " AND pinned = 1"
	}
	q += " ORDER BY pinned DESC, last_used DESC, id LIMIT ?"
	args = append(args, o.Limit)
	return d.queryRecords(q, args...)
}

func (d *DB) queryRecords(q string, args ...any) ([]Record, error) {
	rows, err := d.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	var recs []Record
	for rows.Next() {
		r, err := scanRecord(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		recs = append(recs, r)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	for i := range recs {
		recs[i].Scope = d.scope
		recs[i].Files, err = loadFiles(d.db, recs[i].ID)
		if err != nil {
			return nil, err
		}
	}
	return recs, nil
}

// Search runs an FTS5 full-text query over title, body and tags, best
// BM25 match first. Score is BM25 normalised to (0,1).
func (d *DB) Search(query string, limit int) ([]Scored, error) {
	if limit <= 0 {
		limit = 10
	}
	match := ftsQuery(query)
	if match == "" {
		return nil, nil
	}
	rows, err := d.db.Query(
		"SELECT id, bm25(memories_fts) FROM memories_fts WHERE memories_fts MATCH ? ORDER BY bm25(memories_fts) LIMIT ?",
		match, limit)
	if err != nil {
		return nil, err
	}
	type hit struct {
		id string
		x  float64
	}
	var hits []hit
	for rows.Next() {
		var h hit
		if err := rows.Scan(&h.id, &h.x); err != nil {
			rows.Close()
			return nil, err
		}
		hits = append(hits, hit{h.id, -h.x}) // bm25 is negative; lower is better
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var out []Scored
	for _, h := range hits {
		r, err := loadOne(d.db, h.id)
		if err != nil {
			return nil, err
		}
		r.Scope = d.scope
		out = append(out, Scored{Record: r, Score: bm25Norm(h.x), Why: []string{"text"}})
	}
	return out, nil
}

// Prune deletes unpinned records not used within maxAge that have fewer
// than minUses uses. It returns how many were deleted.
func (d *DB) Prune(maxAge time.Duration, minUses int) (int, error) {
	cutoff := fmtTime(d.now().Add(-maxAge))
	tx, err := d.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	rows, err := tx.Query("SELECT id FROM memories WHERE pinned = 0 AND last_used < ? AND uses < ?", cutoff, minUses)
	if err != nil {
		return 0, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	for _, id := range ids {
		if err := removeRecord(tx, id); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return len(ids), nil
}

// Export writes every record as an indented JSON array.
func (d *DB) Export(w io.Writer) error {
	recs, err := d.List(ListOptions{Limit: 1 << 30})
	if err != nil {
		return err
	}
	if recs == nil {
		recs = []Record{}
	}
	sort.SliceStable(recs, func(i, j int) bool { return recs[i].Created.Before(recs[j].Created) })
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(recs)
}

// Import reads a JSON array written by Export and adds each record through
// Add, so dedupe applies. Ids in the file are not reused. It returns how
// many records were new and how many were merged into existing ones.
func (d *DB) Import(r io.Reader) (added, merged int, err error) {
	var recs []Record
	if err := json.NewDecoder(r).Decode(&recs); err != nil {
		return 0, 0, fmt.Errorf("memory import: %w", err)
	}
	for _, rec := range recs {
		_, dup, err := d.Add(rec)
		if err != nil {
			return added, merged, fmt.Errorf("memory import %q: %w", rec.Title, err)
		}
		if dup {
			merged++
		} else {
			added++
		}
	}
	return added, merged, nil
}

// touch records that the given ids were surfaced to the model.
func (d *DB) touch(ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	now := fmtTime(d.now())
	for _, id := range ids {
		if _, err := d.db.Exec("UPDATE memories SET uses = uses + 1, last_used = ? WHERE id = ?", now, id); err != nil {
			return err
		}
	}
	return nil
}
