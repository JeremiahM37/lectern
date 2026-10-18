package app

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// CloseProviders must drop idle keep-alive connections on the shared transport
// that the provider clients use, and leave the server otherwise alone.
func TestCloseProvidersClosesIdleConnectionsW29(t *testing.T) {
	var closed atomic.Int32
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	srv.Config.ConnState = func(_ net.Conn, st http.ConnState) {
		if st == http.StateClosed {
			closed.Add(1)
		}
	}
	srv.Start()
	defer srv.Close()

	// The default client shares the default transport, as the providers do.
	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	// Drain the body first, or the connection is not reused and closes here.
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	time.Sleep(50 * time.Millisecond)
	if n := closed.Load(); n != 0 {
		t.Fatalf("connection closed before CloseProviders: %d", n)
	}

	b := &scribeBackend{}
	if err := b.CloseProviders(); err != nil {
		t.Fatalf("CloseProviders: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for closed.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if n := closed.Load(); n != 1 {
		t.Fatalf("idle connection still open after CloseProviders (closed=%d)", n)
	}
}

// With no background jobs there is no process group to kill, so the step
// succeeds without touching anything, including a backend with no App.
func TestKillProcessGroupsWithNoJobsW29(t *testing.T) {
	if err := (&scribeBackend{}).KillProcessGroups(); err != nil {
		t.Fatalf("no app: %v", err)
	}
	if err := (&scribeBackend{app: &App{}}).KillProcessGroups(); err != nil {
		t.Fatalf("no job table: %v", err)
	}
}
