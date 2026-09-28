package helpers

// Python's path and file primitives, as the replaced scripts used them:
// os.path.realpath (which, unlike filepath.EvalSymlinks, resolves what exists
// and keeps the rest), expanduser/expandvars, glob.glob and pathlib's glob
// (different hidden-file and symlink rules, both in directory order),
// buffered readline with a size limit, and OSError's message format.

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/user"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"unicode/utf8"
)

// pyErr is a Python exception a port reports the way the script did.
type pyErr struct{ cls, msg string }

func (e *pyErr) Error() string { return e.msg }

func pyError(cls, msg string) error { return &pyErr{cls, msg} }

// errCrash stands for an exception the script did not catch: it printed a
// traceback and nothing on stdout, and exited 1.
var errCrash = errors.New("uncaught exception")

// pyClass is the Python exception class name for err.
func pyClass(err error) string {
	var pe *pyErr
	if errors.As(err, &pe) {
		return pe.cls
	}
	var errno syscall.Errno
	if errors.As(err, &errno) {
		switch errno {
		case syscall.ENOENT:
			return "FileNotFoundError"
		case syscall.EACCES, syscall.EPERM:
			return "PermissionError"
		case syscall.EEXIST:
			return "FileExistsError"
		case syscall.EISDIR:
			return "IsADirectoryError"
		case syscall.ENOTDIR:
			return "NotADirectoryError"
		case syscall.EINTR:
			return "InterruptedError"
		case syscall.ESRCH:
			return "ProcessLookupError"
		}
	}
	return "OSError"
}

// osError renders a Go filesystem error as Python's OSError text,
// "[Errno 2] No such file or directory: '/path'".
func osError(err error, filename string) error {
	var pe *pyErr
	if err == nil || errors.As(err, &pe) {
		return err
	}
	var errno syscall.Errno
	if !errors.As(err, &errno) {
		return pyError("OSError", err.Error())
	}
	text := errno.Error()
	if text != "" {
		text = strings.ToUpper(text[:1]) + text[1:]
	}
	msg := fmt.Sprintf("[Errno %d] %s", int(errno), text)
	if filename != "" {
		msg += ": " + strRepr(filename)
	}
	return &pyErr{pyClass(err), msg}
}

// realpath is os.path.realpath (Python 3.13). Non-strict, it resolves every
// symlink that exists and keeps missing components as written; strict, any
// missing component or symlink loop is an error.
func realpath(filename string, strict bool) (string, error) {
	parts := strings.Split(filename, "/")
	rest := make([]*string, 0, len(parts))
	for i := len(parts) - 1; i >= 0; i-- {
		p := parts[i]
		rest = append(rest, &p)
	}
	partCount := len(parts)
	path := "/"
	if !strings.HasPrefix(filename, "/") {
		wd, err := os.Getwd()
		if err != nil {
			return "", osError(err, "")
		}
		path = wd
	}
	seen := map[string]*string{}
	for partCount > 0 {
		name := rest[len(rest)-1]
		rest = rest[:len(rest)-1]
		if name == nil {
			link := *rest[len(rest)-1]
			rest = rest[:len(rest)-1]
			resolved := path
			seen[link] = &resolved
			continue
		}
		partCount--
		if *name == "" || *name == "." {
			continue
		}
		if *name == ".." {
			path = path[:strings.LastIndex(path, "/")]
			if path == "" {
				path = "/"
			}
			continue
		}
		newpath := path + "/" + *name
		if path == "/" {
			newpath = path + *name
		}
		fi, err := os.Lstat(newpath)
		if err != nil {
			if strict {
				return "", osError(err, newpath)
			}
			path = newpath
			continue
		}
		if fi.Mode()&fs.ModeSymlink == 0 {
			if strict && partCount > 0 && !fi.IsDir() {
				return "", osError(syscall.ENOTDIR, newpath)
			}
			path = newpath
			continue
		}
		if cached, ok := seen[newpath]; ok {
			if cached != nil {
				path = *cached
				continue
			}
			if strict {
				if _, err := os.Stat(newpath); err != nil {
					return "", osError(err, newpath)
				}
			}
			path = newpath
			continue
		}
		target, err := os.Readlink(newpath)
		if err != nil {
			if strict {
				return "", osError(err, newpath)
			}
			path = newpath
			continue
		}
		if strings.HasPrefix(target, "/") {
			path = "/"
		}
		seen[newpath] = nil
		link := newpath
		rest = append(rest, &link, nil)
		targetParts := strings.Split(target, "/")
		for i := len(targetParts) - 1; i >= 0; i-- {
			p := targetParts[i]
			rest = append(rest, &p)
		}
		partCount += len(targetParts)
	}
	return path, nil
}

// realpathLoose is realpath(p) where Python would not have raised.
func realpathLoose(p string) string {
	out, err := realpath(p, false)
	if err != nil {
		return p
	}
	return out
}

// expanduser is os.path.expanduser.
func expanduser(path string) string {
	if !strings.HasPrefix(path, "~") {
		return path
	}
	i := strings.Index(path[1:], "/") + 1
	if i == 0 {
		i = len(path)
	}
	var home string
	if i == 1 {
		if h, ok := os.LookupEnv("HOME"); ok {
			home = h
		} else if u, err := user.Current(); err == nil {
			home = u.HomeDir
		} else {
			return path
		}
	} else {
		u, err := user.Lookup(path[1:i])
		if err != nil {
			return path
		}
		home = u.HomeDir
	}
	home = strings.TrimRight(home, "/")
	if out := home + path[i:]; out != "" {
		return out
	}
	return "/"
}

var varPattern = regexp.MustCompile(`\$([a-zA-Z0-9_]+|\{[^}]*\}?)`)

// expandvars is os.path.expandvars: unknown variables stay as written.
func expandvars(path string) string {
	if !strings.Contains(path, "$") {
		return path
	}
	return varPattern.ReplaceAllStringFunc(path, func(m string) string {
		name := m[1:]
		if strings.HasPrefix(name, "{") {
			if !strings.HasSuffix(name, "}") || len(name) < 2 {
				return m
			}
			name = name[1 : len(name)-1]
		}
		if v, ok := os.LookupEnv(name); ok {
			return v
		}
		return m
	})
}

// pyJoin is os.path.join.
func pyJoin(a string, more ...string) string {
	path := a
	for _, b := range more {
		switch {
		case strings.HasPrefix(b, "/"):
			path = b
		case path == "" || strings.HasSuffix(path, "/"):
			path += b
		default:
			path += "/" + b
		}
	}
	return path
}

// pathStr is str(pathlib.Path(*parts)): joined, with empty and "." parts
// dropped (".." is kept).
func pathStr(parts ...string) string {
	root := ""
	var names []string
	for _, p := range parts {
		if strings.HasPrefix(p, "/") {
			root, names = "/", nil
			if strings.HasPrefix(p, "//") && !strings.HasPrefix(p, "///") {
				root = "//"
			}
		}
		for _, n := range strings.Split(p, "/") {
			if n != "" && n != "." {
				names = append(names, n)
			}
		}
	}
	if root == "" && len(names) == 0 {
		return "."
	}
	return root + strings.Join(names, "/")
}

// basename is os.path.basename.
func basename(p string) string { return p[strings.LastIndex(p, "/")+1:] }

// splitext is os.path.splitext; leading dots never start an extension.
func splitext(p string) (string, string) {
	sep := strings.LastIndex(p, "/")
	dot := strings.LastIndex(p, ".")
	if dot > sep {
		for i := sep + 1; i < dot; i++ {
			if p[i] != '.' {
				return p[:dot], p[dot:]
			}
		}
	}
	return p, ""
}

// suffix is pathlib's PurePath.suffix.
func suffix(p string) string {
	name := basename(p)
	i := strings.LastIndex(name, ".")
	if 0 < i && i < len(name)-1 {
		return name[i:]
	}
	return ""
}

// isUnder is `base in path.parents` for resolved paths.
func isUnder(base, path string) bool {
	if base == path {
		return false
	}
	if base == "/" {
		return strings.HasPrefix(path, "/")
	}
	return strings.HasPrefix(path, base+"/")
}

// commonpathIs is os.path.commonpath([base, p]) == base for absolute,
// normalized paths.
func commonpathIs(base, p string) bool {
	return p == base || isUnder(base, p)
}

// isFile and isDir are os.path.isfile/isdir (symlinks followed, errors false).
func isFile(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular()
}

func isDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

func lexists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

// scandirEntry is one os.scandir entry, in directory order.
type scandirEntry struct {
	name, path string
	symlink    bool
	dir        bool // DirEntry.is_dir(follow_symlinks=False)
}

// scandir lists dir in directory order (os.ReadDir would sort by name).
// entry.path is joined the way os.scandir joins it.
func scandir(dir string) ([]scandirEntry, error) {
	arg := dir
	if arg == "" {
		arg = "."
	}
	f, err := os.Open(arg)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	list, err := f.ReadDir(-1)
	if err != nil && len(list) == 0 {
		return nil, err
	}
	out := make([]scandirEntry, 0, len(list))
	for _, e := range list {
		p := e.Name()
		if dir != "" {
			p = pyJoin(dir, e.Name())
		}
		out = append(out, scandirEntry{name: e.Name(), path: p, symlink: e.Type()&fs.ModeSymlink != 0, dir: e.IsDir()})
	}
	return out, nil
}

// isDirFollow is DirEntry.is_dir(): true for a directory or a symlink to one.
func (e scandirEntry) isDirFollow() bool {
	if !e.symlink {
		return e.dir
	}
	return isDir(e.path)
}

// globMagic is glob.has_magic.
func globMagic(s string) bool { return strings.ContainsAny(s, "*?[") }

func pySplitPath(p string) (string, string) {
	i := strings.LastIndex(p, "/") + 1
	head, tail := p[:i], p[i:]
	if head != "" && head != strings.Repeat("/", len(head)) {
		head = strings.TrimRight(head, "/")
	}
	return head, tail
}

// pyGlob is glob.glob(pattern, recursive=recursive): hidden names are
// matched only by a pattern that itself starts with a dot, intermediate
// wildcards follow directory symlinks, results come in directory order.
func pyGlob(pattern string, recursive bool) []string {
	out := globIter(pattern, recursive, false)
	if pattern == "" || (recursive && strings.HasPrefix(pattern, "**")) {
		if len(out) > 0 && out[0] == "" {
			out = out[1:]
		}
	}
	return out
}

func globIter(pathname string, recursive, dironly bool) []string {
	dirname, base := pySplitPath(pathname)
	if !globMagic(pathname) {
		if base != "" {
			if lexists(pathname) {
				return []string{pathname}
			}
		} else if isDir(dirname) {
			return []string{pathname}
		}
		return nil
	}
	if dirname == "" {
		if recursive && base == "**" {
			return glob2("", dironly)
		}
		return glob1("", base, dironly)
	}
	dirs := []string{dirname}
	if dirname != pathname && globMagic(dirname) {
		dirs = globIter(dirname, recursive, true)
	}
	var out []string
	for _, d := range dirs {
		var names []string
		switch {
		case globMagic(base) && recursive && base == "**":
			names = glob2(d, dironly)
		case globMagic(base):
			names = glob1(d, base, dironly)
		case base != "":
			if lexists(pyJoin(d, base)) {
				names = []string{base}
			}
		case isDir(d):
			names = []string{base}
		}
		for _, n := range names {
			out = append(out, pyJoin(d, n))
		}
	}
	return out
}

func globListdir(dir string, dironly bool) []string {
	entries, err := scandir(dir)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if !dironly || e.isDirFollow() {
			names = append(names, e.name)
		}
	}
	return names
}

func glob1(dir, pattern string, dironly bool) []string {
	match := fnmatchCompile(pattern)
	var out []string
	for _, n := range globListdir(dir, dironly) {
		if !strings.HasPrefix(pattern, ".") && strings.HasPrefix(n, ".") {
			continue
		}
		if match != nil && match.MatchString(n) {
			out = append(out, n)
		}
	}
	return out
}

func glob2(dir string, dironly bool) []string {
	var out []string
	if dir == "" || isDir(dir) {
		out = append(out, "")
	}
	return append(out, rlistdir(dir, dironly)...)
}

func rlistdir(dir string, dironly bool) []string {
	var out []string
	for _, x := range globListdir(dir, dironly) {
		if strings.HasPrefix(x, ".") {
			continue
		}
		out = append(out, x)
		p := x
		if dir != "" {
			p = pyJoin(dir, x)
		}
		for _, y := range rlistdir(p, dironly) {
			out = append(out, pyJoin(x, y))
		}
	}
	return out
}

// fnmatchCompile is fnmatch.translate (Python 3.13) compiled for RE2. The
// atomic groups Python uses only prune backtracking, which RE2 never does,
// so plain groups match the same names. nil means the pattern matches
// nothing.
func fnmatchCompile(pat string) *regexp.Regexp {
	const star = "\x00STAR"
	var res []string
	p := []rune(pat)
	i, n := 0, len(p)
	for i < n {
		c := p[i]
		i++
		switch c {
		case '*':
			if len(res) == 0 || res[len(res)-1] != star {
				res = append(res, star)
			}
		case '?':
			res = append(res, ".")
		case '[':
			j := i
			if j < n && p[j] == '!' {
				j++
			}
			if j < n && p[j] == ']' {
				j++
			}
			for j < n && p[j] != ']' {
				j++
			}
			if j >= n {
				res = append(res, `\[`)
				continue
			}
			stuff := string(p[i:j])
			if !strings.Contains(stuff, "-") {
				stuff = strings.ReplaceAll(stuff, `\`, `\\`)
			} else {
				var chunks []string
				k := i + 1
				if p[i] == '!' {
					k = i + 2
				}
				for {
					k = runeFind(p, '-', k, j)
					if k < 0 {
						break
					}
					chunks = append(chunks, string(p[i:k]))
					i = k + 1
					k = k + 3
				}
				if chunk := string(p[i:j]); chunk != "" {
					chunks = append(chunks, chunk)
				} else {
					chunks[len(chunks)-1] += "-"
				}
				for k := len(chunks) - 1; k > 0; k-- {
					prev, cur := []rune(chunks[k-1]), []rune(chunks[k])
					if len(prev) > 0 && len(cur) > 0 && prev[len(prev)-1] > cur[0] {
						chunks[k-1] = string(prev[:len(prev)-1]) + string(cur[1:])
						chunks = append(chunks[:k], chunks[k+1:]...)
					}
				}
				for k, s := range chunks {
					chunks[k] = strings.ReplaceAll(strings.ReplaceAll(s, `\`, `\\`), "-", `\-`)
				}
				stuff = strings.Join(chunks, "-")
			}
			stuff = regexp.MustCompile(`([&~|])`).ReplaceAllString(stuff, `\$1`)
			i = j + 1
			switch {
			case stuff == "":
				return nil
			case stuff == "!":
				res = append(res, ".")
			default:
				if stuff[0] == '!' {
					stuff = "^" + stuff[1:]
				} else if stuff[0] == '^' || stuff[0] == '[' {
					stuff = `\` + stuff
				}
				res = append(res, "["+stuff+"]")
			}
		default:
			res = append(res, regexp.QuoteMeta(string(c)))
		}
	}
	var b strings.Builder
	for _, r := range res {
		if r == star {
			b.WriteString(".*")
		} else {
			b.WriteString(r)
		}
	}
	re, err := regexp.Compile(`(?s)\A(?:` + b.String() + `)\z`)
	if err != nil {
		return nil
	}
	return re
}

func runeFind(p []rune, c rune, from, to int) int {
	for k := from; k < to && k < len(p); k++ {
		if k >= 0 && p[k] == c {
			return k
		}
	}
	return -1
}

// pathlibWildcard is pathlib's '*'-style selector on one directory: every
// entry, hidden ones included, whose name match accepts. dirOnly keeps
// entries that are (or link to) directories.
func pathlibWildcard(dir string, match func(string) bool, dirOnly bool) []scandirEntry {
	entries, err := scandir(addSlash(dir))
	if err != nil {
		return nil
	}
	var out []scandirEntry
	for _, e := range entries {
		if match != nil && !match(e.name) {
			continue
		}
		if dirOnly && !e.isDirFollow() {
			continue
		}
		out = append(out, e)
	}
	return out
}

func addSlash(p string) string {
	if p == "" || strings.HasSuffix(p, "/") {
		return p
	}
	return p + "/"
}

func jsonlName(name string) bool { return strings.HasSuffix(name, ".jsonl") }

// pathlibGlobJSONL is Path(base).glob('*/*.jsonl') or, recursive,
// Path(base).glob('**/*.jsonl') (Python 3.13: '**' does not descend
// through directory symlinks), in pathlib's own visiting order.
func pathlibGlobJSONL(base string, recursive bool) []string {
	var out []string
	add := func(dir string) {
		for _, e := range pathlibWildcard(dir, jsonlName, false) {
			out = append(out, e.path)
		}
	}
	if !recursive {
		for _, d := range pathlibWildcard(base, nil, true) {
			add(d.path)
		}
		return out
	}
	root := addSlash(base)
	add(root)
	stack := []string{root}
	for len(stack) > 0 {
		dir := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		entries, err := scandir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.dir && !e.symlink {
				add(e.path)
				stack = append(stack, e.path)
			}
		}
	}
	return out
}

// statMtime is os.stat_result.st_mtime: float seconds computed exactly as
// CPython does (sec + 1e-9*nsec, rounded at each step).
func statMtime(fi fs.FileInfo) float64 {
	t := fi.ModTime()
	frac := float64(1e-9 * float64(t.Nanosecond()))
	return float64(t.Unix()) + frac
}

// statMtimeNs is st_mtime_ns.
func statMtimeNs(fi fs.FileInfo) int64 { return fi.ModTime().UnixNano() }

// getmtime is os.path.getmtime.
func getmtime(p string) (float64, error) {
	fi, err := os.Stat(p)
	if err != nil {
		return 0, osError(err, p)
	}
	return statMtime(fi), nil
}

// sortByDesc is list.sort(key=key, reverse=True), which is stable.
func sortByDesc(paths []string, key func(string) float64) {
	keys := make(map[string]float64, len(paths))
	for _, p := range paths {
		keys[p] = key(p)
	}
	// Python's reverse sort keeps equal keys in their original order.
	sort.SliceStable(paths, func(i, j int) bool { return keys[paths[i]] > keys[paths[j]] })
}

// pyFile is a Python binary file object's tell/seek/readline/read, done
// with ReadAt so every read starts exactly where Python's would.
type pyFile struct {
	f    *os.File
	name string
	pos  int64
}

func openPy(name string) (*pyFile, error) {
	f, err := os.Open(name)
	if err != nil {
		return nil, osError(err, name)
	}
	return &pyFile{f: f, name: name}, nil
}

func (p *pyFile) Close() error { return p.f.Close() }

// readline is f.readline(limit); limit < 0 reads the whole line.
func (p *pyFile) readline(limit int64) ([]byte, error) {
	var out []byte
	buf := make([]byte, 64*1024)
	for limit < 0 || int64(len(out)) < limit {
		want := int64(len(buf))
		if limit >= 0 && limit-int64(len(out)) < want {
			want = limit - int64(len(out))
		}
		n, err := p.f.ReadAt(buf[:want], p.pos)
		chunk := buf[:n]
		if i := strings.IndexByte(string(chunk), '\n'); i >= 0 {
			chunk = chunk[:i+1]
			out = append(out, chunk...)
			p.pos += int64(len(chunk))
			return out, nil
		}
		out = append(out, chunk...)
		p.pos += int64(n)
		if err == io.EOF || n == 0 {
			return out, nil
		}
		if err != nil {
			return out, osError(err, "")
		}
	}
	return out, nil
}

// read is f.read(n).
func (p *pyFile) read(n int64) ([]byte, error) {
	if n <= 0 {
		return nil, nil
	}
	buf := make([]byte, n)
	got, err := p.f.ReadAt(buf, p.pos)
	p.pos += int64(got)
	if err != nil && err != io.EOF {
		return buf[:got], osError(err, "")
	}
	return buf[:got], nil
}

// pySplitlines is str.splitlines().
func pySplitlines(s string) []string {
	var out []string
	start := 0
	rs := []rune(s)
	for i := 0; i < len(rs); i++ {
		switch rs[i] {
		case '\n', '\v', '\f', '\x1c', '\x1d', '\x1e', 0x85, 0x2028, 0x2029:
			out = append(out, string(rs[start:i]))
			start = i + 1
		case '\r':
			out = append(out, string(rs[start:i]))
			if i+1 < len(rs) && rs[i+1] == '\n' {
				i++
			}
			start = i + 1
		}
	}
	if start < len(rs) {
		out = append(out, string(rs[start:]))
	}
	return out
}

// decodeReplace is bytes.decode('utf-8', 'replace'): each maximal invalid
// subsequence becomes one U+FFFD, as Python (and the Unicode standard's
// recommended practice) does it.
func decodeReplace(b []byte) string {
	if utf8.Valid(b) {
		return string(b)
	}
	var out strings.Builder
	for i := 0; i < len(b); {
		r, size := utf8.DecodeRune(b[i:])
		if r != utf8.RuneError || size > 1 {
			out.WriteRune(r)
			i += size
			continue
		}
		out.WriteRune(utf8.RuneError)
		i += invalidPrefix(b[i:])
	}
	return out.String()
}

// invalidPrefix is how many bytes one replacement character covers: the
// lead byte plus any continuation bytes that were valid so far.
func invalidPrefix(b []byte) int {
	c := b[0]
	var need int
	lo, hi := byte(0x80), byte(0xbf)
	switch {
	case c >= 0xc2 && c <= 0xdf:
		need = 1
	case c == 0xe0:
		need, lo = 2, 0xa0
	case c >= 0xe1 && c <= 0xec, c == 0xee, c == 0xef:
		need = 2
	case c == 0xed:
		need, hi = 2, 0x9f
	case c == 0xf0:
		need, lo = 3, 0x90
	case c >= 0xf1 && c <= 0xf3:
		need = 3
	case c == 0xf4:
		need, hi = 3, 0x8f
	default:
		return 1
	}
	i := 1
	for ; i <= need && i < len(b); i++ {
		if b[i] < lo || b[i] > hi {
			break
		}
		lo, hi = 0x80, 0xbf
	}
	return i
}
