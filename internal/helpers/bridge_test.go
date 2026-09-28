package helpers

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/executor"
)

func TestBridgeHandshakeParity(t *testing.T) {
	script := pythonConst(t, "executor/bridge.go", "bridgeScript")
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	closed := fmt.Sprint(l.Addr().(*net.TCPAddr).Port)
	l.Close()
	for _, c := range []struct {
		stdin string
		args  []string
	}{
		{"L", []string{closed}}, // nothing listens: E<reason>
		{"X", []string{closed}}, // the caller's start byte is wrong
		{"", []string{closed}},  // stdin closed at once
		{"L", []string{"nope"}},
		{"L", nil},
	} {
		assertParity(t, runSpec{stdin: c.stdin}, script, "bridge", c.args...)
	}
}

// relayConn starts argv as the relay and returns a connection over its stdio.
func relayConn(t *testing.T, argv []string, env []string) net.Conn {
	t.Helper()
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = append(os.Environ(), env...)
	in, _ := cmd.StdinPipe()
	out, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	in.Write([]byte("L"))
	r := bufio.NewReader(out)
	if b, err := r.ReadByte(); err != nil || b != 'K' {
		t.Fatalf("handshake: %q %v", b, err)
	}
	a, b := net.Pipe()
	go func() { io.Copy(in, b); in.Close() }()
	go func() { io.Copy(b, r); b.Close() }()
	return a
}

// Both directions stream without waiting for a buffer to fill, and the relay
// ends when the port closes.
func TestBridgeRelaysBothWays(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				r := bufio.NewReader(c)
				for {
					line, err := r.ReadString('\n')
					if err != nil {
						c.Close()
						return
					}
					if line == "bye\n" {
						c.Close()
						return
					}
					c.Write([]byte("echo " + line))
				}
			}()
		}
	}()
	port := fmt.Sprint(ln.Addr().(*net.TCPAddr).Port)
	conn := relayConn(t, []string{os.Args[0], "helper", "bridge", port}, []string{helperExecEnv + "=1"})
	r := bufio.NewReader(conn)
	for _, msg := range []string{"one\n", "two\n"} {
		conn.SetDeadline(time.Now().Add(5 * time.Second))
		conn.Write([]byte(msg))
		if got, err := r.ReadString('\n'); err != nil || got != "echo "+msg {
			t.Fatalf("got %q %v", got, err)
		}
	}
	conn.Write([]byte("bye\n"))
	if _, err := r.ReadByte(); err != io.EOF {
		t.Fatalf("relay did not end with the port: %v", err)
	}
}

// A pct target whose container has a lectern binary dials through the Go
// relay: `sudo pct exec` is stood in for by a sudo on PATH.
func TestPctDialsThroughTheGoRelay(t *testing.T) {
	bin := t.TempDir()
	fake := "#!/bin/sh\n[ \"$1 $2 $3 $4\" = \"pct exec 101 --\" ] || { echo \"unexpected: $*\" >&2; exit 9; }\nshift 4\nexec \"$@\"\n"
	os.WriteFile(filepath.Join(bin, "sudo"), []byte(fake), 0o755)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv(helperExecEnv, "1")
	// No python3 on this PATH's first entry matters: the relay must not use it.
	os.WriteFile(filepath.Join(bin, "python3"), []byte("#!/bin/sh\nexit 97\n"), 0o755)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(strings.Repeat("a", 3<<20-1) + "z"))
	}))
	defer srv.Close()
	p := executor.NewPct("101")
	executor.SetTargetEnv(p, executor.TargetEnv{Lectern: os.Args[0]})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, err := p.DialTarget(ctx, srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	r := bufio.NewReader(conn)
	for i := 0; i < 2; i++ {
		fmt.Fprintf(conn, "GET / HTTP/1.1\r\nHost: localhost\r\n\r\n")
		resp, err := http.ReadResponse(r, nil)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if len(body) != 3<<20 || body[len(body)-1] != 'z' {
			t.Fatalf("body of %d bytes", len(body))
		}
	}
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	closed := l.Addr().String()
	l.Close()
	if _, err := p.DialTarget(ctx, closed); err == nil || !strings.Contains(err.Error(), "could not connect on the target: [Errno 111] Connection refused") {
		t.Fatalf("closed port: %v", err)
	}
}
