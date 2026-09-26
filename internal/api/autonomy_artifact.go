package api

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/autonomy"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// Old releases mislabeled a two-minute archive timeout as an invalid report.
// Recover only this exact failure with a successful stopped worker and a report
// that passes the unchanged state machine. Never start another model attempt.
func (s *Server) recoverLegacyArtifactTimeout(ctx context.Context, a *autoRecord, provider string, now time.Time) bool {
	if a.State.Reason != "Invalid worker report: signal: killed" && !strings.HasPrefix(a.State.Reason, "runner prepare:") && !strings.HasPrefix(a.State.Reason, "runner copy:") {
		return false
	}
	for _, id := range a.State.ActiveTaskIDs() {
		j := autoFindJob(a, id)
		if j == nil || (j.Status != "failed" && j.Status != "stopped") || j.ReportError != "" || (j.Role != "builder" && j.Role != "reviewer") {
			continue
		}
		raw, err := s.runAutoCommand(ctx, "status", "--job", j.ID)
		var status struct {
			State    string `json:"state"`
			ExitCode *int   `json:"exit_code"`
		}
		if err != nil || json.Unmarshal(raw, &status) != nil || status.State != "done" || status.ExitCode == nil || *status.ExitCode != 0 {
			continue
		}
		report, err := s.runAutoCommand(ctx, "report", "--job", j.ID)
		if err != nil {
			continue
		}
		var resumed autonomy.State
		if json.Unmarshal([]byte(store.J(a.State)), &resumed) != nil || resumed.Resume(a.Config, a.Quota.Providers, []string{provider}, now) != nil {
			continue
		}
		var validation autonomy.State
		_ = json.Unmarshal([]byte(store.J(&resumed)), &validation)
		if validation.ApplyReport(a.Config, j.TaskID, report) != nil {
			continue
		}
		a.State = &resumed
		j.Status = "exporting"
		a.Status = "preserving_artifacts"
		a.Reason = "Recovered archive timeout; preserving original completed work before review"
		return true
	}
	return false
}
