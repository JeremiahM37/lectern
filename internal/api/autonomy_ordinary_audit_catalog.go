package api

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"github.com/JeremiahM37/lectern/v2/internal/autonomy"
)

const autoOrdinaryAuditDelivery = "Both plan auditors receive the selected source and paired review, original-root evidence, controller-held acceptance and original plan audits before deciding the plan. Completed archives are prepared asynchronously. Registration of this delivery path is not proof that a particular archive was delivered, approval of the proposal, or an additional repair attempt."

func autoOrdinaryContractLink(taskID int64) string {
	return fmt.Sprintf("/source-contract?task_id=%d", taskID)
}

// Discovery is read-only and does not reserve a repair. Original criteria remain
// retrievable after the rolling history view no longer includes their cycle.
func (s *Server) autoOrdinarySourceContract(w http.ResponseWriter, r *http.Request) {
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || len(query) != 1 || len(query["task_id"]) != 1 {
		http.Error(w, "one task_id required", 400)
		return
	}
	id, err := strconv.ParseInt(query.Get("task_id"), 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "positive task_id required", 400)
		return
	}
	a, err := s.loadAuto()
	if err != nil {
		http.Error(w, "source contract unavailable", 503)
		return
	}
	j := autoFindJob(a, id)
	if j == nil || j.Role != "builder" || j.Status != "done" {
		http.Error(w, "completed source unavailable", 404)
		return
	}
	task, err := s.DB.Task(id)
	if err != nil {
		http.Error(w, "source task unavailable", 404)
		return
	}
	p := autonomy.Proposal{ProjectID: task.ProjectID}
	if autoCheckpointApproved(a, id) && !j.Rejected {
		p.ContinueTaskID = id
	} else {
		p.RepairTaskID = id
	}
	view := *a
	view.State = &autonomy.State{Items: []autonomy.Proposal{p}}
	rows, err := autoOrdinaryAuditEvidenceRows(&view)
	if err != nil || len(rows) != 1 {
		http.Error(w, "source provenance incomplete", 409)
		return
	}
	// Resolve decision evidence against the actual retained history, not the
	// synthetic selection used only to choose one source contract above.
	rows[0].LineageDecisions = autoOrdinaryLineageDecisions(a, j, s.autoOrdinaryDecisionPromptState)
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, map[string]any{"contract": rows[0], "audit_delivery": autoOrdinaryAuditDelivery, "scope": "Historical source contract only. Current repair/continuation eligibility remains in /repairable and /artifacts. A missing historical decision is unknown, not approval."})
}
