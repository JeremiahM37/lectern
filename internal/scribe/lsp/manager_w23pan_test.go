//go:build unix

package lsp

import (
	"io"
	"testing"
)

// Sessions reports the process id and the open document count of a running
// server, which is what the /lsp status view shows on each row.
func TestSessionsReportsPIDAndOpenDocsOfRunningServer(t *testing.T) {
	m := NewManager(t.TempDir())
	cmd, _ := startSleeper(t)
	pr, pw := io.Pipe()
	t.Cleanup(func() { _ = pw.Close(); _ = pr.Close() })
	s := &session{
		spec: serverSpecs["go"], root: m.workdir, client: NewClient(pr, pw), cmd: cmd,
		bin: "sleep", command: "sleep 60", docs: map[string]*docState{"file:///a.go": {}, "file:///b.go": {}},
	}
	key := s.spec.Language + "\x00" + s.root
	m.noteStarted(key, s)

	var got *SessionInfo
	for _, info := range m.Sessions() {
		if info.Language == "go" {
			info := info
			got = &info
		}
	}
	if got == nil {
		t.Fatal("the running go server should be listed")
	}
	if got.State != StateRunning || got.PID != cmd.Process.Pid || got.OpenDocs != 2 {
		t.Fatalf("status = state %q pid %d open %d; want running pid %d open 2",
			got.State, got.PID, got.OpenDocs, cmd.Process.Pid)
	}
}
