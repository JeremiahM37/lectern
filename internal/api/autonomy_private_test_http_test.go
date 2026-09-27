package api

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/autonomy"
)

func TestPrivateTestHTTPLostObservationKeepsRequestAndLease(t *testing.T) {
	s := autoTestServer(t)
	a, v, j, _ := privateFixture(t)
	j.PythonExpectedTestKey = strings.Repeat("e", 64)
	a.State = &autonomy.State{Phase: autonomy.Review, Assignments: []autonomy.Assignment{{TaskID: j.TaskID, Role: "reviewer"}}}
	expertQuota(a, time.Now())
	if err := s.saveAuto(a); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	shim := t.TempDir()
	t.Setenv("PATH", shim+":"+os.Getenv("PATH"))
	if err := os.WriteFile(filepath.Join(shim, "sudo"), []byte("#!/bin/sh\nexit 92\n"), 0700); err != nil {
		t.Fatal(err)
	}
	input, _ := json.Marshal(autoPrivateTestInput{CandidateSHA: v.Candidate.TreeSHA, Script: "print('candidate check')"})
	var check string
	for i := 0; i < 2; i++ {
		w := httptest.NewRecorder()
		s.autoPrivateTestBridgeAt(root, j.ID, w, httptest.NewRequest("POST", "/integration-tests", strings.NewReader(string(input))))
		if w.Code != 202 {
			t.Fatalf("lost response did not preserve pending work: %d %s", w.Code, w.Body.String())
		}
		var body struct {
			Check string `json:"check_id"`
			State string `json:"state"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if !autoHash256(body.Check) || body.State != "pending" || (check != "" && check != body.Check) {
			t.Fatal("retry replaced immutable check", w.Body.String())
		}
		check = body.Check
		persisted, err := s.loadAuto()
		if err != nil {
			t.Fatal(err)
		}
		attempt, lease, err := autoPrivateWorkerTest(persisted, j.ID, check)
		if err != nil || lease.Receipt != nil || len(attempt.TestLeases) != 1 {
			t.Fatal("lost reservation or double charged", err)
		}
		raw, err := os.ReadFile(filepath.Join(root, j.ID, "integration-requests", v.ID+".check-"+check+".json"))
		if err != nil || autoSHA(raw) != check {
			t.Fatal("exact durable request missing", err)
		}
		var request autoPrivateTestRequest
		if err := json.Unmarshal(raw, &request); err != nil || request.Runtime.PythonTest != j.PythonExpectedTestKey {
			t.Fatal("expected test tooling replaced by active default", err)
		}
	}
}

func TestPrivateTestHTTPRecordsOnlyBoundTerminalEvidence(t *testing.T) {
	s := autoTestServer(t)
	a, v, j, now := privateFixture(t)
	key := strings.Repeat("c", 64)
	receipt := privateTestReceipt(t, a, v, j, now, key)
	if err := s.saveAuto(a); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	response := filepath.Join(dir, "response.json")
	t.Setenv("LECTERN_PRIVATE_TEST_RESPONSE", response)
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	// Any accidental launch/mutation command fails; GET may only observe.
	if err := os.WriteFile(filepath.Join(dir, "sudo"), []byte("#!/bin/sh\n[ \"$3\" = integration-test-status ] || exit 91\ncat \"$LECTERN_PRIVATE_TEST_RESPONSE\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(receipt)
	var original map[string]any
	if err := json.Unmarshal(raw, &original); err != nil {
		t.Fatal(err)
	}
	original["execution_generation"] = receipt.Generation
	privateTestAttachRuntime(t, v, key, original)
	if err := s.saveAuto(a); err != nil {
		t.Fatal(err)
	}
	raw, _ = json.Marshal(original)
	for _, mutate := range []func(map[string]any){
		func(m map[string]any) { delete(m, "executed") },
		func(m map[string]any) { delete(m, "charged_ms") },
		func(m map[string]any) { delete(m, "truncated") },
		func(m map[string]any) { delete(m, "execution_generation") },
		func(m map[string]any) { m["execution_generation"] = 2 },
		func(m map[string]any) { m["generation"] = 2 },
		func(m map[string]any) { delete(m, "runtime") },
		func(m map[string]any) { m["runtime"] = autoExpertProbeRuntime{PythonBundle: strings.Repeat("f", 64)} },
		func(m map[string]any) { m["request_sha256"] = strings.Repeat("d", 64) },
		func(m map[string]any) { m["candidate_tree_sha256"] = strings.Repeat("d", 64) },
	} {
		body := map[string]any{}
		for k, val := range original {
			body[k] = val
		}
		mutate(body)
		bad, _ := json.Marshal(body)
		if err := os.WriteFile(response, bad, 0600); err != nil {
			t.Fatal(err)
		}
		w := httptest.NewRecorder()
		s.autoPrivateTestBridge(j.ID, w, httptest.NewRequest("GET", "/integration-tests?id="+key, nil))
		if w.Code < 400 {
			t.Fatalf("accepted malformed evidence: %s", bad)
		}
		persisted, err := s.loadAuto()
		if err != nil {
			t.Fatal(err)
		}
		_, lease, err := autoPrivateWorkerTest(persisted, j.ID, key)
		if err != nil || lease.Receipt != nil {
			t.Fatal("invalid evidence released reservation", err)
		}
	}
	if err := os.WriteFile(response, raw, 0600); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		w := httptest.NewRecorder()
		s.autoPrivateTestBridge(j.ID, w, httptest.NewRequest("GET", "/integration-tests?id="+key, nil))
		if w.Code != 200 {
			t.Fatalf("terminal receipt/replay failed: %d %s", w.Code, w.Body.String())
		}
	}
	persisted, err := s.loadAuto()
	if err != nil {
		t.Fatal(err)
	}
	attempt, lease, err := autoPrivateWorkerTest(persisted, j.ID, key)
	if err != nil || lease.Receipt == nil || len(attempt.Tests) != 1 || lease.Receipt.ChargedMilliseconds != 1000 {
		t.Fatal("terminal receipt not durably recorded exactly once", err)
	}
	if lease.Receipt.PythonBundleKey != strings.Repeat("1", 64) || lease.Receipt.PythonInputKey != strings.Repeat("2", 64) || lease.Receipt.BrowserKey != strings.Repeat("3", 64) || len(lease.Receipt.ReceiptEvidence) == 0 {
		t.Fatal("nested tested runtime provenance lost")
	}
	attempt.Generation = 2
	if err := s.saveAuto(persisted); err != nil {
		t.Fatal(err)
	}
	original["generation"] = 2
	raw, _ = json.Marshal(original)
	if err := os.WriteFile(response, raw, 0600); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	s.autoPrivateTestBridge(j.ID, w, httptest.NewRequest("GET", "/integration-tests?id="+key, nil))
	if w.Code != 200 {
		t.Fatalf("later observation lost original execution: %d %s", w.Code, w.Body.String())
	}
	persisted, err = s.loadAuto()
	if err != nil {
		t.Fatal(err)
	}
	_, lease, err = autoPrivateWorkerTest(persisted, j.ID, key)
	if err != nil || lease.Receipt.Generation != 1 {
		t.Fatal("observation rewrote launch authority", err)
	}
}

func privateTestAttachRuntime(t *testing.T, v *autoPrivateIntegrationAttempt, key string, body map[string]any) {
	t.Helper()
	runtime := autoExpertProbeRuntime{PythonBundle: strings.Repeat("1", 64), PythonInput: strings.Repeat("2", 64), Browser: strings.Repeat("3", 64)}
	body["runtime"] = runtime
	raw, err := json.Marshal(autoPrivateTestRequest{Runtime: runtime})
	if err != nil {
		t.Fatal(err)
	}
	v.TestLeases[key].RequestRaw = raw
}

func TestPrivateTestHTTPDiscoveryAndSelectors(t *testing.T) {
	s := autoTestServer(t)
	a, v, j, now := privateFixture(t)
	key := strings.Repeat("c", 64)
	privateTestReceipt(t, a, v, j, now, key)
	if err := s.saveAuto(a); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		query  string
		status int
	}{
		{"", 200}, {"?id=bad", 400}, {"?id=" + key + "&id=" + key, 400},
		{"?after=bad", 400}, {"?stream=stdout", 400},
		{"?id=" + key + "&stream=stdout&offset=-1", 400},
		{"?id=" + key + "&offset=0", 400}, {"?id=%zz", 400},
		{"?path=/etc", 400},
	} {
		w := httptest.NewRecorder()
		s.autoPrivateTestBridge(j.ID, w, httptest.NewRequest("GET", "/integration-tests"+tc.query, nil))
		if w.Code != tc.status {
			t.Fatalf("%s: got %d: %s", tc.query, w.Code, w.Body.String())
		}
		if tc.query == "" {
			var body struct {
				Items []struct {
					ID string `json:"check_id"`
				} `json:"items"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || len(body.Items) != 1 || body.Items[0].ID != key {
				t.Fatalf("lost durable check discovery: %s", w.Body.String())
			}
		}
	}
	w := httptest.NewRecorder()
	s.autoPrivateTestBridge("33333333-3333-4333-8333-333333333333", w, httptest.NewRequest("GET", "/integration-tests", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"items":[]`) {
		t.Fatalf("peer saw another review: %s", w.Body.String())
	}
}

func TestPrivateTestReceiptRequiresExecutionAccounting(t *testing.T) {
	for _, raw := range []string{
		`{}`, `{"executed":false}`, `{"charged_ms":0}`,
		`{"executed":null,"charged_ms":0}`,
		`{"executed":false,"charged_ms":null}`,
		`{"executed":"false","charged_ms":0}`,
		`{"executed":false,"charged_ms":-1}`,
		`{"executed":true,"charged_ms":610001}`,
		`{"executed":true,"charged_ms":1.5}`,
		`{"executed":true,"charged_ms":1}`,
		`{"executed":true,"charged_ms":1,"truncated":null}`,
	} {
		if _, err := autoDecodePrivateTestReceipt([]byte(raw)); err == nil {
			t.Fatalf("accepted unknown execution accounting: %s", raw)
		}
	}
	for _, raw := range []string{
		`{"executed":false,"charged_ms":0,"state":"unavailable"}`,
		`{"executed":true,"charged_ms":610000,"state":"timeout","truncated":false}`,
	} {
		if _, err := autoDecodePrivateTestReceipt([]byte(raw)); err != nil {
			t.Fatalf("rejected explicit accounting: %s: %v", raw, err)
		}
	}
}
