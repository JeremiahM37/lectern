package config

import "testing"

func TestSessionForbiddenAddRemove(t *testing.T) {
	t.Cleanup(func() {
		for _, p := range SessionForbidden() {
			RemoveSessionForbidden(p)
		}
	})
	if !AddSessionForbidden("secrets/*") {
		t.Fatal("first add should succeed")
	}
	if AddSessionForbidden("secrets/*") {
		t.Fatal("duplicate add should report false")
	}
	pats, err := ForbiddenPatterns(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, p := range pats {
		found = found || p == "secrets/*"
	}
	if !found {
		t.Fatal("session patterns should be part of the forbidden list")
	}
	if !RemoveSessionForbidden("secrets/*") || len(SessionForbidden()) != 0 {
		t.Fatal("remove should drop the session pattern")
	}
}
