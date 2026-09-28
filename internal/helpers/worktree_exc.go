package helpers

// The worktree helpers are ports of exception-driven Python, and which
// handler catches a failure decides what the caller sees. Rather than thread
// that through every return, they raise (panic with wtExc) and catch by
// Python's exception classes: OSError, ValueError, TimeoutExpired, and
// KeyError/TypeError, which the single-checkout script never caught.

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

type wtExc struct{ err error }

func wtRaise(err error) { panic(wtExc{err}) }

func wtValue(msg string) { wtRaise(errors.New(msg)) }

// wtMust raises err, if any.
func wtMust(err error) {
	if err != nil {
		wtRaise(err)
	}
}

// wtTry runs fn and returns what it raised.
func wtTry(fn func()) (err error) {
	defer func() {
		if r := recover(); r != nil {
			exc, ok := r.(wtExc)
			if !ok {
				panic(r)
			}
			err = exc.err
		}
	}()
	fn()
	return nil
}

// wtKeyError is KeyError: str() is the key's repr.
type wtKeyError struct{ key string }

func (e *wtKeyError) Error() string { return pyReprString(e.key) }

// wtTypeError is TypeError or AttributeError: a record of the wrong shape.
type wtTypeError struct{ msg string }

func (e *wtTypeError) Error() string { return e.msg }

// wtCancelled is SetupCancelled, a ValueError.
type wtCancelled struct{}

func (wtCancelled) Error() string {
	return "Workspace setup cancelled; allocated files retained for inspection"
}

func wtIsOSError(err error) bool {
	_, ok := pyErrno(err)
	return ok
}

func wtIsTimeout(err error) bool {
	var t *pyTimeoutError
	return errors.As(err, &t)
}

func wtIsLookup(err error) bool {
	var k *wtKeyError
	var ty *wtTypeError
	return errors.As(err, &k) || errors.As(err, &ty)
}

// wtIsValue is ValueError (with its JSON and Unicode subclasses): anything
// that is none of the other classes.
func wtIsValue(err error) bool {
	return !wtIsOSError(err) && !wtIsTimeout(err) && !wtIsLookup(err)
}

// wtUncaught ends the process the way an uncaught Python exception does:
// a traceback on stderr, status 1, nothing more on stdout.
func wtUncaught(stderr io.Writer, err error) int {
	class := "ValueError"
	switch {
	case wtIsTimeout(err):
		class = "subprocess.TimeoutExpired"
	case wtIsOSError(err):
		class = "OSError"
	default:
		var k *wtKeyError
		if errors.As(err, &k) {
			class = "KeyError"
		} else if wtIsLookup(err) {
			class = "TypeError"
		}
	}
	fmt.Fprintf(stderr, "Traceback (most recent call last):\n  (lectern helper)\n%s: %s\n", class, err)
	return 1
}

// ---- plan access: dict[key] with KeyError / TypeError ----------------------

func wtObj(v any) *pyObj {
	o, ok := v.(*pyObj)
	if !ok {
		wtRaise(&wtTypeError{fmt.Sprintf("'%s' object is not subscriptable", pyTypeName(v))})
	}
	return o
}

// wtItem is o[key].
func wtItem(o any, key string) any {
	v, ok := wtObj(o).Get(key)
	if !ok {
		wtRaise(&wtKeyError{key})
	}
	return v
}

// wtString is o[key] used as a str.
func wtString(o any, key string) string {
	v := wtItem(o, key)
	s, ok := v.(string)
	if !ok {
		wtRaise(&wtTypeError{fmt.Sprintf("'%s' object has no attribute 'startswith'", pyTypeName(v))})
	}
	return s
}

// wtList is o[key] used as a list.
func wtList(o any, key string) []any {
	v := wtItem(o, key)
	l, ok := v.([]any)
	if !ok {
		wtRaise(&wtTypeError{fmt.Sprintf("'%s' object is not iterable", pyTypeName(v))})
	}
	return l
}

// wtGetString is o.get(key, "") used as a str.
func wtGetString(o *pyObj, key string) string {
	v, ok := o.Get(key)
	if !ok {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		wtRaise(&wtTypeError{fmt.Sprintf("'%s' object has no attribute 'strip'", pyTypeName(v))})
	}
	return s
}

// wtPlan parses a JSON argument as the scripts' json.loads.
func wtPlan(raw string) any {
	v, err := pyLoads(raw)
	wtMust(err)
	return v
}

// pathlibStr is str(pathlib.Path(p)): repeated and trailing slashes and "."
// parts dropped, a leading "//" kept.
func pathlibStr(p string) string {
	prefix := ""
	switch {
	case strings.HasPrefix(p, "//") && !strings.HasPrefix(p, "///"):
		prefix = "//"
	case strings.HasPrefix(p, "/"):
		prefix = "/"
	}
	var parts []string
	for _, c := range strings.Split(p, "/") {
		if c != "" && c != "." {
			parts = append(parts, c)
		}
	}
	out := prefix + strings.Join(parts, "/")
	if out == "" {
		return "."
	}
	return out
}

// pathlibJoin is str(Path(base) / name).
func pathlibJoin(base, name string) string {
	b := pathlibStr(base)
	if b == "." {
		return pathlibStr(name)
	}
	return pathlibStr(b + "/" + name)
}

// pathlibParent is str(Path(p).parent).
func pathlibParent(p string) string {
	s := pathlibStr(p)
	i := strings.LastIndex(s, "/")
	switch {
	case i < 0:
		return "."
	case s == "/" || s == "//":
		return s
	case i == 0:
		return "/"
	case i == 1 && strings.HasPrefix(s, "//"):
		return "//"
	}
	return s[:i]
}

// pathlibMkdir is Path(p).mkdir(mode, parents, exist_ok).
func pathlibMkdir(p string, mode os.FileMode, parents, existOK bool) error {
	err := os.Mkdir(p, mode)
	if err == nil {
		return nil
	}
	if errors.Is(err, os.ErrNotExist) {
		parent := pathlibParent(p)
		if !parents || parent == pathlibStr(p) {
			return pyErr(err, p)
		}
		if err := pathlibMkdir(parent, 0o777, true, true); err != nil {
			return err
		}
		return pathlibMkdir(p, mode, false, existOK)
	}
	if !existOK || !pyIsDir(p) {
		return pyErr(err, p)
	}
	return nil
}

// wtReadText is a text-mode read: strict UTF-8, universal newlines.
func wtReadText(data []byte) (string, error) {
	s, err := pyDecodeStrict(data)
	if err != nil {
		return "", err
	}
	return pyUniversalNewlines(s), nil
}
