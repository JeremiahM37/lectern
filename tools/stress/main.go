// Command stress is Lectern's scale benchmark driver. It is meant to run inside
// the reviewed bubblewrap namespace (tools/stress/run.sh), where it starts a
// private Lectern instance per tier — its own port, database, HOME and tmux
// servers — plus several loopback SSH "machines", launches N interactive agent
// sessions of a stand-in agent across them, and measures what an operator
// feels: list latency, live-update fan-out, approval round trips, terminal
// attach, dashboard refresh, and the control plane's own CPU and memory.
package main

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

//go:embed fake-claude.sh
var fakeClaude string

type options struct {
	lectern, consoleTest, out, python, fixture string
	tiers                                      []int
	sshTargets                                 int
	window, approvalEvery                      time.Duration
	sseClients, attachSamples, launchParallel  int
	cpuCores                                   int
	plainDirs                                  bool
	hangAt                                     time.Duration
	holdTerminals                              int
	terminalPorts                              string
}

func main() {
	var o options
	var tiers string
	flag.StringVar(&o.lectern, "lectern", "", "lectern binary under test")
	flag.StringVar(&o.consoleTest, "console-test", "", "compiled internal/console test binary (go test -c)")
	flag.StringVar(&o.out, "out", "", "directory for results")
	flag.StringVar(&o.python, "python", "/opt/venv/bin/python", "python with asyncssh, for the SSH fixture")
	flag.StringVar(&o.fixture, "fixture", "e2e/ssh_fixture_server.py", "SSH fixture server script")
	flag.StringVar(&tiers, "tiers", "10,25,50,100", "comma-separated session counts")
	flag.IntVar(&o.sshTargets, "ssh-targets", 3, "loopback SSH targets in addition to the local one")
	flag.DurationVar(&o.window, "window", 60*time.Second, "steady-state measurement window per tier")
	flag.DurationVar(&o.approvalEvery, "approval-every", 20*time.Second, "each agent asks for one approval this often")
	flag.IntVar(&o.sseClients, "sse-clients", 25, "concurrent /api/stream subscribers (open dashboards/phones)")
	flag.IntVar(&o.attachSamples, "attach-samples", 10, "sessions to open a web terminal on, per tier")
	flag.IntVar(&o.launchParallel, "launch-parallel", 8, "concurrent session launches")
	flag.BoolVar(&o.plainDirs, "plain-dirs", false, "run agents in plain directories instead of worktrees of one git repository per target")
	flag.DurationVar(&o.hangAt, "hang-at", 0, "freeze the first SSH target this far into the window (0: never) and measure the others' status freshness")
	flag.IntVar(&o.holdTerminals, "hold-terminals", 0, "open this many web terminals at once, keep them all open, and count how many stay connected")
	flag.StringVar(&o.terminalPorts, "terminal-ports", "", "LECTERN_TERMINAL_PORTS for the instance under test (LO-HI)")
	flag.IntVar(&o.cpuCores, "cpu-cores", 0, "CPU quota of the namespace, recorded in the report (run.sh sets it)")
	flag.Parse()
	for _, t := range strings.Split(tiers, ",") {
		n, err := strconv.Atoi(strings.TrimSpace(t))
		if err != nil || n <= 0 {
			log.Fatalf("bad tier %q", t)
		}
		o.tiers = append(o.tiers, n)
	}
	if o.lectern == "" || o.out == "" {
		log.Fatal("-lectern and -out are required")
	}
	if os.Getenv("ADK_TEST_ISOLATED") != "1" || os.Getenv("TMUX") != "" {
		log.Fatal("refusing to run outside the isolated namespace; use tools/stress/run.sh")
	}
	report := Report{Started: time.Now().UTC().Format(time.RFC3339), Host: hostInfo(), Options: map[string]any{
		"tiers": o.tiers, "ssh_targets": o.sshTargets, "window_s": o.window.Seconds(),
		"approval_every_s": o.approvalEvery.Seconds(), "sse_clients": o.sseClients,
		"attach_samples": o.attachSamples, "launch_parallel": o.launchParallel,
		"plain_dirs": o.plainDirs, "hang_at_s": o.hangAt.Seconds(), "hold_terminals": o.holdTerminals, "terminal_ports": o.terminalPorts,
	}}
	if o.cpuCores > 0 {
		report.Host["cpu_quota_cores"] = o.cpuCores
	}
	for _, n := range o.tiers {
		log.Printf("=== tier %d sessions ===", n)
		res, err := runTier(o, n)
		if err != nil {
			log.Printf("tier %d failed: %v", n, err)
			res.Fatal = err.Error()
		}
		report.Tiers = append(report.Tiers, res)
		writeReport(o.out, report)
	}
	log.Printf("results in %s", o.out)
}

// Report is the whole run, written as results.json.
type Report struct {
	Started string         `json:"started"`
	Host    map[string]any `json:"host"`
	Options map[string]any `json:"options"`
	Tiers   []TierResult   `json:"tiers"`
}

// TierResult is everything measured for one session count.
type TierResult struct {
	Sessions int      `json:"sessions"`
	Targets  []string `json:"targets"`
	Fatal    string   `json:"fatal,omitempty"`

	LaunchOK      int      `json:"launch_ok"`
	LaunchFailed  int      `json:"launch_failed"`
	LaunchErrors  []string `json:"launch_errors,omitempty"`
	Launch        Stats    `json:"launch"`
	LaunchWallSec float64  `json:"launch_wall_s"`
	ActiveAtStart int      `json:"active_at_window_start"`
	ActiveAtEnd   int      `json:"active_at_window_end"`

	List       Stats `json:"list"`
	ListBytes  int   `json:"list_bytes"`
	ListErrors int   `json:"list_errors"`

	FanoutClients int   `json:"fanout_clients"`
	Fanout        Stats `json:"fanout"`
	FanoutProbes  int   `json:"fanout_probes"`
	FanoutMissed  int   `json:"fanout_missed"`
	SSEDrops      int   `json:"sse_disconnects"`

	ApprovalsRequested int   `json:"approvals_requested"`
	ApprovalsApproved  int   `json:"approvals_approved"`
	ApprovalsOther     int   `json:"approvals_not_approved"`
	ApprovalVisible    Stats `json:"approval_request_to_visible"`
	ApprovalDecide     Stats `json:"approval_decision_post"`
	ApprovalUnblock    Stats `json:"approval_decision_to_unblocked"`
	ApprovalRoundTrip  Stats `json:"approval_round_trip"`

	AttachPost     Stats    `json:"attach_post"`
	AttachFirstOut Stats    `json:"attach_first_output"`
	AttachTotal    Stats    `json:"attach_total"`
	AttachErrors   []string `json:"attach_errors,omitempty"`

	TUI json.RawMessage `json:"tui,omitempty"`

	Hang *HangResult `json:"unreachable_target,omitempty"`
	Hold *HoldResult `json:"held_terminals,omitempty"`

	Usage      Usage          `json:"lectern_process"`
	Teardown   Stats          `json:"session_delete"`
	LogCounts  map[string]int `json:"lectern_log_levels"`
	LogSamples []string       `json:"lectern_log_warnings,omitempty"`
	HTTPErrors map[string]int `json:"http_errors"`
}

func hostInfo() map[string]any {
	info := map[string]any{"go": runtime.Version(), "numcpu_visible": runtime.NumCPU()}
	if b, err := os.ReadFile("/proc/cpuinfo"); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(line, "model name") {
				info["cpu"] = strings.TrimSpace(strings.SplitN(line, ":", 2)[1])
				break
			}
		}
	}
	if b, err := os.ReadFile("/sys/fs/cgroup/cpu.max"); err == nil {
		info["cgroup_cpu_max_visible"] = strings.TrimSpace(string(b))
	}
	if b, err := os.ReadFile("/proc/meminfo"); err == nil {
		info["meminfo_total"] = strings.TrimSpace(strings.SplitN(string(b), "\n", 2)[0])
	}
	if out, err := exec.Command("uname", "-r").Output(); err == nil {
		info["kernel"] = strings.TrimSpace(string(out))
	}
	if out, err := exec.Command("tmux", "-V").Output(); err == nil {
		info["tmux"] = strings.TrimSpace(string(out))
	}
	return info
}

func writeReport(dir string, r Report) {
	b, _ := json.MarshalIndent(r, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "results.json"), b, 0o644); err != nil {
		log.Printf("writing results: %v", err)
	}
	os.WriteFile(filepath.Join(dir, "results.md"), []byte(markdown(r)), 0o644)
}

// ---- one tier ---------------------------------------------------------------

type target struct {
	ID      int64
	Name    string
	Home    string // $HOME of the agents on this target
	TmuxDir string // TMUX_TMPDIR of this target's tmux server
	Workdir string
}

type tier struct {
	o       options
	n       int
	dir     string
	base    string
	cmd     *exec.Cmd
	logPath string
	targets []target
	fixture []*exec.Cmd
	client  *http.Client

	httpErrMu sync.Mutex
	httpErr   map[string]int
}

func (t *tier) countErr(what string) {
	t.httpErrMu.Lock()
	t.httpErr[what]++
	t.httpErrMu.Unlock()
}

func (t *tier) do(method, path string, body any) (int, []byte, time.Duration, error) {
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, t.base+path, rdr)
	if err != nil {
		return 0, nil, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	start := time.Now()
	resp, err := t.client.Do(req)
	if err != nil {
		return 0, nil, time.Since(start), err
	}
	out, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp.StatusCode, out, time.Since(start), err
}

func freePort() int {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func runTier(o options, n int) (res TierResult, err error) {
	res.Sessions = n
	t := &tier{o: o, n: n, client: &http.Client{Timeout: 60 * time.Second}, httpErr: map[string]int{}}
	t.dir, err = os.MkdirTemp("", fmt.Sprintf("stress-%d-", n))
	if err != nil {
		return res, err
	}
	defer t.teardown()
	if err := t.start(); err != nil {
		return res, err
	}
	for _, tg := range t.targets {
		res.Targets = append(res.Targets, tg.Name)
	}
	defer func() {
		res.HTTPErrors = t.httpErr
		res.LogCounts, res.LogSamples = t.logLevels()
	}()

	// Everything that watches starts before the load does.
	stop := make(chan struct{})
	var stopOnce sync.Once
	stopAll := func() { stopOnce.Do(func() { close(stop) }) }
	defer stopAll()
	fan := newFanout(o.sseClients)
	var sseDrops atomic.Int64
	for i := 0; i < o.sseClients; i++ {
		go func(i int) {
			for {
				err := streamSSE(t.base, "/api/stream", stop, func(ev sseEvent) { fan.seen(i, ev) })
				select {
				case <-stop:
					return
				default:
				}
				sseDrops.Add(1)
				log.Printf("SSE client %d dropped: %v", i, err)
				time.Sleep(500 * time.Millisecond)
			}
		}(i)
	}
	appr := newApprover(t)
	go func() {
		for {
			err := streamSSE(t.base, "/api/stream", stop, appr.onEvent)
			select {
			case <-stop:
				return
			default:
			}
			sseDrops.Add(1)
			log.Printf("approver SSE dropped: %v", err)
			time.Sleep(500 * time.Millisecond)
		}
	}()
	time.Sleep(300 * time.Millisecond)

	// Launch.
	ids, launch, launchErrs, wall := t.launch()
	res.Launch, res.LaunchOK, res.LaunchFailed, res.LaunchWallSec = launch.stats(), len(ids), n-len(ids), round(wall.Seconds())
	if len(launchErrs) > 5 {
		launchErrs = launchErrs[:5]
	}
	res.LaunchErrors = launchErrs
	if len(ids) == 0 {
		return res, errors.New("no session launched")
	}
	// Let every session get past "starting" before the window opens, so the
	// window measures steady state rather than the launch burst.
	settle := time.Now().Add(30 * time.Second)
	for time.Now().Before(settle) && t.countActive(ids, "starting") > 0 {
		time.Sleep(time.Second)
	}
	res.ActiveAtStart = t.countActive(ids, "")

	// Steady-state window.
	var hw *hangWatch
	mine := map[int64]bool{}
	for _, id := range ids {
		mine[id] = true
	}
	healthy := ids
	if o.hangAt > 0 && len(t.fixture) > 0 {
		hw = &hangWatch{target: "ssh-1"}
		healthy = t.idsNotOn(ids, hw.target)
	}
	appr.open()
	samp := startSampler(t.cmd.Process.Pid)
	windowStart := time.Now()
	windowEnd := windowStart.Add(o.window)
	if hw != nil {
		go func() {
			time.Sleep(time.Until(windowStart.Add(o.hangAt)))
			log.Printf("freezing %s", hw.target)
			hw.freeze(t.fixture[0].Process.Pid)
		}()
	}
	var wg sync.WaitGroup
	list := &series{}
	var listBytes, listErrs atomic.Int64
	wg.Add(1)
	go func() { // one dashboard polling the list 4x a second
		defer wg.Done()
		for time.Now().Before(windowEnd) {
			code, body, d, err := t.do("GET", "/api/sessions", nil)
			if err != nil || code != 200 {
				listErrs.Add(1)
				t.countErr("GET /api/sessions")
			} else {
				list.add(d)
				listBytes.Store(int64(len(body)))
				if hw != nil {
					hw.observe(time.Now(), body, mine)
				}
			}
			time.Sleep(250 * time.Millisecond)
		}
	}()
	wg.Add(1)
	go func() { // live-update fan-out probes every 2s
		defer wg.Done()
		seq := 0
		for time.Now().Add(3 * time.Second).Before(windowEnd) {
			seq++
			name := fmt.Sprintf("probe-%d", seq)
			fan.expect(name, time.Now())
			code, _, _, err := t.do("PATCH", fmt.Sprintf("/api/sessions/%d", ids[0]), map[string]any{"name": name})
			if err != nil || code != 200 {
				t.countErr("PATCH /api/sessions/{id}")
			}
			time.Sleep(2 * time.Second)
		}
	}()
	// Terminal attach and the TUI run inside the window so they see the load.
	time.Sleep(o.window / 4)
	res.AttachPost, res.AttachFirstOut, res.AttachTotal, res.AttachErrors = t.attach(healthy)
	if o.holdTerminals > 0 {
		res.Hold = t.holdTerminals(healthy, o.holdTerminals)
	}
	res.TUI = t.tui()
	wg.Wait()
	for time.Now().Before(windowEnd) {
		time.Sleep(100 * time.Millisecond)
	}
	res.Usage = samp.finish()
	if hw != nil {
		hw.resume(t.fixture[0].Process.Pid)
		for limit := time.Now().Add(90 * time.Second); time.Now().Before(limit); time.Sleep(250 * time.Millisecond) {
			if code, body, _, err := t.do("GET", "/api/sessions", nil); err == nil && code == 200 {
				hw.observe(time.Now(), body, mine)
			}
			if r := hw.result(); r.SecondsToRecover >= 0 {
				break
			}
		}
		res.Hang = hw.result()
	}
	appr.close()
	res.ActiveAtEnd = t.countActive(ids, "")
	res.List, res.ListBytes, res.ListErrors = list.stats(), int(listBytes.Load()), int(listErrs.Load())
	res.FanoutClients = o.sseClients
	res.Fanout, res.FanoutProbes, res.FanoutMissed = fan.result()
	// give in-flight approvals a moment to land in the agent logs
	time.Sleep(2 * time.Second)
	a := appr.result(t.agentLogs())
	res.ApprovalsRequested, res.ApprovalsApproved, res.ApprovalsOther = a.requested, a.approved, a.other
	res.ApprovalVisible, res.ApprovalDecide, res.ApprovalUnblock, res.ApprovalRoundTrip = a.visible, a.decide, a.unblock, a.roundTrip
	res.SSEDrops = int(sseDrops.Load())
	stopAll()

	// Teardown through the API, the way an operator ends sessions.
	del := &series{}
	for _, id := range ids {
		code, _, d, err := t.do("DELETE", fmt.Sprintf("/api/sessions/%d", id), nil)
		if err != nil || code != 200 {
			t.countErr("DELETE /api/sessions/{id}")
			continue
		}
		del.add(d)
	}
	res.Teardown = del.stats()
	return res, nil
}

// start brings up the SSH fixtures, the Lectern instance and the targets.
func (t *tier) start() error {
	logDir := filepath.Join(t.dir, "agent-logs")
	os.MkdirAll(logDir, 0o755)
	heartbeat := "0"
	if t.o.hangAt > 0 {
		heartbeat = "1"
	}
	agent := strings.NewReplacer("__STRESS_LOG_DIR__", logDir,
		"__STRESS_APPROVAL_EVERY__", strconv.Itoa(int(t.o.approvalEvery.Seconds())),
		"__STRESS_HEARTBEAT__", heartbeat).Replace(fakeClaude)
	agentPath := filepath.Join(t.dir, "claude")
	if err := os.WriteFile(agentPath, []byte(agent), 0o755); err != nil {
		return err
	}
	key := filepath.Join(t.dir, "id_ed25519")
	if t.o.sshTargets > 0 {
		if out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", key).CombinedOutput(); err != nil {
			return fmt.Errorf("ssh-keygen: %v %s", err, out)
		}
	}
	type sshFix struct{ port int }
	var fixtures []sshFix
	for i := 0; i < t.o.sshTargets; i++ {
		ready := filepath.Join(t.dir, fmt.Sprintf("ssh-%d.json", i+1))
		cmd := exec.Command(t.o.python, t.o.fixture, "--key-path", key, "--ready-path", ready)
		cmd.Stdout, cmd.Stderr = io.Discard, os.Stderr
		if err := cmd.Start(); err != nil {
			return fmt.Errorf("SSH fixture: %w", err)
		}
		t.fixture = append(t.fixture, cmd)
		var port int
		for j := 0; j < 200 && port == 0; j++ {
			if b, err := os.ReadFile(ready); err == nil {
				var r struct {
					Port int `json:"port"`
				}
				if json.Unmarshal(b, &r) == nil {
					port = r.Port
				}
			}
			time.Sleep(50 * time.Millisecond)
		}
		if port == 0 {
			return errors.New("SSH fixture did not publish its port")
		}
		fixtures = append(fixtures, sshFix{port})
	}

	port := freePort()
	t.base = fmt.Sprintf("http://127.0.0.1:%d", port)
	home := filepath.Join(t.dir, "home-local")
	tmuxLocal := filepath.Join(t.dir, "tmux-local")
	for _, d := range []string{home, tmuxLocal, filepath.Join(t.dir, "scratch")} {
		os.MkdirAll(d, 0o700)
	}
	t.logPath = filepath.Join(t.dir, "lectern.log")
	logf, err := os.Create(t.logPath)
	if err != nil {
		return err
	}
	none := filepath.Join(t.dir, "none.json")
	cmd := exec.Command(t.o.lectern, "serve")
	cmd.Env = append(os.Environ(),
		"HOME="+home, "TMUX=", "TMUX_TMPDIR="+tmuxLocal,
		"LECTERN_PORT="+strconv.Itoa(port), "LECTERN_HOST=127.0.0.1", "LECTERN_AUTH=none",
		"LECTERN_DB="+filepath.Join(t.dir, "lectern.db"),
		"LECTERN_CLAUDE_BIN="+agentPath,
		"LECTERN_HOST_CLAUDE_CONFIG="+none, "LECTERN_CREDS="+none, "LECTERN_CODEX_CREDS="+none,
		"LECTERN_SCRATCH_ROOT="+filepath.Join(t.dir, "scratch"),
		"LECTERN_BASE_URL="+t.base,
	)
	if t.o.terminalPorts != "" {
		cmd.Env = append(cmd.Env, "LECTERN_TERMINAL_PORTS="+t.o.terminalPorts)
	}
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	t.cmd = cmd
	up := false
	for i := 0; i < 300 && !up; i++ {
		if code, _, _, err := t.do("GET", "/api/health", nil); err == nil && code == 200 {
			up = true
		} else {
			time.Sleep(100 * time.Millisecond)
		}
	}
	if !up {
		return errors.New("lectern did not answer /api/health")
	}

	mk := func(name string, body map[string]any, home, tmuxDir string) error {
		work := filepath.Join(t.dir, "work-"+name)
		os.MkdirAll(work, 0o755)
		if !t.o.plainDirs {
			// one repository per target; each agent gets its own worktree of it
			if err := gitInit(filepath.Join(work, "repo")); err != nil {
				return err
			}
		}
		body["name"] = name
		body["workroot"] = work
		body["max_concurrent"] = 1000
		code, out, _, err := t.do("POST", "/api/targets", body)
		if err != nil || code >= 300 {
			return fmt.Errorf("creating target %s: %d %s %v", name, code, out, err)
		}
		var row struct {
			ID int64 `json:"id"`
		}
		json.Unmarshal(out, &row)
		t.targets = append(t.targets, target{ID: row.ID, Name: name, Home: home, TmuxDir: tmuxDir, Workdir: work})
		return nil
	}
	if err := mk("local", map[string]any{"kind": "local"}, home, tmuxLocal); err != nil {
		return err
	}
	for i, f := range fixtures {
		name := fmt.Sprintf("ssh-%d", i+1)
		h := filepath.Join(t.dir, "home-"+name)
		td := filepath.Join(t.dir, "tmux-"+name)
		os.MkdirAll(h, 0o700)
		os.MkdirAll(td, 0o700)
		prefix := fmt.Sprintf("env HOME=%s SHELL=/bin/bash TMUX= TMUX_TMPDIR=%s sh -c", h, td)
		if err := mk(name, map[string]any{"kind": "ssh", "host": "127.0.0.1", "port": f.port,
			"user": "fixture", "key_path": key, "command_prefix": prefix}, h, td); err != nil {
			return err
		}
	}
	return nil
}

func gitInit(repo string) error {
	for _, args := range [][]string{
		{"init", "-q", "-b", "main", repo},
		{"-C", repo, "-c", "user.name=stress", "-c", "user.email=stress@example.com",
			"commit", "-q", "--allow-empty", "-m", "initial"},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			return fmt.Errorf("git %v: %v %s", args, err, out)
		}
	}
	return nil
}

// launch starts n sessions round-robin across targets, launchParallel at a time.
func (t *tier) launch() ([]int64, *series, []string, time.Duration) {
	lat := &series{}
	var mu sync.Mutex
	var ids []int64
	var errs []string
	sem := make(chan struct{}, t.o.launchParallel)
	var wg sync.WaitGroup
	start := time.Now()
	for i := 0; i < t.n; i++ {
		tg := t.targets[i%len(t.targets)]
		wd := filepath.Join(tg.Workdir, fmt.Sprintf("agent-%03d", i+1))
		if t.o.plainDirs {
			os.MkdirAll(wd, 0o755)
		} else if out, err := exec.Command("git", "-C", filepath.Join(tg.Workdir, "repo"), "worktree", "add", "-q",
			"-b", fmt.Sprintf("agent-%03d", i+1), wd).CombinedOutput(); err != nil {
			log.Printf("worktree for agent %d: %v %s", i+1, err, out)
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, tg target, wd string) {
			defer wg.Done()
			defer func() { <-sem }()
			code, out, d, err := t.do("POST", "/api/sessions", map[string]any{
				"target_id": tg.ID, "workdir": wd, "name": fmt.Sprintf("agent-%03d", i+1),
				"agent": "claude", "permission_mode": "ask"})
			mu.Lock()
			defer mu.Unlock()
			if err != nil || code >= 300 {
				t.countErr("POST /api/sessions")
				errs = append(errs, fmt.Sprintf("%d %s %v", code, strings.TrimSpace(string(out)), err))
				return
			}
			var row struct {
				ID int64 `json:"id"`
			}
			json.Unmarshal(out, &row)
			ids = append(ids, row.ID)
			lat.add(d)
		}(i, tg, wd)
	}
	wg.Wait()
	return ids, lat, errs, time.Since(start)
}

// countActive counts launched sessions whose status is not dead (or, with
// want set, whose status equals want).
func (t *tier) countActive(ids []int64, want string) int {
	code, body, _, err := t.do("GET", "/api/sessions", nil)
	if err != nil || code != 200 {
		return -1
	}
	var rows []struct {
		ID     int64  `json:"id"`
		Status string `json:"status"`
	}
	json.Unmarshal(body, &rows)
	mine := map[int64]bool{}
	for _, id := range ids {
		mine[id] = true
	}
	n := 0
	for _, r := range rows {
		if !mine[r.ID] {
			continue
		}
		if want != "" && r.Status == want || want == "" && r.Status != "dead" {
			n++
		}
	}
	return n
}

// idsNotOn returns the launched sessions that are not on the named target.
func (t *tier) idsNotOn(ids []int64, targetName string) []int64 {
	_, body, _, err := t.do("GET", "/api/sessions", nil)
	if err != nil {
		return ids
	}
	var rows []listRow
	json.Unmarshal(body, &rows)
	on := map[int64]bool{}
	for _, r := range rows {
		if r.TargetName == targetName {
			on[r.ID] = true
		}
	}
	var out []int64
	for _, id := range ids {
		if !on[id] {
			out = append(out, id)
		}
	}
	return out
}

// attach opens a web terminal on a sample of sessions spread over targets.
func (t *tier) attach(ids []int64) (post, first, total Stats, errs []string) {
	ps, fs, ts := &series{}, &series{}, &series{}
	k := min(t.o.attachSamples, len(ids))
	for i := 0; i < k; i++ {
		id := ids[i*len(ids)/k]
		start := time.Now()
		code, out, d, err := t.do("POST", fmt.Sprintf("/api/sessions/%d/terminal", id), map[string]any{})
		if err != nil || code != 200 {
			t.countErr("POST /api/sessions/{id}/terminal")
			errs = append(errs, fmt.Sprintf("session %d attach: %d %s %v", id, code, strings.TrimSpace(string(out)), err))
			continue
		}
		ps.add(d)
		var r struct {
			URL string `json:"url"`
		}
		json.Unmarshal(out, &r)
		fo, err := ttydFirstOutput(t.base, r.URL, 20*time.Second)
		if err != nil {
			t.countErr("terminal websocket")
			errs = append(errs, fmt.Sprintf("session %d websocket: %v", id, err))
			continue
		}
		fs.add(fo)
		ts.add(time.Since(start))
	}
	return ps.stats(), fs.stats(), ts.stats(), errs
}

// tui runs the dashboard refresh measurement from internal/console against
// this instance and returns its JSON.
func (t *tier) tui() json.RawMessage {
	if t.o.consoleTest == "" {
		return nil
	}
	out := filepath.Join(t.dir, "tui.json")
	cmd := exec.Command(t.o.consoleTest, "-test.run", "^TestStressDashboardRefresh$", "-test.count=1", "-test.v")
	cmd.Env = append(os.Environ(), "LECTERN_STRESS_URL="+t.base, "LECTERN_STRESS_JSON="+out, "HOME="+t.dir)
	if b, err := cmd.CombinedOutput(); err != nil {
		log.Printf("TUI measurement failed: %v\n%s", err, b)
		t.countErr("tui")
		return nil
	}
	b, err := os.ReadFile(out)
	if err != nil {
		return nil
	}
	return b
}

func (t *tier) agentLogs() map[string][]string {
	out := map[string][]string{}
	files, _ := filepath.Glob(filepath.Join(t.dir, "agent-logs", "*.log"))
	for _, f := range files {
		b, _ := os.ReadFile(f)
		out[strings.TrimSuffix(filepath.Base(f), ".log")] = strings.Split(strings.TrimSpace(string(b)), "\n")
	}
	return out
}

func (t *tier) logLevels() (map[string]int, []string) {
	counts := map[string]int{}
	var samples []string
	b, _ := os.ReadFile(t.logPath)
	seen := map[string]bool{}
	for _, line := range strings.Split(string(b), "\n") {
		for _, lvl := range []string{"WARN", "ERROR"} {
			if strings.Contains(line, "level="+lvl) {
				counts[lvl]++
				msg := line
				if i := strings.Index(line, "msg="); i >= 0 {
					msg = line[i:]
				}
				if len(msg) > 160 {
					msg = msg[:160]
				}
				key := msg
				if j := strings.Index(msg, " "); j > 0 && strings.HasPrefix(msg, "msg=\"") {
					if k := strings.Index(msg[5:], "\""); k > 0 {
						key = msg[:5+k]
					}
				}
				if !seen[key] && len(samples) < 12 {
					seen[key] = true
					samples = append(samples, lvl+" "+msg)
				}
			}
		}
	}
	return counts, samples
}

func (t *tier) teardown() {
	if t.cmd != nil && t.cmd.Process != nil {
		syscall.Kill(-t.cmd.Process.Pid, syscall.SIGTERM)
		done := make(chan struct{})
		go func() { t.cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			syscall.Kill(-t.cmd.Process.Pid, syscall.SIGKILL)
			<-done
		}
	}
	// Every tmux server this tier started lives on a socket under t.dir. The
	// namespace's tmux wrapper turns kill-server into exact kill-session calls
	// on that socket, so nothing outside this tier can be touched.
	for _, tg := range t.targets {
		c := exec.Command("tmux", "kill-server")
		c.Env = append(os.Environ(), "TMUX=", "TMUX_TMPDIR="+tg.TmuxDir)
		c.Run()
	}
	for _, f := range t.fixture {
		if f.Process != nil {
			f.Process.Signal(syscall.SIGCONT) // a frozen fixture cannot act on SIGTERM
			f.Process.Signal(syscall.SIGTERM)
			f.Wait()
		}
	}
	// ttyd and stand-in agents started by the tier exit with their tmux
	// sessions; give them a moment before the next tier samples CPU.
	time.Sleep(2 * time.Second)
	if b, err := os.ReadFile(t.logPath); err == nil {
		os.WriteFile(filepath.Join(t.o.out, fmt.Sprintf("lectern-%d.log", t.n)), b, 0o644)
	}
	os.RemoveAll(t.dir)
}
