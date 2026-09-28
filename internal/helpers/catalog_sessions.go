package helpers

// Port of internal/nativeidentity/catalog_sessions.py: list a catalog
// agent's saved conversations for one workspace, read-only, from a listing
// command, session files or a SQLite table named by the agent definition.
//
// argv: spec_json workspace bin [selected]

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"
)

func init() {
	Register("catalog-sessions", catalogSessionsMain)
}

var catalogID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

const catalogMax = 2000

type catalogReader struct {
	spec      *nPyDict
	workspace string
	binary    string
	slug      string
}

func catalogSessionsMain(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	if len(args) < 3 {
		return crashed(stderr, fmt.Errorf("%w: missing arguments", errCrash))
	}
	v, err := jsonLoadsStr(args[0])
	spec, ok := v.(*nPyDict)
	if err != nil || !ok {
		return crashed(stderr, fmt.Errorf("%w: invalid spec", errCrash))
	}
	selected := ""
	if len(args) > 3 {
		selected = args[3]
	}
	workspace := realpathLoose(args[1])
	r := &catalogReader{spec: spec, workspace: workspace, binary: args[2], slug: slugUnsafe.ReplaceAllString(workspace, "-")}
	out, err := r.run(selected)
	if err != nil {
		return printError(stdout, stderr, err)
	}
	fmt.Fprintln(stdout, jsonDumps(out, true))
	return 0
}

// specStr is spec.get(key) as a string, "" when absent or falsy.
func (r *catalogReader) specStr(key string) string {
	s, _ := r.spec.getOr(key, nil).(string)
	return s
}

// specItem is spec[key], with Python's KeyError text.
func (r *catalogReader) specItem(key string) (any, error) {
	v, ok := r.spec.get(key)
	if !ok {
		return nil, pyError("KeyError", strRepr(key))
	}
	return v, nil
}

// expand is ~, $VAR and {slug} expanded; ok is false when a variable is
// unset, so the next candidate (usually the CLI's own default location) is
// used instead.
func (r *catalogReader) expand(pattern string) (string, bool) {
	value := expandvars(expanduser(strings.ReplaceAll(pattern, "{slug}", r.slug)))
	return value, !strings.Contains(value, "$")
}

func catalogField(obj any, name string) any {
	for _, part := range strings.Split(name, ".") {
		d, ok := obj.(*nPyDict)
		if !ok {
			return nil
		}
		obj = d.getOr(part, nil)
	}
	return obj
}

// seconds is epoch seconds from epoch s/ms/us numbers or an ISO-8601
// string; nil is None.
func seconds(value any) (any, error) {
	var v float64
	switch x := value.(type) {
	case nPyInt:
		f, err := pyIntToFloat(x)
		if err != nil {
			return nil, err
		}
		v = f
	case int64:
		v = float64(x)
	case float64:
		v = x
	case string:
		if x == "" {
			return nil, nil
		}
		text := strings.ReplaceAll(nPyStrip(x), "Z", "+00:00")
		if ts, ok := isoTimestamp(text); ok {
			return ts, nil
		}
		f, ok := pyFloatParse(text)
		if !ok {
			return nil, nil
		}
		return seconds(f)
	default:
		return nil, nil
	}
	for v > 1e11 { // ms or us (an infinite value never ends, as in Python)
		v /= 1000.0
	}
	return v, nil
}

// pyIntToFloat is float(int), which overflows rather than returning inf.
func pyIntToFloat(x nPyInt) (float64, error) {
	f, ok := pyFloatParse(string(x))
	if !ok || math.IsInf(f, 0) {
		return 0, fmt.Errorf("%w: OverflowError: int too large to convert to float", errCrash)
	}
	return f, nil
}

func (r *catalogReader) entry(obj *nPyDict, path string) (*nPyDict, error) {
	name := r.specStr("id")
	if name == "" {
		name = "id"
	}
	var cid any
	if name == "@stem" && path != "" {
		cid, _ = splitext(basename(path))
	} else {
		cid = catalogField(obj, name)
	}
	id, ok := cid.(string)
	if !ok || !catalogID.MatchString(id) {
		return nil, nil
	}
	if dirKey := r.specStr("dir"); dirKey != "" {
		dir, ok := catalogField(obj, dirKey).(string)
		if !ok || dir == "" || realpathLoose(dir) != r.workspace {
			return nil, nil
		}
	}
	createdKey := r.specStr("created")
	if createdKey == "" {
		createdKey = "created"
	}
	created, err := seconds(catalogField(obj, createdKey))
	if err != nil {
		return nil, err
	}
	var modified any
	if key := r.specStr("updated"); key != "" {
		if modified, err = seconds(catalogField(obj, key)); err != nil {
			return nil, err
		}
	}
	if path != "" && modified == nil {
		fi, err := os.Stat(path)
		if err != nil {
			return nil, osError(err, path)
		}
		modified = statMtime(fi)
	}
	title := "Saved conversation"
	if key := r.specStr("title"); key != "" {
		if t, ok := catalogField(obj, key).(string); ok && nPyStrip(t) != "" {
			title = runePrefix(strings.ReplaceAll(nPyStrip(t), "\n", " "), 160)
		}
	}
	if !truthy(modified) {
		modified = created
	}
	return dict("id", id, "title", title, "created", created, "modified", modified), nil
}

func (r *catalogReader) fromCommand() ([]*nPyDict, error) {
	raw, err := r.specItem("command")
	if err != nil {
		return nil, err
	}
	command := strings.ReplaceAll(nPyStr(raw), "{bin}", r.binary)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", "-c", command)
	cmd.Dir = r.workspace
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = io.Discard
	if fi, err := os.Stat(r.workspace); err != nil {
		return nil, osError(err, r.workspace)
	} else if !fi.IsDir() {
		return nil, osError(syscall.ENOTDIR, r.workspace)
	}
	if _, err := exec.LookPath("bash"); err != nil {
		return nil, pyError("FileNotFoundError", "[Errno 2] No such file or directory: 'bash'")
	}
	err = cmd.Run()
	if ctx.Err() != nil {
		return nil, pyError("TimeoutExpired", "Command '"+nPyRepr([]any{"bash", "-c", command})+"' timed out after 25 seconds")
	}
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return nil, pyError("ValueError", "the session listing command failed")
		}
		return nil, osError(err, "")
	}
	text := nPyStrip(decodeReplace(stdout.Bytes()))
	var rows []any
	var doc any = []any{}
	if text != "" {
		doc, err = jsonLoadsStr(text)
	}
	if err == nil {
		if d, ok := doc.(*nPyDict); ok {
			doc = []any{}
			for _, k := range d.keys {
				if list, ok := d.vals[k].([]any); ok {
					doc = list
					break
				}
			}
		}
		rows, _ = doc.([]any)
	} else {
		for _, line := range pySplitlines(text) {
			if v, err := jsonLoadsStr(line); err == nil {
				rows = append(rows, v)
			}
		}
	}
	if len(rows) > catalogMax {
		rows = rows[:catalogMax]
	}
	var out []*nPyDict
	for _, row := range rows {
		obj, ok := row.(*nPyDict)
		if !ok {
			continue
		}
		e, err := r.entry(obj, "")
		if err != nil {
			return nil, err
		}
		if e != nil {
			out = append(out, e)
		}
	}
	return out, nil
}

func (r *catalogReader) fromFiles() ([]*nPyDict, error) {
	raw, err := r.specItem("files")
	if err != nil {
		return nil, err
	}
	patterns, _ := raw.([]any)
	var paths []string
	for _, p := range patterns {
		pattern, ok := r.expand(nPyStr(p))
		if !ok {
			continue
		}
		paths = pyGlob(pattern, false)
		break
	}
	var statErr error
	sortByDesc(paths, func(p string) float64 {
		if _, err := os.Stat(p); err != nil {
			return 0
		}
		m, err := getmtime(p)
		if err != nil && statErr == nil {
			statErr = err
		}
		return m
	})
	if statErr != nil {
		return nil, statErr
	}
	if len(paths) > catalogMax {
		paths = paths[:catalogMax]
	}
	whole := truthy(r.spec.getOr("whole", nil))
	header := r.spec.getOr("header", nil)
	var out []*nPyDict
	for _, path := range paths {
		if !isFile(path) {
			continue
		}
		fi, err := os.Stat(path)
		if err != nil {
			return nil, osError(err, path)
		}
		if fi.Size() > 64*nativeLimit {
			continue
		}
		merged, ok := readCatalogFile(path, whole, header)
		if !ok {
			continue
		}
		e, err := r.entry(merged, path)
		if err != nil {
			return nil, err
		}
		if e != nil {
			out = append(out, e)
		}
	}
	return out, nil
}

// readCatalogFile merges a session file's first rows (or its whole JSON
// document); ok is false where the script skipped the file.
func readCatalogFile(path string, whole bool, header any) (*nPyDict, bool) {
	f, err := openPy(path)
	if err != nil {
		return nil, false
	}
	defer f.Close()
	merged := newDict()
	if whole {
		data, err := f.read(nativeLimit)
		if err != nil {
			return nil, false
		}
		doc, err := jsonLoadsBytes(data)
		if err != nil {
			return nil, false
		}
		if d, ok := doc.(*nPyDict); ok {
			merged = d
		}
		return merged, true
	}
	for n := 0; n < 40; n++ {
		line, err := f.readline(nativeLimit)
		if err != nil {
			return nil, false
		}
		if len(line) == 0 {
			break
		}
		v, err := jsonLoadsBytes(line)
		if err != nil {
			continue
		}
		row, ok := v.(*nPyDict)
		if !ok {
			continue
		}
		if truthy(header) && !pyEqual(row.getOr("type", nil), header) {
			continue
		}
		for _, k := range row.keys {
			merged.setdefault(k, row.vals[k])
		}
		if truthy(header) {
			break
		}
	}
	return merged, true
}

// pyEqual is == for the scalar values a spec can hold.
func pyEqual(a, b any) bool {
	switch x := b.(type) {
	case string:
		s, ok := a.(string)
		return ok && s == x
	case bool:
		y, ok := a.(bool)
		return ok && y == x
	}
	return nPyRepr(a) == nPyRepr(b)
}

func (r *catalogReader) fromSQLite() ([]*nPyDict, error) {
	raw, err := r.specItem("sqlite")
	if err != nil {
		return nil, err
	}
	candidates, _ := raw.([]any)
	path, found := "", false
	for _, c := range candidates {
		if p, ok := r.expand(nPyStr(c)); ok {
			path, found = p, true
			break
		}
	}
	if !found || path == "" || !isFile(path) {
		return nil, nil
	}
	db, err := openSQLite("file:"+path+"?mode=ro", 5000)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	q, err := r.specItem("query")
	if err != nil {
		return nil, err
	}
	names, rows, err := catalogQuery(db, nPyStr(q))
	if err != nil {
		return nil, err
	}
	var out []*nPyDict
	for _, row := range rows {
		obj := newDict()
		for i, n := range names {
			obj.set(n, row[i])
		}
		e, err := r.entry(obj, "")
		if err != nil {
			return nil, err
		}
		if e != nil {
			out = append(out, e)
		}
	}
	return out, nil
}

// catalogQuery runs the definition's SELECT and returns up to catalogMax
// rows as Python's sqlite3 would see them. The driver turns TEXT in
// DATE/DATETIME/TIMESTAMP columns into time values; Python never does, so
// such a query is re-read through a CTE whose unary-plus columns carry no
// declared type and keep every value exactly as stored.
func catalogQuery(db *sqliteDB, query string) ([]string, [][]any, error) {
	rows, err := db.query(query)
	if err != nil {
		return nil, nil, err
	}
	names, err := rows.Columns()
	if err != nil {
		rows.Close()
		return nil, nil, sqliteError(err)
	}
	types, _ := rows.ColumnTypes()
	timed := false
	for _, t := range types {
		switch strings.ToUpper(t.DatabaseTypeName()) {
		case "DATE", "DATETIME", "TIMESTAMP":
			timed = true
		}
	}
	if timed && len(names) > 0 {
		rows.Close()
		cols := make([]string, len(names))
		plus := make([]string, len(names))
		for i := range names {
			cols[i] = fmt.Sprintf("c%d", i)
			plus[i] = "+" + cols[i]
		}
		body := strings.TrimRight(strings.TrimSpace(query), "; \t\r\n")
		wrapped := "WITH lectern_rows(" + strings.Join(cols, ",") + ") AS (\n" + body + "\n) SELECT " + strings.Join(plus, ",") + " FROM lectern_rows"
		if rows, err = db.query(wrapped); err != nil {
			return nil, nil, err
		}
	}
	defer rows.Close()
	var out [][]any
	for len(out) < catalogMax && rows.Next() {
		vals := make([]any, len(names))
		ptrs := make([]any, len(names))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, nil, sqliteError(err)
		}
		for i, v := range vals {
			switch x := v.(type) {
			case int64:
				vals[i] = nPyInt(fmt.Sprint(x))
			case string:
				if !utf8.ValidString(x) {
					return nil, nil, pyError("sqlite3.Error", fmt.Sprintf("Could not decode to UTF-8 column '%s' with text '%s'", names[i], x))
				}
			case time.Time:
				vals[i] = x.String() // unreachable: timed columns were re-read raw
			}
		}
		out = append(out, vals)
	}
	return names, out, sqliteError(rows.Err())
}

func (r *catalogReader) run(selected string) (*nPyDict, error) {
	if selected != "" && !catalogID.MatchString(selected) {
		return nil, pyError("ValueError", "Choose a saved conversation ID")
	}
	var rows []*nPyDict
	var err error
	switch {
	case truthy(r.spec.getOr("command", nil)):
		rows, err = r.fromCommand()
	case truthy(r.spec.getOr("files", nil)):
		rows, err = r.fromFiles()
	case truthy(r.spec.getOr("sqlite", nil)):
		rows, err = r.fromSQLite()
	default:
		err = pyError("ValueError", "this agent does not say where it keeps sessions")
	}
	if err != nil {
		return nil, err
	}
	key := func(d *nPyDict) float64 {
		if m := d.vals["modified"]; truthy(m) {
			return m.(float64)
		}
		return 0
	}
	sorted := append([]*nPyDict(nil), rows...)
	sortDictsDesc(sorted, key)
	seen := map[string]bool{}
	conversations := []any{}
	var chosen *nPyDict
	for _, row := range sorted {
		id := row.vals["id"].(string)
		if seen[id] {
			continue
		}
		seen[id] = true
		row.set("agent", r.spec.getOr("agent", ""))
		conversations = append(conversations, row)
		if selected != "" && id == selected && chosen == nil {
			chosen = row
		}
	}
	if selected != "" {
		if chosen == nil {
			return nil, pyError("ValueError", "Conversation not found in this workspace on this target")
		}
		return dict("conversation", chosen, "messages", []any{}, "before", nil, "messages_readable", false), nil
	}
	limited := len(conversations) > 500
	if limited {
		conversations = conversations[:500]
	}
	return dict("conversations", conversations, "scan_limited", limited, "current", dict("state", "unavailable")), nil
}

// sortDictsDesc is sorted(rows, key=key, reverse=True), which is stable.
func sortDictsDesc(rows []*nPyDict, key func(*nPyDict) float64) {
	sort.SliceStable(rows, func(i, j int) bool { return key(rows[i]) > key(rows[j]) })
}
