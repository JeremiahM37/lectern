package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/JeremiahM37/lectern/v2/cmd/lectern/localruntime"
	"github.com/JeremiahM37/lectern/v2/internal/config"
)

// With no LECTERN_API, a client command goes to the Lectern service running
// on this machine when there is one, and to the private local runtime
// otherwise. A host that runs `lectern serve` (a systemd unit, say) is where
// its owner's sessions live; sending that owner's plain `lectern` to an empty
// private runtime beside it looks like everything vanished.

// serviceEnvFiles are the well-known environment files a system service reads
// its settings from. Only LECTERN_PORT is taken from them, and only when the
// caller's own environment does not set it. Unreadable files (root-only is
// normal) are skipped.
var serviceEnvFiles = []string{"/etc/lectern.env", "/etc/default/lectern"}

const serviceProbeTimeout = 750 * time.Millisecond

// hostedService is a Lectern server found on this machine's loopback.
type hostedService struct {
	URL     string
	Version string
	// Refused means it is a Lectern but would not answer this CLI without
	// credentials (token auth mode, no LECTERN_AUTH_TOKEN here).
	Refused bool
}

func (h hostedService) port() string {
	if i := strings.LastIndex(h.URL, ":"); i >= 0 {
		return h.URL[i:]
	}
	return h.URL
}

// serverChoice is where plain client commands go when LECTERN_API is unset.
type serverChoice struct {
	Hosted *hostedService // chosen hosted service (nil means local runtime)
	// RefusedService is a service that exists but refused this CLI; the
	// choice then falls to the local runtime.
	RefusedService *hostedService
	// LocalRunning says a private local runtime is already up beside the
	// chosen service.
	LocalRunning bool
	// InLocalSession says this process runs inside a local runtime session,
	// which keeps its commands on that runtime.
	InLocalSession bool
}

// note is the one-line explanation printed when the choice is not obvious.
func (c serverChoice) note() string {
	switch {
	case c.Hosted != nil && c.LocalRunning:
		return fmt.Sprintf("lectern: using the Lectern service on %s; `lectern local …` uses your private runtime", c.Hosted.port())
	case c.RefusedService != nil:
		return fmt.Sprintf("lectern: the Lectern service on %s needs LECTERN_AUTH_TOKEN (or set LECTERN_API); using your private runtime", c.RefusedService.port())
	}
	return ""
}

// servicePorts lists the loopback ports worth probing: the caller's
// LECTERN_PORT (default 9110), otherwise also any port a service env file
// names.
func servicePorts(cfg *config.Config) []int {
	ports := []int{cfg.Port}
	if strings.TrimSpace(os.Getenv("LECTERN_PORT")) != "" {
		return ports
	}
	for _, path := range serviceEnvFiles {
		if p := envFilePort(path); p > 0 && p != cfg.Port {
			ports = append(ports, p)
		}
	}
	return ports
}

func envFilePort(path string) int {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()
	port := 0
	scan := bufio.NewScanner(f)
	for scan.Scan() {
		line := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(scan.Text()), "export "))
		key, value, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(key) != "LECTERN_PORT" {
			continue
		}
		if p, err := strconv.Atoi(strings.Trim(strings.TrimSpace(value), `"'`)); err == nil && p > 0 && p < 65536 {
			port = p
		}
	}
	return port
}

// probeService reports whether base is a hosted Lectern. A private local
// runtime is never one: its identity route answers 401 to anyone without
// its token, where a hosted server serves the web app there.
func probeService(ctx context.Context, base, token string) (hostedService, bool) {
	client := &http.Client{Timeout: serviceProbeTimeout}
	get := func(path string, bearer bool) (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+path, nil)
		if err != nil {
			return nil, err
		}
		if bearer && token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		return client.Do(req)
	}
	res, err := get("/api/health", true)
	if err != nil {
		return hostedService{}, false
	}
	var health struct {
		OK      bool   `json:"ok"`
		Version string `json:"version"`
		Detail  string `json:"detail"`
	}
	_ = json.NewDecoder(io.LimitReader(res.Body, 64<<10)).Decode(&health)
	res.Body.Close()
	svc := hostedService{URL: base, Version: health.Version}
	switch {
	case res.StatusCode == http.StatusOK && health.OK && health.Version != "":
	case res.StatusCode == http.StatusUnauthorized && health.Detail == "unauthorized":
		// Health is gated in token mode. The Agent Card is public and says
		// whether this is a Lectern at all.
		card, err := get("/.well-known/agent-card.json", false)
		if err != nil {
			return hostedService{}, false
		}
		var body struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		}
		_ = json.NewDecoder(io.LimitReader(card.Body, 64<<10)).Decode(&body)
		card.Body.Close()
		if card.StatusCode != http.StatusOK || body.Name != "Lectern" {
			return hostedService{}, false
		}
		svc.Version, svc.Refused = body.Version, true
	default:
		return hostedService{}, false
	}
	id, err := get("/__lectern_local/identity", false)
	if err != nil {
		return hostedService{}, false
	}
	id.Body.Close()
	if id.StatusCode == http.StatusUnauthorized {
		return hostedService{}, false
	}
	return svc, true
}

// insideLocalSession reports whether this process runs in a tmux session of
// the private local runtime (it hands its sessions its own TMUX_TMPDIR).
func insideLocalSession() bool {
	current := os.Getenv("TMUX_TMPDIR")
	if current == "" {
		return false
	}
	dir, err := localruntime.TmuxDir()
	if err != nil {
		return false
	}
	a, errA := filepath.Abs(current)
	b, errB := filepath.Abs(dir)
	return errA == nil && errB == nil && filepath.Clean(a) == filepath.Clean(b)
}

// chooseServer decides where plain client commands go when LECTERN_API is
// unset: the first Lectern service answering on this machine's loopback, else
// the private local runtime. It never starts anything.
func chooseServer(ctx context.Context, cfg *config.Config) serverChoice {
	var choice serverChoice
	if insideLocalSession() {
		choice.InLocalSession = true
		return choice
	}
	localURL := ""
	if ep, ok := localruntime.Peek(ctx); ok {
		choice.LocalRunning = true
		localURL = ep.URL
	}
	for _, port := range servicePorts(cfg) {
		base := "http://127.0.0.1:" + strconv.Itoa(port)
		if base == localURL {
			continue
		}
		svc, ok := probeService(ctx, base, cfg.AuthToken)
		if !ok {
			continue
		}
		if svc.Refused {
			if choice.RefusedService == nil {
				choice.RefusedService = &svc
			}
			continue
		}
		choice.Hosted = &svc
		choice.RefusedService = nil
		return choice
	}
	return choice
}

// useHostedService applies the choice for this process: when a hosted
// service was found, LECTERN_API names it, exactly as if the caller had set
// it, so every client path (attachment, native controls, terminal tabs)
// agrees. It reports whether a service was chosen. quiet suppresses the
// stderr note (MCP on stdio).
func useHostedService(cfg *config.Config, quiet bool) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	choice := chooseServer(ctx, cfg)
	if note := choice.note(); note != "" && !quiet {
		// The both-running hint is for people; scripts only hear about a
		// service that refused them, because that changes the outcome.
		if choice.RefusedService != nil || stderrIsTerminal() {
			fmt.Fprintln(os.Stderr, note)
		}
	}
	if choice.Hosted == nil {
		return false
	}
	_ = os.Setenv("LECTERN_API", choice.Hosted.URL)
	return true
}

func stderrIsTerminal() bool { return term.IsTerminal(int(os.Stderr.Fd())) }
