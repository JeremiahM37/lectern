//go:build !windows

package exec

import (
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
// job records.
var selfStartTicks = procStartTicks(os.Getpid())

// persistRecord writes this process's running jobs to its record file under
// dir, or removes the file when none are running. Called on job start and
// exit only, never per output line.
func persistRecord(dir string, jobs []jobRecord) {
	path := recordPath(dir, os.Getpid())
	if len(jobs) == 0 {
		clearRecord(path)
		return
	}
	_ = writeRecord(path, recordFile{
		ScribePid:   os.Getpid(),
		ScribeStart: selfStartTicks,
		Jobs:        jobs,
	})
}

// reapOrphans stops the background jobs recorded under dir by Scribe processes
// that are no longer running, then removes their records. A record is acted on
// only when its owner's start time is known and no longer matches (the owner
// is gone); each job is signalled only when its pid still matches the recorded
// start time and it leads its own process group. The pass gives up after
// reapBudget, and records it did not finish stay for the next pass.
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
			// Unreadable, or no owner start time to compare against: leave it.
			continue
		}
		if procStartTicks(owner) == rec.ScribeStart {
			continue // the owner is still running, and its jobs are its own
		}
		for _, job := range rec.Jobs {
			if job.Pid <= 0 || job.Start == 0 || procStartTicks(job.Pid) != job.Start {
				continue // gone, or the pid now names another process
			}
			if pgid, err := syscall.Getpgid(job.Pid); err != nil || pgid != job.Pid {
				continue
			}
			stopGroupBy(job.Pid, deadline)
		}
		_ = os.Remove(path)
	}
}

// stopGroupBy asks the process group led by pid to stop, then force-kills it
// if it is still there at the deadline.
func stopGroupBy(pid int, deadline time.Time) {
	terminateGroup(pid)
	for time.Now().Before(deadline) {
		if syscall.Kill(-pid, 0) != nil { // group empty
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	killGroup(pid)
}
