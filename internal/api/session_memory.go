package api

import (
	"net/http"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/memory"
	"github.com/JeremiahM37/lectern/v2/internal/sessions"
)

// sessionMemory reports what a session's agent wrote to the memory store, read
// back by the key both sides share. Status says which of several quiet outcomes
// this is, because "nothing listed" means very different things: the agent
// wrote nothing, the provider is down, or no provider is configured at all.
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
		"counts": map[string]int{}, "changes": []memory.Change{}}
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
