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

type tunnelKey struct{}

// TunnelResolver answers, for one request, which paired relay device sent it
// and whether that device is still allowed in. It is consulted per request, so
// a revocation takes effect on the very next request of a live connection.
type TunnelResolver func() (Principal, bool)

// WithTunnel marks a connection as coming through the end-to-end encrypted
// relay. Only the relay host's in-process listener calls this, after a Noise
// handshake with a paired key: it is a Go context value, so no header, address
// or other request content can produce it. Authenticate trusts nothing else
// about such a request.
func WithTunnel(ctx context.Context, resolve TunnelResolver) context.Context {
	return context.WithValue(ctx, tunnelKey{}, resolve)
}

func tunnelFrom(ctx context.Context) (TunnelResolver, bool) {
	resolve, ok := ctx.Value(tunnelKey{}).(TunnelResolver)
	return resolve, ok && resolve != nil
}
