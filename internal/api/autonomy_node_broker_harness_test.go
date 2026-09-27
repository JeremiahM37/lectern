package api

import (
	"net"
	"net/http"
	"os"
	"testing"
	"time"
)

// Explicit fixed-registry socket for disposable systemd/namespace proofs only.
// No application database or live worker is touched by this harness.
func TestNodeBrokerSystemdHarness(t *testing.T) {
	socket, stop := os.Getenv("LECTERN_NODE_BROKER_SOCKET"), os.Getenv("LECTERN_NODE_BROKER_STOP")
	if socket == "" || stop == "" {
		t.Skip("disposable Node provisioner proof only")
	}
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(socket, 0666); err != nil {
		listener.Close()
		t.Fatal(err)
	}
	server := &http.Server{Handler: autoNodeDependencyBroker(), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10}
	go server.Serve(listener)
	defer server.Close()
	deadline := time.Now().Add(12 * time.Minute)
	for time.Now().Before(deadline) {
		if _, err = os.Stat(stop); err == nil {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("disposable Node broker harness timed out")
}
