package sessions

import (
	"context"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/bus"
	"github.com/JeremiahM37/lectern/v2/internal/executor"
	"github.com/JeremiahM37/lectern/v2/internal/ptyhost"
	"github.com/JeremiahM37/lectern/v2/internal/sessions/backend"
	"github.com/JeremiahM37/lectern/v2/internal/store"
	"github.com/JeremiahM37/lectern/v2/internal/testutil"
	"github.com/JeremiahM37/lectern/v2/internal/testutil/ptytest"
)

// TestBackendSwitchNeverEndsTmuxSessions is the 2026-10-01 incident: a
// server restarted on a Linux machine whose sessions live in tmux, while an
// unrelated PTY host (a test's, a sandbox's) held sessions on the user's
// socket. auto switched the target to the PTY host, polled the tmux sessions
// there, read them as missing and ended all of them. Neither auto nor an
// explicit setting change may end a session some backend still holds.
func TestBackendSwitchNeverEndsTmuxSessions(t *testing.T) {
	testutil.RequireIsolated(t)
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	bin := testutil.LecternBinary(t)
	for _, setting := range []string{backend.SettingAuto, backend.SettingPty} {
		t.Run(setting, func(t *testing.T) {
			dir, err := os.MkdirTemp("/tmp", "lec-switch-")
			if err != nil {
				t.Fatal(err)
			}
			t.Setenv("TMUX_TMPDIR", dir)
			t.Setenv("TMUX", "")
			t.Setenv("ADK_TEST_TMUX_SOCKET", "")
			t.Cleanup(func() { testutil.CleanupTmux(t, dir); _ = os.RemoveAll(dir) })
			// The "default" PTY socket, private to this test.
			socket := ptytest.Socket(t)
			t.Setenv(ptyhost.SocketEnv, socket)

			db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { db.Close() })
			target, err := db.InsertTarget(&store.Target{Name: "local", Kind: "local"})
			if err != nil {
				t.Fatal(err)
			}
			// Two live sessions in tmux: one recorded from before backends
			// were (every row the incident ended), one recorded as tmux. A
			// third has no terminal anywhere and must still end.
			rows := map[string]*store.Session{}
			for _, s := range []struct{ name, backend string }{{"lec-s1", ""}, {"lec-s2", backend.NameTmux}, {"lec-s3", backend.NameTmux}} {
				row, err := db.InsertSession(&store.Session{TargetID: target.ID, Name: s.name, Agent: "claude", TmuxSession: s.name,
					Workdir: dir, Status: StatusIdle, Origin: "lectern", SessionBackend: s.backend})
				if err != nil {
					t.Fatal(err)
				}
				if err := db.Update("sessions", row.ID, map[string]any{"created_at": store.Now() - 600}); err != nil {
					t.Fatal(err)
				}
				rows[s.name] = row
				if s.name == "lec-s3" {
					continue
				}
				if out, err := exec.Command("tmux", "-f", "/dev/null", "new-session", "-d", "-s", s.name, "--", "sleep", "600").CombinedOutput(); err != nil {
					t.Fatalf("tmux: %s %v", out, err)
				}
			}
			// The stray PTY host, holding a session of its own.
			cmd := exec.Command(bin, "pty", "new-session", "-d", "-s", "lec-s99", "--", "sleep", "600")
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("pty host: %s %v", out, err)
			}

			// The server restarts.
			resolver := backend.NewResolver(setting, bin)
			reg := executor.NewRegistry(false, 0)
			reg.Env = resolver.Env
			m := New(db, reg, bus.New(), Launcher{}, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
			if err := m.Startup(context.Background()); err != nil {
				t.Fatal(err)
			}
			for range 3 {
				m.Poll(context.Background())
			}

			for _, name := range []string{"lec-s1", "lec-s2"} {
				got, err := db.Session(rows[name].ID)
				if err != nil {
					t.Fatal(err)
				}
				if got.EndedAt != nil || got.Status == StatusDead {
					t.Fatalf("%s: a live tmux session was ended: status=%s end_reason=%s", name, got.Status, got.EndReason)
				}
				if got.SessionBackend != backend.NameTmux {
					t.Fatalf("%s: backend recorded as %q, want tmux", name, got.SessionBackend)
				}
				if err := exec.Command("tmux", "has-session", "-t", "="+name).Run(); err != nil {
					t.Fatalf("%s: the tmux session itself is gone: %v", name, err)
				}
			}
			gone, err := db.Session(rows["lec-s3"].ID)
			if err != nil {
				t.Fatal(err)
			}
			if gone.EndedAt == nil || gone.Status != StatusDead {
				t.Fatalf("a session no backend holds was not ended: %+v", gone)
			}
			// New sessions keep going to tmux too.
			if got := resolver.Env(target, nil).SessionBackend; setting == backend.SettingAuto && got != backend.NameTmux {
				t.Fatalf("auto picked %q for new sessions on a machine whose sessions live in tmux", got)
			}
		})
	}
}
