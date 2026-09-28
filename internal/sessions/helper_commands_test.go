package sessions

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/bus"
	"github.com/JeremiahM37/lectern/v2/internal/executor"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

const fakeLectern = "/opt/lectern tools/lectern"

// lastCommand runs fn against a mock target, with or without a lectern
// binary, and returns the command it sent.
func lastCommand(t *testing.T, env executor.TargetEnv, fn func(ex executor.Executor)) string {
	t.Helper()
	m := executor.NewMock(0)
	executor.SetTargetEnv(m, env)
	fn(m)
	log := m.CmdLog()
	if len(log) == 0 {
		t.Fatal("no command was run")
	}
	return log[len(log)-1]
}

// wantHelper checks that a target with lectern gets the Go helper, with its
// arguments and any environment prefix, and one without keeps Python.
func wantHelper(t *testing.T, label, name, args, prefix string, fn func(ex executor.Executor)) {
	t.Helper()
	got := lastCommand(t, executor.TargetEnv{Lectern: fakeLectern}, fn)
	want := prefix + "'" + fakeLectern + "' helper " + name + " " + args
	if got != want {
		t.Errorf("%s with lectern:\n got %q\nwant %q", label, got, want)
	}
	py := lastCommand(t, executor.TargetEnv{}, fn)
	if !strings.HasPrefix(py, prefix+"python3 -c ") || strings.Contains(py, " helper ") {
		t.Errorf("%s without lectern: %q", label, py)
	}
}

func TestNativeHelpersRunInGoWhenTheTargetHasLectern(t *testing.T) {
	ctx := context.Background()
	wantHelper(t, "capture", "native-identity", "codex /w '/home dir' lec-1 abc", "", func(ex executor.Executor) {
		CaptureNativeID(ctx, ex, "codex", "/w", "/home dir", "lec-1", "abc")
	})
	wantHelper(t, "evidence", "native-identity", "claude /w '' lec-1 abc ''", "", func(ex executor.Executor) {
		_, _ = CaptureNativeEvidence(ctx, ex, "claude", "/w", "", "lec-1", "abc", false)
	})
	wantHelper(t, "discovery", "native-identity", "claude /w '' lec-1 abc 1", "", func(ex executor.Executor) {
		_, _ = CaptureNativeEvidence(ctx, ex, "claude", "/w", "", "lec-1", "abc", true)
	})
	wantHelper(t, "configured home", "configured-home", "codex lec-1", "", func(ex executor.Executor) {
		_, _ = ProbeConfiguredHome(ctx, ex, "codex", "lec-1")
	})
	wantHelper(t, "fork path", "claude-fork-path", "/w 11111111-1111-4111-8111-111111111111", "CLAUDE_CONFIG_DIR=/c ", func(ex executor.Executor) {
		_, _ = claudeForkPath(ctx, ex, "CLAUDE_CONFIG_DIR=/c ", "/w", "11111111-1111-4111-8111-111111111111")
	})
	spec := Spec{Name: "pi", Command: "pi --flag", Env: map[string]string{"PI_HOME": "/p"},
		Sessions: &SessionsSpec{Files: []string{"~/.pi/*.jsonl"}, ID: "id", Created: "c"}}
	wantHelper(t, "catalog", "catalog-sessions", `'{"files":["~/.pi/*.jsonl"],"id":"id","created":"c","agent":"pi"}' /w pi sel`, "PI_HOME=/p ", func(ex executor.Executor) {
		_, _ = CatalogConversations(ctx, ex, spec, "/w", "sel")
	})

	// The session backend travels with the call.
	got := lastCommand(t, executor.TargetEnv{Lectern: "lectern", SessionBackend: "pty"}, func(ex executor.Executor) {
		_, _ = ProbeConfiguredHome(ctx, ex, "claude", "lec-1")
	})
	if got != "LECTERN_SESSION_BACKEND=pty lectern helper configured-home claude lec-1" {
		t.Errorf("pty backend: %q", got)
	}
}

func TestAdoptedMatchListsWithTheGoHelper(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	target, err := db.InsertTarget(&store.Target{Name: "mock", Kind: "local"})
	if err != nil {
		t.Fatal(err)
	}
	row, err := db.InsertSession(&store.Session{TargetID: target.ID, Agent: "claude", Workdir: "/w", TmuxSession: "t1", Status: StatusDead, Origin: "discovered"})
	if err != nil {
		t.Fatal(err)
	}
	m := New(db, executor.NewRegistry(true, 0), bus.New(), Launcher{}, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	wantHelper(t, "adopted match", "native-conversations", "claude /w '' '' '' ''", "", func(ex executor.Executor) {
		_, _ = m.nativeCandidates(context.Background(), ex, row)
	})
}
