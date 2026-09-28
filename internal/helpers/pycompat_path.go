package helpers

// posixpath, as the Python helpers use it. Targets are POSIX machines and
// these are the functions whose exact results the helpers' path checks and
// output depend on: realpath resolves as far as the path exists (unlike
// filepath.EvalSymlinks) and normpath keeps a leading "//".

import (
	"errors"
	"io/fs"
	"os"
	"os/user"
	"strings"
)

func wPyJoin(a string, parts ...string) string {
	path := a
	for _, b := range parts {
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

func pyIsAbs(p string) bool { return strings.HasPrefix(p, "/") }

func pyNormpath(p string) string {
	if p == "" {
		return "."
	}
	slashes := ""
	switch {
	case !strings.HasPrefix(p, "/"):
	case !strings.HasPrefix(p, "//") || strings.HasPrefix(p, "///"):
		slashes, p = "/", p[1:]
	default:
		slashes, p = "//", p[2:]
	}
	var comps []string
	for _, c := range strings.Split(p, "/") {
		if c == "" || c == "." {
			continue
		}
		if c != ".." || (slashes == "" && len(comps) == 0) || (len(comps) > 0 && comps[len(comps)-1] == "..") {
			comps = append(comps, c)
		} else if len(comps) > 0 {
			comps = comps[:len(comps)-1]
		}
	}
	out := slashes + strings.Join(comps, "/")
	if out == "" {
		return "."
	}
	return out
}

func pyGetcwd() string {
	wd, err := os.Getwd()
	if err != nil {
		return "/"
	}
	return wd
}

func pyAbspath(p string) string {
	if !pyIsAbs(p) {
		p = wPyJoin(pyGetcwd(), p)
	}
	return pyNormpath(p)
}

func wPyDirname(p string) string {
	i := strings.LastIndex(p, "/") + 1
	head := p[:i]
	if head != "" && head != strings.Repeat("/", len(head)) {
		head = strings.TrimRight(head, "/")
	}
	return head
}

func pyBasename(p string) string { return p[strings.LastIndex(p, "/")+1:] }

// pyRelpath is os.path.relpath(path, start); path must not be empty.
func pyRelpath(path, start string) string {
	split := func(p string) []string {
		tail := strings.TrimLeft(pyAbspath(p), "/")
		if tail == "" {
			return nil
		}
		return strings.Split(tail, "/")
	}
	s, p := split(start), split(path)
	i := 0
	for i < len(s) && i < len(p) && s[i] == p[i] {
		i++
	}
	var rel []string
	for j := i; j < len(s); j++ {
		rel = append(rel, "..")
	}
	rel = append(rel, p[i:]...)
	if len(rel) == 0 {
		return "."
	}
	return strings.Join(rel, "/")
}

// pyCommonpath is os.path.commonpath(paths).
func pyCommonpath(paths ...string) (string, error) {
	if len(paths) == 0 {
		return "", errors.New("commonpath() arg is an empty sequence")
	}
	abs := pyIsAbs(paths[0])
	var split [][]string
	for _, p := range paths {
		if pyIsAbs(p) != abs {
			return "", errors.New("Can't mix absolute and relative paths")
		}
		var comps []string
		for _, c := range strings.Split(p, "/") {
			if c != "" && c != "." {
				comps = append(comps, c)
			}
		}
		split = append(split, comps)
	}
	common := split[0]
	for _, s := range split[1:] {
		n := 0
		for n < len(common) && n < len(s) && common[n] == s[n] {
			n++
		}
		common = common[:n]
	}
	prefix := ""
	if abs {
		prefix = "/"
	}
	return prefix + strings.Join(common, "/"), nil
}

// pyInside is commonpath([root, p]) == root, false when the paths mix.
func pyInside(p, root string) bool {
	c, err := pyCommonpath(root, p)
	return err == nil && c == root
}

// pyRealpath is os.path.realpath (Python 3.13, strict=False): symlinks are
// resolved while the path exists, the rest is kept as written, and a loop
// leaves the looping link unresolved.
func pyRealpath(filename string) string {
	parts := strings.Split(filename, "/")
	// rest is a stack; a nil marker means "the link below me is resolved".
	type item struct {
		name   string
		marker bool
	}
	rest := make([]item, 0, len(parts))
	for i := len(parts) - 1; i >= 0; i-- {
		rest = append(rest, item{name: parts[i]})
	}
	count := len(parts)
	path := "/"
	if !strings.HasPrefix(filename, "/") {
		path = pyGetcwd()
	}
	seen := map[string]*string{}
	pop := func() item {
		it := rest[len(rest)-1]
		rest = rest[:len(rest)-1]
		return it
	}
	for count > 0 {
		it := pop()
		if it.marker {
			resolved := path
			seen[pop().name] = &resolved
			continue
		}
		count--
		name := it.name
		if name == "" || name == "." {
			continue
		}
		if name == ".." {
			path = path[:strings.LastIndex(path, "/")]
			if path == "" {
				path = "/"
			}
			continue
		}
		newpath := path + "/" + name
		if path == "/" {
			newpath = path + name
		}
		info, err := os.Lstat(newpath)
		if err != nil {
			path = newpath
			continue
		}
		if info.Mode()&fs.ModeSymlink == 0 {
			path = newpath
			continue
		}
		if cached, ok := seen[newpath]; ok {
			if cached != nil {
				path = *cached
				continue
			}
			path = newpath // a loop
			continue
		}
		target, err := os.Readlink(newpath)
		if err != nil {
			path = newpath
			continue
		}
		if strings.HasPrefix(target, "/") {
			path = "/"
		}
		seen[newpath] = nil
		rest = append(rest, item{name: newpath}, item{marker: true})
		tparts := strings.Split(target, "/")
		for i := len(tparts) - 1; i >= 0; i-- {
			rest = append(rest, item{name: tparts[i]})
		}
		count += len(tparts)
	}
	return path
}

// wPyExpanduser is os.path.expanduser.
func wPyExpanduser(p string) string {
	if !strings.HasPrefix(p, "~") {
		return p
	}
	i := strings.Index(p[1:], "/") + 1
	if i == 0 {
		i = len(p)
	}
	var home string
	if i == 1 {
		if h, ok := os.LookupEnv("HOME"); ok {
			home = h
		} else if u, err := user.Current(); err == nil {
			home = u.HomeDir
		} else {
			return p
		}
	} else {
		u, err := user.Lookup(p[1:i])
		if err != nil {
			return p
		}
		home = u.HomeDir
	}
	home = strings.TrimRight(home, "/")
	if out := home + p[i:]; out != "" {
		return out
	}
	return "/"
}

// The os.path predicates: any error is false.

func pyExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func pyLexists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

func pyIsDir(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
}

func pyIsFile(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.Mode().IsRegular()
}

func pyIsLink(p string) bool {
	info, err := os.Lstat(p)
	return err == nil && info.Mode()&fs.ModeSymlink != 0
}
