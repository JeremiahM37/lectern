package agent

import (
	"reflect"
	"strings"
	"sync"
	"testing"
)

func TestAgentBusFindingsKeepOrderAndSkipBlank(t *testing.T) {
	b := NewAgentBus()
	b.Post("a1", "  the parser drops empty lines  ")
	b.Post("a2", "   ")
	b.Post("a3", "config is read twice")
	got := b.Findings()
	want := []Finding{{"a1", "the parser drops empty lines"}, {"a3", "config is read twice"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("findings = %+v, want %+v", got, want)
	}
}

// The first agent to claim a file owns it; a second claimant is a conflict,
// and that does not move the owner.
func TestAgentBusFirstClaimWins(t *testing.T) {
	b := NewAgentBus()
	if owner, conflict := b.ClaimEdit("a.go", "a1"); owner != "a1" || conflict {
		t.Fatalf("first claim = %s %v", owner, conflict)
	}
	if owner, conflict := b.ClaimEdit("a.go", "a1"); owner != "a1" || conflict {
		t.Fatalf("same agent again = %s %v", owner, conflict)
	}
	if owner, conflict := b.ClaimEdit("a.go", "a2"); owner != "a1" || !conflict {
		t.Fatalf("second agent = %s %v, want a1 and a conflict", owner, conflict)
	}
	// a.go has two editors now; b.go has one.
	b.ClaimEdit("b.go", "a2")
	if got := b.Conflicts(); len(got) != 1 || !strings.HasPrefix(got[0], "a.go:") {
		t.Fatalf("conflicts = %v, want only a.go", got)
	}
	if got := b.Conflicts("b.go"); len(got) != 0 {
		t.Fatalf("conflicts for b.go = %v, want none", got)
	}
	b.ClaimEdit("b.go", "a1")
	if got := b.Conflicts("b.go"); len(got) != 1 {
		t.Fatalf("conflicts for b.go after a second editor = %v", got)
	}
}

// A read is served from the cache only while the file's stamp is unchanged.
func TestAgentBusReadCacheFollowsTheStamp(t *testing.T) {
	b := NewAgentBus()
	if _, ok := b.CachedRead("f", "10:1"); ok {
		t.Fatal("empty cache hit")
	}
	b.StoreRead("f", "10:1", "contents v1")
	if out, ok := b.CachedRead("f", "10:1"); !ok || out != "contents v1" {
		t.Fatalf("same stamp = %q %v", out, ok)
	}
	if _, ok := b.CachedRead("f", "12:2"); ok {
		t.Fatal("a changed file must miss the cache")
	}
	if _, ok := b.CachedRead("f", ""); ok {
		t.Fatal("no stamp must never hit")
	}
}

// Subagents post and claim from many goroutines at once.
func TestAgentBusIsSafeForConcurrentUse(t *testing.T) {
	b := NewAgentBus()
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := string(rune('a' + i%5))
			b.Post(id, "finding")
			b.ClaimEdit("shared.go", id)
			b.StoreRead("shared.go", "1:1", "x")
			b.CachedRead("shared.go", "1:1")
			_ = b.Conflicts()
		}(i)
	}
	wg.Wait()
	if n := len(b.Findings()); n != 20 {
		t.Fatalf("findings = %d, want 20", n)
	}
}
