package sessions

import (
	"context"
	"github.com/JeremiahM37/lectern/internal/shellq"
	"github.com/JeremiahM37/lectern/internal/testutil"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestArchiveRefusedStopOrChangedIdentityKeepsRecordLive(t *testing.T) {
	testutil.RequireIsolated(t)
	real := "/usr/bin/tmux"
	var err error
	if _, statErr := os.Stat(real); statErr != nil {
		real, err = exec.LookPath("tmux")
	}
	if err != nil {
		t.Skip("tmux not installed")
	}
	for _, change := range []bool{false, true} {
		t.Run(map[bool]string{false: "stop-refused", true: "identity-changed"}[change], func(t *testing.T) {
			dir, err := os.MkdirTemp("/tmp", "lec-archive-")
			if err != nil {
				t.Fatal(err)
			}
			t.Setenv("TMUX_TMPDIR", dir)
			t.Setenv("TMUX", "")
			socket := filepath.Join(dir, "tmux.sock")
			t.Setenv("ADK_TEST_TMUX_SOCKET", socket)
			t.Cleanup(func() { testutil.CleanupTmuxSocket(t, socket); _ = os.RemoveAll(dir) })
			m, s := pollRig(t)
			if out, err := exec.Command(real, "-S", socket, "-f", "/dev/null", "new-session", "-d", "-s", s.TmuxSession, "--", "sleep", "600").CombinedOutput(); err != nil {
				t.Fatalf("tmux %s %v", out, err)
			}
			action := "exit 0"
			if change {
				action = shellq.Quote(real) + " -S " + shellq.Quote(socket) + " set-option -t poll-test @lectern-tracking-identity ffffffffffffffffffffffffffffffff"
			}
			wrapper := "#!/bin/sh\nif [ \"$1\" = if-shell ]; then " + action + "; fi\ncase \"$1\" in -S|-L) exec " + shellq.Quote(real) + " \"$@\" ;; esac\nexec " + shellq.Quote(real) + " -S " + shellq.Quote(socket) + " \"$@\"\n"
			if err := os.WriteFile(filepath.Join(dir, "tmux"), []byte(wrapper), 0755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			if _, err := m.Archive(context.Background(), s.ID, true); err == nil {
				t.Fatal("archive accepted a terminal that did not stop")
			}
			got, err := m.DB.Session(s.ID)
			if err != nil || got.EndedAt != nil || got.ArchivedAt != nil {
				t.Fatalf("live record lost: %+v %v", got, err)
			}
			if err := exec.Command(real, "-S", socket, "has-session", "-t", "=poll-test").Run(); err != nil {
				t.Fatal("different/current terminal was killed")
			}
		})
	}
}
