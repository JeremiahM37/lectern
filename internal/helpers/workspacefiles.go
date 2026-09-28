package helpers

// workspace-files is internal/api's workspace_files.py: every file operation
// the browser's file views make, run on the workspace's own target so path
// checks happen on the machine that owns the files (docs/files.md). Paths
// resolve beneath the workspace root or are refused; a descriptor is
// re-checked after it is opened, so a symlink swapped in meanwhile cannot
// redirect a read; reads, writes and downloads are capped.
//
//	lectern helper workspace-files ROOT REL ACTION MODE [EXTRA…]
//
// It prints one JSON object. A refusal exits 1 with {"error": ...}; a write
// conflict exits 3 with {"conflict": {...}}.

import (
	"archive/zip"
	"bufio"
	"bytes"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"
)

func init() { Register("workspace-files", workspaceFiles) }

const (
	wfCap        = 25 * 1024 * 1024 // read, write and download limit
	wfArchiveCap = 50 * 1024 * 1024 // compressed folder download limit
)

var wfBook = []string{".lectern-lock", ".lectern-state.json", ".lectern-process.json", ".lectern-state.next",
	".agentdeck-lock", ".agentdeck-state.json", ".agentdeck-process.json", ".agentdeck-state.next"}

// wfRefused is the script's Refused (a ValueError): its text is the message.
type wfRefused struct{ msg string }

func (e *wfRefused) Error() string { return e.msg }

func wfRefuse(msg string) error { return &wfRefused{msg} }

// wfConflict is the script's Conflict: a write whose base no longer holds.
type wfConflict struct{ detail *pyObj }

func (e *wfConflict) Error() string { return "conflict" }

type wsFiles struct {
	root    string
	rel     string
	grouped bool
	extra   []string
}

func workspaceFiles(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	if len(args) < 3 {
		return pyUncaught(stderr, "IndexError", errors.New("list index out of range"))
	}
	w := &wsFiles{root: pyRealpath(args[0]), rel: args[1], grouped: len(args) > 3 && args[3] == "grouped"}
	if len(args) > 4 {
		w.extra = args[4:]
	}
	actions := map[string]func() (*pyObj, error){
		"list": w.list, "read": w.read, "stat": w.stat, "write": w.write, "mkdir": w.mkdir, "create": w.create,
		"rename": w.rename, "delete": w.delete, "archive": w.archive, "index": w.index, "gitstatus": w.gitStatus,
		"search": w.search, "watch": w.watch, "exists": w.exists, "ext_stat": w.extStat, "ext_read": w.extRead,
	}
	var out *pyObj
	var err error
	if fn, ok := actions[args[2]]; ok {
		out, err = fn()
	} else {
		err = wfRefuse("unknown file action")
	}
	var conflict *wfConflict
	var unc *pyUncaughtError
	switch {
	case err == nil:
		fmt.Fprintln(stdout, pyDumps(out))
		return 0
	case errors.As(err, &conflict):
		fmt.Fprintln(stdout, pyDumps(newObj("conflict", conflict.detail)))
		return 3
	case errors.As(err, &unc):
		return pyUncaught(stderr, unc.kind, unc.err)
	}
	fmt.Fprintln(stdout, pyDumps(newObj("error", pyStrerrorOf(err))))
	return 1
}

// arg is extra[i], or the IndexError the script would raise.
func (w *wsFiles) arg(i int) (string, error) {
	if i >= len(w.extra) {
		return "", &pyUncaughtError{"IndexError", errors.New("list index out of range")}
	}
	return w.extra[i], nil
}

func (w *wsFiles) inside(p string) bool { return pyInside(p, w.root) }

func (w *wsFiles) bookkeeping(p string) bool {
	if !w.grouped {
		return false
	}
	name := pyRelpath(p, w.root)
	for _, b := range wfBook {
		if name == b {
			return true
		}
	}
	return !strings.Contains(name, "/") && (strings.HasPrefix(name, ".lectern-write-") || strings.HasPrefix(name, ".agentdeck-write-"))
}

// resolved is the real path of r, which must stay beneath the workspace.
func (w *wsFiles) resolved(r string) (string, error) {
	p := pyRealpath(wPyJoin(w.root, r))
	if !w.inside(p) {
		return "", wfRefuse("path is outside this workspace")
	}
	if w.bookkeeping(p) {
		return "", wfRefuse("workspace bookkeeping is not a project file")
	}
	return p, nil
}

// entry is the directory entry r itself (a symlink is not followed): its
// parent must resolve inside the workspace, and it cannot be the root.
func (w *wsFiles) entry(r string) (string, error) {
	r = strings.Trim(r, "/")
	norm := pyNormpath(r)
	if r == "" || r == "." || norm == "." || norm == ".." || strings.HasPrefix(norm, "../") {
		return "", wfRefuse("choose a file or folder inside this workspace")
	}
	name := pyBasename(norm)
	if name == "" || name == "." || name == ".." {
		return "", wfRefuse("choose a file or folder inside this workspace")
	}
	dir := wPyDirname(norm)
	if dir == "" {
		dir = "."
	}
	parent, err := w.resolved(dir)
	if err != nil {
		return "", err
	}
	if !pyIsDir(parent) {
		return "", wfRefuse("the containing folder does not exist")
	}
	p := wPyJoin(parent, name)
	if w.bookkeeping(p) {
		return "", wfRefuse("workspace bookkeeping is not a project file")
	}
	return p, nil
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// pyMtimeMs is int(st.st_mtime * 1000), with st_mtime the double CPython
// makes of the seconds and nanoseconds.
func pyMtimeMs(info fs.FileInfo) int64 {
	t := info.ModTime()
	return int64((float64(t.Unix()) + float64(t.Nanosecond())*1e-9) * 1000)
}

// openRegular opens p for reading and re-checks the opened descriptor: a
// symlink swapped in after resolved() checked the name cannot redirect the
// read outside the workspace.
func (w *wsFiles) openRegular(p string) (*os.File, fs.FileInfo, error) {
	f, err := os.OpenFile(p, os.O_RDONLY|oNonblock, 0)
	if err != nil {
		return nil, nil, wPyErr(err, p)
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, nil, wPyErr(err)
	}
	if info.IsDir() {
		// os.fdopen refused a directory before anything else was checked.
		f.Close()
		return nil, nil, wPyErr(syscall.EISDIR)
	}
	if pyIsDir("/proc/self/fd") && !w.inside(pyRealpath(fmt.Sprintf("/proc/self/fd/%d", f.Fd()))) {
		f.Close()
		return nil, nil, wfRefuse("path left workspace")
	}
	if !info.Mode().IsRegular() {
		f.Close()
		return nil, nil, wfRefuse("choose a regular file")
	}
	return f, info, nil
}

// readUpTo is f.read(n).
func readUpTo(f *os.File, n int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(f, n))
	if err != nil {
		return nil, wPyErr(err)
	}
	return data, nil
}

func (w *wsFiles) readCapped(p string) ([]byte, fs.FileInfo, error) {
	f, info, err := w.openRegular(p)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	if info.Size() > wfCap {
		return nil, nil, wfRefuse("file exceeds 25 MiB limit")
	}
	data, err := readUpTo(f, wfCap+1)
	if err != nil {
		return nil, nil, err
	}
	if len(data) > wfCap {
		return nil, nil, wfRefuse("file exceeds 25 MiB limit")
	}
	return data, info, nil
}

func (w *wsFiles) list() (*pyObj, error) {
	p, err := w.resolved(w.rel)
	if err != nil {
		return nil, err
	}
	names, err := readdirUnsorted(p)
	if err != nil {
		return nil, wPyErr(err, p)
	}
	var entries []*pyObj
	for _, name := range names {
		if len(entries) >= 2000 {
			break
		}
		full := wPyJoin(p, name)
		dest := pyRealpath(full)
		if !w.inside(dest) || w.bookkeeping(dest) {
			continue
		}
		info, err := os.Stat(full)
		if err != nil || (!info.Mode().IsRegular() && !info.IsDir()) {
			continue
		}
		entries = append(entries, newObj("name", name, "path", pyRelpath(full, w.root), "directory", info.IsDir(),
			"size", info.Size(), "mtime", pyMtimeMs(info), "link", pyIsLink(full)))
	}
	sort.SliceStable(entries, func(i, j int) bool {
		di, dj := entries[i].Val("directory") == true, entries[j].Val("directory") == true
		if di != dj {
			return di
		}
		return strings.ToLower(entries[i].Str("name")) < strings.ToLower(entries[j].Str("name"))
	})
	return newObj("entries", objList(entries), "path", pyRelpath(p, w.root), "limit", 2000), nil
}

func (w *wsFiles) read() (*pyObj, error) {
	p, err := w.resolved(w.rel)
	if err != nil {
		return nil, err
	}
	data, info, err := w.readCapped(p)
	if err != nil {
		return nil, err
	}
	return newObj("data", base64.StdEncoding.EncodeToString(data), "sha256", sha256Hex(data), "size", len(data),
		"mtime", pyMtimeMs(info)), nil
}

func (w *wsFiles) stat() (*pyObj, error) {
	p, err := w.resolved(w.rel)
	if err != nil {
		return nil, err
	}
	if !pyExists(p) {
		return newObj("exists", false), nil
	}
	data, info, err := w.readCapped(p)
	if err != nil {
		return nil, err
	}
	return newObj("exists", true, "sha256", sha256Hex(data), "size", len(data), "mtime", pyMtimeMs(info)), nil
}

// exists says whether a workspace path names something, without reading it:
// terminal links check this before offering a bare file name.
func (w *wsFiles) exists() (*pyObj, error) {
	p, err := w.resolved(w.rel)
	if err != nil {
		return nil, err
	}
	if !pyExists(p) {
		return newObj("exists", false, "path", pyRelpath(p, w.root)), nil
	}
	return newObj("exists", true, "directory", pyIsDir(p), "path", pyRelpath(p, w.root)), nil
}

// outside is an absolute or ~/ path anywhere on the target, for a person to
// view read-only (the server allows only signed-in people to ask).
func (w *wsFiles) outside() (string, error) {
	p := w.rel
	if strings.HasPrefix(p, "~/") {
		p = wPyExpanduser(p)
	}
	if !pyIsAbs(p) {
		return "", wfRefuse("give an absolute or ~/ path")
	}
	return pyRealpath(p), nil
}

func (w *wsFiles) extStat() (*pyObj, error) {
	p, err := w.outside()
	if err != nil {
		return nil, err
	}
	if !pyExists(p) {
		return newObj("exists", false, "path", p), nil
	}
	info, err := os.Stat(p)
	if err != nil {
		return nil, wPyErr(err, p)
	}
	if !info.Mode().IsRegular() {
		return newObj("exists", true, "regular", false, "path", p), nil
	}
	return newObj("exists", true, "regular", true, "path", p, "size", info.Size(), "mtime", pyMtimeMs(info)), nil
}

func (w *wsFiles) extRead() (*pyObj, error) {
	p, err := w.outside()
	if err != nil {
		return nil, err
	}
	f, err := os.OpenFile(p, os.O_RDONLY|oNonblock, 0)
	if err != nil {
		return nil, wPyErr(err, p)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, wPyErr(err)
	}
	if !info.Mode().IsRegular() {
		return nil, wfRefuse("choose a regular file")
	}
	if info.Size() > wfCap {
		return nil, wfRefuse("file exceeds 25 MiB limit")
	}
	data, err := readUpTo(f, wfCap+1)
	if err != nil {
		return nil, err
	}
	if len(data) > wfCap {
		return nil, wfRefuse("file exceeds 25 MiB limit")
	}
	return newObj("data", base64.StdEncoding.EncodeToString(data), "sha256", sha256Hex(data), "size", len(data),
		"mtime", pyMtimeMs(info), "path", p), nil
}

// write saves the staged upload at extra[0] over rel when the file still has
// the hash extra[1] ("absent": must not exist, "any": whatever is there),
// atomically, keeping the file's mode. The staged copy is always removed.
func (w *wsFiles) write() (*pyObj, error) {
	temp, err := w.arg(0)
	if err != nil {
		return nil, err
	}
	expected, err := w.arg(1)
	if err != nil {
		return nil, err
	}
	defer os.Remove(temp)
	p, err := w.entry(w.rel)
	if err != nil {
		return nil, err
	}
	if pyIsLink(p) {
		if p, err = w.resolved(w.rel); err != nil {
			return nil, err
		}
	}
	var current any
	mode := uint32(0o644)
	if pyLexists(p) {
		data, info, err := w.readCapped(p)
		if err != nil {
			return nil, err
		}
		current = sha256Hex(data)
		mode = pyImode(info.Mode())
	}
	if expected == "absent" && current != nil {
		return nil, &wfConflict{newObj("sha256", current, "detail", "a file with this name already exists")}
	}
	if expected != "absent" && expected != "any" && !wPyEqual(expected, current) {
		return nil, &wfConflict{newObj("sha256", current, "detail", "the file changed on disk since you opened it")}
	}
	info, err := os.Stat(temp)
	if err != nil {
		return nil, wPyErr(err, temp)
	}
	if info.Size() > wfCap {
		return nil, wfRefuse("file exceeds 25 MiB limit")
	}
	tf, err := os.Open(temp)
	if err != nil {
		return nil, wPyErr(err, temp)
	}
	body, err := readUpTo(tf, wfCap+1)
	tf.Close()
	if err != nil {
		return nil, err
	}
	var random [6]byte
	rand.Read(random[:])
	staged := wPyJoin(wPyDirname(p), "."+pyBasename(p)+".lectern-save-"+hex.EncodeToString(random[:]))
	out, err := os.OpenFile(staged, os.O_WRONLY|os.O_CREATE|os.O_EXCL, pyFileMode(int64(mode)))
	if err != nil {
		return nil, wPyErr(err, staged)
	}
	werr := func() error {
		if _, err := out.Write(body); err != nil {
			return wPyErr(err)
		}
		if err := out.Sync(); err != nil {
			return wPyErr(err)
		}
		if err := out.Close(); err != nil {
			return wPyErr(err)
		}
		if err := os.Chmod(staged, pyFileMode(int64(mode))); err != nil {
			return wPyErr(err, staged)
		}
		return pyReplace(staged, p)
	}()
	if werr != nil {
		out.Close()
		os.Remove(staged)
		return nil, werr
	}
	st, err := os.Stat(p)
	if err != nil {
		return nil, wPyErr(err, p)
	}
	return newObj("sha256", sha256Hex(body), "size", len(body), "mtime", pyMtimeMs(st), "path", pyRelpath(p, w.root)), nil
}

func (w *wsFiles) mkdir() (*pyObj, error) {
	p, err := w.entry(w.rel)
	if err != nil {
		return nil, err
	}
	if err := os.Mkdir(p, 0o755); err != nil {
		return nil, wPyErr(err, p)
	}
	return newObj("path", pyRelpath(p, w.root)), nil
}

func (w *wsFiles) create() (*pyObj, error) {
	p, err := w.entry(w.rel)
	if err != nil {
		return nil, err
	}
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return nil, wPyErr(err, p)
	}
	f.Close()
	return newObj("path", pyRelpath(p, w.root), "sha256", sha256Hex(nil)), nil
}

func (w *wsFiles) rename() (*pyObj, error) {
	src, err := w.entry(w.rel)
	if err != nil {
		return nil, err
	}
	to, err := w.arg(0)
	if err != nil {
		return nil, err
	}
	dst, err := w.entry(to)
	if err != nil {
		return nil, err
	}
	if !pyLexists(src) {
		return nil, wfRefuse("that file no longer exists")
	}
	if pyLexists(dst) {
		return nil, wfRefuse("something with that name already exists")
	}
	if pyIsDir(src) && !pyIsLink(src) && pyInside(dst, src) {
		return nil, wfRefuse("a folder cannot move into itself")
	}
	if pyRelpath(src, w.root) == ".git" {
		return nil, wfRefuse("the repository folder cannot be moved")
	}
	if err := pyRename(src, dst); err != nil {
		return nil, err
	}
	return newObj("path", pyRelpath(dst, w.root)), nil
}

// pyRename is os.rename(src, dst).
func pyRename(src, dst string) error { return pyReplace(src, dst) }

func (w *wsFiles) delete() (*pyObj, error) {
	p, err := w.entry(w.rel)
	if err != nil {
		return nil, err
	}
	if !pyLexists(p) {
		return nil, wfRefuse("that file no longer exists")
	}
	if pyRelpath(p, w.root) == ".git" {
		return nil, wfRefuse("the repository folder cannot be deleted here")
	}
	if pyIsDir(p) && !pyIsLink(p) {
		err = pyRmtree(p)
	} else {
		err = wPyErr(os.Remove(p), p)
	}
	if err != nil {
		return nil, err
	}
	return newObj("path", w.rel), nil
}

// pyRmtree is shutil.rmtree: depth first, in directory order, stopping at
// the first failure.
func pyRmtree(p string) error {
	names, err := readdirUnsorted(p)
	if err != nil {
		return wPyErr(err, p)
	}
	for _, name := range names {
		full := wPyJoin(p, name)
		info, err := os.Lstat(full)
		if err == nil && info.IsDir() {
			if err := pyRmtree(full); err != nil {
				return err
			}
			continue
		}
		if err := os.Remove(full); err != nil {
			return wPyErr(err, full)
		}
	}
	if err := os.Remove(p); err != nil {
		return wPyErr(err, p)
	}
	return nil
}

// pyWalk is os.walk(top) top-down: prune may reorder or drop directories
// before they are entered, and returning false stops the walk. Directories
// that cannot be listed are skipped, as with onerror=None.
func pyWalk(top string, visit func(base string, dirs []string, names []string) (keep []string, more bool)) bool {
	entries, err := readdirUnsorted(top)
	if err != nil {
		return true
	}
	var dirs, names []string
	for _, name := range entries {
		// os.walk sorts with is_dir(), which follows symlinks.
		if pyIsDir(wPyJoin(top, name)) {
			dirs = append(dirs, name)
		} else {
			names = append(names, name)
		}
	}
	keep, more := visit(top, dirs, names)
	if !more {
		return false
	}
	for _, d := range keep {
		full := wPyJoin(top, d)
		if pyIsLink(full) {
			continue // followlinks=False
		}
		if !pyWalk(full, visit) {
			return false
		}
	}
	return true
}

func (w *wsFiles) archive() (*pyObj, error) {
	p, err := w.resolved(w.rel)
	if err != nil {
		return nil, err
	}
	if !pyIsDir(p) {
		return nil, wfRefuse("choose a folder")
	}
	var buf bytes.Buffer
	z := zip.NewWriter(&buf)
	total := int64(0)
	var failure error
	pyWalk(p, func(base string, dirs, names []string) ([]string, bool) {
		var keep []string
		for _, d := range dirs {
			full := wPyJoin(base, d)
			if w.inside(pyRealpath(full)) && !pyIsLink(full) {
				keep = append(keep, d)
			}
		}
		for _, name := range names {
			full := wPyJoin(base, name)
			real := pyRealpath(full)
			if !w.inside(real) || w.bookkeeping(real) {
				continue
			}
			info, err := os.Stat(real)
			if err != nil || !info.Mode().IsRegular() {
				continue
			}
			total += info.Size()
			if total > 4*wfArchiveCap {
				failure = wfRefuse("folder exceeds the 200 MiB download limit")
				return nil, false
			}
			if err := zipAdd(z, real, pyRelpath(full, p), info); err != nil {
				failure = err
				return nil, false
			}
			z.Flush()
			if buf.Len() > wfArchiveCap {
				failure = wfRefuse("folder exceeds the 50 MiB download limit")
				return nil, false
			}
		}
		return keep, true
	})
	if failure != nil {
		return nil, failure
	}
	if err := z.Close(); err != nil {
		return nil, err
	}
	return newObj("data", base64.StdEncoding.EncodeToString(buf.Bytes()), "size", buf.Len()), nil
}

// zipAdd is ZipFile.write(real, arcname) with ZIP_DEFLATED: the same member
// name, time, mode and content. The deflate stream itself is Go's, so the
// archive is equivalent to Python's rather than byte-identical.
func zipAdd(z *zip.Writer, real, arcname string, info fs.FileInfo) error {
	if !utf8.ValidString(arcname) {
		for i, pos := 0, 0; i < len(arcname); pos++ {
			r, size := utf8.DecodeRuneInString(arcname[i:])
			if r == utf8.RuneError && size <= 1 {
				return fmt.Errorf("'utf-8' codec can't encode character '\\udc%02x' in position %d: surrogates not allowed", arcname[i], pos)
			}
			i += size
		}
	}
	mtime := info.ModTime().Local()
	if mtime.Year() < 1980 {
		return errors.New("ZIP does not support timestamps before 1980")
	}
	f, err := os.Open(real)
	if err != nil {
		return wPyErr(err, real)
	}
	defer f.Close()
	// Only the MS-DOS time, as Python writes it (two-second resolution, local
	// time); Modified would add an extended timestamp Python never wrote.
	h := &zip.FileHeader{Name: pyNormpath(arcname), Method: zip.Deflate,
		ModifiedDate: uint16((mtime.Year()-1980)<<9 | int(mtime.Month())<<5 | mtime.Day()),
		ModifiedTime: uint16(mtime.Hour()<<11 | mtime.Minute()<<5 | mtime.Second()/2)}
	h.SetMode(info.Mode())
	out, err := z.CreateHeader(h)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, f); err != nil {
		return wPyErr(err)
	}
	return nil
}

// ---- git, index and search ------------------------------------------------

type gitOut struct {
	stdout []byte
	rc     int
}

// git is subprocess.run(['git', '-C', root, …]) with stdout captured and
// stderr discarded.
func (w *wsFiles) git(timeout float64, args ...string) (gitOut, error) {
	res, err := pyRun{Argv: append([]string{"git", "-C", w.root}, args...), Timeout: timeout,
		TimeoutText: pyTimeout(timeout, true), Stderr: io.Discard}.run()
	return gitOut{res.Stdout, res.RC}, err
}

func (w *wsFiles) isRepo() bool {
	if _, err := exec.LookPath("git"); err != nil {
		return false
	}
	out, err := w.git(10, "rev-parse", "--is-inside-work-tree")
	return err == nil && string(bytes.TrimSpace(out.stdout)) == "true"
}

func (w *wsFiles) keep(name string) bool {
	return name != "" && !(w.grouped && w.bookkeeping(wPyJoin(w.root, name)))
}

func (w *wsFiles) index() (*pyObj, error) {
	const limit = 50000
	started := time.Now()
	files, ignored, truncatedIgnored := []any{}, []any{}, []any{}
	source := "walk"
	switch {
	case w.isRepo():
		source = "git"
		out, err := w.git(20, "ls-files", "-z", "--cached", "--others", "--exclude-standard")
		if err != nil {
			return nil, err
		}
		seen := map[string]bool{}
		for _, name := range strings.Split(wPyDecodeReplace(out.stdout), "\x00") {
			if w.keep(name) && !seen[name] {
				seen[name] = true
				files = append(files, name)
			}
		}
		// Ignored files are a second pass. git reports an ignored folder once;
		// list what is inside it, capped per folder so that one dependency
		// tree cannot crowd out the rest.
		out, err = w.git(20, "ls-files", "-z", "--others", "--ignored", "--exclude-standard", "--directory")
		if err != nil {
			return nil, err
		}
		for _, name := range strings.Split(wPyDecodeReplace(out.stdout), "\x00") {
			if !w.keep(strings.TrimRight(name, "/")) {
				continue
			}
			if !strings.HasSuffix(name, "/") {
				ignored = append(ignored, name)
				continue
			}
			found := 0
			pyWalk(wPyJoin(w.root, name), func(base string, dirs, names []string) ([]string, bool) {
				var keep []string
				for _, d := range dirs {
					if d != ".git" && !pyIsLink(wPyJoin(base, d)) {
						keep = append(keep, d)
					}
				}
				sort.Strings(keep)
				sorted := append([]string(nil), names...)
				sort.Strings(sorted)
				for _, n := range sorted {
					ignored = append(ignored, pyRelpath(wPyJoin(base, n), w.root))
					found++
				}
				if found >= 5000 || len(ignored) > limit {
					truncatedIgnored = append(truncatedIgnored, name)
					return nil, false
				}
				return keep, true
			})
		}
	case lookPath("rg"):
		source = "ripgrep"
		res, err := pyRun{Argv: []string{"rg", "--files", "--hidden", "--glob", "!.git"}, Dir: w.root, Timeout: 20,
			TimeoutText: "20", Stderr: io.Discard}.run()
		if err != nil {
			return nil, err
		}
		for _, n := range strings.Split(wPyDecodeReplace(res.Stdout), "\n") {
			if w.keep(n) {
				files = append(files, n)
			}
		}
	default:
		pyWalk(w.root, func(base string, dirs, names []string) ([]string, bool) {
			var keep []string
			for _, d := range dirs {
				if d != ".git" && d != "node_modules" && !pyIsLink(wPyJoin(base, d)) {
					keep = append(keep, d)
				}
			}
			sort.Strings(keep)
			for _, n := range names {
				if name := pyRelpath(wPyJoin(base, n), w.root); w.keep(name) {
					files = append(files, name)
				}
			}
			return keep, len(files) <= limit
		})
	}
	truncated := len(files) > limit || len(ignored) > limit || len(truncatedIgnored) > 0
	return newObj("files", files[:min(len(files), limit)], "ignored", ignored[:min(len(ignored), limit)],
		"truncated", truncated, "source", source, "partial", truncatedIgnored,
		"elapsed_ms", time.Since(started).Milliseconds()), nil
}

func lookPath(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

func (w *wsFiles) gitStatus() (*pyObj, error) {
	if !w.isRepo() {
		return newObj("repository", false, "status", newObj()), nil
	}
	out, err := w.git(20, "status", "--porcelain=v1", "-z", "--ignored=matching", "--untracked-files=normal")
	if err != nil {
		return nil, err
	}
	parts := strings.Split(wPyDecodeReplace(out.stdout), "\x00")
	status := newObj()
	for i := 0; i < len(parts); {
		item := []rune(parts[i])
		i++
		if len(item) < 4 {
			continue
		}
		code, name := string(item[:2]), string(item[3:])
		if item[0] == 'R' || item[0] == 'C' {
			i++ // the original path follows a rename or copy
		}
		var kind string
		switch {
		case code == "!!":
			kind = "ignored"
		case code == "??":
			kind = "untracked"
		case strings.Contains(code, "U") || code == "AA" || code == "DD":
			kind = "conflict"
		case strings.Contains(code, "D"):
			kind = "deleted"
		case item[0] == 'A':
			kind = "added"
		case item[0] == 'R' || item[0] == 'C':
			kind = "renamed"
		default:
			kind = "modified"
		}
		if w.keep(strings.TrimRight(name, "/")) {
			status.Set(name, kind)
		}
		if status.Len() >= 20000 {
			break
		}
	}
	out, err = w.git(10, "rev-parse", "--show-prefix")
	if err != nil {
		return nil, err
	}
	prefix, err := pyDecodeStrict(out.stdout)
	if err != nil {
		return nil, err
	}
	return newObj("repository", true, "status", status, "prefix", wPyStrip(prefix)), nil
}

// searchPattern is re.compile of the query as the script built it, in Go's
// syntax: Python's own regular expressions are not available, so a pattern
// only Python accepts is refused here.
func searchPattern(query string, regex, caseSensitive, word bool) (*regexp.Regexp, error) {
	body := query
	if !regex {
		body = regexp.QuoteMeta(query)
	}
	if word {
		body = `\b(?:` + body + `)\b`
	}
	if !caseSensitive {
		body = "(?i)" + body
	}
	return regexp.Compile(body)
}

// runeCol is the 1-based character column of a byte offset in s.
func runeCol(s string, off int) int { return utf8.RuneCountInString(s[:off]) + 1 }

// byteToChar turns git grep's 1-based byte column into a character column.
func byteToChar(text string, col int) int {
	b := []byte(text)
	n := min(max(0, col-1), len(b))
	return utf8.RuneCountInString(wPyDecodeReplace(b[:n])) + 1
}

func (w *wsFiles) search() (*pyObj, error) {
	var ex [6]string
	for i := range ex {
		v, err := w.arg(i)
		if err != nil {
			return nil, err
		}
		ex[i] = v
	}
	query, regex, caseSensitive, word, include, ignored := ex[0], ex[1] == "1", ex[2] == "1", ex[3] == "1", ex[4], ex[5] == "1"
	if query == "" {
		return nil, wfRefuse("type something to search for")
	}
	const limit = 2000
	results := []any{}
	source, truncated := "python", false
	deadline := time.Now().Add(20 * time.Second)
	if regex {
		if _, err := regexp.Compile(query); err != nil && !perlOnlySyntax(err) {
			return nil, wfRefuse("invalid regular expression: " + err.Error())
		}
	}
	add := func(path string, line any, col int, text string, end int) bool {
		if len(results) >= limit {
			truncated = true
			return false
		}
		if w.keep(path) {
			text = strings.TrimRight(text, "\r\n")
			if r := []rune(text); len(r) > 400 {
				text = string(r[:400])
			}
			results = append(results, newObj("path", path, "line", line, "column", col, "end", end, "text", text))
		}
		return true
	}
	switch {
	case lookPath("rg"):
		source = "ripgrep"
		args := []string{"rg", "--json", "--hidden", "--glob", "!.git", "--max-columns", "400", "--max-count", "200"}
		if caseSensitive {
			args = append(args, "--case-sensitive")
		} else {
			args = append(args, "--ignore-case")
		}
		if word {
			args = append(args, "--word-regexp")
		}
		if !regex {
			args = append(args, "--fixed-strings")
		}
		if ignored {
			args = append(args, "--no-ignore")
		}
		for _, g := range globList(include) {
			args = append(args, "--glob", g)
		}
		args = append(args, "-e", query, "--", ".")
		err := streamLines(args, w.root, func(raw []byte) (bool, error) {
			if time.Now().After(deadline) {
				truncated = true
				return false, nil
			}
			event, err := pyLoads(wPyDecodeReplace(raw))
			if err != nil {
				return false, err
			}
			ev, _ := event.(*pyObj)
			if ev == nil || ev.Val("type") != "match" {
				return true, nil
			}
			d, _ := ev.Val("data").(*pyObj)
			pathObj, _ := d.Val("path").(*pyObj)
			path := pathObj.Str("text")
			path = strings.TrimPrefix(path, "./")
			linesObj, _ := d.Val("lines").(*pyObj)
			text := linesObj.Str("text")
			start, end := int64(0), int64(0)
			if subs, _ := d.Val("submatches").([]any); len(subs) > 0 {
				sub, _ := subs[0].(*pyObj)
				start, _ = pyIntValue(sub.Val("start"))
				end, _ = pyIntValue(sub.Val("end"))
			}
			col := byteToChar(text, int(start)+1)
			endCol := byteToChar(text, int(end)+1)
			return add(path, d.Val("line_number"), col, text, endCol), nil
		})
		if err != nil {
			return nil, err
		}
	case w.isRepo() && !ignored:
		source = "git grep"
		// PCRE matches ripgrep's syntax most closely; a git built without it
		// exits 128 before printing anything, and ERE is the fallback.
		flavours := []string{"-F"}
		if regex {
			flavours = []string{"-P", "-E"}
		}
		grepLine := regexp.MustCompile(`^(.*?):(\d+):(\d+):(.*)$`)
		for _, flavour := range flavours {
			args := []string{"git", "-C", w.root, "grep", "-n", "--column", "-I", "--untracked", "--no-color", flavour}
			if !caseSensitive {
				args = append(args, "-i")
			}
			if word {
				args = append(args, "-w")
			}
			args = append(args, "-e", query, "--")
			if include != "" {
				for _, g := range strings.Split(include, ",") {
					if wPyStrip(g) == "" {
						continue
					}
					if strings.Contains(g, "/") {
						args = append(args, ":(glob)"+wPyStrip(g))
					} else {
						args = append(args, ":(glob)**/"+wPyStrip(g))
					}
				}
			}
			rc, err := streamLinesRC(args, "", func(raw []byte) (bool, error) {
				if time.Now().After(deadline) {
					truncated = true
					return false, nil
				}
				line := strings.TrimSuffix(wPyDecodeReplace(raw), "\n")
				m := grepLine.FindStringSubmatch(line)
				if m == nil {
					return true, nil
				}
				text := m[4]
				n, _ := strconv.Atoi(m[3])
				col := byteToChar(text, n)
				lineNo, _ := strconv.ParseInt(m[2], 10, 64)
				return add(m[1], lineNo, col, text, col+matchLength(query, regex, caseSensitive, word, text, col)), nil
			})
			if err != nil {
				return nil, err
			}
			if len(results) > 0 || rc != 128 {
				break
			}
		}
	default:
		pattern, err := searchPattern(query, regex, caseSensitive, word)
		if err != nil {
			return nil, wfRefuse("invalid regular expression: " + err.Error())
		}
		globs := globList(include)
		var matchers []*regexp.Regexp
		for _, g := range globs {
			matchers = append(matchers, fnmatchRegexp(g))
		}
		pyWalk(w.root, func(base string, dirs, names []string) ([]string, bool) {
			if time.Now().After(deadline) {
				truncated = true
				return nil, false
			}
			var keep []string
			for _, d := range dirs {
				if d != ".git" && (ignored || d != "node_modules") && !pyIsLink(wPyJoin(base, d)) {
					keep = append(keep, d)
				}
			}
			sort.Strings(keep)
			sorted := append([]string(nil), names...)
			sort.Strings(sorted)
			for _, n := range sorted {
				full := wPyJoin(base, n)
				name := pyRelpath(full, w.root)
				if len(matchers) > 0 {
					hit := false
					for _, m := range matchers {
						hit = hit || m.MatchString(n) || m.MatchString(name)
					}
					if !hit {
						continue
					}
				}
				if pyIsLink(full) {
					continue
				}
				// Only regular files: the Python version opened a FIFO here and
				// hung until the executor's deadline.
				info, err := os.Stat(full)
				if err != nil || info.Size() > 4*1024*1024 || !info.Mode().IsRegular() {
					continue
				}
				data, err := os.ReadFile(full)
				if err != nil {
					continue
				}
				if bytes.IndexByte(data[:min(len(data), 8192)], 0) >= 0 {
					continue
				}
				for number, text := range strings.Split(wPyDecodeReplace(data), "\n") {
					loc := pattern.FindStringIndex(text)
					if loc != nil && !add(name, number+1, runeCol(text, loc[0]), text, runeCol(text, loc[1])) {
						break
					}
				}
				if truncated {
					return nil, false
				}
			}
			return keep, true
		})
	}
	return newObj("results", results, "truncated", truncated, "source", source), nil
}

// perlOnlySyntax reports a pattern Go cannot compile only because it uses
// Perl features (lookaround, backreferences) that ripgrep's PCRE mode and
// git grep -P may still accept: those are left to the tool.
func perlOnlySyntax(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "invalid or unsupported Perl syntax") || strings.Contains(msg, "invalid escape sequence: `\\1") ||
		strings.Contains(msg, "invalid escape sequence: `\\2")
}

func globList(include string) []string {
	var out []string
	if include == "" {
		return out
	}
	for _, g := range strings.Split(include, ",") {
		if g = wPyStrip(g); g != "" {
			out = append(out, g)
		}
	}
	return out
}

// matchLength is how many characters the query matches at col, for the
// highlight's end; the query's own length when it cannot be matched here.
func matchLength(query string, regex, caseSensitive, word bool, text string, col int) int {
	pattern, err := searchPattern(query, regex, caseSensitive, word)
	if err == nil {
		r := []rune(text)
		from := min(max(0, col-1), len(r))
		rest := string(r[from:])
		if loc := pattern.FindStringIndex(rest); loc != nil {
			return utf8.RuneCountInString(rest[loc[0]:loc[1]])
		}
	}
	return utf8.RuneCountInString(query)
}

// fnmatchRegexp is fnmatch.translate: * and ? match any character (slashes
// included), [...] a set, [!...] its complement.
func fnmatchRegexp(pat string) *regexp.Regexp {
	var b strings.Builder
	b.WriteString(`(?s)^`)
	r := []rune(pat)
	for i := 0; i < len(r); i++ {
		switch c := r[i]; c {
		case '*':
			b.WriteString(".*")
		case '?':
			b.WriteString(".")
		case '[':
			j := i + 1
			if j < len(r) && r[j] == '!' {
				j++
			}
			if j < len(r) && r[j] == ']' {
				j++
			}
			for j < len(r) && r[j] != ']' {
				j++
			}
			if j >= len(r) {
				b.WriteString(`\[`)
				continue
			}
			set := string(r[i+1 : j])
			i = j
			if strings.HasPrefix(set, "!") {
				set = "^" + set[1:]
			} else if strings.HasPrefix(set, "^") {
				set = `\` + set
			}
			set = strings.ReplaceAll(set, `\`, `\\`)
			set = strings.ReplaceAll(set, `[`, `\[`)
			b.WriteString("[" + set + "]")
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	b.WriteString(`$`)
	re, err := regexp.Compile(b.String())
	if err != nil {
		return regexp.MustCompile(`^` + regexp.QuoteMeta(pat) + `$`)
	}
	return re
}

// streamLines runs argv (in dir) and hands each stdout line to fn until fn
// says stop; the process is then killed, as the script's Popen was.
func streamLines(argv []string, dir string, fn func([]byte) (bool, error)) error {
	_, err := streamLinesRC(argv, dir, fn)
	return err
}

func streamLinesRC(argv []string, dir string, fn func([]byte) (bool, error)) (int, error) {
	if _, err := exec.LookPath(argv[0]); err != nil {
		return 0, &pyOSError{errno: 2, filename: &argv[0]}
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = dir
	out, err := cmd.StdoutPipe()
	if err != nil {
		return 0, err
	}
	if err := cmd.Start(); err != nil {
		return 0, wPyErr(err, argv[0])
	}
	r := bufio.NewReaderSize(out, 64<<10)
	var ferr error
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 {
			more, e := fn(line)
			if e != nil {
				ferr = e
				break
			}
			if !more {
				break
			}
		}
		if err != nil {
			break
		}
	}
	cmd.Process.Kill()
	werr := cmd.Wait()
	return exitStatus(cmd, werr), ferr
}

// ---- watch ------------------------------------------------------------------

// watchSignature summarises the watched folders (and git's HEAD and index)
// as the script did: sha1 of json.dumps of their listings, 20 hex digits.
func (w *wsFiles) watchSignature(dirs []string) string {
	parts := []any{}
	for _, d := range dirs {
		p, err := w.resolved(d)
		if err != nil {
			continue
		}
		names, err := readdirUnsorted(p)
		if err != nil {
			parts = append(parts, []any{d, nil})
			continue
		}
		type row struct {
			name        string
			mtime, size int64
		}
		var rows []row
		for _, name := range names {
			if len(rows) >= 2000 {
				break
			}
			info, err := os.Lstat(wPyJoin(p, name))
			if err != nil {
				continue
			}
			rows = append(rows, row{name, wStatMtimeNs(info), info.Size()})
		}
		sort.Slice(rows, func(i, j int) bool {
			if rows[i].name != rows[j].name {
				return rows[i].name < rows[j].name
			}
			if rows[i].mtime != rows[j].mtime {
				return rows[i].mtime < rows[j].mtime
			}
			return rows[i].size < rows[j].size
		})
		list := []any{}
		for _, r := range rows {
			list = append(list, []any{r.name, r.mtime, r.size})
		}
		parts = append(parts, []any{d, list})
	}
	for _, name := range []string{"HEAD", "index"} {
		if info, err := os.Stat(wPyJoin(w.root, ".git", name)); err == nil {
			parts = append(parts, []any{".git/" + name, wStatMtimeNs(info), info.Size()})
		}
	}
	sum := sha1.Sum([]byte(pyDumps(parts)))
	return hex.EncodeToString(sum[:])[:20]
}

func wStatMtimeNs(info fs.FileInfo) int64 { return info.ModTime().UnixNano() }

// watch is a long-poll: it answers as soon as a watched folder (or git's
// HEAD/index) differs from the token the client last saw, or when the time
// is up.
func (w *wsFiles) watch() (*pyObj, error) {
	rawDirs, err := w.arg(0)
	if err != nil {
		return nil, err
	}
	decoded, err := pyLoads(rawDirs)
	if err != nil {
		return nil, err
	}
	list, ok := decoded.([]any)
	if !ok {
		return nil, &pyUncaughtError{"TypeError", fmt.Errorf("unhashable type: 'slice'")}
	}
	var dirs []string
	for _, d := range list[:min(len(list), 64)] {
		if s, ok := d.(string); ok {
			dirs = append(dirs, s)
		}
	}
	if len(dirs) == 0 {
		dirs = []string{"."}
	}
	token, err := w.arg(1)
	if err != nil {
		return nil, err
	}
	limitText, err := w.arg(2)
	if err != nil {
		return nil, err
	}
	limit, err := strconv.ParseFloat(wPyStrip(limitText), 64)
	if err != nil {
		return nil, fmt.Errorf("could not convert string to float: %s", pyReprString(limitText))
	}
	limit = min(max(limit, 1), 50)
	// Watch first, then look: a change between the two is still an event.
	watcher := w.startWatch(dirs)
	defer watcher.close()
	current := w.watchSignature(dirs)
	mode := "poll"
	if watcher.ok() {
		mode = "inotify"
	}
	if token == "" || current != token {
		return newObj("token", current, "changed", token != "", "mode", mode), nil
	}
	deadline := time.Now().Add(time.Duration(limit * float64(time.Second)))
	for time.Now().Before(deadline) {
		wait := time.Until(deadline)
		if watcher.ok() {
			if !watcher.wait(wait) {
				break
			}
			time.Sleep(150 * time.Millisecond) // let a burst of writes settle
			watcher.drain()
		} else {
			time.Sleep(min(500*time.Millisecond, wait))
		}
		if now := w.watchSignature(dirs); now != token {
			return newObj("token", now, "changed", true, "mode", mode), nil
		}
	}
	return newObj("token", token, "changed", false, "mode", mode), nil
}
