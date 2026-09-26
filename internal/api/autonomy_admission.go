package api

import (
	"net/http"

	"github.com/JeremiahM37/lectern/v2/internal/autonomy"
)

// Admission is a durable reservation, not an artifact approval. It is captured
// only by the controller after source validation and workspace preparation.
type autoAdmission struct {
	JobID               string                      `json:"job_id"`
	TaskID              int64                       `json:"task_id"`
	Date                string                      `json:"date"`
	Cycle               int                         `json:"cycle"`
	Proposal            autonomy.Proposal           `json:"proposal"`
	RepairAttemptTaskID int64                       `json:"repair_attempt_task_id,omitempty"`
	PlanAudits          map[string]autonomy.Verdict `json:"plan_audits"`
	Scope               string                      `json:"scope"`
}

func autoNewAdmission(a *autoRecord, j *autoJob) *autoAdmission {
	if j.Role != "builder" || !autoRepairAudited(a) || a.State.Item < 0 || a.State.Item >= len(a.State.Items) {
		return nil
	}
	audits := make(map[string]autonomy.Verdict)
	for role, v := range a.State.Audits {
		audits[role] = v
	}
	return &autoAdmission{JobID: j.ID, TaskID: j.TaskID, Date: a.State.Date, Cycle: a.State.Cycle,
		Proposal: a.State.Items[a.State.Item], RepairAttemptTaskID: j.RepairAttemptTaskID, PlanAudits: audits,
		Scope: "Reserved isolated assignment only. No artifact approval, publication, deployment or additional repair attempt authorized. Future catalog availability does not revoke this reservation."}
}

// Bind identity to the controller-created socket, never a worker query parameter.
func (s *Server) autoJobReadBridge(jobID string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/assignment" && r.URL.Path != "/prerequisite" {
			s.autoReadBridge(w, r)
			return
		}
		if r.Method != http.MethodGet {
			http.Error(w, "read-only bridge", http.StatusMethodNotAllowed)
			return
		}
		a, err := s.loadAuto()
		if err != nil {
			http.Error(w, "assignment unavailable", http.StatusServiceUnavailable)
			return
		}
		for _, j := range a.Jobs {
			if r.URL.Path == "/prerequisite" && j.ID == jobID {
				writeJSON(w, http.StatusOK, j.Recovery)
				return
			}
			if j.ID == jobID && j.Admission != nil && j.Admission.JobID == jobID && j.Admission.TaskID == j.TaskID {
				w.Header().Set("Cache-Control", "no-store")
				writeJSON(w, http.StatusOK, j.Admission)
				return
			}
		}
		http.Error(w, "no recorded admission for this worker", http.StatusNotFound)
	}
}
