package api_test

// A scratch session is an empty room: any agent CLI, a throwaway directory, no
// decision about what the work is. Promotion is where that decision gets made,
// afterwards, without moving anything.

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/internal/store"
)

func TestAScratchSessionGetsItsOwnDirectory(t *testing.T) {
	h := newHarness(t)
	code, body := h.request("POST", "/api/sessions", obj{
		"agent": "claude", "scratch": true, "name": "thinking"}, nil)
	if code != 201 {
		t.Fatalf("launching a scratch session: %d %s", code, body)
	}
	var sess store.Session
	json.Unmarshal(body, &sess)
	if sess.ProjectID != nil {
		t.Errorf("a scratch session must not be attached to a project: %v", *sess.ProjectID)
	}
	if sess.Workdir == "" {
		t.Fatal("a scratch session still needs somewhere to work")
	}
	if !strings.Contains(sess.Workdir, "lectern-scratch") {
		t.Errorf("scratch work belongs under the scratch root, got %q", sess.Workdir)
	}
	if !strings.Contains(sess.Workdir, "thinking") {
		t.Errorf("the directory should carry the session's name: %q", sess.Workdir)
	}
}

// Two scratch sessions started together must not land in one directory and
// overwrite each other's work.
func TestScratchSessionsDoNotShareADirectory(t *testing.T) {
	h := newHarness(t)
	seen := map[string]bool{}
	for i := 0; i < 3; i++ {
		code, body := h.request("POST", "/api/sessions", obj{"agent": "claude", "scratch": true}, nil)
		if code != 201 {
			t.Fatalf("%d: %s", code, body)
		}
		var sess store.Session
		json.Unmarshal(body, &sess)
		if seen[sess.Workdir] {
			t.Fatalf("two scratch sessions share %q", sess.Workdir)
		}
		seen[sess.Workdir] = true
	}
}

// The agent is the operator's choice, including one they defined themselves.
func TestAScratchSessionRunsAnyConfiguredAgent(t *testing.T) {
	h := newHarness(t)
	if code, body := h.request("PUT", "/api/agents", []obj{
		{"name": "claude", "command": "claude", "prompt_arg": true},
		{"name": "codex", "command": "codex", "prompt_arg": true},
		{"name": "aider", "command": "aider", "model_flag": "--model"},
	}, nil); code != 200 {
		t.Fatalf("defining agents: %d %s", code, body)
	}

	for _, agent := range []string{"claude", "codex", "aider"} {
		code, body := h.request("POST", "/api/sessions", obj{"agent": agent, "scratch": true}, nil)
		if code != 201 {
			t.Fatalf("%s: %d %s", agent, code, body)
		}
		var sess store.Session
		json.Unmarshal(body, &sess)
		if sess.Agent != agent {
			t.Errorf("asked for %q, got %q", agent, sess.Agent)
		}
	}
	// and an agent that was never defined is refused rather than silently
	// launching whatever the default is
	if code, body := h.request("POST", "/api/sessions",
		obj{"agent": "not-an-agent", "scratch": true}, nil); code != 422 {
		t.Errorf("unknown agent: %d %s", code, body)
	}
}

func TestPromotingAScratchSessionCreatesAProject(t *testing.T) {
	h := newHarness(t)
	code, body := h.request("POST", "/api/sessions", obj{
		"agent": "claude", "scratch": true, "name": "notes app"}, nil)
	if code != 201 {
		t.Fatalf("%d %s", code, body)
	}
	var sess store.Session
	json.Unmarshal(body, &sess)

	code, body = h.request("POST", fmt.Sprintf("/api/sessions/%d/promote", sess.ID),
		obj{"name": "notes-app"}, nil)
	if code != 200 {
		t.Fatalf("promote: %d %s", code, body)
	}
	var out struct {
		Project store.Project `json:"project"`
		Session store.Session `json:"session"`
	}
	json.Unmarshal(body, &out)

	if out.Project.ID == 0 {
		t.Fatal("no project was created")
	}
	if out.Project.Name != "notes-app" {
		t.Errorf("project name: %q", out.Project.Name)
	}
	// nothing moves: the project's repository is where the agent already works
	if out.Project.RepoPath != sess.Workdir {
		t.Errorf("the project should adopt the session's directory: %q vs %q",
			out.Project.RepoPath, sess.Workdir)
	}
	if out.Project.TargetID != sess.TargetID {
		t.Errorf("the project must live on the same target as the session")
	}
	if out.Project.DefaultAgent != "claude" {
		t.Errorf("the project should default to the agent the work was done with, got %q",
			out.Project.DefaultAgent)
	}
	// and the conversation carries straight on, now attached
	if out.Session.ProjectID == nil || *out.Session.ProjectID != out.Project.ID {
		t.Fatalf("the session was not linked to its new project: %v", out.Session.ProjectID)
	}
	stored, err := h.App.DB.Session(sess.ID)
	if err != nil || stored.ProjectID == nil || *stored.ProjectID != out.Project.ID {
		t.Errorf("the link did not persist: %v", err)
	}
	// the session now appears under the project on the board
	code, body = h.request("GET", "/api/sessions", nil, nil)
	if code != 200 || !strings.Contains(string(body), `"project_name":"notes-app"`) {
		t.Errorf("the promoted session is not shown under its project: %s", body)
	}
}

// The name is optional — promoting with no name should still produce something
// readable, not a timestamped scratch directory name.
func TestPromotingWithoutANameUsesTheDirectory(t *testing.T) {
	h := newHarness(t)
	code, body := h.request("POST", "/api/sessions",
		obj{"agent": "claude", "scratch": true, "name": "inference tuning"}, nil)
	if code != 201 {
		t.Fatalf("%d %s", code, body)
	}
	var sess store.Session
	json.Unmarshal(body, &sess)

	code, body = h.request("POST", fmt.Sprintf("/api/sessions/%d/promote", sess.ID), obj{}, nil)
	if code != 200 {
		t.Fatalf("promote: %d %s", code, body)
	}
	var out struct {
		Project store.Project `json:"project"`
	}
	json.Unmarshal(body, &out)
	if out.Project.Name != "inference-tuning" {
		t.Errorf("the timestamp should be dropped from the name, got %q", out.Project.Name)
	}
}

// Promoting into a project that already exists is the other half: work that
// turns out to belong to something you already track.
func TestPromotingIntoAnExistingProject(t *testing.T) {
	h := newHarness(t)
	existing := h.seededProjectID()
	code, body := h.request("POST", "/api/sessions", obj{"agent": "claude", "scratch": true}, nil)
	if code != 201 {
		t.Fatalf("%d %s", code, body)
	}
	var sess store.Session
	json.Unmarshal(body, &sess)

	before, _ := h.App.DB.Projects()
	code, body = h.request("POST", fmt.Sprintf("/api/sessions/%d/promote", sess.ID),
		obj{"project_id": existing}, nil)
	if code != 200 {
		t.Fatalf("promote: %d %s", code, body)
	}
	after, _ := h.App.DB.Projects()
	if len(after) != len(before) {
		t.Errorf("adopting into an existing project created a new one: %d -> %d",
			len(before), len(after))
	}
	stored, _ := h.App.DB.Session(sess.ID)
	if stored.ProjectID == nil || *stored.ProjectID != existing {
		t.Errorf("the session was not adopted: %v", stored.ProjectID)
	}
}

// Two sessions in the same directory are the same project, not two.
func TestPromotingTheSameDirectoryTwiceReusesTheProject(t *testing.T) {
	h := newHarness(t)
	var ids []int64
	var workdir string
	for i := 0; i < 2; i++ {
		body := obj{"agent": "claude"}
		if workdir == "" {
			body["scratch"] = true
		} else {
			body["workdir"] = workdir
		}
		code, raw := h.request("POST", "/api/sessions", body, nil)
		if code != 201 {
			t.Fatalf("%d %s", code, raw)
		}
		var sess store.Session
		json.Unmarshal(raw, &sess)
		workdir = sess.Workdir
		ids = append(ids, sess.ID)
	}

	var projectIDs []int64
	for _, id := range ids {
		code, raw := h.request("POST", fmt.Sprintf("/api/sessions/%d/promote", id), obj{}, nil)
		if code != 200 {
			t.Fatalf("promote %d: %d %s", id, code, raw)
		}
		var out struct {
			Project store.Project `json:"project"`
		}
		json.Unmarshal(raw, &out)
		projectIDs = append(projectIDs, out.Project.ID)
	}
	if projectIDs[0] != projectIDs[1] {
		t.Errorf("one directory produced two projects: %v", projectIDs)
	}
}

// A session that already belongs somewhere must not be silently re-homed.
func TestPromotingAnAttachedSessionIsRefused(t *testing.T) {
	h := newHarness(t)
	project := h.seededProjectID()
	code, body := h.request("POST", "/api/sessions",
		obj{"agent": "claude", "project_id": project}, nil)
	if code != 201 {
		t.Fatalf("%d %s", code, body)
	}
	var sess store.Session
	json.Unmarshal(body, &sess)
	if code, body := h.request("POST", fmt.Sprintf("/api/sessions/%d/promote", sess.ID),
		obj{"name": "somewhere else"}, nil); code != 409 {
		t.Errorf("expected a refusal, got %d %s", code, body)
	}
}

func TestPromotingAnUnknownSessionIs404(t *testing.T) {
	h := newHarness(t)
	if code := h.status("POST", "/api/sessions/9999/promote", obj{"name": "x"}); code != 404 {
		t.Errorf("got %d", code)
	}
}

// ---- the real thing ----------------------------------------------------

// The whole flow on this machine: a real blank tmux session with a real agent
// process in a real directory, worked in, then promoted into a project that a
// task can actually be dispatched against.
func TestARealScratchSessionIsPromotableAndDispatchable(t *testing.T) {
	r := newInteractiveRig(t)
	sess := r.launchSession(map[string]any{
		"agent": "claude", "scratch": true, "name": "spike"})

	if sess.ProjectID != nil {
		t.Error("a scratch session should start unattached")
	}
	r.waitForLog(sess.Workdir, "argv:", 10*time.Second)
	if !tmuxAlive(sess.TmuxSession) {
		t.Fatal("no real tmux session was started")
	}
	// the directory is real, and it is a git repository so it can host worktrees
	if _, err := os.Stat(sess.Workdir); err != nil {
		t.Fatalf("the scratch directory is not on disk: %v", err)
	}
	if _, err := os.Stat(filepath.Join(sess.Workdir, ".git")); err != nil {
		t.Fatalf("a scratch directory must be a git repository to be promotable: %v", err)
	}

	// do some work in the room
	if err := os.WriteFile(filepath.Join(sess.Workdir, "idea.md"),
		[]byte("# the idea\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	code, body := r.do("POST", fmt.Sprintf("/api/sessions/%d/promote", sess.ID),
		map[string]any{"name": "the-spike"})
	if code != 200 {
		t.Fatalf("promote: %d %s", code, body)
	}
	var out struct {
		Project store.Project `json:"project"`
	}
	json.Unmarshal(body, &out)

	// the tmux session is untouched by promotion — the operator is still in it
	if !tmuxAlive(sess.TmuxSession) {
		t.Fatal("promoting the session killed the conversation")
	}
	// and the work is still where it was
	if _, err := os.Stat(filepath.Join(out.Project.RepoPath, "idea.md")); err != nil {
		t.Errorf("promotion moved the work: %v", err)
	}

	// the real payoff: the new project can now be dispatched against, which
	// needs a real commit for a worktree to branch from
	mustRun(t, out.Project.RepoPath, "git", "config", "user.email", "t@example.com")
	mustRun(t, out.Project.RepoPath, "git", "config", "user.name", "t")
	mustRun(t, out.Project.RepoPath, "git", "add", "-A")
	mustRun(t, out.Project.RepoPath, "git", "commit", "-q", "-m", "the spike so far")

	// point the agent binary back at the one-shot fake for the dispatch
	if err := os.WriteFile(r.app.Cfg.ClaudeBin, []byte(fakeAgent), 0o755); err != nil {
		t.Fatal(err)
	}
	saved := r.project
	r.project = out.Project.ID
	id := r.dispatch("first real task", "continue the spike")
	task := r.waitStatus(id, "done", "review", "failed")
	r.project = saved
	if task.Status == "failed" {
		att, _ := r.app.DB.LatestAttempt(id)
		t.Fatalf("a promoted project could not run a task: %s", att.ResultJSON)
	}
	att, _ := r.app.DB.LatestAttempt(id)
	if att.WorktreePath == "" {
		t.Error("no worktree was created for the promoted project")
	}
}

// Deleting a promoted project must not fail on the session that references it.
// Sessions are real tmux sessions that outlive the record, so they go back to
// being unassigned rather than blocking the delete or being deleted with it.
func TestDeletingAPromotedProjectUnassignsItsSessions(t *testing.T) {
	h := newHarness(t)
	code, body := h.request("POST", "/api/sessions", obj{"agent": "claude", "scratch": true}, nil)
	if code != 201 {
		t.Fatalf("%d %s", code, body)
	}
	var sess store.Session
	json.Unmarshal(body, &sess)

	code, body = h.request("POST", fmt.Sprintf("/api/sessions/%d/promote", sess.ID),
		obj{"name": "short lived"}, nil)
	if code != 200 {
		t.Fatalf("promote: %d %s", code, body)
	}
	var out struct {
		Project store.Project `json:"project"`
	}
	json.Unmarshal(body, &out)

	if code, body := h.request("DELETE",
		fmt.Sprintf("/api/projects/%d", out.Project.ID), nil, nil); code != 204 {
		t.Fatalf("deleting the project: %d %s", code, body)
	}
	stored, err := h.App.DB.Session(sess.ID)
	if err != nil {
		t.Fatalf("the session was deleted along with the project: %v", err)
	}
	if stored.ProjectID != nil {
		t.Errorf("the session still points at a project that is gone: %v", *stored.ProjectID)
	}
	// and it is still listed, so it can be reassigned rather than lost
	code, body = h.request("GET", "/api/sessions", nil, nil)
	if code != 200 || !strings.Contains(string(body), fmt.Sprintf(`"id":%d`, sess.ID)) {
		t.Errorf("the session disappeared from the board: %s", body)
	}
}

// The model field is free text and always has been — the list only ever
// suggested. Offering Claude's shorthands under codex was worse than offering
// nothing, which is what sent someone looking for a model that was never
// missing.
func TestModelSuggestionsArePerAgent(t *testing.T) {
	h := newHarness(t)
	models := h.get("/api/models")

	claude, ok := models["claude"].([]any)
	if !ok || len(claude) == 0 {
		t.Fatalf("claude should suggest its shorthands: %v", models["claude"])
	}
	var names []string
	for _, m := range claude {
		names = append(names, fmt.Sprint(m))
	}
	if !strings.Contains(strings.Join(names, ","), "opus") {
		t.Errorf("claude models: %v", names)
	}
	// codex names its models differently and the set moves, so there is nothing
	// honest to hardcode — but it must still be a key, or the UI cannot tell
	// "no suggestions" from "no model switch"
	got, present := models["codex"]
	if !present {
		t.Fatal("codex takes -m, so it must appear with an (empty) list")
	}
	for _, m := range got.([]any) {
		if strings.Contains("fable opus sonnet haiku", fmt.Sprint(m)) {
			t.Errorf("codex was offered a Claude model name: %v", m)
		}
	}
	// gemini has a model flag too; an agent without one must be absent entirely
	if code, body := h.request("PUT", "/api/agents", []obj{
		{"name": "claude", "command": "claude", "model_flag": "--model"},
		{"name": "noswitch", "command": "noswitch"},
	}, nil); code != 200 {
		t.Fatalf("defining agents: %d %s", code, body)
	}
	if _, present := h.get("/api/models")["noswitch"]; present {
		t.Error("an agent with no model flag must not be offered a model list")
	}
}

// A model you have actually run is the best suggestion there is, whatever the
// vendor happens to call it this month.
func TestAModelYouHaveUsedIsSuggestedAgain(t *testing.T) {
	h := newHarness(t)
	if code, body := h.request("PUT", "/api/agents", []obj{
		{"name": "claude", "command": "claude", "model_flag": "--model"},
		{"name": "codex", "command": "codex", "model_flag": "-m", "prompt_arg": true},
	}, nil); code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	code, body := h.request("POST", "/api/sessions", obj{
		"agent": "codex", "scratch": true, "model": "some-new-model"}, nil)
	if code != 201 {
		t.Fatalf("launching with a model the UI never suggested: %d %s", code, body)
	}
	var found bool
	for _, m := range h.get("/api/models")["codex"].([]any) {
		if fmt.Sprint(m) == "some-new-model" {
			found = true
		}
	}
	if !found {
		t.Error("a model that has been used is not suggested next time")
	}
	// and it must not leak into another agent's list
	for _, m := range h.get("/api/models")["claude"].([]any) {
		if fmt.Sprint(m) == "some-new-model" {
			t.Error("a codex model was suggested under claude")
		}
	}
}

// Yolo is on unless the caller says otherwise — the request that omits it is
// the common one, so that default is worth pinning.
func TestYoloIsOnByDefaultAndCanBeTurnedOff(t *testing.T) {
	for name, tc := range map[string]struct {
		body     obj
		wantFlag bool
	}{
		"omitted means on": {obj{"agent": "claude", "scratch": true}, true},
		"explicitly on":    {obj{"agent": "claude", "scratch": true, "yolo": true}, true},
		"explicitly off":   {obj{"agent": "claude", "scratch": true, "yolo": false}, false},
	} {
		h := newHarness(t)
		code, body := h.request("POST", "/api/sessions", tc.body, nil)
		if code != 201 {
			t.Fatalf("%s: %d %s", name, code, body)
		}
		cmd := strings.Join(h.mock().CmdLog(), "\n")
		got := strings.Contains(cmd, "bypassPermissions")
		if got != tc.wantFlag {
			t.Errorf("%s: yolo flag present=%v want=%v\n%s", name, got, tc.wantFlag, cmd)
		}
	}
}

// The picker has to know which agents can do it, or the checkbox lies.
func TestAgentsReportWhetherTheyHaveAYoloMode(t *testing.T) {
	h := newHarness(t)
	code, body := h.request("GET", "/api/agents", nil, nil)
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	var specs []struct {
		Name     string   `json:"name"`
		YoloArgs []string `json:"yolo_args"`
	}
	json.Unmarshal(body, &specs)
	for _, s := range specs {
		if len(s.YoloArgs) == 0 {
			t.Errorf("builtin %q reports no yolo mode; the UI would grey it out", s.Name)
		}
	}
}

// ---- project cleanup ---------------------------------------------------

// Deleting the right project out of eighty-one needs the facts that identify a
// dead one: what is attached, and when anything last happened.
func TestProjectUsageIdentifiesStaleProjects(t *testing.T) {
	h := newHarness(t)
	busy := h.seededProjectID()
	task := h.run(busy, "did work", "x", nil)
	h.waitStatus(task.id(), "review")

	code, body := h.request("GET", "/api/projects/usage", nil, nil)
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	var rows []struct {
		ProjectID  int64   `json:"project_id"`
		Tasks      int     `json:"tasks"`
		OpenTasks  int     `json:"open_tasks"`
		Sessions   int     `json:"sessions"`
		LastActive float64 `json:"last_active_at"`
	}
	json.Unmarshal(body, &rows)

	projects, _ := h.App.DB.Projects()
	if len(rows) != len(projects) {
		t.Fatalf("every project needs a row, got %d for %d projects", len(rows), len(projects))
	}
	var seen bool
	for _, r := range rows {
		if r.LastActive <= 0 {
			t.Errorf("project %d has no last-active time, so it cannot be sorted", r.ProjectID)
		}
		if r.ProjectID == busy {
			seen = true
			if r.Tasks < 1 {
				t.Errorf("a project with a task reports %d", r.Tasks)
			}
			if r.OpenTasks < 1 {
				t.Errorf("a task in review is still open, got %d", r.OpenTasks)
			}
		}
	}
	if !seen {
		t.Error("the busy project is missing from the usage list")
	}
}

// History is not thrown away by accident: a project with tasks is refused
// unless the caller says explicitly that the history goes too.
func TestDeletingAProjectWithHistoryNeedsCascade(t *testing.T) {
	h := newHarness(t)
	project := h.seededProjectID()
	task := h.run(project, "leaves history", "x", nil)
	h.waitStatus(task.id(), "review")

	code, body := h.request("DELETE", fmt.Sprintf("/api/projects/%d", project), nil, nil)
	if code != 409 {
		t.Fatalf("expected a refusal, got %d %s", code, body)
	}
	if !strings.Contains(string(body), "cascade") {
		t.Errorf("the refusal should say how to proceed: %s", body)
	}
	if _, err := h.App.DB.Project(project); err != nil {
		t.Fatal("the project was deleted despite the refusal")
	}

	// with cascade the project and everything under it goes
	if code, body := h.request("DELETE",
		fmt.Sprintf("/api/projects/%d?cascade=true", project), nil, nil); code != 204 {
		t.Fatalf("cascade delete: %d %s", code, body)
	}
	if _, err := h.App.DB.Project(project); err == nil {
		t.Error("the project survived a cascade delete")
	}
	if n, _ := h.App.DB.Count("tasks", "project_id=?", project); n != 0 {
		t.Errorf("%d tasks were orphaned", n)
	}
	if n, _ := h.App.DB.Count("attempts", "task_id=?", task.id()); n != 0 {
		t.Errorf("%d attempts were orphaned", n)
	}
	// and the board still works afterwards
	if code, _ := h.request("GET", "/api/tasks", nil, nil); code != 200 {
		t.Error("the board broke after a cascade delete")
	}
}

// An empty project is the common case in a cleanup and must not need cascade.
func TestDeletingAnEmptyProjectIsSimple(t *testing.T) {
	h := newHarness(t)
	targets, _ := h.App.DB.Targets()
	created := h.post("/api/projects", obj{
		"name": "abandoned", "target_id": targets[0].ID, "repo_path": "/mock/old"}, 201)
	id := int64(created.num("id"))
	if code, body := h.request("DELETE", fmt.Sprintf("/api/projects/%d", id), nil, nil); code != 204 {
		t.Fatalf("%d %s", code, body)
	}
	if _, err := h.App.DB.Project(id); err == nil {
		t.Error("it is still there")
	}
}

// An imported project has no tasks and no sessions, so lectern's own record
// says only "I learned about this at import time" — the same instant for all of
// them. The repository's last commit is what actually distinguishes a project
// abandoned two years ago from one touched last week.
func TestRepoCommitTimeIsUsedWhenLecternHasNoHistory(t *testing.T) {
	requireRealTools(t)
	r := newRealRig(t)

	// backdate the repo's only commit by a year
	old := time.Now().Add(-365 * 24 * time.Hour).Format(time.RFC3339)
	cmd := exec.Command("git", "commit", "-q", "--amend", "--no-edit", "--date", old)
	cmd.Dir = r.repo
	cmd.Env = append(os.Environ(), "GIT_COMMITTER_DATE="+old)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("backdating: %v %s", err, out)
	}

	code, body := r.do("GET", "/api/projects/usage", nil)
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	var rows []struct {
		ProjectID  int64   `json:"project_id"`
		Tasks      int     `json:"tasks"`
		LastActive float64 `json:"last_active_at"`
	}
	json.Unmarshal(body, &rows)

	var found bool
	for _, row := range rows {
		if row.ProjectID != r.project {
			continue
		}
		found = true
		age := time.Since(time.Unix(int64(row.LastActive), 0))
		if age < 300*24*time.Hour {
			t.Errorf("a project whose repo was last committed to a year ago reports "+
				"%.0f days of quiet — imported projects would all look equally fresh",
				age.Hours()/24)
		}
	}
	if !found {
		t.Fatal("the project is missing from the usage list")
	}
}
