package api_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/config"
	"github.com/JeremiahM37/lectern/v2/internal/store"
	"github.com/JeremiahM37/lectern/v2/internal/testutil"
)

func TestReviveRefusesWithoutABoundConversation(t *testing.T) {
	h := newHarness(t)
	sess := h.session(obj{"name": "plain", "scratch": true})
	code, body := h.request("POST", fmt.Sprintf("/api/sessions/%d/revive", sess.id()), obj{}, nil)
	if code != 409 || !strings.Contains(string(body), "no saved conversation") {
		t.Fatalf("revive without a bound conversation: %d %s", code, body)
	}
	// Refusing must not have touched the session.
	if got := h.get(fmt.Sprintf("/api/sessions/%d", sess.id())); got["ended_at"] != nil {
		t.Fatalf("session was ended by a refused revive: %v", got)
	}
	h.decode("DELETE", fmt.Sprintf("/api/sessions/%d", sess.id()), nil, 200, nil)
	if code, _ := h.request("POST", fmt.Sprintf("/api/sessions/%d/revive", sess.id()), obj{}, nil); code != 409 {
		t.Fatalf("revive of an ended session: %d", code)
	}
}

// A hung agent keeps its process alive, so the only recovery is to replace it
// with a resume of the same conversation. The wrapper stands in for the hung
// TUI (it sleeps), and the second launch must be an exact resume of the bound
// conversation, in a successor session, with the original ended.
func TestReviveReplacesTheAgentWithAResumeOfItsConversation(t *testing.T) {
	requireRealTools(t)
	testutil.RequireIsolated(t)
	h := newHarness(t, func(c *config.Config) { c.Mock = false })
	root := t.TempDir()
	captureDir := filepath.Join(root, "captures")
	if err := os.Mkdir(captureDir, 0700); err != nil {
		t.Fatal(err)
	}
	wrapper := writeLifecycleWrapper(t, root)
	repo := filepath.Join(root, "repo")
	if err := os.Mkdir(repo, 0755); err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(root, "codex-home")
	if err := os.Mkdir(home, 0700); err != nil {
		t.Fatal(err)
	}
	privateConfigFixture(t, "codex", home)
	cid := "11111111-1111-4111-8111-111111111111"
	writeNativeLifecycleHistory(t, "codex", home, repo, cid)
	target := insertLifecycleTarget(t, h, "local", "")
	h.decode("PUT", "/api/agents", []obj{{
		"name": "codex", "command": wrapper,
		"env":            obj{"CAPTURE_DIR": captureDir, "HOME": home, "CODEX_HOME": home},
		"resume_args":    []string{"resume", "--last"},
		"resume_id_args": resumeIDArgs("codex"),
		"fork_args":      forkArgs("codex"),
	}}, 200, nil)
	project, err := h.App.DB.InsertProject(&store.Project{Name: "revive", TargetID: target.ID, RepoPath: repo, DefaultAgent: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	hung := h.session(obj{"project_id": project.ID, "agent": "codex", "name": "hung"})
	t.Cleanup(func() {
		for _, row := range h.getList("/api/sessions") {
			if name, _ := row["tmux_session"].(string); name != "" {
				h.request("DELETE", fmt.Sprintf("/api/sessions/%d", int64(row["id"].(float64))), nil, nil)
			}
		}
	})
	waitCapture(t, captureDir, 0)
	if _, err := h.App.DB.Exec(`UPDATE sessions SET native_recovery_cid = ? WHERE id = ?`, cid, hung.id()); err != nil {
		t.Fatal(err)
	}
	revived := h.post(fmt.Sprintf("/api/sessions/%d/revive", hung.id()), obj{}, 201)
	if revived.id() == hung.id() || revived.str("resume_id") != cid || revived.str("name") != "hung" {
		t.Fatalf("successor: %v", revived)
	}
	if got := h.get(fmt.Sprintf("/api/sessions/%d", hung.id())); got["ended_at"] == nil {
		t.Fatalf("the hung session was not ended: %v", got)
	}
	waitCapture(t, captureDir, 1)
	launch := readCapture(t, filepath.Join(captureDir, "1.log"))
	if strings.Join(launch.args, " ") != "resume "+cid {
		t.Fatalf("successor launch argv: %q", launch.args)
	}
	if launch.env["PWD"] != repo {
		t.Fatalf("successor ran in %q, want %q", launch.env["PWD"], repo)
	}
}
