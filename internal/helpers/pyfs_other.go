//go:build !linux

package helpers

// dirFD without the Linux *at calls: the directory is tracked by path, and
// every component is checked with lstat so a symlink is refused rather than
// followed. It is weaker against a concurrent swap than the Linux version.

import (
	"errors"
	"os"
	"syscall"
)

type dirFD struct {
	path string
}

func noFollowDir(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return syscall.Errno(syscall.ELOOP)
	}
	if !info.IsDir() {
		return syscall.Errno(syscall.ENOTDIR)
	}
	return nil
}

func openDirPath(path string) (*dirFD, error) {
	if err := noFollowDir(path); err != nil {
		return nil, wPyErr(err, path)
	}
	return &dirFD{path: path}, nil
}

func (d *dirFD) child(name string) string {
	if d.path == "/" {
		return "/" + name
	}
	return d.path + "/" + name
}

func (d *dirFD) openDir(name string) (*dirFD, error) {
	if err := noFollowDir(d.child(name)); err != nil {
		return nil, wPyErr(err, name)
	}
	return &dirFD{path: d.child(name)}, nil
}

func (d *dirFD) mkdir(name string, mode uint32) error {
	return wPyErr(os.Mkdir(d.child(name), os.FileMode(mode)), name)
}

func (d *dirFD) lstat(name string) (os.FileInfo, error) {
	info, err := os.Lstat(d.child(name))
	return info, wPyErr(err, name)
}

func (d *dirFD) open(name string, flag int, perm uint32, nofollow bool) (*os.File, error) {
	if nofollow {
		if info, err := os.Lstat(d.child(name)); err == nil && info.Mode()&os.ModeSymlink != 0 {
			return nil, wPyErr(syscall.Errno(syscall.ELOOP), name)
		}
	}
	f, err := os.OpenFile(d.child(name), flag, os.FileMode(perm))
	if err != nil {
		return nil, wPyErr(err, name)
	}
	return f, nil
}

func (d *dirFD) link(old, new string) error {
	err := os.Link(d.child(old), d.child(new))
	var le *os.LinkError
	if errors.As(err, &le) {
		return wPyErr(le.Err, old, new)
	}
	return err
}

func (d *dirFD) unlink(name string) error {
	return wPyErr(os.Remove(d.child(name)), name)
}

func (d *dirFD) symlink(target, name string) error {
	err := os.Symlink(target, d.child(name))
	var le *os.LinkError
	if errors.As(err, &le) {
		return wPyErr(le.Err, target, name)
	}
	return err
}

func (d *dirFD) readlink(name string) (string, error) {
	target, err := os.Readlink(d.child(name))
	return target, wPyErr(err, name)
}

func (d *dirFD) stat() (os.FileInfo, error) { return os.Stat(d.path) }

func (d *dirFD) close() {}
