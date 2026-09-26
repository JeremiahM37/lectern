package autonomy

import (
	"encoding/json"
	"errors"
)

func validFinalOutcome(outcome string) bool {
	return outcome == "completed" || outcome == "blocked" || outcome == "incomplete"
}

func validateBuildOutcome(r BuildReport, version int) error {
	if r.Outcome == "" && version < 2 {
		return nil
	}
	if r.Decision != nil {
		if r.Outcome != "decision" {
			return errors.New("decision checkpoint requires outcome=decision")
		}
	} else if !validFinalOutcome(r.Outcome) {
		return errors.New("build outcome must be completed, blocked or incomplete")
	}
	return nil
}

func (s *State) validateReviewOutcome(v Verdict, reviewer Assignment) error {
	if v.Outcome == "" && reviewer.ReportVersion < 2 {
		return nil
	}
	if !validFinalOutcome(v.Outcome) {
		return errors.New("review outcome must be completed, blocked or incomplete")
	}
	if !*v.Approve {
		return nil
	}
	if v.Outcome != "completed" {
		return errors.New("blocked or incomplete work cannot be approved")
	}
	for _, builder := range s.Assignments {
		if builder.Role != "builder" || !builder.Completed || builder.Item != reviewer.Item || builder.Round != reviewer.Round || builder.Step != reviewer.Step {
			continue
		}
		var report BuildReport
		if err := json.Unmarshal(s.Reports[builder.TaskID], &report); err != nil {
			return errors.New("builder outcome evidence unavailable")
		}
		if report.Outcome != "completed" && (report.Outcome != "" || builder.ReportVersion >= 2) {
			return errors.New("builder did not report completed work; cannot approve it")
		}
		return nil
	}
	return errors.New("completed builder evidence unavailable")
}

// Legacy reports remain interpretable; explicit non-completion never promotes.
func (v Verdict) AcceptsWork() bool {
	return v.Approve != nil && *v.Approve && (v.Outcome == "" || v.Outcome == "completed")
}
