package api

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOrdinaryAuditSourceContractSurvivesHistoryRotationWithoutReserving(t *testing.T) {
	s, a, id := ordinaryAuditFixture(t)
	if err := s.saveAuto(a); err != nil {
		t.Fatal(err)
	}
	before, _ := s.loadAuto()
	beforeRaw, _ := json.Marshal(before)
	w := httptest.NewRecorder()
	s.autoReadBridge(w, httptest.NewRequest("GET", autoOrdinaryContractLink(id), nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Preserve original PREREG before measurements") || !strings.Contains(w.Body.String(), "Both plan auditors") || !strings.Contains(w.Body.String(), "Historical source contract only") {
		t.Fatal(w.Code, w.Body.String())
	}
	after, _ := s.loadAuto()
	afterRaw, _ := json.Marshal(after)
	if string(beforeRaw) != string(afterRaw) || autoCheckpointApproved(after, id) {
		t.Fatal("read reserved or approved historical task")
	}
	rows := s.autoRepairableArtifacts(after)
	if len(rows) == 0 || rows[0]["source_contract"] != autoOrdinaryContractLink(id) || !strings.Contains(rows[0]["review_evidence_note"].(string), "both plan auditors") {
		t.Fatal("planner cannot discover preapproval evidence", rows)
	}
}

func TestOrdinaryAuditSourceContractRejectsAmbiguousSelectorsAndWrites(t *testing.T) {
	s, a, id := ordinaryAuditFixture(t)
	if err := s.saveAuto(a); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{"", "?task_id=0", "?task_id=-1", "?task_id=../1", fmt.Sprintf("?task_id=%d&task_id=%d", id, id), fmt.Sprintf("?task_id=%d&job_id=other", id)} {
		w := httptest.NewRecorder()
		s.autoReadBridge(w, httptest.NewRequest("GET", "/source-contract"+query, nil))
		if w.Code != 400 {
			t.Fatal(query, w.Code, w.Body.String())
		}
	}
	w := httptest.NewRecorder()
	s.autoReadBridge(w, httptest.NewRequest("POST", autoOrdinaryContractLink(id), strings.NewReader(`{"approve":true}`)))
	if w.Code != 405 {
		t.Fatal(w.Code, w.Body.String())
	}
	a.Jobs[0].Status = "running"
	if err := s.saveAuto(a); err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	s.autoReadBridge(w, httptest.NewRequest("GET", autoOrdinaryContractLink(id), nil))
	if w.Code != 404 {
		t.Fatal("unfinished source exposed", w.Code)
	}
}
