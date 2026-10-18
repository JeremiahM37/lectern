package xhost

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// A daemon whose home is removed stops its display and exits, so a test's
// temporary home does not leave an Xvfb and a daemon running forever.
func TestDaemonExitsWhenItsHomeIsRemoved(t *testing.T) {
	if dir := os.Getenv("XHOST_DAEMON_HOME"); dir != "" {
		_ = Daemon(dir, 0, func(string, ...any) {})
		return
	}
	if _, err := exec.LookPath("Xvfb"); err != nil {
		t.Skip("needs Xvfb")
	}
	home := filepath.Join(t.TempDir(), "clip")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestDaemonExitsWhenItsHomeIsRemoved$")
	cmd.Env = append(os.Environ(), "XHOST_DAEMON_HOME="+home)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	time.Sleep(time.Second)
	if err := os.RemoveAll(home); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("the daemon kept running after its home was removed")
	}
}
