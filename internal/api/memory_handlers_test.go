package api

// Memory you can see (docs/memory-visibility.md): the operator's half. These
// call the handlers directly, so they run without a socket, a runner or a
// scheduler. What they pin down is the contract that matters: a delivery is
// visible, and correcting the store takes a person.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/auth"
	"github.com/JeremiahM37/lectern/v2/internal/memory"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// fakeMemory is a provider that also reviews. It records what it was asked, so
// a test can prove the handler forwarded the operator's verdict — and, when the
// answer is no, that nothing was sent at all.
type fakeMemory struct {
	feedback  []string
	challenge []string
	err       error
}

func (f *fakeMemory) Name() string                   { return "fake" }
func (f *fakeMemory) Available(context.Context) bool { return true }
func (f *fakeMemory) Recall(context.Context, string, int) ([]memory.Fact, error) {
	return nil, nil
}
func (f *fakeMemory) Remember(context.Context, memory.Entry) error { return nil }

func (f *fakeMemory) Feedback(_ context.Context, itemID string, helpful bool, note string) error {
	if f.err != nil {
		return f.err
	}
	f.feedback = append(f.feedback, fmt.Sprintf("%s|%t|%s", itemID, helpful, note))
	return nil
}

func (f *fakeMemory) Challenge(_ context.Context, itemID, reason string) error {
	if f.err != nil {
		return f.err
	}
	f.challenge = append(f.challenge, itemID+"|"+reason)
	return nil
}

// memTestDB opens a throwaway database with the schema applied.
func memTestDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func memTestServer(t *testing.T, provider memory.Provider) *Server {
	t.Helper()
	return &Server{DB: memTestDB(t), Memory: provider, Auth: &auth.Resolver{Mode: auth.ModeTailscale}}
}

// memRequest builds a request the way the router would: the {id} path value
// set, a JSON body, and, when given, a principal already resolved.
func memRequest(method, path, itemID, body string, principal auth.Principal) *http.Request {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.SetPathValue("id", itemID)
	if principal.Kind != "" {
		r = r.WithContext(auth.WithPrincipal(r.Context(), principal))
	}
	return r
}

func human() auth.Principal {
	return auth.Principal{Kind: auth.KindTailscale, Login: "owner@example.invalid", Human: true}
}

func TestMemoryFeedbackNeedsAPerson(t *testing.T) {
	fake := &fakeMemory{}
	s := memTestServer(t, fake)
	r := memRequest("POST", "/api/memory/items/kestrel-1/feedback", "kestrel-1", `{"helpful":false}`,
		auth.Principal{Kind: auth.KindLocal})
	w := httptest.NewRecorder()
	s.memoryItemFeedback(w, r)
	if w.Code != 403 {
		t.Fatalf("an agent adjusted its own memory: %d %s", w.Code, w.Body.String())
	}
	if len(fake.feedback) != 0 {
		t.Fatalf("a refused request still reached the store: %v", fake.feedback)
	}
}

func TestMemoryFeedbackForwardsTheVerdict(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		helpful    bool
	}{
		{"helpful", `{"helpful":true,"note":"this is what I needed"}`, true},
		{"not relevant", `{"helpful":false,"note":"wrong project"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeMemory{}
			s := memTestServer(t, fake)
			r := memRequest("POST", "/api/memory/items/kestrel-1/feedback", "kestrel-1", tc.body, human())
			w := httptest.NewRecorder()
			s.memoryItemFeedback(w, r)
			if w.Code != 200 {
				t.Fatalf("feedback refused: %d %s", w.Code, w.Body.String())
			}
			want := fmt.Sprintf("kestrel-1|%t|%s", tc.helpful, noteOf(tc.body))
			if len(fake.feedback) != 1 || fake.feedback[0] != want {
				t.Fatalf("store saw %v, want %q", fake.feedback, want)
			}
			var out map[string]any
			_ = json.Unmarshal(w.Body.Bytes(), &out)
			if out["status"] != "recorded" || out["helpful"] != tc.helpful {
				t.Errorf("unexpected response: %v", out)
			}
		})
	}
}

func noteOf(body string) string {
	var in struct {
		Note string `json:"note"`
	}
	_ = json.Unmarshal([]byte(body), &in)
	return in.Note
}

// "helpful" absent is not "not relevant": the two answers mean different things
// and a missing one is a client bug, not a verdict.
func TestMemoryFeedbackRequiresAnAnswer(t *testing.T) {
	fake := &fakeMemory{}
	s := memTestServer(t, fake)
	r := memRequest("POST", "/api/memory/items/kestrel-1/feedback", "kestrel-1", `{"note":"hm"}`, human())
	w := httptest.NewRecorder()
	s.memoryItemFeedback(w, r)
	if w.Code != 422 {
		t.Fatalf("a missing helpful was accepted: %d %s", w.Code, w.Body.String())
	}
	if len(fake.feedback) != 0 {
		t.Fatalf("an unanswerable request reached the store: %v", fake.feedback)
	}
}

func TestMemoryChallengeNeedsAPersonAndAReason(t *testing.T) {
	fake := &fakeMemory{}
	s := memTestServer(t, fake)
	r := memRequest("POST", "/api/memory/items/kestrel-1/challenge", "kestrel-1", `{"reason":"we moved off kestrel"}`,
		auth.Principal{Kind: auth.KindLocal})
	w := httptest.NewRecorder()
	s.memoryItemChallenge(w, r)
	if w.Code != 403 || len(fake.challenge) != 0 {
		t.Fatalf("an agent challenged its own memory: %d %v", w.Code, fake.challenge)
	}

	// A human with no reason gets a schema error rather than an unreviewable
	// challenge in the store.
	r = memRequest("POST", "/api/memory/items/kestrel-1/challenge", "kestrel-1", `{"reason":"   "}`, human())
	w = httptest.NewRecorder()
	s.memoryItemChallenge(w, r)
	if w.Code != 422 || len(fake.challenge) != 0 {
		t.Fatalf("an empty reason was forwarded: %d %v", w.Code, fake.challenge)
	}
}

func TestMemoryChallengeForwardsTheReason(t *testing.T) {
	fake := &fakeMemory{}
	s := memTestServer(t, fake)
	r := memRequest("POST", "/api/memory/items/kestrel-1/challenge", "kestrel-1", `{"reason":"we moved off kestrel"}`, human())
	w := httptest.NewRecorder()
	s.memoryItemChallenge(w, r)
	if w.Code != 200 {
		t.Fatalf("challenge refused: %d %s", w.Code, w.Body.String())
	}
	if len(fake.challenge) != 1 || fake.challenge[0] != "kestrel-1|we moved off kestrel" {
		t.Fatalf("store saw %v", fake.challenge)
	}
}

// A provider that cannot be corrected says so, rather than pretending the
// verdict landed.
func TestMemoryReviewSaysWhenTheProviderCannotTakeIt(t *testing.T) {
	s := memTestServer(t, memory.None{})
	for _, tc := range []struct {
		name string
		body string
		call http.HandlerFunc
	}{
		{"feedback", `{"helpful":true}`, s.memoryItemFeedback},
		{"challenge", `{"reason":"wrong"}`, s.memoryItemChallenge},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := memRequest("POST", "/api/memory/items/kestrel-1/"+tc.name, "kestrel-1", tc.body, human())
			w := httptest.NewRecorder()
			tc.call(w, r)
			if w.Code != 501 {
				t.Fatalf("a provider with no review surface answered %d", w.Code)
			}
		})
	}
}

// A store that refuses is surfaced with its own words, because "502" alone
// tells the operator nothing about what to fix.
func TestMemoryReviewSurfacesAStoreRefusal(t *testing.T) {
	fake := &fakeMemory{err: errors.New("grimoire feedback: item not found")}
	s := memTestServer(t, fake)
	r := memRequest("POST", "/api/memory/items/kestrel-1/feedback", "kestrel-1", `{"helpful":true}`, human())
	w := httptest.NewRecorder()
	s.memoryItemFeedback(w, r)
	if w.Code != 502 {
		t.Fatalf("store refusal not surfaced: %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "item not found") {
		t.Errorf("the store's own message was dropped: %s", w.Body.String())
	}
}

func TestTaskMemoryReportsDeliveriesNewestFirst(t *testing.T) {
	db := memTestDB(t)
	tgt, err := db.InsertTarget(&store.Target{Name: "t", Kind: "local"})
	if err != nil {
		t.Fatal(err)
	}
	project, err := db.InsertProject(&store.Project{Name: "kestrel", TargetID: tgt.ID, RepoPath: "/w"})
	if err != nil {
		t.Fatal(err)
	}
	task, err := db.InsertTask(&store.Task{ProjectID: project.ID, Title: "one", Status: "backlog"})
	if err != nil {
		t.Fatal(err)
	}
	attempt, taskID := int64(9), task.ID
	if _, err := db.InsertMemoryDelivery(store.MemoryDelivery{TaskID: &taskID, AttemptID: &attempt,
		Mode: "managed", Bytes: 900,
		ItemsJSON: `[{"id":"a","source":"memory/kestrel.md","title":"Kestrel","snippet":"deploys"}]`}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.InsertMemoryDelivery(store.MemoryDelivery{TaskID: &taskID, AttemptID: &attempt,
		Mode: "scoped", Bytes: 10, ItemsJSON: `[{"id":"b","title":"newer"}]`}); err != nil {
		t.Fatal(err)
	}
	s := &Server{DB: db, Auth: &auth.Resolver{Mode: auth.ModeTailscale}}
	r := httptest.NewRequest("GET", fmt.Sprintf("/api/tasks/%d/memory", task.ID), nil)
	r.SetPathValue("id", fmt.Sprint(task.ID))
	w := httptest.NewRecorder()
	s.taskMemory(w, r)
	if w.Code != 200 {
		t.Fatalf("task memory: %d %s", w.Code, w.Body.String())
	}
	var out struct {
		TaskID     int64             `json:"task_id"`
		Deliveries []memory.Delivery `json:"deliveries"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.TaskID != task.ID || len(out.Deliveries) != 2 {
		t.Fatalf("unexpected listing: %+v", out)
	}
	if out.Deliveries[0].Items[0].ID != "b" {
		t.Errorf("deliveries must read newest first: %+v", out.Deliveries)
	}
	if out.Deliveries[1].Items[0].Source != "memory/kestrel.md" {
		t.Errorf("items were not parsed for the client: %+v", out.Deliveries[1])
	}

	// An unknown task is a 404; a task nobody handed anything to is an empty
	// list, not an error.
	other, err := db.InsertTask(&store.Task{ProjectID: project.ID, Title: "quiet", Status: "backlog"})
	if err != nil {
		t.Fatal(err)
	}
	quiet := httptest.NewRequest("GET", fmt.Sprintf("/api/tasks/%d/memory", other.ID), nil)
	quiet.SetPathValue("id", fmt.Sprint(other.ID))
	qw := httptest.NewRecorder()
	s.taskMemory(qw, quiet)
	if qw.Code != 200 || !strings.Contains(qw.Body.String(), `"deliveries":[]`) {
		t.Errorf("an empty task should say so: %d %s", qw.Code, qw.Body.String())
	}

	missing := httptest.NewRequest("GET", "/api/tasks/99999/memory", nil)
	missing.SetPathValue("id", "99999")
	mw := httptest.NewRecorder()
	s.taskMemory(mw, missing)
	if mw.Code != 404 {
		t.Errorf("an unknown task should 404, got %d", mw.Code)
	}
}

var _ memory.Reviewer = (*fakeMemory)(nil)
