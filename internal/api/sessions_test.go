package api_test

// Sessions are the interactive half of the board: an agent you work WITH for
// days, as opposed to a task you hand off. These drive the whole loop — launch,
// status, send, attach, discover, adopt, hand off — against a real HTTP server
// and a scripted target.

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/executor"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

func (h *harness) session(body obj) obj {
	h.t.Helper()
	return h.post("/api/sessions", body, 201)
}

func (h *harness) sessionByID(id int64) obj {
	return h.get(fmt.Sprintf("/api/sessions/%d", id))
}

func (h *harness) waitSessionStatus(id int64, want ...string) obj {
	h.t.Helper()
	h.waitUntil(fmt.Sprintf("session %d to reach %v", id, want), func() bool {
		got := h.sessionByID(id).str("status")
		for _, w := range want {
			if got == w {
				return true
			}
		}
		return false
	})
	return h.sessionByID(id)
}

func TestSessionLaunchesAndReportsItsOwnState(t *testing.T) {
	h := newHarness(t)
	pid := h.seededProjectID()
	sess := h.session(obj{"project_id": pid, "name": "sglang", "agent": "claude",
		"model": "opus"})
	if sess.str("name") != "sglang" || sess.str("origin") != "lectern" {
		t.Fatalf("session: %v", sess)
	}
	if sess.str("tmux_session") == "" {
		t.Error("a session must own a tmux session")
	}
	// a project implies its target and working directory
	if int64(sess.num("target_id")) == 0 || sess.str("workdir") == "" {
		t.Errorf("project should supply target and workdir: %v", sess)
	}

	// status is derived from the pane, not from what we asked for
	live := h.waitSessionStatus(sess.id(), "waiting", "idle", "running")
	if live.str("pane_tail") == "" {
		t.Error("the card should preview what the pane is showing")
	}
	if _, ok := live["idle_seconds"].(float64); !ok {
		t.Error("idle time is the honest signal behind the status label")
	}
}

func TestQuickShellCreatesBlankTrackedSession(t *testing.T) {
	h := newHarness(t)
	targets := h.getList("/api/targets")
	if len(targets) == 0 {
		t.Fatal("harness has no target")
	}
	sess := h.post("/api/shells", obj{"target_id": targets[0].id()}, 201)
	if sess.str("agent") != "shell" || sess.str("model") != "" {
		t.Fatalf("quick shell must not select an agent or model: %v", sess)
	}
	if sess.num("project_id") != 0 || sess.str("workdir") == "" || sess.str("tmux_session") == "" {
		t.Fatalf("quick shell should have only a scratch directory and tmux session: %v", sess)
	}
	if sess.str("status") != "idle" {
		t.Fatalf("quick shell should be ready immediately: %v", sess)
	}
	if !strings.Contains(h.launchCmd(), "\"${SHELL:-/bin/sh}\" -i") {
		t.Fatalf("quick shell must invoke the target user's interactive shell: %s", h.launchCmd())
	}
	if strings.Contains(h.launchCmd(), "claude") || strings.Contains(h.launchCmd(), "codex") {
		t.Fatalf("quick shell unexpectedly launched an agent: %s", h.launchCmd())
	}
}

func TestQuickShellRejectsAmbiguousTargetSelection(t *testing.T) {
	h := newHarness(t)
	other, err := h.App.DB.InsertTarget(&store.Target{Name: "another machine", Kind: "local"})
	if err != nil {
		t.Fatal(err)
	}
	if other.ID == 0 {
		t.Fatal("target was not persisted")
	}
	if code := h.status("POST", "/api/shells", obj{"target_id": 999999, "machine": "local"}); code != 400 {
		t.Fatalf("conflicting shell selectors: got %d, want 400", code)
	}
}

func TestSessionProjectTargetMismatchIsRejected(t *testing.T) {
	h := newHarness(t)
	projectID := h.seededProjectID()
	project, err := h.App.DB.Project(projectID)
	if err != nil {
		t.Fatal(err)
	}
	other, err := h.App.DB.InsertTarget(&store.Target{Name: "other project target", Kind: "local"})
	if err != nil {
		t.Fatal(err)
	}
	if code := h.status("POST", "/api/sessions", obj{
		"project_id": projectID, "target_id": other.ID, "agent": "claude",
	}); code != 409 {
		t.Fatalf("project/target mismatch: got %d, want 409", code)
	}
	// An explicit copy of the project's target remains valid, and the manager
	// receives the same authoritative target as the project-only form.
	sess := h.session(obj{"project_id": projectID, "target_id": project.TargetID, "agent": "claude"})
	if int64(sess.num("target_id")) != project.TargetID {
		t.Fatalf("matching target was not retained: %v", sess)
	}
}

func TestSessionInteractiveLaunchIsNotAHeadlessTask(t *testing.T) {
	h := newHarness(t)
	h.session(obj{"project_id": h.seededProjectID(), "agent": "claude"})
	cmd := h.launchCmd()
	for _, unwanted := range []string{"stream-json", "prompt.md", "exit_code"} {
		if strings.Contains(cmd, unwanted) {
			t.Errorf("a session must not launch like a dispatched task (%q): %s", unwanted, cmd)
		}
	}
}

func TestSessionSendTypesIntoThePane(t *testing.T) {
	h := newHarness(t)
	sess := h.session(obj{"project_id": h.seededProjectID()})
	h.waitSessionStatus(sess.id(), "waiting", "idle", "running")

	h.post(fmt.Sprintf("/api/sessions/%d/send", sess.id()),
		obj{"text": "what is the state of the refactor?"}, 200)
	h.waitUntil("the message to appear on the pane", func() bool {
		return strings.Contains(h.sessionByID(sess.id()).str("pane_tail"), "state of the refactor")
	})
	// while the agent answers, the board should say it is working
	h.waitSessionStatus(sess.id(), "waiting", "idle")
}

func TestSessionSendKeyIsAnAllowlist(t *testing.T) {
	h := newHarness(t)
	sess := h.session(obj{"project_id": h.seededProjectID()})
	path := fmt.Sprintf("/api/sessions/%d/send", sess.id())
	h.post(path, obj{"key": "escape"}, 200)
	if code := h.status("POST", path, obj{"key": "rm -rf /"}); code != 422 {
		t.Fatalf("a raw input channel must only accept named keys, got %d", code)
	}
	if code := h.status("POST", path, obj{}); code != 400 {
		t.Errorf("an empty send: %d", code)
	}
}

func TestSessionAttachOpensATerminal(t *testing.T) {
	h := newHarness(t)
	h.App.Terminals.LookPath = func(string) (string, error) { return "/usr/bin/ttyd", nil }
	var argv []string
	h.App.Terminals.Spawn = func(port int, basePath string, a []string) (*exec.Cmd, error) {
		argv = a
		return exec.Command("true"), nil
	}
	sess := h.session(obj{"project_id": h.seededProjectID()})
	got := h.post(fmt.Sprintf("/api/sessions/%d/terminal", sess.id()), nil, 200)
	if got.num("port") == 0 {
		t.Fatalf("attach: %v", got)
	}
	// this is the "drop me into the actual chat" path — it must attach to the
	// session's own tmux, not to a task's
	if len(argv) < 4 || argv[3] != sess.str("tmux_session") {
		t.Fatalf("ttyd wrapped the wrong session: %v", argv)
	}
}

func TestSessionKillEndsItAndDismissClearsIt(t *testing.T) {
	h := newHarness(t)
	sess := h.session(obj{"project_id": h.seededProjectID()})
	h.waitSessionStatus(sess.id(), "waiting", "idle", "running")
	h.decode("DELETE", fmt.Sprintf("/api/sessions/%d", sess.id()), nil, 200, nil)
	if got := h.sessionByID(sess.id()).str("status"); got != "dead" {
		t.Fatalf("status after kill: %s", got)
	}
	// a dead session leaves the live list but keeps its record
	for _, s := range h.getList("/api/sessions") {
		if s.id() == sess.id() {
			t.Error("a dead session should not stay on the live list")
		}
	}
	found := false
	for _, s := range h.getList("/api/sessions?all=true") {
		if s.id() == sess.id() {
			found = true
		}
	}
	if !found {
		t.Error("the record must survive the process")
	}
}

func TestSessionPollNoticesAProcessThatVanished(t *testing.T) {
	h := newHarness(t)
	sess := h.session(obj{"project_id": h.seededProjectID()})
	h.waitSessionStatus(sess.id(), "waiting", "idle", "running")
	// kill the tmux session behind lectern's back, the way a reboot or a
	// stray `tmux kill-server` would
	mock := h.mock()
	if _, err := mock.Run(context.Background(), "tmux kill-session -t "+sess.str("tmux_session"),
		executor.RunOpts{Timeout: 20}); err != nil {
		t.Fatal(err)
	}
	h.waitSessionStatus(sess.id(), "dead")
}

// Discovery is the half a task board misses entirely.
func TestDiscoverFindsAgentsLecternDidNotStart(t *testing.T) {
	h := newHarness(t)
	found := h.getList("/api/sessions/discover")
	var legacy obj
	for _, c := range found {
		if c.str("tmux_session") == "legacy-claude" {
			legacy = c
		}
		if c.str("tmux_session") == "just-a-shell" {
			t.Error("a pane with no agent on it is just a terminal, not a candidate")
		}
	}
	if legacy == nil {
		t.Fatalf("the externally-started agent was not found: %v", found)
	}
	if legacy.str("agent") != "claude" || legacy.str("model") != "opus" {
		t.Errorf("candidate: %v", legacy)
	}
	if legacy["adopted"] != false {
		t.Error("an unknown agent must not claim to be adopted")
	}
	// its working directory matches a registered project, so the UI can offer
	// the right home for it instead of asking
	if legacy.str("project_name") != "demo-app" {
		t.Errorf("expected the matching project, got %q", legacy.str("project_name"))
	}
}

func TestAdoptTakesOverAnExistingTmuxSession(t *testing.T) {
	h := newHarness(t)
	pid := h.seededProjectID()
	sess := h.post("/api/sessions/adopt", obj{
		"target_id": h.firstTargetID(), "tmux_session": "legacy-claude",
		"project_id": pid, "name": "adopted", "workdir": "/mock/demo-app"}, 201)
	if sess.str("origin") != "discovered" {
		t.Errorf("origin: %v", sess.str("origin"))
	}
	if sess.str("tmux_session") != "legacy-claude" {
		t.Errorf("adoption must not rename the session: %v", sess)
	}
	// adopting twice is a conflict, not a duplicate card
	if code := h.status("POST", "/api/sessions/adopt", obj{
		"target_id": h.firstTargetID(), "tmux_session": "legacy-claude"}); code != 409 {
		t.Errorf("second adoption: %d", code)
	}
	// and discovery now reports THAT target's copy as known, so the UI does not
	// offer to adopt the same agent twice
	tid := h.firstTargetID()
	for _, c := range h.getList("/api/sessions/discover") {
		if c.str("tmux_session") != "legacy-claude" || int64(c.num("target_id")) != tid {
			continue
		}
		if c["adopted"] != true || int64(c.num("session_id")) != sess.id() {
			t.Errorf("an adopted session should show as adopted: %v", c)
		}
	}
}

func TestAdoptRefusesASessionThatIsNotThere(t *testing.T) {
	h := newHarness(t)
	code, body := h.request("POST", "/api/sessions/adopt", obj{
		"target_id": h.firstTargetID(), "tmux_session": "ghost-session"}, nil)
	if code != 400 {
		t.Fatalf("got %d %s", code, body)
	}
	if !strings.Contains(string(body), "no tmux session") {
		t.Errorf("the error should say what is missing: %s", body)
	}
}

// The handoff is what makes a six-month project survive the context window that
// happened to be working on it this week.
func TestHandoffWritesAWrapAndPrimesASuccessor(t *testing.T) {
	h := newHarness(t)
	pid := h.seededProjectID()
	sess := h.session(obj{"project_id": pid, "name": "long-haul"})
	h.waitSessionStatus(sess.id(), "waiting", "idle", "running")

	h.post(fmt.Sprintf("/api/sessions/%d/handoff", sess.id()),
		obj{"successor": true, "kill_old": true}, 202)

	// the wrap lands asynchronously — an agent mid-turn can take minutes
	h.waitUntil("the wrap to be written", func() bool {
		return len(h.getList(fmt.Sprintf("/api/sessions/%d/wraps", sess.id()))) > 0
	})
	wrap := h.getList(fmt.Sprintf("/api/sessions/%d/wraps", sess.id()))[0]
	if !strings.Contains(wrap.str("summary"), "WHERE WE ARE") {
		t.Fatalf("the wrap should carry the state the next agent needs: %q", wrap.str("summary"))
	}

	// the old session is retired and a fresh one carries the thread
	h.waitSessionStatus(sess.id(), "dead")
	var successor obj
	h.waitUntil("a successor session", func() bool {
		for _, s := range h.getList("/api/sessions") {
			if s.id() != sess.id() && s.str("name") == "long-haul" {
				successor = s
				return true
			}
		}
		return false
	})
	if successor.str("tmux_session") == sess.str("tmux_session") {
		t.Error("the successor must be a genuinely new process")
	}
	// The successor row is inserted before its tmux process is launched. The
	// wrap is linked only after Launch returns successfully; wait for that
	// transition before inspecting the launch command.
	h.waitUntil("the successor launch to finish", func() bool {
		for _, w := range h.getList(fmt.Sprintf("/api/sessions/%d/wraps", sess.id())) {
			if int64(w.num("next_session_id")) == successor.id() {
				return true
			}
		}
		return false
	})
	// the successor was primed with its predecessor's handoff — carried on the
	// launch command itself, so there is no paste to race
	primed := false
	for _, c := range h.mock().CmdLog() {
		if strings.HasPrefix(c, "tmux new-session") &&
			strings.Contains(c, successor.str("tmux_session")) &&
			strings.Contains(c, "continuing work on") &&
			strings.Contains(c, "WHERE WE ARE") {
			primed = true
		}
	}
	if !primed {
		t.Error("the successor was launched without its predecessor's handoff")
	}

	// and the wrap reached project memory, so a DISPATCHED task on this project
	// starts from the same state an interactive session would
	notes := h.getList(fmt.Sprintf("/api/projects/%d/notes", pid))
	if len(notes) == 0 || !strings.Contains(notes[0].str("note"), "Session handoff") {
		t.Errorf("the wrap did not reach project memory: %v", notes)
	}
	// the project's handoff thread is the continuity across every context
	if len(h.getList(fmt.Sprintf("/api/projects/%d/wraps", pid))) == 0 {
		t.Error("the project should carry its own handoff thread")
	}
}

func TestHandoffRefusesToRunTwiceAtOnce(t *testing.T) {
	h := newHarness(t)
	sess := h.session(obj{"project_id": h.seededProjectID()})
	h.waitSessionStatus(sess.id(), "waiting", "idle", "running")
	path := fmt.Sprintf("/api/sessions/%d/handoff", sess.id())
	h.post(path, obj{}, 202)
	if code := h.status("POST", path, obj{}); code != 409 {
		t.Fatalf("a second concurrent handoff should conflict, got %d", code)
	}
}

func TestSessionValidation(t *testing.T) {
	h := newHarness(t)
	if code := h.status("POST", "/api/sessions", obj{"project_id": 9999}); code != 400 {
		t.Errorf("unknown project: %d", code)
	}
	if code := h.status("POST", "/api/sessions", obj{}); code != 400 {
		t.Errorf("no project and no target: %d", code)
	}
	if code := h.status("POST", "/api/sessions",
		obj{"project_id": h.seededProjectID(), "agent": "cursor"}); code != 422 {
		t.Errorf("unknown agent: %d", code)
	}
	if code := h.status("GET", "/api/sessions/9999", nil); code != 404 {
		t.Errorf("unknown session: %d", code)
	}
}

// An agent you started three days ago should say "up 3d", not "up 4s". tmux
// knows when its own session began; adoption asks rather than assuming the
// moment lectern noticed is the moment work started.
func TestAdoptTakesTmuxsUptimeNotTheMomentWeNoticed(t *testing.T) {
	h := newHarness(t)
	sess := h.post("/api/sessions/adopt", obj{
		"target_id": h.firstTargetID(), "tmux_session": "legacy-claude",
		"workdir": "/mock/demo-app"}, 201)
	if up := sess.num("uptime_seconds"); up < 3000 {
		t.Fatalf("uptime should come from tmux, got %.0fs", up)
	}
	if idle := sess.num("idle_seconds"); idle < 60 {
		t.Errorf("the idle clock should start from tmux's activity stamp, got %.0fs", idle)
	}
}

// Adoption is non-destructive, so un-adoption must be too. A bulk re-adopt once
// called DELETE on seven adopted sessions and killed seven live conversations,
// because the default was "kill" for everything.
func TestDeleteReleasesAnAdoptedSessionWithoutKillingIt(t *testing.T) {
	h := newHarness(t)
	sess := h.post("/api/sessions/adopt", obj{
		"target_id": h.firstTargetID(), "tmux_session": "legacy-claude",
		"workdir": "/mock/demo-app"}, 201)

	got := h.request2("DELETE", fmt.Sprintf("/api/sessions/%d", sess.id()), nil, 200)
	if got["killed"] != false {
		t.Fatalf("an adopted session must not be killed by default: %v", got)
	}
	// it is off the board...
	for _, s := range h.getList("/api/sessions") {
		if s.id() == sess.id() {
			t.Error("released session should leave the live list")
		}
	}
	// ...and its terminal is still running, so discovery finds it again
	if !h.cmdLogHas("kill-session") {
		found := false
		for _, c := range h.getList("/api/sessions/discover") {
			if c.str("tmux_session") == "legacy-claude" && c["target_id"] == sess["target_id"] && c["adopted"] == false {
				found = true
			}
		}
		if !found {
			t.Error("the released terminal should still be discoverable")
		}
		return
	}
	t.Fatal("release must never issue a kill-session")
}

func TestDeleteKillsAnAdoptedSessionOnlyWhenAsked(t *testing.T) {
	h := newHarness(t)
	sess := h.post("/api/sessions/adopt", obj{
		"target_id": h.firstTargetID(), "tmux_session": "legacy-claude",
		"workdir": "/mock/demo-app"}, 201)
	got := h.request2("DELETE", fmt.Sprintf("/api/sessions/%d?kill=true", sess.id()), nil, 200)
	if got["killed"] != true {
		t.Fatalf("an explicit kill must kill: %v", got)
	}
	for _, candidate := range h.getList("/api/sessions/discover") {
		if candidate.str("tmux_session") == "legacy-claude" && candidate["target_id"] == sess["target_id"] {
			t.Error("stopped terminal is still discoverable")
		}
	}
}

// A session lectern launched is its own to end — no extra ceremony.
func TestDeleteKillsASessionLecternStarted(t *testing.T) {
	h := newHarness(t)
	sess := h.session(obj{"project_id": h.seededProjectID()})
	h.waitSessionStatus(sess.id(), "waiting", "idle", "running")
	got := h.request2("DELETE", fmt.Sprintf("/api/sessions/%d", sess.id()), nil, 200)
	if got["killed"] != true {
		t.Fatalf("an lectern-launched session should end on delete: %v", got)
	}
	if h.sessionByID(sess.id()).str("status") != "dead" {
		t.Error("status after delete")
	}
}

// Which project a session belongs to is a judgement the operator makes after the
// fact: an agent's working directory is frequently a scratch dir that matches
// nothing, so adoption must not be the last word.
func TestSessionCanBeReassignedToAProject(t *testing.T) {
	h := newHarness(t)
	sess := h.post("/api/sessions/adopt", obj{
		"target_id": h.firstTargetID(), "tmux_session": "legacy-claude",
		"workdir": "/some/scratch/dir"}, 201)
	if sess["project_id"] != nil {
		t.Fatalf("a scratch dir should match no project: %v", sess["project_id"])
	}
	pid := h.seededProjectID()
	var moved obj
	h.decode("PATCH", fmt.Sprintf("/api/sessions/%d", sess.id()),
		obj{"project_id": pid, "name": "renamed"}, 200, &moved)
	if int64(moved.num("project_id")) != pid || moved.str("name") != "renamed" {
		t.Fatalf("patched: %v", moved)
	}
	if moved.str("project_name") == "" {
		t.Error("the view should carry the project's name for grouping")
	}
	// and it can be un-assigned again
	var cleared obj
	h.decode("PATCH", fmt.Sprintf("/api/sessions/%d", sess.id()),
		obj{"project_id": nil}, 200, &cleared)
	if cleared["project_id"] != nil {
		t.Errorf("expected unassigned, got %v", cleared["project_id"])
	}
	if code := h.status("PATCH", fmt.Sprintf("/api/sessions/%d", sess.id()),
		obj{"project_id": 9999}); code != 400 {
		t.Errorf("unknown project: %d", code)
	}
}

// The dashboard tile leads with "how many agents are waiting for me", so those
// counts have to be flat fields that never disappear.
func TestHealthCarriesSessionCounts(t *testing.T) {
	h := newHarness(t)
	before := h.get("/api/health")
	for _, f := range []string{"sessions", "sessions_waiting"} {
		if _, ok := before[f].(float64); !ok {
			t.Fatalf("%s missing or not a number: %v", f, before[f])
		}
	}
	sess := h.session(obj{"project_id": h.seededProjectID()})
	h.waitSessionStatus(sess.id(), "waiting", "idle", "running")
	if h.get("/api/health").num("sessions") != 1 {
		t.Errorf("sessions: %v", h.get("/api/health")["sessions"])
	}
	h.waitUntil("the waiting count to reflect a session at its prompt", func() bool {
		return h.get("/api/health").num("sessions_waiting") >= 1 ||
			h.sessionByID(sess.id()).str("status") != "waiting"
	})
}

// Switching agents on a project is where context quietly disappears: Claude Code
// reads CLAUDE.md, codex reads AGENTS.md, and neither reads the other's. The
// brief names every onboarding doc the repo actually has, so the handover does
// not depend on which CLI is picking it up.
func TestBriefNamesTheReposOwnDocs(t *testing.T) {
	h := newHarness(t, realLocal)
	tid := h.localTarget(t)
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "HANDOFF.md"), "where we got to")
	writeFile(t, filepath.Join(repo, "AGENTS.md"), "codex reads this")
	writeFile(t, filepath.Join(repo, "README.md"), "readme")
	p := h.post("/api/projects",
		obj{"name": "ctxproj", "target_id": tid, "repo_path": repo}, 201)

	got := h.get(fmt.Sprintf("/api/projects/%d/brief", p.id()))
	brief := got.str("brief")
	for _, want := range []string{"HANDOFF.md", "AGENTS.md", "README.md", repo} {
		if !strings.Contains(brief, want) {
			t.Errorf("brief does not name %q:\n%s", want, brief)
		}
	}
	// a file the repo does not have must not be invented
	if strings.Contains(brief, "CLAUDE.md") {
		t.Errorf("brief names a document that is not there:\n%s", brief)
	}
	// the repo's own handoff is the most deliberate source, so it leads
	if !strings.HasPrefix(strings.TrimSpace(brief), "## Read these first") {
		t.Errorf("brief should open with the repo's own docs:\n%s", brief)
	}
}

func TestBriefCarriesNotesAndTheLastWrap(t *testing.T) {
	h := newHarness(t, realLocal)
	tid := h.localTarget(t)
	p := h.post("/api/projects", obj{"name": "carried", "target_id": tid,
		"repo_path": t.TempDir()}, 201)
	if _, err := h.App.DB.InsertNote(p.id(), "the fan is loud above 3GHz", nil); err != nil {
		t.Fatal(err)
	}
	// a wrap belongs to the session that wrote it, so make one to hang it off
	sess, err := h.App.DB.InsertSession(&store.Session{
		ProjectID: ptr(p.id()), TargetID: tid, Name: "prior", Agent: "claude",
		Workdir: "/tmp", TmuxSession: "gone"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.App.DB.InsertWrap(&store.Wrap{
		SessionID: sess.ID, ProjectID: ptr(p.id()),
		Summary: "mid-refactor of the tick loop"}); err != nil {
		t.Fatal(err)
	}
	brief := h.get(fmt.Sprintf("/api/projects/%d/brief", p.id())).str("brief")
	if !strings.Contains(brief, "mid-refactor of the tick loop") {
		t.Errorf("the last handoff is missing:\n%s", brief)
	}
	if !strings.Contains(brief, "the fan is loud") {
		t.Errorf("project notes are missing:\n%s", brief)
	}
}

func ptr[T any](v T) *T { return &v }

// "Any agent I want, CLI or local" — the board should not hold an opinion about
// which binary is in the terminal.
func TestCustomAgentsAreDefinableAndLaunchable(t *testing.T) {
	h := newHarness(t)
	builtins := h.getList("/api/agents")
	if len(builtins) != 3 {
		t.Fatalf("expected the three built-ins, got %d", len(builtins))
	}

	h.decode("PUT", "/api/agents", []obj{
		{"name": "aider", "command": "aider", "model_flag": "--model",
			"prompt_arg": true, "env": obj{"AIDER_DARK_MODE": "1"}},
	}, 200, nil)
	names := map[string]bool{}
	for _, a := range h.getList("/api/agents") {
		names[a.str("name")] = true
	}
	if !names["aider"] || !names["claude"] {
		t.Fatalf("agent set: %v", names)
	}

	// and a session actually launches with it
	sess := h.session(obj{"project_id": h.seededProjectID(), "agent": "aider",
		"model": "qwen3.6:35b-a3b"})
	if sess.str("agent") != "aider" {
		t.Fatalf("session agent: %v", sess.str("agent"))
	}
	cmd := h.launchCmd()
	for _, want := range []string{"aider", "--model qwen3.6:35b-a3b", "AIDER_DARK_MODE=1"} {
		if !strings.Contains(cmd, want) {
			t.Errorf("launch missing %q: %s", want, cmd)
		}
	}
}

func TestUnknownAgentIsRejectedWithSomethingActionable(t *testing.T) {
	h := newHarness(t)
	code, body := h.request("POST", "/api/sessions",
		obj{"project_id": h.seededProjectID(), "agent": "cursor"}, nil)
	if code != 422 || !strings.Contains(string(body), "/api/agents") {
		t.Fatalf("the error should say how to add it: %d %s", code, body)
	}
	if code := h.status("PUT", "/api/agents", []obj{{"name": "x"}}); code != 400 {
		t.Errorf("an agent with no command must be rejected: %d", code)
	}
}

// A project's env is the local-model door, and it has to reach sessions too —
// it only ever reached dispatched tasks before.
func TestSessionInheritsTheProjectsEnvForLocalModels(t *testing.T) {
	h := newHarness(t)
	p := h.post("/api/projects", obj{"name": "localmodel-sess",
		"target_id": h.firstTargetID(), "repo_path": "/mock/lm",
		"env": obj{"ANTHROPIC_BASE_URL": "http://ollama-host:11434",
			"ANTHROPIC_AUTH_TOKEN": "ollama"}}, 201)
	h.session(obj{"project_id": p.id(), "model": "qwen3.6:35b-a3b"})
	cmd := h.launchCmd()
	for _, want := range []string{
		"ANTHROPIC_BASE_URL=http://ollama-host:11434",
		"ANTHROPIC_AUTH_TOKEN=ollama", "--model qwen3.6:35b-a3b"} {
		if !strings.Contains(cmd, want) {
			t.Errorf("launch missing %q: %s", want, cmd)
		}
	}
}

// The point of a handoff is usually to move the work to a DIFFERENT agent —
// claude hands the inference project to codex, and codex starts knowing what
// happened. The successor must actually be that agent, launched with its own
// binary and its own flags, primed with what its predecessor wrote.
func TestHandoffCanMoveTheWorkToADifferentAgent(t *testing.T) {
	h := newHarness(t)
	pid := h.seededProjectID()
	sess := h.session(obj{"project_id": pid, "name": "inference", "agent": "claude"})
	h.waitSessionStatus(sess.id(), "waiting", "idle", "running")

	h.post(fmt.Sprintf("/api/sessions/%d/handoff", sess.id()),
		obj{"successor": true, "kill_old": true, "agent": "codex"}, 202)

	var successor obj
	h.waitUntil("a codex successor", func() bool {
		for _, s := range h.getList("/api/sessions") {
			if s.id() != sess.id() && s.str("name") == "inference" {
				// Session insertion precedes tmux naming/launch. The wrap's
				// successor link is published only after Launch returns.
				wraps, err := h.App.DB.SessionWraps(sess.id())
				if err != nil {
					return false
				}
				for _, wrap := range wraps {
					if wrap.NextSessionID != nil && *wrap.NextSessionID == s.id() {
						successor = s
						return true
					}
				}
			}
		}
		return false
	})
	if got := successor.str("agent"); got != "codex" {
		t.Fatalf("the work was handed to %q, not codex — a handoff that cannot "+
			"change agent is just a context reset", got)
	}
	// and it is genuinely codex that was launched, with codex's own flags
	var launched string
	for _, c := range h.mock().CmdLog() {
		if strings.HasPrefix(c, "tmux new-session") &&
			successor.str("tmux_session") != "" && strings.Contains(c, " -s "+successor.str("tmux_session")+" ") {
			launched = c
		}
	}
	if launched == "" {
		t.Fatal("the successor was never launched")
	}
	if !strings.Contains(launched, "codex") {
		t.Errorf("the successor did not run codex: %s", launched)
	}
	if strings.Contains(launched, "--permission-mode") {
		t.Errorf("claude's flags were passed to codex: %s", launched)
	}
	// it still carries the predecessor's state — that is the whole point
	if !strings.Contains(launched, "WHERE WE ARE") {
		t.Errorf("codex started without the handoff: %s", launched)
	}
	// the predecessor is retired, so there is one live session on the thread
	h.waitSessionStatus(sess.id(), "dead")
}

// Handing to the same agent is still valid — that is the context-window case.
func TestHandoffToTheSameAgentIsStillAContextReset(t *testing.T) {
	h := newHarness(t)
	sess := h.session(obj{"project_id": h.seededProjectID(),
		"name": "same-agent", "agent": "claude"})
	h.waitSessionStatus(sess.id(), "waiting", "idle", "running")

	h.post(fmt.Sprintf("/api/sessions/%d/handoff", sess.id()),
		obj{"successor": true, "agent": "claude"}, 202)
	h.waitUntil("a successor", func() bool {
		for _, s := range h.getList("/api/sessions") {
			if s.id() != sess.id() && s.str("name") == "same-agent" {
				return s.str("agent") == "claude"
			}
		}
		return false
	})
}

// An agent that was never defined must be refused before anything is asked of
// the running session.
func TestHandoffRefusesAnUnknownSuccessorAgent(t *testing.T) {
	h := newHarness(t)
	sess := h.session(obj{"project_id": h.seededProjectID(), "name": "x"})
	h.waitSessionStatus(sess.id(), "waiting", "idle", "running")
	if code, body := h.request("POST", fmt.Sprintf("/api/sessions/%d/handoff", sess.id()),
		obj{"successor": true, "agent": "not-an-agent"}, nil); code != 422 {
		t.Errorf("expected a refusal, got %d %s", code, body)
	}
	// and the session is untouched — no wrap was requested
	if len(h.getList(fmt.Sprintf("/api/sessions/%d/wraps", sess.id()))) != 0 {
		t.Error("a refused handoff still asked the agent to write one")
	}
}

// Keeping the old session is the obvious way to compare two agents on the same
// work. Successor used to imply killing it, so the option the UI offers did
// nothing and the comparison was impossible.
func TestAHandoffCanLeaveTheOldSessionRunning(t *testing.T) {
	h := newHarness(t)
	sess := h.session(obj{"project_id": h.seededProjectID(),
		"name": "compare", "agent": "claude"})
	h.waitSessionStatus(sess.id(), "waiting", "idle", "running")

	h.post(fmt.Sprintf("/api/sessions/%d/handoff", sess.id()),
		obj{"successor": true, "kill_old": false, "agent": "codex"}, 202)

	h.waitUntil("a codex successor", func() bool {
		for _, s := range h.getList("/api/sessions") {
			if s.id() != sess.id() && s.str("agent") == "codex" {
				return true
			}
		}
		return false
	})
	// the predecessor is still alive, so both can be worked with
	row, err := h.App.DB.Session(sess.id())
	if err != nil {
		t.Fatal(err)
	}
	if row.Status == "dead" {
		t.Error("the old session was killed despite kill_old:false — " +
			"there is no way to compare two agents on the same work")
	}
}

func TestLegacyReleasedRecordRequiresExplicitDiscovery(t *testing.T) {
	h := newHarness(t)
	sess := h.post("/api/sessions/adopt", obj{"target_id": h.firstTargetID(), "tmux_session": "legacy-claude", "workdir": "/mock/demo-app"}, 201)
	path := fmt.Sprintf("/api/sessions/%d", sess.id())
	// Represent a record released before identity capture existed. Releasing an
	// already-released record must not backfill an identity from a newer process.
	h.request2("DELETE", path, nil, 200)
	if err := h.App.DB.Update("sessions", sess.id(), map[string]any{"tracking_identity": ""}); err != nil {
		t.Fatal(err)
	}
	h.request2("DELETE", path, nil, 200)
	if h.get(path)["can_restore"] != false {
		t.Fatal("legacy record offered automatic recovery")
	}
	got := h.request2("POST", path+"/restore", obj{}, 409)
	if !strings.Contains(got.str("detail"), "explicitly") {
		t.Fatal(got)
	}
	h.request2("DELETE", path+"?kill=true", nil, 409)
}
