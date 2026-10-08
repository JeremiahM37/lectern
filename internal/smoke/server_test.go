// Package smoke runs the real lectern binary as a server, the way a user does,
// and drives a PTY-host session through its API and its web terminal. It is
// the check the Windows and macOS CI jobs exist for (docs/ptyhost.md §7).
package smoke

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/JeremiahM37/lectern/v2/internal/testutil"
)

// requireSmoke gates the test: it builds and runs the real binary, starts a
// PTY host and a shell, so it runs only where that is safe — the isolated
// runner on Linux, disposable CI machines elsewhere.
func requireSmoke(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "linux" {
		testutil.RequireIsolated(t)
		return
	}
	if os.Getenv("LECTERN_SERVER_SMOKE") != "1" {
		t.Skip("set LECTERN_SERVER_SMOKE=1 to build and run a real server")
	}
}

type server struct {
	t    *testing.T
	bin  string
	env  []string
	url  string
	proc *exec.Cmd
	log  *bytes.Buffer
}

func freePort(t *testing.T) int {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func (s *server) start() {
	s.t.Helper()
	s.log = &bytes.Buffer{}
	s.proc = exec.Command(s.bin, "serve")
	s.proc.Env = s.env
	s.proc.Stdout, s.proc.Stderr = s.log, s.log
	if err := s.proc.Start(); err != nil {
		s.t.Fatal(err)
	}
	deadline := time.Now().Add(60 * time.Second)
	for {
		if res, err := http.Get(s.url + "/api/health"); err == nil {
			res.Body.Close()
			if res.StatusCode == 200 {
				return
			}
		}
		if time.Now().After(deadline) {
			s.t.Fatalf("server did not start:\n%s", s.log)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// stop ends the server the hard way, as a crash or an upgrade's restart does.
func (s *server) stop() {
	if s.proc != nil && s.proc.Process != nil {
		_ = s.proc.Process.Kill()
		_, _ = s.proc.Process.Wait()
		s.proc = nil
	}
}

func (s *server) do(method, path string, body any, out any) int {
	s.t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, s.url+path, r)
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		s.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			s.t.Fatalf("%s %s: %d %s", method, path, res.StatusCode, raw)
		}
	}
	if res.StatusCode >= 300 {
		s.t.Logf("%s %s: %d %s", method, path, res.StatusCode, raw)
	}
	return res.StatusCode
}

// history reads the session's terminal from the server (capture-pane).
func (s *server) history(id int64) string {
	var out struct {
		Text string `json:"text"`
	}
	s.do("GET", fmt.Sprintf("/api/term/session/%d/history", id), nil, &out)
	return out.Text
}

func (s *server) waitHistory(id int64, want string) {
	s.t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		text := s.history(id)
		if strings.Contains(text, want) {
			return
		}
		if time.Now().After(deadline) {
			s.t.Fatalf("session %d never showed %q:\n%s\nserver log:\n%s", id, want, text, s.log)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func build(t *testing.T, dir string) string {
	t.Helper()
	root, err := exec.Command("go", "env", "GOMOD").Output()
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "lectern")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", bin, "./cmd/lectern")
	cmd.Dir = filepath.Dir(strings.TrimSpace(string(root)))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	return bin
}

// TestAServerKeepsAPtySessionAcrossARestart is the whole pty-backend story on
// one machine: start a server, open a shell, type into it through the API
// and through the web terminal, kill the server, start it again, and find
// the same shell with its screen intact.
func TestAServerKeepsAPtySessionAcrossARestart(t *testing.T) {
	requireSmoke(t)
	dir := t.TempDir()
	short, err := os.MkdirTemp("", "lsm")
	if err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(short, "s.sock")
	// The binary lives outside t.TempDir: on Windows a running executable
	// cannot be deleted, and the PTY host started from it takes a moment to
	// exit after being stopped.
	bin := build(t, short)
	t.Cleanup(func() {
		_ = exec.Command(bin, "ptyhost", "stop", "--force", "--socket", socket).Run()
		for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
			if os.RemoveAll(short) == nil {
				return
			}
		}
		t.Logf("could not remove %s; a process started from it may still be running", short)
	})
	port := freePort(t)
	env := append(os.Environ(),
		"LECTERN_PORT="+fmt.Sprint(port), "LECTERN_HOST=127.0.0.1", "LECTERN_AUTH=none",
		"LECTERN_DB="+filepath.Join(dir, "lectern.db"), "LECTERN_SESSION_BACKEND=pty",
		"LECTERN_PTYHOST_SOCKET="+socket, "LECTERN_SCRATCH_ROOT="+filepath.Join(dir, "scratch"),
		"LECTERN_BASE_URL=http://127.0.0.1:"+fmt.Sprint(port))
	s := &server{t: t, bin: bin, env: env, url: fmt.Sprintf("http://127.0.0.1:%d", port)}
	s.start()
	defer s.stop()

	var target struct{ ID int64 }
	kind := "local"
	if code := s.do("POST", "/api/targets", map[string]any{"name": "here", "kind": kind, "workroot": filepath.Join(dir, "work")}, &target); code >= 300 {
		t.Fatalf("create target: %d", code)
	}
	var probed map[string]any
	s.do("POST", fmt.Sprintf("/api/targets/%d/check", target.ID), nil, &probed)
	var info map[string]any
	_ = json.Unmarshal([]byte(fmt.Sprint(probed["info_json"])), &info)
	if info["session_backend"] != "pty" {
		t.Fatalf("the target does not use the PTY host: %v", probed)
	}
	var shell struct {
		ID          int64  `json:"id"`
		TmuxSession string `json:"tmux_session"`
	}
	if code := s.do("POST", "/api/shells", map[string]any{"target_id": target.ID}, &shell); code >= 300 {
		t.Fatalf("open a shell: %d\n%s", code, s.log)
	}
	s.do("POST", fmt.Sprintf("/api/sessions/%d/send", shell.ID), map[string]any{"text": "echo api-$((6*7+1000))"}, nil)
	s.waitHistory(shell.ID, "api-1042")

	// The web terminal, through the server's own proxy and term-server.
	webTerminal(t, s.url, shell.ID, "echo web-$((6*7+2000))", "web-2042")

	// A server that dies takes nothing with it.
	s.stop()
	s.start()
	s.waitHistory(shell.ID, "web-2042")
	s.do("POST", fmt.Sprintf("/api/sessions/%d/send", shell.ID), map[string]any{"text": "echo again-$((6*7+3000))"}, nil)
	s.waitHistory(shell.ID, "again-3042")
	out, _ := exec.Command(bin, "ptyhost", "status", "--socket", socket).CombinedOutput()
	if !strings.Contains(string(out), shell.TmuxSession) {
		t.Fatalf("the PTY host does not list %s:\n%s", shell.TmuxSession, out)
	}
}

func webTerminal(t *testing.T, base string, id int64, line, want string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	url := "ws" + strings.TrimPrefix(base, "http") + fmt.Sprintf("/term/session/%d/ws", id)
	ws, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{Subprotocols: []string{"tty"}})
	if err != nil {
		t.Fatalf("web terminal: %v", err)
	}
	defer ws.CloseNow()
	hello, _ := json.Marshal(map[string]any{"AuthToken": "", "columns": 120, "rows": 30})
	_ = ws.Write(ctx, websocket.MessageBinary, hello)
	// One reader owns the socket (cancelling a Read would close it).
	type frame struct {
		msg []byte
		err error
	}
	frames := make(chan frame, 64)
	go func() {
		for {
			_, msg, err := ws.Read(ctx)
			frames <- frame{msg, err}
			if err != nil {
				return
			}
		}
	}()
	var seen strings.Builder
	take := func(f frame) {
		if len(f.msg) > 0 && f.msg[0] == '0' {
			seen.Write(f.msg[1:])
		}
	}
	// Attaching resizes the session to the browser's size. On Windows ConPTY
	// repaints after a resize and drops keystrokes that arrive meanwhile (the
	// CI failure typed "cho", the leading "e" lost). A browser only types once
	// it has painted, so wait for the attach output to go quiet first.
	for quiet := false; !quiet; {
		select {
		case f := <-frames:
			if f.err != nil {
				t.Fatalf("web terminal ended before painting: %v\n%s", f.err, seen.String())
			}
			take(f)
		case <-time.After(1500 * time.Millisecond):
			quiet = seen.Len() > 0
		}
	}
	_ = ws.Write(ctx, websocket.MessageBinary, []byte("0"+line+"\r"))
	for !strings.Contains(seen.String(), want) {
		f := <-frames
		if f.err != nil {
			t.Fatalf("web terminal ended before %q: %v\n%s", want, f.err, seen.String())
		}
		take(f)
	}
}
