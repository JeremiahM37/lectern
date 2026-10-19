package agent

import "time"

// unlimitedToolTimeout is the tool timeout the app passes when the user turned
// tool timeouts off. Anything this large is treated as off.
const unlimitedToolTimeout = 100 * time.Hour

// SubagentStepTimeout is the longest one model call of a subagent may run
// (spec 03 §3.6, getAgentTimeoutMs). It is max(300 s, tool timeout x 2.5),
// or 24 h when tool timeouts are off. The lead keeps its own watchdog
// thresholds (agent.Options).
func SubagentStepTimeout(toolTimeout time.Duration) time.Duration {
	if toolTimeout <= 0 || toolTimeout >= unlimitedToolTimeout {
		return 24 * time.Hour
	}
	return max(5*time.Minute, toolTimeout*5/2)
}
