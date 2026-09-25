package triggers

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/store"
)

func newTestManager(t *testing.T) (*Manager, *store.Project) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "triggers.db"))
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
	return m, project
}

func TestAuthorAllowed(t *testing.T) {
	cases := []struct {
		author string
		list   []string
		want   bool
	}{
		{"octocat", []string{"OctoCat", "someone"}, true}, // case-insensitive
		{"octocat", []string{}, false},                    // empty allowlist denies everyone
		{"", []string{"octocat"}, false},                  // no author never matches
		{"mallory", []string{"octocat"}, false},
	}
	for _, c := range cases {
		if got := authorAllowed(c.author, c.list); got != c.want {
			t.Errorf("authorAllowed(%q, %v) = %v, want %v", c.author, c.list, got, c.want)
		}
	}
}

func TestIntakeRejectsUnlistedAuthor(t *testing.T) {
	m, project := newTestManager(t)
	called := false
	m.CreateTask = func(*store.Project, NewTaskSpec) (*store.Task, error) {
		called = true
		return &store.Task{ID: 1}, nil
	}
	src, err := m.DB.InsertTriggerSource(&store.TriggerSource{ProjectID: project.ID, Kind: "github", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	ev := m.intake(project, src, []string{"trusted-user"}, "", "", "", 10, candidate{
		ExternalID: "issue:1", Kind: "issue", Author: "a-stranger", Summary: "x",
	})
	if called {
		t.Fatal("CreateTask must not run for an author outside the allowlist")
	}
	if ev == nil || ev.Action != "skipped" {
		t.Fatalf("expected a skipped event, got %+v", ev)
	}
}

func TestIntakeCreatesTaskForAllowedAuthor(t *testing.T) {
	m, project := newTestManager(t)
	var gotSpec NewTaskSpec
	m.CreateTask = func(_ *store.Project, spec NewTaskSpec) (*store.Task, error) {
		gotSpec = spec
		return &store.Task{ID: 42}, nil
	}
	src, err := m.DB.InsertTriggerSource(&store.TriggerSource{ProjectID: project.ID, Kind: "github", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	ev := m.intake(project, src, []string{"trusted-user"}, "claude", "sonnet", "main", 10, candidate{
		ExternalID: "issue:1", Kind: "issue", Author: "trusted-user", Summary: "fix the thing",
		Title: "fix the thing", Prompt: "please fix it",
	})
	if ev == nil || ev.Action != "task_created" || ev.TaskID == nil || *ev.TaskID != 42 {
		t.Fatalf("expected a task_created event pointing at task 42, got %+v", ev)
	}
	if gotSpec.Agent != "claude" || gotSpec.BaseBranch != "main" || gotSpec.Prompt != "please fix it" {
		t.Fatalf("CreateTask got the wrong spec: %+v", gotSpec)
	}
}

func TestIntakeNeverActsTwiceOnSameEvent(t *testing.T) {
	m, project := newTestManager(t)
	calls := 0
	m.CreateTask = func(*store.Project, NewTaskSpec) (*store.Task, error) {
		calls++
		return &store.Task{ID: int64(calls)}, nil
	}
	src, err := m.DB.InsertTriggerSource(&store.TriggerSource{ProjectID: project.ID, Kind: "github", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	c := candidate{ExternalID: "issue:9", Kind: "issue", Author: "trusted-user", Summary: "x"}
	first := m.intake(project, src, []string{"trusted-user"}, "", "", "", 10, c)
	second := m.intake(project, src, []string{"trusted-user"}, "", "", "", 10, c)
	if first == nil {
		t.Fatal("first delivery should be recorded")
	}
	if second != nil {
		t.Fatalf("redelivery of the same external id must be a no-op, got %+v", second)
	}
	if calls != 1 {
		t.Fatalf("CreateTask ran %d times for one event, want 1", calls)
	}
}

func TestIntakeRateLimitsPerSource(t *testing.T) {
	m, project := newTestManager(t)
	calls := 0
	m.CreateTask = func(*store.Project, NewTaskSpec) (*store.Task, error) {
		calls++
		return &store.Task{ID: int64(calls)}, nil
	}
	src, err := m.DB.InsertTriggerSource(&store.TriggerSource{ProjectID: project.ID, Kind: "github", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		ev := m.intake(project, src, []string{"trusted-user"}, "", "", "", 2, candidate{
			ExternalID: "issue:" + string(rune('a'+i)), Kind: "issue", Author: "trusted-user", Summary: "x",
		})
		if i < 2 {
			if ev == nil || ev.Action != "task_created" {
				t.Fatalf("event %d: expected task_created, got %+v", i, ev)
			}
		} else {
			if ev == nil || ev.Action != "skipped" {
				t.Fatalf("event %d: expected the rate limit to kick in, got %+v", i, ev)
			}
		}
	}
	if calls != 2 {
		t.Fatalf("expected exactly 2 tasks created within the 2/hour limit, got %d", calls)
	}
}

func TestPollIfDueRespectsInterval(t *testing.T) {
	m, project := newTestManager(t)
	src, err := m.DB.InsertTriggerSource(&store.TriggerSource{
		ProjectID: project.ID, Kind: "github", Enabled: true, IntervalS: 300,
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	m.now = func() time.Time { return now }
	polls := 0
	poll := func(_ context.Context, s *store.TriggerSource) error {
		polls++
		// Real pollers (pollGitHub/pollLinear) record their own poll on
		// success; pollIfDue only records on failure, so the stub must do
		// the same to exercise the "already polled recently" branch.
		return m.DB.RecordPoll(s.ID, s.CursorJSON, "ok", "")
	}

	m.pollIfDue(context.Background(), src, poll)
	if polls != 1 {
		t.Fatalf("a never-polled source should poll immediately, got %d polls", polls)
	}

	fresh, _ := m.DB.TriggerSource(src.ID)
	m.pollIfDue(context.Background(), fresh, poll)
	if polls != 1 {
		t.Fatalf("polling again before the interval elapsed should be a no-op, got %d polls", polls)
	}

	m.now = func() time.Time { return now.Add(301 * time.Second) }
	m.pollIfDue(context.Background(), fresh, poll)
	if polls != 2 {
		t.Fatalf("polling after the interval elapsed should poll again, got %d polls", polls)
	}
}
