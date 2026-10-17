package agent

import (
	"sync"
	"testing"
	"time"
)

// boardWith is a board holding one row per id, and the events it emits.
// Reads of the bus run on several goroutines, so the sink takes a lock.
func boardWith(ids ...string) (*board, *[]AgentInfo) {
	var mu sync.Mutex
	var emitted []AgentInfo
	b := &board{sink: func(e Event) {
		mu.Lock()
		defer mu.Unlock()
		emitted = append(emitted, *e.Agent)
	}}
	for _, id := range ids {
		b.infos = append(b.infos, &AgentInfo{ID: id, Status: "running"})
	}
	return b, &emitted
}

func rowOf(b *board, id string) AgentInfo {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, info := range b.infos {
		if info.ID == id {
			return *info
		}
	}
	return AgentInfo{}
}

// Each outcome reaches the row of the subagent it is about, with the subagent
// it names. The reads run through the bus the way a dispatch runs them.
func TestCacheObserverSetsEachOutcomeOnItsRow(t *testing.T) {
	b, _ := boardWith("a1", "a2")
	bus := NewAgentBus()
	bus.SetCacheObserver(cacheObserver(b))

	// a1 reads the file first: stored.
	_, _, ticket := bus.ReadStart("a1", "f", "s1")
	ticket.Finish("body", true)
	if r := rowOf(b, "a1"); r.Cache != CacheStore || r.CacheFrom != "" {
		t.Fatalf("a1 after store = %q from %q", r.Cache, r.CacheFrom)
	}

	// a2 reads it again at the same stamp: served from a1's read.
	if _, hit, _ := bus.ReadStart("a2", "f", "s1"); !hit {
		t.Fatal("a2 read was not served from the cache")
	}
	if r := rowOf(b, "a2"); r.Cache != CacheHit || r.CacheFrom != "a1" {
		t.Fatalf("a2 after hit = %q from %q", r.Cache, r.CacheFrom)
	}

	// a2 reads a file a1 is reading right now at another stamp: it waits for
	// a1, and once a1 finishes it is served the stored result.
	_, _, running := bus.ReadStart("a1", "g", "s2")
	waited := make(chan bool)
	go func() {
		_, hit, _ := bus.ReadStart("a2", "g", "s2")
		waited <- hit
	}()
	deadline := time.Now().Add(5 * time.Second)
	for rowOf(b, "a2").Cache != CacheWait {
		if time.Now().After(deadline) {
			t.Fatal("a2 never showed the wait")
		}
		time.Sleep(time.Millisecond)
	}
	if r := rowOf(b, "a2"); r.CacheFrom != "a1" {
		t.Fatalf("wait names %q, want a1", r.CacheFrom)
	}
	running.Finish("body2", true)
	if !<-waited {
		t.Fatal("waiter was not served a1's read")
	}
	if r := rowOf(b, "a2"); r.Cache != CacheHit || r.CacheFrom != "a1" {
		t.Fatalf("a2 after waiting = %q from %q", r.Cache, r.CacheFrom)
	}

	// a1 reads f again after the file changed: its entry is replaced.
	_, _, ticket = bus.ReadStart("a1", "f", "s3")
	ticket.Finish("body3", true)
	if r := rowOf(b, "a1"); r.Cache != CacheInvalidate || r.CacheFrom != "" {
		t.Fatalf("a1 after replacing = %q from %q", r.Cache, r.CacheFrom)
	}
}

// A note for an id the board does not hold is dropped, and no row changes.
func TestCacheObserverDropsNotesForUnknownAgents(t *testing.T) {
	b, emitted := boardWith("a1")
	bus := NewAgentBus()
	bus.SetCacheObserver(cacheObserver(b))

	_, _, ticket := bus.ReadStart("", "f", "s1")
	ticket.Finish("body", true)

	if r := rowOf(b, "a1"); r.Cache != "" || r.CacheFrom != "" {
		t.Fatalf("unrelated row got cache state %q", r.Cache)
	}
	if len(*emitted) != 0 {
		t.Fatalf("a note with no row emitted %d events", len(*emitted))
	}
}

// With the dispatch cache off no read goes through the bus, so no row gets a
// cache state. A read with no stamp is also not cached, and gets no note.
func TestCacheStateStaysEmptyWhenTheCacheIsOff(t *testing.T) {
	b, _ := boardWith("a1")
	bus := NewAgentBus()
	bus.SetCacheObserver(cacheObserver(b))

	if _, hit, ticket := bus.ReadStart("a1", "f", ""); hit || ticket != nil {
		t.Fatal("a read with no stamp was cached")
	}
	if r := rowOf(b, "a1"); r.Cache != "" || r.CacheFrom != "" {
		t.Fatalf("row has cache state %q with the cache off", r.Cache)
	}
}
