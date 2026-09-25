package store

import (
	"errors"
	"path/filepath"
	"testing"
)

func newTriggersTestDB(t *testing.T) *DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "triggers.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	target, err := db.InsertTarget(&Target{Name: "t1", Kind: "mock"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.InsertProject(&Project{Name: "p1", TargetID: target.ID, RepoPath: "/mock/p1"}); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestTriggerSourceCRUD(t *testing.T) {
	db := newTriggersTestDB(t)
	proj, err := db.Projects()
	if err != nil || len(proj) != 1 {
		t.Fatalf("seed project: %v %v", proj, err)
	}
	pid := proj[0].ID

	src, err := db.InsertTriggerSource(&TriggerSource{
		ProjectID: pid, Kind: "github", Name: "gh", Enabled: true,
		ConfigJSON: `{"repo":"a/b"}`, SecretsJSON: "{}", IntervalS: 300,
	})
	if err != nil {
		t.Fatal(err)
	}
	if src.ID == 0 || src.Status != "unconfigured" {
		t.Fatalf("unexpected source: %+v", src)
	}

	fetched, err := db.TriggerSource(src.ID)
	if err != nil || fetched.ConfigJSON != `{"repo":"a/b"}` {
		t.Fatalf("TriggerSource: %+v %v", fetched, err)
	}

	list, err := db.TriggerSources(pid)
	if err != nil || len(list) != 1 {
		t.Fatalf("TriggerSources: %v %v", list, err)
	}

	all, err := db.AllTriggerSources()
	if err != nil || len(all) != 1 {
		t.Fatalf("AllTriggerSources: %v %v", all, err)
	}

	if err := db.RecordPoll(src.ID, `{"since":"x"}`, "ok", ""); err != nil {
		t.Fatal(err)
	}
	fresh, err := db.TriggerSource(src.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Status != "ok" || fresh.CursorJSON != `{"since":"x"}` || fresh.LastPollAt == nil {
		t.Fatalf("RecordPoll did not persist: %+v", fresh)
	}

	if err := db.DeleteTriggerSource(src.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.TriggerSource(src.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound after delete, got %v", err)
	}
}

func TestTriggerEventDedup(t *testing.T) {
	db := newTriggersTestDB(t)
	proj, _ := db.Projects()
	pid := proj[0].ID
	src, err := db.InsertTriggerSource(&TriggerSource{ProjectID: pid, Kind: "github", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}

	first, err := db.InsertTriggerEvent(&TriggerEvent{
		SourceID: src.ID, ProjectID: pid, ExternalID: "issue:1", Kind: "issue",
		Author: "octocat", Action: "task_created",
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == 0 {
		t.Fatalf("expected a saved row, got %+v", first)
	}

	_, err = db.InsertTriggerEvent(&TriggerEvent{
		SourceID: src.ID, ProjectID: pid, ExternalID: "issue:1", Kind: "issue",
		Author: "octocat", Action: "task_created",
	})
	if !errors.Is(err, ErrDuplicateEvent) {
		t.Fatalf("expected ErrDuplicateEvent for a repeat external_id, got %v", err)
	}

	// A different source may see the same external id without colliding —
	// dedup is scoped per source, not global.
	src2, err := db.InsertTriggerSource(&TriggerSource{ProjectID: pid, Kind: "github", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.InsertTriggerEvent(&TriggerEvent{
		SourceID: src2.ID, ProjectID: pid, ExternalID: "issue:1", Kind: "issue", Action: "task_created",
	}); err != nil {
		t.Fatalf("dedup must be scoped per source: %v", err)
	}

	recent, err := db.RecentTriggerEvents(pid, 10)
	if err != nil || len(recent) != 2 {
		t.Fatalf("RecentTriggerEvents: %v %v", recent, err)
	}
}

func TestEventsInWindowAndPostback(t *testing.T) {
	db := newTriggersTestDB(t)
	proj, _ := db.Projects()
	pid := proj[0].ID
	src, err := db.InsertTriggerSource(&TriggerSource{ProjectID: pid, Kind: "linear", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	task, err := db.InsertTask(&Task{ProjectID: pid, Title: "t", Status: "review"})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := db.InsertTriggerEvent(&TriggerEvent{
		SourceID: src.ID, ProjectID: pid, ExternalID: "linear:1", Action: "task_created", TaskID: &task.ID,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.InsertTriggerEvent(&TriggerEvent{
		SourceID: src.ID, ProjectID: pid, ExternalID: "linear:2", Action: "skipped", Reason: "rate limit reached for this source",
	}); err != nil {
		t.Fatal(err)
	}

	n, err := db.EventsInWindow(src.ID, 0)
	if err != nil || n != 1 {
		t.Fatalf("EventsInWindow should only count task_created rows, got %d (%v)", n, err)
	}

	pending, err := db.PendingPostbackEvents()
	if err != nil || len(pending) != 1 || pending[0].ExternalID != "linear:1" {
		t.Fatalf("PendingPostbackEvents: %+v %v", pending, err)
	}

	if err := db.MarkPostback(pending[0].ID, "sent", ""); err != nil {
		t.Fatal(err)
	}
	pending2, err := db.PendingPostbackEvents()
	if err != nil || len(pending2) != 0 {
		t.Fatalf("expected no pending postbacks after marking sent, got %+v", pending2)
	}
}
