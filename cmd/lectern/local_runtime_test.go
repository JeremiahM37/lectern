package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/internal/testutil"
)

func TestLocalRuntimeRealProcessPersistenceAndConcurrency(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("local runtime currently uses POSIX process locks")
	}
	bin := filepath.Join(t.TempDir(), "lectern")
	build := exec.Command("go", "build", "-o", bin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build local CLI: %v\n%s", err, out)
	}
	state := t.TempDir()
	env := localTestEnv(state)
	t.Cleanup(func() { _, _ = runLocalCLI(bin, env, "local", "stop") })
	fixtureDir := t.TempDir()
	fixtureSocket := filepath.Join(fixtureDir, "fixture")
	if _, err := exec.LookPath("tmux"); err == nil {
		fixture := exec.Command("tmux", "-S", fixtureSocket, "new-session", "-d", "-s", "lec-s1", "sleep", "60")
		if out, err := fixture.CombinedOutput(); err != nil {
			t.Fatalf("create isolated fixture tmux session: %v (%s)", err, out)
		}
		t.Cleanup(func() { testutil.CleanupTmuxSocket(t, fixtureSocket) })
	}

	if out, err := runLocalCLI(bin, env, "local", "--help"); err != nil || !bytes.Contains(out, []byte("lectern local status")) || !bytes.Contains(out, []byte("lectern local [COMMAND ...]")) {
		t.Fatalf("local help: err=%v output=%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(state, "lectern", "local")); !os.IsNotExist(err) {
		t.Fatalf("local help touched runtime state: %v", err)
	}

	var wg sync.WaitGroup
	outs := make([][]byte, 2)
	errs := make([]error, 2)
	for i := range outs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			o, e := runLocalCLI(bin, env, "api", "GET", "/health")
			outs[i], errs[i] = o, e
		}(i)
	}
	wg.Wait()
	for i := range outs {
		if errs[i] != nil || !bytes.Contains(outs[i], []byte(`"mock":false`)) {
			t.Fatalf("concurrent local API %d: err=%v output=%s", i, errs[i], outs[i])
		}
	}
	endpointPath := filepath.Join(state, "lectern", "local", "endpoint.json")
	var endpoint struct {
		URL   string `json:"url"`
		Token string `json:"token"`
		PID   int    `json:"pid"`
	}
	data, err := os.ReadFile(endpointPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &endpoint); err != nil || endpoint.Token == "" || endpoint.PID <= 0 {
		t.Fatalf("invalid endpoint: %s (%v)", data, err)
	}
	if runtime.GOOS == "linux" && strings.Contains(string(readProcCmdline(t, endpoint.PID)), endpoint.Token) {
		t.Fatal("local bearer token leaked into engine argv")
	}
	if mode := os.FileMode(mustStat(t, endpointPath).Mode()); mode.Perm() != 0o600 {
		t.Fatalf("endpoint mode %o, want 600", mode.Perm())
	}
	firstPID := endpoint.PID
	var secondWG sync.WaitGroup
	for i := 0; i < 2; i++ {
		secondWG.Add(1)
		go func() {
			defer secondWG.Done()
			_, _ = runLocalCLI(bin, env, "api", "GET", "/health")
		}()
	}
	secondWG.Wait()
	data, err = os.ReadFile(endpointPath)
	if err != nil {
		t.Fatal(err)
	}
	var concurrentEndpoint struct {
		PID int `json:"pid"`
	}
	if err := json.Unmarshal(data, &concurrentEndpoint); err != nil || concurrentEndpoint.PID != firstPID {
		t.Fatalf("concurrent reuse changed engine identity: first=%d current=%d", firstPID, concurrentEndpoint.PID)
	}
	firstURL := endpoint.URL

	targets, err := runLocalCLI(bin, env, "api", "GET", "/targets")
	if err != nil || !bytes.Contains(targets, []byte(`"name":"local"`)) || !bytes.Contains(targets, []byte(filepath.ToSlash(filepath.Join(state, "lectern", "local", "worktrees")))) {
		t.Fatalf("local target seed: err=%v output=%s", err, targets)
	}
	if _, err := exec.LookPath("tmux"); err == nil {
		if err := exec.Command("tmux", "-S", fixtureSocket, "has-session", "-t", "=lec-s1").Run(); err != nil {
			t.Fatalf("local runtime affected unrelated tmux namespace: %v", err)
		}
	}
	attachOutput, attachErr := runLocalCLI(bin, env, "attach", "session", "999999")
	if attachErr == nil || bytes.Contains(attachOutput, []byte("unknown client command")) {
		t.Fatalf("local attach did not reach the local attachment API: err=%v output=%s", attachErr, attachOutput)
	}
	if out, err := runLocalCLI(bin, env, "local", "stop"); err != nil {
		t.Fatalf("local stop: %v (%s)", err, out)
	}
	status, err := runLocalCLI(bin, env, "local", "status")
	if err != nil || !bytes.Contains(status, []byte(`"state": "stopped"`)) {
		t.Fatalf("stopped status: err=%v output=%s", err, status)
	}
	if _, err := runLocalCLI(bin, env, "api", "GET", "/targets"); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(endpointPath)
	if err != nil {
		t.Fatal(err)
	}
	var restarted struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(data, &restarted); err != nil || restarted.URL != firstURL {
		t.Fatalf("runtime did not retain loopback port: old=%s new=%s", firstURL, restarted.URL)
	}
	_, _ = runLocalCLI(bin, env, "local", "stop")
}

func TestExplicitRemoteFailureDoesNotFallbackToLocal(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("local runtime currently uses POSIX process locks")
	}
	bin := filepath.Join(t.TempDir(), "lectern")
	build := exec.Command("go", "build", "-o", bin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build local CLI: %v\n%s", err, out)
	}
	state := t.TempDir()
	env := append(localTestEnv(state), "LECTERN_API=http://127.0.0.1:1")
	if _, err := runLocalCLI(bin, env, "api", "GET", "/health"); err == nil {
		t.Fatal("explicit remote unexpectedly succeeded")
	}
	if _, err := os.Stat(filepath.Join(state, "lectern", "local")); !os.IsNotExist(err) {
		t.Fatalf("explicit remote failure started local runtime: %v", err)
	}
}

func TestHostedAttachMarkerReachesHostedLookup(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("hosted attachment uses POSIX terminal launch")
	}
	bin := filepath.Join(t.TempDir(), "lectern")
	build := exec.Command("go", "build", "-o", bin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build local CLI: %v\n%s", err, out)
	}
	state := t.TempDir()
	env := localTestEnv(state)
	env = append(env, "LECTERN_PORT=1")
	out, err := runLocalCLI(bin, env, "--hosted-attach", "attach", "session", "17")
	if err == nil {
		t.Fatal("hosted attachment unexpectedly connected")
	}
	if bytes.Contains(out, []byte("usage: --hosted-attach")) {
		t.Fatalf("hosted marker was parsed at the wrong argv offset: %s", out)
	}
	if _, statErr := os.Stat(filepath.Join(state, "lectern", "local")); !os.IsNotExist(statErr) {
		t.Fatalf("hosted attachment started local runtime: %v", statErr)
	}
}

func localTestEnv(state string) []string {
	blocked := map[string]bool{}
	for _, key := range []string{"LECTERN_API", "LECTERN_ATTACH_HOST", "LECTERN_DB", "LECTERN_HOST", "LECTERN_PORT", "LECTERN_BASE_URL", "LECTERN_AUTH_TOKEN", "LECTERN_MOCK", "LECTERN_GRIMOIRE_URL", "LECTERN_GRIMOIRE_TOKEN", "LECTERN_HOST_CLAUDE_CONFIG", "LECTERN_CREDS", "LECTERN_CODEX_CREDS", "LECTERN_ANTHROPIC_API_KEY", "XDG_STATE_HOME"} {
		blocked[key] = true
	}
	base := make([]string, 0, len(os.Environ())+2)
	for _, item := range os.Environ() {
		key, _, _ := strings.Cut(item, "=")
		if !blocked[key] {
			base = append(base, item)
		}
	}
	return append(base, "XDG_STATE_HOME="+state, "LECTERN_MOCK=1")
}

func runLocalCLI(bin string, env []string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = env
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil && stderr.Len() > 0 {
		return append(out, stderr.Bytes()...), fmt.Errorf("%w: %s", err, stderr.String())
	}
	return out, err
}

func mustStat(t *testing.T, path string) os.FileInfo {
	t.Helper()
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func readProcCmdline(t *testing.T, pid int) []byte {
	t.Helper()
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil {
		t.Fatal(err)
	}
	return b
}
