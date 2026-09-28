//go:build unix

package helpers

// Port of internal/worktree/multi_worker.py: grouped worktrees with durable
// progress and conservative cleanup. Each repository is one worktree-gated
// child in its own process group, recorded in the process receipt before it
// may start.
//
// argv: ACTION PLAN_JSON [TIMEOUT]. The script also took the preflight and
// single-checkout sources as arguments; here they are the worktree-preflight
// and worktree helpers of this binary.

import (
	"errors"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func init() {
	Register("worktree-group", worktreeGroup)
}

type wtGroup struct {
	p        *pyObj
	root     string
	lock     *os.File
	owned    bool
	control  *setupControl
	deadline time.Time
}

func worktreeGroup(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	if len(args) < 2 {
		return wtUncaught(stderr, errors.New("not enough values to unpack"))
	}
	timeout := 105.0
	if len(args) > 2 {
		t, err := strconv.ParseFloat(strings.TrimSpace(args[2]), 64)
		if err != nil {
			return wtUncaught(stderr, errors.New("could not convert string to float: "+pyReprString(args[2])))
		}
		timeout = t
	}
	g := &wtGroup{deadline: time.Now().Add(time.Duration(timeout * float64(time.Second)))}
	defer runtime.KeepAlive(g)
	if err := wtTry(func() {
		g.p = wtObj(wtPlan(args[1]))
		g.root = pathlibStr(wtString(g.p, "path"))
	}); err != nil {
		return wtUncaught(stderr, err)
	}
	defer func() {
		if g.lock != nil {
			g.lock.Close()
		}
	}()
	var printed any
	err := wtTry(func() { printed = g.run(args[0]) })
	if err == nil {
		io.WriteString(stdout, pyDumps(newObj("workspace", printed))+"\n")
		return 0
	}
	result := newObj("error", err.Error())
	if g.owned {
		g.p.Update(newObj("state", "failed", "error", err.Error()))
		if saveErr := wtTry(g.save); saveErr != nil {
			if wtIsLookup(saveErr) || wtIsTimeout(saveErr) {
				return wtUncaught(stderr, saveErr)
			}
			result.Set("error", err.Error()+"; could not save workspace progress: "+saveErr.Error())
		}
		result.Set("workspace", g.p)
	}
	io.WriteString(stdout, pyDumps(result)+"\n")
	return 1
}

func (g *wtGroup) path(name string) string { return pathlibJoin(g.root, name) }

// regularFile opens workspace metadata without following a link.
func (g *wtGroup) regularFile(name string, flags int) *os.File {
	path := g.path(name)
	f, err := os.OpenFile(path, flags|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0o600)
	wtMust(wPyErr(err, path))
	info, err := f.Stat()
	if err != nil {
		f.Close()
		wtMust(wPyErr(err))
	}
	if !info.Mode().IsRegular() {
		f.Close()
		wtValue("Workspace metadata must be a regular file: " + name)
	}
	return f
}

// acquireLock waits briefly for the lock: the status probe holds it for a
// moment; a real operation holds it past the grace period and is refused.
func (g *wtGroup) acquireLock() {
	deadline := time.Now().Add(250 * time.Millisecond)
	if g.deadline.Before(deadline) {
		deadline = g.deadline
	}
	for {
		err := flock(g.lock, syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			wtMust(wPyErr(err))
		}
		if !time.Now().Before(deadline) {
			wtValue("A workspace operation is still running; try again after it finishes")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// recordName prefers the current prefix; a workspace made before the rename
// keeps its files (and lock identity) under the old one.
func (g *wtGroup) recordName(kind string) string {
	current, legacy := ".lectern-"+kind, ".agentdeck-"+kind
	if pyExists(g.path(current)) || !pyExists(g.path(legacy)) {
		return current
	}
	return legacy
}

func (g *wtGroup) readRecord(name string) any {
	f := g.regularFile(name, os.O_RDONLY)
	defer f.Close()
	data, err := io.ReadAll(f)
	wtMust(wPyErr(err))
	text, err := wtReadText(data)
	wtMust(err)
	v, err := pyLoads(text)
	wtMust(err)
	return v
}

// writeRecord installs a new private inode under the lock; an existing path
// is never truncated, since it may have been replaced by a link.
func (g *wtGroup) writeRecord(name string, value any) {
	f, err := os.CreateTemp(g.root, ".lectern-write-")
	wtMust(wPyErr(err, filepath.Join(g.root, ".lectern-write-")))
	temp := f.Name()
	defer func() {
		if pyLexists(temp) {
			os.Remove(temp)
		}
	}()
	_, err = f.WriteString(pyDumps(value))
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	wtMust(wPyErr(err))
	dest := g.path(name)
	wtMust(wPyErr(os.Rename(temp, dest), temp, dest))
	dir, err := os.OpenFile(g.root, os.O_RDONLY|syscall.O_DIRECTORY, 0)
	wtMust(wPyErr(err, g.root))
	err = dir.Sync()
	dir.Close()
	wtMust(wPyErr(err))
}

func (g *wtGroup) save() { g.writeRecord(g.recordName("state.json"), g.p) }

// wtIdentity is (path, token, branch, [(repo, path, token, branch)…]).
func wtIdentity(plan any) []any {
	var repos []any
	for _, r := range wtList(plan, "repositories") {
		wt := wtItem(r, "worktree")
		repos = append(repos, []any{wtItem(wt, "repo"), wtItem(wt, "path"), wtItem(wt, "token"), wtItem(wt, "branch")})
	}
	if repos == nil {
		repos = []any{}
	}
	return []any{wtItem(plan, "path"), wtItem(plan, "token"), wtItem(plan, "branch"), repos}
}

func prefixIdentity(older, newer any) bool {
	left, right := wtIdentity(older), wtIdentity(newer)
	if !wPyEqual(left[:3], right[:3]) {
		return false
	}
	l, r := left[3].([]any), right[3].([]any)
	return len(l) > 0 && wPyEqual(l, r[:min(len(l), len(r))])
}

// wProcStat is /proc/PID/stat split after the command name.
func wProcStat(pid string) ([]string, error) {
	data, err := os.ReadFile("/proc/" + pid + "/stat")
	if err != nil {
		return nil, err
	}
	text := string(data)
	i := strings.LastIndex(text, ")")
	if i < 0 {
		return nil, errors.New("not enough values to unpack")
	}
	return strings.Fields(text[i+1:]), nil
}

// processStarttime identifies a process beyond its reusable PID.
func processStarttime(pid int) any {
	fields, err := wProcStat(strconv.Itoa(pid))
	if err != nil || len(fields) <= 19 {
		return nil
	}
	return fields[19]
}

func bootID() any {
	data, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return nil
	}
	return wPyStrip(string(data))
}

// busy refuses while the recorded worker's process group may still write.
func (g *wtGroup) busy() {
	receipt := g.path(g.recordName("process.json"))
	if !pyLexists(receipt) {
		return
	}
	record := wtObj(g.readRecord(g.recordName("process.json")))
	pgidValue := wtItem(record, "pgid")
	n, isInt := pgidValue.(*big.Int)
	if !isInt || n.Cmp(big.NewInt(1)) <= 0 {
		wtValue("Workspace process receipt is invalid; inspect it before cleanup")
	}
	if !n.IsInt64() || n.Int64() > int64(^uint32(0)>>1) {
		wtRaise(&pyOSError{errno: syscall.ERANGE}) // killpg: signed integer is greater than maximum
	}
	pgid := int(n.Int64())
	expectedStart, expectedBoot, currentBoot := record.Val("starttime"), record.Val("boot_id"), bootID()
	if wPyTruthy(expectedBoot) && wPyTruthy(currentBoot) && !wPyEqual(expectedBoot, currentBoot) {
		return
	}
	if s, ok := expectedStart.(string); ok && s != "" {
		if actual := processStarttime(pgid); actual != nil && !wPyEqual(actual, s) {
			return
		}
	}
	if err := syscall.Kill(-pgid, 0); err == syscall.ESRCH {
		return
	} else if err != nil {
		wtMust(wPyErr(err))
	}
	// Zombies cannot write into the workspace and must not make recovery
	// impossible.
	if pyExists("/proc/self/stat") {
		entries, err := os.ReadDir("/proc")
		wtMust(wPyErr(err, "/proc"))
		active := false
		for _, e := range entries {
			if !isDigits(e.Name()) {
				continue
			}
			fields, err := wProcStat(e.Name())
			if err != nil {
				if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ESRCH) {
					continue
				}
				wtMust(wPyErr(err, "/proc/"+e.Name()+"/stat"))
			}
			if len(fields) > 2 && fields[2] == strconv.Itoa(pgid) && fields[0] != "Z" && fields[0] != "X" {
				active = true
				break
			}
		}
		if !active {
			return
		}
	}
	wtValue("A workspace operation is still running; try again after it finishes")
}

func (g *wtGroup) checkTerminals() {
	for _, cwd := range terminalPanes("Could not check active terminals; nothing was removed") {
		if cwd == "" {
			continue
		}
		c, err := pyCommonpath(pyRealpath(cwd), g.root)
		wtMust(err)
		if c == g.root {
			wtValue("A terminal is still using this workspace; end or leave it first")
		}
	}
}

// runChild runs one repository's operation as a gated child in its own
// process group, recorded in the process receipt before it may start.
func (g *wtGroup) runChild(entry any, operation string) {
	budget := time.Until(g.deadline).Seconds()
	if budget <= 0 {
		wtValue(wtTimedOut)
	}
	if g.control != nil {
		wtMust(g.control.check())
	}
	g.busy()
	childPlan := wtObj(wtItem(entry, "worktree")).Copy()
	if operation == "create" {
		childPlan.Set("base", wtItem(childPlan, "commit"))
	}
	gateR, gateW, err := os.Pipe()
	wtMust(wPyErr(err))
	var child *wtChild
	startErr := wtTry(func() {
		argv := selfHelper("worktree-gated", "3", operation, pyDumps(childPlan), "4",
			wPyFloatRepr(max(.01, budget-1)), pyDumps(g.p))
		c, err := wtStart(argv, wtSpawn{extra: []*os.File{gateR, g.lock}, setsid: true})
		wtMust(err)
		child = c
		g.writeRecord(g.recordName("process.json"), newObj(
			"pgid", child.pid(), "starttime", processStarttime(child.pid()), "boot_id", bootID()))
		_, err = gateW.Write([]byte("1"))
		wtMust(wPyErr(err))
	})
	gateR.Close()
	gateW.Close()
	wtMust(startErr)
	var stdout, stderr string
	if g.control != nil {
		stdout, stderr, err = g.control.wait(child, budget, 0)
	} else {
		stdout, stderr, err = child.communicate(budget)
	}
	if err != nil {
		if wtIsTimeout(err) {
			if !child.poll() {
				wtMust(killGroup(child.pid()))
			}
			child.wait(-1)
			wtValue("Repository operation timed out; allocation retained for inspection")
		}
		wtRaise(err)
	}
	resultValue, err := pyLoads(stdout)
	if err != nil {
		wtValue("Repository worker did not return a result: " + wPyStrip(stderr))
	}
	result := wtObj(resultValue)
	if operation == "check-recover" {
		if child.rc != 0 {
			wtValue(firstNonEmpty(wPyStr(orEmpty(result.Val("error"))), "Repository validation failed"))
		}
		return
	}
	if ws := result.Val("workspace"); wPyTruthy(ws) {
		wtObj(ws).Set("base", wtItem(wtItem(entry, "worktree"), "base"))
		wtObj(entry).Set("worktree", ws)
	}
	g.save()
	if child.rc != 0 {
		wtValue(firstNonEmpty(wPyStr(orEmpty(result.Val("error"))), "Repository operation failed"))
	}
}

// orEmpty stands in "" for a falsy value in `x or default`.
func orEmpty(v any) any {
	if !wPyTruthy(v) {
		return ""
	}
	return v
}

func (g *wtGroup) lockIdentity() []any {
	var info syscall.Stat_t
	wtMust(wPyErr(syscall.Fstat(int(g.lock.Fd()), &info)))
	return devIno(&info)
}

func (g *wtGroup) rootMissing() bool {
	return pyIsLink(g.root) || !pyIsDir(g.root)
}

// run performs the action and returns the plan to print.
func (g *wtGroup) run(action string) any {
	switch action {
	case "create", "extend", "remove", "check-remove", "status", "recover":
	default:
		wtValue("Unknown workspace operation")
	}
	if action == "status" {
		// Readers inspect progress while a checkout holds the operation
		// lock; the writer replaces its receipt atomically. Never save here.
		if g.rootMissing() {
			wtValue("Workspace root is missing or replaced")
		}
		g.lock = g.regularFile(g.recordName("lock"), os.O_RDONLY)
		saved := wtObj(g.readRecord(g.recordName("state.json")))
		if !(prefixIdentity(saved, g.p) || prefixIdentity(g.p, saved)) {
			wtValue("Workspace ownership or repository allocation does not match")
		}
		if !wPyEqual(saved.Val("operation_lock"), g.lockIdentity()) {
			wtValue("Workspace operation lock was replaced")
		}
		active := false
		if err := flock(g.lock, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
			if !errors.Is(err, syscall.EWOULDBLOCK) {
				wtMust(wPyErr(err))
			}
			active = true
		} else {
			flock(g.lock, syscall.LOCK_UN)
		}
		if !active {
			if err := wtTry(g.busy); err != nil {
				if !wtIsValue(err) {
					wtRaise(err)
				}
				active = true
			}
		}
		saved.Set("operation_active", active)
		return saved
	}
	if action == "create" {
		g.control = newSetupControl(g.p, true)
		wtMust(g.control.check())
		g.p = wtPreflight(g.p, 0)
		g.root = pathlibStr(wtString(g.p, "path"))
		wtMust(pathlibMkdir(pathlibParent(g.root), 0o777, true, true))
		wtMust(pathlibMkdir(g.root, 0o700, false, false))
		g.lock = g.regularFile(".lectern-lock", os.O_RDWR|os.O_CREATE|os.O_EXCL)
		g.acquireLock()
		g.p.Set("operation_lock", g.lockIdentity())
		g.owned = true
		g.save()
		// Resolve all refs before the first mutation; use those exact commits
		// so a moving branch cannot silently change a later repository base.
		for _, entry := range wtList(g.p, "repositories") {
			g.runChild(entry, "create")
		}
		g.busy()
		g.p.Set("state", "ready")
		g.p.Delete("error")
		g.save()
		return g.p
	}
	if g.rootMissing() {
		wtValue("Workspace root is missing or replaced; inspect it before cleanup")
	}
	g.lock = g.regularFile(g.recordName("lock"), os.O_RDWR)
	g.acquireLock()
	saved := wtObj(g.readRecord(g.recordName("state.json")))
	if action == "extend" {
		return g.extend(saved)
	}
	if !wPyEqual(wtIdentity(saved), wtIdentity(g.p)) {
		wtValue("Workspace ownership or repository allocation does not match")
	}
	if !wPyEqual(saved.Val("operation_lock"), g.lockIdentity()) {
		wtValue("Workspace operation lock was replaced; inspect it before cleanup")
	}
	g.p = saved
	g.owned = true
	g.busy()
	g.checkTerminals()
	if action == "recover" {
		for _, entry := range wtList(g.p, "repositories") {
			g.runChild(entry, "check-recover")
		}
		for _, entry := range wtList(g.p, "repositories") {
			g.runChild(entry, "recover")
		}
		g.p.Update(newObj("state", "failed", "error", "Interrupted checkout validated; files retained for inspection"))
		g.save()
		return g.p
	}
	allowed := map[string]bool{".lectern-lock": true, ".lectern-state.json": true, ".lectern-process.json": true,
		".agentdeck-lock": true, ".agentdeck-state.json": true, ".agentdeck-process.json": true}
	for _, r := range wtList(g.p, "repositories") {
		allowed[pyBasename(pathlibStr(wtString(wtItem(r, "worktree"), "path")))] = true
	}
	names, err := os.ReadDir(g.root)
	wtMust(wPyErr(err, g.root))
	for _, e := range names {
		if !allowed[e.Name()] {
			wtValue("Workspace root contains additional files; move them before removal")
		}
	}
	for _, entry := range wtList(g.p, "repositories") {
		g.runChild(entry, "check-remove")
	}
	if action == "remove" {
		for _, entry := range wtList(g.p, "repositories") {
			g.checkTerminals()
			g.runChild(entry, "remove")
		}
		g.busy()
		// Keep the owned root and receipt as a durable recovery record; the
		// agent may have written root-level files during removal.
		g.p.Set("state", "removed")
		g.p.Delete("error")
		g.save()
	}
	return g.p
}

func (g *wtGroup) extend(saved *pyObj) any {
	savedRepos := wtList(saved, "repositories")
	if !prefixIdentity(saved, g.p) || len(wtList(g.p, "repositories")) != len(savedRepos)+1 {
		wtValue("Extension must preserve every existing repository and add exactly one")
	}
	for _, r := range savedRepos {
		if !wPyEqual(wtItem(wtItem(r, "worktree"), "state"), "ready") {
			wtValue("Recover incomplete repositories before extending the workspace")
		}
	}
	token := g.p.Val("control_token")
	if !wPyTruthy(token) || wPyEqual(token, saved.Val("control_token")) || wPyEqual(token, wtItem(saved, "token")) {
		wtValue("Extension requires a fresh cancellation identity")
	}
	if !wPyEqual(saved.Val("operation_lock"), g.lockIdentity()) {
		wtValue("Workspace operation lock was replaced")
	}
	g.busy()
	candidate := saved.Copy()
	repos := wtList(g.p, "repositories")
	candidate.Update(newObj("repositories", append(append([]any{}, savedRepos...), repos[len(repos)-1]),
		"control_token", token, "state", "extending"))
	g.p = wtPreflight(candidate, len(savedRepos))
	g.control = newSetupControl(g.p, true)
	wtMust(g.control.check())
	g.owned = true
	g.save() // Publish the full allocation before the new child can mutate.
	all := wtList(g.p, "repositories")
	g.runChild(all[len(all)-1], "create")
	g.busy()
	g.p.Set("state", "ready")
	g.p.Delete("error")
	g.save()
	return g.p
}
