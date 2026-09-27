package api

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"time"
)

// Reconciliation only reads already reserved checks. It must work without a
// surviving reviewer process and must never launch or retry an execution.
func (s *Server) reconcileAutoPrivateTests(ctx context.Context, a *autoRecord) error {
	if a.PrivateIntegration == nil {
		return nil
	}
	type pending struct {
		attempt *autoPrivateIntegrationAttempt
		lease   *autoPrivateTestLease
	}
	checks := []pending{}
	for _, run := range a.PrivateIntegration.Runs {
		for _, attempt := range run.Attempts {
			for _, lease := range attempt.TestLeases {
				if lease.Receipt == nil {
					checks = append(checks, pending{attempt, lease})
				}
			}
		}
	}
	sort.Slice(checks, func(i, j int) bool {
		if checks[i].lease.ObservationAttempts != checks[j].lease.ObservationAttempts {
			return checks[i].lease.ObservationAttempts < checks[j].lease.ObservationAttempts
		}
		return checks[i].lease.ID < checks[j].lease.ID
	})
	if len(checks) > 4 {
		checks = checks[:4]
	}
	var failures []error
	for _, check := range checks {
		if err := ctx.Err(); err != nil {
			return err
		}
		v, l := check.attempt, check.lease
		l.ObservationAttempts++
		// Persist fairness before observation so a crashing runner cannot starve
		// all later checks after controller restart.
		if err := s.saveAuto(a); err != nil {
			return err
		}
		poll, cancel := context.WithTimeout(ctx, 2*time.Second)
		raw, err := s.runAutoCommand(poll, "integration-test-status", "--job", l.OwnerJob, "--integration-id", v.ID, "--check-id", l.ID, "--generation", strconv.Itoa(v.Generation))
		cancel()
		if err != nil {
			failures = append(failures, err)
			continue
		}
		var envelope struct {
			ID         string `json:"integration_id"`
			Check      string `json:"check_id"`
			Request    string `json:"request_sha256"`
			Generation int    `json:"generation"`
			Execution  int    `json:"execution_generation"`
			State      string `json:"state"`
		}
		if len(raw) > 256<<10 || json.Unmarshal(raw, &envelope) != nil || envelope.ID != v.ID || envelope.Check != l.ID || envelope.Request != l.RequestSHA || envelope.Generation != v.Generation {
			failures = append(failures, errors.New("private test reconciliation observation binding mismatch"))
			continue
		}
		switch envelope.State {
		case "exited", "timeout", "output_limit", "cancelled", "interrupted", "unavailable", "failed", "rejected":
		default:
			continue
		}
		receipt, err := autoDecodeBoundPrivateTestReceipt(raw, v.ID, l, v.Generation)
		if err != nil {
			failures = append(failures, errors.New("private test reconciliation execution accounting invalid"))
			continue
		}
		if err = autoRecordPrivateTest(a, receipt); err != nil {
			failures = append(failures, err)
			continue
		}
		if err = s.saveAuto(a); err != nil {
			return err
		}
	}
	return errors.Join(failures...)
}
