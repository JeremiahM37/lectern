package isolation

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/shellq"
)

// requireBwrap skips the test unless bwrap is on PATH and this exact
// environment can actually nest a user+pid namespace — tools/run-isolated-
// tests.sh already runs the whole suite inside one bwrap sandbox, so these
// tests are themselves testing a second, nested layer. Per the isolation
// task's own instruction, that is probed here rather than assumed.
func requireBwrap(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("bwrap is Linux-only")
	}
	if _, err := exec.LookPath("bwrap"); err != nil {
		t.Skip("bwrap is not installed on this host")
	}
	// Build the probe from the real profile (BuildBwrapArgv), not a
	// hand-trimmed approximation of it: a minimal "just /usr" bind looks
	// equivalent but is not — on a non-usrmerged host the dynamic linker
	// lives under /lib64, outside /usr, and its absence turns into the exact
	// same ENOENT a genuinely broken nesting environment would produce. Only
	// the full profile tells the two apart.
	dir := t.TempDir()
	wrapped, err := Wrap("true", Config{Mode: Bwrap}, WrapOpts{Agent: "probe", Workdir: dir})
	if err != nil {
		t.Fatalf("building the probe command: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(ctx, "bash", "-c", wrapped).CombinedOutput(); err != nil {
		t.Skipf("nested bwrap does not work in this environment (%v): %s", err, out)
	}
}

func requireSocat(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("socat"); err != nil {
		t.Skip("socat is not installed on this host — required for network=deny's bridge")
	}
}

// TestRealBwrapFilesystemIsolation runs a real bwrap sandbox (item 5 of the
// isolation task: "a stub agent writes a file in the worktree, cannot read a
// file outside it"). No provider CLI is invoked and no network is touched —
// this is purely the mount-namespace guarantee BuildBwrapArgv is supposed to
// deliver.
func TestRealBwrapFilesystemIsolation(t *testing.T) {
	requireBwrap(t)

	workdir := t.TempDir()
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("do not leak me"), 0o600); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(workdir, "marker.txt")

	invocation := fmt.Sprintf(
		"echo written by the sandboxed stub agent > %s\n"+
			"if cat %s >/dev/null 2>&1; then echo RESULT=LEAKED; else echo RESULT=BLOCKED; fi",
		shellq.Quote(marker), shellq.Quote(secret))

	wrapped, err := Wrap(invocation, Config{Mode: Bwrap}, WrapOpts{Agent: "stub-agent", Workdir: workdir})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "bash", "-c", wrapped).CombinedOutput()
	if err != nil {
		t.Fatalf("sandboxed run failed: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "RESULT=BLOCKED") {
		t.Fatalf("expected the file outside the workdir to be unreachable, got:\n%s", out)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("expected the sandboxed process to write inside the bind-mounted workdir: %v", err)
	}
}

// TestRealBwrapNetworkDeny runs a real --unshare-net sandbox and proves both
// halves of item 5's "cannot reach a denied host": a direct connection
// attempt (bypassing the bridged proxy entirely) fails outright, and the one
// bridged path — through the bind-mounted proxy socket — succeeds for an
// allowed host.
func TestRealBwrapNetworkDeny(t *testing.T) {
	requireBwrap(t)
	requireSocat(t)

	// A listener on the *host's* real loopback. Once --unshare-net applies,
	// the sandbox gets its own private lo; this address must be completely
	// unreachable from inside, proxy or no proxy.
	direct, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer direct.Close()
	go func() {
		for {
			c, err := direct.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	_, directPort, _ := net.SplitHostPort(direct.Addr().String())

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "allowed-upstream-ok")
	}))
	defer upstream.Close()
	upstreamHost := strings.TrimPrefix(upstream.URL, "http://")

	runtimeDir := t.TempDir()
	socketPath := filepath.Join(runtimeDir, "proxy.sock")
	pl, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer pl.Close()
	proxy := NewAllowlistProxy([]string{upstreamHost})
	go proxy.Serve(pl)

	workdir := t.TempDir()
	// A couple of retries on the proxied request cost nothing and make this
	// a fair fight: the DIRECT check below has no connection to retry — it
	// is supposed to fail outright — so it never flakes on its own.
	script := fmt.Sprintf(`
if (exec 9<>/dev/tcp/127.0.0.1/%s) 2>/dev/null; then
  echo DIRECT=REACHABLE
else
  echo DIRECT=BLOCKED
fi
response=""
for _i in $(seq 1 5); do
  if exec 8<>/dev/tcp/127.0.0.1/%s 2>/dev/null; then
    printf 'GET http://%s/ok HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n' >&8
    response=$(cat <&8)
    exec 8<&-
  fi
  echo "$response" | grep -q allowed-upstream-ok && break
  sleep 0.1
done
if echo "$response" | grep -q allowed-upstream-ok; then
  echo PROXIED=OK
else
  echo PROXIED=FAILED
fi
`, directPort, proxyBridgeTCPPort, upstreamHost, upstreamHost)

	wrapped, err := Wrap(script, Config{Mode: Bwrap, Network: NetworkDeny},
		WrapOpts{Agent: "stub-agent", Workdir: workdir, ProxySocket: socketPath})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "bash", "-c", wrapped).CombinedOutput()
	if err != nil {
		t.Fatalf("sandboxed run failed: %v\n%s", err, out)
	}
	got := string(out)
	if !strings.Contains(got, "DIRECT=BLOCKED") {
		t.Errorf("expected the direct connection (bypassing the proxy) to fail, got:\n%s", got)
	}
	if !strings.Contains(got, "PROXIED=OK") {
		t.Errorf("expected the allowlisted host to be reachable through the bridged proxy, got:\n%s", got)
	}
}
