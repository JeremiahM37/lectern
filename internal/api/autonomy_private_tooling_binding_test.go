package api

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/autonomy"
)

func TestPrivateToolingConsumerUsesCitedSuccessfulEnvironment(t *testing.T) {
	for _, key := range []string{strings.Repeat("d", 64), ""} {
		a, v, j, now := privateFixture(t)
		receipt := privateTestReceipt(t, a, v, j, now, strings.Repeat("c", 64))
		receipt.PythonTestKey = key
		v.Tests = []autoPrivateTestReceipt{receipt}
		v.Review = &autoPrivateReview{TestReceiptSHAs: []string{receipt.ReceiptSHA}}
		v.Publication = &autoPrivatePublication{ID: v.ID}
		a.State = &autonomy.State{Items: []autonomy.Proposal{{SourceIntegrationID: v.ID}}}
		consumer := &autoJob{Role: "builder"}
		if err := autoInheritPrivateTooling(a, consumer); err != nil {
			t.Fatal("valid tested environment unavailable", err)
		}
		if consumer.PythonExpectedTestKey != key {
			t.Fatal("consumer substituted test tooling")
		}
		for _, change := range []func(*autoPrivateTestReceipt){
			func(r *autoPrivateTestReceipt) { r.Executed = false },
			func(r *autoPrivateTestReceipt) { r.Truncated = true },
			func(r *autoPrivateTestReceipt) { code := 1; r.ExitCode = &code },
			func(r *autoPrivateTestReceipt) { r.ReceiptSHA = strings.Repeat("e", 64) },
		} {
			bad := receipt
			change(&bad)
			v.Tests = []autoPrivateTestReceipt{bad}
			if err := autoInheritPrivateTooling(a, &autoJob{Role: "builder"}); err == nil {
				t.Fatal("uncited or unsuccessful test selected tooling")
			}
		}
	}
}

func TestPrivateToolingMetadataRequiresPinnedIdentity(t *testing.T) {
	a, _, j, _ := privateFixture(t)
	j.PythonExpectedTestKey = strings.Repeat("d", 64)
	valid := autoPrivateToolingReceipt{Capability: "python_test_runtime", Key: j.PythonExpectedTestKey, State: "verified", Scope: "metadata only; launch validates content"}
	for _, change := range []func(*autoPrivateToolingReceipt){
		func(r *autoPrivateToolingReceipt) { r.Key = strings.Repeat("e", 64) },
		func(r *autoPrivateToolingReceipt) { r.Capability = "other" },
		func(r *autoPrivateToolingReceipt) { r.State = "running" },
		func(r *autoPrivateToolingReceipt) { r.Scope = "" },
		func(r *autoPrivateToolingReceipt) { r.State = "unavailable"; r.Reason = "" },
	} {
		bad := valid
		change(&bad)
		raw, _ := json.Marshal(bad)
		if _, err := autoApplyPrivateTooling(a, j, raw); err == nil {
			t.Fatal("invalid tooling metadata admitted")
		}
		if j.PythonTestRecovery != nil {
			t.Fatal("invalid metadata replaced runtime authority")
		}
	}
	raw, _ := json.Marshal(valid)
	if ready, err := autoApplyPrivateTooling(a, j, raw); err != nil || !ready {
		t.Fatal(err)
	}
	valid.State = "unavailable"
	valid.Reason = "retained key missing"
	valid.Diagnostic = "missing"
	raw, _ = json.Marshal(valid)
	if ready, err := autoApplyPrivateTooling(a, j, raw); err != nil || ready {
		t.Fatal("missing selected tooling treated as available", err)
	}
	if j.PythonExpectedTestKey != valid.Key || len(a.Requirements) != 1 {
		t.Fatal("lost exact prerequisite identity")
	}
}
