package trackers

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// recorder is an httptest server answering by path (and, for GraphQL, by a
// substring of the query), keeping every request for assertions.
type recorder struct {
	mu   sync.Mutex
	srv  *httptest.Server
	reqs []recorded
}

type recorded struct {
	Method, Path, Query, Auth string
	Body                     map[string]any
}

func newRecorder(t *testing.T, answer func(r recorded) (int, string)) *recorder {
	t.Helper()
	rec := &recorder{}
	rec.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		req := recorded{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Auth: r.Header.Get("Authorization")}
		_ = json.Unmarshal(raw, &req.Body)
		rec.mu.Lock()
		rec.reqs = append(rec.reqs, req)
		rec.mu.Unlock()
		code, body := answer(req)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		io.WriteString(w, body)
	}))
	t.Cleanup(rec.srv.Close)
	return rec
}

func (r *recorder) last(pred func(recorded) bool) *recorded {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := len(r.reqs) - 1; i >= 0; i-- {
		if pred(r.reqs[i]) {
			return &r.reqs[i]
		}
	}
	return nil
}

func gqlQuery(r recorded) string { q, _ := r.Body["query"].(string); return q }

func TestLinearClient(t *testing.T) {
	rec := newRecorder(t, func(r recorded) (int, string) {
		if r.Auth != "lin_api_k" {
			return 401, `{"errors":[{"message":"bad key"}]}`
		}
		q := gqlQuery(r)
		switch {
		case strings.Contains(q, "issues(filter"):
			return 200, `{"data":{"issues":{"nodes":[{"id":"u1","identifier":"ENG-4","title":"Add SSO","url":"https://linear.app/x/issue/ENG-4",
			  "priorityLabel":"High","updatedAt":"2026-09-26T00:00:00Z","state":{"id":"s2","name":"In Progress","type":"started"},
			  "assignee":{"displayName":"jo"},"labels":{"nodes":[{"name":"auth","color":"#ff0000"}]},"parent":{"identifier":"ENG-1"}}]}}}`
		case strings.Contains(q, "workflowStates"):
			return 200, `{"data":{"workflowStates":{"nodes":[{"id":"s3","name":"Done","type":"completed","position":0},
			  {"id":"s2","name":"In Progress","type":"started","position":1},{"id":"s1","name":"Todo","type":"unstarted","position":0},
			  {"id":"s0","name":"Backlog","type":"backlog","position":0}]}}}`
		case strings.Contains(q, "issue(id"):
			return 200, `{"data":{"issue":{"id":"u1","identifier":"ENG-4","title":"Add SSO","description":"Okta first.","url":"https://linear.app/x/issue/ENG-4",
			  "branchName":"jo/eng-4-add-sso","createdAt":"2026-09-20T00:00:00Z","state":{"id":"s2","name":"In Progress","type":"started"},
			  "team":{"id":"t1","key":"ENG"},"labels":{"nodes":[]},"parent":{"id":"u0","identifier":"ENG-1","title":"Auth epic","url":"p"},
			  "children":{"nodes":[{"id":"u5","identifier":"ENG-5","title":"SAML","state":{"name":"Todo","type":"unstarted"},"labels":{"nodes":[]}}]},
			  "comments":{"nodes":[{"body":"second","createdAt":"2026-09-22T00:00:00Z","user":{"name":"sam"}},{"body":"first","createdAt":"2026-09-21T00:00:00Z","user":{"displayName":"jo"}}]}}}}`
		case strings.Contains(q, "issueUpdate"):
			return 200, `{"data":{"issueUpdate":{"success":true}}}`
		case strings.Contains(q, "commentCreate"):
			return 200, `{"data":{"commentCreate":{"success":true}}}`
		}
		return 200, `{"errors":[{"message":"unrecognised"}]}`
	})
	l := &Linear{APIKey: "lin_api_k", URL: rec.srv.URL, HTTP: rec.srv.Client()}
	ctx := context.Background()
	items, err := l.Issues(ctx, "ENG", Filter{Mine: "assigned", Query: "sso"})
	if err != nil || len(items) != 1 || items[0].ID != "ENG-4" || items[0].StatusType != "started" ||
		items[0].Labels[0].Color != "ff0000" || items[0].Parent != "ENG-1" || items[0].Assignees[0] != "jo" {
		t.Fatalf("issues = %+v, %v", items, err)
	}
	vars := rec.last(func(r recorded) bool { return strings.Contains(gqlQuery(r), "issues(filter") }).Body["variables"].(map[string]any)
	filter, _ := json.Marshal(vars["filter"])
	for _, want := range []string{`"key":{"eq":"ENG"}`, `"isMe":{"eq":true}`, `"containsIgnoreCase":"sso"`, `"nin":["completed","canceled"]`} {
		if !strings.Contains(string(filter), want) {
			t.Errorf("filter %s lacks %s", filter, want)
		}
	}
	d, err := l.Issue(ctx, "ENG-4")
	if err != nil {
		t.Fatal(err)
	}
	if d.BranchName != "jo/eng-4-add-sso" || d.UID != "u1" || d.Team != "ENG" || len(d.Children) != 1 || d.ParentItem.ID != "ENG-1" {
		t.Fatalf("detail = %+v", d)
	}
	if len(d.Timeline) != 2 || d.Timeline[0].Body != "first" || d.Timeline[0].Author != "jo" {
		t.Fatalf("comments not in time order: %+v", d.Timeline)
	}
	var order []string
	for _, s := range d.Transitions {
		order = append(order, s.Name)
	}
	if strings.Join(order, ",") != "Backlog,Todo,In Progress,Done" {
		t.Fatalf("states in board order = %v", order)
	}
	if err := l.SetState(ctx, "u1", "s3"); err != nil {
		t.Fatal(err)
	}
	if err := l.Comment(ctx, "u1", "on it"); err != nil {
		t.Fatal(err)
	}
	bad := &Linear{APIKey: "nope", URL: rec.srv.URL, HTTP: rec.srv.Client()}
	if _, err := bad.Teams(ctx); err == nil || !strings.Contains(err.Error(), "rejected the API key") {
		t.Fatalf("bad key: %v", err)
	}
}

func jiraAnswer(v3 bool) func(r recorded) (int, string) {
	desc := `"Plain *wiki* description"`
	comment := `"a v2 comment"`
	if v3 {
		desc = `{"type":"doc","version":1,"content":[{"type":"paragraph","content":[{"type":"text","text":"ADF description"}]}]}`
		comment = `{"type":"doc","version":1,"content":[{"type":"paragraph","content":[{"type":"text","text":"an ADF comment"}]}]}`
	}
	issue := `{"id":"10001","key":"OPS-7","fields":{"summary":"Rotate keys","description":` + desc + `,
	  "status":{"name":"In Progress","statusCategory":{"key":"indeterminate"}},"assignee":{"displayName":"Jo"},
	  "reporter":{"displayName":"Sam","emailAddress":"sam@example.com","accountId":"acc-1"},"labels":["lectern"],
	  "priority":{"name":"High"},"updated":"2026-09-26T10:00:00.000+0000","created":"2026-09-20T10:00:00.000+0000",
	  "subtasks":[{"id":"10002","key":"OPS-8","fields":{"summary":"Staging","status":{"name":"To Do","statusCategory":{"key":"new"}}}}],
	  "comment":{"comments":[{"author":{"displayName":"Jo"},"body":` + comment + `,"created":"2026-09-21T10:00:00.000+0000"}]}}}`
	return func(r recorded) (int, string) {
		switch {
		case strings.HasSuffix(r.Path, "/myself"):
			return 200, `{"displayName":"Sam"}`
		case strings.HasSuffix(r.Path, "/search/jql") || strings.HasSuffix(r.Path, "/search"):
			return 200, `{"issues":[` + issue + `]}`
		case strings.HasSuffix(r.Path, "/transitions") && r.Method == "GET":
			return 200, `{"transitions":[{"id":"21","name":"Start","to":{"name":"In Progress","statusCategory":{"key":"indeterminate"}}},
			  {"id":"31","name":"Resolve","to":{"name":"Done","statusCategory":{"key":"done"}}}]}`
		case strings.HasSuffix(r.Path, "/transitions"):
			return 204, ``
		case strings.HasSuffix(r.Path, "/comment"):
			return 201, `{"id":"1"}`
		case strings.Contains(r.Path, "/issue/OPS-7"):
			return 200, issue
		case strings.Contains(r.Path, "/issue/"):
			return 404, `{"errorMessages":["Issue does not exist or you do not have permission to see it."]}`
		}
		return 404, `{}`
	}
}

func TestJiraCloud(t *testing.T) {
	rec := newRecorder(t, jiraAnswer(true))
	// the recorder is not *.atlassian.net, so name the flavor explicitly
	j := &Jira{BaseURL: rec.srv.URL, Flavor: "cloud", Email: "sam@example.com", Token: "tok", HTTP: rec.srv.Client()}
	ctx := context.Background()
	items, err := j.Issues(ctx, "OPS", "", Filter{Mine: "assigned"})
	if err != nil || len(items) != 1 || items[0].ID != "OPS-7" || items[0].StatusType != "indeterminate" || items[0].URL != rec.srv.URL+"/browse/OPS-7" {
		t.Fatalf("issues = %+v %v", items, err)
	}
	search := rec.last(func(r recorded) bool { return strings.Contains(r.Path, "search") })
	if search.Path != "/rest/api/3/search/jql" || !strings.Contains(search.Query, "assignee+%3D+currentUser%28%29") {
		t.Fatalf("cloud search = %+v", search)
	}
	if want := "Basic " + base64.StdEncoding.EncodeToString([]byte("sam@example.com:tok")); search.Auth != want {
		t.Fatalf("cloud auth = %q", search.Auth)
	}
	d, err := j.Issue(ctx, "OPS-7")
	if err != nil {
		t.Fatal(err)
	}
	if d.Body != "ADF description" || d.Timeline[0].Body != "an ADF comment" || len(d.Children) != 1 || len(d.Transitions) != 2 ||
		d.Transitions[1].Name != "Resolve → Done" || d.BranchName != "OPS-7-rotate-keys" {
		t.Fatalf("detail = %+v", d)
	}
	if err := j.Comment(ctx, "OPS-7", "done"); err != nil {
		t.Fatal(err)
	}
	c := rec.last(func(r recorded) bool { return strings.HasSuffix(r.Path, "/comment") })
	if body, _ := c.Body["body"].(map[string]any); body["type"] != "doc" {
		t.Fatalf("cloud comment is not ADF: %+v", c.Body)
	}
	if err := j.TransitionByName(ctx, "OPS-7", "done"); err != nil {
		t.Fatal(err)
	}
	tr := rec.last(func(r recorded) bool { return r.Method == "POST" && strings.HasSuffix(r.Path, "/transitions") })
	if id := tr.Body["transition"].(map[string]any)["id"]; id != "31" {
		t.Fatalf("transition id = %v", id)
	}
	if err := j.TransitionByName(ctx, "OPS-7", "Archived"); err == nil {
		t.Fatal("a missing transition was guessed")
	}
	if _, err := j.Issue(ctx, "OPS-99"); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("missing issue error = %v", err)
	}
}

func TestJiraServerDataCenter(t *testing.T) {
	rec := newRecorder(t, jiraAnswer(false))
	j := &Jira{BaseURL: rec.srv.URL, Token: "pat", HTTP: rec.srv.Client()} // flavor guessed: not atlassian.net → server
	ctx := context.Background()
	d, err := j.Issue(ctx, "OPS-7")
	if err != nil || d.Body != "Plain *wiki* description" || d.Timeline[0].Body != "a v2 comment" {
		t.Fatalf("detail = %+v %v", d, err)
	}
	if _, err := j.Search(ctx, "project = OPS", 5); err != nil {
		t.Fatal(err)
	}
	search := rec.last(func(r recorded) bool { return strings.Contains(r.Path, "search") })
	if search.Path != "/rest/api/2/search" || search.Auth != "Bearer pat" {
		t.Fatalf("server search = %+v", search)
	}
	if err := j.Comment(ctx, "OPS-7", "done"); err != nil {
		t.Fatal(err)
	}
	c := rec.last(func(r recorded) bool { return strings.HasSuffix(r.Path, "/comment") })
	if c.Body["body"] != "done" {
		t.Fatalf("server comment body = %+v", c.Body)
	}
	if JiraFlavor("https://acme.atlassian.net", "") != "cloud" || JiraFlavor("https://jira.corp", "") != "server" {
		t.Fatal("flavor guess")
	}
	if err := (&Jira{BaseURL: "https://acme.atlassian.net", Token: "t"}).Validate(); err == nil {
		t.Fatal("cloud without an email accepted")
	}
	if err := (&Jira{BaseURL: "ftp://x", Token: "t"}).Validate(); err == nil {
		t.Fatal("non-http base url accepted")
	}
}
