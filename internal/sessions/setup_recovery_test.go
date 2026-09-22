package sessions

import (
	"context"
	"github.com/JeremiahM37/lectern/internal/executor"
	"github.com/JeremiahM37/lectern/internal/store"
	"github.com/JeremiahM37/lectern/internal/testutil"
	"github.com/JeremiahM37/lectern/internal/worktree"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestRecoveryNeverAdoptsAReusedTerminalName(t *testing.T) {
	socket, err := os.MkdirTemp("", "lec-recovery-")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMUX_TMPDIR", socket)
	t.Setenv("TMUX", "")
	t.Cleanup(func() { testutil.CleanupTmux(t, socket); os.RemoveAll(socket) })
	for _, mode := range []string{"owned", "foreign", "unmarked", "missing", "unavailable"} {
		t.Run(mode, func(t *testing.T) {
			m, row := pollRig(t)
			token := strings.Repeat("a", 32)
			plan := worktree.Interactive{Repo: row.Workdir, Path: row.Workdir, Token: token, State: "ready"}
			if err := m.DB.Update("sessions", row.ID, map[string]any{"setup_state": "creating", "worktree_json": store.J(plan)}); err != nil {
				t.Fatal(err)
			}
			if mode == "unavailable" {
				m.DB.Update("targets", row.TargetID, map[string]any{"kind": "unavailable-test"})
			}
			if mode != "missing" && mode != "unavailable" {
				marker := token
				if mode == "foreign" {
					marker = strings.Repeat("b", 32)
				}
				if mode == "unmarked" {
					marker = ""
				}
				cmd := (Spec{Command: "sleep 600"}).LaunchCommand(Start{Workdir: row.Workdir, TmuxName: row.TmuxSession, SetupToken: marker})
				if result, err := executor.NewLocal().Run(context.Background(), cmd, executor.RunOpts{}); err != nil || !result.OK() {
					t.Fatal("fixture terminal failed")
				}
				defer exec.Command("tmux", "kill-session", "-t", "="+row.TmuxSession).Run()
			}
			m.Poll(context.Background())
			current, err := m.DB.Session(row.ID)
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "owned":
				if current.SetupState != "ready" || current.EndedAt != nil || !validTrackingIdentity(current.TrackingIdentity) {
					t.Fatalf("owned terminal not recovered: %s", current.SetupError)
				}
				if err := m.CancelSetup(context.Background(), row.ID); err == nil {
					t.Fatal("ready agent accepted setup cancellation")
				}
			case "unavailable":
				if current.SetupState != "creating" || current.EndedAt != nil || current.SetupError == "" {
					t.Fatal("unreachable target was declared ended")
				}
				if err := m.CancelSetup(context.Background(), row.ID); err == nil {
					t.Fatal("unverified completed setup accepted cancellation")
				}
			default:
				if current.SetupState != "failed" || current.EndedAt == nil {
					t.Fatal("missing or foreign terminal was recovered")
				}
				if mode != "missing" {
					if err := exec.Command("tmux", "has-session", "-t", "="+row.TmuxSession).Run(); err != nil {
						t.Fatal("foreign terminal was stopped")
					}
					out, _ := exec.Command("tmux", "show-options", "-qv", "-t", "="+row.TmuxSession, trackingOption).Output()
					if len(strings.TrimSpace(string(out))) != 0 {
						t.Fatal("foreign terminal identity was changed")
					}
				}
			}
		})
	}
}
