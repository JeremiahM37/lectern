package webterm

import (
	"context"
	"encoding/json"
	"flag"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/JeremiahM37/lectern/v2/internal/ptyhost"
	"github.com/JeremiahM37/lectern/v2/internal/testutil"
)

// The test binary stands in for `lectern ptyhost serve` when a PTY-host
// attachment starts a host.
func TestMain(m *testing.M) {
	if len(os.Args) > 2 && os.Args[1] == "ptyhost" && os.Args[2] == "serve" {
		fs := flag.NewFlagSet("serve", flag.ExitOnError)
		socket := fs.String("socket", "", "")
		_ = fs.Parse(os.Args[3:])
		if err := ptyhost.Serve(ptyhost.Options{Socket: *socket, Idle: 3 * time.Second}); err != nil && err != ptyhost.ErrRunning {
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func requireRealProcesses(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "linux" {
		testutil.RequireIsolated(t)
		return
	}
	if os.Getenv("CI") != "true" && os.Getenv("LECTERN_PTYHOST_TESTS") != "1" {
		t.Skip("set LECTERN_PTYHOST_TESTS=1 to start real programs")
	}
}

func shell() (argv []string, line, want string) {
	if runtime.GOOS == "windows" {
		return []string{"cmd.exe"}, "set /a 6*7+1000", "1042"
	}
	return []string{"/bin/sh"}, "echo web-$((6*7+1000))", "web-1042"
}

// dial opens the terminal the way the browser does: token, then the tty
// WebSocket with its size.
func dial(t *testing.T, base string) (*websocket.Conn, context.Context) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	ws, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(base, "http")+"/term/x/1/ws", &websocket.DialOptions{Subprotocols: []string{"tty"}})
	if err != nil {
		t.Fatal(err)
	}
	hello, _ := json.Marshal(map[string]any{"AuthToken": "", "columns": 100, "rows": 30})
	if err := ws.Write(ctx, websocket.MessageBinary, hello); err != nil {
		t.Fatal(err)
	}
	return ws, ctx
}

func readUntil(t *testing.T, ctx context.Context, ws *websocket.Conn, want string) {
	t.Helper()
	var seen strings.Builder
	for !strings.Contains(seen.String(), want) {
		_, msg, err := ws.Read(ctx)
		if err != nil {
			t.Fatalf("terminal ended before %q: %v\n%s", want, err, seen.String())
		}
		if len(msg) > 0 && msg[0] == '0' {
			seen.Write(msg[1:])
		}
	}
}

func TestAProgramOnItsOwnTerminal(t *testing.T) {
	requireRealProcesses(t)
	argv, line, want := shell()
	srv := httptest.NewServer(Handler(Options{Base: "/term/x/1", Argv: argv}))
	defer srv.Close()
	ws, ctx := dial(t, srv.URL)
	defer ws.CloseNow()
	if err := ws.Write(ctx, websocket.MessageBinary, []byte("0"+line+"\r")); err != nil {
		t.Fatal(err)
	}
	readUntil(t, ctx, ws, want)
	// A resize reaches the program's terminal.
	if runtime.GOOS != "windows" {
		_ = ws.Write(ctx, websocket.MessageBinary, []byte(`1{"columns":77,"rows":21}`))
		_ = ws.Write(ctx, websocket.MessageBinary, []byte("0stty size\r"))
		readUntil(t, ctx, ws, "21 77")
	}
}

func TestAPtyHostSessionStreamsStraightFromTheHost(t *testing.T) {
	requireRealProcesses(t)
	dir, err := os.MkdirTemp("", "lwt")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	t.Setenv(ptyhost.SocketEnv, filepath.Join(dir, "s.sock"))
	self, _ := os.Executable()
	argv, line, want := shell()
	// The shell form an attachment to a project shell uses: create the
	// session if it is not there, then attach.
	cmd := append([]string{self, "pty", "new-session", "-A", "-s", "webshell", "-c", dir, "--"}, argv...)
	srv := httptest.NewServer(Handler(Options{Base: "/term/x/1", Argv: cmd, Self: self}))
	defer srv.Close()
	ws, ctx := dial(t, srv.URL)
	if err := ws.Write(ctx, websocket.MessageBinary, []byte("0"+line+"\r")); err != nil {
		t.Fatal(err)
	}
	readUntil(t, ctx, ws, want)
	ws.CloseNow()
	// Closing the browser detaches; the session and its screen remain, and
	// the next connection is drawn from the host's snapshot.
	ws2, ctx2 := dial(t, srv.URL)
	defer ws2.CloseNow()
	readUntil(t, ctx2, ws2, want)
	c, err := ptyhost.Dial(filepath.Join(dir, "s.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if res, err := c.Do(ptyhost.Request{Op: "info", Name: "webshell"}); err != nil || !res.OK || res.Info.Cols != 100 {
		t.Fatalf("the session is not there at the browser's size: %+v %v", res, err)
	}
	_, _ = c.Do(ptyhost.Request{Op: "kill", Name: "webshell"})
}
