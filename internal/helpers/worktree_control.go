//go:build unix

package helpers

// Port of internal/worktree/setup_control.py: the cooperative cancellation
// record beside an allocation and the operation lease, plus the child-process
// supervision (wait with cancellation checks, kill by process group) that
// every worktree helper shares. The records are read and written byte for
// byte as the Python wrote them, so either version can pick up the other's.

import (
	"bytes"
	"errors"
	"io"
	"math/big"
	"os"
	"os/exec"
	"regexp"
	"syscall"
	"time"
)

var wtToken = regexp.MustCompile(`^[0-9a-f]{32}$`)

type setupControl struct {
	identity     *pyObj
	path         string
	create       bool
	leaseFile    *os.File
	fileIdentity *[2]uint64
}

var errControlUnreadable = errors.New("Setup control record is unavailable or unreadable; inspect the allocation")

// newSetupControl is SetupControl(plan, create). It raises like __init__.
func newSetupControl(plan any, create bool) *setupControl {
	o := wtObj(plan)
	token := ""
	if v := o.Val("control_token"); wPyTruthy(v) {
		token, _ = v.(string)
	} else if v, ok := o.Get("token"); ok {
		s, isStr := v.(string)
		if !isStr {
			wtRaise(&wtTypeError{"expected string or bytes-like object, got '" + pyTypeName(v) + "'"})
		}
		token = s
	}
	if !wtToken.MatchString(token) {
		wtValue("Invalid setup cancellation identity")
	}
	path := wtString(o, "path")
	if !pyIsAbs(path) {
		wtValue("Invalid setup cancellation identity")
	}
	repo := ""
	if r := wtItem(o, "repo"); wPyTruthy(r) {
		rs, _ := r.(string)
		repo = pyRealpath(rs)
	}
	c := &setupControl{identity: newObj("token", token, "path", pyRealpath(path), "repo", repo), create: create}
	parent := pathlibParent(c.identity.Str("path"))
	if create {
		wtMust(pathlibMkdir(parent, 0o777, true, true))
	}
	c.path = pathlibJoin(parent, ".lectern-setup-"+token+".json")
	wtMust(c.access(false, nil, false).err)
	return c
}

type accessResult struct {
	cancelled bool
	saved     *pyObj
	err       error
}

// access is SetupControl.access: OSError and JSON errors become one message.
func (c *setupControl) access(cancel bool, update *pyObj, snapshot bool) accessResult {
	r := c.accessRaw(cancel, update, snapshot)
	var jsonErr *pyJSONError
	if r.err != nil && (wtIsOSError(r.err) || errors.As(r.err, &jsonErr)) {
		r.err = errControlUnreadable
	}
	return r
}

func (c *setupControl) accessRaw(cancel bool, update *pyObj, snapshot bool) accessResult {
	flags := os.O_RDWR | syscall.O_NOFOLLOW | syscall.O_NONBLOCK
	if c.create {
		flags |= os.O_CREATE
	}
	f, err := os.OpenFile(c.path, flags, 0o600)
	if err != nil {
		return accessResult{err: wPyErr(err, c.path)}
	}
	defer f.Close()
	if err := flock(f, syscall.LOCK_EX); err != nil {
		return accessResult{err: wPyErr(err)}
	}
	var info syscall.Stat_t
	if err := syscall.Fstat(int(f.Fd()), &info); err != nil {
		return accessResult{err: wPyErr(err)}
	}
	if info.Mode&syscall.S_IFMT != syscall.S_IFREG || info.Nlink != 1 {
		return accessResult{err: errors.New("Setup cancellation record is not a private regular file")}
	}
	id := [2]uint64{uint64(info.Dev), uint64(info.Ino)}
	if c.fileIdentity != nil && *c.fileIdentity != id {
		return accessResult{err: errors.New("Setup cancellation record was replaced")}
	}
	c.fileIdentity = &id
	var current syscall.Stat_t
	if err := syscall.Lstat(c.path, &current); err != nil {
		return accessResult{err: wPyErr(err, c.path)}
	}
	if uint64(current.Dev) != id[0] || uint64(current.Ino) != id[1] {
		return accessResult{err: errors.New("Setup cancellation record was replaced")}
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return accessResult{err: wPyErr(err)}
	}
	raw, err := wtReadText(data)
	if err != nil {
		return accessResult{err: err}
	}
	var saved *pyObj
	if raw != "" {
		v, err := pyLoads(raw)
		if err != nil {
			return accessResult{err: err}
		}
		o, ok := v.(*pyObj)
		if !ok {
			return accessResult{err: &wtTypeError{"'" + pyTypeName(v) + "' object has no attribute 'get'"}}
		}
		saved = o
	} else {
		saved = c.identity.Copy()
		saved.Set("cancelled", false)
	}
	for _, k := range c.identity.Keys() {
		if !wPyEqual(saved.Val(k), c.identity.Val(k)) {
			return accessResult{err: errors.New("Setup cancellation ownership does not match")}
		}
	}
	if update.Len() > 0 {
		saved.Update(update)
	}
	if cancel {
		saved.Set("cancelled", true)
	}
	if cancel || update.Len() > 0 || raw == "" {
		body := []byte(pyDumps(saved))
		if _, err := f.WriteAt(body, 0); err != nil {
			return accessResult{err: wPyErr(err)}
		}
		if err := f.Truncate(int64(len(body))); err != nil {
			return accessResult{err: wPyErr(err)}
		}
		if err := f.Sync(); err != nil {
			return accessResult{err: wPyErr(err)}
		}
	}
	b, _ := saved.Val("cancelled").(bool)
	return accessResult{cancelled: b, saved: saved}
}

// lease is SetupControl.lease: the operation lock beside the record.
func (c *setupControl) lease(create bool) *os.File {
	f, err := c.leaseRaw(create)
	if err != nil && wtIsOSError(err) {
		err = errors.New("Setup operation lock is unavailable; inspect the allocation")
	}
	wtMust(err)
	return f
}

func (c *setupControl) leaseRaw(create bool) (*os.File, error) {
	location := c.path + ".lock"
	flags := os.O_RDWR | syscall.O_NOFOLLOW | syscall.O_NONBLOCK
	if create {
		flags |= os.O_CREATE
	}
	f, err := os.OpenFile(location, flags, 0o600)
	if err != nil {
		return nil, wPyErr(err, location)
	}
	c.leaseFile = f
	var info syscall.Stat_t
	if err := syscall.Fstat(int(f.Fd()), &info); err != nil {
		return nil, wPyErr(err)
	}
	if info.Mode&syscall.S_IFMT != syscall.S_IFREG || info.Nlink != 1 {
		return nil, errors.New("Setup operation lock is not a private regular file")
	}
	if err := flock(f, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, errors.New("A workspace operation is still running; cancel it before recovery")
		}
		return nil, wPyErr(err)
	}
	identity := devIno(&info)
	r := c.access(false, nil, true)
	if r.err != nil {
		return nil, r.err
	}
	lock := r.saved.Val("operation_lock")
	if lock == nil && create {
		if r := c.access(false, newObj("operation_lock", identity), false); r.err != nil {
			return nil, r.err
		}
	} else if !wPyEqual(lock, identity) {
		return nil, errors.New("Setup operation lock was replaced")
	}
	return f, nil
}

// devIno is [st_dev, st_ino] as JSON.
func devIno(info *syscall.Stat_t) []any {
	return []any{new(big.Int).SetUint64(uint64(info.Dev)), new(big.Int).SetUint64(uint64(info.Ino))}
}

// allocation is SetupControl.allocation(plan).
func (c *setupControl) allocation(plan *pyObj) any {
	if plan != nil {
		wtMust(c.access(false, newObj("allocation", plan), false).err)
	}
	r := c.access(false, nil, true)
	wtMust(r.err)
	return r.saved.Val("allocation")
}

// check returns SetupCancelled once cancellation was requested.
func (c *setupControl) check() error {
	r := c.access(false, nil, false)
	if r.err != nil {
		return r.err
	}
	if r.cancelled {
		return wtCancelled{}
	}
	return nil
}

func flock(f *os.File, how int) error {
	for {
		err := syscall.Flock(int(f.Fd()), how)
		if err != syscall.EINTR {
			return err
		}
	}
}

// ---- children -------------------------------------------------------------

// wtChild is a Popen with its output collected.
type wtChild struct {
	cmd    *exec.Cmd
	args   []string
	out    bytes.Buffer
	errb   bytes.Buffer
	done   chan struct{}
	exited bool
	rc     int
}

type wtSpawn struct {
	dir    string
	env    []string
	extra  []*os.File
	setsid bool
	stdout io.Writer // default: collected
	stderr io.Writer
	stdin  bool // inherit nothing when false (DEVNULL)
}

// wtStart is subprocess.Popen(argv, …).
func wtStart(argv []string, s wtSpawn) (*wtChild, error) {
	c := &wtChild{args: argv, done: make(chan struct{})}
	cmd := exec.Command(argv[0], argv[1:]...)
	if cmd.Err != nil {
		return nil, &pyOSError{errno: syscall.ENOENT, filename: &argv[0]}
	}
	cmd.Dir, cmd.Env, cmd.ExtraFiles = s.dir, s.env, s.extra
	cmd.Stdout, cmd.Stderr = &c.out, &c.errb
	if s.stdout != nil {
		cmd.Stdout = s.stdout
	}
	if s.stderr != nil {
		cmd.Stderr = s.stderr
	}
	setSession(cmd, s.setsid)
	if err := cmd.Start(); err != nil {
		if s.dir != "" {
			if _, statErr := os.Stat(s.dir); statErr != nil {
				return nil, wPyErr(statErr, s.dir)
			}
		}
		return nil, wPyErr(err, argv[0])
	}
	c.cmd = cmd
	go func() {
		err := cmd.Wait()
		c.rc = exitStatus(cmd, err)
		close(c.done)
	}()
	return c, nil
}

func (c *wtChild) pid() int { return c.cmd.Process.Pid }

// wait waits up to d (forever when d < 0) and reports whether it exited.
func (c *wtChild) wait(d time.Duration) bool {
	if d < 0 {
		<-c.done
		c.exited = true
		return true
	}
	select {
	case <-c.done:
		c.exited = true
		return true
	case <-time.After(d):
		return false
	}
}

// poll is Popen.poll() is not None.
func (c *wtChild) poll() bool { return c.wait(0) }

// text is the collected output as text=True gives it.
func (c *wtChild) text() (string, string, error) {
	out, err := wtReadText(c.out.Bytes())
	if err != nil {
		return "", "", err
	}
	errText, err := wtReadText(c.errb.Bytes())
	if err != nil {
		return "", "", err
	}
	return out, errText, nil
}

// killGroup is os.killpg(group, SIGKILL), a vanished group ignored.
func killGroup(group int) error {
	err := syscall.Kill(-group, syscall.SIGKILL)
	if err == nil || err == syscall.ESRCH {
		return nil
	}
	return wPyErr(err)
}

// wtAbort is the except-BaseException branch shared by the waits: a child
// still running is killed with its process group (or group), then reaped.
func wtAbort(c *wtChild, group int, cause error) error {
	if !c.poll() {
		if group == 0 {
			group = c.pid()
		}
		if err := killGroup(group); err != nil {
			return err
		}
	}
	c.wait(-1)
	return cause
}

// controlWait is SetupControl.wait: the child runs while cancellation is
// polled every 0.2s; cancellation or the deadline kill its process group.
func (c *setupControl) wait(child *wtChild, timeout float64, group int) (string, string, error) {
	deadline := time.Now().Add(time.Duration(timeout * float64(time.Second)))
	for {
		if err := c.check(); err != nil {
			return "", "", wtAbort(child, group, err)
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return "", "", wtAbort(child, group, &pyTimeoutError{argv: child.args, timeout: wPyFloatRepr(timeout)})
		}
		if child.wait(min(200*time.Millisecond, remaining)) {
			if err := c.check(); err != nil {
				return "", "", wtAbort(child, group, err)
			}
			out, errText, err := child.text()
			if err != nil {
				return "", "", wtAbort(child, group, err)
			}
			return out, errText, nil
		}
	}
}

// communicate is Popen.communicate(timeout): TimeoutExpired leaves the
// child running.
func (child *wtChild) communicate(timeout float64) (string, string, error) {
	if !child.wait(time.Duration(timeout * float64(time.Second))) {
		return "", "", &pyTimeoutError{argv: child.args, timeout: wPyFloatRepr(timeout)}
	}
	return child.text()
}

// selfHelper is the argv that runs another helper of this binary.
func selfHelper(name string, args ...string) []string {
	self, err := os.Executable()
	if err != nil {
		self = os.Args[0]
	}
	return append([]string{self, "helper", name}, args...)
}
