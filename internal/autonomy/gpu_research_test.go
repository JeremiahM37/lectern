package autonomy

import (
	"strings"
	"testing"
	"time"
)

func gpuFixture() GPUResearchRequest {
	return GPUResearchRequest{SchemaVersion: 1, OwnerJob: "11111111-1111-1111-1111-111111111111", OwnerTask: 7, Role: "builder", ExperimentRoot: strings.Repeat("a", 64), SourceJob: "22222222-2222-2222-2222-222222222222", SourceArchiveSHA: strings.Repeat("b", 64), AdmissionSHA: strings.Repeat("c", 64), AcceptanceSHA: strings.Repeat("d", 64), Target: GPUResearchTarget, RuntimeKey: strings.Repeat("e", 64), Profile: "gpu-screen600", Script: "print(42)", Argv: []string{}}
}
func gpuReceipt(run *GPUResearchLease, executed *bool, charge int64) GPUResearchReceipt {
	code := 0
	return GPUResearchReceipt{RunID: run.ID, RequestSHA: run.RequestSHA, State: "exited", Executed: executed, ChargedMS: charge, ElapsedMS: charge, ExitCode: &code, OutputSHA: strings.Repeat("f", 64), CleanupConfirmed: true, ReceiptSHA: strings.Repeat("9", 64)}
}
func TestGPUResearchQualificationQuotaAndSingleExecution(t *testing.T) {
	l := GPUResearchLedger{}
	r := gpuFixture()
	now := time.Now()
	q := strings.Repeat("8", 64)
	for _, v := range []struct {
		key            string
		enabled, quota bool
	}{{"", true, true}, {r.RuntimeKey, false, true}, {r.RuntimeKey, true, false}} {
		if _, err := l.Reserve(r, v.key, q, now, v.enabled, v.quota); err == nil {
			t.Fatal("unqualified launch")
		}
	}
	run, err := l.Reserve(r, r.RuntimeKey, q, now, true, true)
	if err != nil {
		t.Fatal(err)
	}
	again, _ := l.Reserve(r, r.RuntimeKey, q, now, true, true)
	if again != run {
		t.Fatal("not idempotent")
	}
	if err = l.Start(run.ID, now, true, true); err != nil {
		t.Fatal(err)
	}
	yes := true
	receipt := gpuReceipt(run, &yes, 2)
	bad := receipt
	bad.RequestSHA = strings.Repeat("0", 64)
	if l.Record(bad) == nil {
		t.Fatal("foreign receipt")
	}
	bad = receipt
	bad.CleanupConfirmed = false
	if l.Record(bad) == nil {
		t.Fatal("uncleared ownership")
	}
	if err = l.Record(receipt); err != nil {
		t.Fatal(err)
	}
	if l.Start(run.ID, now, true, true) == nil {
		t.Fatal("terminal rerun")
	}
	receipt.ChargedMS = 3
	if l.Record(receipt) == nil {
		t.Fatal("receipt mutation")
	}
}
func TestGPUResearchCancelAndUnknownDispatchConservativeBudget(t *testing.T) {
	l := GPUResearchLedger{}
	r := gpuFixture()
	now := time.Now()
	q := strings.Repeat("8", 64)
	for i := 0; i < 6; i++ {
		r.Script = "print(" + strings.Repeat("1", i+1) + ")"
		run, err := l.Reserve(r, r.RuntimeKey, q, now, true, true)
		if err != nil {
			t.Fatal(err)
		}
		l.Cancel(run.ID)
		if l.Start(run.ID, now, true, true) == nil {
			t.Fatal("cancelled launch")
		}
		receipt := gpuReceipt(run, nil, 0)
		receipt.State = "interrupted"
		if l.RecordAt(receipt, now) == nil {
			t.Fatal("unknown dispatch freed budget")
		}
		receipt.ChargedMS = run.MaxMS
		if err = l.RecordAt(receipt, now); err != nil {
			t.Fatal(err)
		}
	}
	r.Script = "print(77)"
	if _, err := l.Reserve(r, r.RuntimeKey, q, now, true, true); err == nil {
		t.Fatal("rolling budget bypass")
	}
	if _, err := l.Reserve(r, r.RuntimeKey, q, now.Add(25*time.Hour), true, true); err != nil {
		t.Fatal("temporary budget became permanent hold", err)
	}
}
func TestGPUResearchWaitingUnchargedAndCleanupBlocksNewOwner(t *testing.T) {
	l := GPUResearchLedger{}
	r := gpuFixture()
	now := time.Now()
	q := strings.Repeat("8", 64)
	run, _ := l.Reserve(r, r.RuntimeKey, q, now, true, true)
	r.Trial = 1
	if _, err := l.Reserve(r, r.RuntimeKey, q, now.Add(48*time.Hour), true, true); err == nil {
		t.Fatal("age cannot clear uncertain owner")
	}
	no := false
	receipt := gpuReceipt(run, &no, 0)
	receipt.State = "cancelled"
	if err := l.RecordAt(receipt, now.Add(48*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Reserve(r, r.RuntimeKey, q, now.Add(48*time.Hour), true, true); err != nil {
		t.Fatal(err)
	}
}

func TestGPUResearchRequestFreezesQualificationAndNormalizesArgv(t *testing.T) {
	l := GPUResearchLedger{}
	r := gpuFixture()
	r.Argv = nil
	q := strings.Repeat("8", 64)
	run, err := l.Reserve(r, r.RuntimeKey, q, time.Now(), true, true)
	if err != nil {
		t.Fatal(err)
	}
	if run.Request.QualificationSHA != q || !strings.Contains(string(run.RequestRaw), `"qualification_sha256":"`+q+`"`) || !strings.Contains(string(run.RequestRaw), `"argv":[]`) {
		t.Fatal("wire identity not frozen")
	}
	r.QualificationSHA = strings.Repeat("9", 64)
	if _, err = l.Reserve(r, r.RuntimeKey, q, time.Now(), true, true); err == nil {
		t.Fatal("foreign qualification accepted")
	}
	r = gpuFixture()
	r.Script = " \n\t"
	if _, err = l.Reserve(r, r.RuntimeKey, q, time.Now(), true, true); err == nil {
		t.Fatal("empty script admitted")
	}
}

func TestGPUResearchSemanticReuseAndChangedSourceProtocol(t *testing.T) {
	l := GPUResearchLedger{}
	r := gpuFixture()
	r.SourceTreeSHA = strings.Repeat("1", 64)
	q := strings.Repeat("8", 64)
	now := time.Now()
	first, err := l.Reserve(r, r.RuntimeKey, q, now, true, true)
	if err != nil {
		t.Fatal(err)
	}
	yes := true
	if err = l.Record(gpuReceipt(first, &yes, 17)); err != nil {
		t.Fatal(err)
	}
	r.OwnerJob = "33333333-3333-3333-3333-333333333333"
	r.OwnerTask = 99
	r.SourceSnapshotID = strings.Repeat("2", 64)
	r.SourceArchiveSHA = strings.Repeat("3", 64)
	reused, err := l.Reserve(r, r.RuntimeKey, q, now, true, true)
	if err != nil || reused != first {
		t.Fatalf("transport churn repeated executed trial: %v", err)
	}
	r.SourceTreeSHA = strings.Repeat("4", 64)
	fixed, err := l.Reserve(r, r.RuntimeKey, q, now, true, true)
	if err != nil || fixed == first {
		t.Fatalf("source fix cannot rerun same protocol: %v", err)
	}
	if err = l.Record(gpuReceipt(fixed, &yes, 23)); err != nil {
		t.Fatal(err)
	}
	r.Trial = 1
	deliberate, err := l.Reserve(r, r.RuntimeKey, q, now, true, true)
	if err != nil || deliberate == fixed {
		t.Fatalf("declared repeat refused: %v", err)
	}
}
