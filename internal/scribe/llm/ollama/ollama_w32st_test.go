package ollama

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
)

// holdingServer accepts a chat request and then sends nothing more. When
// sendHeaders is set it flushes the status first, so the client is past
// Do and blocked on the body, which is the stalled-stream case.
func holdingServer(t *testing.T, sendHeaders bool) *httptest.Server {
	t.Helper()
	done := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if sendHeaders {
			w.Header().Set("Content-Type", "application/x-ndjson")
			w.WriteHeader(http.StatusOK)
			w.(http.Flusher).Flush()
		}
		select {
		case <-done:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(srv.Close)
	// Cleanups run last in first out, so done closes before srv.Close waits
	// on the handler.
	t.Cleanup(func() { close(done) })
	return srv
}

// cancelReturnsWithin cancels ctx after a short wait and fails the test
// unless the stream's channel closes within limit.
func cancelReturnsWithin(t *testing.T, ch <-chan llm.Event, cancel context.CancelFunc, limit time.Duration) {
	t.Helper()
	time.Sleep(50 * time.Millisecond)
	start := time.Now()
	cancel()
	timeout := time.After(limit)
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				if took := time.Since(start); took > limit {
					t.Fatalf("stream closed after %v, want within %v", took, limit)
				}
				return
			}
		case <-timeout:
			t.Fatalf("stream still open %v after cancel: a stalled read ignores the context", limit)
		}
	}
}

// A server that has sent its headers and then holds the body must not keep
// the turn alive after the request is cancelled. Esc and Ctrl+X depend on
// this.
func TestCancelUnblocksStalledBody(t *testing.T) {
	srv := holdingServer(t, true)
	p := New(Options{BaseURL: srv.URL})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, err := p.Stream(ctx, llm.Request{Model: "m"})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	cancelReturnsWithin(t, ch, cancel, time.Second)
}

// A server that holds before it sends any headers is cancelled through the
// request itself. This case was already prompt, and it stays so.
func TestCancelUnblocksSilentServer(t *testing.T) {
	srv := holdingServer(t, false)
	p := New(Options{BaseURL: srv.URL})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errc := make(chan error, 1)
	go func() {
		_, err := p.Stream(ctx, llm.Request{Model: "m"})
		errc <- err
	}()
	time.Sleep(50 * time.Millisecond)
	start := time.Now()
	cancel()
	select {
	case err := <-errc:
		if err == nil {
			t.Fatal("a cancelled request returned no error")
		}
		if took := time.Since(start); took > time.Second {
			t.Fatalf("Stream returned after %v, want within 1s", took)
		}
	case <-time.After(time.Second):
		t.Fatal("Stream still blocked 1s after cancel")
	}
}

// Ending the stream normally must not leave the watcher holding the body:
// a finished response is read to its done line and closes cleanly.
func TestNormalFinishDoesNotNeedCancel(t *testing.T) {
	srv, _ := serve(t, http.StatusOK, `{"message":{"content":"hi"},"done":false}`+"\n"+`{"message":{"content":""},"done":true,"done_reason":"stop","eval_count":1}`+"\n")
	p := New(Options{BaseURL: srv.URL})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, err := p.Stream(ctx, llm.Request{Model: "m"})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	evs := collect(t, ch)
	if len(evs) == 0 || evs[len(evs)-1].Type != llm.EvFinish {
		t.Fatalf("stream did not finish: %+v", evs)
	}
}
