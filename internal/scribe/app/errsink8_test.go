package app

import "testing"

func TestErrorSinkPostsAStandingFaultOnceAndRearmsOnRecovery(t *testing.T) {
	var s errorSink
	s.Post("hook", "pre-edit hook exited 2")
	s.Post("hook", "")
	s.Sync("mcp:docs", "mcp", "docs did not start", true)
	s.Sync("mcp:docs", "mcp", "docs did not start", true) // still failing: not posted again
	got := s.Drain()
	if len(got) != 2 || got[0].Source != "hook" || got[1].Text != "docs did not start" {
		t.Fatalf("drain = %+v, want the hook fault and one mcp fault", got)
	}
	if again := s.Drain(); len(again) != 0 {
		t.Fatalf("queue not emptied: %+v", again)
	}
	s.Sync("mcp:docs", "mcp", "", false) // recovered
	s.Sync("mcp:docs", "mcp", "docs did not start", true)
	if n := len(s.Drain()); n != 1 {
		t.Fatalf("a fault after recovery should post again, got %d", n)
	}
}

func TestStaleMemoriesAndBackgroundErrorsAreZeroWithoutAnApp(t *testing.T) {
	// A backend with no memory store and no extensions reports nothing.
	a := &App{}
	b := &scribeBackend{app: a}
	if n := b.StaleMemories(); n != 0 {
		t.Fatalf("StaleMemories = %d, want 0", n)
	}
	if got := b.BackgroundErrors(); len(got) != 0 {
		t.Fatalf("BackgroundErrors = %+v, want none", got)
	}
}
