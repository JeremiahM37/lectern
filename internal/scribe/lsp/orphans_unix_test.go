//go:build unix

package lsp

import (
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// startSleeper starts `sleep 60` in its own process group, the way a server
// is started, and reaps it in the background. The returned channel closes
// once the process has exited and been reaped. Every test here signals only
// processes it started itself.
func startSleeper(t *testing.T) (*exec.Cmd, <-chan struct{}) {
	t.Helper()
	cmd := exec.Command("sleep", "60")
	setProcGroup(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
	}()
	pid := cmd.Process.Pid
	t.Cleanup(func() {
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		select {
		case <-done:
		case <-time.After(5 * time.Second):
		}
	})
	return cmd, done
}

// exited reports whether the process behind done has ended.
func exited(done <-chan struct{}) bool {
	select {
	case <-done:
		return true
	default:
		return false
	}
}

// waitExit waits up to five seconds for done to close.
func waitExit(t *testing.T, done <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("%s did not exit", what)
	}
}

// deadPid returns the pid of a process that has already exited and been
// reaped, so no live process owns it.
func deadPid(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	return cmd.Process.Pid
}

// writeServerRecord writes the record file a Scribe with rec.ScribePid would
// leave behind, and returns its path.
func writeServerRecord(t *testing.T, dir string, rec recordFile) string {
	t.Helper()
	path := recordPath(dir, rec.ScribePid)
	if err := writeRecord(path, rec); err != nil {
		t.Fatal(err)
	}
	return path
}

func sleeperRecord(pid int, start uint64, bin string) serverRecord {
	return serverRecord{Pid: pid, Start: start, Bin: bin, Command: bin + " 60"}
}

func TestReapOrphansStopsRecordedServer(t *testing.T) {
	dir := t.TempDir()
	cmd, done := startSleeper(t)
	pid := cmd.Process.Pid
	start := procStartTicks(pid)
	if start == 0 {
		t.Skip("process start times are not readable here")
	}
	// Owner pid is gone, so the server is an orphan.
	path := writeServerRecord(t, dir, recordFile{
		ScribePid:   deadPid(t),
		ScribeStart: 1,
		Servers:     []serverRecord{sleeperRecord(pid, start, "sleep")},
	})

	ReapOrphans(dir)

	waitExit(t, done, "recorded server")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("record should be removed once reaped, stat err = %v", err)
	}
}

func TestReapOrphansLeavesReusedPidAlone(t *testing.T) {
	dir := t.TempDir()
	cmd, done := startSleeper(t)
	pid := cmd.Process.Pid
	start := procStartTicks(pid)
	if start == 0 {
		t.Skip("process start times are not readable here")
	}
	// The record names a different process start time for this pid, as if
	// the pid had been reused since the record was written.
	writeServerRecord(t, dir, recordFile{
		ScribePid:   deadPid(t),
		ScribeStart: 1,
		Servers:     []serverRecord{sleeperRecord(pid, start+1, "sleep")},
	})

	ReapOrphans(dir)

	if exited(done) {
		t.Fatal("a pid whose start time does not match the record must not be signalled")
	}
}

func TestReapOrphansLeavesOtherProgramAlone(t *testing.T) {
	dir := t.TempDir()
	cmd, done := startSleeper(t)
	pid := cmd.Process.Pid
	start := procStartTicks(pid)
	if start == 0 {
		t.Skip("process start times are not readable here")
	}
	// Same pid and start time, but the record says the server was gopls and
	// this process is sleep.
	writeServerRecord(t, dir, recordFile{
		ScribePid:   deadPid(t),
		ScribeStart: 1,
		Servers:     []serverRecord{sleeperRecord(pid, start, "gopls")},
	})

	ReapOrphans(dir)

	if exited(done) {
		t.Fatal("a process that is not the recorded program must not be signalled")
	}
}

func TestReapOrphansKeepsRecordOfLiveOwner(t *testing.T) {
	dir := t.TempDir()
	owner, ownerDone := startSleeper(t)
	ownerStart := procStartTicks(owner.Process.Pid)
	server, serverDone := startSleeper(t)
	serverPid := server.Process.Pid
	serverStart := procStartTicks(serverPid)
	if ownerStart == 0 || serverStart == 0 {
		t.Skip("process start times are not readable here")
	}
	path := writeServerRecord(t, dir, recordFile{
		ScribePid:   owner.Process.Pid,
		ScribeStart: ownerStart,
		Servers:     []serverRecord{sleeperRecord(serverPid, serverStart, "sleep")},
	})

	ReapOrphans(dir)

	if exited(serverDone) {
		t.Fatal("servers of a live Scribe are its own and must be left running")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("record of a live owner must stay: %v", err)
	}
	if exited(ownerDone) {
		t.Fatal("the owner must not be touched")
	}
}

func TestReapOrphansIgnoresOtherFiles(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"notes.json", "lsp-notapid.json", "lsp-7.json"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("{not json"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	ReapOrphans(dir)

	for _, name := range []string{"notes.json", "lsp-notapid.json", "lsp-7.json"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("%s should be left alone: %v", name, err)
		}
	}
	ReapOrphans("")
	ReapOrphans(filepath.Join(dir, "missing"))
}

func TestServerRecordFollowsRunningServersAndClearsOnShutdown(t *testing.T) {
	stateDir := t.TempDir()
	m := NewManager(t.TempDir())
	m.SetStateDir(stateDir)

	cmd, _ := startSleeper(t)
	pid := cmd.Process.Pid
	start := procStartTicks(pid)
	if start == 0 {
		t.Skip("process start times are not readable here")
	}
	pr, pw := io.Pipe()
	t.Cleanup(func() { _ = pw.Close(); _ = pr.Close() })
	s := &session{
		spec: serverSpecs["go"], root: m.workdir, client: NewClient(pr, pw), cmd: cmd,
		bin: "sleep", command: "sleep 60", startTicks: start, docs: map[string]*docState{},
	}
	key := s.spec.Language + "\x00" + s.root
	m.noteStarted(key, s)

	path := recordPath(stateDir, os.Getpid())
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("a running server must be recorded: %v", err)
	}
	var rec recordFile
	if err := json.Unmarshal(raw, &rec); err != nil {
		t.Fatal(err)
	}
	if rec.ScribePid != os.Getpid() || len(rec.Servers) != 1 || rec.Servers[0].Pid != pid || rec.Servers[0].Bin != "sleep" {
		t.Fatalf("record does not describe the running server: %+v", rec)
	}

	// Shutdown clears the sessions and releases the record, so the next boot
	// has nothing to reap.
	m.clearStatus()
	m.releaseRecords()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("a clean shutdown must remove its record, stat err = %v", err)
	}
}
