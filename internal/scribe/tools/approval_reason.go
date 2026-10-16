package tools

import (
	"context"
	"strings"
	"sync"
)

// A Host that denies (or approves with a note) can say why. The reason
// travels on the context the caller passed to Approve, so the caller can add
// it to the tool result the model reads. Hosts that have no reason never
// touch it, and callers that do not read it lose nothing.

type reasonKey struct{}

type reasonSlot struct {
	mu     sync.Mutex
	reason string
}

// WithReasonSlot returns a context that carries a reason slot, and a function
// that returns the reason set on it ("" when none). Create it before calling
// Host.Approve, and read it after.
func WithReasonSlot(ctx context.Context) (context.Context, func() string) {
	s := &reasonSlot{}
	return context.WithValue(ctx, reasonKey{}, s), func() string {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.reason
	}
}

// SetApprovalReason records the reason a Host gives for its answer. It is a
// no-op when the context has no slot. Long text is cut to 500 bytes.
func SetApprovalReason(ctx context.Context, reason string) {
	s, ok := ctx.Value(reasonKey{}).(*reasonSlot)
	if !ok {
		return
	}
	reason = strings.TrimSpace(reason)
	if len(reason) > 500 {
		reason = reason[:500] + "…"
	}
	s.mu.Lock()
	s.reason = reason
	s.mu.Unlock()
}

// DeniedWithReason is the tail a caller appends to a denial message: the
// reason the Host gave (the person's words, or why the phone side failed),
// otherwise nothing.
func DeniedWithReason(reason string) string {
	if reason == "" {
		return ""
	}
	return " Reason: " + reason
}
