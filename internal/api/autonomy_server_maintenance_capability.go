package api

func autoServerOperationsCapability(runner, observer, executor map[string]any) map[string]any {
	observation, maintenance := "unavailable", "unavailable"
	if runner["status"] == "installed" && observer["status"] == "installed" {
		observation = "on_demand"
		if executor["status"] == "installed" {
			maintenance = "on_demand"
		}
	}
	return map[string]any{
		"capability": "registered_server_operations", "status": observation,
		"observer": observer, "maintenance_helper": executor, "maintenance_status": maintenance,
		"discovery":   "GET /server-targets lists the currently registered targets and any maintenance policies. Installed helpers do not imply a target or operation is enabled.",
		"observation": "POST /server-observations with target_id, then preserve the returned id and poll GET /server-observations?id=ID. Read timestamps and per-fact availability; unavailable is not healthy.",
		"proposal":    "For a useful change supported by a fresh observation and registered maintenance policy, select maintenance:{target_id,service_id,observation_id,limits:{cpu_quota_percent,memory_max_bytes,tasks_max}} with ordinary project, rationale and acceptance fields. The controller pins the exact candidate before two independent plan audits.",
		"validation":  "An admitted independent reviewer can POST /maintenance-validation with pin_sha256 and poll by the returned id. Executed validation and the archived independent review establish candidate evidence; they do not establish deployment.",
		"execution":   "A maintenance transaction needs controller-verified offbox backup and restore, fresh configuration and resource observations, quota, exact admission and execution evidence. Applied changes require registered post-apply health evidence. Unfinished owned effects retain recovery responsibility through OFF and restart.",
		"authority":   "Only explicitly registered operations and targets. No arbitrary host commands, destructive data operations, public publication, access to credential values or alteration of autonomy policy. Existing terminal sessions remain outside maintenance ownership.",
	}
}
