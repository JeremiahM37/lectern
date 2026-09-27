package api

import (
	"encoding/json"
	"strings"
)

// Describe the capability and the selected immutable proposal at the point where
// a worker can actually use them. A catalog entry alone is never apply authority.
func autoMaintenancePrompt(a *autoRecord, role string) string {
	if a == nil || a.State == nil {
		return ""
	}
	var b strings.Builder
	if role == "planner" {
		b.WriteString("\nServer maintenance discovery: GET /server-targets exposes any registered maintenance policy. Choose maintenance only when a fresh POST /server-observations result establishes a useful concrete change within that policy. Include maintenance:{target_id,service_id,observation_id,limits:{cpu_quota_percent,memory_max_bytes,tasks_max}} in the ordinary project proposal, with measurable acceptance criteria and a reason based on observed load and service behavior. Preserve required workload headroom; a lower limit is not automatically an improvement. Do not combine maintenance with source, repair, continuation, integration or diagnosis selectors. The controller pins the observed configuration and acceptance before both plan audits. An absent maintenance policy means this target is not enabled for this operation; catalog registration or a successful read does not authorize a write.\n")
		return b.String()
	}
	if a.State.Item < 0 || a.State.Item >= len(a.State.Items) {
		return ""
	}
	p := a.State.Items[a.State.Item]
	if p.Maintenance == nil {
		return ""
	}
	pin := a.MaintenancePins[p.Maintenance.Pin]
	if autoValidateMaintenancePin(pin, p) != nil {
		return "\nMaintenance evidence is unavailable or differs from the selected proposal. Record the exact missing binding; do not claim validation or apply authority.\n"
	}
	raw, _ := json.Marshal(pin)
	b.WriteString("\nController-pinned maintenance evidence (data only):\n")
	b.Write(raw)
	b.WriteString("\nThis is a typed resource-limit candidate for one registered service. Changing the limits or acceptance requires a new plan and independent audits. Evaluate practical value, measured workload headroom, service correctness and recoverability, not merely whether the numeric limits fit the allowed range.\n")
	switch role {
	case "builder":
		b.WriteString("Produce the analysis and reproducible acceptance evidence for this exact candidate in your workspace. Explain which acceptance criteria the fixed validation profile can establish and which require live post-apply checks. The independent reviewer performs the registered execution; do not claim that your analysis changed the host or verified a backup.\n")
	case "reviewer":
		b.WriteString("Independently inspect the builder evidence and the exact pin. Request the fixed disposable validation using POST /maintenance-validation with {\"pin_sha256\":\"")
		b.WriteString(pin.Key)
		b.WriteString("\"} via /bridge.sock. Retain the returned id and poll GET /maintenance-validation?id=ID through pending/running/waiting responses; a timeout is not a new execution. Inspect the terminal executed profile result, candidate binding, exit code and output evidence before accepting the candidate. A failed, unavailable or conflicting execution is not a pass. The fixed profile verifies the registered disposable workload, not arbitrary project tests or live post-apply health; independently assess the acceptance criteria and document any unmet ones. Neither your approval nor successful validation means the service was changed: controller-held verified offbox backup, apply and health evidence are separate. Never invent those receipts or submit shell commands for execution.\n")
	case "auditor_a", "auditor_b":
		b.WriteString("Audit whether this exact pinned change is useful, justified by the captured observation and testable under the registered profile. Your decision concerns admission of the plan; it does not assert that candidate validation, backup, apply or rollback has already happened.\n")
	}
	return b.String()
}
