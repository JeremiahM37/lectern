//go:build unix

package helpers

// Port of internal/worktree/interactive.py: one owned Git worktree, created,
// recovered or removed. Never reset, force-remove, or delete branches.
//
// argv (as the script's): OPERATION PLAN_JSON [LOCK_FD|- [TIMEOUT [OWNER_JSON]]].
// LOCK_FD is a group workspace's lock inherited from worktree-group, which
// also passes the group plan as OWNER_JSON. The script's two inline monitors
// are the helpers worktree-monitor and worktree-setup below; worktree-gated
// is worktree-group's start gate in front of this helper.

import (
	"bytes"
	"errors"
	"io"
	"os"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func init() {
	Register("worktree", func(args []string, _ io.Reader, stdout, stderr io.Writer) int {
		return worktreeMain(args, stdout, stderr)
	})
	Register("worktree-gated", worktreeGated)
	Register("worktree-monitor", worktreeMonitor)
	Register("worktree-setup", worktreeSetupMonitor)
}

var wtCommit = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)

const wtTimedOut = "Workspace operation timed out; allocation retained for inspection"

type wtSingle struct {
	p          *pyObj
	args       []string
	inherited  *os.File // the inherited lock, when run under a group
	inheritFD  int
	gitTimeout float64
	deadline   time.Time
	control    *setupControl
	// recovery holds the lease a recovery takes, for the process lifetime.
	recovery *os.File
	stdout   io.Writer
}

func worktreeMain(args []string, stdout, stderr io.Writer) int {
	if len(args) < 2 {
		return wtUncaught(stderr, errors.New("not enough values to unpack"))
	}
	w := &wtSingle{args: args, gitTimeout: 90, stdout: stdout}
	// Descriptors held here (the inherited lock, a recovery lease) keep
	// their locks until exit, as the script's module globals did.
	defer runtime.KeepAlive(w)
	var planErr error
	if err := wtTry(func() { w.p = wtObj(wtPlan(args[1])) }); err != nil {
		planErr = err
	}
	if planErr != nil {
		return wtUncaught(stderr, planErr)
	}
	if len(args) > 2 && args[2] != "-" {
		fd, err := strconv.Atoi(args[2])
		if err != nil {
			return wtUncaught(stderr, errors.New("invalid literal for int() with base 10: "+pyReprString(args[2])))
		}
		w.inheritFD = fd
		w.inherited = os.NewFile(uintptr(fd), "lock")
	}
	if len(args) > 3 {
		t, err := strconv.ParseFloat(strings.TrimSpace(args[3]), 64)
		if err != nil {
			return wtUncaught(stderr, errors.New("could not convert string to float: "+pyReprString(args[3])))
		}
		w.gitTimeout = t
	}
	w.deadline = time.Now().Add(time.Duration(w.gitTimeout * float64(time.Second)))

	exited := false
	err := wtTry(func() { exited = w.run(args[0]) })
	if err == nil {
		if !exited {
			w.print(newObj("workspace", w.p))
		}
		return 0
	}
	if wtIsLookup(err) {
		return wtUncaught(stderr, err)
	}
	msg := err.Error()
	if wtIsTimeout(err) {
		msg = wtTimedOut
	}
	out := newObj("error", msg)
	if wPyEqual(w.p.Val("state"), "failed") && wPyTruthy(w.p.Val("error")) {
		out.Set("workspace", w.p)
	}
	w.print(out)
	return 1
}

func (w *wtSingle) print(v any) { io.WriteString(w.stdout, pyDumps(v)+"\n") }

func (w *wtSingle) remaining() float64 {
	r := time.Until(w.deadline).Seconds()
	if r <= 0 {
		wtValue(wtTimedOut)
	}
	return r
}

// git runs git -C repo args and returns its stripped output, raising
// ValueError with its message when it fails. With a cancellation record it
// runs supervised; `worktree add` then runs under worktree-monitor, which
// keeps the lease and cancellation watch even if this process dies.
func (w *wtSingle) git(repo string, cancelCheck bool, args ...string) string {
	budget := w.gitTimeout
	if cancelCheck {
		budget = w.remaining()
	}
	command := append([]string{"git", "-C", repo}, args...)
	var out, errText string
	if w.control != nil && cancelCheck {
		wtMust(w.control.check())
		var extra []*os.File
		if w.inherited != nil {
			extra = []*os.File{w.inherited}
		} else if w.control.leaseFile != nil {
			extra = []*os.File{w.control.leaseFile}
		}
		if w.inherited == nil && len(args) >= 2 && args[0] == "worktree" && args[1] == "add" {
			command = append(selfHelper("worktree-monitor", pyDumps(w.p), "3", wPyFloatRepr(budget)), command...)
		}
		child, err := wtStart(command, wtSpawn{extra: extra, setsid: w.inherited == nil})
		wtMust(err)
		group := 0
		if w.inherited != nil {
			group = syscall.Getpgrp()
		}
		o, e, err := w.control.wait(child, budget, group)
		wtMust(err)
		out, errText = o, e
		if child.rc != 0 {
			wtValue(firstNonEmpty(wPyStrip(errText), wPyStrip(out), "Git command failed"))
		}
		return wPyStrip(out)
	}
	var extra []*os.File
	if w.inherited != nil {
		extra = []*os.File{w.inherited}
	}
	res, err := pyRun{Argv: command, Timeout: budget, ExtraFiles: extra}.run()
	wtMust(err)
	o, e, err := decodePair(res)
	wtMust(err)
	if res.RC != 0 {
		wtValue(firstNonEmpty(wPyStrip(e), wPyStrip(o), "Git command failed"))
	}
	return wPyStrip(o)
}

func decodePair(res pyResult) (string, string, error) {
	o, err := wtReadText(res.Stdout)
	if err != nil {
		return "", "", err
	}
	e, err := wtReadText(res.Stderr)
	return o, e, err
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// runSetup runs the plan's setup command under worktree-setup, which owns
// the cancellation lease while the setup shell runs.
func (w *wtSingle) runSetup() {
	if wPyStrip(wtGetString(w.p, "setup_command")) == "" {
		return
	}
	budget := w.remaining()
	w.p.Set("setup_state", "running")
	if w.control != nil && w.inherited == nil {
		w.control.allocation(w.p.Copy())
	}
	owner := any(w.p)
	lease := w.inherited
	if w.inherited != nil {
		owner = wtPlan(w.args[4])
	} else {
		lease = w.control.leaseFile
	}
	argv := selfHelper("worktree-setup", pyDumps(w.p), pyDumps(owner), "3", wPyFloatRepr(budget))
	child, err := wtStart(argv, wtSpawn{extra: []*os.File{lease}, setsid: w.inherited == nil})
	wtMust(err)
	group := 0
	if w.inherited != nil {
		group = syscall.Getpgrp()
	}
	stdout, _, err := w.control.wait(child, budget, group)
	wtMust(err)
	if child.rc != 0 {
		wtValue("Workspace setup did not finish; inspect the retained allocation")
	}
	result := wtObj(wtPlan(stdout))
	w.p.Update(wtObj(wtItem(result, "plan")))
	if rc := wtItem(result, "rc"); wPyTruthy(rc) {
		wtValue("Workspace setup command exited with status " + wPyStr(rc) + "\n" + wtGetString(w.p, "setup_output"))
	}
}

func within(path, root string) bool {
	c, err := pyCommonpath(pyRealpath(path), root)
	wtMust(err)
	return c == root
}

// claimCreated claims a worktree Git allocated before failing (a post-
// checkout hook, say): only that exact new allocation.
func (w *wtSingle) claimCreated(dest, common, commit string) {
	if w.git(dest, false, "rev-parse", "--show-toplevel") != dest {
		wtValue("Created directory is not the worktree root")
	}
	if w.git(dest, false, "rev-parse", "--path-format=absolute", "--git-common-dir") != common {
		wtValue("Created worktree belongs to another repository")
	}
	if !wPyEqual(w.git(dest, false, "symbolic-ref", "--quiet", "--short", "HEAD"), w.p.Val("branch")) {
		wtValue("Created worktree branch does not match")
	}
	if w.git(dest, false, "rev-parse", "HEAD") != commit {
		wtValue("Created worktree revision does not match")
	}
	owner := pathlibJoin(w.git(dest, false, "rev-parse", "--absolute-git-dir"), "lectern-owner")
	writeOwner(owner, wtString(w.p, "token"))
}

// writeOwner is Path.open('x').write(token).
func writeOwner(path, token string) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o666)
	wtMust(wPyErr(err, path))
	_, err = f.WriteString(token)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	wtMust(wPyErr(err, path))
}

// terminalPanes lists the session backend's pane directories, raising msg
// when that fails for any reason but there being no server.
func terminalPanes(msg string) []string {
	cmd := Mux("list-panes", "-a", "-F", "#{pane_current_path}")
	res, err := pyRun{Argv: cmd.Args, Timeout: 10, TimeoutText: "10"}.run()
	wtMust(err)
	out, errText, err := decodePair(res)
	wtMust(err)
	if res.RC != 0 {
		lower := strings.ToLower(errText)
		if !strings.Contains(lower, "no server running") && !strings.Contains(lower, "no such file or directory") {
			wtValue(msg)
		}
	}
	return pySplitLines(out, false)
}

func (w *wtSingle) registered(repo, dest string) bool {
	for _, entry := range strings.Split(w.git(repo, true, "worktree", "list", "--porcelain", "-z"), "\x00") {
		if entry == "worktree "+dest {
			return true
		}
	}
	return false
}

// run performs the operation; true means it already printed and exited 0.
func (w *wtSingle) run(operation string) bool {
	p := w.p
	if operation == "create" {
		if w.inherited == nil {
			w.control = newSetupControl(p, true)
			w.control.lease(true)
		} else if len(w.args) > 4 {
			w.control = newSetupControl(wtPlan(w.args[4]), true)
		}
		if w.control != nil {
			wtMust(w.control.check())
		}
	}
	repoArg, pathArg := wtString(p, "repo"), wtString(p, "path")
	repo, dest := pyRealpath(repoArg), pyRealpath(pathArg)
	if !pyIsAbs(repoArg) || !pyIsAbs(pathArg) {
		wtValue("Worktree paths must be absolute")
	}
	common := w.git(repo, true, "rev-parse", "--path-format=absolute", "--git-common-dir")
	switch operation {
	case "create":
		w.create(repo, dest, common)
	case "recover", "check-recover":
		if pyIsLink(pathArg) {
			wtValue("Allocation path was replaced by a symlink")
		}
		if w.inherited == nil {
			w.checkRecorded(repo, dest)
		}
		if !pyIsDir(dest) {
			if pyLexists(pathArg) {
				wtValue("Allocation path was replaced")
			}
			if w.registered(repo, dest) {
				wtValue("Missing checkout is still registered with Git; inspect it manually")
			}
			p.Set("state", "removed")
			w.print(newObj("workspace", p))
			return true
		}
		if w.git(dest, true, "rev-parse", "--show-toplevel") != dest {
			wtValue("Directory is not the recorded worktree root")
		}
		if w.git(dest, true, "rev-parse", "--path-format=absolute", "--git-common-dir") != common {
			wtValue("Worktree repository does not match")
		}
		if !wPyEqual(w.git(dest, true, "symbolic-ref", "--quiet", "--short", "HEAD"), wtItem(p, "branch")) {
			wtValue("Worktree branch changed; inspect it manually")
		}
		for _, cwd := range terminalPanes("Could not check active terminals before recovery") {
			if cwd != "" && within(cwd, dest) {
				wtValue("A terminal is using this allocation; leave it before recovery")
			}
		}
		owner := pathlibJoin(w.git(dest, true, "rev-parse", "--absolute-git-dir"), "lectern-owner")
		if pyLexists(owner) {
			f, err := os.OpenFile(owner, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
			wtMust(wPyErr(err, owner))
			info, err := f.Stat()
			var text string
			if err == nil && info.Mode().IsRegular() {
				data, rerr := io.ReadAll(f)
				err = rerr
				if err == nil {
					text, err = wtReadText(data)
				}
			}
			f.Close()
			wtMust(wPyErr(err))
			if !info.Mode().IsRegular() || !wPyEqual(text, wtItem(p, "token")) {
				wtValue("Worktree ownership does not match")
			}
		} else {
			if !wPyTruthy(p.Val("commit")) || !wPyEqual(w.git(dest, true, "rev-parse", "HEAD"), wtItem(p, "commit")) {
				wtValue("Worktree revision changed; ownership was not recovered")
			}
			if operation == "recover" {
				w.claimCreated(dest, common, wtString(p, "commit"))
			}
		}
		state := p.Val("setup_state")
		if wPyTruthy(p.Val("setup_command")) && !wPyEqual(state, "complete") && !wPyEqual(state, "failed") {
			p.Set("setup_state", "interrupted")
		}
		p.Update(newObj("repo", repo, "path", dest, "state", "ready"))
		p.Delete("error")
	case "remove", "check-remove":
		if !pyIsDir(dest) {
			if w.registered(repo, dest) {
				wtValue("Worktree directory is missing but still registered with Git; inspect it before cleanup")
			}
			p.Set("state", "removed")
			w.print(newObj("workspace", p))
			return true
		}
		if w.git(dest, true, "rev-parse", "--show-toplevel") != dest {
			wtValue("Directory is not the recorded worktree root")
		}
		if w.git(dest, true, "rev-parse", "--path-format=absolute", "--git-common-dir") != common {
			wtValue("Worktree belongs to another repository")
		}
		owner := pathlibJoin(w.git(dest, true, "rev-parse", "--absolute-git-dir"), "lectern-owner")
		if !pyIsFile(owner) {
			wtValue("Worktree ownership does not match; nothing was removed")
		}
		data, err := os.ReadFile(owner)
		wtMust(wPyErr(err, owner))
		text, err := wtReadText(data)
		wtMust(err)
		if !wPyEqual(text, wtItem(p, "token")) {
			wtValue("Worktree ownership does not match; nothing was removed")
		}
		if !wPyEqual(w.git(dest, true, "symbolic-ref", "--quiet", "--short", "HEAD"), wtItem(p, "branch")) {
			wtValue("Worktree branch changed; nothing was removed")
		}
		for _, cwd := range terminalPanes("Could not check active terminals; nothing was removed") {
			if cwd != "" && within(cwd, dest) {
				wtValue("A terminal is still using this worktree; end or leave it first")
			}
		}
		if w.git(dest, true, "--no-optional-locks", "status", "--porcelain", "--untracked-files=all", "--ignored=matching") != "" {
			wtValue("Worktree contains changed, untracked or ignored files; commit or move them before removal")
		}
		if operation == "remove" {
			w.git(repo, true, "worktree", "remove", "--", dest)
			p.Set("state", "removed")
		}
	default:
		wtValue("Unknown worktree operation")
	}
	return false
}

func (w *wtSingle) create(repo, dest, common string) {
	p := w.p
	base := wtString(p, "base")
	if strings.HasPrefix(base, "-") || base == "" {
		wtValue("Choose a branch, tag or commit as the base")
	}
	branch := wtString(p, "branch")
	w.git(repo, true, "check-ref-format", "--branch", branch)
	commit := w.git(repo, true, "rev-parse", "--verify", "--end-of-options", base+"^{commit}")
	if pyLexists(wtString(p, "path")) {
		wtValue("Worktree path already exists; nothing was changed")
	}
	wtMust(pathlibMkdir(pathlibParent(dest), 0o777, true, true))
	if w.control != nil && w.inherited == nil {
		plan := p.Copy()
		plan.Update(newObj("repo", repo, "path", dest, "commit", commit))
		w.control.allocation(plan)
	}
	if err := wtTry(func() { w.git(repo, true, "worktree", "add", "-b", branch, "--", dest, commit) }); err != nil {
		if wtIsValue(err) && pyIsDir(dest) {
			if claimErr := wtTry(func() { w.claimCreated(dest, common, commit) }); claimErr == nil {
				p.Update(newObj("repo", repo, "path", dest, "commit", commit, "state", "failed", "error", err.Error()))
			} else if wtIsLookup(claimErr) {
				wtRaise(claimErr)
			}
		}
		wtRaise(err)
	}
	owner := pathlibJoin(w.git(dest, true, "rev-parse", "--absolute-git-dir"), "lectern-owner")
	writeOwner(owner, wtString(p, "token"))
	p.Update(newObj("repo", repo, "path", dest, "commit", commit, "state", "creating"))
	if err := wtTry(w.runSetup); err != nil {
		if !wtIsLookup(err) {
			msg := err.Error()
			if wtIsTimeout(err) {
				msg = "Workspace setup command timed out; allocation retained for inspection"
			}
			p.Update(newObj("state", "failed", "error", msg))
			if wPyEqual(p.Val("setup_state"), "running") {
				p.Set("setup_state", "interrupted")
			}
		}
		wtRaise(err)
	}
	p.Set("state", "ready")
}

// checkRecorded compares a recovery against the allocation record written
// before the checkout, under the operation lease.
func (w *wtSingle) checkRecorded(repo, dest string) {
	p := w.p
	recovery := newSetupControl(p, false)
	w.recovery = recovery.lease(false)
	recorded := recovery.allocation(nil)
	if !wPyTruthy(recorded) {
		wtValue("The original checkout revision was not recorded; inspect this allocation manually")
	}
	rec := wtObj(recorded)
	for _, k := range []string{"token", "branch"} {
		if !wPyEqual(rec.Val(k), p.Val(k)) {
			wtValue("Recorded allocation identity does not match")
		}
	}
	if !wPyEqual(rec.Val("repo"), repo) || !wPyEqual(rec.Val("path"), dest) {
		wtValue("Recorded allocation paths do not match")
	}
	commit, ok := rec.Val("commit").(string)
	if !ok || !wtCommit.MatchString(commit) {
		wtValue("Recorded checkout revision is unavailable")
	}
	if wPyTruthy(p.Val("commit")) && !wPyEqual(p.Val("commit"), commit) {
		wtValue("Recorded checkout revisions do not match")
	}
	p.Set("commit", commit)
	for _, key := range []string{"setup_command", "setup_env", "setup_state", "setup_output"} {
		if v, ok := rec.Get(key); ok {
			p.Set(key, v)
		}
	}
	if wPyEqual(p.Val("setup_state"), "running") {
		p.Set("setup_state", "interrupted")
	}
}

// worktreeGated is worktree-group's child: it waits for the one byte the
// group writes once this process group is durably recorded (EOF: the group
// died first, so exit without running Git), then runs as worktree.
// argv: GATE_FD OPERATION PLAN_JSON LOCK_FD TIMEOUT OWNER_JSON.
func worktreeGated(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) < 1 {
		return 1
	}
	fd, err := strconv.Atoi(args[0])
	if err != nil {
		return 1
	}
	gate := os.NewFile(uintptr(fd), "gate")
	var b [1]byte
	n, _ := gate.Read(b[:])
	gate.Close()
	if n != 1 || b[0] != '1' {
		return 1
	}
	return worktreeMain(args[1:], stdout, stderr)
}

// worktreeMonitor runs a command (git worktree add) with the lease inherited
// and cancellation watched, killing its own process group when cancelled.
// argv: PLAN_JSON LEASE_FD TIMEOUT COMMAND…
func worktreeMonitor(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	rc := 0
	err := wtTry(func() {
		if len(args) < 4 {
			wtRaise(&wtTypeError{"missing arguments"})
		}
		c := newSetupControl(wtPlan(args[0]), false)
		wtMust(c.check())
		fd, err := strconv.Atoi(args[1])
		wtMust(err)
		timeout, err := strconv.ParseFloat(args[2], 64)
		wtMust(err)
		lease := os.NewFile(uintptr(fd), "lease")
		defer runtime.KeepAlive(lease)
		child, err := wtStart(args[3:], wtSpawn{extra: []*os.File{lease}})
		wtMust(err)
		o, e, err := c.wait(child, timeout, syscall.Getpgrp())
		wtMust(err)
		io.WriteString(stdout, o)
		io.WriteString(stderr, e)
		rc = child.rc
	})
	if err != nil {
		return wtUncaught(stderr, err)
	}
	return rc & 0xff
}

// worktreeSetupMonitor runs the setup command in the checkout, output to a
// temporary file of which the final 4 KiB is kept, and records the result in
// the allocation. argv: PLAN_JSON OWNER_JSON LEASE_FD TIMEOUT.
func worktreeSetupMonitor(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	err := wtTry(func() {
		if len(args) < 4 {
			wtRaise(&wtTypeError{"missing arguments"})
		}
		plan := wtObj(wtPlan(args[0]))
		owner := wtPlan(args[1])
		fd, err := strconv.Atoi(args[2])
		wtMust(err)
		timeout, err := strconv.ParseFloat(args[3], 64)
		wtMust(err)
		c := newSetupControl(owner, false)
		wtMust(c.check())
		output, err := os.CreateTemp("", "tmp")
		wtMust(wPyErr(err))
		os.Remove(output.Name())
		defer output.Close()
		var env []string
		var extra []string
		if se, ok := plan.Get("setup_env"); ok {
			for _, k := range wtObj(se).Keys() {
				v, _ := wtObj(se).Get(k)
				s, isStr := v.(string)
				if !isStr {
					wtRaise(&wtTypeError{"expected str, bytes or os.PathLike object, not " + pyTypeName(v)})
				}
				extra = append(extra, k, s)
			}
		}
		env = pyEnviron(extra...)
		lease := os.NewFile(uintptr(fd), "lease")
		defer runtime.KeepAlive(lease)
		child, err := wtStart([]string{"bash", "-c", wtString(plan, "setup_command")},
			wtSpawn{dir: wtString(plan, "path"), env: env, extra: []*os.File{lease}, stdout: output, stderr: output})
		wtMust(err)
		waitErr := wtTry(func() {
			_, _, err := c.wait(child, timeout, syscall.Getpgrp())
			wtMust(err)
			if child.rc == 0 {
				plan.Set("setup_state", "complete")
			} else {
				plan.Set("setup_state", "failed")
			}
		})
		if waitErr != nil {
			plan.Set("setup_state", "interrupted")
		}
		size, _ := output.Seek(0, io.SeekEnd)
		output.Seek(max(0, size-4096), io.SeekStart)
		var tail bytes.Buffer
		io.Copy(&tail, output)
		plan.Set("setup_output", wPyDecodeReplace(tail.Bytes()))
		if wPyEqual(wtItem(plan, "token"), wtItem(owner, "token")) {
			c.allocation(plan)
		}
		wtMust(waitErr)
		io.WriteString(stdout, pyDumps(newObj("rc", child.rc, "plan", plan))+"\n")
	})
	if err != nil {
		return wtUncaught(stderr, err)
	}
	return 0
}
