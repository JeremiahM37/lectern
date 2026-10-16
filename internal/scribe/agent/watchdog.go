package agent

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// watchTick is how often the tool watchdog checks its clock.
const watchTick = 250 * time.Millisecond

// watchClock runs the tool watchdog's clock. It returns a channel that closes
// once limit has passed without a person being waited on, and a stop
// function. Ticks that happen while waits is non-zero do not count.
func watchClock(limit time.Duration, waits *atomic.Int32) (<-chan struct{}, func()) {
	fired := make(chan struct{})
	stop := make(chan struct{})
	go func() {
		t := time.NewTicker(watchTick)
		defer t.Stop()
		var active time.Duration
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				if waits.Load() == 0 {
					active += watchTick
				}
				if active >= limit {
					close(fired)
					return
				}
			}
		}
	}()
	return fired, func() { close(stop) }
}

// waitHost wraps the tool host so that the agent knows when a tool is
// waiting on a person. Approvals and questions are the only waits that the
// tool watchdog must not time.
type waitHost struct {
	tools.Host
	waits *atomic.Int32
}

func (h waitHost) Approve(ctx context.Context, a tools.Approval) tools.Decision {
	h.waits.Add(1)
	defer h.waits.Add(-1)
	return h.Host.Approve(ctx, a)
}

func (h waitHost) Ask(ctx context.Context, q tools.Question) (string, error) {
	h.waits.Add(1)
	defer h.waits.Add(-1)
	return h.Host.Ask(ctx, q)
}
