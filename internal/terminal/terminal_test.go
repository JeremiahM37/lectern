package terminal

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// fakeTTYD stands in for the real binary: it records the socket it was handed
// and stays alive, the way a listening ttyd does.
func fakeManager(t *testing.T) (*Manager, func() []string) {
	t.Helper()
	m := NewManager()
	m.SocketDir = t.TempDir()
	m.LookPath = func(string) (string, error) { return "/usr/bin/ttyd", nil }
	var mu sync.Mutex
	var sockets []string
	m.Spawn = func(socket, basePath string, argv []string) (*exec.Cmd, error) {
		mu.Lock()
		sockets = append(sockets, socket)
		mu.Unlock()
		cmd := exec.Command("sleep", "30")
		if err := cmd.Start(); err != nil {
			return nil, err
		}
		return cmd, nil
	}
	t.Cleanup(m.Shutdown)
	return m, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), sockets...)
	}
}

func target(kind string) *store.Target {
	return &store.Target{Kind: kind, Host: "192.168.0.5", User: "admin", ID: 1}
}

// Two people clicking attach at the same moment is ordinary — the board shows a
// running attempt and a live session side by side. Each must get its own ttyd.
func TestConcurrentAttachesGetDistinctSockets(t *testing.T) {
	m, spawned := fakeManager(t)
	const n = 6
	var wg sync.WaitGroup
	got := make([]string, n)
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

	seen := map[string]int{}
	for i, socket := range got {
		if errs[i] != nil {
			t.Fatalf("attach %d failed: %v", i, errs[i])
		}
		if prev, dup := seen[socket]; dup {
			t.Fatalf("attachments %d and %d were both given %s", prev, i, socket)
		}
		seen[socket] = i
	}
	if len(spawned()) != n {
		t.Errorf("spawned %d ttyds for %d attachments", len(spawned()), n)
	}
}

// Attaching twice to the same thing must reuse the existing terminal rather
// than starting a second ttyd.
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
		t.Errorf("the same attachment got two terminals: %s then %s", first, second)
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
		t.Errorf("attempt:3 and session:3 shared %s", x)
	}
}

// Sockets live in a directory only this user can enter — a ttyd is an
// unauthenticated shell — and each Lectern has its own. On loopback ports,
// two instances on one host picked the same port and served each other's
// terminals, and any local user could connect.
func TestSocketsArePrivateAndPerInstance(t *testing.T) {
	m, _ := fakeManager(t)
	other, _ := fakeManager(t)
	other.SocketDir = m.SocketDir // same parent, as two instances on one host
	a, err := m.Attach(context.Background(), Attachment{Key: "session:1"}, target("local"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := other.Attach(context.Background(), Attachment{Key: "session:1"}, target("local"))
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(a) == filepath.Dir(b) {
		t.Fatalf("two managers shared a socket directory: %s", filepath.Dir(a))
	}
	info, err := os.Stat(filepath.Dir(a))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("socket directory mode %v, want 0700", info.Mode().Perm())
	}
}

func TestTerminalsAreReleasedWhenShutDown(t *testing.T) {
	m, _ := fakeManager(t)
	var dir string
	for i := 0; i < 4; i++ {
		socket, err := m.Attach(context.Background(), Attachment{Key: fmt.Sprintf("attempt:%d", i)}, target("local"))
		if err != nil {
			t.Fatal(err)
		}
		dir = filepath.Dir(socket)
	}
	m.Shutdown()
	m.mu.Lock()
	n := len(m.procs)
	m.mu.Unlock()
	if n != 0 {
		t.Errorf("%d terminals still tracked after shutdown", n)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("socket directory left behind: %v", err)
	}
	if _, err := m.Attach(context.Background(),
		Attachment{Key: "attempt:99"}, target("local")); err != nil {
		t.Fatalf("could not attach after a shutdown: %v", err)
	}
}

// Terminals no longer exit when their last viewer leaves, so they pile up.
// Past the limit, refusing to attach would make the board unusable until a
// restart, so the least recently used idle one is retired instead — the tmux
// session behind it is untouched, and re-attaching costs one click — and the
// caller is told which.
func TestAtTheLimitTheIdlestTerminalIsRetired(t *testing.T) {
	m, _ := fakeManager(t)
	m.Max = 3
	for i := 0; i < 3; i++ {
		if _, retired, err := m.AttachWithNotice(context.Background(),
			Attachment{Key: fmt.Sprintf("attempt:%d", i)}, target("local")); err != nil || retired != "" {
			t.Fatalf("attach %d: retired %q, %v", i, retired, err)
		}
	}
	// attempt:0 is the oldest, but someone looked at it more recently than 1
	m.Viewing("attempt:0")()
	_, retired, err := m.AttachWithNotice(context.Background(), Attachment{Key: "one-too-many"}, target("local"))
	if err != nil {
		t.Fatalf("a full manager refused a new terminal instead of making room: %v", err)
	}
	if retired != "attempt:1" {
		t.Fatalf("retired %q, want the least recently used idle terminal attempt:1", retired)
	}
	m.mu.Lock()
	held := len(m.procs)
	m.mu.Unlock()
	if held != 3 {
		t.Errorf("%d terminals running, want 3", held)
	}
}

// A terminal someone has open in a browser is never closed to make room; when
// every one is open, the new attach is refused with a reason instead.
func TestAViewedTerminalIsNeverRetired(t *testing.T) {
	m, _ := fakeManager(t)
	m.Max = 2
	var done []func()
	for i := 0; i < 2; i++ {
		key := fmt.Sprintf("session:%d", i)
		done = append(done, m.Viewing(key))
		if _, err := m.Attach(context.Background(), Attachment{Key: key}, target("local")); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := m.AttachWithNotice(context.Background(), Attachment{Key: "session:9"}, target("local")); err != ErrFull {
		t.Fatalf("got %v, want ErrFull while every terminal is being viewed", err)
	}
	for _, key := range []string{"session:0", "session:1"} {
		if _, ok := m.SocketFor(key); !ok {
			t.Fatalf("%s was closed while being viewed", key)
		}
	}
	// once a viewer leaves, its terminal can make room again
	done[1]()
	if _, retired, err := m.AttachWithNotice(context.Background(), Attachment{Key: "session:9"}, target("local")); err != nil || retired != "session:1" {
		t.Fatalf("retired %q, %v", retired, err)
	}
	if m.Viewers("session:0") != 1 || m.Viewers("session:1") != 0 {
		t.Fatal("viewer counts drifted")
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
			[]string{"tmux", "attach", "-t", "lec-7", ";", "set-option", "-w", "-t", "=lec-7:", "window-size", "latest"},
		},
		"pct container": {
			Attachment{TmuxSession: "lec-7"},
			&store.Target{Kind: "pct", Host: "104"},
			[]string{"sudo", "pct", "exec", "104", "--", "tmux", "attach", "-t", "lec-7", ";", "set-option", "-w", "-t", "=lec-7:", "window-size", "latest"},
		},
		"ephemeral sandbox beats the target kind": {
			Attachment{TmuxSession: "lec-7", SandboxVMID: "9001"},
			&store.Target{Kind: "sandbox", Host: "irrelevant"},
			[]string{"sudo", "pct", "exec", "9001", "--", "tmux", "attach", "-t", "lec-7", ";", "set-option", "-w", "-t", "=lec-7:", "window-size", "latest"},
		},
		"ssh with a key": {
			Attachment{TmuxSession: "lec-7"},
			&store.Target{Kind: "ssh", Host: "192.0.2.14", User: "claude", KeyPath: "/home/admin/.ssh/id_ed25519"},
			[]string{"ssh", "-tt", "-o", "StrictHostKeyChecking=accept-new", "-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=3",
				"-i", "/home/admin/.ssh/id_ed25519", "claude@192.0.2.14", "tmux", "attach", "-t", "lec-7", "';'", "set-option", "-w", "-t", "=lec-7:", "window-size", "latest"},
		},
		"ssh config alias with a jump host and agent forwarding": {
			Attachment{TmuxSession: "lec-7"},
			&store.Target{Kind: "ssh", Host: "10.0.0.5", User: "dev", Port: 22,
				SSHJSON: `{"alias":"build-box","proxy_jump":"bastion","forward_agent":true,"options":["GSSAPIAuthentication=yes"]}`},
			[]string{"ssh", "-tt", "-o", "StrictHostKeyChecking=accept-new", "-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=3",
				"-J", "bastion", "-A", "-o", "GSSAPIAuthentication=yes", "-l", "dev", "build-box", "tmux", "attach", "-t", "lec-7", "';'", "set-option", "-w", "-t", "=lec-7:", "window-size", "latest"},
		},
		"ssh defaults to root": {
			Attachment{TmuxSession: "lec-7"},
			&store.Target{Kind: "ssh", Host: "h"},
			[]string{"ssh", "-tt", "-o", "StrictHostKeyChecking=accept-new", "-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=3", "root@h", "tmux", "attach", "-t", "lec-7", "';'", "set-option", "-w", "-t", "=lec-7:", "window-size", "latest"},
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
			"tmux new-session -A -s lec-sh12 -c /srv/code -- /bin/sh -c exec \"${SHELL:-/bin/sh}\" -i ; set-option -w -t =lec-sh12: window-size latest"},
		"pct": {&store.Target{Kind: "pct", Host: "104"},
			"sudo pct exec 104 -- tmux new-session -A -s lec-sh12 -c /srv/code -- /bin/sh -c exec \"${SHELL:-/bin/sh}\" -i ; set-option -w -t =lec-sh12: window-size latest"},
		"ssh": {&store.Target{Kind: "ssh", Host: "192.0.2.14", User: "claude"},
			"ssh -tt -o StrictHostKeyChecking=accept-new -o ServerAliveInterval=15 -o ServerAliveCountMax=3 claude@192.0.2.14 " +
				"tmux new-session -A -s lec-sh12 -c /srv/code -- /bin/sh -c 'exec \"${SHELL:-/bin/sh}\" -i' ';' set-option -w -t =lec-sh12: window-size latest"},
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
	if strings.Join(got, " ") != "tmux attach -t lec-s3 ; set-option -w -t =lec-s3: window-size latest" {
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
	if len(got) != 12 || got[10] != "operator@example.test" {
		t.Fatalf("SSH command must be one argument: %v", got)
	}
	if !strings.HasPrefix(got[11], "wrapper ") {
		t.Fatalf("wrapper lost: %v", got)
	}
	// Execute a harmless wrapper that reports its received arguments. This proves
	// that spaces, apostrophes and command substitutions survive both shell layers.
	dir := t.TempDir()
	script := filepath.Join(dir, "wrapper")
	os.WriteFile(script, []byte("#!/bin/sh\nprintf '%s' \"$1\"\n"), 0700)
	cmd := exec.Command("sh", "-c", got[11])
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

// Attach returns as soon as the new ttyd accepts connections instead of
// sleeping a fixed 300ms, which was nearly all of an attach's latency.
func TestAttachReturnsOnceTheTerminalListens(t *testing.T) {
	m, _ := fakeManager(t)
	var listeners []net.Listener
	t.Cleanup(func() {
		for _, l := range listeners {
			l.Close()
		}
	})
	m.Spawn = func(socket, basePath string, argv []string) (*exec.Cmd, error) {
		l, err := net.Listen("unix", socket)
		if err != nil {
			return nil, err
		}
		listeners = append(listeners, l)
		cmd := exec.Command("sleep", "30")
		return cmd, cmd.Start()
	}
	start := time.Now()
	if _, err := m.Attach(context.Background(), Attachment{Key: "session:1", TmuxSession: "lec-s1"}, target("local")); err != nil {
		t.Fatal(err)
	}
	if took := time.Since(start); took >= BindWait {
		t.Fatalf("attach waited %v for a terminal that was already listening", took)
	}
}

// A ttyd that dies before it listens is reported, and its slot released,
// rather than handed to the browser as a dead terminal.
func TestAttachReportsATerminalThatExits(t *testing.T) {
	m, _ := fakeManager(t)
	m.Spawn = func(socket, basePath string, argv []string) (*exec.Cmd, error) {
		cmd := exec.Command("false")
		return cmd, cmd.Start()
	}
	_, err := m.Attach(context.Background(), Attachment{Key: "session:2", TmuxSession: "lec-s2"}, target("local"))
	if err == nil || !strings.Contains(err.Error(), "exited immediately") {
		t.Fatalf("got %v", err)
	}
	if _, ok := m.SocketFor("session:2"); ok {
		t.Fatal("a terminal that never started kept its slot")
	}
}

// The browser's attachment turns on tmux's extended keys and declares that
// the browser speaks them, by tmux version on the target: -T extkeys only
// exists from 3.2, and an older tmux rejects it and would never attach.
func TestWebAttachDeclaresExtendedKeysByTmuxVersion(t *testing.T) {
	native, _ := AttachArgv(Attachment{TmuxSession: "lec-7"}, &store.Target{Kind: "local"})
	if strings.Contains(strings.Join(native, " "), "extkeys") {
		t.Fatalf("a native attachment lets tmux detect the real terminal: %v", native)
	}
	argv, err := WebAttachArgv(Attachment{TmuxSession: "lec-7"}, &store.Target{Kind: "local"})
	if err != nil || len(argv) != 3 || argv[0] != "sh" || argv[1] != "-c" {
		t.Fatalf("web argv: %v %v", argv, err)
	}
	pct, _ := WebAttachArgv(Attachment{TmuxSession: "lec-7"}, &store.Target{Kind: "pct", Host: "104"})
	if strings.Join(pct[:6], " ") != "sudo pct exec 104 -- sh" || pct[7] != argv[2] {
		t.Fatalf("pct runs the same probe inside the container: %v", pct)
	}
	// Run the probe against stand-in tmux binaries that report a version and
	// print the arguments they were given.
	dir := t.TempDir()
	for version, want := range map[string]string{
		"tmux 3.1c":        "if-shell -F #{==:#{extended-keys},off} set-option -sq extended-keys on ; set-option -sq extended-keys-format csi-u ; attach -t lec-7 ; set-option -w -t =lec-7: window-size latest",
		"tmux 2.9a":        "if-shell -F #{==:#{extended-keys},off} set-option -sq extended-keys on ; set-option -sq extended-keys-format csi-u ; attach -t lec-7 ; set-option -w -t =lec-7: window-size latest",
		"tmux 3.2a":        "-T extkeys if-shell -F #{==:#{extended-keys},off} set-option -sq extended-keys on ; set-option -sq extended-keys-format csi-u ; attach -t lec-7 ; set-option -w -t =lec-7: window-size latest",
		"tmux 3.5a":        "-T extkeys if-shell",
		"tmux 3.10":        "-T extkeys if-shell",
		"tmux next-3.6":    "-T extkeys if-shell",
		"tmux openbsd-7.6": "-T extkeys if-shell",
	} {
		fake := filepath.Join(dir, "tmux")
		script := "#!/bin/sh\nif [ \"$1\" = -V ]; then echo '" + version + "'; exit 0; fi\necho \"$@\"\n"
		if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(argv[0], argv[1:]...)
		cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"))
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("%s: %v", version, err)
		}
		if got := strings.TrimSpace(string(out)); !strings.HasPrefix(got, want) {
			t.Errorf("%s:\n got %s\nwant %s", version, got, want)
		}
	}
}
