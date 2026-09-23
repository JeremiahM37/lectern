package sessions

import (
	"sync"
	"testing"
)

// The picker renders progress from these accessors, so a claim, a phase
// change, a successor and a failure all have to be observable and ordered.
func TestHandoffPhaseLifecycle(t *testing.T) {
	m := &Manager{}
	if m.InFlight(7) {
		t.Fatal("unknown session reported in flight")
	}
	if !m.beginHandoff(7) {
		t.Fatal("first claim refused")
	}
	if !m.InFlight(7) {
		t.Fatal("claim not visible")
	}
	if status, ok := m.Handoff(7); !ok || status.Phase != "saving" {
		t.Fatalf("initial phase: %+v %v", status, ok)
	}
	if m.beginHandoff(7) {
		t.Fatal("second claim accepted while the first was running")
	}

	m.setHandoffPhase(7, "starting", "Codex · astra-test")
	if status, _ := m.Handoff(7); status.Phase != "starting" || status.Destination != "Codex · astra-test" {
		t.Fatalf("phase change lost: %+v", status)
	}
	m.setHandoffSuccessor(7, 42)
	if status, _ := m.Handoff(7); status.SuccessorID != 42 {
		t.Fatalf("successor not recorded: %+v", status)
	}
	m.finishHandoff(7, "")
	if m.InFlight(7) || m.HandoffError(7) != "" {
		t.Fatalf("success left state behind: %v %q", m.InFlight(7), m.HandoffError(7))
	}

	if !m.beginHandoff(9) {
		t.Fatal("claim refused")
	}
	m.finishHandoff(9, "the agent did not write a handoff within 4m0s")
	if m.InFlight(9) {
		t.Fatal("failure left the switch in flight")
	}
	if m.HandoffError(9) == "" {
		t.Fatal("failure not remembered")
	}
	// A retry must not surface the previous attempt's error as its own.
	if !m.beginHandoff(9) {
		t.Fatal("retry refused")
	}
	if m.HandoffError(9) != "" {
		t.Fatalf("retry kept the old error: %q", m.HandoffError(9))
	}
}

func TestHandoffDestinationNamesTheSuccessor(t *testing.T) {
	cases := []struct {
		name string
		opts LaunchOpts
		want string
	}{
		{"model", LaunchOpts{Agent: "codex", Model: "astra-test"}, "Codex · astra-test"},
		{"default model", LaunchOpts{Agent: "claude"}, "Claude · default model"},
		{"saved provider", LaunchOpts{Agent: "codex", Configuration: &LaunchConfiguration{ProfileName: "DeepSeek"}}, "DeepSeek"},
	}
	for _, tc := range cases {
		if got := handoffDestination(tc.opts); got != tc.want {
			t.Fatalf("%s: got %q want %q", tc.name, got, tc.want)
		}
	}
}

// A Manager built by hand (or one whose state was never initialized) must not
// race when several sessions reach it at once. Run under -race this is the proof
// that both the lazy check and the assignment are inside the lock.
func TestHandoffStateIsSafeOnAConcurrentZeroManager(t *testing.T) {
	m := &Manager{}
	const workers = 32
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(id int64) {
			defer wg.Done()
			if m.InFlight(id) {
				t.Errorf("session %d reported in flight before any claim", id)
			}
			if m.HandoffError(id) != "" {
				t.Errorf("session %d had an error before any switch", id)
			}
			if !m.beginHandoff(id) {
				t.Errorf("session %d claim refused", id)
			}
			m.setHandoffSuccessor(id, id*10)
		}(int64(i + 1))
	}
	wg.Wait()
	if m.handoffs == nil {
		t.Fatal("concurrent access left the state uninitialized")
	}
	for i := int64(1); i <= workers; i++ {
		status, ok := m.Handoff(i)
		if !ok || status.Phase != "saving" || status.SuccessorID != i*10 {
			t.Fatalf("session %d lost its state: %+v %v", i, status, ok)
		}
	}
	// A different session on the same manager still sees the one shared map.
	if m.InFlight(workers + 1) {
		t.Fatal("unknown session reported in flight")
	}
}
