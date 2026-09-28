package helpers

// OSError text and subprocess behaviour as the Python helpers had them.
// Several helpers print str(error) to their caller, so a failed open reads
// "[Errno 2] No such file or directory: 'x'" from either version.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math/big"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// pyOSError is an OSError with errno, strerror and up to two file names.
type pyOSError struct {
	errno     syscall.Errno
	filename  *string
	filename2 *string
}

func (e *pyOSError) Error() string {
	s := fmt.Sprintf("[Errno %d] %s", int(e.errno), pyStrerror(e.errno))
	if e.filename != nil {
		s += ": " + pyReprString(*e.filename)
		if e.filename2 != nil {
			s += " -> " + pyReprString(*e.filename2)
		}
	}
	return s
}

func (e *pyOSError) Unwrap() error { return e.errno }

// Strerror is OSError.strerror.
func (e *pyOSError) Strerror() string { return pyStrerror(e.errno) }

// pyStrerror is the C library's message: Go's table in sentence case.
func pyStrerror(errno syscall.Errno) string {
	msg := errno.Error()
	if msg == "" {
		return fmt.Sprintf("Unknown error %d", int(errno))
	}
	return strings.ToUpper(msg[:1]) + msg[1:]
}

// wPyErr turns a Go error from a filesystem call into the OSError Python
// would have raised naming names (none, one, or source and destination).
// An error with no errno is returned unchanged.
func wPyErr(err error, names ...string) error {
	if err == nil {
		return nil
	}
	var pe *pyOSError
	if errors.As(err, &pe) {
		return err
	}
	var errno syscall.Errno
	if !errors.As(err, &errno) {
		return err
	}
	out := &pyOSError{errno: errno}
	if len(names) > 0 {
		out.filename = &names[0]
	}
	if len(names) > 1 {
		out.filename2 = &names[1]
	}
	return out
}

// pyErrno reports err's errno, if it carries one.
func pyErrno(err error) (syscall.Errno, bool) {
	var errno syscall.Errno
	ok := errors.As(err, &errno)
	return errno, ok
}

// pyStrerrorOf is `e.strerror if isinstance(e, OSError) and e.strerror else
// str(e)`.
func pyStrerrorOf(err error) string {
	if errno, ok := pyErrno(err); ok {
		return pyStrerror(errno)
	}
	return err.Error()
}

// pyTimeoutError is subprocess.TimeoutExpired.
type pyTimeoutError struct {
	argv    []string
	timeout string
}

func (e *pyTimeoutError) Error() string {
	return fmt.Sprintf("Command '%s' timed out after %s seconds", wPyRepr(e.argv), e.timeout)
}

// pyTimeout formats a timeout as Python prints the number it was given.
func pyTimeout(seconds float64, integral bool) string {
	if integral {
		return fmt.Sprintf("%d", int64(seconds))
	}
	return wPyFloatRepr(seconds)
}

// pyRun is subprocess.run(argv) capturing both outputs: the exit status
// (negative for a signal, as Python reports it), a missing program as
// FileNotFoundError and a timeout as TimeoutExpired, with the child killed.
type pyRun struct {
	Argv    []string
	Dir     string
	Env     []string // nil: inherit
	Stdin   []byte
	Timeout float64 // seconds; 0: none
	// TimeoutText is how the error prints Timeout ("15", "0.3").
	TimeoutText string
	// Stdout / Stderr, when set, receive the output instead of a buffer.
	Stdout, Stderr io.Writer
	ExtraFiles     []*os.File
	Setsid         bool
}

type pyResult struct {
	RC             int
	Stdout, Stderr []byte
}

func (r pyRun) run() (pyResult, error) {
	var res pyResult
	ctx := context.Background()
	cancel := func() {}
	if r.Timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, time.Duration(r.Timeout*float64(time.Second)))
	}
	defer cancel()
	cmd := exec.Command(r.Argv[0], r.Argv[1:]...)
	if _, err := exec.LookPath(r.Argv[0]); err != nil {
		return res, &pyOSError{errno: syscall.ENOENT, filename: &r.Argv[0]}
	}
	cmd.Dir = r.Dir
	cmd.Env = r.Env
	cmd.ExtraFiles = r.ExtraFiles
	if r.Stdin != nil {
		cmd.Stdin = bytes.NewReader(r.Stdin)
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if r.Stdout != nil {
		cmd.Stdout = r.Stdout
	}
	if r.Stderr != nil {
		cmd.Stderr = r.Stderr
	}
	setSession(cmd, r.Setsid)
	if err := cmd.Start(); err != nil {
		return res, wPyErr(err, r.Argv[0])
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		res.Stdout, res.Stderr = out.Bytes(), errb.Bytes()
		res.RC = exitStatus(cmd, err)
		return res, nil
	case <-ctx.Done():
		_ = cmd.Process.Kill()
		<-done
		text := r.TimeoutText
		if text == "" {
			text = wPyFloatRepr(r.Timeout)
		}
		return res, &pyTimeoutError{argv: r.Argv, timeout: text}
	}
}

// exitStatus is Popen.returncode: the exit code, or -signal.
func exitStatus(cmd *exec.Cmd, err error) int {
	if cmd.ProcessState == nil {
		return -1
	}
	if ws, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return -int(ws.Signal())
	}
	return cmd.ProcessState.ExitCode()
}

// pyEnviron is dict(os.environ, **extra) as an exec environment.
func pyEnviron(extra ...string) []string {
	env := os.Environ()
	for i := 0; i+1 < len(extra); i += 2 {
		prefix := extra[i] + "="
		kept := env[:0:0]
		for _, kv := range env {
			if !strings.HasPrefix(kv, prefix) {
				kept = append(kept, kv)
			}
		}
		env = append(kept, prefix+extra[i+1])
	}
	return env
}

// text is how text=True reads a child's output: strict UTF-8 (a decoding
// failure is a UnicodeDecodeError) with universal newlines.
func (r pyResult) text(data []byte) (string, error) {
	s, err := pyDecodeStrict(data)
	if err != nil {
		return "", err
	}
	return wPyUniversalNewlines(s), nil
}

// wPyMakedirs is os.makedirs(name, mode, exist_ok): parents missing on the way
// are made with the default 0o777, only the last with mode.
func wPyMakedirs(name string, mode os.FileMode, existOK bool) error {
	head, tail := wPySplit(name)
	if tail == "" {
		head, tail = wPySplit(head)
	}
	if head != "" && tail != "" && !pyExists(head) {
		if err := wPyMakedirs(head, 0o777, existOK); err != nil && pyErrKind(err) != "FileExistsError" {
			return err
		}
		if tail == "." {
			return nil
		}
	}
	if err := os.Mkdir(name, mode); err != nil {
		if !existOK || !pyIsDir(name) {
			return wPyErr(err, name)
		}
	}
	return nil
}

// wPySplit is os.path.split.
func wPySplit(p string) (string, string) {
	i := strings.LastIndex(p, "/") + 1
	head, tail := p[:i], p[i:]
	if head != "" && head != strings.Repeat("/", len(head)) {
		head = strings.TrimRight(head, "/")
	}
	return head, tail
}

const tempChars = "abcdefghijklmnopqrstuvwxyz0123456789_"

// pyMkstemp is tempfile.mkstemp(prefix=prefix, dir=dir): a new 0600 file
// with eight random name characters.
func pyMkstemp(dir, prefix string) (*os.File, string, error) {
	var last error
	for range 100 {
		path := pyAbspath(wPyJoin(dir, prefix+tempName()))
		f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL|oNoFollow, 0o600)
		if err == nil {
			return f, path, nil
		}
		if pyErrKind(err) != "FileExistsError" {
			return nil, "", wPyErr(err, path)
		}
		last = wPyErr(err, path)
	}
	return nil, "", last
}

// pyReplace is os.replace(src, dst).
func pyReplace(src, dst string) error {
	err := os.Rename(src, dst)
	var le *os.LinkError
	if errors.As(err, &le) {
		return wPyErr(le.Err, src, dst)
	}
	return err
}

// pyKeyError is KeyError(key); str() of it is the key's repr.
type pyKeyError struct{ key string }

func (e *pyKeyError) Error() string { return pyReprString(e.key) }

// pyTypeName is type(v).__name__ for a JSON value.
func pyTypeName(v any) string {
	switch v.(type) {
	case nil:
		return "NoneType"
	case bool:
		return "bool"
	case int, int64, *big.Int:
		return "int"
	case float64:
		return "float"
	case string:
		return "str"
	case []any:
		return "list"
	case *pyObj:
		return "dict"
	}
	return "object"
}
