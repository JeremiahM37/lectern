package api

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestPrivateTestReconcileSurvivesReviewerExitAndObservationFailure(t *testing.T) {
	s := autoTestServer(t)
	a, v, j, now := privateFixture(t)
	dir := t.TempDir()
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	t.Setenv("LECTERN_PRIVATE_RECEIPTS", dir)
	if err := os.WriteFile(filepath.Join(dir, "sudo"), []byte("#!/bin/sh\n[ \"$3\" = integration-test-status ] || exit 91\ncat \"$LECTERN_PRIVATE_RECEIPTS/$9.json\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 6; i++ {
		key := fmt.Sprintf("%064x", i)
		r := privateTestReceipt(t, a, v, j, now, key)
		if i <= 2 {
			continue
		} // Broken early observations must not starve later checks.
		raw, _ := json.Marshal(r)
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Fatal(err)
		}
		body["execution_generation"] = 1
		body["generation"] = 2
		privateTestAttachRuntime(t, v, key, body)
		raw, _ = json.Marshal(body)
		if err := os.WriteFile(filepath.Join(dir, key+".json"), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	j.Status = "failed"
	v.Status = "rejected"
	v.StopRequested = true
	v.Generation = 2
	if err := s.reconcileAutoPrivateTests(context.Background(), a); err == nil {
		t.Fatal("missing observations not surfaced")
	}
	if len(v.Tests) != 2 {
		t.Fatalf("first bounded sweep recovered %d receipts", len(v.Tests))
	}
	// Reload proves scheduling and exact execution authority survive restart.
	a, err := s.loadAuto()
	if err != nil {
		t.Fatal(err)
	}
	v = a.PrivateIntegration.Runs[v.RootID].Attempts[0]
	_ = s.reconcileAutoPrivateTests(context.Background(), a)
	if len(v.Tests) != 4 {
		t.Fatalf("early failures starved later receipts: %d", len(v.Tests))
	}
	if v.Status != "rejected" || !v.StopRequested {
		t.Fatal("accounting reopened rejected work")
	}
	for _, r := range v.Tests {
		if r.Generation != 1 {
			t.Fatal("observation rewrote execution authority")
		}
	}
	for i := 1; i <= 2; i++ {
		lease := v.TestLeases[fmt.Sprintf("%064x", i)]
		if lease.Receipt != nil {
			t.Fatal("failed observation released reservation")
		}
	}
}

func TestPrivateTestReconcilePreexecutionRejectionReleasesReservation(t *testing.T) {
	s := autoTestServer(t)
	a, v, j, now := privateFixture(t)
	key := fmt.Sprintf("%064x", 1)
	r := privateTestReceipt(t, a, v, j, now, key)
	r.State = "rejected"
	r.Reason = "candidate receipt mismatch before execution"
	r.Executed = false
	r.ChargedMilliseconds = 0
	r.ExitCode = nil
	r.RuntimeSHA = ""
	r.OutputSHA = ""
	raw, _ := json.Marshal(r)
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	body["execution_generation"] = 1
	raw, _ = json.Marshal(body)
	dir := t.TempDir()
	response := filepath.Join(dir, "response.json")
	if err := os.WriteFile(response, raw, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	t.Setenv("LECTERN_PRIVATE_RESPONSE", response)
	if err := os.WriteFile(filepath.Join(dir, "sudo"), []byte("#!/bin/sh\n[ \"$3\" = integration-test-status ] || exit 91\ncat \"$LECTERN_PRIVATE_RESPONSE\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := s.reconcileAutoPrivateTests(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	l := v.TestLeases[key]
	if l.Receipt == nil || l.Receipt.Executed || l.Receipt.ChargedMilliseconds != 0 || !l.StopConfirmed {
		t.Fatal("preexecution rejection stranded reservation")
	}
	if v.Status != "reviewing" {
		t.Fatal("diagnostic changed review disposition")
	}
}
