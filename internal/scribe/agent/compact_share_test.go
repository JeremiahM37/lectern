package agent

import "testing"

// With a small local window the system prompt and tools can fill most of
// it; compacting then frees almost nothing, so it is skipped.
func TestAutoCompactSkippedWhenThePromptFillsTheContext(t *testing.T) {
	f := newFake(t)
	log := &eventLog{}
	opts := baseOptions(f, lookRegistry(), log, t.TempDir())
	opts.Compactor = shortHistoryCompactor{}
	opts.CompactAt = 0.75
	a := New(opts)
	for i := 0; i < 8; i++ {
		a.msgs = append(a.msgs, userMsg("short"))
	}
	a.model.Info.ContextWindow = 32000
	a.lastIn = 26000
	a.sysChars = 4 * 22000 // prompt and tools: about 85% of what is used
	if err := a.maybeCompact(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, e := range log.snapshot() {
		if e.Kind == EvCompactStart {
			t.Fatal("compaction ran although the conversation is a small share of the context")
		}
	}
}
