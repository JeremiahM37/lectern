package session

import "github.com/JeremiahM37/lectern/v2/internal/scribe/llm"

// ValidateRestored makes a message history loaded from disk safe to send
// again. It drops what no provider accepts and leaves the rest alone:
//
//   - a message with an unknown role, or with no parts once its bad parts are
//     gone, is dropped;
//   - a tool result whose call is not in an earlier message is dropped, since
//     providers reject a result with no call to answer.
//
// It deliberately keeps a tool call that has no result yet. That is what an
// interrupted turn looks like, and the agent's continue logic depends on it
// (agent.Unfinished). It returns the repaired history and how many parts were
// dropped.
func ValidateRestored(msgs []llm.Message) ([]llm.Message, int) {
	if len(msgs) == 0 {
		return msgs, 0
	}
	dropped := 0
	calls := map[string]bool{}
	out := make([]llm.Message, 0, len(msgs))
	for _, m := range msgs {
		if !validRole(m.Role) {
			dropped += len(m.Parts)
			continue
		}
		keep := make([]llm.Part, 0, len(m.Parts))
		for _, p := range m.Parts {
			switch p.Type {
			case llm.PartToolCall:
				if p.ToolCallID != "" {
					calls[p.ToolCallID] = true
				}
			case llm.PartToolResult:
				if !calls[p.ToolCallID] {
					dropped++
					continue
				}
			}
			keep = append(keep, p)
		}
		if len(keep) == 0 {
			continue
		}
		m.Parts = keep
		out = append(out, m)
	}
	if dropped == 0 {
		return msgs, 0
	}
	return out, dropped
}

func validRole(r llm.Role) bool {
	switch r {
	case llm.RoleUser, llm.RoleAssistant, llm.RoleTool:
		return true
	}
	return false
}
