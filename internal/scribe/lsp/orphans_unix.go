//go:build unix

package lsp

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// procStartTicks returns the start time of pid from /proc (field 22 of stat,
// in clock ticks since boot), or 0 when it cannot be read. Without procfs
// (macOS) it returns 0, and the reaper then does nothing: it cannot prove a
// pid still names the same process, so it never signals one.
func procStartTicks(pid int) uint64 {
	if pid <= 0 {
		return 0
	}
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0
	}
	// comm may contain spaces and parens; fields start after the last ')'.
	s := string(raw)
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return 0
	}
	fields := strings.Fields(s[i+1:])
	// fields[0] is field 3 (state), so starttime (field 22) is fields[19].
	if len(fields) < 20 {
		return 0
	}
	v, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil {
		return 0
	}
	return v
}

// selfStartTicks is this process's start time, recorded as the owner of its
// server records.
var selfStartTicks = procStartTicks(os.Getpid())

// argvNames reports whether one argument of pid, after its directory is
// stripped, is name. A script run through an interpreter shows the script
// as an argument, not as argv[0], so both places are checked.
func argvNames(pid int, name string) bool {
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cmdline")
	if err != nil {
		return false
	}
	for _, arg := range bytes.Split(raw, []byte{0}) {
		if len(arg) > 0 && filepath.Base(string(arg)) == name {
			return true
		}
	}
	return false
}

// serverIsOurs reports whether pid still names the server srv describes: the
// same start time, the recorded program among its arguments, and it leads its
// own process group, which is how the server was started.
func serverIsOurs(srv serverRecord) bool {
	if srv.Pid <= 0 || srv.Start == 0 || srv.Bin == "" {
		return false
	}
	if procStartTicks(srv.Pid) != srv.Start {
		return false
	}
	if !argvNames(srv.Pid, filepath.Base(srv.Bin)) {
		return false
	}
	pgid, err := syscall.Getpgid(srv.Pid)
	return err == nil && pgid == srv.Pid
}

// reapOrphans stops the servers recorded under dir by Scribe processes that
// are no longer running, then removes their records. A record is acted on
// only when its owner's start time is known and no longer matches (the owner
// is gone). The pass gives up after reapBudget, and a record it did not
// finish stays for the next pass.
func reapOrphans(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	deadline := time.Now().Add(reapBudget)
	self := os.Getpid()
	for _, e := range entries {
		owner := ownerPidOf(e.Name())
		if owner == 0 || owner == self || time.Now().After(deadline) {
			continue
		}
		path := filepath.Join(dir, e.Name())
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var rec recordFile
		if err := json.Unmarshal(raw, &rec); err != nil || rec.ScribePid != owner || rec.ScribeStart == 0 {
			continue // unreadable, or no owner start time to compare against
		}
		if procStartTicks(owner) == rec.ScribeStart {
			continue // the owner still runs, and its servers are its own
		}
		for _, srv := range rec.Servers {
			if serverIsOurs(srv) {
				stopGroup(srv.Pid, deadline)
			}
		}
		_ = os.Remove(path)
	}
}

// stopGroup asks the process group led by pid to stop, then kills it if it is
// still there at the deadline.
func stopGroup(pid int, deadline time.Time) {
	_ = syscall.Kill(-pid, syscall.SIGTERM)
	for time.Now().Before(deadline) {
		if syscall.Kill(-pid, 0) != nil { // group empty
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	_ = syscall.Kill(-pid, syscall.SIGKILL)
}
