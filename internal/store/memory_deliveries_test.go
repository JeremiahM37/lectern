package store

import (
	"testing"
)

// The delivery log is what makes "what memory was this agent given" answerable
// after the fact (docs/memory-visibility.md). These tests cover the store half:
// the row records what it was told, the two delivery paths stay separable, and
// a listing is newest first.
func TestMemoryDeliveryRecordsAndListsNewestFirst(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	session := int64(42)
	first, err := db.InsertMemoryDelivery(MemoryDelivery{
		SessionID: &session, Mode: "scoped", Bytes: 120,
		ItemsJSON: `[{"id":"n1","source":"memory/kestrel.md","title":"Kestrel","snippet":"deploys"}]`})
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == 0 || first.At == 0 {
		t.Fatalf("insert did not fill the row: %+v", first)
	}
	second, err := db.InsertMemoryDelivery(MemoryDelivery{
		SessionID: &session, Mode: "scoped", Bytes: 40, ItemsJSON: "[]"})
	if err != nil {
		t.Fatal(err)
	}
	// A second delivery in the same instant must still order by id, or the
	// section shows a delivery twice under a stale heading.
	if second.At < first.At {
		t.Fatalf("clock went backwards: %v then %v", first.At, second.At)
	}
	rows, err := db.MemoryDeliveriesForSession(session, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].ID != second.ID || rows[1].ID != first.ID {
		t.Fatalf("newest first expected, got %+v", rows)
	}
	if rows[1].ItemsJSON != `[{"id":"n1","source":"memory/kestrel.md","title":"Kestrel","snippet":"deploys"}]` {
		t.Errorf("items_json was not kept verbatim: %q", rows[1].ItemsJSON)
	}
	if rows[0].SessionID == nil || *rows[0].SessionID != session {
		t.Errorf("session id lost: %+v", rows[0])
	}
	if rows[0].TaskID != nil || rows[0].AttemptID != nil {
		t.Errorf("a session delivery must not invent a task: %+v", rows[0])
	}
	if rows[0].ItemsJSON != "[]" {
		t.Errorf("an item-less delivery must still be valid JSON: %q", rows[0].ItemsJSON)
	}
}

func TestMemoryDeliveryKeepsSessionAndTaskRowsApart(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	task, attempt := int64(7), int64(11)
	if _, err := db.InsertMemoryDelivery(MemoryDelivery{
		TaskID: &task, AttemptID: &attempt, Mode: "managed", Bytes: 800}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.InsertMemoryDelivery(MemoryDelivery{SessionID: ptr(int64(7))}); err != nil {
		t.Fatal(err)
	}
	sessions, err := db.MemoryDeliveriesForSession(7, 0)
	if err != nil || len(sessions) != 1 {
		t.Fatalf("session listing: %v %v", sessions, err)
	}
	tasks, err := db.MemoryDeliveriesForTask(7, 0)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("task listing: %v %v", tasks, err)
	}
	if tasks[0].TaskID == nil || *tasks[0].TaskID != task || tasks[0].AttemptID == nil || *tasks[0].AttemptID != attempt {
		t.Errorf("task delivery lost its attempt: %+v", tasks[0])
	}
	if other, _ := db.MemoryDeliveriesForTask(8, 0); len(other) != 0 {
		t.Errorf("a different task saw this delivery: %+v", other)
	}
	if limited, _ := db.MemoryDeliveriesForTask(7, 1); len(limited) != 1 {
		t.Errorf("limit not applied: %+v", limited)
	}
}

// A delivery is a record of something that happened. Deleting the run it
// describes must not erase it — otherwise "the agent was given a wrong memory"
// disappears along with the session that received it. The columns are
// deliberately not foreign keys, and this is the test that says so.
func TestMemoryDeliverySurvivesSessionDeletion(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	target, err := db.InsertTarget(&Target{Name: "t", Kind: "local"})
	if err != nil {
		t.Fatal(err)
	}
	sess, err := db.InsertSession(&Session{TargetID: target.ID, Name: "s",
		Agent: "claude", Workdir: "/w", TmuxSession: "lec-s1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.InsertMemoryDelivery(MemoryDelivery{SessionID: &sess.ID, Bytes: 10}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM sessions WHERE id=?`, sess.ID); err != nil {
		t.Fatal(err)
	}
	rows, err := db.MemoryDeliveriesForSession(sess.ID, 0)
	if err != nil || len(rows) != 1 {
		t.Fatalf("delivery lost with its session: %v %v", rows, err)
	}
}

func ptr[T any](v T) *T { return &v }
