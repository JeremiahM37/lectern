package api_test

// Session effects: GET /api/sessions/{id}/effects, GET /api/effects/reliability
// and the project effect_checks setting. See internal/effects and
// docs/effect-checks.md.

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// seedEffect writes one evaluation straight into the store, the way the
// watcher would, so the read endpoints can be tested without an agent turn.
func seedEffect(t *testing.T, h *harness, sessionID int64, verified, contradicted, unknown int) *store.EffectCheck {
	t.Helper()
	row, err := h.App.DB.InsertEffectCheck(&store.EffectCheck{SessionID: sessionID,
		MessageHash: fmt.Sprintf("hash-%d-%d", sessionID, verified*100+contradicted*10+unknown),
		Verified:    verified, Contradicted: contradicted, Unknown: unknown,
		Results: json.RawMessage(`[]`)})
	if err != nil {
		t.Fatal(err)
	}
	return row
}

func TestSessionEffectsLatestAndRecentNewestFirst(t *testing.T) {
	h := newHarness(t)
	sess := h.session(obj{"project_id": h.seededProjectID()})
	path := fmt.Sprintf("/api/sessions/%d/effects", sess.id())

	empty := h.get(path)
	if empty["latest"] != nil || len(empty.list("recent")) != 0 {
		t.Fatalf("a session with no evaluations: %v", empty)
	}

	var last *store.EffectCheck
	for i := 0; i < 3; i++ {
		last = seedEffect(t, h, sess.id(), i+1, 0, 0)
	}
	got := h.get(path)
	if got.sub("latest").num("id") != float64(last.ID) {
		t.Fatalf("latest id = %v; want %d", got.sub("latest")["id"], last.ID)
	}
	recent := got.list("recent")
	if len(recent) != 3 || recent[0].num("id") != float64(last.ID) {
		t.Fatalf("recent = %d rows, first %v; want 3 newest first", len(recent), recent)
	}
}

func TestSessionEffectsRecentIsCappedAtTwenty(t *testing.T) {
	h := newHarness(t)
	sess := h.session(obj{"project_id": h.seededProjectID()})
	for i := 0; i < 22; i++ {
		seedEffect(t, h, sess.id(), 1, 0, 0)
	}
	got := h.get(fmt.Sprintf("/api/sessions/%d/effects", sess.id()))
	if n := len(got.list("recent")); n != 20 {
		t.Fatalf("recent has %d rows; want 20", n)
	}
}

func TestEffectReliabilityGroupsByAgentAndModel(t *testing.T) {
	h := newHarness(t)
	sess := h.session(obj{"project_id": h.seededProjectID()})
	seedEffect(t, h, sess.id(), 2, 1, 0)
	seedEffect(t, h, sess.id(), 0, 0, 3)

	code, body := h.request("GET", "/api/effects/reliability?days=30", nil, nil)
	if code != 200 {
		t.Fatalf("reliability: %d %s", code, body)
	}
	var out struct {
		Days int                          `json:"days"`
		Rows []store.EffectReliabilityRow `json:"rows"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	if out.Days != 30 {
		t.Fatalf("days = %d; want 30", out.Days)
	}
	var found *store.EffectReliabilityRow
	for i := range out.Rows {
		if out.Rows[i].Agent == sess.str("agent") && out.Rows[i].Model == sess.str("model") {
			found = &out.Rows[i]
		}
	}
	if found == nil {
		t.Fatalf("no row for agent %q model %q in %+v", sess.str("agent"), sess.str("model"), out.Rows)
	}
	if found.Checks != 2 || found.Claims != 6 || found.Verified != 2 || found.Contradicted != 1 || found.Unknown != 3 {
		t.Fatalf("reliability row = %+v; want checks 2, claims 6, verified 2, contradicted 1, unknown 3", found)
	}
}

func TestEffectReliabilityValidatesDays(t *testing.T) {
	h := newHarness(t)
	for _, bad := range []string{"0", "366", "abc", "-3"} {
		if code := h.status("GET", "/api/effects/reliability?days="+bad, nil); code != 422 {
			t.Fatalf("days=%s: got %d; want 422", bad, code)
		}
	}
}

func TestProjectEffectChecksDefaultsToShowAndIsValidated(t *testing.T) {
	h := newHarness(t)
	p := h.project("effects-default", nil)
	if p.str("effect_checks") != "show" {
		t.Fatalf("new project effect_checks = %q; want show", p.str("effect_checks"))
	}
	pid := p.id()
	for _, bad := range []string{"", "on", "ENFORCE"} {
		if code := h.status("PATCH", fmt.Sprintf("/api/projects/%d", pid), obj{"effect_checks": bad}); code != 422 {
			t.Fatalf("effect_checks=%q: got %d; want 422", bad, code)
		}
	}
	got := h.patch(fmt.Sprintf("/api/projects/%d", pid), obj{"effect_checks": "enforce"}, 200)
	if got.str("effect_checks") != "enforce" {
		t.Fatalf("patched project: %v", got)
	}
}
