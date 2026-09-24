package sessions

import (
	"context"

	"github.com/JeremiahM37/lectern/v2/internal/agentevents"
	"github.com/JeremiahM37/lectern/v2/internal/executor"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// pollCodexUsage is codex's equivalent of the statusline hook: codex has no
// http-hook usage push (docs/agent-events.md section 2's codex worker note),
// so lectern instead tails the rollout JSONL codex itself writes, batched
// into the same per-target round trip the pane poll above just made. Only
// sessions with a known codex_thread_id (learned from the
// AgentTurnComplete notify payload — see internal/agentevents.IngestEvent)
// are candidates; a codex session that has not completed a turn yet has no
// thread id to look up and is silently skipped until it does.
func (m *Manager) pollCodexUsage(ctx context.Context, ex executor.Executor, group []*store.Session) {
	byThread := map[string]*store.Session{}
	ids := make([]string, 0, len(group))
	for _, s := range group {
		if s.Agent != "codex" || s.CodexThreadID == "" {
			continue
		}
		byThread[s.CodexThreadID] = s
		ids = append(ids, s.CodexThreadID)
	}
	if len(ids) == 0 {
		return
	}
	r, err := ex.Run(ctx, agentevents.CodexRolloutTailCommand(ids), executor.RunOpts{Timeout: 20})
	if err != nil || !r.OK() {
		// Same posture as the pane poll above: an unreachable target or a
		// missing rollout file is not evidence of anything wrong with the
		// session, so leave usage at its last known value and retry next tick.
		return
	}
	tails, complete := agentevents.ParseCodexRolloutSnapshot(r.Stdout, ids)
	if !complete {
		m.Log.Debug("incomplete codex rollout poll")
		return
	}
	in := agentevents.New(m.DB, m.Bus)
	for id, tail := range tails {
		if len(tail) == 0 {
			continue
		}
		s, ok := byThread[id]
		if !ok {
			continue
		}
		usage, ok := agentevents.ParseCodexRolloutUsage(tail)
		if !ok {
			continue
		}
		if err := in.IngestCodexUsage(s, usage); err != nil {
			m.Log.Debug("codex usage ingest failed", "session", s.ID, "err", err)
		}
	}
}
