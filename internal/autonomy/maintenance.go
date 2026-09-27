package autonomy

import (
	"regexp"
	"strings"
)

type MaintenanceLimits struct {
	CPUPercent  int   `json:"cpu_quota_percent"`
	MemoryBytes int64 `json:"memory_max_bytes"`
	Tasks       int   `json:"tasks_max"`
}
type MaintenanceProposal struct {
	TargetID      string            `json:"target_id"`
	ServiceID     string            `json:"service_id"`
	ObservationID string            `json:"observation_id"`
	Limits        MaintenanceLimits `json:"limits"`
	Pin           string            `json:"pin,omitempty"`
}

var maintenanceID = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)

func ValidMaintenanceProposal(p Proposal) bool {
	m := p.Maintenance
	if m == nil {
		return true
	}
	hash := func(s string) bool { return len(s) == 64 && strings.Trim(s, "0123456789abcdef") == "" }
	return maintenanceID.MatchString(m.TargetID) && maintenanceID.MatchString(m.ServiceID) && hash(m.ObservationID) && (m.Pin == "" || hash(m.Pin)) && m.Limits.CPUPercent > 0 && m.Limits.MemoryBytes > 0 && m.Limits.Tasks > 0 && p.SourceRevision == "" && p.ContinueTaskID == 0 && p.RepairTaskID == 0 && p.DocumentationTaskID == 0 && p.ExpertRecoveryTaskID == 0 && p.ExpertProgressKey == "" && p.IntegrationTaskID == 0 && p.IntegrationPin == "" && len(p.IntegrationPaths) == 0 && p.SourceIntegrationID == "" && p.DiagnoseTaskID == 0 && p.DiagnoseRequirement == "" && p.EnvironmentDiagnosisTaskID == 0
}
