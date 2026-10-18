package proxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
)

// The sidecar tests run a fake proxy: a copy of this test binary named
// cliproxyapi. When it is started under that name, TestMain runs the fake
// instead of the tests. No real CLIProxyAPI is needed, and no process that a
// test did not start is ever signalled.
func TestMain(m *testing.M) {
	if filepath.Base(os.Args[0]) == binaryName {
		runFakeProxy(os.Args)
		return
	}
	os.Exit(m.Run())
}

// runFakeProxy serves /v1/models and /v1/chat/completions on the port and with
// the keys in the -config file it is given. FAKE_PROXY_HANG makes it start and
// then never listen, which is a sidecar that is alive but not answering.
func runFakeProxy(args []string) {
	cfgPath := ""
	for i := 1; i+1 < len(args); i++ {
		if args[i] == "-config" {
			cfgPath = args[i+1]
		}
	}
	if os.Getenv("FAKE_PROXY_HANG") != "" {
		for {
			time.Sleep(time.Hour)
		}
	}
	cfg, ok, err := loadSidecarConfig(cfgPath)
	if err != nil || !ok {
		fmt.Fprintln(os.Stderr, "fake proxy: no config", err)
		os.Exit(1)
	}
	ln, err := net.Listen("tcp", net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port)))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	authorised := func(r *http.Request) bool {
		for _, k := range cfg.APIKeys {
			if r.Header.Get("Authorization") == "Bearer "+k {
				return true
			}
		}
		return false
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) {
		if !authorised(r) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		fmt.Fprint(w, `{"data":[]}`)
	})
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		if !authorised(r) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n")
	})
	_ = http.Serve(ln, mux)
	os.Exit(0)
}

// rig is one test's world: its own Scribe home, auth dir, PATH and port.
type rig struct {
	t      *testing.T
	home   string // Scribe home; the sidecar keeps proxy/ under it
	binary string // the fake proxy
	port   int
	base   string
	opts   SidecarOptions
}

func newRig(t *testing.T) *rig {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("sidecar process checks read /proc")
	}
	// No cliproxyapi on PATH, so the vendored copy is the only binary found.
	t.Setenv("PATH", t.TempDir())
	t.Setenv("PROXY_API_KEY", "")
	t.Setenv("SCRIBE_HOME", t.TempDir())
	r := &rig{t: t, home: os.Getenv("SCRIBE_HOME"), binary: copyFakeProxy(t), port: freePort(t)}
	r.base = fmt.Sprintf("http://127.0.0.1:%d/v1", r.port)
	r.opts = SidecarOptions{
		BaseURL:    r.base,
		HTTPClient: &http.Client{Timeout: 2 * time.Second},
		Home:       filepath.Join(r.home, "proxy"),
		AuthDir:    filepath.Join(t.TempDir(), "auth"),
		Vendored:   r.binary,
	}
	return r
}

func (r *rig) sidecar() *Sidecar { return NewSidecar(r.opts) }

func (r *rig) configPath() string { return filepath.Join(r.opts.Home, "config.yaml") }
func (r *rig) pidPath() string    { return filepath.Join(r.opts.Home, "sidecar.pid") }

func (r *rig) writeConfig(port int, keys ...string) {
	r.t.Helper()
	if err := writeSidecarConfig(r.configPath(), sidecarConfig{Host: "127.0.0.1", Port: port, AuthDir: r.opts.AuthDir, APIKeys: keys}); err != nil {
		r.t.Fatal(err)
	}
}

// fakeProc is a fake proxy the test started itself, with its exit channel.
type fakeProc struct {
	pid  int
	done chan struct{}
}

// startFake runs the fake proxy directly, not through the sidecar. env adds
// variables for this process only, so no test environment is changed.
func (r *rig) startFake(env ...string) *fakeProc {
	r.t.Helper()
	cmd := exec.Command(r.binary, "-config", r.configPath())
	cmd.Env = append(os.Environ(), env...)
	if err := cmd.Start(); err != nil {
		r.t.Fatal(err)
	}
	p := &fakeProc{pid: cmd.Process.Pid, done: make(chan struct{})}
	go func() { _ = cmd.Wait(); close(p.done) }()
	r.t.Cleanup(func() {
		_ = killPID(p.pid)
		<-p.done
	})
	return p
}

func (p *fakeProc) alive() bool {
	select {
	case <-p.done:
		return false
	default:
		return true
	}
}

// recordFor writes a pid record by hand, as a dead or other Scribe would have.
func (r *rig) recordFor(pid int, owner int, binary string, start uint64) {
	r.t.Helper()
	if err := writeRecord(r.pidPath(), sidecarRecord{PID: pid, Start: start, Owner: owner, Binary: binary}); err != nil {
		r.t.Fatal(err)
	}
}

// deadPID runs the fake with no arguments, so it exits at once, and returns the
// pid of a process that has exited and been reaped.
func (r *rig) deadPID() int {
	r.t.Helper()
	cmd := exec.Command(r.binary)
	if err := cmd.Run(); err == nil {
		r.t.Fatal("fake proxy with no config should exit with an error")
	}
	return cmd.Process.Pid
}

func (r *rig) resolvedBinary() string {
	r.t.Helper()
	p, err := filepath.EvalSymlinks(r.binary)
	if err != nil {
		r.t.Fatal(err)
	}
	return p
}

func (r *rig) startTicksOf(pid int) uint64 {
	r.t.Helper()
	st, err := startTicks(pid)
	if err != nil {
		r.t.Fatal(err)
	}
	return st
}

func (r *rig) waitUp(key string) {
	r.t.Helper()
	if st := WaitHealthy(context.Background(), r.opts.HTTPClient, r.base, key); st != Running {
		r.t.Fatalf("proxy state %s, want running", st)
	}
}

func (r *rig) stopAtCleanup(s *Sidecar) {
	r.t.Cleanup(func() { _ = s.Stop(context.Background()) })
}

func copyFakeProxy(t *testing.T) string {
	t.Helper()
	src, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	in, err := os.Open(src)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	dst := filepath.Join(t.TempDir(), binaryName)
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
	return dst
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestSidecarNotInstalledIsNoOp(t *testing.T) {
	r := newRig(t)
	r.opts.Vendored = filepath.Join(t.TempDir(), "missing")
	s := r.sidecar()
	ctx := context.Background()

	if _, err := s.Ensure(ctx); !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("Ensure err = %v, want ErrNotInstalled", err)
	}
	if _, err := s.Bounce(ctx); !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("Bounce err = %v, want ErrNotInstalled", err)
	}
	if _, err := os.Stat(r.opts.Home); !os.IsNotExist(err) {
		t.Fatalf("a missing binary still wrote %s (stat err %v)", r.opts.Home, err)
	}
}

func TestSidecarSpawnsAndWritesConfig(t *testing.T) {
	r := newRig(t)
	s := r.sidecar()
	r.stopAtCleanup(s)

	ep, err := s.Ensure(context.Background())
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	cfg, ok, err := loadSidecarConfig(r.configPath())
	if err != nil || !ok {
		t.Fatalf("config not written: ok=%v err=%v", ok, err)
	}
	if cfg.Host != "127.0.0.1" || cfg.Port != r.port || cfg.AuthDir != r.opts.AuthDir {
		t.Fatalf("config = %+v", cfg)
	}
	if len(cfg.APIKeys) != 1 || ep.APIKey != cfg.APIKeys[0] || ep.BaseURL != r.base {
		t.Fatalf("endpoint %+v does not use the config key %v", ep, cfg.APIKeys)
	}
	if info, err := os.Stat(r.configPath()); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("config mode: %v %v, want 0600", info.Mode().Perm(), err)
	}
	rec, ok := readRecord(r.pidPath())
	if !ok || rec.Owner != os.Getpid() || !rec.identifies() {
		t.Fatalf("pid record %+v ok=%v does not identify the started proxy", rec, ok)
	}
}

func TestSidecarAdoptsRunningProxyAndNeverStopsIt(t *testing.T) {
	r := newRig(t)
	r.writeConfig(r.port, "adopt-key")
	fake := r.startFake()
	r.waitUp("adopt-key")

	s := r.sidecar()
	ep, err := s.Ensure(context.Background())
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if ep.APIKey != "adopt-key" {
		t.Fatalf("adopted endpoint key %q", ep.APIKey)
	}
	if _, err := os.Stat(r.pidPath()); !os.IsNotExist(err) {
		t.Fatalf("adopting wrote a pid record (stat err %v)", err)
	}
	if err := s.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !fake.alive() {
		t.Fatal("Stop killed a proxy this Scribe did not start")
	}
}

func TestSidecarStopsOnlyWhatItStarted(t *testing.T) {
	r := newRig(t)
	s := r.sidecar()
	if _, err := s.Ensure(context.Background()); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	rec, ok := readRecord(r.pidPath())
	if !ok {
		t.Fatal("no pid record after start")
	}
	if err := s.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	waitFor(t, "the started proxy to exit", func() bool { return !rec.identifies() })
	if _, err := os.Stat(r.pidPath()); !os.IsNotExist(err) {
		t.Fatalf("pid record kept after Stop (stat err %v)", err)
	}
}

func TestSidecarRefusesRejectedKeyWithoutStarting(t *testing.T) {
	r := newRig(t)
	r.writeConfig(r.port, "old-key")
	r.startFake()
	r.waitUp("old-key")
	// The config changes after the proxy read it, so the proxy now rejects the
	// key Scribe holds.
	r.writeConfig(r.port, "new-key")

	s := r.sidecar()
	_, err := s.Ensure(context.Background())
	if err == nil || !strings.Contains(err.Error(), "rejected") || !strings.Contains(err.Error(), "/proxy login") {
		t.Fatalf("Ensure err = %v, want a rejected-key message with /proxy login", err)
	}
	if _, err := os.Stat(r.pidPath()); !os.IsNotExist(err) {
		t.Fatalf("a rejected key still started a proxy (stat err %v)", err)
	}
}

func TestSidecarPlaceholderKeysStopBeforeStart(t *testing.T) {
	r := newRig(t)
	r.writeConfig(r.port, "changeme")
	s := r.sidecar()
	_, err := s.Ensure(context.Background())
	if err == nil || !strings.Contains(err.Error(), "placeholder") {
		t.Fatalf("Ensure err = %v, want a placeholder message", err)
	}
	if _, err := os.Stat(r.pidPath()); !os.IsNotExist(err) {
		t.Fatalf("placeholder keys still started a proxy (stat err %v)", err)
	}
}

func TestSidecarRefusesPortMismatch(t *testing.T) {
	r := newRig(t)
	r.writeConfig(r.port+1, "k")
	_, err := r.sidecar().Ensure(context.Background())
	if err == nil || !strings.Contains(err.Error(), "listens on port") {
		t.Fatalf("Ensure err = %v, want a port mismatch", err)
	}
	if _, err := os.Stat(r.pidPath()); !os.IsNotExist(err) {
		t.Fatalf("a port mismatch still started a proxy (stat err %v)", err)
	}
}

func TestSidecarKillsOrphanOfDeadScribe(t *testing.T) {
	r := newRig(t)
	r.writeConfig(r.port, "orphan-key")
	orphan := r.startFake("FAKE_PROXY_HANG=1")
	r.recordFor(orphan.pid, r.deadPID(), r.resolvedBinary(), r.startTicksOf(orphan.pid))

	s := r.sidecar()
	r.stopAtCleanup(s)
	if _, err := s.Ensure(context.Background()); err != nil {
		t.Fatalf("Ensure after an orphan: %v", err)
	}
	waitFor(t, "the orphan to be killed", func() bool { return !orphan.alive() })
	rec, ok := readRecord(r.pidPath())
	if !ok || rec.PID == orphan.pid || rec.Owner != os.Getpid() {
		t.Fatalf("record after reclaim = %+v ok=%v, want a fresh proxy owned by this Scribe", rec, ok)
	}
}

func TestSidecarLeavesProxyOfLiveOtherScribeAlone(t *testing.T) {
	r := newRig(t)
	r.writeConfig(r.port, "other-key")
	other := r.startFake("FAKE_PROXY_HANG=1")
	// The owner is the test runner, which is alive and is not this process.
	r.recordFor(other.pid, os.Getppid(), r.resolvedBinary(), r.startTicksOf(other.pid))

	_, err := r.sidecar().Ensure(context.Background())
	if err == nil || !strings.Contains(err.Error(), "another Scribe") {
		t.Fatalf("Ensure err = %v, want a refusal naming the other Scribe", err)
	}
	if !other.alive() {
		t.Fatal("a live other Scribe's proxy was signalled")
	}
}

func TestSidecarNeverSignalsUnconfirmedPid(t *testing.T) {
	cases := map[string]func(r *rig, p *fakeProc) sidecarRecord{
		"wrong executable": func(r *rig, p *fakeProc) sidecarRecord {
			return sidecarRecord{PID: p.pid, Start: r.startTicksOf(p.pid), Owner: r.deadPID(), Binary: "/nonexistent/cliproxyapi"}
		},
		"reused pid (start time differs)": func(r *rig, p *fakeProc) sidecarRecord {
			return sidecarRecord{PID: p.pid, Start: r.startTicksOf(p.pid) + 1, Owner: r.deadPID(), Binary: r.resolvedBinary()}
		},
	}
	for name, mk := range cases {
		t.Run(name, func(t *testing.T) {
			r := newRig(t)
			r.writeConfig(r.port, "k")
			p := r.startFake("FAKE_PROXY_HANG=1")
			if err := writeRecord(r.pidPath(), mk(r, p)); err != nil {
				t.Fatal(err)
			}
			s := r.sidecar()
			r.stopAtCleanup(s)
			if _, err := s.Ensure(context.Background()); err != nil {
				t.Fatalf("Ensure: %v", err)
			}
			if !p.alive() {
				t.Fatal("an unconfirmed pid was signalled")
			}
		})
	}
}

func TestSidecarBounceRestartsDeadSidecar(t *testing.T) {
	r := newRig(t)
	s := r.sidecar()
	r.stopAtCleanup(s)
	ep, err := s.Ensure(context.Background())
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	first, _ := readRecord(r.pidPath())
	if err := killPID(first.PID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the first proxy to die", func() bool { return !first.identifies() })

	ep2, err := s.Bounce(context.Background())
	if err != nil {
		t.Fatalf("Bounce: %v", err)
	}
	second, ok := readRecord(r.pidPath())
	if !ok || second.PID == first.PID || ep2.APIKey != ep.APIKey {
		t.Fatalf("bounce did not start a new proxy: first %d, second %+v ok=%v", first.PID, second, ok)
	}
}

func TestSidecarBounceRefusesRemoteProxy(t *testing.T) {
	r := newRig(t)
	r.opts.BaseURL = "http://10.20.30.40:8317/v1"
	s := r.sidecar()
	_, err := s.Bounce(context.Background())
	if err == nil || !strings.Contains(err.Error(), "not on this machine") {
		t.Fatalf("Bounce err = %v, want a refusal for a remote proxy", err)
	}
	if _, err := os.Stat(r.opts.Home); !os.IsNotExist(err) {
		t.Fatalf("a remote proxy still wrote %s (stat err %v)", r.opts.Home, err)
	}
}

func TestSidecarHomeFollowsScribeHome(t *testing.T) {
	scribeHome := t.TempDir()
	t.Setenv("SCRIBE_HOME", scribeHome)
	s := NewSidecar(SidecarOptions{BaseURL: "http://127.0.0.1:8317/v1"})
	if want := filepath.Join(scribeHome, "proxy"); s.home != want {
		t.Fatalf("home = %q, want %q", s.home, want)
	}
	if want := filepath.Join(scribeHome, "proxy", binaryName); s.vendored != want {
		t.Fatalf("vendored = %q, want %q", s.vendored, want)
	}
	if s.port != defaultProxyPort {
		t.Fatalf("port = %d, want %d", s.port, defaultProxyPort)
	}
}

// TestBounceThroughSidecarRecoversStream wires the sidecar into the provider
// the way the registry does. The sidecar is killed mid-life, and the next
// stream restarts it and delivers text.
func TestBounceThroughSidecarRecoversStream(t *testing.T) {
	r := newRig(t)
	s := r.sidecar()
	r.stopAtCleanup(s)
	ep, err := s.Ensure(context.Background())
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	first, _ := readRecord(r.pidPath())
	p := New(Options{BaseURL: ep.BaseURL, APIKey: ep.APIKey, HTTPClient: r.opts.HTTPClient, Bounce: s.Bounce})
	if err := killPID(first.PID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the proxy to die", func() bool { return !first.identifies() })

	ch, err := p.Stream(context.Background(), llm.Request{Model: "gpt-5", Messages: []llm.Message{llm.UserText("hi")}})
	if err != nil {
		t.Fatalf("Stream after the sidecar died: %v", err)
	}
	text, err := collect(t, ch)
	if err != nil || text != "ok" {
		t.Fatalf("text %q err %v, want ok", text, err)
	}
	second, ok := readRecord(r.pidPath())
	if !ok || second.PID == first.PID {
		t.Fatalf("the provider did not restart the sidecar (first %d, second %+v)", first.PID, second)
	}
}
