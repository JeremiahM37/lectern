package agent

// Plan depth follows the context in use, as the plan-mode prompt does: at or
// under half the window a plan is a light checklist, over half it is full.
const (
	PlanLight = "light"
	PlanFull  = "full"
)

// PlanDepth returns the depth for a plan written with used of window tokens
// in context. An unknown window counts as full, so nothing is hidden.
func PlanDepth(used, window int) string {
	if window <= 0 || used*100 > window*50 {
		return PlanFull
	}
	return PlanLight
}

// PlanOptionsFor is the review choices for a plan of the given depth. A light
// plan has no "Clear & Implement": there is little context to clear.
func PlanOptionsFor(depth string) []string {
	if depth != PlanLight {
		return PlanOptions()
	}
	var out []string
	for _, o := range PlanOptions() {
		if o != AnswerClear {
			out = append(out, o)
		}
	}
	return out
}
