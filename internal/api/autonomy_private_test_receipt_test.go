package api

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestPrivateTestMissingRequestRequiresActualRevocation(t *testing.T) {
	a, v, j, now := privateFixture(t)
	key := strings.Repeat("c", 64)
	privateTestReceipt(t, a, v, j, now, key)
	l := v.TestLeases[key]
	body := map[string]any{"state": "cancelled", "reason": "cancelled_missing_request", "evidence_scope": "revoked_missing_request", "integration_id": v.ID, "owner_job": j.ID, "check_id": key, "request_sha256": key, "generation": 2, "revoked_through_generation": 1, "executed": false, "charged_ms": 0, "receipt_sha256": strings.Repeat("d", 64)}
	raw, _ := json.Marshal(body)
	r, err := autoDecodeBoundPrivateTestReceipt(raw, v.ID, l, 2)
	if err != nil {
		t.Fatal(err)
	}
	if r.Generation != 1 || r.OwnerTask != l.OwnerTask || r.CandidateSHA != l.CandidateSHA || !bytes.Equal(r.ReceiptEvidence, raw) {
		t.Fatal("lost reserved authority or original evidence")
	}
	v.Status = "rejected"
	if err := autoRecordPrivateTest(a, r); err != nil {
		t.Fatal(err)
	}
	if !l.StopConfirmed || l.Receipt.ChargedMilliseconds != 0 || l.Receipt.Executed || v.Status != "rejected" {
		t.Fatal("cancellation accounting changed execution or admission")
	}
	for _, mutate := range []func(map[string]any){
		func(m map[string]any) { m["revoked_through_generation"] = 0 },
		func(m map[string]any) { m["executed"] = true; m["truncated"] = false },
		func(m map[string]any) { m["charged_ms"] = 1 },
		func(m map[string]any) { m["state"] = "exited" },
		func(m map[string]any) { m["execution_generation"] = 1 },
		func(m map[string]any) { m["owner_task"] = 999 },
		func(m map[string]any) { m["receipt_sha256"] = "" },
	} {
		bad := map[string]any{}
		for k, val := range body {
			bad[k] = val
		}
		mutate(bad)
		encoded, _ := json.Marshal(bad)
		if _, err := autoDecodeBoundPrivateTestReceipt(encoded, v.ID, l, 2); err == nil {
			t.Fatalf("accepted unsupported cancellation: %s", encoded)
		}
	}
	// Later observation retains the first diagnostic rather than changing the
	// immutable accounting record solely because its envelope generation moved.
	body["generation"] = 3
	later, _ := json.Marshal(body)
	replay, err := autoDecodeBoundPrivateTestReceipt(later, v.ID, l, 3)
	if err != nil {
		t.Fatal(err)
	}
	if err = autoRecordPrivateTest(a, replay); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(l.Receipt.ReceiptEvidence, raw) || len(v.Tests) != 1 {
		t.Fatal("re-observation replaced historical evidence")
	}
	newLease := *l
	newLease.Generation = 2
	if _, err = autoDecodeBoundPrivateTestReceipt(later, v.ID, &newLease, 3); err == nil {
		t.Fatal("old revocation released newer reservation")
	}
}
