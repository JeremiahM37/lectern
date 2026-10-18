package agent

import (
	"context"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
)

// deafProvider streams nothing and never closes its channel, even when the
// request is cancelled: the worst case of a slow server.
type deafProvider struct{}

func (deafProvider) ID() string { return "deaf" }
func (deafProvider) Stream(context.Context, llm.Request) (<-chan llm.Event, error) {
	return make(chan llm.Event), nil
}
func (deafProvider) Models(context.Context) ([]llm.ModelInfo, error) { return nil, nil }

// Esc must end the turn at once even when the provider ignores the cancel.
func TestCancelEndsATurnWhoseProviderIgnoresIt(t *testing.T) {
	opts := baseOptions(newFake(t), lookRegistry(), &eventLog{}, t.TempDir())
	opts.Model = Model{Ref: "deaf/m", Provider: deafProvider{}, Info: llm.ModelInfo{ID: "m"}}
	opts.StallTimeout = time.Minute
	opts.FirstTokenTimeout = time.Minute
	a := New(opts)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_, _ = a.Run(ctx, "hello", nil)
		close(done)
	}()
	time.Sleep(200 * time.Millisecond)
	start := time.Now()
	cancel()
	select {
	case <-done:
		if d := time.Since(start); d > 2*time.Second {
			t.Fatalf("the turn took %s to stop", d)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the turn did not stop after cancel")
	}
}
