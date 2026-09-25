package api

import (
	"net/http"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/memory"
	"github.com/JeremiahM37/lectern/v2/internal/sessions"
)

// sessionMemory is GET /api/sessions/{id}/memory. It reports both directions of
// the session's memory: `deliveries` is what lectern handed THIS session, and
// `changes` is what the session's agent then wrote back, read by the key both
// sides share. They are one URL because they are one question — "what is this
// session's memory" — and they were separate silences before: nobody could see
// either (docs/memory-visibility.md).
//
// Status describes the write-back half only. Deliveries are already recorded
// rows, so their absence means the agent genuinely was handed nothing.
func (s *Server) sessionMemory(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		httpError(w, 404, "no such session")
		return
	}
	sess, err := s.DB.Session(id)
	if err != nil {
		respondErr(w, err)
		return
	}
	key := sessions.MemorySessionKey(sess.ID)
	out := map[string]any{"session": key, "provider": "none", "status": "disabled",
		"counts": map[string]int{}, "changes": []memory.Change{},
		"deliveries": []memory.Delivery{}}
	if rows, err := s.DB.MemoryDeliveriesForSession(sess.ID, deliveryLimit(r)); err == nil {
		out["deliveries"] = memoryDeliveries(rows)
	}
	provider, ok := s.Memory.(memory.ActivityProvider)
	if s.Memory == nil || !ok {
		writeJSON(w, 200, out)
		return
	}
	out["provider"] = s.Memory.Name()
	// A little before the row was created: the clocks are two machines'.
	since := time.Unix(int64(sess.CreatedAt), 0).Add(-time.Minute)
	activity, err := provider.SessionActivity(r.Context(), key, since)
	if err == nil && len(activity.Changes) == 0 {
		// A session still running from before the rename exported the old key;
		// its writes are there, under that name.
		if legacy, lerr := provider.SessionActivity(r.Context(), sessions.LegacyMemorySessionKey(sess.ID), since); lerr == nil && len(legacy.Changes) > 0 {
			activity, out["session"] = legacy, legacy.Session
		}
	}
	switch {
	case err != nil:
		out["status"], out["message"] = "unavailable", err.Error()
	case len(activity.Changes) == 0:
		out["status"] = "empty"
	default:
		out["status"] = "ready"
	}
	out["counts"], out["changes"], out["since"] = activity.Counts, activity.Changes, activity.Since
	writeJSON(w, 200, out)
}
