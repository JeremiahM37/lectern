package drivers

import (
	"context"

	"github.com/JeremiahM37/lectern/v2/internal/agents"
)

// noticeHandle wraps a Handle to prepend one synthetic timeline event before
// relaying everything else unchanged. Used when codex-appserver falls back to
// codex-exec: the operator should see why a task that asked for gated
// approvals is running in bypass mode instead, not just silently get one.
type noticeHandle struct {
	inner Handle
	text  string
	out   chan agents.Event
}

func withNotice(inner Handle, text string) Handle {
	h := &noticeHandle{inner: inner, text: text, out: make(chan agents.Event, 64)}
	go h.relay()
	return h
}

func (h *noticeHandle) relay() {
	defer close(h.out)
	h.out <- agents.Event{Type: "raw", Payload: map[string]any{"line": h.text}}
	for ev := range h.inner.Events() {
		h.out <- ev
	}
}

func (h *noticeHandle) Events() <-chan agents.Event { return h.out }
func (h *noticeHandle) Send(ctx context.Context, text string) error {
	return h.inner.Send(ctx, text)
}
func (h *noticeHandle) Cancel(ctx context.Context) error { return h.inner.Cancel(ctx) }
func (h *noticeHandle) Wait(ctx context.Context) (Result, error) {
	return h.inner.Wait(ctx)
}
