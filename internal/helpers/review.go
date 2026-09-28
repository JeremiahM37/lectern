package helpers

// review is internal/api's scripts/review.py: the working or staged Git
// changes of a workspace on its own target, and the patch of one file. It
// never stages, checks out or writes. Git runs without fsmonitor, colour,
// external diff or textconv programs, and its output is capped rather than
// held whole.
//
//	lectern helper review WORKSPACE working|staged [PATH]

import (
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
)

func init() { Register("review", reviewHelper) }

const reviewLimit = 512 * 1024

// capWriter keeps the first max bytes written and counts the rest, as the
// scripts' read(limit + 1) of a spooled temporary file did.
type capWriter struct {
	max  int
	data []byte
	more bool
}

func (w *capWriter) Write(p []byte) (int, error) {
	if room := w.max - len(w.data); room > 0 {
		w.data = append(w.data, p[:min(room, len(p))]...)
		if len(p) > room {
			w.more = true
		}
	} else if len(p) > 0 {
		w.more = true
	}
	return len(p), nil
}

// reviewGit runs git the way both review scripts do.
type reviewGit struct {
	env     []string
	timeout float64
	errCap  int // bytes of stderr kept for the error message
	errKind func(string) error
}

func newReviewGit(timeout float64, errCap int, errKind func(string) error) reviewGit {
	return reviewGit{env: pyEnviron("GIT_OPTIONAL_LOCKS", "0", "GIT_TERMINAL_PROMPT", "0", "GIT_LITERAL_PATHSPECS", "1"),
		timeout: timeout, errCap: errCap, errKind: errKind}
}

// run returns at most limit bytes of stdout and whether there was more; an
// exit status outside allowed is an error carrying git's stderr.
func (g reviewGit) run(args []string, limit int, allowed []int, stdin []byte) ([]byte, bool, error) {
	out := &capWriter{max: limit + 1}
	errOut := &capWriter{max: g.errCap}
	argv := append([]string{"git", "-c", "core.fsmonitor=false", "-c", "color.ui=false"}, args...)
	res, err := pyRun{Argv: argv, Env: g.env, Stdin: stdin, Timeout: g.timeout,
		TimeoutText: pyTimeout(g.timeout, true), Stdout: out, Stderr: errOut}.run()
	if err != nil {
		return nil, false, err
	}
	ok := false
	for _, rc := range allowed {
		ok = ok || rc == res.RC
	}
	if !ok {
		msg := wPyStrip(wPyDecodeReplace(errOut.data))
		if msg == "" {
			msg = "Git command failed"
		}
		return nil, false, g.errKind(msg)
	}
	data := out.data
	cut := len(data) > limit
	if cut {
		data = data[:limit]
	}
	return data, cut, nil
}

// statusRecord is one entry of `git status --porcelain=v1 -z`.
type statusRecord struct {
	xy, name string
	old      *string
}

// porcelainRecords splits status output; renamed or copied entries carry
// their original path in the next field. renamedEither also counts a rename
// in the worktree column, as review.py does.
func porcelainRecords(raw []byte, renamedEither bool) []statusRecord {
	parts := strings.Split(string(raw), "\x00")
	var out []statusRecord
	for i := 0; i < len(parts) && parts[i] != ""; {
		item := parts[i]
		i++
		head := item
		if len(head) > 2 {
			head = head[:2]
		}
		rec := statusRecord{xy: wPyDecodeReplace([]byte(head))}
		if len(item) > 3 {
			rec.name = item[3:]
		}
		x, y := "", ""
		if r := []rune(rec.xy); len(r) > 1 {
			x, y = string(r[0]), string(r[1])
		} else if len(r) == 1 {
			x = string(r[0])
		}
		if (x == "R" || x == "C" || (renamedEither && (y == "R" || y == "C"))) && i < len(parts) {
			old := parts[i]
			rec.old = &old
			i++
		}
		out = append(out, rec)
	}
	return out
}

func reviewHelper(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	if len(args) < 3 {
		return pyUncaught(stderr, "ValueError", fmt.Errorf("not enough values to unpack (expected 2, got %d)", max(0, len(args)-1)))
	}
	scope, requested := args[1], args[2]
	workspace := pyRealpath(args[0])
	if err := os.Chdir(workspace); err != nil {
		return pyUncaught(stderr, pyErrKind(err), wPyErr(err, workspace))
	}
	out, err := reviewChanges(workspace, scope, requested)
	if err != nil {
		fmt.Fprintln(stdout, pyDumps(newObj("error", err.Error())))
		return 1
	}
	fmt.Fprintln(stdout, pyDumps(out))
	return 0
}

func reviewChanges(workspace, scope, requested string) (*pyObj, error) {
	g := newReviewGit(15, 2048, func(msg string) error { return errors.New(msg) })
	text := wPyDecodeReplace
	if scope != "working" && scope != "staged" {
		return nil, errors.New("Choose working or staged changes")
	}
	raw, _, err := g.run([]string{"rev-parse", "--show-toplevel"}, reviewLimit, []int{0}, nil)
	if err != nil {
		return nil, err
	}
	top := pyRealpath(wPyStrip(text(raw)))
	raw, _, err = g.run([]string{"symbolic-ref", "--short", "-q", "HEAD"}, reviewLimit, []int{0, 1}, nil)
	if err != nil {
		return nil, err
	}
	branch := wPyStrip(text(raw))
	if branch == "" {
		raw, _, err = g.run([]string{"rev-parse", "--short", "HEAD"}, reviewLimit, []int{0, 128}, nil)
		if err != nil {
			return nil, err
		}
		if branch = wPyStrip(text(raw)); branch == "" {
			branch = "unborn"
		}
	}
	raw, cut, err := g.run([]string{"status", "--porcelain=v1", "-z", "--untracked-files=all", "--", "."}, 2*reviewLimit, []int{0}, nil)
	if err != nil {
		return nil, err
	}
	if cut {
		return nil, errors.New("Too many changed paths to review; narrow the session workspace")
	}
	var files []*pyObj
	for _, rec := range porcelainRecords(raw, true) {
		if len([]rune(rec.xy)) < 2 {
			return nil, errors.New("string index out of range")
		}
		full := pyNormpath(wPyJoin(top, rec.name))
		if !pyInside(full, workspace) {
			continue
		}
		var old any
		if rec.old != nil {
			old = *rec.old
		}
		xy := []rune(rec.xy)
		x, y := string(xy[0]), string(xy[1])
		files = append(files, newObj("path", pyRelpath(full, workspace), "status", rec.xy,
			"working", y != " " || rec.xy == "??", "staged", x != " " && x != "?", "previous_path", old))
		if len(files) > 2000 {
			return nil, errors.New("More than 2000 changed files; narrow the session workspace")
		}
	}
	sort.SliceStable(files, func(i, j int) bool {
		return pyCasefold(files[i].Str("path")) < pyCasefold(files[j].Str("path"))
	})
	var chosen *pyObj
	for _, f := range files {
		if f.Val(scope) != true {
			continue
		}
		if (requested != "" && f.Str("path") == requested) || (requested == "" && chosen == nil) {
			chosen = f
			break
		}
	}
	if requested != "" && chosen == nil {
		return nil, errors.New("This file is no longer in the selected changes; refresh the list")
	}
	patch, truncated := "", false
	if chosen != nil {
		rel := chosen.Str("path")
		diff := []string{"diff", "--no-ext-diff", "--no-textconv", "--no-color", "--unified=3"}
		if chosen.Str("status") == "??" {
			// Do not follow untracked symlinks or open FIFOs/devices. Tracked
			// symlinks are handled by Git itself as links, not target contents.
			full := wPyJoin(workspace, rel)
			info, err := os.Lstat(full)
			if err != nil {
				return nil, wPyErr(err, full)
			}
			if !info.Mode().IsRegular() || pyRealpath(full) != full {
				patch = "Untracked link or special file: content preview unavailable."
			} else {
				data, cut, err := g.run(append(diff, "--no-index", "--", "/dev/null", rel), reviewLimit, []int{0, 1}, nil)
				if err != nil {
					return nil, err
				}
				patch, truncated = text(data), cut
			}
		} else {
			if scope == "staged" {
				diff = append(diff, "--cached")
			}
			paths := []string{rel}
			if prev, ok := chosen.Val("previous_path").(string); ok && prev != "" {
				prior := pyNormpath(wPyJoin(top, prev))
				if pyInside(prior, workspace) {
					paths = append(paths, pyRelpath(prior, workspace))
				}
			}
			data, cut, err := g.run(append(append(diff, "--"), paths...), reviewLimit, []int{0}, nil)
			if err != nil {
				return nil, err
			}
			patch, truncated = text(data), cut
		}
	}
	list := []any{}
	for _, f := range files {
		list = append(list, f)
	}
	path := ""
	if chosen != nil {
		path = chosen.Str("path")
	}
	return newObj("branch", branch, "scope", scope, "files", list, "path", path, "patch", patch,
		"truncated", truncated, "limit_bytes", reviewLimit), nil
}
