package helpers

// Port of internal/api/scripts/native_search.py: an incremental,
// target-local SQLite FTS5 index of visible native conversation messages.
// The index file, its schema and every stored value are the same as the
// Python version's, so either can pick up an index the other built.

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"sort"
	"strings"
	"time"
)

func init() {
	Register("native-search", nativeSearchMain)
	Register("native-search-read", nativeSearchReadMain)
}

const searchLineLimit = 10 * 1024 * 1024

const searchSchema = `
CREATE TABLE IF NOT EXISTS documents(
 id INTEGER PRIMARY KEY, path TEXT UNIQUE NOT NULL, cid TEXT NOT NULL, cwd TEXT NOT NULL,
 title TEXT NOT NULL, modified REAL NOT NULL, device INTEGER, inode INTEGER,
 observed INTEGER DEFAULT 0, stamp INTEGER DEFAULT 0, offset INTEGER DEFAULT 0,
 prefix_len INTEGER DEFAULT 0, prefix TEXT DEFAULT '', tail TEXT DEFAULT '',
 pending INTEGER DEFAULT 1, skipping INTEGER DEFAULT 0, oversized INTEGER DEFAULT 0, touched INTEGER DEFAULT 0);
CREATE TABLE IF NOT EXISTS failures(
 path TEXT PRIMARY KEY, device INTEGER, inode INTEGER, observed INTEGER, stamp INTEGER,
 changed INTEGER, reason TEXT NOT NULL, retry_after REAL, touched INTEGER);
CREATE TABLE IF NOT EXISTS messages(
 id INTEGER PRIMARY KEY, doc INTEGER NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
 offset INTEGER NOT NULL, end_offset INTEGER NOT NULL, role TEXT NOT NULL, text TEXT NOT NULL, fingerprint TEXT NOT NULL, UNIQUE(doc,offset));
CREATE INDEX IF NOT EXISTS messages_doc ON messages(doc);
CREATE VIRTUAL TABLE IF NOT EXISTS message_search USING fts5(text,content=messages,content_rowid=id);
CREATE TRIGGER IF NOT EXISTS message_insert AFTER INSERT ON messages BEGIN
 INSERT INTO message_search(rowid,text) VALUES(new.id,new.text); END;
CREATE TRIGGER IF NOT EXISTS message_delete AFTER DELETE ON messages BEGIN
 INSERT INTO message_search(message_search,rowid,text) VALUES('delete',old.id,old.text); END;
`

type searchIndex struct {
	home, agent, path, key string
	db                     *sqliteDB
}

// sqliteURI names a plain file path as an SQLite URI, so no character in it
// is read as URI syntax (Python's sqlite3.connect(path) takes it verbatim).
func sqliteURI(path string) string {
	r := strings.NewReplacer("%", "%25", "?", "%3f", "#", "%23")
	return "file:" + r.Replace(path)
}

// mkdirPy is Path.mkdir(mode, parents=True, exist_ok=True): missing parents
// get the default mode, only the leaf gets mode.
func mkdirPy(dir string, mode os.FileMode) error {
	err := os.Mkdir(dir, mode)
	if err == nil {
		return nil
	}
	if errors.Is(err, fs.ErrNotExist) {
		if parent := pathParent(dir); parent != dir {
			if err := mkdirPy(parent, 0o777); err != nil {
				return err
			}
		}
		if err = os.Mkdir(dir, mode); err == nil {
			return nil
		}
	}
	if isDir(dir) {
		return nil
	}
	return osError(err, dir)
}

// pathParent is str(Path(p).parent) for a normalized path.
func pathParent(p string) string {
	i := strings.LastIndex(p, "/")
	switch {
	case i < 0:
		return "."
	case i == 0:
		return "/"
	}
	return p[:i]
}

func openSearchIndex(cache, home, agent string) (*searchIndex, error) {
	if agent != "claude" && agent != "codex" {
		return nil, pyError("ValueError", "Unsupported native agent")
	}
	ix := &searchIndex{home: realpathLoose(pathStr(expanduser(pathStr(home)))), agent: agent}
	// Keep indexes separate even when callers use a common private cache root.
	sum := sha256.Sum256([]byte(agent + "\x00" + ix.home))
	ix.key = hex.EncodeToString(sum[:])
	folder := pathStr(expanduser(pathStr(cache)), "native-search-v2")
	if err := mkdirPy(folder, 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(folder, 0o700); err != nil {
		return nil, osError(err, folder)
	}
	ix.path = pathStr(folder, ix.key+".sqlite3")
	f, err := openCreateNoFollow(ix.path)
	if err != nil {
		return nil, osError(err, ix.path)
	}
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return nil, osError(err, "")
	}
	f.Close()
	db, err := openSQLite(sqliteURI(ix.path), 3000)
	if err != nil {
		return nil, err
	}
	ix.db = db
	if _, err := db.exec("PRAGMA foreign_keys=ON"); err != nil {
		db.Close()
		return nil, err
	}
	row, err := db.queryRow("PRAGMA user_version")
	if err != nil {
		db.Close()
		return nil, err
	}
	if v := asInt64(row["user_version"]); v != 0 && v != 2 {
		db.Close()
		return nil, pyError("ValueError", "Search cache uses an unsupported version")
	}
	if _, err := db.exec(searchSchema + "\nPRAGMA user_version=2;"); err != nil {
		db.Close()
		return nil, err
	}
	return ix, nil
}

func (ix *searchIndex) Close() { ix.db.Close() }

// transaction is `with self.db:` around an explicit BEGIN: commit on
// success, roll back on error.
func (ix *searchIndex) transaction(begin string, fn func() error) error {
	if _, err := ix.db.exec(begin); err != nil {
		return err
	}
	if err := fn(); err != nil {
		_, _ = ix.db.exec("ROLLBACK")
		return err
	}
	_, err := ix.db.exec("COMMIT")
	return err
}

func (ix *searchIndex) reset() error {
	// Derived data only: source conversations and other profile indexes
	// stay intact.
	return ix.transaction("BEGIN IMMEDIATE", func() error {
		if _, err := ix.db.exec("DELETE FROM documents"); err != nil {
			return err
		}
		_, err := ix.db.exec("DELETE FROM failures")
		return err
	})
}

func (ix *searchIndex) base() string {
	if ix.agent == "codex" {
		return pathStr(ix.home, "sessions")
	}
	return pathStr(ix.home, "projects")
}

func (ix *searchIndex) files() ([]string, error) {
	base := ix.base()
	resolved := realpathLoose(base)
	var found []string
	seen := map[string]bool{}
	for _, p := range pathlibGlobJSONL(base, ix.agent == "codex") {
		real := realpathLoose(p)
		if !isUnder(resolved, real) || !isFile(real) {
			continue
		}
		if !seen[real] {
			seen[real] = true
			found = append(found, real)
		}
		if len(found) > 10000 {
			return nil, pyError("ValueError", "Native history exceeds the 10000-file discovery limit")
		}
	}
	var statErr error
	sortByDesc(found, func(p string) float64 {
		m, err := getmtime(p)
		if err != nil && statErr == nil {
			statErr = err
		}
		return m
	})
	return found, statErr
}

func digestAt(f *pyFile, start, length int64) (string, error) {
	f.pos = start
	data, err := f.read(length)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func messageFingerprint(role, text string) string {
	sum := sha256.Sum256([]byte(role + "\x00" + text))
	return hex.EncodeToString(sum[:])
}

// fileStat is the os.stat_result fields the index compares.
type fileStat struct {
	dev, ino, size, mtimeNs, ctimeNs int64
	mtime                            float64
}

func statPath(p string) (fileStat, error) {
	fi, err := os.Stat(p)
	if err != nil {
		return fileStat{}, osError(err, p)
	}
	return statOf(fi), nil
}

func statOf(fi os.FileInfo) fileStat {
	dev, ino, ctime := statIDs(fi)
	return fileStat{dev: dev, ino: ino, size: fi.Size(), mtimeNs: statMtimeNs(fi), ctimeNs: ctime, mtime: statMtime(fi)}
}

func nowFloat() float64 { return float64(time.Now().UnixNano()) / 1e9 }

func (ix *searchIndex) failed(path string, st fileStat, reason string) error {
	_, err := ix.db.exec("INSERT OR REPLACE INTO failures VALUES(?,?,?,?,?,?,?,?,?)",
		path, st.dev, st.ino, st.size, st.mtimeNs, st.ctimeNs, reason, nowFloat()+30, time.Now().UnixNano())
	return err
}

// caughtBySync reports whether sync's per-file handler caught err (OSError,
// ValueError, TypeError); SQLite errors and crashes abort the whole pass.
func caughtBySync(err error) bool {
	if errors.Is(err, errCrash) {
		return false
	}
	return pyClass(err) != "sqlite3.Error"
}

func (ix *searchIndex) sync(byteBudget int64, seconds time.Duration) (*pyDict, error) {
	deadline := time.Now().Add(seconds)
	used := int64(0)
	var issues []string
	failed := map[string]bool{}
	files, err := ix.files()
	if err != nil {
		return nil, err
	}
	names := map[string]bool{}
	for _, p := range files {
		names[p] = true
	}
	err = ix.transaction("BEGIN IMMEDIATE", func() error {
		touched := map[string]int64{}
		for _, q := range []string{"SELECT path,touched FROM documents", "SELECT path,touched FROM failures"} {
			rows, err := ix.db.queryAll(q, -1)
			if err != nil {
				return err
			}
			for _, r := range rows {
				touched[asString(r["path"])] = asInt64(r["touched"])
			}
		}
		sort.SliceStable(files, func(i, j int) bool { return touched[files[i]] < touched[files[j]] })
		rows, err := ix.db.queryAll("SELECT path FROM failures", -1)
		if err != nil {
			return err
		}
		for _, r := range rows {
			if p := asString(r["path"]); !names[p] {
				if _, err := ix.db.exec("DELETE FROM failures WHERE path=?", p); err != nil {
					return err
				}
			}
		}
		rows, err = ix.db.queryAll("SELECT id,path FROM documents", -1)
		if err != nil {
			return err
		}
		for _, r := range rows {
			if !names[asString(r["path"])] {
				if _, err := ix.db.exec("DELETE FROM documents WHERE id=?", r["id"]); err != nil {
					return err
				}
			}
		}
		for _, path := range files {
			if used >= byteBudget || !time.Now().Before(deadline) {
				break
			}
			old, err := ix.db.queryRow("SELECT * FROM documents WHERE path=?", path)
			if err != nil {
				return err
			}
			var st *fileStat
			issue, err := ix.syncFile(path, old, &st, &used, byteBudget, deadline)
			if err != nil {
				if !caughtBySync(err) {
					return err
				}
				// Include any replacement row allocated during this attempt.
				if _, e := ix.db.exec("DELETE FROM documents WHERE path=?", path); e != nil {
					return e
				}
				issue = pyClass(err) + " reading a transcript"
				if st != nil {
					if e := ix.failed(path, *st, issue); e != nil {
						return e
					}
				}
			}
			if issue != "" {
				failed[path] = true
				issues = append(issues, issue)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	docs, err := ix.db.queryAll("SELECT path,pending,observed,stamp FROM documents", -1)
	if err != nil {
		return nil, err
	}
	byPath := map[string]map[string]any{}
	for _, r := range docs {
		byPath[asString(r["path"])] = r
	}
	pending := 0
	for _, path := range files {
		if failed[path] {
			continue
		}
		st, err := statPath(path)
		row := byPath[path]
		if err != nil || row == nil || asInt64(row["pending"]) != 0 || asInt64(row["observed"]) != st.size || asInt64(row["stamp"]) != st.mtimeNs {
			pending++
		}
	}
	over, err := ix.db.queryRow("SELECT COALESCE(sum(oversized),0) AS n FROM documents")
	if err != nil {
		return nil, err
	}
	sort.Strings(issues)
	unique := []any{}
	for i, s := range issues {
		if i == 0 || s != issues[i-1] {
			unique = append(unique, s)
		}
	}
	return dict("complete", pending == 0, "pending_files", pending, "scanned_bytes", used, "documents", len(byPath),
		"oversized_entries", asInt64(over["n"]), "issues", unique), nil
}

// syncFile indexes one transcript as far as the budget allows. It returns
// the issue to report when the file is skipped as failed; *st is set once
// the file has been stat'ed, for the caller's failure record.
func (ix *searchIndex) syncFile(path string, old map[string]any, st **fileStat, used *int64, budget int64, deadline time.Time) (string, error) {
	s, err := statPath(path)
	if err != nil {
		return "", err
	}
	*st = &s
	failure, err := ix.db.queryRow("SELECT * FROM failures WHERE path=?", path)
	if err != nil {
		return "", err
	}
	if failure != nil && asInt64(failure["device"]) == s.dev && asInt64(failure["inode"]) == s.ino && asInt64(failure["observed"]) == s.size &&
		asInt64(failure["stamp"]) == s.mtimeNs && asInt64(failure["changed"]) == s.ctimeNs && asFloat(failure["retry_after"]) > nowFloat() {
		return asString(failure["reason"]), nil
	}
	if _, err := ix.db.exec("DELETE FROM failures WHERE path=?", path); err != nil {
		return "", err
	}
	if old != nil && asInt64(old["pending"]) == 0 && s.size == asInt64(old["observed"]) && s.mtimeNs == asInt64(old["stamp"]) &&
		s.ino == asInt64(old["inode"]) && s.dev == asInt64(old["device"]) {
		return "", nil
	}
	info, err := nativeMetadata(path, ix.agent, nil, searchLineLimit)
	if err != nil {
		return "", err
	}
	if info == nil {
		if old != nil {
			if _, err := ix.db.exec("DELETE FROM documents WHERE id=?", old["id"]); err != nil {
				return "", err
			}
		}
		const reason = "A transcript has no valid native header"
		if err := ix.failed(path, s, reason); err != nil {
			return "", err
		}
		return reason, nil
	}
	f, err := openPy(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	reset := old == nil
	if old != nil {
		reset = s.ino != asInt64(old["inode"]) || s.dev != asInt64(old["device"]) || s.size < asInt64(old["observed"]) ||
			info.vals["id"] != asString(old["cid"]) || info.vals["cwd"] != asString(old["cwd"]) ||
			(asInt64(old["pending"]) == 0 && s.size == asInt64(old["observed"]) && s.mtimeNs != asInt64(old["stamp"]))
		if !reset {
			d, err := digestAt(f, 0, asInt64(old["prefix_len"]))
			if err != nil {
				return "", err
			}
			reset = d != asString(old["prefix"])
		}
		if !reset {
			off := asInt64(old["offset"])
			d, err := digestAt(f, max(0, off-256), min(256, off))
			if err != nil {
				return "", err
			}
			reset = d != asString(old["tail"])
		}
	}
	var doc, offset, skipping, oversized int64
	if reset {
		if old != nil {
			if _, err := ix.db.exec("DELETE FROM documents WHERE id=?", old["id"]); err != nil {
				return "", err
			}
		}
		res, err := ix.db.exec("INSERT INTO documents(path,cid,cwd,title,modified,device,inode) VALUES(?,?,?,?,?,?,?)",
			path, info.vals["id"], info.vals["cwd"], info.vals["title"], s.mtime, s.dev, s.ino)
		if err != nil {
			return "", err
		}
		if doc, err = res.LastInsertId(); err != nil {
			return "", sqliteError(err)
		}
	} else {
		doc, offset, skipping, oversized = asInt64(old["id"]), asInt64(old["offset"]), asInt64(old["skipping"]), asInt64(old["oversized"])
	}
	f.pos = offset
	pending := int64(0)
	for offset < s.size {
		if *used >= budget || !time.Now().Before(deadline) {
			pending = 1
			break
		}
		line, err := f.readline(min(searchLineLimit+1, s.size-offset))
		if err != nil {
			return "", err
		}
		*used += int64(len(line))
		if len(line) == 0 {
			break
		}
		complete := line[len(line)-1] == '\n'
		if skipping != 0 {
			offset += int64(len(line))
			skipping = boolInt(!complete)
			continue
		}
		if len(line) > searchLineLimit {
			oversized++
			offset += int64(len(line))
			skipping = boolInt(!complete)
			continue
		}
		if !complete {
			break // retry this partial entry after append
		}
		position := offset
		offset += int64(len(line))
		v, err := jsonLoadsBytes(line)
		if err != nil {
			continue
		}
		row, ok := v.(*pyDict)
		if !ok {
			continue
		}
		if m := nativeRecord(row, ix.agent, noLimit); m != nil {
			role, text := m.vals["role"].(string), m.vals["text"].(string)
			if _, err := ix.db.exec("INSERT OR IGNORE INTO messages(doc,offset,end_offset,role,text,fingerprint) VALUES(?,?,?,?,?,?)",
				doc, position, offset, role, text, messageFingerprint(role, text)); err != nil {
				return "", err
			}
		}
	}
	prefixLen := min(offset, 4096)
	prefix, err := digestAt(f, 0, prefixLen)
	if err != nil {
		return "", err
	}
	tail, err := digestAt(f, max(0, offset-256), min(256, offset))
	if err != nil {
		return "", err
	}
	_, err = ix.db.exec("UPDATE documents SET title=?,modified=?,observed=?,stamp=?,offset=?,prefix_len=?,prefix=?,tail=?,pending=?,skipping=?,oversized=?,touched=? WHERE id=?",
		info.vals["title"], s.mtime, s.size, s.mtimeNs, offset, prefixLen, prefix, tail, pending, skipping, oversized, time.Now().UnixNano(), doc)
	return "", err
}

func boolInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

// searchMatchExpr quotes every term, so the query is plain words, never
// FTS5 syntax.
func searchMatchExpr(query string) string {
	terms := pySplit(query)
	quoted := make([]string, len(terms))
	for i, t := range terms {
		quoted[i] = `"` + strings.ReplaceAll(t, `"`, `""`) + `"`
	}
	return strings.Join(quoted, " AND ")
}

func searchMarkers() (string, string) {
	var b [12]byte
	_, _ = rand.Read(b[:])
	marker := "lec-" + hex.EncodeToString(b[:])
	return "[" + marker + "]", "[/" + marker + "]"
}

func (ix *searchIndex) search(query string) (*pyDict, error) {
	if len(pySplit(query)) == 0 || runeLen(query) > 500 {
		return nil, pyError("ValueError", "Enter between 1 and 500 characters")
	}
	match := searchMatchExpr(query)
	const limit = 50
	opening, closing := searchMarkers()
	rows, err := ix.db.queryAll(`WITH hits AS (
            SELECT min(m.id) AS id FROM message_search
            JOIN messages m ON m.id=message_search.rowid
            WHERE message_search MATCH ? GROUP BY m.doc)
            SELECT d.id AS document,d.cid,d.cwd,d.title,d.modified,m.role,m.offset,m.end_offset,m.fingerprint,
            snippet(message_search,0,?,?, ' … ',32) AS snippet
            FROM message_search JOIN messages m ON m.id=message_search.rowid
            JOIN documents d ON d.id=m.doc JOIN hits ON hits.id=m.id
            WHERE message_search MATCH ? ORDER BY d.modified DESC LIMIT ?`, -1, match, opening, closing, match, limit+1)
	if err != nil {
		return nil, err
	}
	matches := []any{}
	for i, r := range rows {
		if i == limit {
			break
		}
		snippet, _ := markedExcerpt(asString(r["snippet"]), opening, closing, 1200)
		matches = append(matches, dict("document", r["document"], "cid", r["cid"], "cwd", r["cwd"], "title", r["title"],
			"modified", sqlValue(r["modified"]), "role", r["role"], "offset", r["offset"], "end_offset", r["end_offset"],
			"fingerprint", r["fingerprint"], "snippet", snippet))
	}
	return dict("matches", matches, "more", len(rows) > limit), nil
}

// sqlValue is a database value as Python's sqlite3 returns it.
func sqlValue(v any) any {
	if b, ok := v.([]byte); ok {
		return string(b)
	}
	return v
}

// markedExcerpt removes the highlight markers and, beyond limit
// characters, keeps a window that starts shortly before the first match.
func markedExcerpt(text, opening, closing string, limit int) (string, bool) {
	position := runeIndex(text, opening)
	clean := strings.ReplaceAll(strings.ReplaceAll(text, opening, ""), closing, "")
	n := runeLen(clean)
	if n <= limit {
		return clean, false
	}
	start := max(0, max(position, 0)-min(200, limit/4))
	out := runeSlice(clean, start, start+limit)
	if start != 0 {
		out = "… " + out
	}
	if start+limit < n {
		out += " …"
	}
	return out, true
}

// searchHomeAndCache is where the scripts found the native profile and the
// index cache.
func searchHomeAndCache(agent string) (string, string) {
	home := expanduser(envOr("CLAUDE_CONFIG_DIR", "~/.claude"))
	if agent == "codex" {
		home = expanduser(envOr("CODEX_HOME", "~/.codex"))
	}
	cache := os.Getenv("LECTERN_NATIVE_SEARCH_CACHE")
	if cache == "" {
		cache = pathStr(envOr("XDG_CACHE_HOME", pathStr(expanduser("~"), ".cache")), "lectern")
	}
	return home, cache
}

// argparseError is argparse's usage failure: exit status 2.
func argparseError(stderr io.Writer, prog, msg string) int {
	fmt.Fprintf(stderr, "usage: %s\n%s: error: %s\n", prog, prog, msg)
	return 2
}

// nativeSearchMain is `native-search [--reset] AGENT -- QUERY`.
func nativeSearchMain(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	const prog = "native-search [--reset] {claude,codex} query"
	reset := false
	var positional []string
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--":
			positional = append(positional, args[i+1:]...)
			i = len(args)
		case a == "--reset":
			reset = true
		case strings.HasPrefix(a, "-") && a != "-":
			return argparseError(stderr, prog, "unrecognized arguments: "+a)
		default:
			positional = append(positional, a)
		}
	}
	if len(positional) != 2 {
		return argparseError(stderr, prog, "expected AGENT and QUERY")
	}
	agent, query := positional[0], positional[1]
	if agent != "claude" && agent != "codex" {
		return argparseError(stderr, prog, "argument agent: invalid choice: "+strRepr(agent)+" (choose from 'claude', 'codex')")
	}
	home, cache := searchHomeAndCache(agent)
	out, err := func() (*pyDict, error) {
		if pyStrip(query) == "" || runeLen(query) > 500 {
			return nil, pyError("ValueError", "Enter between 1 and 500 characters")
		}
		ix, err := openSearchIndex(cache, home, agent)
		if err != nil {
			return nil, err
		}
		defer ix.Close()
		if reset {
			if err := ix.reset(); err != nil {
				return nil, err
			}
		}
		progress, err := ix.sync(16*1024*1024, 2*time.Second)
		if err != nil {
			return nil, err
		}
		found, err := ix.search(query)
		if err != nil {
			return nil, err
		}
		return dict("agent", agent, "profile_key", ix.key, "progress", progress, "matches", found.vals["matches"], "more", found.vals["more"]), nil
	}()
	if err != nil {
		return printError(stdout, stderr, err)
	}
	fmt.Fprintln(stdout, jsonDumps(out, false))
	return 0
}
