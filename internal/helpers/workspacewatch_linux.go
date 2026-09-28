//go:build linux

package helpers

import (
	"os"
	"syscall"
	"time"
)

// inotify events: modify, attrib, close_write, moved from/to, create,
// delete, delete_self and move_self.
const wfEvents = 0x2 | 0x4 | 0x8 | 0x40 | 0x80 | 0x100 | 0x200 | 0x400 | 0x800

// dirWatcher is an inotify descriptor on the watched folders (and .git).
type dirWatcher struct{ f *os.File }

// startWatch watches dirs, or returns a watcher that is not ok where the
// kernel offers no inotify or no folder could be watched.
func (w *wsFiles) startWatch(dirs []string) *dirWatcher {
	fd, err := syscall.InotifyInit1(syscall.IN_NONBLOCK | syscall.IN_CLOEXEC)
	if err != nil {
		return &dirWatcher{}
	}
	added := 0
	for _, d := range append(append([]string(nil), dirs...), ".git") {
		p, err := w.resolved(d)
		if err != nil {
			continue
		}
		if pyIsDir(p) {
			if _, err := syscall.InotifyAddWatch(fd, p, wfEvents); err == nil {
				added++
			}
		}
	}
	if added == 0 {
		syscall.Close(fd)
		return &dirWatcher{}
	}
	return &dirWatcher{os.NewFile(uintptr(fd), "inotify")}
}

func (d *dirWatcher) ok() bool { return d.f != nil }

// wait reports whether an event arrived within timeout.
func (d *dirWatcher) wait(timeout time.Duration) bool {
	d.f.SetReadDeadline(time.Now().Add(timeout))
	buf := make([]byte, 65536)
	n, err := d.f.Read(buf)
	return err == nil && n > 0
}

// drain discards the events that are already queued.
func (d *dirWatcher) drain() {
	buf := make([]byte, 65536)
	for {
		d.f.SetReadDeadline(time.Now().Add(time.Millisecond))
		if n, err := d.f.Read(buf); err != nil || n == 0 {
			return
		}
	}
}

func (d *dirWatcher) close() {
	if d.f != nil {
		d.f.Close()
	}
}
