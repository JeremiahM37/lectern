package api_test

import (
	"strings"
	"testing"
)

// A new project from the New session forms gets its folder made on the
// target: ~/projects/<name> unless a path is given.
func TestCreateProjectMakesItsFolder(t *testing.T) {
	h := newHarness(t)
	p := h.post("/api/projects", obj{"name": "Notes App", "target_id": h.firstTargetID(), "create_dir": true}, 201)
	if got := p.str("repo_path"); got != "/mock/home/projects/notes-app" {
		t.Fatalf("repo_path = %q", got)
	}
	if !h.cmdLogHas("# lectern-new-project") || !h.cmdLogHas("git init") {
		t.Fatalf("folder was not created on the target: %v", h.mock().CmdLog())
	}
	q := h.post("/api/projects", obj{"name": "elsewhere", "target_id": h.firstTargetID(),
		"create_dir": true, "repo_path": "~/work/elsewhere"}, 201)
	if got := q.str("repo_path"); got != "/mock/home/work/elsewhere" {
		t.Fatalf("repo_path = %q", got)
	}
}

func TestCreateProjectFolderRefusals(t *testing.T) {
	h := newHarness(t)
	h.post("/api/projects", obj{"name": "dup", "target_id": h.firstTargetID(), "create_dir": true}, 201)
	h.post("/api/projects", obj{"name": "DUP", "target_id": h.firstTargetID(), "create_dir": true}, 409)
	h.post("/api/projects", obj{"name": "rel", "target_id": h.firstTargetID(), "create_dir": true, "repo_path": "rel/x"}, 422)
	h.post("/api/projects", obj{"name": "!!!", "target_id": h.firstTargetID(), "create_dir": true}, 422)
	// Without create_dir a path is still required.
	h.post("/api/projects", obj{"name": "nopath", "target_id": h.firstTargetID()}, 422)
	if strings.Contains(strings.Join(h.mock().CmdLog(), "\n"), "rel/x") {
		t.Fatal("a relative path reached the target")
	}
}
