package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/config"
)

// stopWait is how long a sidecar gets to exit after SIGTERM before it is
// killed. The same wait is used for the port to free after a stop.
const stopWait = 3 * time.Second

// SidecarOptions locates the proxy sidecar. Zero values mean the defaults.
type SidecarOptions struct {
	// BaseURL is the OpenAI-compatible root the provider uses, e.g.
	// http://127.0.0.1:8317/v1. Only a loopback address is managed here.
	BaseURL    string
	HTTPClient *http.Client
	// Home holds config.yaml, the pid record and the log. Empty means
	// proxy/ under Scribe's home directory.
	Home string
	// AuthDir is where CLIProxyAPI keeps provider logins. Empty means
	// ~/.cli-proxy-api, the directory CLIProxyAPI uses by default.
	AuthDir string
	// Vendored is a pinned binary, tried after PATH. Empty means cliproxyapi
	// inside Home.
	Vendored string
}

// Sidecar starts, adopts and stops the CLIProxyAPI process behind the proxy
// provider (spec 03 section 11). It only signals a process it can confirm
// is the proxy it recorded, and it only stops a proxy that this Scribe
// started. Methods are safe for concurrent use.
type Sidecar struct {
	mu       sync.Mutex
	base     string
	port     int
	client   *http.Client
	home     string
	authDir  string
	vendored string
}

// NewSidecar returns a sidecar for the proxy at o.BaseURL. Nothing is read or
// written until one of its methods is called.
func NewSidecar(o SidecarOptions) *Sidecar {
	home := o.Home
	if home == "" {
		home = filepath.Join(config.Home(), "proxy")
	}
	authDir := o.AuthDir
	if authDir == "" {
		authDir = defaultAuthDir()
	}
	vendored := o.Vendored
	if vendored == "" {
		vendored = filepath.Join(home, binaryName)
	}
	return &Sidecar{
		base:     o.BaseURL,
		port:     portOf(o.BaseURL),
		client:   o.HTTPClient,
		home:     home,
		authDir:  authDir,
		vendored: vendored,
	}
}

// Ensure makes a proxy answer at the base URL and returns where to send
// requests. A proxy that is already up is adopted and never restarted. When
// nothing answers, a sidecar is started, unless the binary is missing, in
// which case it returns ErrNotInstalled and touches nothing.
func (s *Sidecar) Ensure(ctx context.Context) (Endpoint, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ensure(ctx)
}

// Bounce is the restart the provider calls after a connection error. It stops
// a sidecar this Scribe started, waits for the port to free, and starts again
// through ensure. A proxy another process started is never stopped.
func (s *Sidecar) Bounce(ctx context.Context) (Endpoint, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkLocal(); err != nil {
		return Endpoint{}, err
	}
	if err := s.stopOwned(ctx); err != nil {
		return Endpoint{}, err
	}
	s.waitFree(ctx)
	return s.ensure(ctx)
}

// Stop stops the sidecar if this Scribe started it. A proxy that was adopted,
// or that another Scribe started, keeps running.
func (s *Sidecar) Stop(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stopOwned(ctx)
}

func (s *Sidecar) ensure(ctx context.Context) (Endpoint, error) {
	if err := s.checkLocal(); err != nil {
		return Endpoint{}, err
	}
	cfg, _, err := loadSidecarConfig(s.configPath())
	if err != nil {
		return Endpoint{}, err
	}
	// Adopt first. A proxy that answers with a key we hold is used as it is,
	// whoever started it.
	switch st, key := s.probeKeys(ctx, candidateKeys(cfg)); st {
	case Running:
		return Endpoint{BaseURL: s.base, APIKey: key}, nil
	case NeedsAuth:
		return Endpoint{}, rejectedErr(s.base, s.configPath(), cfg)
	}

	binary, err := FindBinary(s.vendored)
	if err != nil {
		return Endpoint{}, fmt.Errorf("%w: no %s on PATH or at %s", ErrNotInstalled, binaryName, s.vendored)
	}
	cfg, err = s.ensureConfig()
	if err != nil {
		return Endpoint{}, err
	}
	keys := candidateKeys(cfg)
	if len(keys) == 0 {
		return Endpoint{}, placeholderErr(s.configPath())
	}
	if err := s.reclaim(ctx); err != nil {
		return Endpoint{}, err
	}
	if err := s.start(binary); err != nil {
		return Endpoint{}, err
	}
	st := WaitHealthy(ctx, s.client, s.base, keys[0])
	switch st {
	case Running:
		return Endpoint{BaseURL: s.base, APIKey: keys[0]}, nil
	case NeedsAuth:
		return Endpoint{}, rejectedErr(s.base, s.configPath(), cfg)
	}
	// Scribe started this process, so it stops it rather than leave a half-up
	// sidecar behind.
	_ = s.stopOwned(ctx)
	return Endpoint{}, fmt.Errorf("proxy: not responding at %s after Scribe started %s; it did not answer in time. Check %s and run /proxy login", s.base, binary, s.logPath())
}

// probeKeys probes with each key in turn. It returns the first Running result
// and the key that gave it. If nothing answers at all it stops early, since
// every key would get the same silence. NeedsAuth means something answered
// and rejected every key.
func (s *Sidecar) probeKeys(ctx context.Context, keys []string) (State, string) {
	if len(keys) == 0 {
		keys = []string{""}
	}
	st := Unreachable
	for _, k := range keys {
		st = Probe(ctx, s.client, s.base, k)
		if st == Running {
			return Running, k
		}
		if st == Unreachable {
			return Unreachable, ""
		}
	}
	return st, ""
}

// reclaim clears the pid record a sidecar left behind before a new start. A
// record whose Scribe is still alive belongs to that Scribe. It is never
// touched, and the start is refused rather than overwriting the record. A
// record left by a dead Scribe is an orphan, and stopRecord kills it only when
// the pid is confirmed to be the proxy.
func (s *Sidecar) reclaim(ctx context.Context) error {
	rec, ok := readRecord(s.pidPath())
	if !ok {
		return nil
	}
	if rec.Owner != os.Getpid() && processAlive(rec.Owner) && rec.identifies() {
		return fmt.Errorf("proxy: another Scribe (pid %d) started the proxy at %s, and it is not answering; close that Scribe or stop pid %d", rec.Owner, s.base, rec.PID)
	}
	return s.stopRecord(ctx, rec)
}

// start launches the sidecar detached from this process, so it outlives a
// Scribe that exits. The pid record is written before start returns, because
// a later Scribe needs it to tell this process apart.
func (s *Sidecar) start(binary string) error {
	if err := os.MkdirAll(s.home, 0o700); err != nil {
		return fmt.Errorf("proxy: cannot create %s: %w", s.home, err)
	}
	logf, err := os.OpenFile(s.logPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("proxy: cannot open the log %s: %w", s.logPath(), err)
	}
	defer logf.Close()

	cmd := exec.Command(binary, "-config", s.configPath())
	cmd.Stdout = logf
	cmd.Stderr = logf
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("proxy: could not start %s: %w", binary, err)
	}
	// Reap the process if it exits, so it does not linger as a zombie.
	go func() { _ = cmd.Wait() }()

	pid := cmd.Process.Pid
	resolved, err := filepath.EvalSymlinks(binary)
	if err != nil {
		resolved = binary
	}
	start, err := startTicks(pid)
	if err != nil {
		// Without a start time the process cannot be confirmed later, so it
		// could never be stopped safely. Refuse rather than leave it running.
		_ = killPID(pid)
		return fmt.Errorf("proxy: cannot confirm the started process on this system (%v), so Scribe will not run the proxy", err)
	}
	rec := sidecarRecord{PID: pid, Start: start, Owner: os.Getpid(), Binary: resolved}
	if err := writeRecord(s.pidPath(), rec); err != nil {
		_ = killPID(pid)
		return err
	}
	return nil
}

// stopOwned stops the recorded sidecar only when this process started it.
func (s *Sidecar) stopOwned(ctx context.Context) error {
	rec, ok := readRecord(s.pidPath())
	if !ok || rec.Owner != os.Getpid() {
		return nil
	}
	return s.stopRecord(ctx, rec)
}

// stopRecord signals the recorded pid, but only when it is still the proxy
// the record names. Any doubt means nothing is signalled. The record is
// removed either way, since the pid is either gone or not ours to manage.
func (s *Sidecar) stopRecord(ctx context.Context, rec sidecarRecord) error {
	if !rec.identifies() {
		return removeRecord(s.pidPath())
	}
	if err := terminatePID(rec.PID); err != nil {
		return fmt.Errorf("proxy: could not stop pid %d: %w", rec.PID, err)
	}
	if waitGone(ctx, rec, stopWait) {
		return removeRecord(s.pidPath())
	}
	_ = killPID(rec.PID)
	waitGone(ctx, rec, time.Second)
	return removeRecord(s.pidPath())
}

// waitFree waits for the port to stop answering after a stop, up to stopWait.
// If something still answers, ensure adopts it or reports it.
func (s *Sidecar) waitFree(ctx context.Context) {
	deadline := time.Now().Add(stopWait)
	for time.Now().Before(deadline) {
		if Probe(ctx, s.client, s.base, "") == Unreachable {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// waitGone polls until the recorded process is no longer the proxy, or until
// d or ctx runs out. It reports whether the process is gone.
func waitGone(ctx context.Context, rec sidecarRecord, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for {
		if !rec.identifies() {
			return true
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			return false
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// sidecarRecord is what Scribe writes next to the proxy it starts. It is what
// lets a later Scribe find an orphan and check that the pid is still the
// proxy before signalling it.
type sidecarRecord struct {
	PID    int    `json:"pid"`
	Start  uint64 `json:"start"` // kernel start time, to tell a reused pid apart
	Owner  int    `json:"owner"` // pid of the Scribe that started it
	Binary string `json:"binary"`
}

func readRecord(path string) (sidecarRecord, bool) {
	var rec sidecarRecord
	b, err := os.ReadFile(path)
	if err != nil {
		return rec, false
	}
	if err := json.Unmarshal(b, &rec); err != nil || rec.PID <= 0 {
		return rec, false
	}
	return rec, true
}

func writeRecord(path string, rec sidecarRecord) error {
	b, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	if err := writeFileAtomic(path, append(b, '\n'), 0o600); err != nil {
		return fmt.Errorf("proxy: cannot write %s: %w", path, err)
	}
	return nil
}

func removeRecord(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// checkLocal refuses to manage a proxy on another machine. Scribe can only
// start, stop or confirm a process on this host.
func (s *Sidecar) checkLocal() error {
	u, err := url.Parse(s.base)
	if err != nil || !loopbackHostname(u.Hostname()) {
		return fmt.Errorf("proxy: %s is not on this machine, so Scribe will not start or restart it; start it there", s.base)
	}
	return nil
}

func (s *Sidecar) configPath() string { return filepath.Join(s.home, "config.yaml") }
func (s *Sidecar) pidPath() string    { return filepath.Join(s.home, "sidecar.pid") }
func (s *Sidecar) logPath() string    { return filepath.Join(s.home, "proxy.log") }

// candidateKeys lists the keys to try, from PROXY_API_KEY and the config.
func candidateKeys(cfg sidecarConfig) []string {
	return CandidateKeys(os.Getenv("PROXY_API_KEY"), cfg.APIKeys...)
}

func rejectedErr(base, cfgPath string, cfg sidecarConfig) error {
	if len(candidateKeys(cfg)) == 0 {
		return placeholderErr(cfgPath)
	}
	return fmt.Errorf("proxy: %s rejected every key; run /proxy login to fix the provider login", base)
}

func placeholderErr(cfgPath string) error {
	return fmt.Errorf("proxy: every key in %s is a placeholder; put a real key there or set PROXY_API_KEY", cfgPath)
}

// portOf returns the port in a base URL, or the proxy's default port.
func portOf(base string) int {
	u, err := url.Parse(base)
	if err != nil || u.Port() == "" {
		return defaultProxyPort
	}
	p, err := strconv.Atoi(u.Port())
	if err != nil || p <= 0 {
		return defaultProxyPort
	}
	return p
}

func defaultAuthDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ".cli-proxy-api"
	}
	return filepath.Join(home, ".cli-proxy-api")
}
