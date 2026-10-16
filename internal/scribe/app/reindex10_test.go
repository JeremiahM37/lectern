package app

import (
	"sync"
	"testing"
	"time"
)

func TestReindexerCoalescesABurst(t *testing.T) {
	var mu sync.Mutex
	var got [][]string
	r := newReindexer(30*time.Millisecond, func(p []string) {
		mu.Lock()
		got = append(got, p)
		mu.Unlock()
	})
	r.Add("b.go")
	r.Add("a.go")
	r.Add("b.go")
	time.Sleep(10 * time.Millisecond)
	r.Add("c.go") // restarts the quiet period
	time.Sleep(120 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 {
		t.Fatalf("flushes = %d, want 1: %v", len(got), got)
	}
	if want := []string{"a.go", "b.go", "c.go"}; len(got[0]) != 3 || got[0][0] != want[0] || got[0][2] != want[2] {
		t.Fatalf("flushed %v, want %v", got[0], want)
	}
}

func TestReindexerStopDropsPending(t *testing.T) {
	fired := make(chan struct{}, 1)
	r := newReindexer(20*time.Millisecond, func([]string) { fired <- struct{}{} })
	r.Add("x.go")
	r.Stop()
	r.Add("y.go") // ignored after Stop
	select {
	case <-fired:
		t.Fatal("flush ran after Stop")
	case <-time.After(80 * time.Millisecond):
	}
}
