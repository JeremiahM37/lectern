package drivers

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/agents"
	"github.com/JeremiahM37/lectern/v2/internal/executor"
)

// Hold the prompt's FIFO write across the process exit. This guarantees the
// exact failing ordering: watchExit closes Events before runPrompt emits its
// final error, independently of scheduler timing or a real tmux process.
type acpHeldWrite struct {
	executor.Executor
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (e *acpHeldWrite) Run(context.Context, string, executor.RunOpts) (executor.Result, error) {
	e.once.Do(func() { close(e.entered) })
	<-e.release
	return executor.Result{}, errors.New("agent exited during FIFO write")
}
func (*acpHeldWrite) ReadFile(context.Context, string, int64) ([]byte, error) {
	return []byte("0\n"), nil
}

func TestACPExitWhilePromptWriteIsReturning(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		name := "process_exit"
		if cancelled {
			name = "context_cancel"
		}
		t.Run(name, func(t *testing.T) {
			ex := &acpHeldWrite{entered: make(chan struct{}), release: make(chan struct{})}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			r := &acpRun{ex: ex, rt: "/unused", interval: time.Millisecond,
				pending: map[int64]chan rpcResult{}, events: make(chan agents.Event, 8), done: make(chan struct{}),
				baseCtx: ctx, cancelLoop: func() {}, active: true, queue: []string{"must not launch after exit"}}
			promptDone := make(chan struct{})
			go func() { defer close(promptDone); r.runPrompt(ctx, "first prompt") }()
			select {
			case <-ex.entered:
			case <-time.After(time.Second):
				t.Fatal("prompt write did not start")
			}
			if cancelled {
				cancel()
			}
			go r.watchExit(ctx)
			select {
			case <-r.done:
			case <-time.After(time.Second):
				t.Fatal("exit waited on blocked prompt")
			}
			if _, open := <-r.Events(); open {
				t.Fatal("event stream not closed after exit")
			}
			// Poll handlers can also return after cancellation; they must not send on
			// the closed stream, and Send must not start a fresh prompt after shutdown.
			r.handleLine("late malformed protocol output")
			if err := r.Send(context.Background(), "late steering"); err == nil {
				t.Fatal("accepted steering after exit")
			}
			if _, err := r.request(context.Background(), "session/prompt", nil, time.Hour); err == nil {
				t.Fatal("accepted request after final pending drain")
			}
			close(ex.release)
			select {
			case <-promptDone:
			case <-time.After(time.Second):
				t.Fatal("prompt or queued followup leaked after exit")
			}
			r.mu.Lock()
			defer r.mu.Unlock()
			if len(r.pending) != 0 || len(r.queue) != 0 || r.active {
				t.Fatal("shutdown retained active/queued RPC state")
			}
		})
	}
}

func TestACPEventClosureRacesWithProducers(t *testing.T) {
	r := &acpRun{events: make(chan agents.Event, 8)}
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for j := 0; j < 100; j++ {
				r.emit(agents.Event{Type: "text"})
			}
		}()
	}
	close(start)
	r.closeEvents()
	wg.Wait()
	for range r.Events() {
	}
}
