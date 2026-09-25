package triggers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// fakeLinear is a minimal GraphQL server standing in for api.linear.app: it
// inspects the query text for a recognizable substring and answers with a
// canned response, logging every request body it received for assertions.
type fakeLinear struct {
	srv     *httptest.Server
	reqs    []map[string]any
	issues  string // raw JSON for the issues query's "nodes" array
	authKey string // Authorization header every request must carry
}

func newFakeLinear(t *testing.T, issuesNodesJSON string) *fakeLinear {
	t.Helper()
	f := &fakeLinear{issues: issuesNodesJSON, authKey: "lin_api_test123"}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != f.authKey {
			w.WriteHeader(401)
			w.Write([]byte(`{"errors":[{"message":"bad token"}]}`))
			return
		}
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		f.reqs = append(f.reqs, body)
		query, _ := body["query"].(string)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(query, "issues(filter:"):
			w.Write([]byte(`{"data":{"issues":{"nodes":` + f.issues + `}}}`))
		case strings.Contains(query, "commentCreate"):
			w.Write([]byte(`{"data":{"commentCreate":{"success":true}}}`))
		case strings.Contains(query, "workflowStates"):
			w.Write([]byte(`{"data":{"workflowStates":{"nodes":[{"id":"state-done"}]}}}`))
		case strings.Contains(query, "issueUpdate"):
			w.Write([]byte(`{"data":{"issueUpdate":{"success":true}}}`))
		case strings.Contains(query, "teams(filter:"):
			w.Write([]byte(`{"data":{"teams":{"nodes":[{"key":"ENG","name":"Engineering"}]}}}`))
		default:
			w.Write([]byte(`{"errors":[{"message":"unrecognized query"}]}`))
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func newLinearManager(t *testing.T, f *fakeLinear) (*Manager, *store.Project) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "linear.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	target, err := db.InsertTarget(&store.Target{Name: "t1", Kind: "mock"})
	if err != nil {
		t.Fatal(err)
	}
	project, err := db.InsertProject(&store.Project{Name: "p1", TargetID: target.ID, RepoPath: "/mock/p1"})
	if err != nil {
		t.Fatal(err)
	}
	m := New(db, nil, nil)
	m.HTTP = f.srv.Client()
	// linearRequest always POSTs to the real api.linear.app URL; point the
	// package var at the fake server's client via a custom RoundTripper
	// instead of trying to override the URL constant.
	m.HTTP.Transport = rewriteHost{f.srv.URL, http.DefaultTransport}
	return m, project
}

// rewriteHost redirects every request to base's host — the simplest way to
// keep linearAPIURL a real constant (so production code reads as production
// code) while tests still hit an httptest server.
type rewriteHost struct {
	base string
	next http.RoundTripper
}

func (r rewriteHost) RoundTrip(req *http.Request) (*http.Response, error) {
	baseURL, err := http.NewRequest(req.Method, r.base, nil)
	if err != nil {
		return nil, err
	}
	req.URL.Scheme = baseURL.URL.Scheme
	req.URL.Host = baseURL.URL.Host
	return r.next.RoundTrip(req)
}

const oneLinearIssueJSON = `[{"id":"iss-1","identifier":"ENG-1","title":"fix the bug","description":"details","url":"https://linear.app/x/issue/ENG-1","updatedAt":"2026-01-01T00:00:00Z","creator":{"email":"trusted@example.com","displayName":"Trusted User"}}]`

func TestPollLinearCreatesTaskForAllowedCreator(t *testing.T) {
	f := newFakeLinear(t, oneLinearIssueJSON)
	m, project := newLinearManager(t, f)
	var created NewTaskSpec
	m.CreateTask = func(_ *store.Project, spec NewTaskSpec) (*store.Task, error) {
		created = spec
		return &store.Task{ID: 9}, nil
	}
	src, err := m.DB.InsertTriggerSource(&store.TriggerSource{
		ProjectID: project.ID, Kind: "linear", Enabled: true,
		ConfigJSON:  `{"team_key":"ENG","allowed_users":["trusted@example.com"]}`,
		SecretsJSON: `{"api_key":"` + f.authKey + `"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.pollLinear(t.Context(), src); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(created.Title, "ENG-1") || !strings.Contains(created.Prompt, "fix the bug") {
		t.Fatalf("unexpected task spec: %+v", created)
	}
	events, err := m.DB.RecentTriggerEvents(project.ID, 10)
	if err != nil || len(events) != 1 || events[0].Action != "task_created" {
		t.Fatalf("expected one task_created event, got %v %v", events, err)
	}
	fresh, err := m.DB.TriggerSource(src.ID)
	if err != nil || fresh.Status != "ok" {
		t.Fatalf("expected the poll to record success, got %+v (%v)", fresh, err)
	}
}

func TestPollLinearRejectsUnknownCreator(t *testing.T) {
	f := newFakeLinear(t, oneLinearIssueJSON)
	m, project := newLinearManager(t, f)
	called := false
	m.CreateTask = func(*store.Project, NewTaskSpec) (*store.Task, error) {
		called = true
		return &store.Task{ID: 1}, nil
	}
	src, err := m.DB.InsertTriggerSource(&store.TriggerSource{
		ProjectID: project.ID, Kind: "linear", Enabled: true,
		ConfigJSON:  `{"team_key":"ENG","allowed_users":["someone-else@example.com"]}`,
		SecretsJSON: `{"api_key":"` + f.authKey + `"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.pollLinear(t.Context(), src); err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("an issue from a creator outside the allowlist must not create a task")
	}
}

func TestPostLinearCommentAndMoveState(t *testing.T) {
	f := newFakeLinear(t, "[]")
	m, _ := newLinearManager(t, f)
	if err := postLinearComment(t.Context(), m, f.authKey, "iss-1", "done"); err != nil {
		t.Fatal(err)
	}
	if err := moveLinearIssueState(t.Context(), m, f.authKey, "ENG", "Done", "iss-1"); err != nil {
		t.Fatal(err)
	}
	var sawUpdate bool
	for _, r := range f.reqs {
		if q, _ := r["query"].(string); strings.Contains(q, "issueUpdate") {
			vars, _ := r["variables"].(map[string]any)
			if vars["stateId"] == "state-done" && vars["id"] == "iss-1" {
				sawUpdate = true
			}
		}
	}
	if !sawUpdate {
		t.Fatalf("expected issueUpdate to resolve the state name to an id, requests: %+v", f.reqs)
	}
}

func TestLinearRequestRejectsBadAPIKey(t *testing.T) {
	f := newFakeLinear(t, "[]")
	m, _ := newLinearManager(t, f)
	if err := postLinearComment(t.Context(), m, "wrong-key", "iss-1", "done"); err == nil {
		t.Fatal("expected an error for a rejected API key")
	}
}
