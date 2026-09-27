package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/autonomy"
)

// Only a projection from the installed root-owned registry. These fields are
// never decoded from a planner submission to confer maintenance permission.
type autoServerMaintenancePolicy struct {
	ServiceID           string                `json:"service_id"`
	Action              string                `json:"action"`
	Min                 autoMaintenanceLimits `json:"min"`
	Max                 autoMaintenanceLimits `json:"max"`
	MemoryHeadroomBytes int64                 `json:"memory_headroom_bytes"`
	MemoryHeadroomRatio int                   `json:"memory_headroom_ratio"`
	Stateless           bool                  `json:"stateless"`
	Protected           bool                  `json:"protected"`
	ValidationProfile   string                `json:"validation_profile"`
}

func autoMaintenanceRegisteredPolicy(c autoServerTargetCatalog, target, service string) (autoMaintenanceRegistry, error) {
	var result autoMaintenanceRegistry
	count := 0
	for _, t := range c.Targets {
		if t.ID != target {
			continue
		}
		for _, p := range t.Maintenance {
			if p.ServiceID != service {
				continue
			}
			if p.Action != "service_resource_limits" || p.ValidationProfile != "service_resource_limits_v1" || p.MemoryHeadroomRatio != 2 || !p.Stateless || p.Protected || p.Min.CPUPercent <= 0 || p.Min.MemoryBytes <= 0 || p.Min.Tasks <= 0 || p.Min.CPUPercent > p.Max.CPUPercent || p.Min.MemoryBytes > p.Max.MemoryBytes || p.Min.Tasks > p.Max.Tasks || p.MemoryHeadroomBytes < 0 {
				return result, errors.New("registered maintenance policy incomplete or protected")
			}
			result = autoMaintenanceRegistry{ResourceID: target + "/" + service, Digest: c.RegistrySHA, Stateless: p.Stateless, Protected: p.Protected, Min: p.Min, Max: p.Max, MemoryHeadroomBytes: p.MemoryHeadroomBytes}
			count++
		}
	}
	if count != 1 || !autoHash256(c.RegistrySHA) {
		return result, errors.New("maintenance resource is not uniquely registered")
	}
	return result, nil
}

func autoMaintenanceResourceFromObservation(body map[string]any, service string) (autoMaintenanceObservedResource, error) {
	var result autoMaintenanceObservedResource
	if body["state"] != "observed" || body["configuration_complete"] != true {
		return result, errors.New("maintenance observation is not complete")
	}
	facts, _ := body["facts"].(map[string]any)
	services, _ := facts["services"].(map[string]any)
	value, _ := services[service].(map[string]any)
	fields, _ := value["fields"].(map[string]any)
	if value["state"] != "available" || fields["LoadState"] != "loaded" {
		return result, errors.New("selected service was not observed")
	}
	files, _ := value["identity_files"].(map[string]any)
	// Execution and configuration identities must exist for a mutable target;
	// merely observing a service name and a running PID is insufficient.
	if len(files) == 0 {
		return result, errors.New("selected service has no authenticated file identity")
	}
	for _, raw := range files {
		file, _ := raw.(map[string]any)
		sha, _ := file["sha256"].(string)
		if file["state"] != "available" || !autoHash256(sha) {
			return result, errors.New("selected service file identity incomplete")
		}
	}
	result.TargetID, _ = body["target_id"].(string)
	result.ServiceID = service
	result.ObservationID, _ = body["request_id"].(string)
	result.RegistrySHA, _ = body["registry_sha256"].(string)
	result.ConfigurationSHA, _ = body["configuration_sha256"].(string)
	result.ReceiptSHA, _ = body["receipt_sha256"].(string)
	captured, _ := body["captured_at"].(string)
	var err error
	result.CapturedAt, err = time.Parse(time.RFC3339Nano, captured)
	if err != nil {
		return result, err
	}
	result.Complete = true
	return result, nil
}

func (s *Server) pinAutoMaintenance(ctx context.Context, a *autoRecord, planner *autoJob, items []autonomy.Proposal) error {
	return s.pinAutoMaintenanceAt(ctx, autoRoot, a, planner, items)
}

func (s *Server) pinAutoMaintenanceAt(ctx context.Context, root string, a *autoRecord, planner *autoJob, items []autonomy.Proposal) error {
	needed := false
	for _, p := range items {
		needed = needed || p.Maintenance != nil
	}
	if !needed {
		return nil
	}
	if a == nil || planner == nil || planner.Role != "planner" || !autoExpertJobID.MatchString(planner.ID) {
		return errors.New("maintenance planner identity unavailable")
	}
	_, catalog, err := s.autoServerTargets(ctx)
	if err != nil {
		return err
	}
	// Stage all pins before changing any proposal: a partial failure must not
	// leave the same plan half bound to trusted inputs.
	pins := map[string]*autoMaintenancePlanPin{}
	indices := map[int]*autoMaintenancePlanPin{}
	resources := map[string]bool{}
	for i, p := range items {
		if p.Maintenance == nil {
			continue
		}
		m := *p.Maintenance
		p.Maintenance = &m
		if !autonomy.ValidMaintenanceProposal(p) {
			return fmt.Errorf("item %d: invalid maintenance selection", i)
		}
		policy, err := autoMaintenanceRegisteredPolicy(catalog, m.TargetID, m.ServiceID)
		if err != nil {
			return err
		}
		if resources[policy.ResourceID] {
			return errors.New("duplicate maintenance resource in plan")
		}
		resources[policy.ResourceID] = true
		raw, err := autoReadRegular(filepath.Join(root, planner.ID, "server-observations", m.ObservationID, "request.json"), 4096)
		var request autoServerObservationRequest
		if err != nil || json.Unmarshal(raw, &request) != nil || request.OwnerJob != planner.ID || request.OwnerTask != planner.TaskID || request.RequestID != m.ObservationID || request.TargetID != m.TargetID || request.RegistrySHA != catalog.RegistrySHA {
			return errors.New("maintenance observation does not belong to this planner and registry")
		}
		c, cancel := context.WithTimeout(ctx, 5*time.Second)
		result, err := s.runAutoCommand(c, "server-observe-status", "--job", planner.ID, "--observation-id", m.ObservationID)
		cancel()
		if err != nil {
			return fmt.Errorf("%w: maintenance observation unavailable", errAutoArtifactPending)
		}
		body, status, err := autoDecodeServerObservation(result, request, raw, time.Now())
		if err != nil {
			return err
		}
		if status == 202 {
			return fmt.Errorf("%w: maintenance observation in progress", errAutoArtifactPending)
		}
		observed, err := autoMaintenanceResourceFromObservation(body, m.ServiceID)
		if err != nil {
			return err
		}
		pin, err := autoPinMaintenanceProposal(p.ProjectID, &p, observed, policy, time.Now())
		if err != nil {
			return err
		}
		if old := a.MaintenancePins[pin.Key]; old != nil && old.Key != autoMaintenancePinHash(*old) {
			return errors.New("stored maintenance pin integrity differs")
		}
		pins[pin.Key], indices[i] = pin, pin
	}
	if a.MaintenancePins == nil {
		a.MaintenancePins = map[string]*autoMaintenancePlanPin{}
	}
	for key, pin := range pins {
		a.MaintenancePins[key] = pin
	}
	for index, pin := range indices {
		items[index].Maintenance.Pin = pin.Key
	}
	return nil
}

func autoBindMaintenanceJob(a *autoRecord, j *autoJob) error {
	if a == nil || a.State == nil || j == nil {
		return errors.New("maintenance assignment state missing")
	}
	if j.Role != "builder" && j.Role != "reviewer" {
		return nil
	}
	if a.State.Item < 0 || a.State.Item >= len(a.State.Items) {
		return errors.New("maintenance assignment item missing")
	}
	p := a.State.Items[a.State.Item]
	if p.Maintenance == nil {
		return nil
	}
	pin := a.MaintenancePins[p.Maintenance.Pin]
	if err := autoValidateMaintenancePin(pin, p); err != nil {
		return err
	}
	if j.Role == "reviewer" {
		builder := autoPrivateCurrentBuilder(a)
		if builder == nil || builder.MaintenanceAdmission == nil || builder.MaintenancePin != pin.Key {
			return errors.New("maintenance review builder admission missing")
		}
		raw, _ := json.Marshal(builder.MaintenanceAdmission)
		var admitted autoMaintenanceAdmitted
		if err := json.Unmarshal(raw, &admitted); err != nil {
			return err
		}
		j.MaintenancePin, j.MaintenanceAdmission = pin.Key, &admitted
		return nil
	}
	if !autoRepairAudited(a) {
		return errors.New("maintenance requires both current plan audits")
	}
	var audits [2]autoMaintenanceEvidence
	for index, role := range []string{"auditor_a", "auditor_b"} {
		found := false
		for _, assignment := range a.State.Assignments {
			if assignment.Role != role || assignment.Round != a.State.Revision || !assignment.Completed {
				continue
			}
			owner := autoFindJob(a, assignment.TaskID)
			raw := a.State.Reports[assignment.TaskID]
			var verdict autonomy.Verdict
			if found || owner == nil || owner.Role != role || owner.Status != "done" || !autoExpertJobID.MatchString(owner.ID) || json.Unmarshal(raw, &verdict) != nil || verdict.Approve == nil || !*verdict.Approve {
				return errors.New("maintenance audit provenance invalid")
			}
			audits[index] = autoMaintenanceEvidence{ReceiptSHA: autoSHA(raw), BindingSHA: autoMaintenanceBinding(autoMaintenancePinAuthority(*pin)), TaskID: owner.TaskID, JobID: owner.ID, Successful: true}
			found = true
		}
		if !found {
			return errors.New("maintenance completed audit assignment missing")
		}
	}
	admitted, err := autoAdmitMaintenance(pin, p, j, audits)
	if err != nil {
		return err
	}
	j.MaintenancePin, j.MaintenanceAdmission = pin.Key, admitted
	return nil
}
