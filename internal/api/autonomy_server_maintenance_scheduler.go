package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/autonomy"
)

// Enabled ticks revisit pre-effect cancellations with fresh quota. Disabled
// ticks use the narrower unfinished-stop predicate.
func autoMaintenanceWorkPending(a *autoRecord) bool {
	for _, t := range a.MaintenanceTransactions {
		if t != nil && !autoMaintenanceTransactionTerminal(t) {
			return true
		}
	}
	return false
}

// A failed read must not leave yesterday's admissible sample in the scheduler.
// Maintenance and models consume this same single freshly fetched response,
// applying their own provider gate independently.
func refreshAutoQuota(ctx context.Context, a *autoRecord) error {
	a.Quota = autonomy.Usage{}
	url := "http://127.0.0.1:9105/api/agent-usage"
	if a.State != nil && a.State.Phase != autonomy.Paused {
		url += "?autonomous=1"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := (&http.Client{Timeout: 25 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("usage returned %d", resp.StatusCode)
	}
	var usage autonomy.Usage
	if err = json.NewDecoder(io.LimitReader(resp.Body, 256<<10)).Decode(&usage); err != nil {
		return err
	}
	a.Quota = usage
	return nil
}

type autoMaintenanceSchedulerStatus struct {
	Pending           int            `json:"pending"`
	ValidationCleanup int            `json:"validation_cleanup"`
	Phases            map[string]int `json:"phases,omitempty"`
	Reason            string         `json:"reason,omitempty"`
}

func autoEndedMaintenanceValidation(a *autoRecord, l *autoMaintenanceValidationLease) bool {
	if l == nil || l.StopConfirmed || len(l.ReceiptRaw) > 0 {
		return false
	}
	j := autoFindJob(a, l.OwnerTask)
	if j == nil {
		return true
	}
	// Same-task report correction owns the original lease; preserve it while the
	// replacement reviewer is preparing/running, rather than cancelling by UUID.
	switch j.Status {
	case "done", "failed", "stopped":
		return true
	default:
		return false
	}
}

func autoProjectMaintenanceStatus(a *autoRecord, err error) {
	p := &autoMaintenanceSchedulerStatus{Phases: map[string]int{}}
	for _, t := range a.MaintenanceTransactions {
		if t != nil && !autoMaintenanceTransactionTerminal(t) {
			p.Pending++
			p.Phases[t.Phase]++
		}
	}
	for _, l := range a.MaintenanceValidations {
		if autoEndedMaintenanceValidation(a, l) {
			p.ValidationCleanup++
		}
	}
	if err != nil {
		p.Reason = clipEnd(err.Error(), 1200)
	}
	a.MaintenanceStatus = nil
	if p.Pending+p.ValidationCleanup == 0 && p.Reason == "" {
		return
	}
	a.MaintenanceStatus = p
	if a.Config.Enabled && a.State != nil && a.State.Phase == autonomy.Complete && a.Status != "paused" && a.Status != "error" {
		a.Status = "maintenance_pending"
		a.Reason = fmt.Sprintf("Model checkpoint complete; %d maintenance operations and %d validation cleanups pending", p.Pending, p.ValidationCleanup)
		if p.Reason != "" {
			a.Reason += ": " + p.Reason
		}
	}
}

// An ended reviewer can no longer poll its HTTP bridge. Preserve a finished
// authentic receipt, or cancel its owned unfinished validation. Never launch
// from here, and never disturb a current same-task reviewer.
func (s *Server) reconcileEndedMaintenanceValidations(ctx context.Context, a *autoRecord) error {
	ids := []string{}
	for id, l := range a.MaintenanceValidations {
		if autoEndedMaintenanceValidation(a, l) {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	// Rotate across ticks so a few broken collectors cannot starve later leases.
	if len(ids) > 0 {
		n := sort.SearchStrings(ids, a.MaintenanceValidationCursor)
		if n < len(ids) && ids[n] == a.MaintenanceValidationCursor {
			n++
		}
		ids = append(append([]string(nil), ids[n:]...), ids[:n]...)
	}
	var failures []error
	for n, id := range ids {
		if n == 4 {
			break
		}
		a.MaintenanceValidationCursor = id
		l := a.MaintenanceValidations[id]
		c, cancel := context.WithTimeout(ctx, 5*time.Second)
		raw, err := s.runAutoCommand(c, "server-maintenance-status", "--job", l.OwnerJob, "--operation-id", l.OperationID, "--generation", strconv.Itoa(l.Generation), "--phase", "validate")
		cancel()
		if err == nil {
			state, v, decodeErr := autoDecodeMaintenanceValidation(raw, l)
			if decodeErr == nil && state != "running" && state != "waiting" && state != "pending" {
				l.State = state
				l.Validation = v
				l.ReceiptRaw = append(json.RawMessage(nil), raw...)
				l.StopConfirmed = true
				continue
			}
		}
		// Even missing/corrupt status cannot block cancellation of our exact lease.
		l.StopRequested = true
		if err = s.saveAuto(a); err != nil {
			return errors.Join(append(failures, err)...)
		}
		c, cancel = context.WithTimeout(ctx, 5*time.Second)
		raw, err = s.runAutoCommand(c, "server-maintenance-stop", "--job", l.OwnerJob, "--operation-id", l.OperationID, "--generation", strconv.Itoa(l.Generation))
		cancel()
		if err != nil {
			failures = append(failures, err)
			continue
		}
		stopState, stopErr := autoDecodeMaintenanceExecutionStop(raw, l.OperationID, l.Generation)
		if stopErr != nil || stopState != "stopped" {
			failures = append(failures, errors.New("ended reviewer validation cleanup pending"))
			continue
		}
		l.StopConfirmed = true
	}
	return errors.Join(failures...)
}
