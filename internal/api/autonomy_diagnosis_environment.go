package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/JeremiahM37/lectern/v2/internal/autonomy"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// Catalog only environments actually used by an approved diagnosis and its
// independent reviewer. A package proposal or a provisioner import probe alone
// is not a reviewed, reusable test environment.
func autoVerifiedDiagnosisEnvironments(a *autoRecord) []*autoVerifiedDiagnosisEnvironment {
	out := []*autoVerifiedDiagnosisEnvironment{}
	for _, d := range a.RequirementDiagnoses {
		e := d.VerifiedEnvironment
		if e == nil || d.Outcome != "completed" || d.TaskID != e.DiagnosisTaskID {
			continue
		}
		b := autoFindJob(a, e.DiagnosisTaskID)
		r := autoFindJob(a, d.ReviewTaskID)
		if b == nil || r == nil || b.ID != e.BuilderJob || r.ID != e.ReviewerJob || b.Role != "builder" || r.Role != "reviewer" || b.Status != "done" || r.Status != "done" || !b.Approved || b.Rejected || b.ReviewTaskID != r.TaskID || b.ReviewOutcome != "completed" {
			continue
		}
		if !autoDiagnosisEnvironmentUsed(b, e.Request, e.Receipt) || !autoDiagnosisEnvironmentUsed(r, e.Request, e.ReviewerReceipt) {
			continue
		}
		if e.Receipt.InputKey != e.ReviewerReceipt.InputKey || e.Receipt.BundleKey != e.ReviewerReceipt.BundleKey || e.Receipt.RuntimeDigest != e.ReviewerReceipt.RuntimeDigest || e.Receipt.BrowserKey != e.ReviewerReceipt.BrowserKey {
			continue
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].DiagnosisTaskID < out[j].DiagnosisTaskID })
	return out
}

func autoDiagnosisEnvironmentUsed(j *autoJob, request *autoPythonRequest, receipt *autoPythonReceipt) bool {
	if request == nil || receipt == nil || j.PythonRequest == nil || j.PythonRecovery == nil || receipt.State != "verified" || receipt.Capability != "python_wheels" || !autoHash256(receipt.InputKey) || !autoHash256(receipt.BundleKey) || !autoHash256(receipt.RuntimeDigest) || j.PythonUsedBundle != receipt.BundleKey || !autoPythonBound(request, *receipt) {
		return false
	}
	return store.J(j.PythonRequest) == store.J(request) && store.J(j.PythonRecovery) == store.J(receipt)
}

func autoDiagnosisEnvironment(a *autoRecord, id int64) *autoVerifiedDiagnosisEnvironment {
	for _, e := range autoVerifiedDiagnosisEnvironments(a) {
		if e.DiagnosisTaskID == id {
			return e
		}
	}
	return nil
}

// An environment selector is auxiliary: the ordinary source, continuation and
// repair checks still run and confer all implementation authority separately.
func autoValidateDiagnosisEnvironments(a *autoRecord, items []autonomy.Proposal) error {
	for _, p := range items {
		id := p.EnvironmentDiagnosisTaskID
		if id < 0 {
			return errors.New("environment_diagnosis_task_id must be positive")
		}
		if id == 0 {
			continue
		}
		e := autoDiagnosisEnvironment(a, id)
		if e == nil {
			return fmt.Errorf("diagnosis %d has no independently reviewed verified environment", id)
		}
		pin := a.EnvironmentPins[id]
		if pin != nil && store.J(pin) != store.J(e) {
			return errors.New("selected diagnosis environment changed after pinning")
		}
		if a.State != nil && a.State.Phase == autonomy.Build && pin == nil {
			return errors.New("diagnosis environment was not pinned before plan audits")
		}
	}
	return nil
}

func pinAutoDiagnosisEnvironments(a *autoRecord, items []autonomy.Proposal) error {
	if err := autoValidateDiagnosisEnvironments(a, items); err != nil {
		return err
	}
	for _, p := range items {
		if p.EnvironmentDiagnosisTaskID == 0 {
			continue
		}
		if a.EnvironmentPins == nil {
			a.EnvironmentPins = map[int64]*autoVerifiedDiagnosisEnvironment{}
		}
		if a.EnvironmentPins[p.EnvironmentDiagnosisTaskID] != nil {
			continue
		}
		var frozen autoVerifiedDiagnosisEnvironment
		if err := json.Unmarshal([]byte(store.J(autoDiagnosisEnvironment(a, p.EnvironmentDiagnosisTaskID))), &frozen); err != nil {
			return err
		}
		a.EnvironmentPins[p.EnvironmentDiagnosisTaskID] = &frozen
	}
	return nil
}

func applyAutoDiagnosisEnvironment(a *autoRecord, j *autoJob) error {
	if a.State == nil || j.Role != "builder" || a.State.Step != 0 || a.State.Item < 0 || a.State.Item >= len(a.State.Items) || !autoRepairAudited(a) {
		return errors.New("environment selection requires an initial builder admitted by both plan audits")
	}
	p := a.State.Items[a.State.Item]
	if err := autoValidateDiagnosisEnvironments(a, []autonomy.Proposal{p}); err != nil {
		return err
	}
	e := a.EnvironmentPins[p.EnvironmentDiagnosisTaskID]
	if e == nil {
		return errors.New("audited environment pin missing")
	}
	var request autoPythonRequest
	if err := json.Unmarshal([]byte(store.J(e.Request)), &request); err != nil {
		return err
	}
	j.PythonRequest = &request
	j.PythonRecovery = nil
	j.PythonExpectedInput = e.Receipt.InputKey
	j.PythonExpectedBundle = e.Receipt.BundleKey
	// This is a request to reproduce the reviewed environment, never a forged
	// receipt for this consumer. The normal provisioner must verify it again.
	return nil
}
