//go:build unix

package helpers

import (
	"bytes"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The port list is read from a fabricated /proc, so the test sees exactly the
// sockets it made and probes only its own listeners.
func TestPortsParity(t *testing.T) {
	script := pythonConst(t, "api/browser.go", "portsScript")
	root := t.TempDir()
	proc := filepath.Join(root, "proc")
	script = strings.ReplaceAll(script, "'/proc", "'"+proc)

	httpLn, _ := net.Listen("tcp", "127.0.0.1:0")
	go http.Serve(httpLn, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer httpLn.Close()
	rawLn, _ := net.Listen("tcp", "127.0.0.1:0")
	defer rawLn.Close()
	go func() {
		for {
			c, err := rawLn.Accept()
			if err != nil {
				return
			}
			c.Write([]byte("SSH-2.0-x\r\n"))
			c.Close()
		}
	}()
	closed, _ := net.Listen("tcp", "127.0.0.1:0")
	closedPort := closed.Addr().(*net.TCPAddr).Port
	closed.Close()
	port := func(ln net.Listener) int { return ln.Addr().(*net.TCPAddr).Port }

	work := filepath.Join(root, "work")
	os.MkdirAll(work+"/sub", 0o755)
	row := func(addr string, p int, state string, ino int) string {
		return fmt.Sprintf("   0: %s:%04X 00000000:0000 %s 00000000:00000000 00:00000000 00000000  1000        0 %d 1 0000000000000000 100 0 0 10 0\n", addr, p, state, ino)
	}
	os.MkdirAll(proc+"/net", 0o755)
	header := "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n"
	os.WriteFile(proc+"/net/tcp", []byte(header+
		row("0100007F", port(httpLn), "0A", 101)+
		row("00000000", port(rawLn), "0A", 102)+
		row("0100007F", closedPort, "0A", 103)+
		row("0100A8C0", 9999, "0A", 104)+ // a LAN address: not listed
		row("0100007F", 8888, "01", 105)+ // established: not listed
		row("0100007F", port(httpLn), "0A", 106)), 0o644)
	os.WriteFile(proc+"/net/tcp6", []byte(header+
		fmt.Sprintf("   0: 00000000000000000000000001000000:%04X 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000  1000 0 107 1\n", port(rawLn))), 0o644)
	process := func(pid, cwd, cmdline string, inodes ...int) {
		dir := filepath.Join(proc, pid)
		os.MkdirAll(dir+"/fd", 0o755)
		os.Symlink(cwd, dir+"/cwd")
		os.WriteFile(dir+"/cmdline", []byte(cmdline), 0o644)
		for i, ino := range inodes {
			os.Symlink(fmt.Sprintf("socket:[%d]", ino), fmt.Sprintf("%s/fd/%d", dir, i+3))
		}
		os.Symlink("/dev/null", dir+"/fd/0")
	}
	process("200", work+"/sub", "node\x00server.js\x00--port\x00"+strings.Repeat("x", 200), 101)
	process("300", "/elsewhere", "sshd\x00-D\x00", 102, 107)
	process("400", work, "python3\x00-m\x00http.server\x00", 106)
	os.MkdirAll(proc+"/self", 0o755)

	for _, args := range [][]string{{work}, {""}, {}, {work + "/sub"}} {
		py := runPy(t, runSpec{dir: root}, script, args...)
		var out bytes.Buffer
		rc := portsMain(proc, args, &out)
		sameResult(t, py, runResult{stdout: out.String(), rc: rc})
		if !strings.Contains(out.String(), `"http": true`) {
			t.Fatalf("no HTTP port found: %s", out.String())
		}
	}
}
