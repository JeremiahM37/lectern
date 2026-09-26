package api

import (
	"errors"
	"fmt"
	"regexp"

	"github.com/JeremiahM37/lectern/v2/internal/autonomy"
)

// Plan auditors receive the same completed planner snapshot, never each other's
// workspace or verdict. Match the exact revision so an older plan cannot supply
// evidence for a newly revised proposal.
var autoPlanJobID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func autoPlanEvidence(a *autoRecord) (*autoJob, error) {
	if a == nil || a.State == nil || a.State.Phase != autonomy.Audit {
		return nil, errors.New("planner evidence requires the current audit phase")
	}
	var source *autoJob
	for _, assignment := range a.State.Assignments {
		if assignment.Role != "planner" || assignment.Round != a.State.Revision || assignment.Item != a.State.Item || assignment.Step != a.State.Step || !assignment.Completed {
			continue
		}
		job := autoFindJob(a, assignment.TaskID)
		if job == nil || job.Role != "planner" || job.Status != "done" || !autoPlanJobID.MatchString(job.ID) {
			return nil, errors.New("completed planner evidence is unavailable")
		}
		if source != nil {
			return nil, errors.New("ambiguous planner evidence")
		}
		source = job
	}
	if source == nil {
		return nil, errors.New("current planner evidence is unavailable")
	}
	return source, nil
}

func autoPlanEvidencePath(job *autoJob) string {
	return fmt.Sprintf("/work/.lectern-review/%s/work", job.ID)
}
