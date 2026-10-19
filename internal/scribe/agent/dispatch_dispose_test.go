package agent

import "testing"

// DisposeLastBus must dispose the newest bus and forget it, so a closed tab's
// caches are released and CancelDispatch no longer reaches the bus.
func TestDisposeLastBusDisposesAndForgetsTheBus(t *testing.T) {
	d := &Dispatcher{}
	bus := NewAgentBus()
	d.lastBus.Store(bus)

	d.DisposeLastBus()

	if !bus.disposed {
		t.Fatal("the last bus was not disposed when its tab closed")
	}
	if d.lastBus.Load() != nil {
		t.Fatal("the dispatcher still holds the closed tab's bus")
	}
	// With nothing on record it is a no-op, not a panic.
	d.DisposeLastBus()
}
