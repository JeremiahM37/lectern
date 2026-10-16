package memory

import (
	"sync"
	"testing"
)

// TestSurfacedSetIsSafeAcrossGoroutines: recall marks memories surfaced on
// the turn's goroutine while compaction resets the set from the event path.
func TestSurfacedSetIsSafeAcrossGoroutines(t *testing.T) {
	s := &Stores{}
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(3)
		go func() { defer wg.Done(); s.rememberSurfaced([]string{"a", "b"}) }()
		go func() { defer wg.Done(); _ = s.ResetSurfaced() }()
		go func() { defer wg.Done(); _ = s.Surfaced("a") }()
	}
	wg.Wait()
	if err := s.ResetSurfaced(); err != nil || s.Surfaced("a") {
		t.Fatalf("after reset a is still surfaced (err %v)", err)
	}
}
