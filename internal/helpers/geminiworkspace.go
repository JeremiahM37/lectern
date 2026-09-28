package helpers

// gemini-workspace is internal/agents' geminiWorkspaceScript
// (`lectern-gemini-workspace-mcp`): it merges Lectern's MCP servers into a
// workspace's .gemini/settings.json and takes them out again when the last
// session using the workspace ends. A ledger in the target user's state
// directory records what was added, so only values still exactly as Lectern
// wrote them are removed. It always exits 0; failures are {"error": ...}.
//
//	lectern helper gemini-workspace JSON

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
)

func init() { Register("gemini-workspace", geminiWorkspace) }

const (
	geminiMarker  = "# lectern-gemini-mcp"
	geminiPattern = "/.gemini/settings.json"
)

// geminiReply is the script's reply(): print compactly and stop.
type geminiReply struct{ obj *pyObj }

func (r *geminiReply) Error() string { return "reply" }

func geminiReplyOf(kv ...any) error { return &geminiReply{newObj(kv...)} }

func geminiWorkspace(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	if len(args) < 1 {
		return pyUncaught(stderr, "IndexError", fmt.Errorf("list index out of range"))
	}
	raw, err := pyLoads(args[0])
	if err != nil {
		return pyUncaught(stderr, "json.decoder.JSONDecodeError", err)
	}
	err = geminiRun(raw)
	reply, ok := err.(*geminiReply)
	if !ok {
		reply = &geminiReply{newObj("error", err.Error())}
	}
	fmt.Fprintln(stdout, pyDumpsCompact(reply.obj))
	return 0
}

// pyIndex is a[key] on a JSON value, with the KeyError / TypeError Python
// raises.
func pyIndex(v any, key string) (any, error) {
	o, ok := v.(*pyObj)
	if !ok {
		if s, isStr := v.(string); isStr {
			_ = s
			return nil, fmt.Errorf("string indices must be integers, not 'str'")
		}
		return nil, fmt.Errorf("'%s' object is not subscriptable", pyTypeName(v))
	}
	val, ok := o.Get(key)
	if !ok {
		return nil, &pyKeyError{key}
	}
	return val, nil
}

func geminiGit(wd string, args ...string) (pyResult, string, error) {
	res, err := pyRun{Argv: append([]string{"git", "-c", "safe.directory=" + wd, "-C", wd}, args...)}.run()
	if err != nil {
		return res, "", err
	}
	out, err := res.text(res.Stdout)
	if err != nil {
		return res, "", err
	}
	_, err = res.text(res.Stderr)
	return res, out, err
}

func geminiScratchRoot() string {
	if root := os.Getenv("LECTERN_SCRATCH_ROOT"); root != "" {
		return root
	}
	if root := os.Getenv("AGENTDECK_SCRATCH_ROOT"); root != "" {
		return root
	}
	home := wPyExpanduser("~")
	if pyIsDir(wPyJoin(home, "lectern-scratch")) || !pyIsDir(wPyJoin(home, "agentdeck-scratch")) {
		return wPyJoin(home, "lectern-scratch")
	}
	return wPyJoin(home, "agentdeck-scratch")
}

func geminiUnder(path, root string) bool {
	root = pyRealpath(root)
	return path != root && pyInside(path, root)
}

func geminiStateDir() (string, error) {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		base = wPyJoin(wPyExpanduser("~"), ".local", "state")
	}
	d := wPyJoin(base, "lectern", "gemini-mcp")
	return d, wPyMakedirs(d, 0o700, true)
}

// pyWriteJSON is the script's write_json: json.dump(indent=2) and a newline
// into a temporary file beside path, then chmod and an atomic replace.
func pyWriteJSON(path string, obj any, mode os.FileMode) error {
	f, tmp, err := pyMkstemp(wPyDirname(path), ".lectern-")
	if err != nil {
		return err
	}
	_, werr := io.WriteString(f, pyDumpsIndent(obj, 2)+"\n")
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return wPyErr(werr)
	}
	if err := os.Chmod(tmp, mode); err != nil {
		return wPyErr(err, tmp)
	}
	return pyReplace(tmp, path)
}

// pyReadText is open(path, encoding='utf8').read(): strict UTF-8, universal
// newlines.
func pyReadText(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", wPyErr(err, path)
	}
	s, err := pyDecodeStrict(data)
	if err != nil {
		return "", err
	}
	return wPyUniversalNewlines(s), nil
}

func geminiExcludePath(wd string) (string, error) {
	_, out, err := geminiGit(wd, "rev-parse", "--git-path", "info/exclude")
	if err != nil {
		return "", err
	}
	p := wPyStrip(out)
	if p == "" || pyIsAbs(p) {
		return p, nil
	}
	return wPyJoin(wd, p), nil
}

func geminiEditExclude(wd string, remove bool) (bool, error) {
	p, err := geminiExcludePath(wd)
	if err != nil || p == "" {
		return false, err
	}
	if err := wPyMakedirs(wPyDirname(p), 0o777, true); err != nil {
		return false, err
	}
	old, err := pyReadText(p)
	if err != nil {
		if _, isOS := pyErrno(err); !isOS {
			return false, err
		}
		old = ""
	}
	// Linked worktrees share the repository's info/exclude, so each workspace
	// gets its own marker and removes only its own pair.
	sum := sha256.Sum256([]byte(wd))
	pair := geminiMarker + ":" + hex.EncodeToString(sum[:])[:12] + "\n" + geminiPattern + "\n"
	var updated string
	switch {
	case remove:
		updated = strings.ReplaceAll(old, pair, "")
	case strings.Contains(old, pair):
		return true, nil
	default:
		sep := ""
		if old != "" && !strings.HasSuffix(old, "\n") {
			sep = "\n"
		}
		updated = old + sep + pair
	}
	if updated != old {
		f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o666)
		if err != nil {
			return false, wPyErr(err, p)
		}
		_, werr := io.WriteString(f, updated)
		if cerr := f.Close(); werr == nil {
			werr = cerr
		}
		if werr != nil {
			return false, wPyErr(werr)
		}
	}
	return true, nil
}

// geminiWorkdir holds what the script keeps in its closure.
type geminiWorkdir struct {
	wd, gdir, path, ledgerPath string
}

func (g *geminiWorkdir) fileMode() os.FileMode {
	info, err := os.Stat(g.path)
	if err != nil {
		return 0o600
	}
	return info.Mode().Perm()
}

// loadSettings is nil when the file does not exist.
func (g *geminiWorkdir) loadSettings() (*pyObj, error) {
	if !pyExists(g.path) {
		return nil, nil
	}
	text, err := pyReadText(g.path)
	if err != nil {
		if _, isOS := pyErrno(err); isOS {
			return nil, err
		}
		return nil, geminiReplyOf("error", ".gemini/settings.json is not plain JSON (comments?); not editing it")
	}
	data, err := pyLoads(text)
	if err != nil {
		return nil, geminiReplyOf("error", ".gemini/settings.json is not plain JSON (comments?); not editing it")
	}
	obj, ok := data.(*pyObj)
	if !ok {
		return nil, geminiReplyOf("error", ".gemini/settings.json is not a JSON object; not editing it")
	}
	return obj, nil
}

func (g *geminiWorkdir) strip(settings *pyObj, entries any) ([]any, error) {
	removed := []any{}
	servers, ok := settings.Val("mcpServers").(*pyObj)
	if !ok {
		return removed, nil
	}
	list, ok := entries.(*pyObj)
	if !ok {
		return nil, fmt.Errorf("'%s' object has no attribute 'items'", pyTypeName(entries))
	}
	for _, name := range list.Keys() {
		if cur, ok := servers.Get(name); ok && wPyEqual(cur, list.Val(name)) {
			servers.Delete(name)
			removed = append(removed, name)
		}
	}
	if servers.Len() == 0 {
		settings.Delete("mcpServers")
	}
	return removed, nil
}

func (g *geminiWorkdir) finishRemove(ledger, settings *pyObj) ([]any, error) {
	removed := []any{}
	if settings != nil {
		entries, ok := ledger.Get("entries")
		if !ok {
			entries = newObj()
		}
		var err error
		if removed, err = g.strip(settings, entries); err != nil {
			return nil, err
		}
		if wPyTruthy(ledger.Val("created_file")) && settings.Len() == 0 {
			if err := os.Remove(g.path); err != nil {
				return nil, wPyErr(err, g.path)
			}
			if wPyTruthy(ledger.Val("created_dir")) {
				_ = removeDir(g.gdir)
			}
		} else if err := pyWriteJSON(g.path, settings, g.fileMode()); err != nil {
			return nil, err
		}
	}
	if wPyTruthy(ledger.Val("excluded")) {
		if _, err := geminiEditExclude(g.wd, true); err != nil {
			return nil, err
		}
	}
	if err := os.Remove(g.ledgerPath); err != nil {
		return nil, wPyErr(err, g.ledgerPath)
	}
	return removed, nil
}

// removeDir is os.rmdir: never a file.
// pyIter is list(v): a list, a string's characters or a dict's keys.
func pyIter(v any) ([]any, error) {
	switch x := v.(type) {
	case []any:
		return x, nil
	case string:
		var out []any
		for _, r := range x {
			out = append(out, string(r))
		}
		return out, nil
	case *pyObj:
		var out []any
		for _, k := range x.Keys() {
			out = append(out, k)
		}
		return out, nil
	}
	return nil, fmt.Errorf("'%s' object is not iterable", pyTypeName(v))
}

func removeDir(p string) error {
	if info, err := os.Lstat(p); err != nil || !info.IsDir() {
		return fmt.Errorf("not a directory")
	}
	return os.Remove(p)
}

func geminiRun(raw any) error {
	workdir, err := pyIndex(raw, "workdir")
	if err != nil {
		return err
	}
	wdText, ok := workdir.(string)
	if !ok {
		return fmt.Errorf("expected str, bytes or os.PathLike object, not %s", pyTypeName(workdir))
	}
	a := raw.(*pyObj)
	g := &geminiWorkdir{wd: pyRealpath(wdText)}
	if !pyIsDir(g.wd) {
		return geminiReplyOf("error", "workspace does not exist")
	}
	state, err := geminiStateDir()
	if err != nil {
		return err
	}
	sum := sha256.Sum256([]byte(g.wd))
	g.ledgerPath = wPyJoin(state, hex.EncodeToString(sum[:])[:24]+".json")
	var ledgerRaw any
	if text, err := pyReadText(g.ledgerPath); err == nil {
		ledgerRaw, _ = pyLoads(text)
	}
	// Any JSON value loads; only a dict is usable, and anything else fails
	// where Python would first call .get on it.
	ledger, _ := ledgerRaw.(*pyObj)
	notDict := func() error { return fmt.Errorf("'%s' object has no attribute 'get'", pyTypeName(ledgerRaw)) }
	g.gdir = wPyJoin(g.wd, ".gemini")
	g.path = wPyJoin(g.gdir, "settings.json")
	for _, p := range []string{g.gdir, g.path} {
		if pyIsLink(p) {
			return geminiReplyOf("error", p+" is a symlink; not editing it")
		}
	}
	op, err := pyIndex(a, "op")
	if err != nil {
		return err
	}
	if op == "remove" {
		if ledgerRaw == nil {
			return geminiReplyOf("removed", []any{})
		}
		if ledger == nil {
			return notDict()
		}
		list := []any{}
		if v, ok := ledger.Get("sessions"); ok {
			if list, err = pyIter(v); err != nil {
				return err
			}
		}
		sessions := []any{}
		for _, s := range list {
			session, err := pyIndex(a, "session")
			if err != nil {
				return err
			}
			if !wPyEqual(s, session) {
				sessions = append(sessions, s)
			}
		}
		if len(sessions) > 0 {
			ledger.Set("sessions", sessions)
			if err := pyWriteJSON(g.ledgerPath, ledger, 0o600); err != nil {
				return err
			}
			return geminiReplyOf("path", g.path, "removed", []any{})
		}
		settings, err := g.loadSettings()
		if err != nil {
			return err
		}
		removed, err := g.finishRemove(ledger, settings)
		if err != nil {
			return err
		}
		return geminiReplyOf("path", g.path, "removed", removed)
	}
	// install
	if !wPyTruthy(a.Val("owned")) && !geminiUnder(g.wd, geminiScratchRoot()) {
		return geminiReplyOf("skipped", "not-owned")
	}
	res, _, err := geminiGit(g.wd, "ls-files", "--error-unmatch", "--", ".gemini/settings.json")
	if err != nil {
		return err
	}
	if res.RC == 0 {
		return geminiReplyOf("skipped", "tracked")
	}
	if ledgerRaw != nil && ledger == nil {
		return notDict()
	}
	if ledger != nil && !wPyTruthy(ledger.Val("sessions")) {
		settings, err := g.loadSettings()
		if err != nil {
			return err
		}
		if _, err := g.finishRemove(ledger, settings); err != nil {
			return err
		}
		ledger = nil
	}
	settings, err := g.loadSettings()
	if err != nil {
		return err
	}
	if ledger == nil {
		ledger = newObj("workdir", g.wd, "sessions", []any{}, "entries", newObj(),
			"created_file", settings == nil, "created_dir", !pyIsDir(g.gdir), "excluded", false)
	}
	if settings == nil {
		settings = newObj()
	}
	if !settings.Has("mcpServers") {
		settings.Set("mcpServers", newObj())
	}
	servers, ok := settings.Val("mcpServers").(*pyObj)
	if !ok {
		return geminiReplyOf("error", "mcpServers in .gemini/settings.json is not an object; not editing it")
	}
	wanted, err := pyIndex(a, "servers")
	if err != nil {
		return err
	}
	declared, ok := wanted.(*pyObj)
	if !ok {
		return fmt.Errorf("'%s' object has no attribute 'items'", pyTypeName(wanted))
	}
	// ledger['entries'] is looked up where the script used it.
	ledgerEntries := func() (*pyObj, error) {
		v, err := pyIndex(ledger, "entries")
		if err != nil {
			return nil, err
		}
		o, ok := v.(*pyObj)
		if !ok {
			return nil, fmt.Errorf("'%s' object has no attribute 'get'", pyTypeName(v))
		}
		return o, nil
	}
	added, kept := []any{}, []any{}
	names := declared.Keys()
	sort.Strings(names)
	for _, name := range names {
		entry := declared.Val(name)
		entries, err := ledgerEntries()
		if _, exists := servers.Get(name); exists && err != nil {
			return err
		}
		if cur, ok := servers.Get(name); ok && !wPyEqual(cur, entries.Val(name)) {
			kept = append(kept, name)
			continue
		}
		servers.Set(name, entry)
		if err != nil {
			return err
		}
		entries.Set(name, entry)
		added = append(added, name)
	}
	if err := wPyMakedirs(g.gdir, 0o700, true); err != nil {
		return err
	}
	if err := pyWriteJSON(g.path, settings, g.fileMode()); err != nil {
		return err
	}
	createdFile, err := pyIndex(ledger, "created_file")
	if err != nil {
		return err
	}
	if wPyTruthy(createdFile) {
		excluded, err := pyIndex(ledger, "excluded")
		if err != nil {
			return err
		}
		if !wPyTruthy(excluded) {
			done, err := geminiEditExclude(g.wd, false)
			if err != nil {
				return err
			}
			ledger.Set("excluded", done)
		}
	}
	session, err := pyIndex(a, "session")
	if err != nil {
		return err
	}
	sessionsVal, err := pyIndex(ledger, "sessions")
	if err != nil {
		return err
	}
	sessions, ok := sessionsVal.([]any)
	if !ok {
		return fmt.Errorf("argument of type '%s' is not iterable", pyTypeName(sessionsVal))
	}
	present := false
	for _, s := range sessions {
		present = present || wPyEqual(s, session)
	}
	if !present {
		ledger.Set("sessions", append(sessions, session))
	}
	if err := pyWriteJSON(g.ledgerPath, ledger, 0o600); err != nil {
		return err
	}
	return geminiReplyOf("path", g.path, "added", added, "kept", kept)
}
