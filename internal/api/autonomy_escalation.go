package api

// autoExpertBuilder escalates repeated substantive repair failures. A failed
// process, schema retry or missing dependency is not evidence that a stronger
// builder is needed. Admission, repair allowances and quota gates are unchanged.
func autoExpertBuilder(a *autoRecord) bool {
	if a == nil || a.State == nil || a.State.Item < 0 || a.State.Item >= len(a.State.Items) {
		return false
	}
	p := a.State.Items[a.State.Item]
	if p.Expert {
		return true
	}
	if p.RepairTaskID <= 0 {
		return false
	}
	previous := autoFindJob(a, p.RepairTaskID)
	if previous == nil || previous.RepairSourceTaskID <= 0 {
		return false
	}
	if _, _, rejected := autoRejectedCheckpoint(a, previous.TaskID); !rejected {
		return false
	}
	_, _, rejected := autoRejectedCheckpoint(a, previous.RepairSourceTaskID)
	return rejected
}
