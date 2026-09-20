package sessions

import (
	"fmt"
	"strconv"
)

// A session's identity, as the processes inside it see it.
//
// AgentDeck knew a session by its row id and everything it launched knew
// nothing: a tool an agent called could only work out where it was by asking
// tmux, and the memory store recorded the agent's writes under no run at all, so
// the two systems had no key in common. The id now goes into the environment of
// everything the session starts, under a name for each reader.
const (
	// EnvSessionID is read by AgentDeck's own client commands and MCP tools.
	EnvSessionID = "AGENTDECK_SESSION_ID"
	// EnvMemorySession is read by the memory provider's MCP server, which stamps
	// it on every write so "what did this session learn" has an exact answer.
	EnvMemorySession = "GRIMOIRE_SESSION"
)

// MemorySessionKey is how a session is named in the memory store. It is
// namespaced because the store is shared: a bare number would collide with any
// other tool's run ids, and with another AgentDeck's.
func MemorySessionKey(id int64) string { return fmt.Sprintf("agentdeck-s%d", id) }

// identityEnv is set last, over the agent's and the project's own environment:
// it is provenance, and a launch profile must not be able to file one session's
// writes under another.
func identityEnv(env map[string]string, id int64) {
	env[EnvSessionID] = strconv.FormatInt(id, 10)
	env[EnvMemorySession] = MemorySessionKey(id)
}
