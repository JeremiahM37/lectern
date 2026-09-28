//go:build unix

package helpers

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// fakeDevTools accepts one websocket client at a time, records every message
// it receives and answers Storage.setCookies, refusing any batch holding a
// cookie named "refuse".
type fakeDevTools struct {
	ln     net.Listener
	refuse bool // answer the upgrade with 403
	mu     sync.Mutex
	got    []string
}

func newFakeDevTools(t *testing.T, refuse bool) *fakeDevTools {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeDevTools{ln: ln, refuse: refuse}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go f.serve(c)
		}
	}()
	return f
}

func (f *fakeDevTools) port() int { return f.ln.Addr().(*net.TCPAddr).Port }

func (f *fakeDevTools) serve(c net.Conn) {
	defer c.Close()
	r := bufio.NewReader(c)
	var request string
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		request += line
		if line == "\r\n" {
			break
		}
	}
	first, _, _ := strings.Cut(request, "\r\n")
	f.record("REQUEST " + first)
	if f.refuse {
		io.WriteString(c, "HTTP/1.1 403 Forbidden\r\n\r\n")
		return
	}
	io.WriteString(c, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n")
	for {
		var h [2]byte
		if _, err := io.ReadFull(r, h[:]); err != nil {
			return
		}
		n := uint64(h[1] & 0x7f)
		switch n {
		case 126:
			var ext [2]byte
			io.ReadFull(r, ext[:])
			n = uint64(binary.BigEndian.Uint16(ext[:]))
		case 127:
			var ext [8]byte
			io.ReadFull(r, ext[:])
			n = binary.BigEndian.Uint64(ext[:])
		}
		var mask [4]byte
		io.ReadFull(r, mask[:])
		data := make([]byte, n)
		io.ReadFull(r, data)
		for i := range data {
			data[i] ^= mask[i%4]
		}
		f.record(string(data))
		var msg struct {
			ID     int `json:"id"`
			Params struct {
				Cookies []struct {
					Name any `json:"name"`
				} `json:"cookies"`
			} `json:"params"`
		}
		json.Unmarshal(data, &msg)
		reply := fmt.Sprintf(`{"id":%d,"result":{}}`, msg.ID)
		for _, ck := range msg.Params.Cookies {
			if ck.Name == "refuse" {
				reply = fmt.Sprintf(`{"id":%d,"error":{"code":-32602,"message":"Invalid cookie fields"}}`, msg.ID)
			}
		}
		// An unrelated event first: the client must wait for its own id.
		for _, out := range []string{`{"method":"Target.attachedToTarget","params":{}}`, reply} {
			frame := []byte{0x81}
			if len(out) < 126 {
				frame = append(frame, byte(len(out)))
			} else {
				frame = append(frame, 126)
				frame = binary.BigEndian.AppendUint16(frame, uint16(len(out)))
			}
			c.Write(append(frame, out...))
		}
	}
}

func (f *fakeDevTools) record(s string) {
	f.mu.Lock()
	f.got = append(f.got, s)
	f.mu.Unlock()
}

func (f *fakeDevTools) take() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := strings.Join(f.got, "\n")
	f.got = nil
	return out
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	return lines[len(lines)-1]
}

func TestBrowserCookiesFileParity(t *testing.T) {
	script := pythonFile(t, "browser/cookies.py")
	var many strings.Builder
	for i := 0; i < 450; i++ {
		fmt.Fprintf(&many, "a.example\tTRUE\t/\tFALSE\t0\tc%d\tv%d\n", i, i)
	}
	fmt.Fprintf(&many, "a.example\tTRUE\t/\tFALSE\t0\trefuse\tx\n")
	netscape := "# Netscape HTTP Cookie File\n" +
		"a.example\tTRUE\t/\tFALSE\t1900000000\tone\t1\n" +
		"#HttpOnly_.a.example\tTRUE\t/p\tTRUE\t0\ttwo\t2\n" +
		"sub.a.example\tFALSE\t\tfalse\tsoon\tthree\t3\r\n" +
		"b.example\tTRUE\t/\tFALSE\t5\tfour\t4\n" +
		"broken line\n\n   \n#comment\n"
	jsonCookies := `[{"name":"n1","value":"v","domain":".a.example","sameSite":"no_restriction","expirationDate":1900000000.25,"secure":1},
		{"name":"n2","value":7,"host":"a.example","same_site":"LAX","expires":-3,"httpOnly":true,"path":""},
		{"name":"n3","value":[1,"x"],"domain":"a.example","sameSite":true,"expires":null},
		{"name":"refuse","value":"","domain":"a.example"},
		{"value":"no name","domain":"a.example"}, "string", {"name":"x","value":"y"},
		{"name":"big","value":"b","domain":"a.example","expires":123456789012345678901234567890}]`
	cases := []struct {
		name, file, domains string
		refuse, closed      bool
		noFile              bool
	}{
		{name: "netscape", file: netscape, domains: " .A.example ,,c.example"},
		{name: "netscape unfiltered", file: netscape},
		{name: "json list", file: jsonCookies},
		{name: "json object", file: `{"cookies": ` + jsonCookies + `}`, domains: "a.example"},
		{name: "json without cookies", file: `{"other": 1}`},
		{name: "json cookies null", file: `{"cookies": null}`},
		{name: "invalid json", file: `[{"name": }`},
		{name: "batches", file: many.String()},
		{name: "missing file", noFile: true},
		{name: "refused upgrade", file: netscape, refuse: true},
		{name: "nothing listening", file: netscape, closed: true},
		{name: "bytes", file: "a.example\tTRUE\t/\tFALSE\t0\tn\t\xff\xfe\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dev := newFakeDevTools(t, c.refuse)
			port := dev.port()
			if c.closed {
				dev.ln.Close()
			}
			run := func(runner func(spec runSpec, args ...string) runResult) (runResult, string, bool) {
				dir := t.TempDir()
				path := filepath.Join(dir, "import.cookies")
				if !c.noFile {
					os.WriteFile(path, []byte(c.file), 0o600)
				}
				args := []string{fmt.Sprint(port), "/devtools/browser/abc", "file", path}
				if c.domains != "" {
					args = append(args, c.domains)
				}
				res := runner(runSpec{dir: dir}, args...)
				res.stdout, res.stderr = strings.ReplaceAll(res.stdout, dir, "$DIR"), strings.ReplaceAll(res.stderr, dir, "$DIR")
				_, err := os.Stat(path)
				return res, strings.ReplaceAll(dev.take(), dir, "$DIR"), os.IsNotExist(err)
			}
			py, pySent, pyGone := run(func(spec runSpec, args ...string) runResult { return runPy(t, spec, script, args...) })
			goRes, goSent, goGone := run(func(spec runSpec, args ...string) runResult { return runGo(t, spec, "browser-cookies", args...) })
			sameResult(t, py, goRes)
			if a, b := lastLine(py.stdout+py.stderr), lastLine(goRes.stdout+goRes.stderr); a != b {
				t.Fatalf("last line differs\npython: %s\ngo:     %s", a, b)
			}
			if pySent != goSent {
				t.Fatalf("the browser was sent different messages\npython:\n%s\ngo:\n%s", pySent, goSent)
			}
			if !pyGone || !goGone {
				t.Fatal("the cookies file was left behind")
			}
			t.Log(lastLine(goRes.stdout + goRes.stderr))
		})
	}
}
