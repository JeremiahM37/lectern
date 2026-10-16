package chat

import (
	"encoding/json"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/agent"
)

// seedKeyLen is how many leading characters of a task identify it when a
// seeded row is matched with the real subagent that runs it.
const seedKeyLen = 30

// taskKey is the comparison form of a subagent task: its first characters,
// trimmed.
func taskKey(task string) string {
	rs := []rune(strings.TrimSpace(task))
	if len(rs) > seedKeyLen {
		rs = rs[:seedKeyLen]
	}
	return string(rs)
}

// seedAgents adds a pending row for each task in a dispatch's arguments, so the
// subagents show as soon as the call starts rather than when each one starts.
// Rows are only added once; a call that is run again keeps its rows.
func seedAgents(c *Call) {
	if len(c.Agents) > 0 || len(c.Args) == 0 {
		return
	}
	var args struct {
		Tasks []struct {
			Task string `json:"task"`
			Role string `json:"role"`
		} `json:"tasks"`
	}
	if json.Unmarshal(c.Args, &args) != nil {
		return
	}
	for _, t := range args.Tasks {
		role := t.Role
		if role == "" {
			role = "explore"
		}
		c.Agents = append(c.Agents, &agent.AgentInfo{Task: t.Task, Role: role, Status: "pending"})
	}
}

// matchSeed returns the index of the pending seed whose task is task, or -1.
func matchSeed(agents []*agent.AgentInfo, task string) int {
	key := taskKey(task)
	if key == "" {
		return -1
	}
	for i, a := range agents {
		if a.ID == "" && a.Status == "pending" && taskKey(a.Task) == key {
			return i
		}
	}
	return -1
}

// dropSeeds removes the seeds no subagent took. It runs when the dispatch
// call ends, so a seed never outlives its call.
func dropSeeds(c *Call) {
	kept := c.Agents[:0]
	for _, a := range c.Agents {
		if a.ID == "" && a.Status == "pending" {
			continue
		}
		kept = append(kept, a)
	}
	c.Agents = kept
}
