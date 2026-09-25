package sessions

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/bus"
	"github.com/JeremiahM37/lectern/v2/internal/executor"
	"github.com/JeremiahM37/lectern/v2/internal/isolation"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

func isolationLaunchRig(t *testing.T, targetKind string) (*Manager, *store.Target) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	target, err := db.InsertTarget(&store.Target{Name: "target", Kind: targetKind})
	if err != nil {
		t.Fatal(err)
	}
	m := New(db, executor.NewRegistry(true, 0), bus.New(), Launcher{}, nil,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	m.Specs = func() []Spec { return Builtins() }
	return m, target
}

// TestLaunchRejectsIsolationTargetMismatch covers the manager-level half of
// item 3's "per-project default + per-launch override ... in API": an
// explicit per-launch override that the target cannot honestly support is
// refused before a session row is left behind.
func TestLaunchRejectsIsolationTargetMismatch(t *testing.T) {
	m, target := isolationLaunchRig(t, "pct")
	cfg := isolation.Config{Mode: isolation.Bwrap, Network: isolation.NetworkDeny}
	_, err := m.Launch(context.Background(), LaunchOpts{
		TargetID: target.ID, Agent: "claude", Workdir: "/work", Isolation: &cfg,
	})
	if err == nil || !strings.Contains(err.Error(), "isolation") {
		t.Fatalf("expected an isolation target-mismatch error, got %v", err)
	}
	// The row is marked dead (m.end), matching every other early-abort
	// failure in launch() — not deleted, so it stays inspectable — but it
	// must not linger as a live/pending session.
	if rows, err := m.DB.Sessions(false); err != nil {
		t.Fatal(err)
	} else if len(rows) != 0 {
		t.Fatalf("a rejected launch must not leave a live session behind: %v", rows)
	}
}

// TestLaunchPersistsIsolationAndTearsDownProxy covers item 3's persistence
// requirement end to end through the real Launch/Kill path (not just the
// LaunchConfiguration helpers above): a network=deny launch starts this
// session's own egress proxy, the session's stored launch configuration
// carries the isolation choice, and ending the session tears the proxy down
// — the same lifecycle docs/isolation.md promises.
func TestLaunchPersistsIsolationAndTearsDownProxy(t *testing.T) {
	m, target := isolationLaunchRig(t, "local")
	m.IsolationProxies.Dir = t.TempDir()
	cfg := isolation.Config{Mode: isolation.Bwrap, Network: isolation.NetworkDeny}
	sess, err := m.Launch(context.Background(), LaunchOpts{
		TargetID: target.ID, Agent: "claude", Workdir: t.TempDir(), Isolation: &cfg,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !m.IsolationProxies.Running(sess.ID) {
		t.Fatal("expected a network=deny launch to start this session's egress proxy")
	}
	stored, err := m.DB.Session(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	var launchCfg LaunchConfiguration
	if err := json.Unmarshal([]byte(stored.LaunchConfigJSON), &launchCfg); err != nil {
		t.Fatal(err)
	}
	if launchCfg.Isolation.Mode != isolation.Bwrap || launchCfg.Isolation.Network != isolation.NetworkDeny {
		t.Fatalf("expected the isolation choice persisted on the session, got %+v", launchCfg.Isolation)
	}
	if err := m.Kill(context.Background(), sess.ID); err != nil {
		t.Fatal(err)
	}
	if m.IsolationProxies.Running(sess.ID) {
		t.Fatal("expected Kill to tear down the session's egress proxy")
	}
}

// TestLaunchAllowNetworkStartsNoProxy: network=allow (the default) is not
// bwrap network=deny's unix-socket bridge, so it must not start a proxy at
// all — only network=deny needs the egress allowlist.
func TestLaunchAllowNetworkStartsNoProxy(t *testing.T) {
	m, target := isolationLaunchRig(t, "local")
	m.IsolationProxies.Dir = t.TempDir()
	cfg := isolation.Config{Mode: isolation.Bwrap, Network: isolation.NetworkAllow}
	sess, err := m.Launch(context.Background(), LaunchOpts{
		TargetID: target.ID, Agent: "claude", Workdir: t.TempDir(), Isolation: &cfg,
	})
	if err != nil {
		t.Fatal(err)
	}
	if m.IsolationProxies.Running(sess.ID) {
		t.Fatal("network=allow must not start an egress proxy")
	}
}
