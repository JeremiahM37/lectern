package api

import (
	"encoding/json"
	"strings"
	"testing"
)

func nodeRuntimeJob(j *autoJob) {
	r, _ := autoNodeInputs(nodeRequirement())
	r.SourceJob = "retained-source"
	r.SourceSHA = autoSHA([]byte("source"))
	r.AdmissionSHA = autoSHA([]byte("admission"))
	j.NodeRequest = r
	p := nodeVerified(j)
	j.NodeRecovery = &p
	j.NodeUsedBundle = p.BundleKey
}
func TestNodePrivateExecutionRequiresExactRuntimeProof(t *testing.T) {
	a, v, j, now := privateFixture(t)
	nodeRuntimeJob(j)
	input := &autoPrivateTestInput{CandidateSHA: v.Candidate.TreeSHA, Profile: "integration600", Script: "print('actual test')"}
	requestRaw, e := autoPrivateTestRequestBytes(a, j, input, strings.Repeat("d", 64))
	if e != nil {
		t.Fatal(e)
	}
	var request autoPrivateTestRequest
	json.Unmarshal(requestRaw, &request)
	if request.Runtime.NodeBundle != j.NodeUsedBundle || request.Runtime.NodeLock != j.NodeRecovery.LockSHA || request.Runtime.PythonTest != strings.Repeat("d", 64) {
		t.Fatal("selected Node displaced Python or lost identity")
	}
	key := autoSHA(requestRaw)
	r := privateTestReceipt(t, a, v, j, now, key)
	lease := v.TestLeases[key]
	lease.RequestRaw = requestRaw
	raw, _ := json.Marshal(r)
	var body map[string]any
	json.Unmarshal(raw, &body)
	body["execution_generation"] = r.Generation
	body["runtime"] = request.Runtime
	for _, change := range []func(*autoExpertProbeRuntime){func(x *autoExpertProbeRuntime) { x.NodeBundle = "" }, func(x *autoExpertProbeRuntime) { x.NodeLock = autoSHA([]byte("newlock")) }, func(x *autoExpertProbeRuntime) { x.NodeRuntime = "" }, func(x *autoExpertProbeRuntime) { x.NodeInput = "" }} {
		bad := request.Runtime
		change(&bad)
		body["runtime"] = bad
		raw, _ = json.Marshal(body)
		if _, e := autoDecodeBoundPrivateTestReceipt(raw, v.ID, lease, r.Generation); e == nil {
			t.Fatal("unbound Node execution accepted")
		}
	}
	body["runtime"] = request.Runtime
	raw, _ = json.Marshal(body)
	got, e := autoDecodeBoundPrivateTestReceipt(raw, v.ID, lease, r.Generation)
	if e != nil {
		t.Fatal(e)
	}
	if !autoPrivateCheckMatchesRuntime(j, got) || got.NodeRuntimeDigest != j.NodeRecovery.RuntimeDigest || got.NodeLockSHA != j.NodeRecovery.LockSHA {
		t.Fatal("tested runtime provenance lost")
	}
	corrected := *j
	corrected.ID = "corrected"
	if !autoPrivateCheckMatchesRuntime(&corrected, got) {
		t.Fatal("report correction lost exact executed runtime")
	}
	corrected.NodeRecovery = nil
	if autoPrivateCheckMatchesRuntime(&corrected, got) {
		t.Fatal("missing Node delivery promoted")
	}
	j.NodeUsedBundle = ""
	if _, e := autoPrivateTestRequestBytes(a, j, input, ""); e == nil {
		t.Fatal("request alone became available runtime")
	}
}
func TestNodeExpertProbePinsAndRetainsActualRuntimeAcrossReload(t *testing.T) {
	s, a, p, now := expertFixture(t)
	expertAudits(t, s, a, p, now, "existing fixture")
	var lease *autoExpertProbeLease
	for _, l := range a.ExpertRecovery.Probes {
		lease = l
		break
	}
	j := autoFindJob(a, lease.OwnerTask)
	nodeRuntimeJob(j)
	input := &autoExpertProbeInput{ProgressKey: p.ExpertProgressKey, Profile: "ordinary180", Script: "print('independent')"}
	_, raw, e := autoExpertProbeRequestBytes(a, j, input, "")
	if e != nil {
		t.Fatal(e)
	}
	lease.RequestKey = autoSHA(raw)
	original := *lease.Receipt
	original.RequestKey = lease.RequestKey
	lease.Receipt = nil
	if e := autoBindExpertNodeRuntime(lease, raw); e != nil {
		t.Fatal(e)
	}
	saved, _ := json.Marshal(a)
	var restored autoRecord
	json.Unmarshal(saved, &restored)
	l := restored.ExpertRecovery.Probes[lease.ID]
	if e := autoRecordExpertProbe(&restored, original); e == nil {
		t.Fatal("successful Node probe omitted runtime proof")
	}
	bad := *l.Runtime
	bad.NodeLock = autoSHA([]byte("other"))
	original.Runtime = &bad
	if e := autoRecordExpertProbe(&restored, original); e == nil {
		t.Fatal("changed Node lock accepted")
	}
	exact := *l.Runtime
	original.Runtime = &exact
	if e := autoRecordExpertProbe(&restored, original); e != nil {
		t.Fatal(e)
	}
	if l.Receipt.Runtime.NodeBundle != j.NodeUsedBundle {
		t.Fatal("actual selected environment not retained")
	}
}
func TestNodeRuntimeLegacyBytesAndNoUnexpectedSelection(t *testing.T) {
	r := autoExpertProbeRuntime{}
	raw, _ := json.Marshal(r)
	if string(raw) != `{"python_bundle_key":"","python_input_key":"","browser_key":"","python_test_key":""}` {
		t.Fatal("legacy request bytes changed", string(raw))
	}
	yes := true
	legacy := autoExpertProbeReceipt{State: "exited", Executed: &yes}
	if e := autoExpertNodeReceiptRuntime(&autoExpertProbeLease{}, legacy); e != nil {
		t.Fatal("legacy non-Node execution refused", e)
	}
	legacy.Runtime = &autoExpertProbeRuntime{NodeBundle: autoSHA([]byte("not selected"))}
	if e := autoExpertNodeReceiptRuntime(&autoExpertProbeLease{}, legacy); e == nil {
		t.Fatal("legacy request acquired undeclared Node runtime")
	}
}
func TestNodeExpertFallbackOnlyExactPinnedSource(t *testing.T) {
	s, a, p, now := expertFixture(t)
	pin := a.ExpertRecovery.Pins[p.ExpertProgressKey]
	source := autoFindJob(a, pin.SourceTaskID)
	nodeRuntimeJob(source)
	if e := autoPinExpertNodeSource(pin, source); e != nil {
		t.Fatal(e)
	}
	// Pin changed before any audit: no previous audit/probe authority is reused.
	delete(a.ExpertRecovery.Pins, p.ExpertProgressKey)
	pin.Key = autoExpertPinKey(pin)
	p.ExpertProgressKey = pin.Key
	a.ExpertRecovery.Pins[pin.Key] = pin
	a.State.Items[0] = p
	expertAudits(t, s, a, p, now, "independent before fallback")
	owner := autoFindJob(a, a.State.Assignments[0].TaskID)
	input := &autoExpertProbeInput{ProgressKey: pin.Key, Profile: "ordinary180", Script: "print('reproduce')"}
	_, raw, e := autoExpertProbeRequestBytes(a, owner, input, strings.Repeat("a", 64))
	if e != nil {
		t.Fatal(e)
	}
	var request autoExpertProbeRequest
	json.Unmarshal(raw, &request)
	if request.Runtime.NodeBundle != source.NodeUsedBundle || owner.NodeRequest != nil || owner.NodeUsedBundle != "" {
		t.Fatal("source fallback added ordinary auditor privileges or missed runtime")
	}
	original := *pin.NodeSource
	pin.NodeSource.ArchiveSHA = autoSHA([]byte("other archive"))
	if _, _, e := autoExpertProbeRequestBytes(a, owner, input, ""); e == nil {
		t.Fatal("foreign archive accepted")
	}
	*pin.NodeSource = original
	source.NodeUsedBundle = autoSHA([]byte("other use"))
	if _, _, e := autoExpertProbeRequestBytes(a, owner, input, ""); e == nil {
		t.Fatal("different actual used bundle accepted")
	}
	source.NodeUsedBundle = original.Runtime.NodeBundle
	source.NodeRecovery.ProbeSHA = autoSHA([]byte("changed receipt"))
	if _, _, e := autoExpertProbeRequestBytes(a, owner, input, ""); e == nil {
		t.Fatal("different delivery receipt accepted")
	}
}
