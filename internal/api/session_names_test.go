package api_test

import (
	"fmt"
	"testing"
)

// Sessions nobody named are told apart by project and topic (re-audit N7).
func TestSessionsGetDistinctDefaultNames(t *testing.T) {
	h := newHarness(t)
	pid := h.seededProjectID()
	project := ""
	for _, p := range h.getList("/api/projects") {
		if p.id() == pid {
			project = p.str("name")
		}
	}
	first := h.session(obj{"project_id": pid, "agent": "claude"})
	second := h.session(obj{"project_id": pid, "agent": "claude"})
	if first.str("name") != project || second.str("name") != project+" #2" {
		t.Fatalf("names: %q, %q (project %q)", first.str("name"), second.str("name"), project)
	}
	primed := h.session(obj{"project_id": pid, "agent": "claude", "prime": "Fix the login page"})
	if primed.str("name") != project+" — Fix the login page" {
		t.Fatalf("first message names it: %q", primed.str("name"))
	}
	// The first prompt sent later names a session that still has its default.
	h.post(fmt.Sprintf("/api/sessions/%d/send", first.id()), obj{"text": "add a health endpoint"}, 200)
	if got := h.sessionByID(first.id()).str("name"); got != project+" — add a health endpoint" {
		t.Fatalf("named from the first prompt: %q", got)
	}
	// A name someone chose is never replaced.
	h.patch(fmt.Sprintf("/api/sessions/%d", second.id()), obj{"name": "mine"}, 200)
	h.post(fmt.Sprintf("/api/sessions/%d/send", second.id()), obj{"text": "something else"}, 200)
	if got := h.sessionByID(second.id()).str("name"); got != "mine" {
		t.Fatalf("a chosen name stays: %q", got)
	}
}
