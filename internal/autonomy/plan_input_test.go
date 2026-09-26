package autonomy

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCopiedBacklogCatalogKeyDoesNotRequireModelRepair(t *testing.T) {
	raw := `{"items":[{"project_id":86,"title":"Measured experiment","why":"Falsifiable useful comparison","acceptance":["Freeze oracle; reproduce and compare"],"score":78}],"backlog":[{"key":"archive-destination-collision-preflight","project_id":86,"title":"Measured experiment","why":"Useful if independently verified","score":78}]}`
	s := emptyPlanState(t, 3, raw)
	if s.Phase != Audit || len(s.Backlog) != 1 || s.Backlog[0].ProjectID != 86 {
		t.Fatalf("bad transition: %+v", s)
	}
	encoded, _ := json.Marshal(s.Backlog)
	if strings.Contains(string(encoded), `"key"`) {
		t.Fatal("display key leaked into authoritative proposal")
	}
	if string(s.Reports[1]) != raw {
		t.Fatal("original report evidence changed")
	}
	if s.RegisterTask("builder", 9) == nil {
		t.Fatal("catalog metadata bypassed audits")
	}
}
func TestBacklogMetadataDoesNotRelaxReportValidation(t *testing.T) {
	good := `{"items":[{"project_id":86,"title":"Experiment","why":"Useful","acceptance":["Compare"]}],"backlog":[{"key":"display","project_id":86,"title":"Later","why":"Useful"}]}`
	for _, raw := range []string{
		strings.Replace(good, `"key":"display"`, `"approve":true`, 1),
		strings.Replace(good, `"key":"display"`, `"key":"a","key":"b"`, 1),
		strings.Replace(good, `"key":"display"`, `"key":123`, 1),
		strings.Replace(good, `"key":"display"`, `"key":"`+strings.Repeat("x", 129)+`"`, 1),
		strings.Replace(good, `"items":[{`, `"items":[{"key":"not-authority",`, 1),
		strings.Replace(good, `"title":"Later"`, `"title":""`, 1),
		strings.Replace(good, `"key":"display"`, `"key":"display","repair_task_id":5,"continue_task_id":6`, 1),
		good + `{}`,
	} {
		s, _ := NewState("2026-09-25")
		if err := s.RegisterTask("planner", 1); err != nil {
			t.Fatal(err)
		}
		before, _ := json.Marshal(s)
		if err := s.ApplyReport(enabled(), 1, []byte(raw)); err == nil {
			t.Fatal("accepted invalid report", raw)
		}
		after, _ := json.Marshal(s)
		if string(before) != string(after) {
			t.Fatal("invalid report changed state")
		}
	}
}

func TestNullBacklogCatalogKeyIsAbsentMetadata(t *testing.T) {
	var out PlanReport
	if err := decodePlanReport([]byte(`{"items":[],"backlog":[{"key":null,"project_id":1,"title":"Later","why":"Useful"}]}`), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Backlog) != 1 || out.Backlog[0].ProjectID != 1 {
		t.Fatal(out)
	}
}
