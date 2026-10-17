//go:build !windows

package exec

import (
	"context"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

// startGroupSleep starts sleep as a process group leader, the way a
// background job does. It returns the pid and a channel closed when the
// process has been reaped.
func startGroupSleep(t *testing.T) (int, <-chan struct{}) {
	t.Helper()
	cmd := exec.Command("sleep", "60")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	pid := cmd.Process.Pid
	t.Cleanup(func() {
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		<-done
	})
	return pid, done
}

// deadPid returns a pid that belongs to no running process.
func deadPid(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	return cmd.Process.Pid
}

func alive(pid int) bool {
	return procStartTicks(pid) != 0
}

func writeTestRecord(t *testing.T, dir string, owner int, ownerStart uint64, jobs ...jobRecord) string {
	t.Helper()
	path := recordPath(dir, owner)
	if err := writeRecord(path, recordFile{ScribePid: owner, ScribeStart: ownerStart, Jobs: jobs}); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestJobRecordFollowsRunningJob(t *testing.T) {
	dir := t.TempDir()
	jobs := NewJobs(Options{StateDir: dir})
	job := jobs.start(context.Background(), "sleep 30", t.TempDir(), nil, 0)
	path := recordPath(dir, os.Getpid())
	eventually(t, 5*time.Second, "job record written", func() bool {
		_, err := os.Stat(path)
		return err == nil
	})
	rec, ok := job.record()
	if !ok || rec.Pid <= 0 {
		t.Fatalf("running job has no pid: %+v", rec)
	}
	if err := jobs.Kill(job.ID); err != nil {
		t.Fatal(err)
	}
	eventually(t, 5*time.Second, "job record cleared after the job ends", func() bool {
		_, err := os.Stat(path)
		return os.IsNotExist(err)
	})
}

func TestReapOrphansStopsJobsOfDeadOwner(t *testing.T) {
	dir := t.TempDir()
	pid, done := startGroupSleep(t)
	start := procStartTicks(pid)
	if start == 0 {
		t.Skip("no procfs")
	}
	path := writeTestRecord(t, dir, deadPid(t), 1, jobRecord{ID: "bg-1", Pid: pid, Start: start, Command: "sleep 60"})

	reapOrphans(dir)

	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("orphaned job was not stopped")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("record of a dead owner should be removed, stat err=%v", err)
	}
}

func TestReapOrphansLeavesLiveOwnersJobs(t *testing.T) {
	dir := t.TempDir()
	pid, done := startGroupSleep(t)
	ppid := os.Getppid()
	ownerStart := procStartTicks(ppid)
	if ownerStart == 0 {
		t.Skip("no procfs")
	}
	writeTestRecord(t, dir, ppid, ownerStart, jobRecord{ID: "bg-1", Pid: pid, Start: procStartTicks(pid), Command: "sleep 60"})

	reapOrphans(dir)

	select {
	case <-done:
		t.Fatal("a job of a live owner was stopped")
	case <-time.After(300 * time.Millisecond):
	}
}

func TestReapOrphansSkipsReusedPid(t *testing.T) {
	dir := t.TempDir()
	pid, done := startGroupSleep(t)
	// The recorded start time does not match the process now holding the pid.
	writeTestRecord(t, dir, deadPid(t), 1, jobRecord{ID: "bg-1", Pid: pid, Start: procStartTicks(pid) + 1, Command: "sleep 60"})

	reapOrphans(dir)

	select {
	case <-done:
		t.Fatal("a process whose start time does not match the record was signalled")
	case <-time.After(300 * time.Millisecond):
	}
}

func TestOwnerPidOf(t *testing.T) {
	if ownerPidOf("jobs-42.json") != 42 {
		t.Fatal("record name not parsed")
	}
	for _, bad := range []string{"jobs-x.json", "other.json", "jobs-42.json.tmp"} {
		if ownerPidOf(bad) != 0 {
			t.Fatalf("%s should not be a record", bad)
		}
	}
}
