package lsp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// A language server is started in its own process group, and a Scribe that
// is killed without running Shutdown leaves those servers running. Each
// Scribe process therefore writes one record file under the state directory
// while it has servers running, and a later process stops the servers in a
// record whose owner is gone. A record is acted on only when the owner and
// each server still match the start time written down for them, and the
// server still runs the program that was recorded, so a reused pid is never
// signalled.

const (
	// recordPrefix and recordSuffix frame the record name: lsp-<pid>.json.
	recordPrefix = "lsp-"
	recordSuffix = ".json"
	// reapBudget bounds how long one reap pass may take in total.
	reapBudget = 10 * time.Second
)

// serverRecord is one running server as the record file holds it.
type serverRecord struct {
	Pid     int    `json:"pid"`   // the server's process group leader
	Start   uint64 `json:"start"` // /proc start time of Pid; 0 when unknown
	Bin     string `json:"bin"`   // the binary as found on the machine
	Command string `json:"command"`
}

// recordFile is the content of one lsp-<pid>.json.
type recordFile struct {
	ScribePid   int            `json:"scribe_pid"`
	ScribeStart uint64         `json:"scribe_start"`
	Servers     []serverRecord `json:"servers"`
}

// ReapOrphans stops the language servers recorded under dir by Scribe
// processes that are no longer running, then removes their records. The app
// calls it at boot. An empty dir does nothing.
func ReapOrphans(dir string) {
	if dir == "" {
		return
	}
	reapOrphans(dir)
}

// persistLocked writes this process's running servers to its record file, or
// removes the file when none are running. Callers hold st.mu. It does nothing
// until a state directory is set, and where process start times are not
// readable it writes nothing, because the reaper could not confirm a pid
// anyway.
func (st *sessionStatus) persistLocked() {
	if st.dir == "" || selfStartTicks == 0 {
		return
	}
	path := recordPath(st.dir, os.Getpid())
	var servers []serverRecord
	for _, s := range st.live {
		if pid := s.pid(); pid > 0 && s.startTicks != 0 && s.alive() {
			servers = append(servers, serverRecord{Pid: pid, Start: s.startTicks, Bin: s.bin, Command: s.command})
		}
	}
	if len(servers) == 0 {
		_ = os.Remove(path)
		return
	}
	_ = writeRecord(path, recordFile{ScribePid: os.Getpid(), ScribeStart: selfStartTicks, Servers: servers})
}

// SetStateDir sets the directory where running servers are recorded for
// ReapOrphans. An empty dir turns recording off, as in ephemeral runs.
func (m *Manager) SetStateDir(dir string) {
	m.status.mu.Lock()
	defer m.status.mu.Unlock()
	m.status.dir = dir
	m.status.persistLocked()
}

// releaseRecords removes this process's record once its servers are stopped,
// so a clean shutdown leaves nothing for the next boot to reap.
func (m *Manager) releaseRecords() {
	m.status.mu.Lock()
	defer m.status.mu.Unlock()
	m.status.persistLocked()
}

// recordPath returns the record file a process with pid writes under dir.
func recordPath(dir string, pid int) string {
	return filepath.Join(dir, recordPrefix+strconv.Itoa(pid)+recordSuffix)
}

// writeRecord replaces the record at path with rec. It writes a temporary
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

// ownerPidOf parses the pid out of a record name, or returns 0 when the name
// is not a record.
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
