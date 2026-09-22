package sessions

import (
	"context"
	"strings"
	"time"

	"github.com/JeremiahM37/lectern/internal/memory"
	"github.com/JeremiahM37/lectern/internal/store"
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
