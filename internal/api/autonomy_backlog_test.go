package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/JeremiahM37/lectern/v2/internal/autonomy"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

func TestBacklogIndexIsExplicitlyIncompleteAndDetailsSurviveRotation(t *testing.T) {
	s := autoTestServer(t)
	state, _ := autonomy.NewState("2026-09-25")
	proposal := autonomy.Proposal{ProjectID: 1, Title: "Unicode 🧪 research", Why: strings.Repeat("界", 300) + " REQUIRED OWNERSHIP CLEARANCE", RepairTaskID: 7, Acceptance: []string{"preserve unique evidence"}, Novelty: "unmeasured, see original study", Score: 80}
	state.Backlog = []autonomy.Proposal{proposal}
	a := &autoRecord{Config: autonomy.DefaultConfig(), State: state}
	before := store.J(a.State.Backlog)
	index := autoBacklogIndex(state.Backlog)
	if len(index) != 1 || !index[0].DetailRequired || !utf8.ValidString(index[0].WhyPreview) || strings.Contains(index[0].WhyPreview, "REQUIRED OWNERSHIP") {
		t.Fatal(index)
	}
	if index[0].RepairTaskID != 7 || !strings.Contains(index[0].WhyPreview, "preview") {
		t.Fatal("lineage or omission label lost")
	}
	if store.J(a.State.Backlog) != before {
		t.Fatal("index rewrote retained evidence")
	}
	// Retained and deferred cycles remain addressable by their exact content;
	// the same title in a new cycle cannot silently supply different conditions.
	next, _ := autonomy.NewState("2026-09-26")
	changed := proposal
	changed.Why = "different conditions"
	next.Backlog = []autonomy.Proposal{changed}
	a.State = next
	a.DeferredRuns = []*autonomy.State{state}
	if e := s.saveAuto(a); e != nil {
		t.Fatal(e)
	}
	w := httptest.NewRecorder()
	s.autoReadBridge(w, httptest.NewRequest("GET", index[0].DetailsURI, nil))
	var got autonomy.Proposal
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &got) != nil || !reflect.DeepEqual(got, proposal) {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, query := range []string{"/backlog?key=" + strings.Repeat("0", 64), "/backlog?key=../bad"} {
		w = httptest.NewRecorder()
		s.autoReadBridge(w, httptest.NewRequest("GET", query, nil))
		if w.Code != 404 {
			t.Fatal(query, w.Code)
		}
	}
	w = httptest.NewRecorder()
	s.autoReadBridge(w, httptest.NewRequest("GET", "/backlog", nil))
	var full []autonomy.Proposal
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &full) != nil || !reflect.DeepEqual(full, next.Backlog) {
		t.Fatal("legacy full endpoint changed", w.Body.String())
	}
}
func TestBacklogDiscoveryDoesNotDisplaceSelectedAcceptance(t *testing.T) {
	s := autoTestServer(t)
	state, _ := autonomy.NewState("2026-09-25")
	selected := autonomy.Proposal{ProjectID: 1, Title: "Selected milestone", Why: "selected context", Acceptance: []string{"MANDATORY_ACCEPTANCE_" + strings.Repeat("unique", 400)}}
	state.Items = []autonomy.Proposal{selected}
	state.Backlog = []autonomy.Proposal{{ProjectID: 2, Title: "Other opportunity", Why: strings.Repeat("old cycle status ", 1500) + "UNSELECTED_TAIL", Acceptance: []string{"UNSELECTED_CHECKLIST"}}}
	a := &autoRecord{Config: autonomy.DefaultConfig(), State: state}
	before := store.J(state)
	for _, role := range []string{"planner", "auditor_a", "auditor_b", "builder", "reviewer", "decision_a", "decision_b"} {
		prompt := s.autoPrompt(context.Background(), a, role, &store.Project{Name: "fixture"})
		if !strings.Contains(prompt, selected.Acceptance[0]) {
			t.Fatal(role, "selected acceptance truncated")
		}
		if strings.Contains(prompt, "UNSELECTED_TAIL") || strings.Contains(prompt, "UNSELECTED_CHECKLIST") {
			t.Fatal(role, "full backlog leaked into assignment")
		}
		if role == "planner" && (!strings.Contains(prompt, "INCOMPLETE previews") || !strings.Contains(prompt, autoBacklogKey(state.Backlog[0]))) {
			t.Fatal("planner cannot discover full entry")
		}
	}
	if store.J(state) != before {
		t.Fatal("prompt mutated authoritative state")
	}
}
func TestBacklogMeasurement(t *testing.T) {
	path := os.Getenv("LECTERN_BACKLOG_MEASURE_REPORT")
	if path == "" {
		t.Skip("explicit historical report required")
	}
	raw, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	var report autonomy.PlanReport
	if e = json.Unmarshal(raw, &report); e != nil {
		t.Fatal(e)
	}
	full, _ := json.Marshal(report.Backlog)
	index, _ := json.Marshal(autoBacklogIndex(report.Backlog))
	if len(index) >= len(full) {
		t.Fatal("index did not reduce real context")
	}
	a := &autoRecord{State: &autonomy.State{Backlog: report.Backlog}}
	for _, entry := range autoBacklogIndex(report.Backlog) {
		p, ok := autoBacklogDetail(a, entry.Key)
		if !ok || autoBacklogKey(p) != entry.Key {
			t.Fatal("detail lookup lost content")
		}
	}
	t.Logf("historical backlog: %d entries; full=%d bytes; index=%d bytes; reduction=%.1f%%; all exact full entries recoverable", len(report.Backlog), len(full), len(index), 100*(1-float64(len(index))/float64(len(full))))
}
