//go:build unix

package nvim

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// sweepStale removes sockets left by Scribe processes that have exited, such
// as after a crash. A socket belongs to the process named in its file name,
// and is kept while that process exists.
func sweepStale() {
	matches, _ := filepath.Glob(filepath.Join(os.TempDir(), "scribe-nvim-*.sock"))
	for _, m := range matches {
		name := strings.TrimSuffix(filepath.Base(m), ".sock")
		parts := strings.Split(strings.TrimPrefix(name, "scribe-nvim-"), "-")
		pid, err := strconv.Atoi(parts[0])
		if err != nil || pid <= 0 || pid == os.Getpid() {
			continue
		}
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			_ = os.Remove(m)
		}
	}
}
