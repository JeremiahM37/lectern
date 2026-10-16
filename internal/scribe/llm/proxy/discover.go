package proxy

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// binaryName is the CLIProxyAPI executable as installed on PATH.
const binaryName = "cliproxyapi"

// ErrNotInstalled is returned when no CLIProxyAPI binary can be found. The
// message the user sees is the one spec 11 gives for this case.
var ErrNotInstalled = errors.New("Proxy addon not installed")

// Health poll settings from spec 11, step 4: up to 10 probes, 500 ms apart.
const (
	healthAttempts = 10
	healthInterval = 500 * time.Millisecond
)

// FindBinary returns the CLIProxyAPI executable to start. A system-installed
// binary on PATH is preferred over the vendored pin at vendored, which is only
// used when PATH has none. An empty or non-executable vendored path counts as
// absent.
func FindBinary(vendored string) (string, error) {
	if p, err := exec.LookPath(binaryName); err == nil {
		return p, nil
	}
	if vendored != "" {
		if fi, err := os.Stat(filepath.Clean(vendored)); err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0 {
			return vendored, nil
		}
	}
	return "", ErrNotInstalled
}

// WaitHealthy probes the proxy until it answers Running or NeedsAuth, which
// both mean the process is up, or until the attempts run out or ctx ends. It
// returns the last state seen. Unreachable after the last attempt means the
// caller should report that the proxy is not responding.
func WaitHealthy(ctx context.Context, client *http.Client, base, key string) State {
	return waitHealthy(ctx, client, base, key, healthAttempts, healthInterval)
}

func waitHealthy(ctx context.Context, client *http.Client, base, key string, attempts int, interval time.Duration) State {
	st := Unreachable
	for i := 0; i < attempts; i++ {
		st = Probe(ctx, client, base, key)
		if st != Unreachable {
			return st
		}
		if i == attempts-1 {
			break
		}
		t := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			t.Stop()
			return st
		case <-t.C:
		}
	}
	return st
}
