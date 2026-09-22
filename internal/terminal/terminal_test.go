package terminal

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/JeremiahM37/lectern/internal/store"
)

// fakeTTYD stands in for the real binary: it records the port it was handed and
// stays alive, the way a bound ttyd does.
func fakeManager(t *testing.T) (*Manager, func() []int) {
	t.Helper()
	m := NewManager()
	m.LookPath = func(string) (string, error) { return "/usr/bin/ttyd", nil }
	var mu sync.Mutex
	var ports []int
	m.Spawn = func(port int, basePath string, argv []string) (*exec.Cmd, error) {
		mu.Lock()
		ports = append(ports, port)
		mu.Unlock()
		cmd := exec.Command("sleep", "30")
		if err := cmd.Start(); err != nil {
			return nil, err
		}
		return cmd, nil
	}
	t.Cleanup(m.Shutdown)
	return m, func() []int {
		mu.Lock()
		defer mu.Unlock()
		out := append([]int(nil), ports...)
		return out
	}
}

func target(kind string) *store.Target {
	return &store.Target{Kind: kind, Host: "192.168.0.5", User: "admin", ID: 1}
}

// Two people clicking attach at the same moment is ordinary — the board shows a
// running attempt and a live session side by side. If both land on one port the
// second ttyd cannot bind and its terminal is dead on arrival.
func TestConcurrentAttachesGetDistinctPorts(t *testing.T) {
	m, spawned := fakeManager(t)
	const n = 6
	var wg sync.WaitGroup
	got := make([]int, n)
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			got[i], errs[i] = m.Attach(context.Background(),
				Attachment{Key: fmt.Sprintf("attempt:%d", i), TmuxSession: "lec-1"}, target("local"))
		}(i)
	}
	wg.Wait()

	seen := map[int]int{}
	for i, port := range got {
		if errs[i] != nil {
			t.Fatalf("attach %d failed: %v", i, errs[i])
		}
		if prev, dup := seen[port]; dup {
			t.Fatalf("attachments %d and %d were both given port %d — the second ttyd "+
				"cannot bind, so that terminal is dead on arrival", prev, i, port)
		}
		seen[port] = i
	}
	if len(spawned()) != n {
		t.Errorf("spawned %d ttyds for %d attachments", len(spawned()), n)
	}
}

// Attaching twice to the same thing must reuse the existing terminal rather than
// burning another port from a range of twenty.
func TestAttachingTwiceReusesTheSameTerminal(t *testing.T) {
	m, spawned := fakeManager(t)
	a := Attachment{Key: "session:3", TmuxSession: "lec-sess-3"}
	first, err := m.Attach(context.Background(), a, target("local"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := m.Attach(context.Background(), a, target("local"))
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Errorf("the same attachment got two ports: %d then %d", first, second)
	}
	if len(spawned()) != 1 {
		t.Errorf("spawned %d ttyds for one attachment", len(spawned()))
	}
}

// An attempt and a session must never share a ttyd — the keys are namespaced
// precisely so id 3 in one kind cannot collide with id 3 in the other.
func TestAttemptAndSessionKeysDoNotCollide(t *testing.T) {
	m, _ := fakeManager(t)
	x, err := m.Attach(context.Background(), Attachment{Key: "attempt:3", TmuxSession: "lec-3"}, target("local"))
	if err != nil {
		t.Fatal(err)
	}
	y, err := m.Attach(context.Background(), Attachment{Key: "session:3", TmuxSession: "lec-sess-3"}, target("local"))
	if err != nil {
		t.Fatal(err)
	}
	if x == y {
		t.Errorf("attempt:3 and session:3 shared port %d", x)
	}
}

func TestPortsAreReturnedWhenTerminalsAreShutDown(t *testing.T) {
	m, _ := fakeManager(t)
	for i := 0; i < 4; i++ {
		if _, err := m.Attach(context.Background(),
			Attachment{Key: fmt.Sprintf("attempt:%d", i)}, target("local")); err != nil {
			t.Fatal(err)
		}
	}
	m.Shutdown()
	m.mu.Lock()
	n := len(m.procs)
	m.mu.Unlock()
	if n != 0 {
		t.Errorf("%d terminals still tracked after shutdown", n)
	}
	// the range is only twenty wide, so a leak here exhausts it in a day's use
	if _, err := m.Attach(context.Background(),
		Attachment{Key: "attempt:99"}, target("local")); err != nil {
		t.Fatalf("could not attach after a shutdown: %v", err)
	}
}

// Terminals no longer exit when their last viewer leaves, so the range can fill
// with ones nobody is looking at. Refusing to attach at that point would make
// the board unusable until a restart, so the oldest is retired instead — the
// tmux session behind it is untouched, and re-attaching costs one click.
func TestAFullRangeRetiresTheOldestTerminal(t *testing.T) {
	m, _ := fakeManager(t)
	var first string
	got := 0
	for i := 0; i <= PortHi-PortLo; i++ {
		key := fmt.Sprintf("attempt:%d", i)
		if _, err := m.Attach(context.Background(),
			Attachment{Key: key}, target("local")); err != nil {
			break
		}
		if first == "" {
			first = key
		}
		got++
	}
	if got == 0 {
		t.Fatal("could not allocate a single terminal")
	}
	m.mu.Lock()
	held := len(m.procs)
	m.mu.Unlock()

	// one more than the range holds must still succeed
	port, err := m.Attach(context.Background(),
		Attachment{Key: "one-too-many"}, target("local"))
	if err != nil {
		t.Fatalf("a full range refused a new terminal instead of making room: %v", err)
	}
	if port < PortLo || port > PortHi {
		t.Errorf("port %d is outside the range", port)
	}
	m.mu.Lock()
	nowHeld := len(m.procs)
	_, oldestStillThere := m.procs[first]
	m.mu.Unlock()

	if nowHeld > held {
		t.Errorf("the range grew from %d to %d instead of recycling", held, nowHeld)
	}
	if got > PortHi-PortLo && oldestStillThere {
		t.Errorf("the oldest terminal (%s) was not the one retired", first)
	}
}

// The argv is what actually reaches the target. A wrong one fails at connect
// time in a browser tab, which is the worst place to discover it.
func TestAttachArgvPerTargetKind(t *testing.T) {
	for name, tc := range map[string]struct {
		a      Attachment
		target *store.Target
		want   []string
	}{
		"local": {
			Attachment{TmuxSession: "lec-7"},
			&store.Target{Kind: "local"},
			[]string{"tmux", "attach", "-t", "lec-7", ";", "set-option", "-w", "-t", "=lec-7:", "window-size", "smallest"},
		},
		"pct container": {
			Attachment{TmuxSession: "lec-7"},
			&store.Target{Kind: "pct", Host: "104"},
			[]string{"sudo", "pct", "exec", "104", "--", "tmux", "attach", "-t", "lec-7", ";", "set-option", "-w", "-t", "=lec-7:", "window-size", "smallest"},
		},
		"ephemeral sandbox beats the target kind": {
			Attachment{TmuxSession: "lec-7", SandboxVMID: "9001"},
			&store.Target{Kind: "sandbox", Host: "irrelevant"},
			[]string{"sudo", "pct", "exec", "9001", "--", "tmux", "attach", "-t", "lec-7", ";", "set-option", "-w", "-t", "=lec-7:", "window-size", "smallest"},
		},
		"ssh with a key": {
			Attachment{TmuxSession: "lec-7"},
			&store.Target{Kind: "ssh", Host: "192.0.2.14", User: "claude", KeyPath: "/home/admin/.ssh/id_ed25519"},
			[]string{"ssh", "-tt", "-o", "StrictHostKeyChecking=accept-new",
				"-i", "/home/admin/.ssh/id_ed25519", "claude@192.0.2.14", "tmux", "attach", "-t", "lec-7", "';'", "set-option", "-w", "-t", "=lec-7:", "window-size", "smallest"},
		},
		"ssh defaults to root": {
			Attachment{TmuxSession: "lec-7"},
			&store.Target{Kind: "ssh", Host: "h"},
			[]string{"ssh", "-tt", "-o", "StrictHostKeyChecking=accept-new", "root@h", "tmux", "attach", "-t", "lec-7", "';'", "set-option", "-w", "-t", "=lec-7:", "window-size", "smallest"},
		},
	} {
		got, err := AttachArgv(tc.a, tc.target)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if strings.Join(got, " ") != strings.Join(tc.want, " ") {
			t.Errorf("%s:\n got %v\nwant %v", name, got, tc.want)
		}
	}
}

// A sandbox attach that lost its vmid must not silently fall through to running
// tmux on the control plane itself.
func TestSandboxWithoutAVMIDDoesNotAttachToTheHost(t *testing.T) {
	got, err := AttachArgv(Attachment{TmuxSession: "lec-7"}, &store.Target{Kind: "sandbox"})
	if err == nil {
		t.Fatalf("a sandbox attach with no vmid must fail, got %v", got)
	}
	if strings.Join(got, " ") == "tmux attach -t lec-7" {
		t.Error("it fell through to the control plane's own tmux")
	}
	// and the failure must reach the caller rather than spawning anything
	m, spawned := fakeManager(t)
	if _, err := m.Attach(context.Background(),
		Attachment{Key: "attempt:1"}, &store.Target{Kind: "sandbox"}); err == nil {
		t.Error("Attach accepted a sandbox with no container id")
	}
	if len(spawned()) != 0 {
		t.Errorf("it spawned %d ttyds anyway", len(spawned()))
	}
}

func TestAttachFailsClearlyWithoutTTYD(t *testing.T) {
	m, _ := fakeManager(t)
	m.LookPath = func(string) (string, error) { return "", exec.ErrNotFound }
	_, err := m.Attach(context.Background(), Attachment{Key: "attempt:1"}, target("local"))
	if err == nil || !strings.Contains(err.Error(), "ttyd is not installed") {
		t.Errorf("expected a clear missing-ttyd error, got %v", err)
	}
}

// A shell is the same machinery as an agent attach, pointed at a directory. It
// has to be a tmux session too, or closing the tab loses whatever you were
// halfway through.
func TestShellAttachOpensAPersistentSessionInTheRepo(t *testing.T) {
	att := Attachment{Key: "project:12", TmuxSession: "lec-sh12", Workdir: "/srv/code"}
	for name, tc := range map[string]struct {
		target *store.Target
		want   string
	}{
		"local": {&store.Target{Kind: "local"},
			"tmux new-session -A -s lec-sh12 -c /srv/code -- /bin/sh -c exec \"${SHELL:-/bin/sh}\" -i ; set-option -w -t =lec-sh12: window-size smallest"},
		"pct": {&store.Target{Kind: "pct", Host: "104"},
			"sudo pct exec 104 -- tmux new-session -A -s lec-sh12 -c /srv/code -- /bin/sh -c exec \"${SHELL:-/bin/sh}\" -i ; set-option -w -t =lec-sh12: window-size smallest"},
		"ssh": {&store.Target{Kind: "ssh", Host: "192.0.2.14", User: "claude"},
			"ssh -tt -o StrictHostKeyChecking=accept-new claude@192.0.2.14 " +
				"tmux new-session -A -s lec-sh12 -c /srv/code -- /bin/sh -c 'exec \"${SHELL:-/bin/sh}\" -i' ';' set-option -w -t =lec-sh12: window-size smallest"},
	} {
		got, err := AttachArgv(att, tc.target)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if strings.Join(got, " ") != tc.want {
			t.Errorf("%s:\n got %v\nwant %s", name, got, tc.want)
		}
	}
	// -A is what makes it the SAME shell when you come back
	got, _ := AttachArgv(att, &store.Target{Kind: "local"})
	if !strings.Contains(strings.Join(got, " "), "new-session -A") {
		t.Error("reopening a shell must return to the existing one, not start over")
	}
}

// An agent attach must not accidentally become a shell, or reopening a session
// would create a new tmux session beside the agent instead of joining it.
func TestAnAgentAttachIsStillAnAttach(t *testing.T) {
	att := Attachment{Key: "session:3", TmuxSession: "lec-s3"}
	if att.IsShell() {
		t.Fatal("an attachment with no workdir is not a shell")
	}
	got, _ := AttachArgv(att, &store.Target{Kind: "local"})
	if strings.Join(got, " ") != "tmux attach -t lec-s3 ; set-option -w -t =lec-s3: window-size smallest" {
		t.Errorf("got %v", got)
	}
}

func TestSSHAttachPreservesPortWrapperAndQuotedWorkingDirectory(t *testing.T) {
	att := Attachment{Key: "project:7", TmuxSession: "project-7", Workdir: "/tmp/a path/it's $(touch bad)"}
	got, err := AttachArgv(att, &store.Target{Kind: "ssh", Host: "example.test", User: "operator", Port: 2222, CommandPrefix: "wrapper {cmd}"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(got, " "), "-p 2222") {
		t.Fatalf("port lost: %v", got)
	}
	if len(got) != 8 || got[6] != "operator@example.test" {
		t.Fatalf("SSH command must be one argument: %v", got)
	}
	if !strings.HasPrefix(got[7], "wrapper ") {
		t.Fatalf("wrapper lost: %v", got)
	}
	// Execute a harmless wrapper that reports its received arguments. This proves
	// that spaces, apostrophes and command substitutions survive both shell layers.
	dir := t.TempDir()
	script := filepath.Join(dir, "wrapper")
	os.WriteFile(script, []byte("#!/bin/sh\nprintf '%s' \"$1\"\n"), 0700)
	cmd := exec.Command("sh", "-c", got[7])
	cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"))
	output, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	command := string(output)
	if !strings.Contains(command, "tmux new-session") || !strings.Contains(command, "touch bad") {
		t.Fatalf("mangled command: %s", command)
	}
	// A Windows SSH wrapper gets a base64 command, avoiding cmd.exe's parsing.
	got, err = AttachArgv(att, &store.Target{Kind: "ssh", Host: "desktop", CommandPrefix: `wsl -e bash -lc "echo {b64} | base64 -d | bash"`})
	if err != nil || strings.Contains(got[len(got)-1], "touch bad") {
		t.Fatalf("unencoded wrapper: %v %v", got, err)
	}
}
