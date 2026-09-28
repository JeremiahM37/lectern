//go:build linux

package helpers

// dirFD is a directory held open for the dir_fd= operations the Python
// helpers use, so a later change to a path above it cannot redirect them.
// On Linux these are the real *at system calls.

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

type dirFD struct {
	fd   int
	path string
}

const dirFlags = syscall.O_RDONLY | syscall.O_DIRECTORY | syscall.O_NOFOLLOW | syscall.O_CLOEXEC

// openDirPath opens path itself as a directory, refusing a final symlink.
func openDirPath(path string) (*dirFD, error) {
	fd, err := syscall.Open(path, dirFlags, 0)
	if err != nil {
		return nil, pyErr(err, path)
	}
	return &dirFD{fd: fd, path: path}, nil
}

func (d *dirFD) child(name string) string {
	if d.path == "/" {
		return "/" + name
	}
	return d.path + "/" + name
}

// openDir is os.open(name, O_RDONLY|O_DIRECTORY|O_NOFOLLOW, dir_fd=d).
func (d *dirFD) openDir(name string) (*dirFD, error) {
	fd, err := syscall.Openat(d.fd, name, dirFlags, 0)
	if err != nil {
		return nil, pyErr(err, name)
	}
	return &dirFD{fd: fd, path: d.child(name)}, nil
}

func (d *dirFD) mkdir(name string, mode uint32) error {
	return pyErr(syscall.Mkdirat(d.fd, name, mode), name)
}

// lstat is os.stat(name, dir_fd=d, follow_symlinks=False), through the
// descriptor's /proc entry (fstatat is not in package syscall everywhere).
func (d *dirFD) lstat(name string) (os.FileInfo, error) {
	via := fmt.Sprintf("/proc/self/fd/%d/%s", d.fd, name)
	if _, err := os.Stat("/proc/self/fd"); err != nil {
		via = d.child(name)
	}
	info, err := os.Lstat(via)
	if err != nil {
		return nil, pyErr(err, name)
	}
	return info, nil
}

// open is os.open(name, flag, perm, dir_fd=d); nofollow adds O_NOFOLLOW.
func (d *dirFD) open(name string, flag int, perm uint32, nofollow bool) (*os.File, error) {
	if nofollow {
		flag |= syscall.O_NOFOLLOW
	}
	fd, err := syscall.Openat(d.fd, name, flag|syscall.O_CLOEXEC, perm)
	if err != nil {
		return nil, pyErr(err, name)
	}
	return os.NewFile(uintptr(fd), d.child(name)), nil
}

// link is os.link(old, new, src_dir_fd=d, dst_dir_fd=d, follow_symlinks=False).
func (d *dirFD) link(old, new string) error {
	o, err := syscall.BytePtrFromString(old)
	if err != nil {
		return err
	}
	n, err := syscall.BytePtrFromString(new)
	if err != nil {
		return err
	}
	_, _, errno := syscall.Syscall6(syscall.SYS_LINKAT, uintptr(d.fd), uintptr(unsafe.Pointer(o)),
		uintptr(d.fd), uintptr(unsafe.Pointer(n)), 0, 0)
	if errno != 0 {
		return pyErr(errno, old, new)
	}
	return nil
}

func (d *dirFD) unlink(name string) error {
	return pyErr(syscall.Unlinkat(d.fd, name), name)
}

// symlink is os.symlink(target, name, dir_fd=d).
func (d *dirFD) symlink(target, name string) error {
	t, err := syscall.BytePtrFromString(target)
	if err != nil {
		return err
	}
	n, err := syscall.BytePtrFromString(name)
	if err != nil {
		return err
	}
	_, _, errno := syscall.Syscall(syscall.SYS_SYMLINKAT, uintptr(unsafe.Pointer(t)), uintptr(d.fd), uintptr(unsafe.Pointer(n)))
	if errno != 0 {
		return pyErr(errno, target, name)
	}
	return nil
}

// readlink is os.readlink(name, dir_fd=d).
func (d *dirFD) readlink(name string) (string, error) {
	n, err := syscall.BytePtrFromString(name)
	if err != nil {
		return "", err
	}
	for size := 256; ; size *= 2 {
		buf := make([]byte, size)
		r, _, errno := syscall.Syscall6(syscall.SYS_READLINKAT, uintptr(d.fd), uintptr(unsafe.Pointer(n)),
			uintptr(unsafe.Pointer(&buf[0])), uintptr(size), 0, 0)
		if errno != 0 {
			return "", pyErr(errno, name)
		}
		if int(r) < size {
			return string(buf[:r]), nil
		}
	}
}

// stat is os.fstat(d), read through /proc so the result compares with
// os.SameFile; without /proc it falls back to the path.
func (d *dirFD) stat() (os.FileInfo, error) {
	if info, err := os.Stat(fmt.Sprintf("/proc/self/fd/%d", d.fd)); err == nil {
		return info, nil
	}
	return os.Stat(d.path)
}

func (d *dirFD) close() {
	if d != nil && d.fd >= 0 {
		syscall.Close(d.fd)
		d.fd = -1
	}
}
