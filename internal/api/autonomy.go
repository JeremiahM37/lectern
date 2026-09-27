package api

// The daily experiment is a separate controller, not a privileged agent loop.
// Its task rows are receipts; only the constrained OS runner executes them.
import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/agents"
	"github.com/JeremiahM37/lectern/v2/internal/auth"
	"github.com/JeremiahM37/lectern/v2/internal/autonomy"
	"github.com/JeremiahM37/lectern/v2/internal/memory"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

const autoKey = "autonomous_experiment_v1"
const autoOwner = "autonomous-experiment"
const autoRoot = "/mnt/bulk/lectern-autonomy/jobs"
const autoRunner = "/usr/local/libexec/lectern-autonomy-runner"

type autoJob struct {
	NodeGeneration         int              `json:"node_generation,omitempty"`
	NodeStopRequested      bool             `json:"node_stop_requested,omitempty"`
	NodeRequest            *autoNodeRequest `json:"node_request,omitempty"`
	PendingNodeRequest     *autoNodeRequest `json:"pending_node_request,omitempty"`
	NodeRecovery           *autoNodeReceipt `json:"node_recovery,omitempty"`
	NodeStopped            bool             `json:"node_stopped,omitempty"`
	NodeUsedBundle         string           `json:"node_used_bundle,omitempty"`
	NodePreviousBundle     string           `json:"node_previous_bundle,omitempty"`
	NodeNeedsResume        bool             `json:"node_needs_resume,omitempty"`
	NodeNeedsChange        bool             `json:"node_needs_change,omitempty"`
	NodeExpectedInput      string           `json:"node_expected_input,omitempty"`
	NodeExpectedBundle     string           `json:"node_expected_bundle,omitempty"`
	NodeExpectedLock       string           `json:"node_expected_lock,omitempty"`
	WorkerRecoveryPrepared bool             `json:"worker_recovery_prepared,omitempty"`
	WorkerFailures         int              `json:"worker_failures,omitempty"`
	WorkerFailureRecorded  bool             `json:"worker_failure_recorded,omitempty"`
	WorkerRecoveryAt       time.Time        `json:"worker_recovery_at,omitempty"`
	WorkerRecoveryArchive  string           `json:"worker_recovery_archive_sha256,omitempty"`
	WorkerRecoveryReason   string           `json:"worker_recovery_reason,omitempty"`

	MaintenancePin            string                     `json:"maintenance_pin,omitempty"`
	MaintenanceAdmission      *autoMaintenanceAdmitted   `json:"maintenance_admission,omitempty"`
	PythonTestNeedsResume     bool                       `json:"python_test_needs_resume,omitempty"`
	PythonExpectedTestKey     string                     `json:"python_expected_test_key,omitempty"`
	PythonTestRecovery        *autoPrivateToolingReceipt `json:"python_test_recovery,omitempty"`
	PrivateOperations         []*autoPrivateOperation    `json:"private_operations,omitempty"`
	PrivateIntegrationRoot    string                     `json:"private_integration_root,omitempty"`
	PrivateIntegrationAttempt int                        `json:"private_integration_attempt,omitempty"`
	ExpertRecoveryRoot        int64                      `json:"expert_recovery_root,omitempty"`
	ExpertRecoveryAttempt     int                        `json:"expert_recovery_attempt,omitempty"`
	DiagnosisReservation      string                     `json:"diagnosis_reservation,omitempty"`
	DiagnosisRequirement      string                     `json:"diagnosis_requirement,omitempty"`
	PythonUsedBundle          string                     `json:"python_used_bundle,omitempty"`
	PythonExpectedInput       string                     `json:"python_expected_input,omitempty"`
	PythonExpectedBundle      string                     `json:"python_expected_bundle,omitempty"`
	RequirementHold           bool                       `json:"requirement_hold,omitempty"`
	RequirementIDs            []string                   `json:"requirement_ids,omitempty"`
	PythonRequest             *autoPythonRequest         `json:"python_request,omitempty"`
	PendingPythonRequest      *autoPythonRequest         `json:"pending_python_request,omitempty"`
	PythonRecovery            *autoPythonReceipt         `json:"python_recovery,omitempty"`
	PythonStopped             bool                       `json:"python_stopped,omitempty"`
	PythonPreviousBundle      string                     `json:"python_previous_bundle,omitempty"`
	PythonNeedsChange         bool                       `json:"python_needs_change,omitempty"`
	DocumentationStopped      bool                       `json:"documentation_stopped,omitempty"`
	DocumentationCopies       []autoDocumentationCopy    `json:"documentation_copies,omitempty"`
	DocumentationRoot         int64                      `json:"documentation_root,omitempty"`
	Documentation             *autoDocumentationReceipt  `json:"documentation,omitempty"`
	// A paid runner-start cooldown belongs only to this interrupted launch.
	LaunchRetryPaid     bool                 `json:"launch_retry_paid,omitempty"`
	RecoveryCheckAt     time.Time            `json:"recovery_check_at,omitempty"`
	Recovery            *autoRecoveryReceipt `json:"recovery,omitempty"`
	ReviewOutcome       string               `json:"review_outcome,omitempty"`
	Admission           *autoAdmission       `json:"admission,omitempty"`
	ReportError         string               `json:"report_error,omitempty"`
	ReportRepairs       int                  `json:"report_repairs,omitempty"`
	ReportRetryAt       time.Time            `json:"report_retry_at,omitempty"`
	ID                  string               `json:"id"`
	TaskID              int64                `json:"task_id"`
	Role                string               `json:"role"`
	Model               string               `json:"model,omitempty"`
	Provider            string               `json:"provider"`
	Status              string               `json:"status"`
	ArtifactPath        string               `json:"artifact_path"`
	Summary             string               `json:"summary,omitempty"`
	StartedAt           time.Time            `json:"started_at"`
	Rejected            bool                 `json:"rejected,omitempty"`
	ReviewReason        string               `json:"review_reason,omitempty"`
	RepairAttemptTaskID int64                `json:"repair_attempt_task_id,omitempty"`
	RepairSourceTaskID  int64                `json:"repair_source_task_id,omitempty"`
	Approved            bool                 `json:"approved,omitempty"`
	ReviewTaskID        int64                `json:"review_task_id,omitempty"`
}
type autoRecord struct {
	MaintenanceValidationCursor string                                       `json:"maintenance_validation_cursor,omitempty"`
	MaintenanceStatus           *autoMaintenanceSchedulerStatus              `json:"maintenance_status,omitempty"`
	MaintenanceTransactions     map[string]*autoMaintenanceTransaction       `json:"maintenance_transactions,omitempty"`
	MaintenanceReviews          map[string]*autoMaintenanceReviewedCandidate `json:"maintenance_reviews,omitempty"`
	MaintenanceValidations      map[string]*autoMaintenanceValidationLease   `json:"maintenance_validations,omitempty"`
	MaintenancePins             map[string]*autoMaintenancePlanPin           `json:"maintenance_pins,omitempty"`
	Maintenance                 *autoMaintenanceLedger                       `json:"maintenance,omitempty"`
	PrivateIntegration          *autoPrivateIntegrationLedger                `json:"private_integration,omitempty"`
	ExpertRecovery              *autoExpertRecoveryLedger                    `json:"expert_recovery,omitempty"`
	HistoricalReportPending     map[string]bool                              `json:"historical_report_pending,omitempty"`
	EnvironmentPins             map[int64]*autoVerifiedDiagnosisEnvironment  `json:"environment_pins,omitempty"`
	HeldRuns                    []*autonomy.State                            `json:"held_runs,omitempty"`
	RequirementDiagnoses        map[string]*autoRequirementDiagnosis         `json:"requirement_diagnoses,omitempty"`
	Requirements                map[string]*autoRequirement                  `json:"requirements,omitempty"`
	DocumentationPins           map[int64]*autoDocumentationReservation      `json:"documentation_pins,omitempty"`
	DocumentationReservations   map[int64]*autoDocumentationReservation      `json:"documentation_reservations,omitempty"`
	DeferredRuns                []*autonomy.State                            `json:"deferred_runs,omitempty"`
	CycleSequence               int                                          `json:"cycle_sequence,omitempty"`
	Config                      autonomy.Config                              `json:"config"`
	State                       *autonomy.State                              `json:"state"`
	Runs                        []*autonomy.State                            `json:"runs"`
	Jobs                        []*autoJob                                   `json:"jobs"`
	Status                      string                                       `json:"status"`
	Reason                      string                                       `json:"reason"`
	Quota                       autonomy.Usage                               `json:"quota"`
	RequestedDay                string                                       `json:"requested_day,omitempty"`
	ProjectID                   int64                                        `json:"project_id"`
	RememberedDay               string                                       `json:"remembered_day"`
	RetryCount                  int                                          `json:"retry_count"`
	RetryScope                  string                                       `json:"retry_scope,omitempty"`
	RetryDay                    string                                       `json:"retry_day,omitempty"`
	RetryAt                     time.Time                                    `json:"retry_at,omitempty"`
	NextCycleAt                 time.Time                                    `json:"next_cycle_at,omitempty"`
	NextCycleScheduled          bool                                         `json:"next_cycle_scheduled"`
	RememberedCycle             string                                       `json:"remembered_cycle,omitempty"`
	StrategyDay                 string                                       `json:"strategy_day,omitempty"`
}

func (s *Server) loadAuto() (*autoRecord, error) {
	a := &autoRecord{Config: autonomy.DefaultConfig(), Status: "off", Runs: []*autonomy.State{}, Jobs: []*autoJob{}}
	if raw := s.DB.Setting(autoKey); raw != "" {
		if err := json.Unmarshal([]byte(raw), a); err != nil {
			return nil, fmt.Errorf("autonomy state unreadable: %w", err)
		}
	}
	return a, a.Config.Validate()
}
func (s *Server) saveAuto(a *autoRecord) error { return s.DB.SetSetting(autoKey, store.J(a)) }
func (s *Server) getAutonomy(w http.ResponseWriter, r *http.Request) {
	// Settings are atomic JSON snapshots; readers must not wait for worker I/O.
	a, e := s.loadAuto()
	if e != nil {
		respondErr(w, e)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, a)
}
func (s *Server) autonomyHuman(w http.ResponseWriter, r *http.Request) bool {
	p, _ := auth.FromContext(r.Context())
	if s.Auth == nil || !s.Auth.CanDecide(p) {
		httpError(w, 403, "autonomous mode requires your signed-in identity; agents cannot enable it")
		return false
	}
	return true
}
func (s *Server) putAutonomy(w http.ResponseWriter, r *http.Request) {
	if !s.autonomyHuman(w, r) {
		return
	}
	var in struct {
		Enabled *bool `json:"enabled"`
	}
	if e := decodeBody(r, &in); e != nil || in.Enabled == nil {
		httpError(w, 400, "enabled boolean required")
		return
	}
	s.autoMu.Lock()
	defer s.autoMu.Unlock()
	a, e := s.loadAuto()
	if e != nil {
		respondErr(w, e)
		return
	}
	if *in.Enabled {
		if _, e = s.runAutoCommand(r.Context(), "probe"); e != nil {
			httpError(w, 503, "isolation preflight failed: %s", e)
			return
		}
		a.Config.Enabled = true
		a.Status = "waiting"
		a.Reason = "Continuous work enabled; independent review between milestones"
	} else {
		a.Config.Enabled = false
		if e = s.saveAuto(a); e != nil {
			respondErr(w, e)
			return
		}
		s.stopAutoJobs(r.Context(), a, "Turned off by you")
	}
	if e = s.saveAuto(a); e != nil {
		respondErr(w, e)
		return
	}
	writeJSON(w, 200, a)
}
func (s *Server) startAutonomy(w http.ResponseWriter, r *http.Request) {
	if !s.autonomyHuman(w, r) {
		return
	}
	s.autoMu.Lock()
	defer s.autoMu.Unlock()
	a, e := s.loadAuto()
	if e != nil {
		respondErr(w, e)
		return
	}
	if !a.Config.Enabled {
		httpError(w, 409, "turn autonomous mode on first")
		return
	}
	loc, _ := time.LoadLocation(a.Config.Timezone)
	a.RequestedDay = time.Now().In(loc).Format("2006-01-02")
	if !a.Config.Continuous && a.State != nil && a.State.Date == a.RequestedDay && a.State.Phase == autonomy.Complete {
		httpError(w, 409, "today's cycle is complete; the next plan is tomorrow")
		return
	}
	if a.State != nil && a.State.Phase == autonomy.Paused {
		a.State.Reason = "Retry requested by you"
		for _, id := range a.State.ActiveTaskIDs() {
			if j := autoFindJob(a, id); j != nil && j.Status == "failed" {
				j.Status = "stopped"
			}
		}
	}
	if e = s.saveAuto(a); e != nil {
		respondErr(w, e)
		return
	}
	writeJSON(w, 200, a)
}
func (s *Server) stopAutonomy(w http.ResponseWriter, r *http.Request) {
	if !s.autonomyHuman(w, r) {
		return
	}
	s.autoMu.Lock()
	defer s.autoMu.Unlock()
	a, e := s.loadAuto()
	if e != nil {
		respondErr(w, e)
		return
	}
	a.Config.Enabled = false
	if e = s.saveAuto(a); e != nil {
		respondErr(w, e)
		return
	}
	s.stopAutoJobs(r.Context(), a, "Stopped by you; artifacts retained")
	if e = s.saveAuto(a); e != nil {
		respondErr(w, e)
		return
	}
	writeJSON(w, 200, a)
}
func (s *Server) runAutoCommand(ctx context.Context, args ...string) ([]byte, error) {
	c, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	// Fixed root-owned runner; no model-controlled command or path is executed here.
	out, e := exec.CommandContext(c, "sudo", append([]string{"-n", autoRunner}, args...)...).CombinedOutput()
	if e != nil {
		return nil, fmt.Errorf("runner %s: %s", args[0], clipEnd(string(out), 600))
	}
	return out, nil
}
func (s *Server) stopAutoJobs(ctx context.Context, a *autoRecord, reason string) {
	s.stopAutoJobsScoped(ctx, a, reason, true)
}

// Model eligibility is independent of an already admitted maintenance provider.
// Explicit OFF, persistence failures and global cancellation retain the full scope.
func (s *Server) stopAutoJobsScoped(ctx context.Context, a *autoRecord, reason string, maintenance bool) {
	var stopErrors []string
	if maintenance {
		if err := s.pollAutoMaintenance(ctx, a, false); err != nil {
			stopErrors = append(stopErrors, "Maintenance recovery: "+err.Error())
		}
	}
	if err := s.stopAutoMaintenanceValidations(ctx, a); err != nil {
		stopErrors = append(stopErrors, err.Error())
	}
	if err := s.stopAutoPrivate(ctx, a); err != nil {
		stopErrors = append(stopErrors, err.Error())
	}
	if err := s.stopAutoExpertProbes(ctx, a); err != nil {
		stopErrors = append(stopErrors, err.Error())
	}
	for id := range a.HistoricalReportPending {
		if _, err := s.runAutoCommand(ctx, "archive-report-stop", "--job", id); err != nil {
			stopErrors = append(stopErrors, "Historical report stop: "+err.Error())
		} else {
			delete(a.HistoricalReportPending, id)
		}
	}
	for _, j := range a.Jobs {
		if autoNodePending(j) {
			if err := s.stopAutoNode(ctx, a, j); err != nil {
				stopErrors = append(stopErrors, "Node stop: "+err.Error())
			}
		}
		if autoPythonPending(j) {
			if _, err := s.runAutoCommand(ctx, "python-dependencies-stop", "--job", j.ID); err != nil {
				stopErrors = append(stopErrors, "Python stop: "+err.Error())
			} else {
				j.PythonStopped = true
			}
		}
		if autoDocumentationStopPending(j) {
			if err := s.stopAutoProgressCopies(ctx, a, j); err != nil {
				stopErrors = append(stopErrors, "Evidence copy stop: "+err.Error())
			}
		}
		if (j.Status == "prepared" || j.Status == "deferred") && j.Recovery != nil {
			if _, err := s.runAutoCommand(ctx, "dependencies-stop", "--job", j.ID); err != nil {
				stopErrors = append(stopErrors, err.Error())
				continue
			}
			s.closeAutoBridge(j.ID)
			if j.Status == "deferred" {
				j.Recovery.State = "waiting"
			} else {
				j.Recovery = nil
			}
		}
		if j.Status == "running" || j.Status == "starting" {
			if _, e := s.runAutoCommand(ctx, "stop", "--job", j.ID); e != nil {
				stopErrors = append(stopErrors, e.Error())
				continue
			}
			if j.ExpertRecoveryAttempt > 0 || j.PrivateIntegrationAttempt > 0 {
				raw, err := s.runAutoCommand(ctx, "status", "--job", j.ID)
				if err != nil {
					stopErrors = append(stopErrors, err.Error())
					continue
				}
				if err = autoProgressCharge(a, j, raw); err != nil {
					left, e := autoProgressRemaining(a, j)
					if e != nil || left > 0 {
						stopErrors = append(stopErrors, err.Error())
						continue
					}
				}
			}
			s.closeAutoBridge(j.ID)
			j.Status = "stopped"
			if e := s.snapshotAutoJob(ctx, j); e != nil {
				j.Summary = "Artifact backup pending: " + e.Error()
			}
			_ = s.DB.Update("tasks", j.TaskID, map[string]any{"status": "backlog"})
		}
	}
	if a.State != nil {
		a.State.Pause(reason)
	}
	a.Reason = reason
	a.Status = "paused"
	if !a.Config.Enabled {
		a.Status = "off"
	}
	if len(stopErrors) > 0 {
		a.Status = "error"
		a.Reason = "Stop needs attention: " + strings.Join(stopErrors, "; ")
	}
}

// RunAutonomyTick is called by the scheduler. It owns only its persisted job
// UUIDs, never interactive sessions or arbitrary tmux/process IDs.

func (s *Server) RunAutonomyTick(ctx context.Context) {
	if !s.autoMu.TryLock() {
		return
	}
	defer s.autoMu.Unlock()
	if time.Since(s.autoChecked) < 20*time.Second {
		return
	}
	s.autoChecked = time.Now()
	a, e := s.loadAuto()
	if e != nil {
		s.Log.Error("autonomy state", "err", e)
		return
	}
	var maintenanceErr error
	if !a.Config.Enabled { // Retry failed stops even while disabled.
		defer func() { autoProjectMaintenanceStatus(a, maintenanceErr); _ = s.saveAuto(a) }()
		if err := s.reconcileAutoPrivateTests(ctx, a); err != nil {
			a.Reason = "Private test observation pending: " + err.Error()
		}
		if len(a.HistoricalReportPending) > 0 || autoProgressPendingProbes(a) || autoPrivatePending(a) || autoMaintenanceValidationStopPending(a) || autoMaintenanceTransactionsPending(a) {
			s.stopAutoJobs(ctx, a, "Autonomous mode is off")
			_ = s.saveAuto(a)
			return
		}
		for _, j := range a.Jobs {
			if autoNodePending(j) || autoPythonPending(j) || autoDocumentationStopPending(j) || j.Status == "running" || j.Status == "starting" || ((j.Status == "prepared" || (j.Status == "deferred" && j.Recovery != nil && j.Recovery.State == "recovering")) && j.Recovery != nil) {
				s.stopAutoJobs(ctx, a, "Autonomous mode is off")
				_ = s.saveAuto(a)
				break
			}
		}
		return
	}
	defer func() {
		autoProjectMaintenanceStatus(a, maintenanceErr)
		if e := s.saveAuto(a); e != nil {
			s.stopAutoJobs(ctx, a, "State persistence failed")
			s.Log.Error("autonomy persistence", "err", e)
		}
	}()
	maintenanceErr = autoQueueReviewedMaintenance(a)
	maintenanceErr = errors.Join(maintenanceErr, s.reconcileEndedMaintenanceValidations(ctx, a))
	quotaFetched := false
	var quotaErr error
	if autoMaintenanceWorkPending(a) {
		quotaFetched = true
		quotaErr = refreshAutoQuota(ctx, a)
		// Even unavailable quota must reach the owned rollback/stop reconciler.
		maintenanceErr = errors.Join(maintenanceErr, s.pollAutoMaintenance(ctx, a, quotaErr == nil))
	}
	now := time.Now()
	if a.State != nil && a.State.Phase == autonomy.Complete {
		s.rememberAuto(ctx, a)
	}
	if autoCycleDue(a, now) {
		if e = s.archiveAutoCycle(a); e != nil {
			a.Status = "paused"
			a.Reason = "Cycle archive unavailable: " + e.Error()
			return
		}
		if !autoResumeRecovered(a) {
			autoNewCycle(a, now)
		}
	}
	if a.State == nil || (a.State.Phase == autonomy.Complete && len(a.DeferredRuns) == 0 && len(a.HeldRuns) == 0 && !autoPrivatePublishPending(a) && !autoMaintenanceWorkPending(a)) {
		return
	}
	if !quotaFetched {
		quotaErr = refreshAutoQuota(ctx, a)
	}
	requiredProvider := ""
	e = quotaErr
	if e == nil {
		now = time.Now()
		requiredProvider, e = s.autoQuotaProvider(a, now)
	}
	if e != nil {
		s.stopAutoJobsScoped(ctx, a, "Budget pause: "+e.Error(), false)
		return
	}
	if err := s.reconcileAutoPrivateTests(ctx, a); err != nil {
		a.Reason = "Private test observation pending: " + err.Error()
	}
	if err := s.pollAutoPrivatePublications(ctx, a); err != nil {
		a.Reason = "Private publication pending: " + err.Error()
	}
	s.pollDeferredPrerequisites(ctx, a, now)
	if a.State.Phase == autonomy.Complete {
		return
	}
	if a.State.Phase == autonomy.Paused {
		if s.recoverLegacyArtifactTimeout(ctx, a, requiredProvider, now) {
			return
		}
		if s.recoverRejectedContinuation(a) {
			return
		}
		if s.recoverAutoReport(ctx, a, now) {
			return
		}
		if strings.HasPrefix(a.State.Reason, "Budget pause:") || strings.Contains(a.State.Reason, "by you") || a.State.Reason == "Autonomous mode is off" {
			if e = a.State.Resume(a.Config, a.Quota.Providers, []string{requiredProvider}, now); e != nil {
				return
			}
		} else {
			// Storage can recover independently of an exhausted daily retry
			// budget. Probe real capacity; never bypass quota or safety gates.
			if autoStoragePause(a.State.Reason) {
				out, probeErr := s.runAutoCommand(ctx, "storage")
				var capacity struct {
					Ready bool `json:"ready"`
				}
				if probeErr == nil && json.Unmarshal(out, &capacity) == nil && capacity.Ready {
					a.RetryAt = now
				}
			}
			launchRetry := strings.HasPrefix(a.State.Reason, "runner start:")
			if !autoRetryReady(a, now) {
				return
			}
			if e = a.State.Resume(a.Config, a.Quota.Providers, []string{requiredProvider}, now); e != nil {
				return
			}
			for _, id := range a.State.ActiveTaskIDs() {
				if j := autoFindJob(a, id); j != nil {
					if launchRetry && j.Status == "starting" {
						j.LaunchRetryPaid = true
					}
					if j.Status == "failed" {
						j.Status = "stopped"
					}
				}
			}
		}
	}
	a.Status = string(a.State.Phase)
	a.Reason = ""
	for _, id := range a.State.ActiveTaskIDs() {
		j := autoFindJob(a, id)
		if j == nil {
			a.State.Pause("Missing job receipt; manual inspection required")
			return
		}
		if j.ReportRepairs > 0 && j.ReportError != "" {
			a.Status = "repairing_report"
			a.Reason = "Automatically correcting the report using retained work"
		}
		if j.Status == "deferred" {
			a.Status = "waiting_prerequisite"
			return
		}
		if j.Status == "stopped" {
			if e = s.resumeAutoJob(ctx, a, j); e != nil {
				if errors.Is(e, errAutoArtifactPending) {
					a.Status = "preserving_artifacts"
					a.Reason = e.Error()
					return
				}
				a.State.Pause(e.Error())
				a.Reason = e.Error()
			}
			return
		}
		if j.Status == "prepared" || j.Status == "starting" {
			if e = s.launchAutoJob(ctx, a, j); e != nil {
				a.State.Pause(e.Error())
				a.Reason = e.Error()
			}
			return
		}
		if err := s.ensureAutoBridges(j); err != nil {
			s.stopAutoJobs(ctx, a, "Bridge unavailable")
			return
		}
		_ = os.Chtimes(filepath.Join(autoRoot, j.ID, "heartbeat"), time.Now(), time.Now())
		raw, err := s.runAutoCommand(ctx, "status", "--job", j.ID)
		if err != nil {
			s.stopAutoJobs(ctx, a, "Runner status unavailable: "+err.Error())
			return
		}
		var st struct {
			State    string `json:"state"`
			ExitCode *int   `json:"exit_code"`
		}
		if json.Unmarshal(raw, &st) != nil {
			s.stopAutoJobs(ctx, a, "Invalid runner status")
			return
		}
		if handled, err := s.handleAutoNodeLaunchFailure(ctx, a, j, raw); handled || err != nil {
			if err != nil {
				a.Reason = "Node runtime recovery pending: " + err.Error()
			}
			return
		}
		if handled, err := s.handleAutoPrivateToolingFailure(ctx, a, j, raw); handled || err != nil {
			if err != nil {
				a.Reason = "Selected tooling recovery pending: " + err.Error()
			}
			return
		}
		if err := autoProgressCharge(a, j, raw); err != nil {
			left, e := autoProgressRemaining(a, j)
			if e != nil || left > 0 {
				s.stopAutoJobs(ctx, a, err.Error())
				return
			}
		}
		// A completed successful process may use its full allowance and still
		// submit evidence for review. Exhaustion forbids another execution;
		// it must not discard a valid final report already produced.
		if st.State != "done" || st.ExitCode == nil || *st.ExitCode != 0 {
			if ended, err := s.endAutoProgressBudget(ctx, a, j); ended || err != nil {
				if err != nil {
					a.Reason = err.Error()
				}
				return
			}
		}
		if st.State == "running" {
			j.LaunchRetryPaid = false
			return
		}
		if st.State != "done" && st.State != "failed" && st.State != "stopped" {
			s.stopAutoJobs(ctx, a, "Invalid runner status")
			return
		}
		if st.State == "done" {
			j.LaunchRetryPaid = false
		}
		// Persist preservation intent before invoking the external exporter. OFF
		// skips this state; a restart polls it rather than relaunching the model.
		j.Status = "exporting"
		if e = s.saveAuto(a); e != nil {
			a.Reason = "State persistence: " + e.Error()
			return
		}
		if e = s.snapshotAutoJob(ctx, j); e != nil {
			a.Status = "preserving_artifacts"
			a.Reason = "Preserving completed worker artifacts: " + e.Error()
			return
		}
		if st.State != "done" || st.ExitCode == nil || *st.ExitCode != 0 {
			if j.LaunchRetryPaid && (st.State == "failed" || st.State == "stopped") {
				// Export succeeded. Persist credit consumption and stopped together;
				// the next tick rechecks quota before preparing a fresh UUID.
				j.LaunchRetryPaid = false
				j.Status = "stopped"
				_ = s.DB.Update("tasks", id, map[string]any{"status": "backlog"})
				return
			}
			if handled, err := s.deferAutoInterruptedWorker(ctx, a, j, raw, time.Now()); handled || err != nil {
				if err != nil {
					a.Reason = "Interrupted worker preservation: " + err.Error()
				}
				return
			}
			j.Status = "failed"
			a.State.Pause("Worker failed; artifacts retained for inspection")
			a.Reason = a.State.Reason
			_ = s.DB.Update("tasks", id, map[string]any{"status": "failed"})
			return
		}
		if e = s.finishAutoJob(ctx, a, j); e != nil {
			if errors.Is(e, errAutoArtifactPending) {
				a.Status = "reconstructing_documentation"
				a.Reason = e.Error()
				return
			}
			j.Status = "failed"
			_ = s.snapshotAutoJob(ctx, j)
			var reportErr *autoReportError
			if errors.As(e, &reportErr) && autoReportRepairable(reportErr.Error()) {
				j.ReportError = reportErr.Error()
			}
			a.State.Pause("Invalid worker report: " + e.Error())
			a.Reason = a.State.Reason
			return
		}
		return
	}
	roles := a.State.NeededRoles()
	if len(roles) == 0 {
		return
	}
	if e = s.prepareAutoJob(ctx, a, roles[0]); e != nil {
		a.State.Pause(e.Error())
		a.Reason = e.Error()
		a.Status = "paused"
	}
}
func autoFindJob(a *autoRecord, id int64) *autoJob {
	for i := len(a.Jobs) - 1; i >= 0; i-- {
		if a.Jobs[i].TaskID == id {
			return a.Jobs[i]
		}
	}
	return nil
}
func (s *Server) launchAutoJob(ctx context.Context, a *autoRecord, j *autoJob) error {
	if j.Status == "prepared" {
		ready, e := s.prepareAutoPrivate(ctx, a, j)
		if e != nil {
			var terminal *autoPrivateOperationError
			if errors.As(e, &terminal) {
				return s.endAutoPrivateOperation(ctx, a, j, terminal.Error())
			}
		}
		if e != nil || !ready {
			return e
		}
	}
	if ended, err := s.endAutoProgressBudget(ctx, a, j); ended || err != nil {
		return err
	}
	if j.Status == "prepared" && len(j.DocumentationCopies) > 0 {
		ready, e := s.pollAutoDocumentationCopies(ctx, a, j)
		if e != nil || !ready {
			return e
		}
	}
	if j.Status == "prepared" && j.DocumentationRoot > 0 {
		ready, err := s.prepareAutoDocumentation(ctx, a, j)
		if err != nil || !ready {
			return err
		}
	}
	if j.Status == "prepared" { // Also permit a safe provider change before any process exists.
		provider, model, err := s.autoRoute(a, j.Role, time.Now())
		if err != nil {
			return err
		}
		j.Provider = provider
		j.Model = model
	}
	// Persist intent before side effects. start is idempotent for this UUID.
	if e := s.ensureAutoBridges(j); e != nil {
		return e
	}
	if j.Status == "starting" {
		raw, err := s.runAutoCommand(ctx, "launch-state", "--job", j.ID)
		if err != nil {
			return err
		}
		wait, err := autoReconcileLaunch(j, raw)
		if err != nil || wait {
			return err
		}
	}
	if j.Status == "prepared" {
		if err := autoInheritPythonRequest(a, j); err != nil {
			return err
		}
	}
	ready, err := s.recoverAutoPrerequisites(ctx, a, j)
	if err != nil {
		return err
	}
	if !ready {
		return nil
	}
	ready, err = s.recoverAutoPython(ctx, a, j)
	if err != nil {
		return err
	}
	if !ready {
		if j.PythonRecovery != nil && j.PythonRecovery.State == "unavailable" && j.Status == "prepared" {
			autoDeferRequirements(a, j, time.Now())
			return s.saveAuto(a)
		}
		return nil
	}
	ready, err = s.recoverAutoNode(ctx, a, j)
	if err != nil {
		return err
	}
	if !ready {
		if j.NodeRecovery != nil && j.NodeRecovery.State == "unavailable" && j.Status == "prepared" {
			autoDeferRequirements(a, j, time.Now())
			return s.saveAuto(a)
		}
		return nil
	}
	toolingReady, toolingErr := s.recoverAutoPrivateTooling(ctx, a, j)
	if toolingErr != nil {
		return toolingErr
	}
	if !toolingReady {
		if j.PythonTestRecovery != nil && j.PythonTestRecovery.Diagnostic != "missing" {
			err := s.holdAutoPrivateTooling(ctx, a, j, false)
			if errors.Is(err, errAutoArtifactPending) {
				return nil
			}
			return err
		}
		j.RecoveryCheckAt = time.Now().Add(5 * time.Minute)
		autoDeferRequirements(a, j, time.Now())
		return s.saveAuto(a)
	}
	if j.PythonRecovery != nil && j.PythonRecovery.State == "verified" {
		j.PythonUsedBundle = j.PythonRecovery.BundleKey
	}
	if j.NodeRecovery != nil && j.NodeRecovery.State == "verified" {
		j.NodeUsedBundle = j.NodeRecovery.BundleKey
	}
	j.Status = "starting"
	if j.ExpertRecoveryAttempt > 0 {
		v, err := autoExpertAttempt(a, j)
		if err != nil {
			return err
		}
		if j.Role == "reviewer" {
			v.Status = "reviewing"
		} else {
			v.Status = "implementing"
		}
	}
	if e := s.saveAuto(a); e != nil {
		return e
	}

	model := j.Model
	args := []string{"start", "--job", j.ID, "--provider", j.Provider, "--model", model, "--prompt", filepath.Join(autoRoot, j.ID, "prompt.txt")}
	if j.ExpertRecoveryAttempt > 0 || j.PrivateIntegrationAttempt > 0 {
		left, err := autoProgressRemaining(a, j)
		if err != nil {
			return err
		}
		args = append(args, "--runtime-seconds", fmt.Sprint(int64(left/time.Second)))
	}
	if j.PythonExpectedTestKey != "" && j.PythonRequest == nil {
		args = append(args, "--python-test-key", j.PythonExpectedTestKey)
	}
	launch, e := s.runAutoCommand(ctx, args...)
	if e != nil {
		return e
	}
	var receipt struct {
		Capability string `json:"capability"`
	}
	if json.Unmarshal(launch, &receipt) == nil && receipt.Capability == "python_test_runtime" {
		ready, err := autoApplyPrivateTooling(a, j, launch)
		if err != nil {
			return err
		}
		if !ready {
			j.Status = "prepared"
			if j.PythonTestRecovery != nil && j.PythonTestRecovery.Diagnostic != "missing" {
				err := s.holdAutoPrivateTooling(ctx, a, j, false)
				if errors.Is(err, errAutoArtifactPending) {
					return nil
				}
				return err
			}
			j.RecoveryCheckAt = time.Now().Add(5 * time.Minute)
			autoDeferRequirements(a, j, time.Now())
			return s.saveAuto(a)
		}
	}
	j.Status = "running"
	_ = s.DB.Update("tasks", j.TaskID, map[string]any{"status": "running", "agent": j.Provider, "model": j.Model})
	return nil
}

// Only the locked privileged runner can distinguish an unused UUID from an
// interrupted launch. A timeout or missing unit alone cannot authorize a retry.
func autoReconcileLaunch(j *autoJob, raw []byte) (bool, error) {
	var receipt struct {
		State       string `json:"state"`
		WorkerState string `json:"worker_state"`
	}
	if json.Unmarshal(raw, &receipt) != nil {
		return false, errors.New("Invalid runner status: launch-state JSON")
	}
	switch receipt.State {
	case "unused":
		j.LaunchRetryPaid = false
		return false, nil
	case "launching":
		return true, nil // retain starting; the original launcher still holds its lock
	case "running":
		j.LaunchRetryPaid = false
		j.Status = "running"
		return true, nil
	case "consumed":
		if receipt.WorkerState == "done" || receipt.WorkerState == "failed" || receipt.WorkerState == "stopped" {
			if receipt.WorkerState == "done" {
				j.LaunchRetryPaid = false
			}
			// The existing polling/export/report path preserves evidence, then
			// normal operational recovery resumes the same task in a fresh UUID.
			j.Status = "running"
			return true, nil
		}
	}
	return false, errors.New("Invalid runner status: unknown launch-state")
}

func (s *Server) finishAutoJob(ctx context.Context, a *autoRecord, j *autoJob) error {
	raw, e := autoReadRegular(filepath.Join(autoRoot, j.ID, "output.jsonl"), 32<<20)
	if e != nil {
		return e
	}
	if len(raw) > 32<<20 {
		return errors.New("worker log exceeds 32 MiB")
	}
	events, _ := agents.ParseStreamLines(j.Provider, string(raw)+"\n")
	report := ""
	for _, ev := range events {
		if ev.Type == "text" {
			if t, ok := ev.Payload["text"].(string); ok {
				report = t
			}
		}
	}
	// result.json is an explicitly requested strict report artifact, read without
	// following symlinks. Never run shell text, check commands or paths from it.
	if r, err := s.runAutoCommand(ctx, "report", "--job", j.ID); err == nil {
		report = string(r)
	}
	if j.PrivateIntegrationAttempt > 0 && j.Role == "reviewer" {
		if err := s.reconcileAutoPrivateTests(ctx, a); err != nil {
			a.Reason = "Private test observation pending: " + err.Error()
		}
	}
	next, reportValidation := autoValidateWorkerReport(a, j, []byte(report))
	if e = reportValidation; e != nil {
		if j.DocumentationRoot > 0 && j.Role == "builder" && strings.Contains(e.Error(), "documentary completion cannot authorize") {
			s.rejectAutoDocumentation(ctx, a, j, "Documentary submission failed: "+e.Error())
			return nil
		}
		return &autoReportError{e}
	}
	autoReportValidated(j)
	if intercepted, err := s.recordAutoRequirements(ctx, a, j, []byte(report)); err != nil {
		return err
	} else if intercepted {
		return nil
	}
	if j.DocumentationRoot > 0 && j.Role == "builder" {
		if e = s.finishAutoDocumentation(ctx, a, j); e != nil {
			return e
		}
		if j.Status == "done" {
			return nil
		}
	}
	if j.Role == "planner" {
		if e = s.pinAutoMaintenance(ctx, a, j, next.Items); e != nil {
			if errors.Is(e, errAutoArtifactPending) {
				return e
			}
			return &autoReportError{e}
		}
		if e = s.pinAutoSources(ctx, a, next.Items); e != nil {
			if errors.Is(e, errAutoArtifactPending) {
				return e
			}
			return &autoReportError{e}
		}
	}
	if e = s.snapshotAutoJob(ctx, j); e != nil {
		return e
	}
	if e = s.finishAutoMaintenanceReview(ctx, a, j, []byte(report)); e != nil {
		return e
	}
	if e = s.finishAutoPrivate(ctx, a, j, []byte(report)); e != nil {
		var terminal *autoPrivateOperationError
		if errors.As(e, &terminal) {
			return s.endAutoPrivateOperation(ctx, a, j, terminal.Error())
		}
		return e
	}
	if e = s.finishAutoRequirementDiagnosis(ctx, a, j, []byte(report)); e != nil {
		return e
	}
	s.closeAutoBridge(j.ID)
	j.Status = "done"
	if j.Role == "builder" && j.ExpertRecoveryAttempt > 0 {
		v, err := autoExpertAttempt(a, j)
		if err != nil {
			return err
		}
		v.Status = "awaiting_review"
	}
	j.Summary = clipEnd(report, 3000)
	ended := store.Now()
	rc := 0
	att, e := s.DB.LatestAttempt(j.TaskID)
	if e != nil {
		att, e = s.DB.InsertAttempt(&store.Attempt{TaskID: j.TaskID, N: 1, Status: "done", Driver: "autonomy-isolated", Agent: j.Provider, WorktreePath: j.ArtifactPath, FinishedAt: &ended, ExitCode: &rc, ResultJSON: store.J(map[string]any{"summary": report, "isolated": true})})
	}
	if e != nil {
		return e
	}
	seq, _ := s.DB.MaxEventSeq(att.ID)
	if seq == 0 {
		for i, ev := range events {
			_ = s.DB.InsertEvent(att.ID, int64(i+1), ev.Type, store.J(ev.Payload))
		}
		_ = s.DB.InsertEvent(att.ID, int64(len(events)+1), "text", store.J(map[string]any{"text": report}))
	}
	if j.Role == "reviewer" {
		var verdict autonomy.Verdict
		if json.Unmarshal([]byte(report), &verdict) == nil && verdict.Approve != nil {
			for i := len(a.State.Assignments) - 1; i >= 0; i-- {
				as := a.State.Assignments[i]
				if as.Role == "builder" && as.Item == a.State.Item && as.Step == a.State.Step && as.Round == a.State.Revision && as.Completed {
					if builder := autoFindJob(a, as.TaskID); builder != nil {
						builder.Approved = verdict.AcceptsWork()
						builder.ReviewOutcome = verdict.Outcome
						builder.Rejected = !verdict.AcceptsWork()
						builder.ReviewReason = verdict.Reason
						builder.ReviewTaskID = j.TaskID
						if builder.ExpertRecoveryAttempt > 0 {
							v, err := autoExpertAttempt(a, builder)
							if err != nil {
								return err
							}
							outcome := "rejected"
							if builder.Approved {
								outcome = "approved"
							}
							if err = autoFinishExpertRecovery(v, outcome, j.TaskID, verdict.Reason, time.Now()); err != nil {
								return err
							}
						}
						if r := a.DocumentationReservations[builder.DocumentationRoot]; r != nil {
							if builder.Approved {
								r.Outcome = "approved"
							} else {
								r.Outcome = "rejected"
							}
						}
					}
					break
				}
			}
		}
	}
	a.State = &next
	if j.Role == "planner" {
		loc, _ := time.LoadLocation(a.Config.Timezone)
		local := time.Now().In(loc)
		if local.Hour() >= a.Config.MorningHour {
			a.StrategyDay = local.Format("2006-01-02")
		}
	}
	status := "done"
	if j.Role == "builder" {
		status = "review"
	}
	_ = s.DB.Update("tasks", j.TaskID, map[string]any{"status": status})
	s.Bus.Publish("board", "autonomy", map[string]any{"task_id": j.TaskID})
	return nil
}
func (s *Server) rememberAuto(ctx context.Context, a *autoRecord) {
	key := fmt.Sprintf("%s-%d", a.State.Date, a.State.Cycle)
	if a.RememberedCycle == key || s.Memory == nil {
		return
	}
	// Only controller-authored identifiers/status enter shared memory. Agent
	// reports remain in isolated artifacts, avoiding accidental secret writes.
	text := fmt.Sprintf("Autonomous experiment %s cycle %d: phase=%s; %d proposals, %d role tasks. Reports and artifacts are on Lectern /autonomy.html. No production deployments or publishing.", a.State.Date, a.State.Cycle, a.State.Phase, len(a.State.Items), len(a.State.Assignments))
	if e := s.Memory.Remember(ctx, memory.Entry{Key: "autonomy-" + key, Project: autoOwner, Topic: autoOwner, Session: "autonomy-" + key, Agent: "lectern", Category: "checkpoint", Text: text}); e == nil {
		a.RememberedDay = a.State.Date
		a.RememberedCycle = key
	} else {
		a.Reason = "Grimoire handoff pending: " + e.Error()
	}
}
func autoUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:])
}

func (s *Server) ScheduleAutonomyTick(ctx context.Context) {
	s.autoWG.Add(1)
	go func() { defer s.autoWG.Done(); s.RunAutonomyTick(ctx) }()
}

// Validate a transport report without advancing the durable assignment. Documentary
// reconstruction follows only valid transport; bounded report correction retains
// the original admission and does not freeze an unusable candidate receipt.
func autoValidateWorkerReport(a *autoRecord, j *autoJob, report []byte) (autonomy.State, error) {
	var next autonomy.State
	if err := json.Unmarshal([]byte(store.J(a.State)), &next); err != nil {
		return next, err
	}
	if err := validateAutoExpertAuditReport(a, j.Role, report); err != nil {
		return next, err
	}
	if err := autoValidatePrivateReport(a, j, report); err != nil {
		return next, err
	}
	err := next.ApplyReport(a.Config, j.TaskID, report)
	if err == nil {
		err = autoValidateMaintenanceReviewReport(a, j, report)
	}
	return next, err
}
