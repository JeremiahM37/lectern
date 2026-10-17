package exec

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Background jobs normally die with the Scribe process that started them
// (KillAll on exit). If that process is killed, its job groups keep running.
// Each Scribe process therefore writes one record file under StateDir while
// it has running jobs, and a later process stops the jobs of a record whose
// owner is gone. Records are only acted on when both the owner and each job
// still match the start time written down for them, so a reused pid is never
// signalled.

// recordPrefix and recordSuffix frame the record file name: jobs-<pid>.json.
const (
	recordPrefix = "jobs-"
	recordSuffix = ".json"
	// reapBudget bounds how long one reap pass may take in total.
	reapBudget = 10 * time.Second
)

// jobRecord is one running background job as the record file holds it.
type jobRecord struct {
	ID      string `json:"id"`
	Pid     int    `json:"pid"`   // the job's process group leader; its pgid is Pid
	Start   uint64 `json:"start"` // /proc start time of Pid; 0 when unknown
	Command string `json:"command"`
}

// recordFile is the content of one jobs-<pid>.json.
type recordFile struct {
	ScribePid   int         `json:"scribe_pid"`
	ScribeStart uint64      `json:"scribe_start"`
	Jobs        []jobRecord `json:"jobs"`
}

// recordPath returns the record file a process with pid writes under dir.
func recordPath(dir string, pid int) string {
	return filepath.Join(dir, recordPrefix+strconv.Itoa(pid)+recordSuffix)
}

// writeRecord replaces the record file at path with rec. It writes a temporary
// file and renames it, so a reader never sees a half-written record.
func writeRecord(path string, rec recordFile) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	raw, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// clearRecord removes a record file that no longer lists any running job.
func clearRecord(path string) {
	_ = os.Remove(path)
}

// ownerPidOf parses the pid out of a record file name, or 0 when the name is
// not a record.
func ownerPidOf(name string) int {
	if !strings.HasPrefix(name, recordPrefix) || !strings.HasSuffix(name, recordSuffix) {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(name, recordPrefix), recordSuffix))
	if err != nil {
		return 0
	}
	return pid
}
