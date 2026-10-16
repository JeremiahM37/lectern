package hooks

import "testing"

func TestEventOffSwitchesEventsForThisRunner(t *testing.T) {
	r := New(Options{Root: t.TempDir(), UserSettings: t.TempDir() + "/none.json"})
	r.entries = append(r.entries, entry{event: "PreToolUse", command: "true"}, entry{event: "Stop", command: "true"})
	counts := r.EventCounts()
	if len(counts) != 2 || counts[0].Event != "PreToolUse" || counts[0].Hooks != 1 {
		t.Fatalf("EventCounts = %+v", counts)
	}
	r.SetEventOff("PreToolUse", true)
	if r.take(entry{event: "PreToolUse"}) {
		t.Fatal("an event switched off must not run")
	}
	if !r.take(entry{event: "Stop"}) {
		t.Fatal("other events must still run")
	}
	r.SetEventOff("PreToolUse", false)
	if !r.take(entry{event: "PreToolUse"}) {
		t.Fatal("switching the event back on must let it run")
	}
}
