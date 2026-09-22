package sessions

import (
	"context"
	"encoding/base64"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/internal/bus"
	"github.com/JeremiahM37/lectern/internal/executor"
	"github.com/JeremiahM37/lectern/internal/shellq"
	"github.com/JeremiahM37/lectern/internal/store"
	"github.com/JeremiahM37/lectern/internal/testutil"
)

func pollRig(t *testing.T) (*Manager, *store.Session) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	target, err := db.InsertTarget(&store.Target{Name: "local-test", Kind: "local"})
	if err != nil {
		t.Fatal(err)
	}
	sess, err := db.InsertSession(&store.Session{TargetID: target.ID, Name: "Still running", TmuxSession: "poll-test", Workdir: t.TempDir(), Status: StatusIdle, Origin: "discovered"})
	if err != nil {
		t.Fatal(err)
	}
	m := New(db, executor.NewRegistry(false, 0), bus.New(), Launcher{}, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	return m, sess
}

func TestFailedPollDoesNotEndLiveRecord(t *testing.T) {
	for _, kind := range []string{"command-failed", "capture-failed", "truncated-success"} {
		t.Run(kind, func(t *testing.T) {
			m, original := pollRig(t)
			dir := t.TempDir()
			name, script := "tmux", "#!/bin/sh\necho 'permission denied while reading socket' >&2\nexit 1\n"
			if kind == "command-failed" {
				name = "bash"
				script = "#!/bin/sh\nprintf partial\nexit 124\n"
			}
			if kind == "truncated-success" {
				name = "bash"
				script = "#!/bin/sh\nprintf 'cG9sbC10ZXN0\\tok\\taGVsbG8=\\n'\n"
			}
			if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			m.Poll(context.Background())
			got, err := m.DB.Session(original.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got.EndedAt != nil || got.Status != original.Status || got.UpdatedAt != original.UpdatedAt {
				t.Fatalf("failed poll changed a live record: %+v", got)
			}
		})
	}
}

func TestUnknownBootDoesNotEndOwnedMissingPane(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	target, err := db.InsertTarget(&store.Target{Name: "mock-target", Kind: "mock"})
	if err != nil {
		t.Fatal(err)
	}
	row, err := db.InsertSession(&store.Session{
		TargetID: target.ID, Name: "reboot candidate", Agent: "codex", Workdir: "/mock/work",
		TmuxSession: "missing-owned", Status: StatusIdle, Origin: "lectern",
		BootID: "11111111-2222-3333-4444-555555555555", TrackingIdentity: "0123456789abcdef0123456789abcdef",
	})
	if err != nil {
		t.Fatal(err)
	}
	m := New(db, executor.NewRegistry(true, 0), bus.New(), Launcher{}, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	m.Poll(context.Background())
	got, err := db.Session(row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.EndedAt != nil || got.Status == StatusDead {
		t.Fatalf("unknown boot identity ended an owned row: %+v", got)
	}
}

func TestPollSnapshotRejectsTruncationAndForgedFrames(t *testing.T) {
	frame := func(name, state, body string) string {
		return base64.StdEncoding.EncodeToString([]byte(name)) + "\t" + state + "\t" + base64.StdEncoding.EncodeToString([]byte(body)) + "\n"
	}
	payload := PollDelimiter + "other\n" + PollEnd + "\nb3RoZXI=\tmissing\t\n"
	valid := frame("one", "ok", payload) + frame("other", "ok", "") + PollEnd + "\n"
	got, ok := ParsePollSnapshot(valid, []string{"one", "other"})
	if !ok || got["one"].Text != payload || got["other"].Missing {
		t.Fatal("payload changed framing", got)
	}
	for _, bad := range []string{strings.TrimSuffix(valid, PollEnd+"\n"), valid + "junk", frame("one", "ok", "") + PollEnd + "\n", frame("one", "ok", "") + frame("one", "ok", "") + PollEnd + "\n", frame("unknown", "missing", "") + frame("other", "ok", "") + PollEnd + "\n", frame("one", "missing", "unexpected body") + frame("other", "ok", "") + PollEnd + "\n"} {
		if _, ok := ParsePollSnapshot(bad, []string{"one", "other"}); ok {
			t.Fatalf("accepted malformed snapshot %q", bad)
		}
	}
}

func TestRealTmuxBlankPaneIsLiveAndAbsentSessionIsDead(t *testing.T) {
	testutil.RequireIsolated(t)
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	dir, err := os.MkdirTemp("/tmp", "lec-poll-")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMUX_TMPDIR", dir)
	t.Setenv("TMUX", "")
	socket := filepath.Join(dir, "tmux.sock")
	t.Setenv("ADK_TEST_TMUX_SOCKET", socket)
	t.Cleanup(func() { testutil.CleanupTmuxSocket(t, socket); _ = os.RemoveAll(dir) })
	m, sess := pollRig(t)
	if err := m.DB.Update("sessions", sess.ID, map[string]any{"created_at": store.Now() - 60}); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("tmux", "-S", socket, "-f", "/dev/null", "new-session", "-d", "-s", sess.TmuxSession, "--", "sleep", "600").CombinedOutput(); err != nil {
		t.Fatalf("tmux: %s %v", out, err)
	}
	m.Poll(context.Background())
	got, _ := m.DB.Session(sess.ID)
	if got.EndedAt != nil || got.Status == StatusDead {
		t.Fatal("blank live pane declared dead", got)
	}
	command := "printf '%s\n' " + shellq.Quote(strings.Repeat("visible Ω ", 30)+"\n"+PollEnd) + "; sleep 600"
	if out, err := exec.Command("tmux", "-S", socket, "respawn-pane", "-k", "-t", "="+sess.TmuxSession+":", "--", "bash", "-c", command).CombinedOutput(); err != nil {
		t.Fatalf("respawn: %s %v", out, err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		m.Poll(context.Background())
		got, _ = m.DB.Session(sess.ID)
		if strings.Contains(got.PaneTail, PollEnd) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.Contains(got.PaneTail, PollEnd) || got.EndedAt != nil {
		t.Fatal("normal multiline capture did not update", got)
	}
	// Opening-message readiness uses the same wire format. Exercise actual
	// capture and paste against a shell prompt without starting a paid agent.
	received := filepath.Join(dir, "received")
	command = "printf '❯ '; IFS= read -r reply; printf '%s' \"$reply\" > " + shellq.Quote(received) + "; sleep 600"
	if out, err := exec.Command("tmux", "-S", socket, "respawn-pane", "-k", "-t", "="+sess.TmuxSession+":", "--", "bash", "-c", command).CombinedOutput(); err != nil {
		t.Fatalf("prompt: %s %v", out, err)
	}
	m.primeWhenReady(sess.ID, "opening message proof")
	deadline = time.Now().Add(2 * time.Second)
	var data []byte
	for time.Now().Before(deadline) {
		data, _ = os.ReadFile(received)
		if string(data) == "opening message proof" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if string(data) != "opening message proof" {
		t.Fatalf("opening message was not delivered: %q", data)
	}
	if err := exec.Command("tmux", "-S", socket, "kill-session", "-t", "="+sess.TmuxSession).Run(); err != nil {
		t.Fatal(err)
	}
	m.Poll(context.Background())
	got, _ = m.DB.Session(sess.ID)
	if got.EndedAt == nil || got.Status != StatusDead {
		t.Fatal("missing last session not detected", got)
	}
}
