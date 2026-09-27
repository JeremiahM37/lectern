package api

import (
	"encoding/json"
	"github.com/JeremiahM37/lectern/v2/internal/autonomy"
	"strings"
	"testing"
)

func TestPrivateTestInputAuthorityAndLimits(t *testing.T) {
	good := autoPrivateTestInput{CandidateSHA: strings.Repeat("a", 64), Script: "print('test')"}
	raw, _ := json.Marshal(good)
	got, err := autoDecodePrivateTestInput(strings.NewReader(string(raw)))
	if err != nil || got.Profile != "integration600" {
		t.Fatal("valid test rejected", err)
	}
	for _, change := range []func(*autoPrivateTestInput){
		func(p *autoPrivateTestInput) { p.Profile = "unlimited" },
		func(p *autoPrivateTestInput) { p.CandidateSHA = "HEAD" },
		func(p *autoPrivateTestInput) { p.Script = " \n" },
		func(p *autoPrivateTestInput) { p.Fixtures = []autoExpertProbeFixture{{Path: "main.py", Content: ""}} },
		func(p *autoPrivateTestInput) {
			p.Fixtures = []autoExpertProbeFixture{{Path: "../../host", Content: ""}}
		},
	} {
		input := good
		change(&input)
		body, _ := json.Marshal(input)
		if _, err := autoDecodePrivateTestInput(strings.NewReader(string(body))); err == nil {
			t.Fatal("invalid test accepted", string(body))
		}
	}
	for _, field := range []string{`"owner_job":"forged"`, `"runtime":{}`, `"integration_id":"forged"`, `"repo_path":"/etc"`} {
		body := strings.TrimSuffix(string(raw), "}") + "," + field + "}"
		if _, err := autoDecodePrivateTestInput(strings.NewReader(body)); err == nil {
			t.Fatal("worker supplied controller authority", field)
		}
	}
	if _, err := autoDecodePrivateTestInput(strings.NewReader(string(raw) + string(raw))); err == nil {
		t.Fatal("trailing request accepted")
	}
}

func TestPrivateTestOwnerAndDerivedRequest(t *testing.T) {
	a, v, j, _ := privateFixture(t)
	a.State = &autonomy.State{Phase: autonomy.Review, Assignments: []autonomy.Assignment{{TaskID: j.TaskID, Role: "reviewer"}}}
	if _, _, err := autoPrivateTestOwner(a, j.ID); err != nil {
		t.Fatal(err)
	}
	input := &autoPrivateTestInput{CandidateSHA: v.Candidate.TreeSHA, Profile: "integration600", Script: "print('check')"}
	raw, err := autoPrivateTestRequestBytes(a, j, input, strings.Repeat("c", 64))
	if err != nil {
		t.Fatal(err)
	}
	var request autoPrivateTestRequest
	if err = json.Unmarshal(raw, &request); err != nil {
		t.Fatal(err)
	}
	if request.OwnerJob != j.ID || request.IntegrationID != v.ID || request.CandidateReceiptSHA != v.Candidate.ReceiptSHA || request.Runtime.PythonTest != strings.Repeat("c", 64) || request.Fixtures == nil || request.Argv == nil {
		t.Fatal("request lost trusted authority or list shape")
	}
	input.CandidateSHA = strings.Repeat("f", 64)
	if _, err = autoPrivateTestRequestBytes(a, j, input, ""); err == nil {
		t.Fatal("unreviewed candidate selected")
	}
	input.CandidateSHA = v.Candidate.TreeSHA
	j.PythonUsedBundle = "unbound"
	if _, err = autoPrivateTestRequestBytes(a, j, input, ""); err == nil {
		t.Fatal("unverified runtime used")
	}
	j.PythonUsedBundle = ""
	if _, _, err = autoPrivateTestOwner(a, "other"); err == nil {
		t.Fatal("peer admitted")
	}
	a.Config.Enabled = false
	if _, _, err = autoPrivateTestOwner(a, j.ID); err == nil {
		t.Fatal("OFF admitted")
	}
	a.Config.Enabled = true
	v.StopRequested = true
	if _, _, err = autoPrivateTestOwner(a, j.ID); err == nil {
		t.Fatal("cancelled attempt admitted")
	}
	v.StopRequested = false
	a.State.Assignments[0].Completed = true
	if _, _, err = autoPrivateTestOwner(a, j.ID); err == nil {
		t.Fatal("completed reviewer admitted")
	}
}

func TestPrivateTestReadSurvivesCorrectionButNotPeerOrCandidateChange(t *testing.T) {
	a, v, j, now := privateFixture(t)
	receipt := privateTestReceipt(t, a, v, j, now, strings.Repeat("c", 64))
	if err := autoRecordPrivateTest(a, receipt); err != nil {
		t.Fatal(err)
	}
	oldID := j.ID
	corrected := *j
	corrected.ID = autoUUID()
	j.Status = "done"
	a.Jobs = append(a.Jobs, &corrected)
	v.ReviewerJob = corrected.ID
	if _, _, err := autoPrivateWorkerTest(a, corrected.ID, receipt.CheckID); err != nil {
		t.Fatal("lost same-task evidence", err)
	}
	rows := autoPrivateTestDiscovery(a, corrected.ID, "")
	if len(rows) != 1 || rows[0]["owner_job"] != oldID {
		t.Fatal("correction cannot discover original check")
	}
	if _, _, err := autoPrivateWorkerTest(a, autoUUID(), receipt.CheckID); err == nil {
		t.Fatal("peer read admitted")
	}
	j.Status = "running"
	if _, _, err := autoPrivateWorkerTest(a, corrected.ID, receipt.CheckID); err == nil {
		t.Fatal("concurrent reviewer inherited evidence")
	}
	j.Status = "done"
	v.Candidate.TreeSHA = strings.Repeat("f", 64)
	if _, _, err := autoPrivateWorkerTest(a, corrected.ID, receipt.CheckID); err == nil {
		t.Fatal("changed candidate retained check eligibility")
	}
}
