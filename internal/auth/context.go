package auth

import "context"

type ctxKey struct{}

// WithPrincipal attaches the resolved principal to a request context.
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, ctxKey{}, p)
}

// FromContext reads back a principal attached by WithPrincipal. ok is false
// for a context that never went through the auth middleware (e.g. a hook
// request, which is exempt and authenticates its own way).
func FromContext(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(ctxKey{}).(Principal)
	return p, ok
}
