package executor

import (
	"bufio"
	"context"
	"errors"
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

	"github.com/JeremiahM37/lectern/v2/internal/testutil"
)

func needPython(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("needs python3 for the relay")
	}
}

// roundTrip speaks HTTP over a bridged connection, twice on the same
// connection, and a large body, so framing and both directions are exercised.
func roundTrip(t *testing.T, conn net.Conn) {
	t.Helper()
	defer conn.Close()
	r := bufio.NewReader(conn)
	for i := 0; i < 2; i++ {
		fmt.Fprintf(conn, "GET /big HTTP/1.1\r\nHost: localhost\r\n\r\n")
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
}

func bigServer(t *testing.T) string {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(strings.Repeat("a", 3<<20-1) + "z"))
	}))
	t.Cleanup(srv.Close)
	return srv.Listener.Addr().String()
}

// A pct target: `sudo pct exec VMID -- bash -c ...` stood in for by a sudo on
// PATH that drops the pct part and runs the command here.
func TestPctDialsTheContainersLoopbackThroughARelay(t *testing.T) {
	needPython(t)
	bin := t.TempDir()
	fake := "#!/bin/sh\n[ \"$1 $2 $3 $4\" = \"pct exec 101 --\" ] || { echo \"unexpected: $*\" >&2; exit 9; }\nshift 4\nexec \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "sudo"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	addr := bigServer(t)
	p := NewPct("101")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, err := p.DialTarget(ctx, addr)
	if err != nil {
		t.Fatal(err)
	}
	roundTrip(t, conn)
	// Nothing listens here: the relay says so instead of hanging.
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	closed := l.Addr().String()
	l.Close()
	if _, err := p.DialTarget(ctx, closed); err == nil || !strings.Contains(err.Error(), "could not connect on the target") {
		t.Fatalf("closed port: %v", err)
	}
	if _, err := p.DialTarget(ctx, "10.0.0.1:80"); err == nil {
		t.Fatal("a bridged dial reached beyond the loopback")
	}
}

func TestWrappedSSHDialsThroughTheWrapper(t *testing.T) {
	needPython(t)
	fixture := testutil.NewSSHFixture(t)
	addr := bigServer(t)
	user := os.Getenv("USER")
	if user == "" {
		user = "nobody"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	wrapped := NewSSH("127.0.0.1", user, fixture.Port, fixture.KeyPath, "bash -c {cmd}")
	defer wrapped.Close()
	conn, err := wrapped.DialTarget(ctx, addr)
	if err != nil {
		t.Fatal(err)
	}
	roundTrip(t, conn)
	// A wrapper that swallows input cannot carry a connection, and says why.
	deaf := NewSSH("127.0.0.1", user, fixture.Port, fixture.KeyPath, "bash -c {cmd} </dev/null")
	defer deaf.Close()
	start := time.Now()
	if _, err := deaf.DialTarget(ctx, addr); !errors.Is(err, ErrNoStdin) && (err == nil || !strings.Contains(err.Error(), "relay on the target ended")) {
		t.Fatalf("a wrapper without stdin: %v", err)
	}
	if time.Since(start) > 25*time.Second {
		t.Fatal("the refusal took too long")
	}
}
