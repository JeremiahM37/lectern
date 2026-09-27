package api

import (
	"net"
	"net/http"
	"os"
	"testing"
	"time"
)

// Opt-in serving harness for the root-owned disposable systemd/bwrap proof.
// Normal suites skip it; it has no application DB or workshop state access.
func TestPythonBrokerSystemdHarness(t *testing.T) {
	socket := os.Getenv("LECTERN_PYTHON_BROKER_SOCKET")
	stop := os.Getenv("LECTERN_PYTHON_BROKER_STOP")
	if socket == "" || stop == "" {
		t.Skip("disposable real-systemd proof only")
	}
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(socket, 0666); err != nil {
		listener.Close()
		t.Fatal(err)
	}
	server := &http.Server{Handler: autoPythonDependencyBroker(), ReadHeaderTimeout: 5 * time.Second}
	go server.Serve(listener)
	defer server.Close()
	deadline := time.Now().Add(12 * time.Minute)
	for time.Now().Before(deadline) {
		if _, err = os.Stat(stop); err == nil {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("disposable broker harness timed out")
}
