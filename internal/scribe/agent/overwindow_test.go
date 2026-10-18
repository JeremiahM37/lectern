package agent

import (
	"context"
	"strings"
	"testing"
)

// A request that clearly does not fit the window is not sent: a server that
// cuts it silently would leave the model without its instructions.
func TestOversizedRequestStopsWithAClearMessage(t *testing.T) {
	f := newFake(t)
	f.setPick(scripted(t, turn(sseTurn{text: []string{"should not be asked"}})))
	opts := baseOptions(f, lookRegistry(), &eventLog{}, t.TempDir())
	opts.System = func(string) string { return strings.Repeat("instructions ", 2000) } // ~6.5k tokens
	a := New(opts)
	a.model.Info.ContextWindow = 1000
	stats, err := a.Run(context.Background(), "hello", nil)
	msg := stats.Error
	if err != nil {
		msg += err.Error()
	}
	if !strings.Contains(msg, "larger than this model's context window") {
		t.Fatalf("want the window message, got stats.Error=%q err=%v", stats.Error, err)
	}
	if f.count() != 0 {
		t.Fatalf("the oversized request was sent %d times", f.count())
	}
}

func TestHumanK(t *testing.T) {
	for n, want := range map[int]string{900: "900", 8192: "8.2k", 8000: "8k", 24500: "24k"} {
		if got := humanK(n); got != want {
			t.Errorf("humanK(%d) = %q, want %q", n, got, want)
		}
	}
}
