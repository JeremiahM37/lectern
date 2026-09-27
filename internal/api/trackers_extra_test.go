package api_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestReactionsOnPullRequestsAndComments(t *testing.T) {
	h := newHarness(t)
	pid := h.seededProjectID()
	base := fmt.Sprintf("/api/projects/%d/forge/prs/14", pid)
	h.post(base+"/reactions", obj{"emoji": "hooray"}, 200)
	pr := h.get(base)
	var body []string
	for _, r := range pr.list("reactions") {
		body = append(body, fmt.Sprintf("%s%v", r.str("emoji"), r.num("count")))
	}
	if strings.Join(body, ",") != "🎉1,🚀2" {
		t.Fatalf("PR reactions = %v", body)
	}
	comment := pr.list("timeline")
	var id string
	for _, e := range comment {
		if e.str("kind") == "comment" {
			id = e.str("id")
		}
	}
	h.post(base+"/reactions", obj{"subject": id, "emoji": "+1"}, 200)
	for _, e := range h.get(base).list("timeline") {
		if e.str("id") == id && (len(e.list("reactions")) != 1 || e.list("reactions")[0].num("count") != 1) {
			t.Fatalf("comment reactions = %v", e["reactions"])
		}
	}
	if code := h.status("POST", base+"/reactions", obj{"emoji": "thumbsup"}); code != 422 {
		t.Fatalf("unknown emoji: %d", code)
	}
	h.post(fmt.Sprintf("/api/projects/%d/forge/issues/3/reactions", pid), obj{"emoji": "eyes"}, 200)
	if r := h.get(fmt.Sprintf("/api/projects/%d/forge/issues/3", pid)).list("reactions"); len(r) != 1 || r[0].str("emoji") != "👀" {
		t.Fatalf("issue reactions = %v", r)
	}
}

func TestMergeQueueView(t *testing.T) {
	h := newHarness(t)
	pid := h.seededProjectID()
	q := h.get(fmt.Sprintf("/api/projects/%d/forge/queue", pid))
	if q["supported"] != true || q.str("base") != "main" || len(q.list("entries")) != 0 {
		t.Fatalf("empty queue = %v", q)
	}
	for _, n := range []int{17, 15} {
		pr := h.get(fmt.Sprintf("/api/projects/%d/forge/prs/%d", pid, n))
		h.post(fmt.Sprintf("/api/projects/%d/forge/prs/%d/merge", pid, n), obj{"method": "merge", "auto": true, "confirm": true, "head_sha": pr.str("head_sha")}, 200)
	}
	entries := h.get(fmt.Sprintf("/api/projects/%d/forge/queue?base=main", pid)).list("entries")
	if len(entries) != 2 || entries[0].num("number") != 17 || entries[0].num("position") != 1 || entries[1].num("eta_seconds") != 600 {
		t.Fatalf("queue = %v", entries)
	}
	if code := h.status("POST", fmt.Sprintf("/api/projects/%d/forge/queue/remove", pid), obj{"base": "main", "id": "MQE_bogus"}); code != 404 {
		t.Fatalf("remove unknown entry: %d", code)
	}
	h.post(fmt.Sprintf("/api/projects/%d/forge/queue/remove", pid), obj{"base": "main", "id": entries[0].str("id")}, 200)
	left := h.get(fmt.Sprintf("/api/projects/%d/forge/queue", pid)).list("entries")
	if len(left) != 1 || left[0].num("number") != 15 || left[0].num("position") != 1 {
		t.Fatalf("after removal = %v", left)
	}
}

// fakeJiraCloud serves one issue whose description holds a table, which the
// editor cannot keep.
func fakeJiraCloud(t *testing.T) (*httptest.Server, *[]map[string]any) {
	var mu sync.Mutex
	puts := &[]map[string]any{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == "PUT":
			var b map[string]any
			raw, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(raw, &b)
			mu.Lock()
			*puts = append(*puts, b)
			mu.Unlock()
			w.WriteHeader(204)
		case strings.HasSuffix(r.URL.Path, "/transitions"):
			io.WriteString(w, `{"transitions":[]}`)
		case strings.Contains(r.URL.Path, "/issue/OPS-3"):
			io.WriteString(w, `{"id":"1","key":"OPS-3","fields":{"summary":"Tabled","status":{"name":"To Do","statusCategory":{"key":"new"}},
			  "description":{"type":"doc","version":1,"content":[{"type":"paragraph","content":[{"type":"text","text":"Intro"}]},{"type":"table","content":[]}]}}}`)
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, puts
}

func TestJiraDescriptionEditWarnsBeforeDroppingFormatting(t *testing.T) {
	h := newHarness(t)
	pid := h.seededProjectID()
	srv, puts := fakeJiraCloud(t)
	c := h.post(fmt.Sprintf("/api/projects/%d/trackers", pid), obj{"kind": "jira",
		"config": obj{"base_url": srv.URL, "flavor": "cloud", "email": "me@example.com"}, "secrets": obj{"token": "t"}}, 201)
	d := h.get(fmt.Sprintf("/api/trackers/%d/issues/OPS-3", c.id()))
	if d["body_lossy"] != true || d["editable"] != true || d.str("body") != "Intro" {
		t.Fatalf("issue = %v", d)
	}
	path := fmt.Sprintf("/api/trackers/%d/issues/OPS-3/description", c.id())
	if code := h.status("POST", path, obj{"body": "New **text**"}); code != 409 || len(*puts) != 0 {
		t.Fatalf("lossy save without confirmation: %d, %d writes", code, len(*puts))
	}
	h.post(path, obj{"body": "New **text**", "confirm_lossy": true}, 200)
	desc, _ := json.Marshal((*puts)[0]["fields"].(map[string]any)["description"])
	if !strings.Contains(string(desc), `"type":"strong"`) {
		t.Fatalf("saved description = %s", desc)
	}
}

func TestGiteaConnectionServesTheHub(t *testing.T) {
	h := newHarness(t)
	pid := h.seededProjectID()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "token gt" {
			w.WriteHeader(401)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/repos/team/app/pulls":
			io.WriteString(w, `[{"number":2,"title":"Gitea PR","html_url":"u","state":"open","mergeable":true,"user":{"login":"a"},"head":{"ref":"x"},"base":{"ref":"main"}}]`)
		case "/api/v1/repos/team/app/issues":
			io.WriteString(w, `[{"number":1,"title":"Gitea issue","html_url":"u","state":"open","user":{"login":"a"}}]`)
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	// no token yet: the hub says what to add instead of failing obscurely
	h.post(fmt.Sprintf("/api/projects/%d/trackers", pid), obj{"kind": "gitea", "config": obj{"repo": "team/app", "host": "gitea.example", "base_url": srv.URL + "/api/v1"}}, 201)
	work := h.get(fmt.Sprintf("/api/projects/%d/work", pid))
	if src := work.list("sources")[0]; src["ok"] == true || !strings.Contains(src.str("error"), "access token") {
		t.Fatalf("without a token = %v", src)
	}
	conn := h.get(fmt.Sprintf("/api/projects/%d/trackers", pid)).list("connections")[0]
	h.patch(fmt.Sprintf("/api/trackers/%d", conn.id()), obj{"secrets": obj{"token": "gt"}}, 200)
	work = h.get(fmt.Sprintf("/api/projects/%d/work", pid))
	if items := work.list("items"); len(items) != 2 || items[0].str("source") != "gitea" {
		t.Fatalf("gitea items = %v (%v)", items, work["sources"])
	}
	if f := h.get(fmt.Sprintf("/api/projects/%d/trackers", pid)).sub("forge"); f.str("kind") != "gitea" || f.str("source") != "connection" {
		t.Fatalf("forge = %v", f)
	}
	if q := h.get(fmt.Sprintf("/api/projects/%d/forge/queue", pid)); q["supported"] != false {
		t.Fatalf("gitea queue = %v", q)
	}
}
