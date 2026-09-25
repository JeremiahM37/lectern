package sessions

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/memory"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

type contextDelivery struct {
	seen map[string]time.Time
}

func (m *Manager) automaticContext(ctx context.Context, session *store.Session, query string) memory.ContextResult {
	if session.ProjectID == nil {
		return memory.ContextResult{}
	}
	project, err := m.DB.Project(*session.ProjectID)
	if err != nil {
		return memory.ContextResult{}
	}
	trimmed := strings.ToLower(strings.Trim(strings.TrimSpace(query), ".!"))
	switch trimmed {
	case "continue", "thanks", "ok", "yes", "no", "hello":
		return memory.ContextResult{}
	}
	now := time.Now()
	m.mu.Lock()
	state := m.contextDelivered[session.ID]
	var excluded []string
	for key, stamp := range state.seen {
		if now.Sub(stamp) < 30*time.Minute {
			excluded = append(excluded, key)
		}
	}
	m.mu.Unlock()
	if len(excluded) > 256 {
		excluded = excluded[:256]
	}
	result := memory.Automatic(ctx, m.Memory, project.Name, query, excluded, project.MemoryTopic)
	return result
}

// recordDelivery writes the delivery log row and announces it. The cache above
// answers "do not send this again within 30 minutes"; this answers "what was
// this agent actually given", which nobody could ask before — see
// docs/memory-visibility.md.
//
// Only a delivery that carried something is recorded. A lookup that matched
// nothing injected nothing, and logging it would make the section read as a
// stream of things the agent never saw.
func (m *Manager) recordDelivery(sessionID int64, result memory.ContextResult) {
	if result.Context == "" {
		return
	}
	row, err := m.DB.InsertMemoryDelivery(store.MemoryDelivery{
		SessionID: &sessionID, Mode: result.Mode, Bytes: result.Size(),
		ItemsJSON: result.ItemsJSON()})
	if err != nil {
		m.Log.Warn("memory delivery could not be recorded", "session", sessionID, "err", err)
		return
	}
	payload := map[string]any{"id": row.ID, "session_id": sessionID, "at": row.At,
		"mode": row.Mode, "bytes": row.Bytes, "items": result.Items}
	m.Bus.Publish("board", "memory.delivered", payload)
	m.Bus.Publish(fmt.Sprintf("session:%d", sessionID), "memory.delivered", payload)
}

func (m *Manager) recordContext(sessionID int64, result memory.ContextResult) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.contextDelivered == nil {
		m.contextDelivered = make(map[int64]contextDelivery)
	}
	state := m.contextDelivered[sessionID]
	if state.seen == nil {
		state.seen = make(map[string]time.Time)
	}
	now := time.Now()
	for key, stamp := range state.seen {
		if now.Sub(stamp) >= 30*time.Minute {
			delete(state.seen, key)
		}
	}
	for _, key := range result.Keys {
		state.seen[key] = now
	}
	if len(state.seen) > 256 {
		state.seen = make(map[string]time.Time)
		for _, key := range result.Keys {
			state.seen[key] = now
		}
	}
	m.contextDelivered[sessionID] = state
}
