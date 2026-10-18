package proxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"syscall"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
)

// bounceCap bounds one bounce: the restart hook and the wait for the proxy
// to answer again (spec 03 section 10). A variable so tests can shorten it.
var bounceCap = 8 * time.Second

// bounceable reports whether err is a connection failure worth a bounce. A
// caller that cancelled, and an HTTP status from the proxy, are not.
func bounceable(ctx context.Context, err error) bool {
	if err == nil || ctx.Err() != nil {
		return false
	}
	return connectionError(err)
}

// connectionError recognises a transport failure: the proxy refused the
// connection, reset it, or closed it early. Status errors from the proxy are
// not connection errors, and neither is a cancelled context.
func connectionError(err error) bool {
	var apiErr *llm.APIError
	if errors.As(err, &apiErr) {
		return false
	}
	if errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.EPIPE) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		return true
	}
	// The OpenAI adapter reports an unreachable server as text, without
	// keeping the transport error in its chain.
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "cannot reach ") ||
		strings.Contains(msg, "connection refused") ||
		strings.Contains(msg, "connection reset") ||
		strings.Contains(msg, "broken pipe") ||
		strings.HasSuffix(msg, "eof")
}

// relay forwards a stream from the route. When mayBounce is set, a connection
// error that arrives before any event was forwarded is bounced and retried
// once. After output has reached the caller a retry would repeat text, so the
// error is passed on. The first event forwarded clears the bounce budget.
// A stream that is itself a retry is relayed with mayBounce false, so one
// run never bounces twice.
func (p *Provider) relay(ctx context.Context, req llm.Request, in <-chan llm.Event, out chan<- llm.Event, mayBounce bool) {
	defer close(out)
	sent := false
	for ev := range in {
		if mayBounce && !sent && ev.Type == llm.EvError && bounceable(ctx, ev.Err) {
			retry, err := p.bounceAndRetry(ctx, req, ev.Err)
			if err != nil {
				out <- llm.Event{Type: llm.EvError, Err: err}
				return
			}
			for ev := range retry {
				out <- ev
			}
			return
		}
		if !sent {
			sent = true
			p.clearBounce()
		}
		out <- ev
	}
}

// bounceAndRetry runs the bounce path after a connection error. The health
// probe runs first. If the proxy answers, the connection just dropped and the
// request is retried on the same proxy. If nothing answers, the sidecar is
// restarted through Options.Bounce, and the request is retried once the proxy
// is healthy again. The whole bounce is capped at bounceCap. Only one bounce
// happens until a stream succeeds, and the retry itself is never bounced
// again.
func (p *Provider) bounceAndRetry(ctx context.Context, req llm.Request, cause error) (<-chan llm.Event, error) {
	bctx, cancel := context.WithTimeout(ctx, bounceCap)
	defer cancel()

	rt := p.current()
	st := Probe(bctx, p.opts.HTTPClient, rt.base, rt.key)
	if st == Unreachable {
		if p.bouncedNow() {
			return nil, unreachableErr(rt.base, cause, "it was already restarted once in this run and still does not answer")
		}
		if p.opts.Bounce == nil {
			return nil, unreachableErr(rt.base, cause, "there is no way to restart it from here")
		}
		p.markBounced()
		ep, err := p.opts.Bounce(bctx)
		if err != nil {
			return nil, fmt.Errorf("proxy: not responding at %s (%v), and the restart failed: %w", rt.base, cause, err)
		}
		if ep.BaseURL == "" {
			ep.BaseURL = rt.base
		}
		p.setRoute(ep)
		rt = p.current()
		st = WaitHealthy(bctx, p.opts.HTTPClient, rt.base, rt.key)
		if st == Unreachable {
			return nil, unreachableErr(rt.base, cause, "it did not answer after a restart")
		}
	}
	if st == NeedsAuth {
		return nil, fmt.Errorf("proxy: %s rejected the key (%v); run /proxy login to fix the provider login", rt.base, cause)
	}
	ch, err := p.open(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("proxy: retry after the connection error failed: %w", err)
	}
	out := make(chan llm.Event, 16)
	go p.relay(ctx, req, ch, out, false)
	return out, nil
}

func unreachableErr(base string, cause error, why string) error {
	return fmt.Errorf("proxy: not responding at %s (%v); %s. Start the proxy and run /proxy login", base, cause, why)
}

// setRoute swaps in the endpoint a bounce found.
func (p *Provider) setRoute(ep Endpoint) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.rt = newRoute(p.opts, ep.BaseURL, ep.APIKey)
}

func (p *Provider) markBounced() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.bounced = true
}

func (p *Provider) bouncedNow() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.bounced
}

func (p *Provider) clearBounce() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.bounced = false
}
