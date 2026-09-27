package main

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestPlainCommandsPreferHostedService runs the real binary with both a real
// hosted server (`lectern serve`, mock mode so its health says "mock":true)
// and the real private local runtime ("mock":false) on the same machine.
func TestPlainCommandsPreferHostedService(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("local runtime currently uses POSIX process locks")
	}
	bin := filepath.Join(t.TempDir(), "lectern")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build CLI: %v\n%s", err, out)
	}
	state := t.TempDir()
	base := localTestEnv(state)
	t.Cleanup(func() { _, _ = runLocalCLI(bin, base, "local", "stop") })

	open := startHostedServe(t, bin, base, "")
	withOpen := append(append([]string{}, base...), "LECTERN_PORT="+strconv.Itoa(open))

	// No local runtime yet: the service is chosen and nothing local starts.
	out, _, err := runCLISplit(bin, withOpen, "api", "GET", "/health")
	if err != nil || !bytes.Contains(out, []byte(`"mock":true`)) {
		t.Fatalf("plain command did not reach the hosted service: err=%v out=%s", err, out)
	}
	if status, _ := runLocalCLI(bin, withOpen, "local", "status"); !bytes.Contains(status, []byte(`"state": "stopped"`)) {
		t.Fatalf("choosing the service started the local runtime: %s", status)
	}

	// `lectern local …` still forces the private runtime.
	out, _, err = runCLISplit(bin, withOpen, "local", "api", "GET", "/health")
	if err != nil || !bytes.Contains(out, []byte(`"mock":false`)) {
		t.Fatalf("local command did not reach the local runtime: err=%v out=%s", err, out)
	}
	// Both running: plain still goes to the service.
	out, _, err = runCLISplit(bin, withOpen, "api", "GET", "/health")
	if err != nil || !bytes.Contains(out, []byte(`"mock":true`)) {
		t.Fatalf("plain command left the service once local was running: err=%v out=%s", err, out)
	}
	doctor, _, _ := runCLISplit(bin, withOpen, "doctor")
	if !bytes.Contains(doctor, []byte("[OK] server — Lectern service at http://127.0.0.1:"+strconv.Itoa(open))) ||
		!bytes.Contains(doctor, []byte("[OK] local runtime — running")) {
		t.Fatalf("doctor did not report the choice:\n%s", doctor)
	}

	// A token-mode service refuses a CLI with no token: the choice falls to
	// local and says why; with the token it is the service.
	guarded := startHostedServe(t, bin, base, "secret")
	withGuarded := append(append([]string{}, base...), "LECTERN_PORT="+strconv.Itoa(guarded))
	out, stderr, err := runCLISplit(bin, withGuarded, "api", "GET", "/health")
	if err != nil || !bytes.Contains(out, []byte(`"mock":false`)) || !strings.Contains(stderr, "needs LECTERN_AUTH_TOKEN") {
		t.Fatalf("refusing service: err=%v out=%s stderr=%s", err, out, stderr)
	}
	out, _, err = runCLISplit(bin, append(withGuarded, "LECTERN_AUTH_TOKEN=secret"), "api", "GET", "/health")
	if err != nil || !bytes.Contains(out, []byte(`"mock":true`)) {
		t.Fatalf("token did not reach the service: err=%v out=%s", err, out)
	}

	// Nothing on the configured port: the private runtime.
	out, _, err = runCLISplit(bin, append(append([]string{}, base...), "LECTERN_PORT="+strconv.Itoa(freeTCPPort(t))), "api", "GET", "/health")
	if err != nil || !bytes.Contains(out, []byte(`"mock":false`)) {
		t.Fatalf("no service: err=%v out=%s", err, out)
	}
}

// startHostedServe runs `lectern serve` on a free loopback port with its own
// database, and stops it when the test ends.
func startHostedServe(t *testing.T, bin string, env []string, token string) int {
	t.Helper()
	port := freeTCPPort(t)
	env = append(append([]string{}, env...),
		"LECTERN_PORT="+strconv.Itoa(port), "LECTERN_HOST=127.0.0.1",
		"LECTERN_DB="+filepath.Join(t.TempDir(), "hosted.db"))
	if token != "" {
		env = append(env, "LECTERN_AUTH=token", "LECTERN_AUTH_TOKEN="+token)
	}
	cmd := exec.Command(bin, "serve")
	cmd.Env = env
	cmd.Dir = t.TempDir()
	var logs bytes.Buffer
	cmd.Stdout, cmd.Stderr = &logs, &logs
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		<-done
	})
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		res, err := http.Get("http://127.0.0.1:" + strconv.Itoa(port) + "/.well-known/agent-card.json")
		if err == nil {
			res.Body.Close()
			if res.StatusCode == http.StatusOK {
				return port
			}
		}
		select {
		case <-done:
			t.Fatalf("hosted serve exited:\n%s", logs.String())
		case <-time.After(100 * time.Millisecond):
		}
	}
	t.Fatalf("hosted serve did not start:\n%s", logs.String())
	return 0
}

func freeTCPPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func runCLISplit(bin string, env []string, args ...string) ([]byte, string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = env
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	return out, stderr.String(), err
}
