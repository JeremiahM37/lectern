package api_test

// lectern is a systemd unit. It gets restarted — by a deploy, by a package
// upgrade, by the box rebooting — while real work is in flight, and the whole
// design rests on the claim that every piece of state lives in SQLite rather
// than in memory. That claim had never been tested. These tests run two full
// App lifetimes against one database file, which is what a restart actually is.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/internal/app"
	"github.com/JeremiahM37/lectern/internal/config"
	"github.com/JeremiahM37/lectern/internal/store"
)

// deployment is one database plus a fixed address, which successive Apps take
// turns owning — exactly the relationship a systemd unit has with its state.
type deployment struct {
	t    *testing.T
	dir  string
	addr string
	cfg  *config.Config
	app  *app.App
	http *http.Server
}

func newDeployment(t *testing.T) *deployment {
	t.Helper()
	dir := t.TempDir()
	// claim a port, then release it: every boot rebinds the SAME address, so the
	// BaseURL staged into an attempt before the restart still resolves after it
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := probe.Addr().String()
	probe.Close()
	d := &deployment{t: t, dir: dir, addr: addr, cfg: &config.Config{
		DBPath:           filepath.Join(dir, "lectern.db"),
		Mock:             true,
		TickInterval:     40 * time.Millisecond,
		MockAgentDelay:   40 * time.Millisecond,
		ApprovalPoll:     400 * time.Millisecond,
		ApprovalExpire:   900 * time.Second,
		SessionPoll:      40 * time.Millisecond,
		JanitorDays:      7,
		ClaudeBin:        "claude",
		CodexBin:         "codex",
		BaseURL:          "http://" + addr,
		HostClaudeConfig: filepath.Join(dir, "none.json"),
		ClaudeCredsPath:  filepath.Join(dir, "none.json"),
		CodexCredsPath:   filepath.Join(dir, "none.json"),
	}}
	t.Cleanup(d.stop)
	d.boot()
	return d
}

// boot starts a fresh App on the existing database, as a restart does.
func (d *deployment) boot() {
	d.t.Helper()
	a, err := app.New(d.cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		d.t.Fatal(err)
	}
	d.app = a
	ln, err := net.Listen("tcp", d.addr)
	if err != nil {
		d.t.Fatalf("rebinding %s after a restart: %v", d.addr, err)
	}
	d.http = &http.Server{Handler: a.Handler()}
	go d.http.Serve(ln)
	d.waitServing()
}

// waitServing blocks until the freshly booted App answers, so a test never races
// the listener.
func (d *deployment) waitServing() {
	d.t.Helper()
	for i := 0; i < 200; i++ {
		resp, err := http.Get(d.cfg.BaseURL + "/api/health")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	d.t.Fatal("the control plane never came back up")
}

func (d *deployment) stop() {
	if d.app == nil {
		return
	}
	d.http.Close() // closes the listener, freeing the port for the next boot
	d.app.Close()
	d.app, d.http = nil, nil
}

func (d *deployment) restart() {
	d.t.Helper()
	d.stop()
	d.boot()
}

func (d *deployment) do(method, path string, body any) (int, []byte) {
	d.t.Helper()
	var rdr io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rdr = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, d.cfg.BaseURL+path, rdr)
	if err != nil {
		d.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		d.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, out
}

func (d *deployment) seedProject() int64 {
	d.t.Helper()
	projects, err := d.app.DB.Projects()
	if err != nil || len(projects) == 0 {
		d.t.Fatalf("mock mode should seed a project: %v", err)
	}
	return projects[0].ID
}

func (d *deployment) newTask(project int64, title, prompt string) int64 {
	d.t.Helper()
	code, body := d.do("POST", "/api/tasks", map[string]any{
		"project_id": project, "title": title, "prompt": prompt})
	if code != 200 && code != 201 {
		d.t.Fatalf("creating a task: %d %s", code, body)
	}
	var out struct {
		ID int64 `json:"id"`
	}
	json.Unmarshal(body, &out)
	return out.ID
}

func (d *deployment) waitTask(id int64, want string, limit time.Duration) *store.Task {
	d.t.Helper()
	deadline := time.Now().Add(limit)
	var last string
	for time.Now().Before(deadline) {
		task, err := d.app.DB.Task(id)
		if err == nil {
			last = task.Status
			if task.Status == want {
				return task
			}
		}
		time.Sleep(15 * time.Millisecond)
	}
	d.t.Fatalf("task %d never reached %q (stuck at %q)", id, want, last)
	return nil
}

// The important half: a task mid-flight when the process dies must not be
// stranded in "running" forever. The new process re-reads running attempts from
// the database, finds the tmux session gone, and finalises it — visibly failed
// beats invisibly stuck.
func TestARestartDoesNotStrandARunningTask(t *testing.T) {
	d := newDeployment(t)
	project := d.seedProject()
	id := d.newTask(project, "long job", "work [mock:slow]")
	if code, body := d.do("POST", fmt.Sprintf("/api/tasks/%d/dispatch", id), map[string]any{}); code != 200 {
		t.Fatalf("dispatch: %d %s", code, body)
	}
	d.waitTask(id, "running", 4*time.Second)

	att, err := d.app.DB.LatestAttempt(id)
	if err != nil {
		t.Fatal(err)
	}
	d.restart()

	// the attempt row survived the process
	after, err := d.app.DB.Attempt(att.ID)
	if err != nil {
		t.Fatalf("the attempt did not survive the restart: %v", err)
	}
	if after.ID != att.ID {
		t.Fatalf("attempt identity changed across restart: %d -> %d", att.ID, after.ID)
	}
	// and the new scheduler takes ownership of it rather than ignoring it
	task := d.waitTask(id, "failed", 6*time.Second)
	if task.Status == "running" {
		t.Fatal("the task is stranded in running — no process owns it any more")
	}
	final, _ := d.app.DB.Attempt(att.ID)
	if final.Status == "running" {
		t.Errorf("attempt still running after its task finished: %+v", final)
	}
	t.Logf("recovered as %q / %q", task.Status, final.Status)
}

// A restart must leave the board itself intact — the reason the Python database
// could be opened by the Go binary at all.
func TestABoardSurvivesARestart(t *testing.T) {
	d := newDeployment(t)
	project := d.seedProject()
	id := d.newTask(project, "survives", "echo hi")
	if _, err := d.app.DB.InsertNote(project, "the auth module uses bcrypt", nil); err != nil {
		t.Fatal(err)
	}
	before, _ := d.app.DB.Projects()
	targetsBefore, _ := d.app.DB.Targets()

	d.restart()

	if _, err := d.app.DB.Task(id); err != nil {
		t.Fatalf("the task vanished: %v", err)
	}
	after, _ := d.app.DB.Projects()
	if len(after) != len(before) {
		t.Errorf("project count changed across restart: %d -> %d", len(before), len(after))
	}
	targetsAfter, _ := d.app.DB.Targets()
	if len(targetsAfter) != len(targetsBefore) {
		t.Errorf("seeding ran again on an existing database: %d -> %d targets",
			len(targetsBefore), len(targetsAfter))
	}
	notes, err := d.app.DB.ProjectNotes(project, 10)
	if err != nil || len(notes) == 0 {
		t.Fatalf("project memory did not survive: %v %d", err, len(notes))
	}
	// and the API serves it, not just the database
	code, body := d.do("GET", "/api/tasks", nil)
	if code != 200 {
		t.Fatalf("listing tasks after restart: %d %s", code, body)
	}
	var tasks []struct {
		ID    int64  `json:"id"`
		Title string `json:"title"`
	}
	if err := json.Unmarshal(body, &tasks); err != nil {
		d.t.Fatalf("task list after restart: %v (%s)", err, body)
	}
	var found bool
	for _, task := range tasks {
		if task.ID == id && task.Title == "survives" {
			found = true
		}
	}
	if !found {
		t.Errorf("the task is in the database but the API does not list it: %s", body)
	}
}

// A session is a tmux session the operator is talking to. The control plane
// restarting must not kill it, duplicate its row, or lose which project it
// belongs to — that link is what gives a resumed agent its context.
func TestASessionRowSurvivesARestart(t *testing.T) {
	d := newDeployment(t)
	project := d.seedProject()
	projectRow, err := d.app.DB.Project(project)
	if err != nil {
		t.Fatal(err)
	}
	code, body := d.do("POST", "/api/sessions", map[string]any{
		"target_id": projectRow.TargetID, "project_id": project,
		"agent": "claude", "cwd": "/home/admin/projects/thing"})
	if code != 200 && code != 201 {
		t.Fatalf("launching a session: %d %s", code, body)
	}
	var created struct {
		ID   int64  `json:"id"`
		Tmux string `json:"tmux_session"`
	}
	json.Unmarshal(body, &created)
	if created.ID == 0 || created.Tmux == "" {
		t.Fatalf("a session needs an id and a tmux name to be re-attachable: %s", body)
	}

	before, err := d.app.DB.Sessions(true)
	if err != nil {
		t.Fatal(err)
	}
	d.restart()
	time.Sleep(300 * time.Millisecond) // let the new session poller run

	after, err := d.app.DB.Sessions(true)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Errorf("session count changed across restart: %d -> %d (a duplicate row means "+
			"the poller re-adopted a session it already owned)", len(before), len(after))
	}
	sess, err := d.app.DB.Session(created.ID)
	if err != nil {
		t.Fatalf("the session row vanished: %v", err)
	}
	if sess.TmuxSession != created.Tmux {
		t.Errorf("the tmux name changed, so nothing can re-attach: %q -> %q",
			created.Tmux, sess.TmuxSession)
	}
	if sess.ProjectID == nil || *sess.ProjectID != project {
		t.Errorf("the session lost its project link, so a resume would have no context: %+v", sess.ProjectID)
	}
}
