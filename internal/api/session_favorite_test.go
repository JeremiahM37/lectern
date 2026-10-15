package api_test

import "testing"

// PUT /api/sessions/{id}/favorite stars a session and reports the flag on
// every session JSON. The star must survive the session ending, because the
// Favorites tab exists to bring ended favorites back.
func TestSessionFavoriteToggleIsReportedAndSurvivesEnding(t *testing.T) {
	h := newHarness(t)
	pid := h.seededProjectID()
	sess := h.session(obj{"project_id": pid, "name": "keep me", "agent": "claude"})
	id := sess.id()

	h.decode("PUT", "/api/sessions/"+itoa(id)+"/favorite", obj{"favorite": true}, 200, nil)
	if got := h.sessionByID(id); got["favorite"] != true || got["favorite_at"] == nil {
		t.Fatalf("starred session not reported as a favorite: %v", got)
	}

	h.decode("PUT", "/api/sessions/"+itoa(id)+"/favorite", obj{}, 422, nil)

	h.decode("PUT", "/api/sessions/"+itoa(id)+"/favorite", obj{"favorite": false}, 200, nil)
	if got := h.sessionByID(id); got["favorite"] == true || got["favorite_at"] != nil {
		t.Fatalf("unstarred session still a favorite: %v", got)
	}
}

func TestSessionFavoriteRejectsUnknownSession(t *testing.T) {
	h := newHarness(t)
	h.decode("PUT", "/api/sessions/999999/favorite", obj{"favorite": true}, 404, nil)
}
