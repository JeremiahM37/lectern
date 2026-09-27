package api

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// Opt-in compatibility check of actual isolated-runner evidence. All ledger
// state remains in memory; this test neither launches nor alters live work.
func TestPrivateTestActualRunnerReceiptCompatibility(t *testing.T) {
	proofPath := os.Getenv("LECTERN_PRIVATE_RECEIPT_PROOF")
	if proofPath == "" {
		t.Skip("requires actual disposable runner proof")
	}
	raw, err := os.ReadFile(proofPath)
	if err != nil {
		t.Fatal(err)
	}
	var proof struct {
		Fixture string `json:"fixture"`
		Results []struct {
			Label   string          `json:"label"`
			Receipt json.RawMessage `json:"receipt"`
		} `json:"results"`
	}
	if err = json.Unmarshal(raw, &proof); err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, row := range proof.Results {
		var observed autoPrivateTestReceipt
		if err = json.Unmarshal(row.Receipt, &observed); err != nil {
			t.Fatal(err)
		}
		if observed.CheckID == "" {
			continue
		}
		var shape struct {
			Scope     string `json:"evidence_scope"`
			Execution int    `json:"execution_generation"`
		}
		if err = json.Unmarshal(row.Receipt, &shape); err != nil {
			t.Fatal(err)
		}
		if shape.Scope != "" {
			continue
		} // Separate missing-request proof has no request bytes.
		t.Run(row.Label, func(t *testing.T) {
			request, err := os.ReadFile(filepath.Join(proof.Fixture, "jobs", observed.OwnerJob, "integration-requests", observed.IntegrationID+".check-"+observed.CheckID+".json"))
			if err != nil {
				t.Fatal(err)
			}
			if autoSHA(request) != observed.RequestSHA {
				t.Fatal("actual request bytes mismatch")
			}
			lease := &autoPrivateTestLease{ID: observed.CheckID, RequestSHA: observed.RequestSHA, OwnerJob: observed.OwnerJob, OwnerTask: observed.OwnerTask, CandidateSHA: observed.CandidateSHA, Generation: shape.Execution, RequestRaw: request}
			r, err := autoDecodeBoundPrivateTestReceipt(row.Receipt, observed.IntegrationID, lease, observed.Generation)
			if err != nil {
				t.Fatal(err)
			}
			v := &autoPrivateIntegrationAttempt{ID: r.IntegrationID, Status: "reviewing", ReviewerTaskID: r.OwnerTask, Candidate: &autoPrivateCandidate{TreeSHA: r.CandidateSHA}}
			if err = autoRecordPrivateTestReceipt(v, r); err != nil {
				t.Fatal(err)
			}
			if len(v.Tests) != 1 || v.Tests[0].ReceiptSHA != observed.ReceiptSHA {
				t.Fatal("actual receipt identity lost")
			}
		})
		count++
	}
	if count < 3 {
		t.Fatalf("insufficient actual executed/failed/cancelled evidence: %d", count)
	}
}
