package api

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/autonomy"
)

func serverObservationFixture(t *testing.T) (*autoJob, autoServerObservationRequest) {
	t.Helper()
	j := &autoJob{ID: "12345678-1234-1234-1234-123456789abc", TaskID: 17, Role: "planner", Status: "running"}
	r, err := autoNewServerObservation(j, "local", strings.Repeat("a", 64), time.Unix(600, 0))
	if err != nil {
		t.Fatal(err)
	}
	return j, r
}

func TestServerObservationDurableRetryAndConflict(t *testing.T) {
	j, r := serverObservationFixture(t)
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, j.ID), 0755); err != nil {
		t.Fatal(err)
	}
	first, err := autoWriteServerObservation(root, r)
	if err != nil {
		t.Fatal(err)
	}
	retry, err := autoNewServerObservation(j, r.TargetID, r.RegistrySHA, time.Unix(659, 0))
	if err != nil || retry != r {
		t.Fatal("same sampling interval changed request", err)
	}
	second, err := autoWriteServerObservation(root, retry)
	if err != nil || string(first) != string(second) {
		t.Fatal("retry did not recover exact request", err)
	}
	r.TargetID = "another"
	if _, err := autoWriteServerObservation(root, r); err == nil {
		t.Fatal("overwrote sealed request")
	}
	name, _ := autoServerObservationPath(root, retry)
	got, _ := os.ReadFile(name)
	if string(got) != string(first) {
		t.Fatal("conflict damaged original")
	}
	next, _ := autoNewServerObservation(j, retry.TargetID, retry.RegistrySHA, time.Unix(660, 0))
	if next.RequestID == retry.RequestID {
		t.Fatal("fresh interval cannot observe changed server")
	}
}

func TestServerObservationRejectsPathAndSymlinkEscape(t *testing.T) {
	j, r := serverObservationFixture(t)
	for _, target := range []string{"../other", "/etc", "local?command=stop", "local/service"} {
		if _, err := autoNewServerObservation(j, target, r.RegistrySHA, time.Now()); err == nil {
			t.Fatal("accepted target", target)
		}
	}
	for _, component := range []string{"job", "observations", "request"} {
		t.Run(component, func(t *testing.T) {
			root, elsewhere := t.TempDir(), t.TempDir()
			path := filepath.Join(root, j.ID)
			if component != "job" {
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
				path = filepath.Join(path, "server-observations")
			}
			if component == "request" {
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
				path = filepath.Join(path, r.RequestID)
			}
			if err := os.Symlink(elsewhere, path); err != nil {
				t.Fatal(err)
			}
			if _, err := autoWriteServerObservation(root, r); err == nil {
				t.Fatal("followed symlink")
			}
			entries, _ := os.ReadDir(elsewhere)
			if len(entries) != 0 {
				t.Fatal("wrote beyond authority")
			}
		})
	}
}

func TestServerObservationOwnerCannotRestartFromStaleAssignment(t *testing.T) {
	j, _ := serverObservationFixture(t)
	a := &autoRecord{Jobs: []*autoJob{j}, State: &autonomy.State{Assignments: []autonomy.Assignment{{TaskID: j.TaskID, Role: j.Role}}}}
	a.Config.Enabled = true
	if _, err := autoServerObservationOwner(a, j.ID, true); err != nil {
		t.Fatal(err)
	}
	a.State.Assignments[0].TaskID++
	if _, err := autoServerObservationOwner(a, j.ID, true); err == nil {
		t.Fatal("stale task launched")
	}
	a.State.Assignments[0].TaskID = j.TaskID
	a.Config.Enabled = false
	if _, err := autoServerObservationOwner(a, j.ID, true); err == nil {
		t.Fatal("OFF launched")
	}
	j.Status = "done"
	if _, err := autoServerObservationOwner(a, j.ID, false); err != nil {
		t.Fatal("OFF lost existing evidence", err)
	}
	if _, err := autoServerObservationOwner(a, "33333333-3333-4333-8333-333333333333", false); err == nil {
		t.Fatal("unknown owner read evidence")
	}
}
