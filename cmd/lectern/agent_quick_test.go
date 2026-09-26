package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/console"
)

func TestParseAgentQuickArgs(t *testing.T) {
	if opts, err := parseAgentQuickArgs("claude", nil); err != nil || (opts != agentQuickOpts{}) {
		t.Fatalf("no args should parse to zero opts, got %+v, %v", opts, err)
	}
	opts, err := parseAgentQuickArgs("claude", []string{"--model", "opus", "--resume"})
	if err != nil {
		t.Fatal(err)
	}
	if opts.Model != "opus" || !opts.Resume {
		t.Errorf("got %+v", opts)
	}
	if _, err := parseAgentQuickArgs("claude", []string{"--model"}); err == nil {
		t.Fatal("--model with no value should error")
	}
	if _, err := parseAgentQuickArgs("claude", []string{"--new", "--attach"}); err == nil {
		t.Fatal("--new and --attach together should error")
	}
	_, err = parseAgentQuickArgs("claude", []string{"--yolo"})
	if err == nil || !strings.Contains(err.Error(), `unsupported argument "--yolo"`) {
		t.Fatalf("unknown flag should error clearly, got %v", err)
	}
	if _, err := parseAgentQuickArgs("claude", []string{"some", "positional"}); err == nil {
		t.Fatal("a bare positional argument should error rather than being silently dropped")
	}
}

// agentQuickFakeAPI stubs just enough of the API surface for
// resolveAgentSession: GET /projects, GET /sessions, POST /sessions.
type agentQuickFakeAPI struct {
	projects []quickProjectView
	sessions []quickSessionView
	created  map[string]any // last POST /sessions body
	nextID   int64
}

func newAgentQuickFakeAPI() *agentQuickFakeAPI {
	return &agentQuickFakeAPI{nextID: 100}
}

func (f *agentQuickFakeAPI) server(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == "GET" && r.URL.Path == "/api/projects":
			json.NewEncoder(w).Encode(f.projects)
		case r.Method == "GET" && r.URL.Path == "/api/sessions":
			json.NewEncoder(w).Encode(f.sessions)
		case r.Method == "POST" && r.URL.Path == "/api/sessions":
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			f.created = body
			f.nextID++
			name, _ := body["name"].(string)
			agent, _ := body["agent"].(string)
			workdir, _ := body["workdir"].(string)
			w.WriteHeader(201)
			json.NewEncoder(w).Encode(quickSessionView{ID: f.nextID, Name: name, Agent: agent, Workdir: workdir})
		default:
			w.WriteHeader(404)
			fmt.Fprintf(w, `{"detail":"no route for %s %s"}`, r.Method, r.URL.Path)
		}
	}))
}

func TestResolveAgentSessionCreatesWithWorkdirNameAndProject(t *testing.T) {
	dir := t.TempDir()
	repo := filepath.Join(dir, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(repo, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	f := newAgentQuickFakeAPI()
	f.projects = []quickProjectView{{ID: 7, RepoPath: repo}}
	srv := f.server(t)
	defer srv.Close()
	c := console.New(srv.URL, "")

	sess, err := resolveAgentSession(c, "claude", sub, agentQuickOpts{}, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if sess.ID != f.nextID {
		t.Fatalf("got session id %d, want %d", sess.ID, f.nextID)
	}
	if f.created["agent"] != "claude" {
		t.Errorf("agent = %v", f.created["agent"])
	}
	if f.created["workdir"] != sub {
		t.Errorf("workdir = %v, want %v", f.created["workdir"], sub)
	}
	if f.created["name"] != filepath.Base(sub) {
		t.Errorf("name = %v, want %v", f.created["name"], filepath.Base(sub))
	}
	if got, ok := f.created["project_id"].(float64); !ok || int64(got) != 7 {
		t.Errorf("project_id = %v, want 7 (workdir is inside the registered repo)", f.created["project_id"])
	}
}

func TestResolveAgentSessionOmitsProjectWhenNoneMatches(t *testing.T) {
	dir := t.TempDir()
	f := newAgentQuickFakeAPI()
	f.projects = []quickProjectView{{ID: 1, RepoPath: t.TempDir()}} // unrelated repo
	srv := f.server(t)
	defer srv.Close()
	c := console.New(srv.URL, "")

	if _, err := resolveAgentSession(c, "codex", dir, agentQuickOpts{}, false, nil); err != nil {
		t.Fatal(err)
	}
	if _, has := f.created["project_id"]; has {
		t.Errorf("project_id should be absent, got %v", f.created["project_id"])
	}
}

func TestResolveAgentSessionPassesModelAndResume(t *testing.T) {
	dir := t.TempDir()
	f := newAgentQuickFakeAPI()
	srv := f.server(t)
	defer srv.Close()
	c := console.New(srv.URL, "")

	if _, err := resolveAgentSession(c, "claude", dir, agentQuickOpts{Model: "opus", Resume: true}, false, nil); err != nil {
		t.Fatal(err)
	}
	if f.created["model"] != "opus" {
		t.Errorf("model = %v", f.created["model"])
	}
	if f.created["resume"] != true {
		t.Errorf("resume = %v", f.created["resume"])
	}
}

func TestResolveAgentSessionReuseOnNonInteractiveDefaultsToExisting(t *testing.T) {
	dir := t.TempDir()
	f := newAgentQuickFakeAPI()
	f.sessions = []quickSessionView{{ID: 42, Name: "existing", Agent: "claude", Workdir: dir}}
	srv := f.server(t)
	defer srv.Close()
	c := console.New(srv.URL, "")

	sess, err := resolveAgentSession(c, "claude", dir, agentQuickOpts{}, false, func(*quickSessionView) (bool, error) {
		t.Fatal("confirm should not be called on a non-interactive terminal")
		return false, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if sess.ID != 42 {
		t.Fatalf("should have reused the existing session, got %+v", sess)
	}
	if f.created != nil {
		t.Errorf("should not have created a new session, created %v", f.created)
	}
}

func TestResolveAgentSessionInteractiveAsksAndHonorsAnswer(t *testing.T) {
	dir := t.TempDir()
	for _, tc := range []struct {
		name    string
		confirm func(*quickSessionView) (bool, error)
		wantID  int64
	}{
		{"yes", func(*quickSessionView) (bool, error) { return true, nil }, 42},
		{"no", func(*quickSessionView) (bool, error) { return false, nil }, 0}, // 0 means "a new one was created"
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newAgentQuickFakeAPI()
			f.sessions = []quickSessionView{{ID: 42, Name: "existing", Agent: "claude", Workdir: dir}}
			srv := f.server(t)
			defer srv.Close()
			c := console.New(srv.URL, "")
			asked := false
			sess, err := resolveAgentSession(c, "claude", dir, agentQuickOpts{}, true, func(s *quickSessionView) (bool, error) {
				asked = true
				return tc.confirm(s)
			})
			if err != nil {
				t.Fatal(err)
			}
			if !asked {
				t.Fatal("an interactive terminal with an existing session must ask")
			}
			if tc.wantID == 42 && sess.ID != 42 {
				t.Fatalf("expected reuse, got %+v", sess)
			}
			if tc.wantID == 0 && (sess.ID == 42 || f.created == nil) {
				t.Fatalf("expected a fresh session, got %+v (created=%v)", sess, f.created)
			}
		})
	}
}

func TestResolveAgentSessionNewAlwaysStartsFresh(t *testing.T) {
	dir := t.TempDir()
	f := newAgentQuickFakeAPI()
	f.sessions = []quickSessionView{{ID: 42, Name: "existing", Agent: "claude", Workdir: dir}}
	srv := f.server(t)
	defer srv.Close()
	c := console.New(srv.URL, "")

	sess, err := resolveAgentSession(c, "claude", dir, agentQuickOpts{New: true}, true, func(*quickSessionView) (bool, error) {
		t.Fatal("--new must not ask")
		return false, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if sess.ID == 42 {
		t.Fatal("--new should never reuse the existing session")
	}
	if f.created == nil {
		t.Fatal("--new should have created a session")
	}
}

func TestResolveAgentSessionAttachAlwaysReusesOrErrors(t *testing.T) {
	dir := t.TempDir()
	f := newAgentQuickFakeAPI()
	f.sessions = []quickSessionView{{ID: 42, Name: "existing", Agent: "claude", Workdir: dir}}
	srv := f.server(t)
	defer srv.Close()
	c := console.New(srv.URL, "")

	sess, err := resolveAgentSession(c, "claude", dir, agentQuickOpts{Attach: true}, true, func(*quickSessionView) (bool, error) {
		t.Fatal("--attach must not ask")
		return false, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if sess.ID != 42 {
		t.Fatalf("--attach should reuse the existing session, got %+v", sess)
	}

	f2 := newAgentQuickFakeAPI() // no existing sessions
	srv2 := f2.server(t)
	defer srv2.Close()
	c2 := console.New(srv2.URL, "")
	if _, err := resolveAgentSession(c2, "claude", dir, agentQuickOpts{Attach: true}, true, nil); err == nil {
		t.Fatal("--attach with nothing to reuse should error rather than silently create one")
	}
}

func TestFindLiveSessionMatchesAgentAndWorkdirOnly(t *testing.T) {
	dir := t.TempDir()
	f := newAgentQuickFakeAPI()
	f.sessions = []quickSessionView{
		{ID: 1, Agent: "codex", Workdir: dir},          // wrong agent
		{ID: 2, Agent: "claude", Workdir: t.TempDir()}, // wrong workdir
		{ID: 3, Agent: "claude", Workdir: dir},         // match
	}
	srv := f.server(t)
	defer srv.Close()
	c := console.New(srv.URL, "")
	sess, err := findLiveSession(c, "claude", dir)
	if err != nil {
		t.Fatal(err)
	}
	if sess == nil || sess.ID != 3 {
		t.Fatalf("got %+v", sess)
	}
}

func TestSessionDisplayNameIncludesGitBranch(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "f"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "f")
	run("commit", "-q", "-m", "init")
	name := sessionDisplayName(dir)
	want := filepath.Base(dir) + " · main"
	if name != want {
		t.Errorf("name = %q, want %q", name, want)
	}
}

func TestSessionDisplayNameWithoutGitFallsBackToBasename(t *testing.T) {
	dir := t.TempDir()
	if name := sessionDisplayName(dir); name != filepath.Base(dir) {
		t.Errorf("name = %q, want %q", name, filepath.Base(dir))
	}
}
