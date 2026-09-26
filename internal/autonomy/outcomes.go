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
	} else if !validFinalOutcome(r.Outcome) && r.Outcome != "ready_for_review" {
		return errors.New("build outcome must be ready_for_review, blocked or incomplete (legacy completed is also accepted)")
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
		// The independent reviewer owns the final outcome. A builder may be
		// overly conservative (for example, waiting for this very review).
		// Preserve its report, but do not let a self-assessment veto independently
		// verified completion. Explicit incomplete REVIEW outcomes still fail above.
		return validateBuildOutcome(report, builder.ReportVersion)
	}
	return errors.New("completed builder evidence unavailable")
}

// Legacy reports remain interpretable; explicit non-completion never promotes.
func (v Verdict) AcceptsWork() bool {
	return v.Approve != nil && *v.Approve && (v.Outcome == "" || v.Outcome == "completed")
}
