package helpers

// review-git is internal/api's scripts/review_git.py: the review workspace's
// Git operations on the session's target — status with staged and unstaged
// patches, stage/unstage/discard per file and per hunk, conflicts and their
// resolution, blobs and blame. Every path is checked to stay inside the
// workspace, and nothing runs repository-defined diff or textconv programs.
// A refusal or a Git failure is {"error": ...} with exit status 1.
//
//	lectern helper review-git WORKSPACE ACTION [BASE64_JSON]

import (
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"
)

func init() { Register("review-git", reviewGitHelper) }

const (
	reviewBlobLimit = 8 * 1024 * 1024
	reviewZero      = "0000000000000000000000000000000000000000"
	emptyBlob       = "e69de29bb2d1d6434b8b29ae775ad8c2e48c5391"
)

var (
	conflictMarker = regexp.MustCompile(`(?m)^(<{7}|>{7})( |$)`)
	agentTrailer   = regexp.MustCompile(`(?i)(co-authored-by|generated[- ]with|generated[- ]by)[^\n]*(claude|anthropic|codex|openai|gemini|copilot|cursor|aider|opencode)`)
	agentAuthor    = regexp.MustCompile(`(?i)(claude|anthropic|codex|openai|gemini|copilot|cursor|aider|opencode|\[bot\])`)
	blameLine      = regexp.MustCompile(`^([0-9a-f]{40}) \d+ (\d+)`)
	resolveStaged  = regexp.MustCompile(`^/tmp/lectern-resolve-[0-9]+-[0-9]+$`)
)

// refused is the script's Refused.
type refused struct{ msg string }

func (e *refused) Error() string { return e.msg }

func refuse(format string, args ...any) error { return &refused{fmt.Sprintf(format, args...)} }

// pyB64decodeLoose is base64.b64decode(s): characters outside the alphabet
// are discarded.
func pyB64decodeLoose(s string) ([]byte, error) {
	var b strings.Builder
	for _, c := range s {
		if c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '+' || c == '/' || c == '=' {
			b.WriteRune(c)
		}
	}
	clean := b.String()
	if data, err := base64.StdEncoding.DecodeString(clean); err == nil {
		return data, nil
	}
	data, err := base64.RawStdEncoding.DecodeString(strings.TrimRight(clean, "="))
	if err != nil || len(strings.TrimRight(clean, "="))%4 != 0 && !strings.HasSuffix(clean, "=") {
		return nil, errors.New("Incorrect padding")
	}
	return data, nil
}

type reviewRepo struct {
	workspace string
	params    *pyObj
	g         reviewGit
}

func reviewGitHelper(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	if len(args) < 2 {
		return pyUncaught(stderr, "IndexError", errors.New("list index out of range"))
	}
	r := &reviewRepo{workspace: pyRealpath(args[0]), params: newObj()}
	action := args[1]
	if len(args) > 2 {
		raw, err := pyB64decodeLoose(args[2])
		if err != nil {
			return pyUncaught(stderr, "binascii.Error", err)
		}
		text, err := pyDecodeStrict(raw)
		if err != nil {
			return pyUncaught(stderr, "UnicodeDecodeError", err)
		}
		if text == "" {
			text = "{}"
		}
		v, err := pyLoads(text)
		if err != nil {
			return pyUncaught(stderr, "json.decoder.JSONDecodeError", err)
		}
		o, ok := v.(*pyObj)
		if !ok {
			return pyUncaught(stderr, "AttributeError", fmt.Errorf("'%s' object has no attribute 'get'", pyTypeName(v)))
		}
		r.params = o
	}
	if err := os.Chdir(r.workspace); err != nil {
		return pyUncaught(stderr, pyErrKind(err), pyErr(err, r.workspace))
	}
	r.g = newReviewGit(30, 4096, func(msg string) error { return &refused{msg} })
	actions := map[string]func() (*pyObj, error){
		"status": r.status, "stage": r.stage, "unstage": r.unstage, "discard": r.discard, "hunk": r.hunk,
		"conflicts": r.conflicts, "resolve": r.resolve, "abort": r.abort, "blob": r.blob, "blame": r.blame,
	}
	var out *pyObj
	var err error
	if fn, ok := actions[action]; ok {
		out, err = fn()
	} else {
		err = refuse("Unknown action")
	}
	var unc *pyUncaughtError
	if errors.As(err, &unc) {
		return pyUncaught(stderr, unc.kind, unc.err)
	}
	if err != nil {
		fmt.Fprintln(stdout, pyDumps(newObj("error", err.Error())))
		return 1
	}
	fmt.Fprintln(stdout, pyDumps(out))
	return 0
}

// pyUncaughtError is an exception the script did not catch (a TypeError on a
// malformed parameter): a traceback and exit status 1, nothing on stdout.
type pyUncaughtError struct {
	kind string
	err  error
}

func (e *pyUncaughtError) Error() string { return e.err.Error() }

func typeError(format string, args ...any) error {
	return &pyUncaughtError{"TypeError", fmt.Errorf(format, args...)}
}

func (r *reviewRepo) git(args []string, limit int, allowed []int, stdin []byte) ([]byte, bool, error) {
	return r.g.run(args, limit, allowed, stdin)
}

func (r *reviewRepo) gitOK(args ...string) error {
	_, _, err := r.git(args, reviewLimit, []int{0}, nil)
	return err
}

// out is text(git(args)[0]).strip().
func (r *reviewRepo) out(args []string, allowed ...int) (string, error) {
	if len(allowed) == 0 {
		allowed = []int{0}
	}
	data, _, err := r.git(args, reviewLimit, allowed, nil)
	if err != nil {
		return "", err
	}
	return pyStrip(pyDecodeReplace(data)), nil
}

func (r *reviewRepo) toplevel() (string, error) {
	top, err := r.out([]string{"rev-parse", "--show-toplevel"})
	return pyRealpath(top), err
}

// safePath is a repository-relative path inside the workspace, or refused.
func (r *reviewRepo) safePath(v any) (string, error) {
	rel, ok := v.(string)
	if !ok || rel == "" || strings.Contains(rel, "\x00") {
		return "", refuse("A file path is required")
	}
	full := pyNormpath(pyJoin(r.workspace, rel))
	if !pyInside(full, r.workspace) || full == r.workspace {
		return "", refuse("That path is outside this workspace")
	}
	if !pyInside(pyRealpath(pyDirname(full)), r.workspace) {
		return "", refuse("That path is outside this workspace")
	}
	return pyRelpath(full, r.workspace), nil
}

func (r *reviewRepo) gitPath(name string) (string, error) {
	p, err := r.out([]string{"rev-parse", "--git-path", name})
	return pyJoin(r.workspace, p), err
}

func hunkFingerprint(hunk string) string {
	sum := sha1.Sum([]byte(hunk))
	return hex.EncodeToString(sum[:])[:16]
}

// splitHunks is (header, hunks) for a single-file patch.
func splitHunks(patch string) (string, []string) {
	var header strings.Builder
	var hunks []*strings.Builder
	for _, line := range pySplitLines(patch, true) {
		switch {
		case strings.HasPrefix(line, "@@"):
			cur := &strings.Builder{}
			cur.WriteString(line)
			hunks = append(hunks, cur)
		case len(hunks) == 0:
			header.WriteString(line)
		default:
			hunks[len(hunks)-1].WriteString(line)
		}
	}
	out := make([]string, len(hunks))
	for i, h := range hunks {
		out[i] = h.String()
	}
	return header.String(), out
}

func (r *reviewRepo) statusEntries() ([]*pyObj, error) {
	raw, cut, err := r.git([]string{"status", "--porcelain=v1", "-z", "--untracked-files=all", "--", "."}, 2*reviewLimit, []int{0}, nil)
	if err != nil {
		return nil, err
	}
	if cut {
		return nil, refuse("Too many changed paths to review")
	}
	top, err := r.toplevel()
	if err != nil {
		return nil, err
	}
	var files []*pyObj
	for _, rec := range porcelainRecords(raw, false) {
		xy := []rune(rec.xy)
		if len(xy) < 2 {
			return nil, &pyUncaughtError{"IndexError", errors.New("string index out of range")}
		}
		full := pyNormpath(pyJoin(top, rec.name))
		if !pyInside(full, r.workspace) {
			continue
		}
		var old any
		if rec.old != nil {
			old = *rec.old
		}
		conflicted := false
		for _, c := range []string{"DD", "AU", "UD", "UA", "DU", "AA", "UU"} {
			conflicted = conflicted || rec.xy == c
		}
		untracked := rec.xy == "??"
		files = append(files, newObj("path", pyRelpath(full, r.workspace), "status", rec.xy, "previous_path", old,
			"conflicted", conflicted, "untracked", untracked, "new_file", untracked || rec.xy == " A",
			"staged", !conflicted && xy[0] != ' ' && xy[0] != '?',
			"unstaged", !conflicted && (xy[1] != ' ' || untracked)))
	}
	sort.SliceStable(files, func(i, j int) bool {
		return pyCasefold(files[i].Str("path")) < pyCasefold(files[j].Str("path"))
	})
	return files, nil
}

// filePatch is the patch for one file in one scope ("staged" or "unstaged").
func (r *reviewRepo) filePatch(entry *pyObj, scope string) (string, bool, error) {
	rel := entry.Str("path")
	args := []string{"diff", "--no-ext-diff", "--no-textconv", "--no-color", "--unified=3"}
	if scope == "unstaged" && pyTruthy(entry.Val("untracked")) {
		full := pyJoin(r.workspace, rel)
		info, err := os.Lstat(full)
		if err != nil {
			return "", false, pyErr(err, full)
		}
		if !info.Mode().IsRegular() || pyRealpath(full) != full {
			return "Untracked link or special file: content preview unavailable.\n", false, nil
		}
		data, cut, err := r.git(append(args, "--no-index", "--", "/dev/null", rel), reviewLimit, []int{0, 1}, nil)
		return pyDecodeReplace(data), cut, err
	}
	if scope == "staged" {
		args = append(args, "--cached")
	}
	paths := []string{rel}
	if prev := entry.Val("previous_path"); pyTruthy(prev) && scope == "staged" {
		paths = append(paths, prev.(string))
	}
	data, cut, err := r.git(append(append(args, "--"), paths...), reviewLimit, []int{0}, nil)
	return pyDecodeReplace(data), cut, err
}

func (r *reviewRepo) hooks() []any {
	found := []any{}
	base, err := r.gitPath("hooks")
	if err != nil {
		return found
	}
	for _, name := range []string{"pre-commit", "prepare-commit-msg", "commit-msg", "post-commit", "pre-push"} {
		p := pyJoin(base, name)
		if pyIsFile(p) && pyAccessX(p) {
			found = append(found, name)
		}
	}
	return found
}

func (r *reviewRepo) operation() (string, error) {
	for _, m := range [][2]string{{"MERGE_HEAD", "merge"}, {"rebase-merge", "rebase"}, {"rebase-apply", "rebase"},
		{"CHERRY_PICK_HEAD", "cherry-pick"}, {"REVERT_HEAD", "revert"}} {
		p, err := r.gitPath(m[0])
		if err != nil {
			return "", err
		}
		if pyExists(p) {
			return m[1], nil
		}
	}
	return "", nil
}

func (r *reviewRepo) branchState() (*pyObj, error) {
	branch, err := r.out([]string{"symbolic-ref", "--short", "-q", "HEAD"}, 0, 1)
	if err != nil {
		return nil, err
	}
	head, err := r.out([]string{"rev-parse", "-q", "--verify", "HEAD"}, 0, 1)
	if err != nil {
		return nil, err
	}
	upstream, err := r.out([]string{"rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}"}, 0, 128)
	if err != nil {
		return nil, err
	}
	remoteSHA := ""
	if branch != "" {
		if remoteSHA, err = r.out([]string{"rev-parse", "-q", "--verify", "refs/remotes/origin/" + branch}, 0, 1); err != nil {
			return nil, err
		}
	}
	var ahead, behind any = 0, 0
	if upstream != "" && head != "" {
		counts, err := r.out([]string{"rev-list", "--left-right", "--count", "HEAD...@{u}"}, 0, 128)
		if err != nil {
			return nil, err
		}
		if f := strings.FieldsFunc(counts, pyIsSpace); len(f) == 2 {
			a, errA := pyInt(f[0])
			b, errB := pyInt(f[1])
			if errA != nil {
				return nil, errA
			}
			if errB != nil {
				return nil, errB
			}
			ahead, behind = a, b
		}
	}
	pushed := false
	message := ""
	if head != "" {
		contains, err := r.out([]string{"branch", "-r", "--contains", "HEAD"}, 0, 129)
		if err != nil {
			return nil, err
		}
		pushed = contains != ""
		if message, err = r.out([]string{"log", "-1", "--format=%B", "HEAD"}, 0, 128); err != nil {
			return nil, err
		}
	}
	mergeMsg := ""
	msgPath, err := r.gitPath("MERGE_MSG")
	if err != nil {
		return nil, err
	}
	if data, err := os.ReadFile(msgPath); err == nil {
		var kept []string
		for _, l := range pySplitLines(pyUniversalNewlines(pyDecodeReplace(data)), false) {
			if !strings.HasPrefix(l, "#") {
				kept = append(kept, l)
			}
		}
		mergeMsg = pyStrip(strings.Join(kept, "\n"))
	}
	op, err := r.operation()
	if err != nil {
		return nil, err
	}
	return newObj("branch", branch, "head", head, "upstream", upstream, "remote_sha", remoteSHA, "ahead", ahead,
		"behind", behind, "head_pushed", pushed, "head_message", message, "operation", op,
		"merge_message", mergeMsg, "hooks", r.hooks()), nil
}

func (r *reviewRepo) status() (*pyObj, error) {
	files, err := r.statusEntries()
	if err != nil {
		return nil, err
	}
	budget := map[string]int{"staged": reviewLimit, "unstaged": reviewLimit}
	truncated := false
	for _, f := range files {
		for _, scope := range []string{"staged", "unstaged"} {
			f.Set(scope+"_patch", "")
			f.Set(scope+"_hunks", []any{})
			if !pyTruthy(f.Val(scope)) || len(files) > 400 {
				continue
			}
			if budget[scope] <= 0 {
				truncated = true
				continue
			}
			patch, cut, err := r.filePatch(f, scope)
			if err != nil {
				return nil, err
			}
			budget[scope] -= pyLen(patch)
			truncated = truncated || cut
			f.Set(scope+"_patch", patch)
			// The client names a hunk by index and this fingerprint; the hunk
			// action recomputes both before applying anything.
			_, hunks := splitHunks(patch)
			prints := []any{}
			for _, h := range hunks {
				prints = append(prints, hunkFingerprint(h))
			}
			f.Set(scope+"_hunks", prints)
		}
	}
	state, err := r.branchState()
	if err != nil {
		return nil, err
	}
	state.Set("files", objList(files))
	state.Set("truncated", truncated)
	return state, nil
}

func objList(list []*pyObj) []any {
	out := []any{}
	for _, o := range list {
		out = append(out, o)
	}
	return out
}

func (r *reviewRepo) entryFor(v any) (*pyObj, error) {
	rel, err := r.safePath(v)
	if err != nil {
		return nil, err
	}
	files, err := r.statusEntries()
	if err != nil {
		return nil, err
	}
	for _, f := range files {
		if f.Str("path") == rel {
			return f, nil
		}
	}
	return nil, refuse("This file has no changes any more; refresh the list")
}

func (r *reviewRepo) pathsParam() ([]string, error) {
	v := r.params.Val("paths")
	var list []any
	if pyTruthy(v) {
		var err error
		if list, err = pyIter(v); err != nil {
			return nil, &pyUncaughtError{"TypeError", err}
		}
	}
	if len(list) == 0 || len(list) > 2000 {
		return nil, refuse("Choose one or more files")
	}
	var out []string
	for _, p := range list {
		rel, err := r.safePath(p)
		if err != nil {
			return nil, err
		}
		out = append(out, rel)
	}
	return out, nil
}

func (r *reviewRepo) stage() (*pyObj, error) {
	paths, err := r.pathsParam()
	if err != nil {
		return nil, err
	}
	if err := r.gitOK(append([]string{"add", "-A", "--"}, paths...)...); err != nil {
		return nil, err
	}
	return newObj("ok", true), nil
}

func (r *reviewRepo) unstage() (*pyObj, error) {
	paths, err := r.pathsParam()
	if err != nil {
		return nil, err
	}
	head, err := r.out([]string{"rev-parse", "-q", "--verify", "HEAD"}, 0, 1)
	if err != nil {
		return nil, err
	}
	if head != "" {
		err = r.gitOK(append([]string{"reset", "-q", "HEAD", "--"}, paths...)...)
	} else {
		err = r.gitOK(append([]string{"rm", "-q", "--cached", "-r", "--"}, paths...)...)
	}
	if err != nil {
		return nil, err
	}
	return newObj("ok", true), nil
}

func (r *reviewRepo) discard() (*pyObj, error) {
	paths, err := r.pathsParam()
	if err != nil {
		return nil, err
	}
	discarded := []any{}
	for _, rel := range paths {
		f, err := r.entryFor(rel)
		if err != nil {
			return nil, err
		}
		if pyTruthy(f.Val("conflicted")) {
			return nil, refuse("%s has a merge conflict; resolve it instead", rel)
		}
		full := pyJoin(r.workspace, rel)
		status := []rune(f.Str("status"))
		switch {
		case pyTruthy(f.Val("new_file")) && !pyTruthy(f.Val("staged")):
			if f.Str("status") == " A" {
				if err := r.gitOK("rm", "-q", "--cached", "--", rel); err != nil {
					return nil, err
				}
			}
			info, err := os.Lstat(full)
			if err != nil {
				return nil, pyErr(err, full)
			}
			if info.IsDir() {
				return nil, refuse("%s is a directory", rel)
			}
			if err := os.Remove(full); err != nil {
				return nil, pyErr(err, full)
			}
		case status[1] == 'D' || pyTruthy(f.Val("unstaged")):
			if err := r.gitOK("checkout", "-q", "--", rel); err != nil {
				return nil, err
			}
		}
		discarded = append(discarded, rel)
	}
	return newObj("ok", true, "discarded", discarded), nil
}

// pickLines reduces a hunk to the chosen body lines (indices after the @@
// line). Applied forward, an unchosen addition is dropped and an unchosen
// removal becomes context; applied in reverse (-R) it is the mirror image.
// This is how `git add -p`'s line editing builds its patches.
func pickLines(hunk string, chosen []any, reverse bool) (string, error) {
	lines := pySplitLines(hunk, true)
	body := lines[1:]
	picked := map[int64]bool{}
	for _, v := range chosen {
		i, ok := pyIntValue(v)
		if !ok || i < 0 || i >= int64(len(body)) || !strings.ContainsAny(body[i][:1], "+-") {
			return "", refuse("Choose added or removed lines in this hunk")
		}
		picked[i] = true
	}
	keep := "+"
	if reverse {
		keep = "-"
	}
	out := []string{lines[0]}
	dropped := false
	for i, line := range body {
		tag := line[:1]
		if tag == `\` {
			if !dropped {
				out = append(out, line)
			}
			continue
		}
		dropped = false
		if (tag == "+" || tag == "-") && !picked[int64(i)] {
			if tag == keep {
				dropped = true
				continue
			}
			line = " " + line[1:]
		}
		out = append(out, line)
	}
	return strings.Join(out, ""), nil
}

// asModification rewrites a new file's patch header as a change to an
// existing (empty) file, so part of it can be applied: git only applies a
// new-file patch whole.
func asModification(header, rel string) string {
	var out strings.Builder
	for _, line := range pySplitLines(header, true) {
		if strings.HasPrefix(line, "new file mode") || strings.HasPrefix(line, "index ") || strings.HasPrefix(line, "deleted file mode") {
			continue
		}
		if strings.HasPrefix(line, "--- /dev/null") {
			line = "--- a/" + rel + "\n"
		}
		out.WriteString(line)
	}
	return out.String()
}

// hunk stages, unstages or discards one hunk, or chosen lines of it. The
// hunk is found again by index and checked against the fingerprint the
// client saw, so a stale click never applies a different change.
func (r *reviewRepo) hunk() (*pyObj, error) {
	op, _ := r.params.Val("op").(string)
	scope := map[string]string{"stage": "unstaged", "discard": "unstaged", "unstage": "staged"}[op]
	if scope == "" {
		return nil, refuse("Choose stage, unstage or discard")
	}
	f, err := r.entryFor(r.params.Val("path"))
	if err != nil {
		return nil, err
	}
	if pyTruthy(f.Val("conflicted")) {
		return nil, refuse("This file has a merge conflict; resolve it instead")
	}
	patch, cut, err := r.filePatch(f, scope)
	if err != nil {
		return nil, err
	}
	if cut {
		return nil, refuse("This diff is too large to stage by hunk")
	}
	header, hunks := splitHunks(patch)
	index, ok := pyIntValue(r.params.Val("index"))
	if !ok || index < 0 || index >= int64(len(hunks)) {
		return nil, refuse("That hunk is no longer in the diff; refresh the list")
	}
	hunk := hunks[index]
	if !pyEqual(r.params.Val("fingerprint"), hunkFingerprint(hunk)) {
		return nil, refuse("The file changed since this diff was shown; refresh and try again")
	}
	if strings.Contains(header, "Binary files") || strings.Contains(header, "GIT binary patch") {
		return nil, refuse("Binary changes are staged as a whole file")
	}
	reverse := op == "unstage" || op == "discard"
	if chosen, ok := r.params.Get("lines"); ok && chosen != nil {
		list, isList := chosen.([]any)
		if !isList || len(list) == 0 {
			return nil, refuse("Choose at least one line")
		}
		for _, v := range list {
			if _, unhashable := v.(*pyObj); unhashable {
				return nil, typeError("unhashable type: 'dict'")
			}
			if _, unhashable := v.([]any); unhashable {
				return nil, typeError("unhashable type: 'list'")
			}
		}
		if hunk, err = pickLines(hunk, list, reverse); err != nil {
			return nil, err
		}
	}
	rel := f.Str("path")
	newFile := strings.Contains(header, "new file mode") || strings.Contains(header, "--- /dev/null")
	if newFile {
		header = asModification(header, rel)
	}
	args := []string{"apply", "--whitespace=nowarn", "--recount"}
	if op == "stage" || op == "unstage" {
		args = append(args, "--cached")
	}
	if reverse {
		args = append(args, "-R")
	}
	restore := ""
	restoring := false
	if newFile && op == "stage" {
		// Give the index an empty entry for the file to apply against, and
		// put the old entry (none, or intent-to-add) back if that fails.
		mode := "100644"
		if pyAccessX(pyJoin(r.workspace, rel)) {
			mode = "100755"
		}
		restore, restoring = f.Str("status"), true
		data, _, err := r.git([]string{"hash-object", "-w", "--stdin"}, reviewLimit, []int{0}, []byte{})
		if err != nil {
			return nil, err
		}
		empty := pyStrip(pyDecodeReplace(data))
		if empty == "" {
			empty = emptyBlob
		}
		if err := r.gitOK("update-index", "--add", "--cacheinfo", mode+","+empty+","+rel); err != nil {
			return nil, err
		}
	}
	if _, _, err := r.git(append(args, "-"), reviewLimit, []int{0}, []byte(header+hunk)); err != nil {
		var ref *refused
		if errors.As(err, &ref) && restoring {
			if err := r.gitOK("rm", "-q", "-f", "--cached", "--", rel); err != nil {
				return nil, err
			}
			if restore == " A" {
				if err := r.gitOK("add", "-N", "--", rel); err != nil {
					return nil, err
				}
			}
		}
		return nil, err
	}
	full := pyJoin(r.workspace, rel)
	if op == "discard" && pyTruthy(f.Val("new_file")) && !pyTruthy(f.Val("staged")) && pyIsFile(full) && !pyIsLink(full) {
		info, err := os.Stat(full)
		if err != nil {
			return nil, pyErr(err, full)
		}
		if info.Size() == 0 {
			// Every line of a file that never existed is gone: so is the file.
			if f.Str("status") == " A" {
				if err := r.gitOK("rm", "-q", "-f", "--cached", "--", rel); err != nil {
					return nil, err
				}
			}
			if err := os.Remove(full); err != nil {
				return nil, pyErr(err, full)
			}
		}
	}
	return newObj("ok", true), nil
}

// ---- conflicts ------------------------------------------------------------

func (r *reviewRepo) stageBlob(rel string, n int) ([]byte, error) {
	data, cut, err := r.git([]string{"show", fmt.Sprintf(":%d:%s", n, rel)}, reviewBlobLimit, []int{0, 128}, nil)
	if err != nil || cut {
		return nil, err
	}
	if data == nil {
		data = []byte{}
	}
	return data, nil
}

func isBinaryBlob(data []byte) bool {
	return data != nil && strings.Contains(string(data[:min(len(data), 8000)]), "\x00")
}

func (r *reviewRepo) conflicts() (*pyObj, error) {
	raw, _, err := r.git([]string{"ls-files", "-u", "-z", "--", "."}, reviewLimit, []int{0}, nil)
	if err != nil {
		return nil, err
	}
	var names []string
	stages := map[string]map[int64]bool{}
	for _, item := range strings.Split(string(raw), "\x00") {
		if item == "" {
			continue
		}
		meta, name, _ := strings.Cut(item, "\t")
		fields := strings.Fields(meta)
		if len(fields) < 3 {
			return nil, &pyUncaughtError{"IndexError", errors.New("list index out of range")}
		}
		n, err := pyInt(fields[2])
		if err != nil {
			return nil, err
		}
		if stages[name] == nil {
			stages[name] = map[int64]bool{}
			names = append(names, name)
		}
		stages[name][n] = true
	}
	top, err := r.toplevel()
	if err != nil {
		return nil, err
	}
	sort.SliceStable(names, func(i, j int) bool { return pyCasefold(names[i]) < pyCasefold(names[j]) })
	var files []*pyObj
	for _, name := range names {
		full := pyNormpath(pyJoin(top, name))
		if !pyInside(full, r.workspace) {
			continue
		}
		s := stages[name]
		files = append(files, newObj("path", pyRelpath(full, r.workspace), "base", s[1], "ours", s[2], "theirs", s[3]))
	}
	result, err := r.branchState()
	if err != nil {
		return nil, err
	}
	result.Set("files", objList(files))
	want := r.params.Val("path")
	if !pyTruthy(want) {
		return result, nil
	}
	rel, err := r.safePath(want)
	if err != nil {
		return nil, err
	}
	var match *pyObj
	for _, f := range files {
		if f.Str("path") == rel {
			match = f
			break
		}
	}
	if match == nil {
		return nil, refuse("That file is no longer in conflict; refresh the list")
	}
	keys := []string{"base", "ours", "theirs"}
	blobs := map[string][]byte{}
	for i, k := range keys {
		if match.Val(k) == true {
			if blobs[k], err = r.stageBlob(rel, i+1); err != nil {
				return nil, err
			}
		}
	}
	full := pyJoin(r.workspace, rel)
	var working []byte
	if pyIsFile(full) && !pyIsLink(full) {
		if working, err = readCapped(full, reviewBlobLimit+1); err != nil {
			return nil, err
		}
	}
	binary := isBinaryBlob(working)
	for _, k := range keys {
		binary = binary || isBinaryBlob(blobs[k])
	}
	detail := newObj("path", rel, "binary", binary)
	for _, k := range keys {
		detail.Set(k, blobs[k] != nil)
	}
	if !binary {
		all := true
		for _, k := range keys {
			detail.Set(k+"_text", pyDecodeReplace(blobs[k]))
			all = all && blobs[k] != nil
		}
		wt := ""
		if working != nil && len(working) <= reviewBlobLimit {
			wt = pyDecodeReplace(working)
		}
		detail.Set("working_text", wt)
		if all {
			// A diff3-style rendering of the pristine conflict, so every region
			// carries its common ancestor even when the checkout wrote plain
			// two-way markers.
			merged, err := r.mergedText(blobs)
			if err != nil {
				return nil, err
			}
			detail.Set("merged_text", merged)
		}
	}
	result.Set("file", detail)
	return result, nil
}

func (r *reviewRepo) mergedText(blobs map[string][]byte) (string, error) {
	dir, err := os.MkdirTemp("", "tmp")
	if err != nil {
		return "", pyErr(err)
	}
	defer os.RemoveAll(dir)
	var names []string
	for _, k := range []string{"ours", "base", "theirs"} {
		p := pyJoin(dir, k)
		names = append(names, p)
		if err := os.WriteFile(p, blobs[k], 0o644); err != nil {
			return "", pyErr(err, p)
		}
	}
	theirs := "incoming"
	mergeHead, err := r.gitPath("MERGE_HEAD")
	if err != nil {
		return "", err
	}
	if pyExists(mergeHead) {
		name, err := r.out([]string{"name-rev", "--name-only", "--always", "MERGE_HEAD"}, 0, 128)
		if err != nil {
			return "", err
		}
		if name != "" {
			theirs = name
		}
	}
	state, err := r.branchState()
	if err != nil {
		return "", err
	}
	ours := state.Str("branch")
	if ours == "" {
		ours = "HEAD"
	}
	allowed := make([]int, 128)
	for i := range allowed {
		allowed[i] = i
	}
	merged, _, err := r.git(append([]string{"merge-file", "-p", "--diff3", "-L", ours, "-L", "base", "-L", theirs}, names...),
		reviewBlobLimit, allowed, nil)
	return pyDecodeReplace(merged), err
}

// readCapped is open(p, 'rb').read(n).
func readCapped(p string, n int) ([]byte, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, pyErr(err, p)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, int64(n)))
	if err != nil {
		return nil, pyErr(err)
	}
	return data, nil
}

func (r *reviewRepo) resolve() (*pyObj, error) {
	rel, err := r.safePath(r.params.Val("path"))
	if err != nil {
		return nil, err
	}
	conflicts, err := r.conflicts()
	if err != nil {
		return nil, err
	}
	found := false
	for _, f := range conflicts.Val("files").([]any) {
		found = found || f.(*pyObj).Str("path") == rel
	}
	if !found {
		return nil, refuse("That file is no longer in conflict; refresh the list")
	}
	full := pyJoin(r.workspace, rel)
	take := r.params.Val("take")
	if take == "ours" || take == "theirs" {
		n := 3
		if take == "ours" {
			n = 2
		}
		blob, err := r.stageBlob(rel, n)
		if err != nil {
			return nil, err
		}
		if blob == nil {
			err = r.gitOK("rm", "-q", "--", rel)
		} else if err = r.gitOK("checkout", "--"+take.(string), "--", rel); err == nil {
			err = r.gitOK("add", "--", rel)
		}
		if err != nil {
			return nil, err
		}
		return newObj("ok", true, "path", rel), nil
	}
	staged := r.params.Val("staged_file")
	if !pyTruthy(staged) {
		staged = ""
	}
	stagedPath, ok := staged.(string)
	if !ok {
		return nil, typeError("expected string or bytes-like object, got '%s'", pyTypeName(staged))
	}
	if !resolveStaged.MatchString(stagedPath) {
		return nil, refuse("No resolved content was supplied")
	}
	content, err := readCapped(stagedPath, reviewBlobLimit+1)
	if err != nil {
		return nil, err
	}
	if err := os.Remove(stagedPath); err != nil {
		return nil, pyErr(err, stagedPath)
	}
	if len(content) > reviewBlobLimit {
		return nil, refuse("The resolved file is too large")
	}
	if !pyTruthy(r.params.Val("allow_markers")) && conflictMarker.MatchString(pyDecodeReplace(content)) {
		return nil, refuse("The result still contains conflict markers")
	}
	mode := uint32(0o644)
	if info, err := os.Lstat(full); err == nil {
		if !info.Mode().IsRegular() {
			return nil, refuse("Only a regular file can be resolved here")
		}
		mode = pyImode(info.Mode())
	}
	f, err := os.OpenFile(full, os.O_WRONLY|os.O_CREATE|os.O_TRUNC|oNoFollow, os.FileMode(mode&0o777))
	if err != nil {
		return nil, pyErr(err, full)
	}
	_, werr := f.Write(content)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return nil, pyErr(werr)
	}
	if err := r.gitOK("add", "--", rel); err != nil {
		return nil, err
	}
	return newObj("ok", true, "path", rel), nil
}

func (r *reviewRepo) abort() (*pyObj, error) {
	op, err := r.operation()
	if err != nil {
		return nil, err
	}
	if op == "" {
		return nil, refuse("No merge or rebase is in progress")
	}
	// Intent-to-add entries (the live diff marks new files that way) make git
	// refuse to abort; drop just those marks, never the files.
	files, err := r.statusEntries()
	if err != nil {
		return nil, err
	}
	var ita []string
	for _, f := range files {
		if f.Str("status") == " A" {
			ita = append(ita, f.Str("path"))
		}
	}
	if len(ita) > 0 {
		if err := r.gitOK(append([]string{"rm", "-q", "--cached", "--"}, ita...)...); err != nil {
			return nil, err
		}
	}
	if err := r.gitOK(op, "--abort"); err != nil {
		return nil, err
	}
	return newObj("ok", true, "aborted", op), nil
}

// ---- blobs and blame ------------------------------------------------------

func (r *reviewRepo) blob() (*pyObj, error) {
	rel, err := r.safePath(r.params.Val("path"))
	if err != nil {
		return nil, err
	}
	var data []byte
	var cut bool
	if r.params.Val("side") == "old" {
		ref := "HEAD"
		if v := r.params.Val("ref"); pyTruthy(v) {
			ref = pyStr(v)
		}
		if data, cut, err = r.git([]string{"show", ref + ":" + rel}, reviewBlobLimit, []int{0, 128}, nil); err != nil {
			return nil, err
		}
		if len(data) == 0 && !cut {
			return newObj("missing", true), nil
		}
	} else {
		full := pyJoin(r.workspace, rel)
		if !pyIsFile(full) || pyIsLink(full) {
			return newObj("missing", true), nil
		}
		if data, err = readCapped(full, reviewBlobLimit+1); err != nil {
			return nil, err
		}
		cut = len(data) > reviewBlobLimit
	}
	if cut {
		return newObj("too_large", true), nil
	}
	return newObj("data", base64.StdEncoding.EncodeToString(data)), nil
}

// blame says which commit last touched each requested line, and which
// commits since the base look agent-made (an agent co-author trailer or an
// agent author).
func (r *reviewRepo) blame() (*pyObj, error) {
	agentCommits := []any{}
	if base := r.params.Val("base"); pyTruthy(base) {
		baseText, ok := base.(string)
		if !ok {
			return nil, typeError("unsupported operand type(s) for +: '%s' and 'str'", pyTypeName(base))
		}
		raw, err := r.out([]string{"log", "--format=%H%x00%an%x00%ae%x00%B%x1e", baseText + "..HEAD"}, 0, 128)
		if err != nil {
			return nil, err
		}
		for _, rec := range strings.Split(raw, "\x1e") {
			parts := strings.Split(strings.Trim(rec, "\n"), "\x00")
			if len(parts) < 4 {
				continue
			}
			if agentTrailer.MatchString(parts[3]) || agentAuthor.MatchString(parts[1]+" "+parts[2]) {
				agentCommits = append(agentCommits, pyStrip(parts[0]))
			}
		}
	}
	files, hashes := newObj(), newObj()
	requested := newObj()
	if v := r.params.Val("files"); pyTruthy(v) {
		o, ok := v.(*pyObj)
		if !ok {
			return nil, &pyUncaughtError{"AttributeError", fmt.Errorf("'%s' object has no attribute 'items'", pyTypeName(v))}
		}
		requested = o
	}
	keys := requested.Keys()
	if len(keys) > 300 {
		keys = keys[:300]
	}
	for _, key := range keys {
		ranges := requested.Val(key)
		rel, err := r.safePath(key)
		if err != nil {
			return nil, err
		}
		full := pyJoin(r.workspace, rel)
		if !pyIsFile(full) || pyIsLink(full) {
			hashes.Set(rel, []any{})
			continue
		}
		if pyTruthy(r.params.Val("hashes")) {
			// Every line's identity, as review_attribution.go's lineHash
			// computes it, so stale agent marks for this file can be dropped.
			data, err := readCapped(full, reviewBlobLimit)
			if err != nil {
				return nil, err
			}
			seen := map[string]bool{}
			for _, line := range strings.Split(string(data), "\n") {
				line = strings.TrimRight(line, " \t\r")
				if strings.Trim(line, " \t\n\r\v\f") != "" {
					sum := sha1.Sum([]byte(line))
					seen[hex.EncodeToString(sum[:])[:16]] = true
				}
			}
			list := make([]string, 0, len(seen))
			for h := range seen {
				list = append(list, h)
			}
			sort.Strings(list)
			hashes.Set(rel, list)
		}
		if !pyTruthy(ranges) {
			continue
		}
		spans, err := pyIter(ranges)
		if err != nil {
			return nil, &pyUncaughtError{"TypeError", err}
		}
		if len(spans) > 200 {
			spans = spans[:200]
		}
		args := []string{"blame", "--porcelain"}
		for _, span := range spans {
			pair, err := pyIter(span)
			if err != nil || len(pair) != 2 {
				return nil, typeError("cannot unpack a line range")
			}
			a, errA := pyInt(pair[0])
			b, errB := pyInt(pair[1])
			if _, isStr := pair[0].(string); isStr || errA != nil {
				return nil, typeError("%%d format: a real number is required, not %s", pyTypeName(pair[0]))
			}
			if _, isStr := pair[1].(string); isStr || errB != nil {
				return nil, typeError("%%d format: a real number is required, not %s", pyTypeName(pair[1]))
			}
			args = append(args, "-L", fmt.Sprintf("%d,%d", a, b))
		}
		raw, _, err := r.git(append(args, "--", rel), reviewLimit, []int{0, 128}, nil)
		if err != nil {
			return nil, err
		}
		lines := newObj()
		for _, line := range pySplitLines(pyDecodeReplace(raw), false) {
			if m := blameLine.FindStringSubmatch(line); m != nil {
				lines.Set(m[2], m[1])
			}
		}
		files.Set(rel, lines)
	}
	return newObj("agent_commits", agentCommits, "files", files, "hashes", hashes, "zero", reviewZero), nil
}
