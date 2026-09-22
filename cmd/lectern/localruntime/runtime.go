// Package localruntime starts the per-user Lectern control plane used by the
// local CLI. It deliberately keeps its state away from the checkout so a fresh
// install can run without a hosted server or a configured target.
package localruntime

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/app"
	"github.com/JeremiahM37/lectern/v2/internal/config"
	"github.com/JeremiahM37/lectern/v2/internal/store"
	"github.com/JeremiahM37/lectern/v2/internal/version"
)

const (
	endpointFile  = "endpoint.json"
	lockFile      = "engine.lock"
	engineRoute   = "/__lectern_local/stop"
	identityRoute = "/__lectern_local/identity"
)

type Endpoint struct {
	URL       string       `json:"url"`
	Token     string       `json:"token"`
	Instance  string       `json:"instance"`
	PID       int          `json:"pid"`
	StartedAt string       `json:"started_at"`
	Build     version.Info `json:"build"`
	TmuxDir   string       `json:"tmux_dir"`
}

type Status struct {
	State    string       `json:"state"`
	Endpoint *Endpoint    `json:"endpoint,omitempty"`
	Health   any          `json:"health,omitempty"`
	Detail   string       `json:"detail,omitempty"`
	Version  string       `json:"version,omitempty"`
	Build    version.Info `json:"build,omitempty"`
}

func StateDir() (string, error) {
	return stateDir(true)
}

func stateDir(create bool) (string, error) {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".local", "state")
	}
	dir := filepath.Join(base, "lectern", "local")
	var err error
	if dir, err = filepath.Abs(dir); err != nil {
		return "", err
	}
	if create {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return "", err
		}
		if err := os.Chmod(dir, 0o700); err != nil {
			return "", err
		}
	}
	return dir, nil
}

func Ensure(ctx context.Context, binary string, base *config.Config) (Endpoint, error) {
	dir, err := StateDir()
	if err != nil {
		return Endpoint{}, err
	}
	lock, err := os.OpenFile(filepath.Join(dir, lockFile), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return Endpoint{}, err
	}
	defer lock.Close()
	_ = lock.Chmod(0o600)

	if ep, ok := healthyEndpoint(ctx, dir); ok {
		return ep, nil
	}
	if err := runtimeSupported(); err != nil {
		return Endpoint{}, err
	}
	deadline := time.Now().Add(20 * time.Second)
	for {
		var held bool
		held, err = tryLock(lock)
		if held {
			break
		}
		if err != nil {
			return Endpoint{}, fmt.Errorf("local runtime lock: %w", err)
		}
		if ep, ok := healthyEndpoint(ctx, dir); ok {
			return ep, nil
		}
		if time.Now().After(deadline) {
			return Endpoint{}, errors.New("local runtime is starting but did not become healthy")
		}
		select {
		case <-ctx.Done():
			return Endpoint{}, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	if ep, ok := healthyEndpoint(ctx, dir); ok {
		return ep, nil
	}
	token, err := newToken()
	if err != nil {
		return Endpoint{}, err
	}
	if binary == "" {
		binary, err = os.Executable()
		if err != nil {
			return Endpoint{}, err
		}
	}
	logPath := filepath.Join(dir, "engine.log")
	tmuxDir := filepath.Join(dir, "tmux")
	if err := os.MkdirAll(tmuxDir, 0o700); err != nil {
		return Endpoint{}, err
	}
	if err := os.Chmod(tmuxDir, 0o700); err != nil {
		return Endpoint{}, err
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return Endpoint{}, err
	}
	defer logFile.Close()
	tokenReader, tokenWriter, err := os.Pipe()
	if err != nil {
		return Endpoint{}, err
	}
	defer tokenWriter.Close()
	cmd := exec.Command(binary, "--local-engine", "--local-state-dir", dir,
		"--local-token-fd", strconv.Itoa(4), "--local-lock-fd", strconv.Itoa(3))
	cmd.Env = localEnv(tmuxDir)
	cmd.ExtraFiles = []*os.File{lock, tokenReader}
	detach(cmd)
	cmd.Stdout, cmd.Stderr = logFile, logFile
	if err := cmd.Start(); err != nil {
		_ = tokenReader.Close()
		return Endpoint{}, fmt.Errorf("start local runtime: %w", err)
	}
	_ = tokenReader.Close()
	if _, err := io.WriteString(tokenWriter, token+"\n"); err != nil {
		_ = cmd.Process.Kill()
		go cmd.Wait()
		return Endpoint{}, fmt.Errorf("send local runtime token: %w", err)
	}
	_ = tokenWriter.Close()
	go cmd.Wait()
	for time.Now().Before(deadline) {
		if ep, ok := healthyEndpoint(ctx, dir); ok && ep.Token == token {
			return ep, nil
		}
		select {
		case <-ctx.Done():
			_ = cmd.Process.Kill()
			return Endpoint{}, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	_ = cmd.Process.Kill()
	return Endpoint{}, errors.New("local runtime did not publish a healthy endpoint")
}

func localEnv(tmuxDir string) []string {
	blocked := map[string]bool{
		"LECTERN_API": true, "LECTERN_ATTACH_HOST": true, "LECTERN_DB": true,
		"LECTERN_HOST": true, "LECTERN_PORT": true, "LECTERN_BASE_URL": true,
		"LECTERN_AUTH_TOKEN": true, "LECTERN_LOCAL_ENGINE": true,
		"TMUX": true, "TMUX_TMPDIR": true,
	}
	out := make([]string, 0, len(os.Environ()))
	for _, item := range os.Environ() {
		key, _, ok := strings.Cut(item, "=")
		if !ok || !blocked[key] {
			out = append(out, item)
		}
	}
	return append(out, "TMUX_TMPDIR="+tmuxDir, "TMUX=")
}

func newToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func readEndpoint(dir string) (Endpoint, error) {
	var ep Endpoint
	data, err := os.ReadFile(filepath.Join(dir, endpointFile))
	if err != nil {
		return ep, err
	}
	if err := json.Unmarshal(data, &ep); err != nil {
		return ep, err
	}
	if ep.Token == "" || ep.Instance == "" || ep.PID <= 0 {
		return ep, errors.New("invalid local endpoint")
	}
	u, err := neturl(ep.URL)
	if err != nil || u != "127.0.0.1" {
		return ep, errors.New("local endpoint is not loopback")
	}
	if ep.TmuxDir == "" {
		ep.TmuxDir = filepath.Join(dir, "tmux")
	}
	return ep, nil
}

func neturl(raw string) (string, error) {
	if !strings.HasPrefix(raw, "http://") {
		return "", errors.New("local endpoint must use http")
	}
	host := strings.TrimPrefix(raw, "http://")
	host = strings.TrimSuffix(host, "/")
	if strings.Contains(host, "/") || strings.Contains(host, "@") {
		return "", errors.New("invalid local endpoint")
	}
	h, _, err := net.SplitHostPort(host)
	return h, err
}

func healthyEndpoint(ctx context.Context, dir string) (Endpoint, bool) {
	ep, err := readEndpoint(dir)
	if err != nil {
		return Endpoint{}, false
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ep.URL+identityRoute, nil)
	if err != nil {
		return Endpoint{}, false
	}
	req.Header.Set("Authorization", "Bearer "+ep.Token)
	client := &http.Client{Timeout: 500 * time.Millisecond}
	res, err := client.Do(req)
	if err != nil {
		return Endpoint{}, false
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return Endpoint{}, false
	}
	var identity struct {
		Instance string       `json:"instance"`
		PID      int          `json:"pid"`
		Version  string       `json:"version"`
		Build    version.Info `json:"build"`
	}
	if err := json.NewDecoder(res.Body).Decode(&identity); err != nil || identity.Instance != ep.Instance || identity.PID != ep.PID || identity.Version != ep.Build.Version || identity.Build.Revision != ep.Build.Revision || identity.Build.CommitTime != ep.Build.CommitTime {
		return Endpoint{}, false
	}
	return ep, true
}

func writeEndpoint(dir string, ep Endpoint) error {
	data, err := json.Marshal(ep)
	if err != nil {
		return err
	}
	tmp, err := os.OpenFile(filepath.Join(dir, fmt.Sprintf(".%s.%d", endpointFile, os.Getpid())), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }()
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, filepath.Join(dir, endpointFile)); err != nil {
		return err
	}
	return os.Chmod(filepath.Join(dir, endpointFile), 0o600)
}

func Engine(ctx context.Context, base *config.Config, dir, token string, lockFD int, log *slog.Logger) error {
	if dir == "" || token == "" {
		return errors.New("local engine requires private state and token")
	}
	if lockFD > 0 {
		// The descriptor is inherited from Ensure. Keeping it open holds the
		// kernel lock for the engine lifetime; it is never closed early.
		lock := os.NewFile(uintptr(lockFD), "lectern-local-lock")
		// Only the engine owns this descriptor. Do not let agent subprocesses
		// inherit it and accidentally extend the singleton lifetime.
		keepPrivate(lockFD)
		defer lock.Close()
	}
	listenAddr := "127.0.0.1:0"
	if ep, err := readEndpoint(dir); err == nil {
		if host, port, splitErr := net.SplitHostPort(strings.TrimPrefix(ep.URL, "http://")); splitErr == nil && host == "127.0.0.1" {
			listenAddr = net.JoinHostPort(host, port)
		}
		if ep.TmuxDir != "" {
			_ = os.MkdirAll(ep.TmuxDir, 0o700)
		}
	}
	listener, err := net.Listen("tcp", listenAddr)
	if err != nil {
		if listenAddr != "127.0.0.1:0" {
			return fmt.Errorf("local runtime port %s is unavailable; stop the other loopback service or remove its stale endpoint after verifying it: %w", listenAddr, err)
		}
		return err
	}
	port := listener.Addr().(*net.TCPAddr).Port
	cfg := *base
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return fmt.Errorf("resolve local state directory: %w", err)
	}
	nsDigest := sha256.Sum256([]byte(absDir))
	cfg.WorktreeNamespace = "local-" + hex.EncodeToString(nsDigest[:6])
	cfg.DBPath = filepath.Join(dir, "lectern.db")
	cfg.Host = "127.0.0.1"
	cfg.Port = port
	cfg.BaseURL = "http://127.0.0.1:" + strconv.Itoa(port)
	cfg.AuthToken = token
	cfg.Mock = false
	appInstance, err := app.New(&cfg, log)
	if err != nil {
		_ = listener.Close()
		return err
	}
	defer appInstance.Close()
	if !cfg.Mock {
		targets, err := appInstance.DB.Targets()
		if err != nil {
			_ = listener.Close()
			return err
		}
		if len(targets) == 0 {
			if _, err := appInstance.DB.InsertTarget(&store.Target{Name: "local", Kind: "local", Workroot: filepath.Join(dir, "worktrees"), Status: "online", MaxConcurrent: 4}); err != nil {
				_ = listener.Close()
				return err
			}
		}
	}
	ep := Endpoint{URL: cfg.BaseURL, Token: token, Instance: token[:16], PID: os.Getpid(), StartedAt: time.Now().UTC().Format(time.RFC3339Nano), Build: version.Current(), TmuxDir: filepath.Join(dir, "tmux")}
	var server *http.Server
	shutdownDone := make(chan struct{})
	var shutdownOnce sync.Once
	requestShutdown := func() {
		shutdownOnce.Do(func() {
			go func() {
				defer close(shutdownDone)
				appInstance.Server.DrainStreams()
				shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				_ = server.Shutdown(shutdownCtx)
			}()
		})
	}
	server = &http.Server{Handler: localHandler(appInstance.Handler(), token, ep.Instance, func() error {
		active, err := appInstance.DB.TasksWhere("status IN ('queued','running','review')")
		if err != nil {
			return err
		}
		if len(active) > 0 {
			return fmt.Errorf("local runtime has %d active task(s); finish or cancel them before stopping", len(active))
		}
		requestShutdown()
		return nil
	})}
	if err := writeEndpoint(dir, ep); err != nil {
		_ = listener.Close()
		return err
	}
	go func() {
		<-ctx.Done()
		requestShutdown()
	}()
	if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	<-shutdownDone
	return nil
}

func localHandler(next http.Handler, token, instance string, stop func() error) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(identityRoute, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer "+token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"instance": instance, "pid": os.Getpid(), "version": version.Version, "build": version.Current()})
	})
	mux.HandleFunc(engineRoute, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer "+token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if err := stop(); err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.Handle("/", next)
	return mux
}

func StatusOf(ctx context.Context) (Status, error) {
	dir, err := stateDir(false)
	if err != nil {
		return Status{}, err
	}
	ep, err := readEndpoint(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Status{State: "stopped"}, nil
		}
		return Status{State: "stale", Detail: err.Error()}, nil
	}
	if healthy, ok := healthyEndpoint(ctx, dir); ok {
		status := Status{State: "running", Endpoint: publicEndpoint(healthy), Version: healthy.Build.Version, Build: healthy.Build}
		if health, err := getHealth(ctx, healthy); err == nil {
			status.Health = health
		}
		return status, nil
	}
	if lockFree, err := localLockFree(dir); err == nil && lockFree {
		return Status{State: "stopped"}, nil
	}
	return Status{State: "unreachable", Endpoint: publicEndpoint(ep), Detail: "endpoint is present but identity check failed"}, nil
}

func localLockFree(dir string) (bool, error) {
	lock, err := os.OpenFile(filepath.Join(dir, lockFile), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return false, err
	}
	defer lock.Close()
	if held, err := tryLock(lock); err != nil {
		return false, err
	} else if !held {
		return false, nil
	}
	if err := unlock(lock); err != nil {
		return false, err
	}
	return true, nil
}

func publicEndpoint(ep Endpoint) *Endpoint {
	ep.Token = ""
	return &ep
}

func getHealth(ctx context.Context, ep Endpoint) (map[string]any, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ep.URL+"/api/health", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+ep.Token)
	res, err := (&http.Client{Timeout: 2 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("health returned %s", res.Status)
	}
	var health map[string]any
	if err := json.NewDecoder(res.Body).Decode(&health); err != nil {
		return nil, err
	}
	return health, nil
}

func Stop(ctx context.Context) error {
	dir, err := stateDir(false)
	if err != nil {
		return err
	}
	ep, err := readEndpoint(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("refusing to stop an unverified local endpoint: %w", err)
	}
	if _, ok := healthyEndpoint(ctx, dir); !ok {
		return errors.New("refusing to stop an unverified local endpoint: identity check failed")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ep.URL+engineRoute, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+ep.Token)
	res, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	body, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
	res.Body.Close()
	if res.StatusCode != http.StatusNoContent {
		detail := strings.TrimSpace(string(body))
		if detail != "" {
			return fmt.Errorf("local runtime stop: %s", detail)
		}
		return fmt.Errorf("local runtime stop returned %s", res.Status)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := healthyEndpoint(ctx, dir); !ok {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return errors.New("local runtime did not stop")
}
