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
	"unicode/utf8"

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

// Size limits on one record. They apply at the tool boundary and in Add, so
// a memory stays a short note and not a dumped transcript.
const (
	MaxTitle  = 200
	MaxBody   = 2000
	MaxTags   = 8
	MaxTagLen = 32
	MaxFiles  = 16
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
	// Source is who wrote the record: "user" (the person, through the TUI
	// or an import) or "agent" (the memory tool).
	Source string `json:"source,omitempty"`
	// SupersededBy is the id of the record that replaced this one. A
	// superseded record is kept but never recalled, searched or listed.
	SupersededBy string `json:"superseded_by,omitempty"`
	// Hidden is the soft-delete state: a hidden record is kept on disk but
	// never recalled, searched, listed or deduplicated against. Restore
	// clears it. The memory tool's delete is still a hard delete.
	Hidden bool `json:"hidden,omitempty"`
	// SessionID is the session that saved the record ("" when unknown).
	SessionID string `json:"session_id,omitempty"`
}

// Scored is a recalled record with its relevance score and the signals
// that produced it ("fts", "trigram", "file", "similar", "pinned", "co-change").
type Scored struct {
	Record
	Score float64  `json:"score"`
	Why   []string `json:"why,omitempty"`
	// Sim is the cosine of the query and the record's embedding. It is set
	// only by a recall, and is shown when Why carries "similar".
	Sim float64 `json:"sim,omitempty"`
}

// DB is one memory database.
type DB struct {
	db      *sql.DB
	scope   string
	path    string
	now     func() time.Time
	session string // stamped on records saved while set; see SetSession
}

// live is the SQL filter for records that recall, search, list and dedupe
// may see: not superseded and not hidden.
const live = "superseded_by = '' AND hidden = 0"

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

// migrations are applied in order after schema, which is the version-1
// layout and is idempotent, so a database that predates versioning takes the
// same path as a new one. Entry i moves user_version from i+1 to i+2; the
// list only ever grows. Entries 0-1 add provenance and supersession; entries
// 2-3 add the trigram index and back-fill it from the existing rows.
var migrations = []string{
	"ALTER TABLE memories ADD COLUMN source TEXT NOT NULL DEFAULT 'user'",
	"ALTER TABLE memories ADD COLUMN superseded_by TEXT NOT NULL DEFAULT ''",
	// Trigram matches inside words and identifiers (getUserName, "migrat"),
	// which the unicode61 word index cannot see.
	"CREATE VIRTUAL TABLE IF NOT EXISTS memories_tri USING fts5(id UNINDEXED, title, body, tags, tokenize = 'trigram')",
	"INSERT INTO memories_tri (id, title, body, tags) SELECT id, title, body, tags FROM memories_fts",
	// Soft delete: a hidden record stays on disk until restored or cleared.
	"ALTER TABLE memories ADD COLUMN hidden INTEGER NOT NULL DEFAULT 0",
	// Provenance: the session that saved the record.
	"ALTER TABLE memories ADD COLUMN session_id TEXT NOT NULL DEFAULT ''",
}

// Sources a record may carry.
const (
	SourceUser  = "user"
	SourceAgent = "agent"
)

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
	return d.migrate()
}

// schemaVersion is the user_version after every migration has run.
func schemaVersion() int { return 1 + len(migrations) }

// migrate applies each pending migration in its own transaction and records
// the new version in PRAGMA user_version, so an interrupted upgrade reruns
// from the last committed step.
func (d *DB) migrate() error {
	var cur int
	if err := d.db.QueryRow("PRAGMA user_version").Scan(&cur); err != nil {
		return err
	}
	if cur > schemaVersion() {
		return fmt.Errorf("database schema v%d is newer than this Scribe (v%d); upgrade Scribe", cur, schemaVersion())
	}
	for v := cur; v < schemaVersion(); v++ {
		tx, err := d.db.Begin()
		if err != nil {
			return err
		}
		if v >= 1 {
			if _, err := tx.Exec(migrations[v-1]); err != nil {
				tx.Rollback()
				return fmt.Errorf("migration to v%d: %w", v+1, err)
			}
		}
		if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", v+1)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
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

const cols = "id, category, title, body, tags, created, updated, uses, last_used, pinned, source, superseded_by, hidden, session_id"

type scanner interface {
	Scan(dest ...any) error
}

func scanRecord(sc scanner) (Record, error) {
	var r Record
	var tags, created, updated, lastUsed string
	var pinned, hidden int
	if err := sc.Scan(&r.ID, &r.Category, &r.Title, &r.Body, &tags, &created, &updated, &r.Uses, &lastUsed, &pinned, &r.Source, &r.SupersededBy, &hidden, &r.SessionID); err != nil {
		return Record{}, err
	}
	r.Hidden = hidden != 0
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
	source := r.Source
	if source == "" {
		source = SourceUser
	}
	if _, err := q.Exec(`INSERT INTO memories
		(id, category, title, body, norm_title, tags, created, updated, uses, last_used, pinned, embedding, source, superseded_by, hidden, session_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
		category = excluded.category, title = excluded.title, body = excluded.body,
		norm_title = excluded.norm_title, tags = excluded.tags, updated = excluded.updated,
		uses = excluded.uses, last_used = excluded.last_used, pinned = excluded.pinned,
		embedding = excluded.embedding, superseded_by = excluded.superseded_by,
		hidden = excluded.hidden, session_id = excluded.session_id`,
		r.ID, r.Category, r.Title, r.Body, normTitle(r.Title), string(tags),
		fmtTime(r.Created), fmtTime(r.Updated), r.Uses, fmtTime(r.LastUsed), boolInt(r.Pinned), emb,
		source, r.SupersededBy, boolInt(r.Hidden), r.SessionID); err != nil {
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
	for _, table := range []string{"memories_fts", "memories_tri"} {
		if _, err := q.Exec("DELETE FROM "+table+" WHERE id = ?", r.ID); err != nil {
			return err
		}
		if _, err := q.Exec("INSERT INTO "+table+" (id, title, body, tags) VALUES (?, ?, ?, ?)",
			r.ID, r.Title, r.Body, strings.Join(r.Tags, " ")); err != nil {
			return err
		}
	}
	return nil
}

func removeRecord(q execer, id string) error {
	for _, stmt := range []string{
		"DELETE FROM memories_fts WHERE id = ?",
		"DELETE FROM memories_tri WHERE id = ?",
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
	if utf8.RuneCountInString(r.Title) > MaxTitle {
		return fmt.Errorf("title is longer than %d characters; shorten it and put detail in body", MaxTitle)
	}
	if utf8.RuneCountInString(r.Body) > MaxBody {
		return fmt.Errorf("body is longer than %d characters; keep the essentials or save two memories", MaxBody)
	}
	if r.Category == "" {
		r.Category = CatFact
	}
	if !validCategory(r.Category) {
		return fmt.Errorf("unknown category %q; use one of %s", r.Category, strings.Join(Categories, ", "))
	}
	if r.Source == "" {
		r.Source = SourceUser
	}
	if r.Source != SourceUser && r.Source != SourceAgent {
		return fmt.Errorf("unknown source %q; use user or agent", r.Source)
	}
	r.Tags = cleanTags(r.Tags)
	if len(r.Tags) > MaxTags {
		return fmt.Errorf("at most %d tags per memory; you gave %d", MaxTags, len(r.Tags))
	}
	for _, t := range r.Tags {
		if utf8.RuneCountInString(t) > MaxTagLen {
			return fmt.Errorf("tag %q is longer than %d characters", t, MaxTagLen)
		}
	}
	r.Files = normalizeFiles(r.Files)
	if len(r.Files) > MaxFiles {
		return fmt.Errorf("at most %d file paths per memory; you gave %d", MaxFiles, len(r.Files))
	}
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
		cur.Uses++
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
	if r.SessionID == "" {
		r.SessionID = d.session
	}
	r.Hidden = false
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
	rows, err := q.Query("SELECT id, norm_title, body FROM memories WHERE " + live)
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
	Topic      string // matched case-insensitively against tags
	PinnedOnly bool
	Limit      int // default 100
}

// List returns live records (not superseded), pinned first, then most
// recently used.
func (d *DB) List(o ListOptions) ([]Record, error) {
	if o.Limit <= 0 {
		o.Limit = 100
	}
	q := "SELECT " + cols + " FROM memories WHERE " + live
	var args []any
	if o.Category != "" {
		q += " AND category = ?"
		args = append(args, o.Category)
	}
	if t := strings.ToLower(strings.TrimSpace(o.Topic)); t != "" {
		q += " AND EXISTS (SELECT 1 FROM json_each(memories.tags) WHERE json_each.value = ?)"
		args = append(args, t)
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

// Search runs a full-text query over title, body and tags, best match first.
// Word matches come from the unicode61 index; substring matches from the
// trigram index (see textHits). Score is BM25 normalised to (0,1).
func (d *DB) Search(query string, limit int) ([]Scored, error) {
	if limit <= 0 {
		limit = 10
	}
	hits, triOnly, err := d.textHits(query, ftsCandidates)
	if err != nil {
		return nil, err
	}
	ids := sortedByScore(hits)
	var out []Scored
	for _, id := range ids {
		r, err := loadOne(d.db, id)
		if err != nil {
			return nil, err
		}
		if r.SupersededBy != "" || r.Hidden {
			continue
		}
		r.Scope = d.scope
		why := "fts"
		if triOnly[id] {
			why = "trigram"
		}
		out = append(out, Scored{Record: r, Score: bm25Norm(hits[id]), Why: []string{why}})
		if len(out) == limit {
			break
		}
	}
	return out, nil
}

// Similar returns live records whose hashed embedding has cosine >= min to
// the record idOrPrefix, most similar first. The record itself is excluded.
// It is how a save reports near-duplicates for the agent to supersede.
func (d *DB) Similar(idOrPrefix string, min float64, limit int) ([]Scored, error) {
	if limit <= 0 {
		limit = 3
	}
	id, err := resolveID(d.db, idOrPrefix)
	if err != nil {
		return nil, err
	}
	var emb []byte
	if err := d.db.QueryRow("SELECT embedding FROM memories WHERE id = ?", id).Scan(&emb); err != nil {
		return nil, err
	}
	base := decodeVec(emb)
	if base == nil {
		return nil, nil
	}
	rows, err := d.db.Query("SELECT id, embedding FROM memories WHERE id <> ? AND "+live, id)
	if err != nil {
		return nil, err
	}
	type cand struct {
		id  string
		sim float64
	}
	var cands []cand
	for rows.Next() {
		var other string
		var e []byte
		if err := rows.Scan(&other, &e); err != nil {
			rows.Close()
			return nil, err
		}
		if v := decodeVec(e); v != nil {
			if sim := dot(base, v); sim >= min {
				cands = append(cands, cand{other, sim})
			}
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].sim != cands[j].sim {
			return cands[i].sim > cands[j].sim
		}
		return cands[i].id < cands[j].id
	})
	var out []Scored
	for _, c := range cands {
		if len(out) == limit {
			break
		}
		r, err := loadOne(d.db, c.id)
		if err != nil {
			return nil, err
		}
		r.Scope = d.scope
		out = append(out, Scored{Record: r, Score: c.sim, Why: []string{"similar"}})
	}
	return out, nil
}

// Supersede marks the record oldIDOrPrefix as replaced by newID. Both must
// be in this database. A superseded record stays on disk for inspection but
// is no longer recalled, searched, listed or deduplicated against.
func (d *DB) Supersede(oldIDOrPrefix, newID string) error {
	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	oldID, err := resolveID(tx, oldIDOrPrefix)
	if err != nil {
		return err
	}
	newFull, err := resolveID(tx, newID)
	if err != nil {
		return err
	}
	if oldID == newFull {
		return errors.New("a memory cannot supersede itself")
	}
	if _, err := tx.Exec("UPDATE memories SET superseded_by = ?, updated = ? WHERE id = ?", newFull, fmtTime(d.now()), oldID); err != nil {
		return err
	}
	return tx.Commit()
}

// StaleCandidates lists unpinned live records not used within maxAge and
// used fewer than minUses times, least recently used first. It only reports:
// nothing is deleted, so the agent or the person decides what to remove.
func (d *DB) StaleCandidates(maxAge time.Duration, minUses, limit int) ([]Record, error) {
	if limit <= 0 {
		limit = 20
	}
	cutoff := fmtTime(d.now().Add(-maxAge))
	return d.queryRecords("SELECT "+cols+" FROM memories WHERE pinned = 0 AND "+live+" AND last_used < ? AND uses < ? ORDER BY last_used ASC, id LIMIT ?",
		cutoff, minUses, limit)
}

// Clear hard-deletes every record in category (every record when category is
// ""), and returns how many were removed. It is for user-initiated bulk
// removal only; the agent has no path to it.
func (d *DB) Clear(category string) (int, error) {
	tx, err := d.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	q, args := "SELECT id FROM memories", []any(nil)
	if category != "" {
		q += " WHERE category = ?"
		args = append(args, category)
	}
	rows, err := tx.Query(q, args...)
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
