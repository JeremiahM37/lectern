package triggers

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// fakeJira stands in for a Jira Server/Data Center REST v2 API (Bearer PAT):
// search answers with the configured issues, and every request is kept.
type fakeJira struct {
	mu     sync.Mutex
	srv    *httptest.Server
	issues []string
	reqs   []*http.Request
	bodies []map[string]any
}

func newFakeJira(t *testing.T, issues ...string) *fakeJira {
	t.Helper()
	f := &fakeJira{issues: issues}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer pat-1" {
			w.WriteHeader(401)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		f.mu.Lock()
		f.reqs, f.bodies = append(f.reqs, r), append(f.bodies, body)
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/rest/api/2/search"):
			io.WriteString(w, `{"issues":[`+strings.Join(f.issues, ",")+`]}`)
		case strings.HasSuffix(r.URL.Path, "/myself"):
			io.WriteString(w, `{"displayName":"Lectern Bot","name":"lectern"}`)
		case strings.HasSuffix(r.URL.Path, "/transitions") && r.Method == "GET":
			io.WriteString(w, `{"transitions":[{"id":"5","name":"Start work","to":{"name":"In Progress"}},{"id":"9","name":"Close","to":{"name":"Done"}}]}`)
		case strings.HasSuffix(r.URL.Path, "/transitions"), strings.HasSuffix(r.URL.Path, "/comment"):
			w.WriteHeader(204)
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeJira) find(pred func(*http.Request) bool) (*http.Request, map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.reqs) - 1; i >= 0; i-- {
		if pred(f.reqs[i]) {
			return f.reqs[i], f.bodies[i]
		}
	}
	return nil, nil
}

func jiraIssueJSON(id, key, summary, reporterName string) string {
	return `{"id":"` + id + `","key":"` + key + `","fields":{"summary":"` + summary + `","description":"Details for ` + key + `",
	  "reporter":{"name":"` + reporterName + `","displayName":"` + strings.ToUpper(reporterName) + `"},"labels":["lectern"],
	  "status":{"name":"To Do","statusCategory":{"key":"new"}},"updated":"2026-09-27T09:00:00.000+0000"}}`
}

func newJiraManager(t *testing.T) (*Manager, *store.Project) {
	t.Helper()
	m, project := newTestManager(t)
	m.HTTP = &http.Client{Timeout: 5 * time.Second}
	return m, project
}

func jiraSource(t *testing.T, m *Manager, project *store.Project, f *fakeJira, extra string) *store.TriggerSource {
	t.Helper()
	cfg := `{"base_url":"` + f.srv.URL + `","project_key":"OPS","allowed_users":["alice"]` + extra + `}`
	if err := ValidateConfig(KindJira, cfg, `{"token":"pat-1"}`); err != nil {
		t.Fatal(err)
	}
	src, err := m.DB.InsertTriggerSource(&store.TriggerSource{ProjectID: project.ID, Kind: "jira", Enabled: true,
		ConfigJSON: cfg, SecretsJSON: `{"token":"pat-1"}`, CursorJSON: `{"since":"2026-09-27T08:00:00Z"}`})
	if err != nil {
		t.Fatal(err)
	}
	return src
}

func TestJiraTriggerDispatchesAllowlistedReporter(t *testing.T) {
	f := newFakeJira(t, jiraIssueJSON("100", "OPS-1", "Rotate keys", "alice"), jiraIssueJSON("101", "OPS-2", "Drop tables", "mallory"))
	m, project := newJiraManager(t)
	m.now = func() time.Time { return time.Date(2026, 9, 27, 8, 30, 0, 0, time.UTC) }
	var specs []NewTaskSpec
	m.CreateTask = func(_ *store.Project, spec NewTaskSpec) (*store.Task, error) {
		specs = append(specs, spec)
		return &store.Task{ID: int64(len(specs))}, nil
	}
	src := jiraSource(t, m, project, f, "")
	// the scheduler's own entry point, not pollJira directly
	m.Tick(t.Context())
	if len(specs) != 1 || !strings.Contains(specs[0].Title, "[OPS-1] Rotate keys") ||
		!strings.Contains(specs[0].Prompt, "Details for OPS-1") || !strings.Contains(specs[0].Prompt, f.srv.URL+"/browse/OPS-1") ||
		specs[0].CreatedBy != "trigger:jira:"+strconv.FormatInt(src.ID, 10) {
		t.Fatalf("specs = %+v", specs)
	}
	req, _ := f.find(func(r *http.Request) bool { return strings.HasSuffix(r.URL.Path, "/search") })
	jql, _ := url.QueryUnescape(req.URL.Query().Get("jql"))
	if !strings.Contains(jql, `project = "OPS" AND labels = "lectern"`) || !strings.Contains(jql, `updated >= "-31m"`) {
		t.Fatalf("jql = %s", jql)
	}
	if !strings.Contains(req.URL.Query().Get("fields"), "description") {
		t.Fatalf("search did not ask for descriptions: %s", req.URL.RawQuery)
	}
	events, _ := m.DB.RecentTriggerEvents(project.ID, 10)
	actions := map[string]string{}
	for _, e := range events {
		actions[e.ExternalID] = e.Action + "/" + e.Author
	}
	if actions["jira:100"] != "task_created/alice" || !strings.HasPrefix(actions["jira:101"], "skipped/") {
		t.Fatalf("events = %v", actions)
	}
	// a redelivery of the same issues is a no-op
	fresh, _ := m.DB.TriggerSource(src.ID)
	if err := m.pollJira(t.Context(), fresh); err != nil {
		t.Fatal(err)
	}
	if len(specs) != 1 {
		t.Fatalf("an issue created a second task: %+v", specs)
	}
}

func TestJiraTriggerRateLimit(t *testing.T) {
	f := newFakeJira(t, jiraIssueJSON("200", "OPS-3", "One", "alice"), jiraIssueJSON("201", "OPS-4", "Two", "alice"))
	m, project := newJiraManager(t)
	n := 0
	m.CreateTask = func(*store.Project, NewTaskSpec) (*store.Task, error) { n++; return &store.Task{ID: int64(n)}, nil }
	src := jiraSource(t, m, project, f, `,"max_per_hour":1`)
	if err := m.pollJira(t.Context(), src); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("rate limit of 1/h let %d tasks through", n)
	}
}

func TestJiraPostbackCommentsAndTransitions(t *testing.T) {
	f := newFakeJira(t)
	m, project := newJiraManager(t)
	src := jiraSource(t, m, project, f, `,"done_transition":"done"`)
	ev := &store.TriggerEvent{ProjectID: project.ID, SourceID: src.ID, RawJSON: `{"issue_key":"OPS-1"}`}
	if err := m.postbackJira(t.Context(), src, ev, &store.Task{ID: 3, Status: "done"}, nil); err != nil {
		t.Fatal(err)
	}
	_, comment := f.find(func(r *http.Request) bool { return strings.HasSuffix(r.URL.Path, "/issue/OPS-1/comment") })
	if body, _ := comment["body"].(string); !strings.Contains(body, "Task #3") {
		t.Fatalf("comment = %v", comment)
	}
	_, tr := f.find(func(r *http.Request) bool { return r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/transitions") })
	if tr == nil || tr["transition"].(map[string]any)["id"] != "9" {
		t.Fatalf("transition = %v (matched by destination status name)", tr)
	}
	// a failed task only comments
	f2 := newFakeJira(t)
	src2 := jiraSource(t, m, project, f2, "")
	if err := m.postbackJira(t.Context(), src2, ev, &store.Task{ID: 4, Status: "failed"}, nil); err != nil {
		t.Fatal(err)
	}
	if r, _ := f2.find(func(r *http.Request) bool { return strings.HasSuffix(r.URL.Path, "/transitions") }); r != nil {
		t.Fatal("a failed task moved its issue")
	}
}

func TestJiraConfigValidationAndTest(t *testing.T) {
	if err := ValidateConfig(KindJira, `{"base_url":"https://x.atlassian.net"}`, `{"token":"t"}`); err == nil || !strings.Contains(err.Error(), "project_key") {
		t.Fatalf("missing project key: %v", err)
	}
	if err := ValidateConfig(KindJira, `{"base_url":"https://x.atlassian.net","project_key":"OPS"}`, `{"token":"t"}`); err == nil || !strings.Contains(err.Error(), "email") {
		t.Fatalf("cloud without email: %v", err)
	}
	if err := ValidateConfig(KindJira, `{"base_url":"https://jira.corp","project_key":"OPS"}`, `{}`); err == nil {
		t.Fatal("missing token accepted")
	}
	f := newFakeJira(t)
	m, project := newJiraManager(t)
	src := jiraSource(t, m, project, f, "")
	msg, err := m.TestConnection(t.Context(), src)
	if err != nil || !strings.Contains(msg, "Lectern Bot") {
		t.Fatalf("test connection = %q %v", msg, err)
	}
	if got := jiraSinceClause("2026-09-27T08:00:00Z", time.Date(2026, 9, 27, 8, 0, 20, 0, time.UTC)); got != ` AND updated >= "-2m"` {
		t.Fatalf("since clause = %q", got)
	}
}
