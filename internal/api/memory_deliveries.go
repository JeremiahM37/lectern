package api

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/auth"
	"github.com/JeremiahM37/lectern/v2/internal/memory"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// memoryDeliveries is GET /api/tasks/{id}/memory — what every attempt of this
// task was handed, newest first. The session side lives on
// GET /api/sessions/{id}/memory (session_memory.go), which already answered
// "what did this session write"; deliveries are the other half of the same
// question, so they are reported on the same URL rather than a second one.
func (s *Server) taskMemory(w http.ResponseWriter, r *http.Request) {
	task, ok := s.taskParam(w, r)
	if !ok {
		return
	}
	rows, err := s.DB.MemoryDeliveriesForTask(task.ID, deliveryLimit(r))
	if err != nil {
		respondErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"task_id": task.ID, "deliveries": memoryDeliveries(rows)})
}

// deliveryLimit bounds a listing. The default is generous because the Memory
// section is a history, not a feed; ?limit= can raise it to 200.
func deliveryLimit(r *http.Request) int {
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 && n <= 200 {
			limit = n
		}
	}
	return limit
}

// memoryDeliveries turns stored rows into the wire shape. Items are parsed here
// rather than in the component so a malformed items_json degrades to "no item
// list" instead of breaking the read.
func memoryDeliveries(rows []*store.MemoryDelivery) []memory.Delivery {
	out := make([]memory.Delivery, 0, len(rows))
	for _, row := range rows {
		out = append(out, memory.Delivery{
			ID: row.ID, SessionID: row.SessionID, TaskID: row.TaskID,
			AttemptID: row.AttemptID, At: row.At, Mode: row.Mode, Bytes: row.Bytes,
			Items: memory.ParseItems(row.ItemsJSON),
		})
	}
	return out
}

type memoryFeedbackIn struct {
	// Helpful is a pointer so an absent field is a schema error rather than a
	// silent "not relevant" — the two answers mean different things.
	Helpful *bool  `json:"helpful"`
	Note    string `json:"note"`
}

// memoryItemFeedback is POST /api/memory/items/{id}/feedback — "this helped" or
// "this was not relevant". It changes the operator's own memory store, so like
// an approval decision it needs a human behind it, not merely something that
// can reach the API (an agent must not be able to retune its own recall).
func (s *Server) memoryItemFeedback(w http.ResponseWriter, r *http.Request) {
	if !s.humanPrincipal(w, r, "memory feedback") {
		return
	}
	provider, ok := s.Memory.(memory.Reviewer)
	if !ok {
		httpError(w, 501, "the configured memory provider does not accept feedback")
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		httpError(w, 404, "no such memory item")
		return
	}
	var body memoryFeedbackIn
	if err := decodeBody(r, &body); err != nil {
		httpError(w, 422, "%s", err.Error())
		return
	}
	if body.Helpful == nil {
		httpError(w, 422, "helpful is required")
		return
	}
	if err := provider.Feedback(r.Context(), id, *body.Helpful, strings.TrimSpace(body.Note)); err != nil {
		httpError(w, 502, "%s", err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"id": id, "helpful": *body.Helpful, "status": "recorded"})
}

type memoryChallengeIn struct {
	Reason string `json:"reason"`
}

// memoryItemChallenge is POST /api/memory/items/{id}/challenge — "this is
// wrong". The item is not deleted here: the claim and the objection are the
// store's to reconcile, and lectern only forwards the operator's reason.
func (s *Server) memoryItemChallenge(w http.ResponseWriter, r *http.Request) {
	if !s.humanPrincipal(w, r, "challenging a memory") {
		return
	}
	provider, ok := s.Memory.(memory.Reviewer)
	if !ok {
		httpError(w, 501, "the configured memory provider does not accept challenges")
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		httpError(w, 404, "no such memory item")
		return
	}
	var body memoryChallengeIn
	if err := decodeBody(r, &body); err != nil {
		httpError(w, 422, "%s", err.Error())
		return
	}
	reason := strings.TrimSpace(body.Reason)
	if reason == "" {
		// A challenge without a reason is unreviewable, and it is the whole
		// point of the call.
		httpError(w, 422, "reason is required")
		return
	}
	if err := provider.Challenge(r.Context(), id, reason); err != nil {
		httpError(w, 502, "%s", err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"id": id, "status": "challenged"})
}

// humanPrincipal applies the same rule approvals use: a signed-in person, or a
// control plane deliberately running with no auth at all.
func (s *Server) humanPrincipal(w http.ResponseWriter, r *http.Request, what string) bool {
	if principal, _ := auth.FromContext(r.Context()); !s.Auth.CanDecide(principal) {
		httpError(w, 403, "%s requires a signed-in human (tailscale identity or access token)", what)
		return false
	}
	return true
}
