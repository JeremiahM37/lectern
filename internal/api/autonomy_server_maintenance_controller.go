package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/autonomy"
)

// Independent of active model assignments: completed reviewers cannot orphan
// pending effects. Exact wire bytes are strings so JSON persistence preserves
// the bytes hashed by the privileged runner.
type autoMaintenanceTransaction struct {
	Inspections            []*autoMaintenanceConflictInspection   `json:"inspections,omitempty"`
	InspectionRetryAt      time.Time                              `json:"inspection_retry_at,omitempty"`
	PreeffectReceipt       json.RawMessage                        `json:"preeffect_receipt,omitempty"`
	RetryAt                time.Time                              `json:"retry_at,omitempty"`
	InfrastructureFailures int                                    `json:"infrastructure_failures,omitempty"`
	ObservationHistory     []autoMaintenanceObservationAttempt    `json:"observation_history,omitempty"`
	ObservationRetryAt     time.Time                              `json:"observation_retry_at,omitempty"`
	PhaseRequests          map[string]string                      `json:"phase_requests"`
	ID                     string                                 `json:"id"`
	ReviewID               string                                 `json:"review_id"`
	Candidate              autoMaintenanceReviewedCandidate       `json:"candidate"`
	Provider               string                                 `json:"provider"`
	Generation             int                                    `json:"generation"`
	Phase                  string                                 `json:"phase"`
	State                  string                                 `json:"state"`
	Request                string                                 `json:"request"`
	Authority              string                                 `json:"authority,omitempty"`
	Binding                autoMaintenanceExecutionBinding        `json:"binding"`
	Started                bool                                   `json:"started"`
	Backup                 *autoMaintenanceBackup                 `json:"backup,omitempty"`
	Receipts               []json.RawMessage                      `json:"receipts,omitempty"`
	History                []autoMaintenanceTransactionGeneration `json:"history,omitempty"`
	StopRequested          bool                                   `json:"stop_requested,omitempty"`
	StopConfirmed          bool                                   `json:"stop_confirmed,omitempty"`
	StopReceipt            string                                 `json:"stop_receipt,omitempty"`
	Observation            *autoServerObservationRequest          `json:"observation,omitempty"`
	ObservationStarted     bool                                   `json:"observation_started,omitempty"`
	ObservationStopped     bool                                   `json:"observation_stopped,omitempty"`
	ObservationSequence    int                                    `json:"observation_sequence,omitempty"`
	ObservationReceipt     json.RawMessage                        `json:"observation_receipt,omitempty"`
	Polls                  int                                    `json:"polls"`
	Reason                 string                                 `json:"reason,omitempty"`
}
type autoMaintenanceObservationAttempt struct {
	Request     autoServerObservationRequest `json:"request"`
	Receipt     json.RawMessage              `json:"receipt,omitempty"`
	StopReceipt string                       `json:"stop_receipt"`
	Reason      string                       `json:"reason,omitempty"`
}
type autoMaintenanceTransactionGeneration struct {
	PreeffectReceipt   json.RawMessage                 `json:"preeffect_receipt,omitempty"`
	PhaseRequests      map[string]string               `json:"phase_requests"`
	StopReceipt        string                          `json:"stop_receipt"`
	Observation        *autoServerObservationRequest   `json:"observation,omitempty"`
	ObservationReceipt json.RawMessage                 `json:"observation_receipt,omitempty"`
	Generation         int                             `json:"generation"`
	Request            string                          `json:"request"`
	Authority          string                          `json:"authority,omitempty"`
	Binding            autoMaintenanceExecutionBinding `json:"binding"`
	Receipts           []json.RawMessage               `json:"receipts"`
}
type autoMaintenanceControllerIO struct {
	InspectWrite func(*autoMaintenanceTransaction, *autoMaintenanceConflictInspection) error
	Save         func() error
	Call         func(context.Context, ...string) ([]byte, error)
	Write        func(*autoMaintenanceTransaction) error
	Observe      func(context.Context, *autoMaintenanceTransaction) (*autoMaintenanceObservation, error)
}

func autoMaintenanceTransactionTerminal(t *autoMaintenanceTransaction) bool {
	switch t.State {
	case "verified", "restored", "stale", "unavailable", "superseded":
		return true
	}
	return false
}
func autoMaintenanceControllerGate(a *autoRecord, t *autoMaintenanceTransaction, enabled bool, now time.Time) (autoMaintenanceGate, error) {
	g := autoMaintenanceGate{Config: a.Config, Enabled: enabled && a.Config.Enabled, QuotaObservedAt: time.Time{}, RemainingPercent: 100}
	if !g.Enabled {
		return g, errors.New("maintenance autonomous mode off")
	}
	if e := autonomy.QuotaGate(a.Config, a.Quota.Providers, []string{t.Provider}, now); e != nil {
		return g, e
	}
	for _, p := range a.Quota.Providers {
		if p.ID == t.Provider {
			if p.UpdatedAt != nil {
				g.QuotaObservedAt = time.Unix(0, int64(*p.UpdatedAt*1e9))
			}
			for _, b := range p.Buckets {
				for _, w := range b.Windows {
					if w.RemainingPercent != nil && *w.RemainingPercent < g.RemainingPercent {
						g.RemainingPercent = *w.RemainingPercent
					}
				}
			}
		}
	}
	return g, autoMaintenanceQuota(g, now)
}
func autoQueueReviewedMaintenance(a *autoRecord) error {
	var queueErr error
	keys := make([]string, 0, len(a.MaintenanceReviews))
	for k := range a.MaintenanceReviews {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		c := a.MaintenanceReviews[key]
		if c == nil || c.State != "candidate_reviewed" {
			continue
		}
		id := autoMaintenanceOperationID(c.Admission.Pin)
		if old := a.MaintenanceTransactions[id]; old != nil {
			if old.ReviewID != key {
				queueErr = errors.Join(queueErr, fmt.Errorf("maintenance reviewed candidate %s duplicates immutable operation %s (%s); fresh progress-bound predecessor required", key, id, old.State))
			}
			continue
		}
		if c.Validation == nil || c.ValidationLease == nil || c.ValidationLease.State != "validated" || c.ValidationLease.Generation < 1 || autoSHA([]byte(c.Report)) != c.Review.ReportSHA {
			return errors.New("maintenance reviewed candidate evidence incomplete")
		}
		state, v, e := autoDecodeMaintenanceValidation(c.ValidationLease.ReceiptRaw, c.ValidationLease)
		if e != nil || state != "validated" || v == nil || v.ReceiptSHA != c.Validation.ReceiptSHA {
			return errors.New("maintenance reviewed validation changed")
		}
		latestReview := autoFindJob(a, c.Review.TaskID)
		if latestReview == nil || latestReview.ID != c.Review.JobID || latestReview.Status != "done" {
			continue
		}
		if e = autoValidateMaintenanceCandidateReview(&c.Admission, *v, c.Review); e != nil {
			return e
		}
		var j *autoJob
		for _, candidateJob := range a.Jobs {
			if candidateJob.ID == c.Admission.JobID && candidateJob.TaskID == c.Admission.TaskID {
				j = candidateJob
				break
			}
		}
		if j == nil || j.Provider == "" {
			return errors.New("maintenance admitted builder provider unavailable")
		}
		raw, b, e := autoBuildMaintenanceBackup(&c.Admission, c.ValidationLease.Generation)
		if e != nil {
			return e
		}
		var detached autoMaintenanceReviewedCandidate
		copyRaw, _ := json.Marshal(c)
		if e = json.Unmarshal(copyRaw, &detached); e != nil {
			return e
		}
		if a.MaintenanceTransactions == nil {
			a.MaintenanceTransactions = map[string]*autoMaintenanceTransaction{}
		}
		a.MaintenanceTransactions[id] = &autoMaintenanceTransaction{ID: id, ReviewID: key, Candidate: detached, Provider: j.Provider, Generation: b.Generation, Phase: "backup", State: "pending", Request: string(raw), Binding: b, PhaseRequests: map[string]string{"backup": string(raw)}}
	}
	return queueErr
}
func autoMaintenanceApplyResult(a *autoRecord, t *autoMaintenanceTransaction, r *autoMaintenanceExecutionResult) error {
	if r.State == "running" {
		return nil
	}
	if r.State == "waiting" {
		t.Started = false
		return nil
	}
	if len(t.Receipts) == 0 || autoMaintenanceReceiptID(t.Receipts[len(t.Receipts)-1]) != r.ReceiptSHA {
		t.Receipts = append(t.Receipts, append(json.RawMessage(nil), r.Raw...))
	}
	t.Reason = r.Reason
	var op *autoMaintenanceOperation
	if a.Maintenance != nil {
		op = a.Maintenance.Operations[t.ID]
	}
	switch r.State {
	case "offbox_restored_verified":
		if r.Backup == nil {
			return errors.New("maintenance backup proof missing")
		}
		t.Backup = r.Backup
		t.State = "observe"
		t.Started = false
	case "applied":
		if op == nil || r.Apply == nil || r.Health == nil {
			return errors.New("maintenance owned apply proof missing")
		}
		if e := autoRecordMaintenanceApply(op, *r.Apply); e != nil {
			return e
		}
		if e := autoFinishMaintenanceHealth(op, *r.Health); e != nil {
			return e
		}
		if op.State == "verified" {
			t.State = "verified"
		} else {
			t.Phase = "reconcile"
			t.Binding.Phase = "reconcile"
			t.State = "pending"
			t.Started = false
		}
	case "rolled_back":
		if op == nil || r.Compensation == nil {
			return errors.New("maintenance compensation proof missing")
		}
		if e := autoRecordMaintenanceCompensation(op, *r.Compensation); e != nil {
			return e
		}
		t.State = "restored"
	case "rollback_conflict":
		if op == nil {
			return errors.New("maintenance rollback conflict has no owned intent")
		}
		op.ConflictReceiptSHA = r.InnerReceiptSHA
		op.State = "conflict"
		op.Reason = r.Reason
		t.State = "conflict"
	case "reconciliation_required":
		if op == nil || op.State == "reserved" {
			return errors.New("maintenance reconciliation has no apply intent")
		}
		t.Phase = "reconcile"
		t.Binding.Phase = "reconcile"
		t.State = "pending"
		t.Started = false
	case "cancelled":
		t.StopRequested = true
		if op != nil && op.State != "reserved" && op.State != "cancelled" && !r.NoEffects {
			t.StopRequested = true
			t.Started = true
			return nil
		}
		if op != nil {
			autoRequestMaintenanceStop(op)
		}
		t.State = "cancelled"
	case "conflict":
		if op != nil && op.State != "reserved" && op.State != "apply_pending" {
			return errors.New("maintenance conflict after effects requires reconciliation")
		}
		if op != nil {
			op.State = "stale"
			op.Reason = r.Reason
		}
		t.State = "stale"
	case "unavailable":
		// Prerequisite/transport failure does not invalidate an audited
		// candidate. Get authoritative no-effect stop evidence before a new
		// transport generation, with bounded backoff and immutable history.
		// Existing journals instead resolve through their actual receipts.
		t.StopRequested = true
		t.StopConfirmed = false
		t.State = "pending"
		t.Started = true
		t.InfrastructureFailures++
		delay := 30 * time.Second
		for i := 1; i < t.InfrastructureFailures && delay < 3*time.Hour; i++ {
			delay *= 2
		}
		if delay > 6*time.Hour {
			delay = 6 * time.Hour
		}
		t.RetryAt = time.Now().Add(delay)

	default:
		return errors.New("maintenance terminal state unsupported")
	}
	if r.NoEffects && (r.State == "unavailable" || r.State == "cancelled") {
		if op == nil {
			return errors.New("journal no-effects receipt without owned apply intent")
		}
		autoRequestMaintenanceStop(op)
		if e := autoCancelMaintenanceBeforeEffects(op, r.InnerReceiptSHA, true); e != nil {
			return e
		}
		t.PreeffectReceipt = append(json.RawMessage(nil), r.Raw...)
		t.StopRequested = true
		t.StopConfirmed = false
		t.State = "cancelled"
	}

	return nil
}
func autoAdvanceMaintenance(ctx context.Context, a *autoRecord, t *autoMaintenanceTransaction, registry autoMaintenanceRegistry, enabled bool, now time.Time, io autoMaintenanceControllerIO) error {
	if t.State == "conflict" {
		return autoAdvanceMaintenanceConflict(ctx, a, t, registry, now, io)
	}
	if autoMaintenanceTransactionTerminal(t) {
		return nil
	}
	gate, gateErr := autoMaintenanceControllerGate(a, t, enabled, now)
	var op *autoMaintenanceOperation
	if a.Maintenance != nil {
		op = a.Maintenance.Operations[t.ID]
	}
	if gateErr != nil && !t.StopRequested {
		t.StopRequested = true
		if op != nil {
			autoRequestMaintenanceStop(op)
		}
		if e := io.Save(); e != nil {
			return e
		}
	}
	if t.StopRequested && t.Observation != nil && !t.ObservationStopped {
		o := t.Observation
		raw, e := io.Call(ctx, "server-observe-stop", "--job", o.OwnerJob, "--observation-id", o.RequestID)
		if e != nil {
			return e
		}
		var stopped struct {
			State   string `json:"state"`
			Owner   string `json:"owner_job"`
			Request string `json:"request_id"`
		}
		if json.Unmarshal(raw, &stopped) != nil || stopped.Owner != o.OwnerJob || stopped.Request != o.RequestID {
			return errors.New("maintenance observer stop binding invalid")
		}
		if stopped.State == "stopped" {
			t.ObservationStopped = true
		} else if stopped.State != "stopping" {
			return errors.New("maintenance observer stop state invalid")
		}
		if e = io.Save(); e != nil {
			return e
		}
		if !t.ObservationStopped {
			return nil
		}
	}

	if t.StopRequested && !t.StopConfirmed {
		raw, e := io.Call(ctx, "server-maintenance-stop", "--job", t.Binding.OwnerJob, "--operation-id", t.ID, "--generation", strconv.Itoa(t.Generation))
		if e != nil {
			return e
		}
		state, e := autoDecodeMaintenanceExecutionStop(raw, t.ID, t.Generation)
		if e != nil {
			return e
		}
		if state == "stopped" {
			t.StopReceipt = string(raw)
			t.StopConfirmed = true
			if op != nil {
				if e = autoCancelMaintenanceBeforeEffects(op, autoSHA(raw), true); e != nil {
					return e
				}
			}
			t.State = "cancelled"
		} else if state == "reconciliation_required" {
			if len(t.PreeffectReceipt) > 0 {
				proof, pe := autoDecodeMaintenanceExecution(t.PreeffectReceipt, t.Binding)
				var st struct {
					Journal string `json:"journal_state"`
				}
				json.Unmarshal(raw, &st)
				if pe != nil || !proof.NoEffects || (proof.State != "cancelled" && proof.State != "unavailable") || st.Journal != proof.State {
					return errors.New("maintenance preeffect journal stop differs")
				}
				t.StopConfirmed = true
				t.StopReceipt = string(raw)
				t.State = "cancelled"
			} else {
				if op == nil || op.State == "reserved" || op.State == "cancelled" {
					return errors.New("maintenance stop requires unknown effect reconciliation")
				}
				t.State = "pending"
				if t.Phase != "reconcile" {
					t.Started = true
				}
			}
		}

		if e = io.Save(); e != nil {
			return e
		}
		if state == "stopping" {
			return nil
		}
	}
	if t.State == "cancelled" {
		if gateErr != nil || !t.StopConfirmed || now.Before(t.RetryAt) {
			return nil
		}
		proofSHA, e := autoMaintenanceCancellationProof(t)
		if e != nil {
			return e
		}
		if op != nil {
			if e = autoResumeMaintenanceCancelled(op, proofSHA, registry, gate, now); e != nil {
				return e
			}
		}

		t.History = append(t.History, autoMaintenanceTransactionGeneration{PreeffectReceipt: t.PreeffectReceipt, Generation: t.Generation, Request: t.Request, Authority: t.Authority, Binding: t.Binding, Receipts: t.Receipts, PhaseRequests: t.PhaseRequests, StopReceipt: t.StopReceipt, Observation: t.Observation, ObservationReceipt: t.ObservationReceipt})
		t.Generation++
		t.Phase = "backup"
		raw, b, e := autoBuildMaintenanceBackup(&t.Candidate.Admission, t.Generation)
		if e != nil {
			return e
		}
		t.Request = string(raw)
		t.PhaseRequests = map[string]string{"backup": string(raw)}
		t.Binding = b
		t.Authority = ""
		t.Receipts = nil
		t.Backup = nil
		t.State = "retry_observe"
		t.Started = false
		t.StopRequested = false
		t.StopConfirmed = false
		t.StopReceipt = ""
		t.PreeffectReceipt = nil
		t.Observation = nil
		t.ObservationStarted = false
		t.ObservationStopped = false
		return io.Save()
	}
	if t.State == "retry_observe" {
		if gateErr != nil {
			return nil
		}
		obs, e := io.Observe(ctx, t)
		if e != nil {
			return e
		}
		if obs == nil {
			return nil
		}
		if registry.Digest != t.Candidate.Admission.Pin.RegistrySHA || obs.RegistrySHA != t.Candidate.Admission.Pin.RegistrySHA || obs.ConfigurationSHA != t.Candidate.Admission.Pin.BeforeSHA {
			t.State = "stale"
			t.Reason = "registered configuration changed before prerequisite retry; obtain fresh observation and audits"
			if op != nil {
				op.State = "stale"
				op.Reason = t.Reason
			}
		} else {
			t.State = "pending"
		}
		return io.Save()
	}

	if t.State == "observe" {
		if gateErr != nil {
			return io.Save()
		}
		if t.Backup == nil {
			return errors.New("maintenance verified backup missing")
		}
		authority, e := autoReviewedMaintenanceAuthority(&t.Candidate.Admission, *t.Candidate.Validation, t.Candidate.Review, *t.Backup)
		if e != nil {
			return e
		}
		if a.Maintenance == nil {
			a.Maintenance = &autoMaintenanceLedger{}
		}
		op, e = autoReserveMaintenance(a.Maintenance, authority, registry, gate, now)
		if e != nil {
			return e
		}
		if e = io.Save(); e != nil {
			return e
		}
		obs, e := io.Observe(ctx, t)
		if e != nil {
			return e
		}
		if obs == nil {
			return nil
		}
		now = time.Now()
		gate, e = autoMaintenanceControllerGate(a, t, enabled, now)
		if e != nil {
			return e
		}
		if e = autoBeginMaintenance(op, *obs, registry, gate, now); e != nil {
			if op.State == "stale" {
				t.State = "stale"
			}
			return errors.Join(e, io.Save())
		}
		raw, auth, b, e := autoBuildMaintenanceExecution(&t.Candidate.Admission, *t.Candidate.Validation, t.Candidate.Review, *t.Backup, t.Generation)
		if e != nil {
			return e
		}
		t.Request = string(raw)
		if t.PhaseRequests == nil {
			t.PhaseRequests = map[string]string{}
		}
		t.PhaseRequests["apply"] = string(raw)
		t.Authority = string(auth)
		t.Binding = b
		t.Phase = "apply"
		t.State = "pending"
		t.Started = false
		// Persist core apply intent and exact transport before publishing or launch.
		return io.Save()
	}
	if gateErr != nil && t.Phase == "backup" && !t.Started && !t.StopConfirmed {
		return nil
	}
	command := "server-maintenance-status"
	if !t.Started && !t.StopRequested {
		command = "server-maintenance-" + t.Phase
	}
	if t.Phase == "reconcile" && !t.Started {
		command = "server-maintenance-reconcile"
	}
	if command != "server-maintenance-status" {
		if e := io.Write(t); e != nil {
			return e
		}
	}
	args := []string{command, "--job", t.Binding.OwnerJob, "--operation-id", t.ID, "--generation", strconv.Itoa(t.Generation)}
	if command == "server-maintenance-status" {
		args = append(args, "--phase", t.Phase)
	}
	raw, e := io.Call(ctx, args...)
	if e != nil {
		return e
	}
	result, e := autoDecodeMaintenanceExecution(raw, t.Binding)
	if e != nil {
		return e
	}
	t.Started = true
	if e = autoMaintenanceApplyResult(a, t, result); e != nil {
		return e
	}
	return io.Save()
}

func autoWriteMaintenanceTransaction(root string, t *autoMaintenanceTransaction) error {
	if !autoExpertJobID.MatchString(t.Binding.OwnerJob) || !autoHash256(t.ID) || t.ID != t.Binding.OperationID || autoSHA([]byte(t.Request)) != t.Binding.RequestSHA {
		return errors.New("maintenance transport identity invalid")
	}
	if t.Phase == "reconcile" {
		return nil
	} // Runner reuses its frozen original apply inputs.
	dir := filepath.Join(root, t.Binding.OwnerJob, "server-maintenance")
	for _, p := range []string{filepath.Join(root, t.Binding.OwnerJob), dir} {
		if p == dir {
			if e := os.Mkdir(p, 0700); e != nil && !os.IsExist(e) {
				return e
			}
		}
		st, e := os.Lstat(p)
		if e != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
			return errors.New("unsafe maintenance request directory")
		}
	}
	write := func(name, raw string) error {
		target := filepath.Join(dir, name)
		old, e := autoReadRegular(target, 32<<10)
		if e == nil && string(old) == raw {
			return nil
		}
		replace := false
		if e == nil {
			if len(t.History) == 0 {
				return errors.New("immutable maintenance request differs")
			}
			h := t.History[len(t.History)-1]
			if h.Generation+1 != t.Generation {
				return errors.New("maintenance generation publication mismatch")
			}
			// A generation can be cancelled before reaching a phase; its
			// pointer then still belongs to an older retained generation.
			prior := ""
			for i := len(t.History) - 1; i >= 0; i-- {
				v := t.History[i].PhaseRequests[t.Phase]
				if name == t.ID+".authority.json" {
					v = t.History[i].Authority
				}
				if v != "" {
					prior = v
					break
				}
			}
			if prior == "" || string(old) != prior {
				return errors.New("maintenance generation publication mismatch")
			}

			replace = true
		} else if !os.IsNotExist(e) {
			return e
		}
		f, e := os.CreateTemp(dir, ".phase-")
		if e != nil {
			return e
		}
		defer os.Remove(f.Name())
		if _, e = f.WriteString(raw); e == nil {
			e = f.Sync()
		}
		ce := f.Close()
		if e != nil {
			return e
		}
		if ce != nil {
			return ce
		}
		if replace {
			e = os.Rename(f.Name(), target)
		} else {
			e = os.Link(f.Name(), target)
		}
		if e != nil {
			return e
		}
		d, e := os.Open(dir)
		if e != nil {
			return e
		}
		defer d.Close()
		return d.Sync()
	}
	if t.Authority != "" {
		if autoSHA([]byte(t.Authority)) != t.Binding.AuthoritySHA {
			return errors.New("maintenance authority changed")
		}
		if e := write(t.ID+".authority.json", t.Authority); e != nil {
			return e
		}
	}
	return write(t.ID+"."+t.Phase+".json", t.Request)
}

func (s *Server) pollAutoMaintenance(ctx context.Context, a *autoRecord, enabled bool) error {
	return s.pollAutoMaintenanceAt(ctx, autoRoot, a, enabled)
}
func (s *Server) pollAutoMaintenanceAt(ctx context.Context, root string, a *autoRecord, enabled bool) error {
	var queueErr error
	if enabled {
		queueErr = autoQueueReviewedMaintenance(a)
	}
	if e := s.saveAuto(a); e != nil {
		return e
	}
	transactions := []*autoMaintenanceTransaction{}
	for _, t := range a.MaintenanceTransactions {
		if !autoMaintenanceTransactionTerminal(t) {
			transactions = append(transactions, t)
		}
	}
	sort.Slice(transactions, func(i, j int) bool {
		if transactions[i].Polls != transactions[j].Polls {
			return transactions[i].Polls < transactions[j].Polls
		}
		return transactions[i].ID < transactions[j].ID
	})
	if len(transactions) > 4 {
		transactions = transactions[:4]
	}
	if len(transactions) == 0 {
		return queueErr
	}
	_, catalog, catalogErr := s.autoServerTargets(ctx)
	var e error
	result := queueErr
	call := func(ctx context.Context, args ...string) ([]byte, error) {
		c, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		return s.runAutoCommand(c, args...)
	}
	for _, t := range transactions {
		t.Polls++
		if e = s.saveAuto(a); e != nil {
			return e
		}
		registry, re := autoMaintenanceRegisteredPolicy(catalog, t.Candidate.Admission.Pin.TargetID, t.Candidate.Admission.Pin.ServiceID)
		// A catalog outage/change may forbid new effects, but cannot forbid stopping
		// or reconciling already-owned effects with frozen runner authority.
		permit := enabled && re == nil && catalogErr == nil
		io := autoMaintenanceControllerIO{Save: func() error { return s.saveAuto(a) }, Call: call, Write: func(v *autoMaintenanceTransaction) error { return autoWriteMaintenanceTransaction(root, v) }}
		io.InspectWrite = func(v *autoMaintenanceTransaction, l *autoMaintenanceConflictInspection) error {
			return autoWriteMaintenanceInspection(root, v, l)
		}
		io.Observe = func(c context.Context, v *autoMaintenanceTransaction) (*autoMaintenanceObservation, error) {
			return s.autoMaintenanceObserve(c, root, a, v, call)
		}
		if err := autoAdvanceMaintenance(ctx, a, t, registry, permit, time.Now(), io); err != nil {
			t.Reason = err.Error()
			result = errors.Join(result, err)
			if e = s.saveAuto(a); e != nil {
				return errors.Join(result, e)
			}
		}
	}
	return result
}
func autoMaintenanceTransactionsPending(a *autoRecord) bool {
	for _, t := range a.MaintenanceTransactions {
		if !autoMaintenanceTransactionTerminal(t) && t.State != "cancelled" {
			return true
		}
	}
	return false
}

func (s *Server) autoMaintenanceObserve(ctx context.Context, root string, a *autoRecord, t *autoMaintenanceTransaction, call func(context.Context, ...string) ([]byte, error)) (*autoMaintenanceObservation, error) {
	now := time.Now()
	if now.Before(t.ObservationRetryAt) {
		return nil, nil
	}
	pin := t.Candidate.Admission.Pin
	if t.Observation == nil {
		t.ObservationSequence++
		owner := autoUUID()
		req, e := autoNewServerObservation(&autoJob{ID: owner, TaskID: t.Candidate.Admission.TaskID}, pin.TargetID, pin.RegistrySHA, now)
		if e != nil {
			return nil, e
		}
		t.Observation = &req
		t.ObservationStarted = false
		t.ObservationStopped = false
		if e = s.saveAuto(a); e != nil {
			return nil, e
		}
	}
	req := *t.Observation
	path := filepath.Join(root, req.OwnerJob)
	if e := os.Mkdir(path, 0700); e != nil && !os.IsExist(e) {
		return nil, e
	}
	raw, e := autoWriteServerObservation(root, req)
	if e != nil {
		return nil, e
	}
	command := "server-observe-status"
	if !t.ObservationStarted {
		command = "server-observe"
	}
	out, e := call(ctx, command, "--job", req.OwnerJob, "--observation-id", req.RequestID)
	if e != nil {
		return nil, e
	}
	body, status, e := autoDecodeServerObservation(out, req, raw, now)
	if e != nil {
		return nil, e
	}
	t.ObservationStarted = true
	if e = s.saveAuto(a); e != nil {
		return nil, e
	}
	if status == 202 {
		return nil, nil
	}
	t.ObservationReceipt = append(json.RawMessage(nil), out...)
	observed, e := autoMaintenanceResourceFromObservation(body, pin.ServiceID)
	if e != nil {
		return nil, s.retireAutoMaintenanceObservation(ctx, a, t, call, e.Error())
	}
	if now.Sub(observed.CapturedAt) > 25*time.Second {
		return nil, s.retireAutoMaintenanceObservation(ctx, a, t, call, "observation expired before apply")
	}
	facts, _ := body["facts"].(map[string]any)
	services, _ := facts["services"].(map[string]any)
	value, _ := services[pin.ServiceID].(map[string]any)
	fields, _ := value["fields"].(map[string]any)
	number := func(k string) (int64, error) {
		switch v := fields[k].(type) {
		case float64:
			if v >= 0 && v == float64(int64(v)) {
				return int64(v), nil
			}
		case string:
			return strconv.ParseInt(v, 10, 64)
		}
		return 0, fmt.Errorf("maintenance observed %s unavailable", k)
	}
	rss, e := number("MemoryCurrent")
	if e != nil {
		return nil, e
	}
	tasks, e := number("TasksCurrent")
	if e != nil {
		return nil, e
	}
	inv, _ := fields["InvocationID"].(string)
	return &autoMaintenanceObservation{RegistrySHA: observed.RegistrySHA, ConfigurationSHA: observed.ConfigurationSHA, InvocationID: inv, RSSBytes: rss, CurrentTasks: int(tasks), CapturedAt: observed.CapturedAt}, s.saveAuto(a)
}

func (s *Server) retireAutoMaintenanceObservation(ctx context.Context, a *autoRecord, t *autoMaintenanceTransaction, call func(context.Context, ...string) ([]byte, error), reason string) error {
	req := t.Observation
	if req == nil {
		return nil
	}
	raw, e := call(ctx, "server-observe-stop", "--job", req.OwnerJob, "--observation-id", req.RequestID)
	if e != nil {
		return e
	}
	var stop struct {
		State   string `json:"state"`
		Owner   string `json:"owner_job"`
		Request string `json:"request_id"`
	}
	if json.Unmarshal(raw, &stop) != nil || stop.Owner != req.OwnerJob || stop.Request != req.RequestID {
		return errors.New("maintenance observation retirement binding invalid")
	}
	if stop.State == "stopping" {
		return nil
	}
	if stop.State != "stopped" {
		return errors.New("maintenance observation retirement unconfirmed")
	}
	t.ObservationHistory = append(t.ObservationHistory, autoMaintenanceObservationAttempt{*req, t.ObservationReceipt, string(raw), reason})
	t.Observation = nil
	t.ObservationReceipt = nil
	t.ObservationStarted = false
	t.ObservationStopped = false
	t.ObservationRetryAt = time.Now().Add(30 * time.Second)
	t.Reason = reason
	return s.saveAuto(a)
}

func autoMaintenanceReceiptID(raw []byte) string {
	var v struct {
		SHA string `json:"receipt_sha256"`
	}
	if json.Unmarshal(raw, &v) != nil {
		return ""
	}
	return v.SHA
}

func autoMaintenanceCancellationProof(t *autoMaintenanceTransaction) (string, error) {
	state, e := autoDecodeMaintenanceExecutionStop([]byte(t.StopReceipt), t.ID, t.Generation)
	if e != nil {
		return "", e
	}
	if len(t.PreeffectReceipt) == 0 {
		if state != "stopped" {
			return "", errors.New("maintenance no-effect stop missing")
		}
		return autoSHA([]byte(t.StopReceipt)), nil
	}
	r, e := autoDecodeMaintenanceExecution(t.PreeffectReceipt, t.Binding)
	if e != nil {
		return "", e
	}
	var st struct {
		Journal string `json:"journal_state"`
	}
	json.Unmarshal([]byte(t.StopReceipt), &st)
	if state != "reconciliation_required" || !r.NoEffects || (r.State != "cancelled" && r.State != "unavailable") || st.Journal != r.State {
		return "", errors.New("maintenance sealed no-effect journal mismatch")
	}
	return r.InnerReceiptSHA, nil
}
