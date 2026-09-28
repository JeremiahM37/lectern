package helpers

// skills is internal/skills' target-side script: discover Agent Skills
// directories, link one into a worktree (materialize), take such a link out
// again (remove) and keep it out of `git status` (exclude / unexclude). The
// link is made through directory descriptors that refuse symlinks, below a
// worktree root that must itself be a real directory.
//
//	lectern helper skills '{"op":…}'

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"unicode/utf8"
)

func init() { Register("skills", skillsHelper) }

func skillsHelper(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	if len(args) < 1 {
		return pyUncaught(stderr, "IndexError", errors.New("list index out of range"))
	}
	raw, err := pyLoads(args[0])
	if err != nil {
		return pyUncaught(stderr, "json.decoder.JSONDecodeError", err)
	}
	a, ok := raw.(*pyObj)
	if !ok {
		return pyUncaught(stderr, "AttributeError", fmt.Errorf("'%s' object has no attribute 'get'", pyTypeName(raw)))
	}
	out, err := skillsMain(a, stderr)
	if err != nil {
		return pyUncaught(stderr, pyErrKind(err), err)
	}
	fmt.Fprintln(stdout, pyDumpsCompact(out))
	return 0
}

// pathArg is a[key] used as a path.
func pathArg(a *pyObj, key string) (string, error) {
	v, err := pyIndex(a, key)
	if err != nil {
		return "", err
	}
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("expected str, bytes or os.PathLike object, not %s", pyTypeName(v))
	}
	return s, nil
}

// safeParent opens dst's parent below base, one component at a time without
// following symlinks; create makes missing directories. A nil dirFD means
// the parent does not exist (create false).
func safeParent(dst, base string, create bool) (*dirFD, string, error) {
	cur := pyAbspath(base)
	parent := pyDirname(pyAbspath(dst))
	if pyIsLink(cur) || !pyIsDir(cur) {
		return nil, "", errors.New("worktree root is unsafe")
	}
	rel := pyRelpath(parent, cur)
	if strings.HasPrefix(rel, "../") || rel == ".." {
		return nil, "", errors.New("destination escapes worktree")
	}
	fd, err := openDirPath(cur)
	if err != nil {
		return nil, "", err
	}
	if rel != "." {
		for _, part := range strings.Split(rel, "/") {
			next, err := fd.openDir(part)
			if err != nil && pyErrKind(err) == "FileNotFoundError" {
				if !create {
					fd.close()
					return nil, pyBasename(dst), nil
				}
				if err := fd.mkdir(part, 0o777); err != nil {
					fd.close()
					return nil, "", err
				}
				next, err = fd.openDir(part)
			}
			fd.close()
			if err != nil {
				return nil, "", err
			}
			fd = next
		}
	}
	return fd, pyBasename(dst), nil
}

// gitpat anchors a path as a literal gitignore pattern.
func gitpat(line string) string {
	r := strings.NewReplacer(`\`, `\\`, "*", `\*`, "?", `\?`, "[", `\[`, "]", `\]`)
	return "/" + r.Replace(line)
}

func skillsEditExclude(repo, marker, line string, remove bool, stderr io.Writer) (string, error) {
	// The script ran this through os.popen: Git's errors reach stderr and its
	// exit status is not checked.
	res, err := pyRun{Argv: []string{"git", "-c", "safe.directory=" + repo, "-C", repo, "rev-parse", "--git-path", "info/exclude"},
		Stderr: stderr}.run()
	p := ""
	if err == nil {
		if p, err = res.text(res.Stdout); err != nil {
			return "", err
		}
	}
	p = pyStrip(p)
	if p == "" {
		return "", errors.New("Git info/exclude unavailable")
	}
	if !pyIsAbs(p) {
		p = pyJoin(repo, p)
	}
	lockPath := p + ".lectern.lock"
	lock, err := os.OpenFile(lockPath, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0o666)
	if err != nil {
		return "", pyErr(err, lockPath)
	}
	defer lock.Close()
	if err := lockExclusive(lock); err != nil {
		return "", pyErr(err)
	}
	old, err := pyReadText(p)
	if err != nil {
		if _, isOS := pyErrno(err); !isOS {
			return "", err
		}
		old = ""
	}
	pair := marker + "\n" + gitpat(line) + "\n"
	updated := old
	switch {
	case remove:
		updated = strings.ReplaceAll(old, pair, "")
	case !strings.Contains(old, pair):
		sep := ""
		if old != "" && !strings.HasSuffix(old, "\n") {
			sep = "\n"
		}
		updated = old + sep + pair
	}
	if updated != old {
		f, tmp, err := pyMkstemp(pyDirname(p), ".lectern-exclude-")
		if err != nil {
			return "", err
		}
		_, werr := f.WriteString(updated)
		f.Close()
		if werr != nil {
			return "", pyErr(werr)
		}
		if err := pyReplace(tmp, p); err != nil {
			return "", err
		}
	}
	return p, nil
}

// gitText is subprocess.check_output(['git', …], text=True): nil error only
// for exit status 0.
func gitText(args ...string) (string, error) {
	res, err := pyRun{Argv: append([]string{"git"}, args...), Stderr: io.Discard}.run()
	if err != nil {
		return "", err
	}
	if res.RC != 0 {
		return "", fmt.Errorf("git exited %d", res.RC)
	}
	return res.text(res.Stdout)
}

// trackedNative reports whether dst is src itself as the same repository
// file checked out elsewhere (a committed skill in a linked worktree).
func trackedNative(src, dst string) bool {
	sr, err1 := gitText("-C", src, "rev-parse", "--show-toplevel")
	dr, err2 := gitText("-C", dst, "rev-parse", "--show-toplevel")
	sc, err3 := gitText("-C", src, "rev-parse", "--git-common-dir")
	dc, err4 := gitText("-C", dst, "rev-parse", "--git-common-dir")
	if err1 != nil || err2 != nil || err3 != nil || err4 != nil {
		return false
	}
	sr, dr, sc, dc = pyStrip(sr), pyStrip(dr), pyStrip(sc), pyStrip(dc)
	if pyRealpath(pyJoin(src, sc)) != pyRealpath(pyJoin(dst, dc)) {
		return false
	}
	if sr == "" || dr == "" {
		return false // relpath of an empty path raises
	}
	rel := pyRelpath(src, sr)
	if strings.HasPrefix(rel, "../") || rel == ".." {
		return false
	}
	if pyNormpath(pyRelpath(dst, dr)) != pyNormpath(rel) {
		return false
	}
	res, err := pyRun{Argv: []string{"git", "-C", dr, "ls-files", "--error-unmatch", "--", pyJoin(rel, "SKILL.md")},
		Stdout: io.Discard, Stderr: io.Discard}.run()
	return err == nil && res.RC == 0
}

type skillRoot struct{ kind, path string }

func skillRoots(a *pyObj) ([]skillRoot, error) {
	var out []skillRoot
	add := func(kind, p string) {
		if p != "" && pyIsDir(p) {
			out = append(out, skillRoot{kind, pyRealpath(p)})
		}
	}
	repo := ""
	if v := a.Val("repo"); pyTruthy(v) {
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("expected str, bytes or os.PathLike object, not %s", pyTypeName(v))
		}
		repo = pyAbspath(s)
	}
	agent := "claude"
	if v, ok := a.Get("agent"); ok {
		agent, _ = v.(string)
	}
	if repo != "" {
		gr, err := gitText("-C", repo, "rev-parse", "--show-toplevel")
		if err != nil {
			gr = repo
		} else {
			gr = pyStrip(gr)
		}
		dir := ".agents"
		if agent == "claude" {
			dir = ".claude"
		}
		top := pyRealpath(gr)
		for cur := repo; cur != ""; cur = pyDirname(cur) {
			common, err := pyCommonpath(pyRealpath(cur), top)
			if err != nil {
				return nil, err
			}
			if common != top {
				break
			}
			add("repo:"+pyRealpath(cur), pyJoin(cur, dir, "skills"))
			if pyRealpath(cur) == top {
				break
			}
		}
	}
	home := func(p string) string { return pyExpanduser(p) }
	switch agent {
	case "claude":
		base, ok := os.LookupEnv("CLAUDE_CONFIG_DIR")
		if !ok {
			base = home("~/.claude")
		}
		add("claude-user", pyJoin(base, "skills"))
	case "codex":
		add("codex-user", home("~/.agents/skills"))
		add("codex-system", "/etc/codex/skills")
	case "gemini":
		add("gemini-user", home("~/.gemini/skills"))
		add("agents-user", home("~/.agents/skills"))
	case "qwen":
		add("qwen-user", home("~/.qwen/skills"))
		add("agents-user", home("~/.agents/skills"))
	case "copilot":
		add("copilot-user", home("~/.copilot/skills"))
		add("agents-user", home("~/.agents/skills"))
	case "opencode":
		cfg := os.Getenv("XDG_CONFIG_HOME")
		if cfg == "" {
			cfg = home("~/.config")
		}
		for _, d := range []string{"skills", "skill"} {
			add("opencode-user-"+d, pyJoin(cfg, "opencode", d))
		}
		add("agents-user", home("~/.agents/skills"))
	}
	if v, ok := a.Get("configured"); ok {
		list, err := pyIter(v)
		if err != nil {
			return nil, err
		}
		for _, item := range list {
			p, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("expected str, bytes or os.PathLike object, not %s", pyTypeName(item))
			}
			sum := sha256.Sum256([]byte(pyRealpath(p)))
			add("configured:"+hex.EncodeToString(sum[:])[:12], p)
		}
	}
	return out, nil
}

// readTextChars is open(p, encoding='utf8').read(n): n characters, decoded
// strictly chunk by chunk the way TextIOWrapper does (8 KiB at a time), with
// universal newlines.
func readTextChars(p string, n int) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", pyErr(err, p)
	}
	defer f.Close()
	var raw []byte
	buf := make([]byte, 8192)
	for {
		m, rerr := f.Read(buf)
		raw = append(raw, buf[:m]...)
		// Everything read so far must decode, except an unfinished sequence
		// at the end while more may follow.
		complete := raw
		if rerr == nil {
			for cut := len(raw); cut > 0 && cut > len(raw)-4; cut-- {
				if utf8.RuneStart(raw[cut-1]) {
					if _, truncated := utf8Span(raw[cut-1:]); truncated {
						complete = raw[:cut-1]
					}
					break
				}
			}
		}
		text, derr := pyDecodeStrict(complete)
		if derr != nil {
			return "", derr
		}
		text = pyUniversalNewlines(text)
		if rerr != nil || utf8.RuneCountInString(text) >= n {
			if rerr != nil && rerr != io.EOF {
				return "", pyErr(rerr, p)
			}
			if rerr == io.EOF && len(complete) < len(raw) {
				if _, derr := pyDecodeStrict(raw); derr != nil {
					return "", derr
				}
			}
			runes := []rune(text)
			if len(runes) > n {
				runes = runes[:n]
			}
			return string(runes), nil
		}
	}
}

func discoverSkills(a *pyObj) ([]any, error) {
	roots, err := skillRoots(a)
	if err != nil {
		return nil, err
	}
	out := []any{}
	seen := map[[2]string]bool{}
	for _, root := range roots {
		entries, err := os.ReadDir(root.path)
		if err != nil {
			continue
		}
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		sort.Strings(names)
		for _, n := range names {
			q := pyJoin(root.path, n)
			md := pyJoin(q, "SKILL.md")
			if !pyIsDir(q) || !pyIsFile(md) {
				continue
			}
			key := [2]string{pyRealpath(root.path), n}
			if seen[key] {
				continue
			}
			seen[key] = true
			name, desc := n, ""
			text, err := readTextChars(md, 8192)
			if err != nil {
				if _, isOS := pyErrno(err); !isOS {
					return nil, err
				}
			} else {
				for _, line := range pySplitLines(text, false) {
					lower := strings.ToLower(line)
					if strings.HasPrefix(lower, "name:") {
						_, after, _ := strings.Cut(line, ":")
						if name = pyStrip(after); name == "" {
							name = n
						}
					}
					if strings.HasPrefix(lower, "description:") {
						_, after, _ := strings.Cut(line, ":")
						desc = pyStrip(after)
					}
				}
			}
			out = append(out, newObj("id", root.kind+"/"+n, "name", name, "source", root.kind, "source_path", q,
				"entry_name", n, "kind", "dir", "description", desc))
		}
	}
	return out, nil
}

func skillsMain(a *pyObj, stderr io.Writer) (*pyObj, error) {
	switch a.Val("op") {
	case "discover":
		list, err := discoverSkills(a)
		if err != nil {
			return nil, err
		}
		return newObj("skills", list), nil
	case "materialize":
		return skillsMaterialize(a)
	case "remove":
		return skillsRemove(a)
	case "exclude", "unexclude":
		repo, err := pathArg(a, "repo")
		if err != nil {
			return nil, err
		}
		marker, err := pyIndex(a, "marker")
		if err != nil {
			return nil, err
		}
		line, err := pyIndex(a, "line")
		if err != nil {
			return nil, err
		}
		m, _ := marker.(string)
		l, _ := line.(string)
		p, err := skillsEditExclude(pyAbspath(repo), m, l, a.Val("op") == "unexclude", stderr)
		if err != nil {
			return nil, err
		}
		return newObj("path", p), nil
	}
	return newObj("error", "unknown operation"), nil
}

func skillsMaterialize(a *pyObj) (*pyObj, error) {
	source, err := pathArg(a, "source")
	if err != nil {
		return nil, err
	}
	target, err := pathArg(a, "target")
	if err != nil {
		return nil, err
	}
	base, err := pathArg(a, "base")
	if err != nil {
		return nil, err
	}
	src, dst := pyRealpath(source), pyAbspath(target)
	fd, name, err := safeParent(dst, base, true)
	if err != nil {
		return nil, err
	}
	defer fd.close()
	if !pyIsDir(src) || !pyIsFile(pyJoin(src, "SKILL.md")) {
		return newObj("error", "skill source is unavailable"), nil
	}
	if _, err := fd.lstat(name); err == nil {
		if pyAbspath(src) == dst {
			return newObj("preexisting", true), nil
		}
		kind, _ := a.Val("source_kind").(string)
		if strings.HasPrefix(kind, "repo:") && !pyIsLink(dst) && pyIsDir(dst) && pyIsFile(pyJoin(dst, "SKILL.md")) && trackedNative(src, dst) {
			return newObj("preexisting", true), nil
		}
		if pyIsLink(dst) && pyRealpath(dst) == src && pyTruthy(a.Val("owned")) {
			return newObj("already", true), nil
		}
		return newObj("error", "skill destination already exists"), nil
	} else if pyErrKind(err) != "FileNotFoundError" {
		return nil, err
	}
	if err := fd.symlink(src, name); err != nil {
		return newObj("error", "symlink materialization unavailable: "+err.Error()), nil
	}
	return newObj("created", true), nil
}

func skillsRemove(a *pyObj) (*pyObj, error) {
	target, err := pathArg(a, "target")
	if err != nil {
		return nil, err
	}
	source, err := pathArg(a, "source")
	if err != nil {
		return nil, err
	}
	base, err := pathArg(a, "base")
	if err != nil {
		return nil, err
	}
	dst, src := pyAbspath(target), pyRealpath(source)
	fd, name, err := safeParent(dst, base, false)
	if err != nil {
		return nil, err
	}
	if fd == nil {
		return newObj("removed", false, "missing", true), nil
	}
	defer fd.close()
	st, err := fd.lstat(name)
	if err != nil {
		if pyErrKind(err) == "FileNotFoundError" {
			return newObj("removed", false, "missing", true), nil
		}
		return nil, err
	}
	if !pyTruthy(a.Val("owned")) {
		return newObj("error", "skill ownership is not established; preserving target"), nil
	}
	link, err := fd.readlink(name)
	if err != nil || st.Mode()&os.ModeSymlink == 0 || pyRealpath(pyJoin(pyDirname(dst), link)) != src {
		return newObj("error", "owned skill link changed; preserving target"), nil
	}
	if err := fd.unlink(name); err != nil {
		return nil, err
	}
	return newObj("removed", true), nil
}
