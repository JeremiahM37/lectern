package helpers

// ports is internal/api's portsScript: listening TCP ports on the target's
// loopback or wildcard addresses, which process owns each (and whether it
// runs in the session's workspace), and which answer HTTP; those come first.
// It reads Linux's /proc; elsewhere the list is empty, as before.
//
//	lectern helper ports [WORKDIR]

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

func init() {
	Register("ports", func(args []string, _ io.Reader, stdout, _ io.Writer) int {
		return portsMain("/proc", args, stdout)
	})
}

// readdirUnsorted is os.listdir: names in the directory's own order, which
// decides which process is reported as a socket's owner.
func readdirUnsorted(dir string) ([]string, error) {
	f, err := os.Open(dir)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return f.Readdirnames(-1)
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

type portEntry struct {
	obj  *pyObj
	port int64
}

func portsMain(proc string, args []string, stdout io.Writer) int {
	work := ""
	if len(args) > 0 && args[0] != "" {
		work = pyRealpath(args[0])
	}
	listen := newObj() // inode -> port, first seen wins
	for _, t := range []struct {
		file string
		v6   bool
	}{{proc + "/net/tcp", false}, {proc + "/net/tcp6", true}} {
		data, err := os.ReadFile(t.file)
		if err != nil {
			continue
		}
		lines := pySplitLines(wPyDecodeReplace(data), false)
		if len(lines) > 0 {
			lines = lines[1:]
		}
		for _, l := range lines {
			f := strings.Fields(l)
			if len(f) < 10 || f[3] != "0A" {
				continue
			}
			addr, portHex, _ := strings.Cut(f[1], ":")
			port, err := strconv.ParseInt(portHex, 16, 64)
			if err != nil {
				continue
			}
			var ok bool
			if t.v6 {
				ok = addr == "00000000000000000000000000000000" || addr == "00000000000000000000000001000000" || addr == "0000000000000000FFFF00000100007F"
			} else {
				ok = addr == "00000000" || strings.HasSuffix(addr, "7F")
			}
			ino, err := strconv.ParseInt(f[9], 10, 64)
			if ok && err == nil && !listen.Has(strconv.FormatInt(ino, 10)) {
				listen.Set(strconv.FormatInt(ino, 10), port)
			}
		}
	}
	owner := map[string]string{}
	pids, _ := readdirUnsorted(proc)
	for _, pid := range pids {
		if !isDigits(pid) {
			continue
		}
		fds, err := readdirUnsorted(proc + "/" + pid + "/fd")
		if err != nil {
			continue
		}
		for _, fd := range fds {
			link, err := os.Readlink(proc + "/" + pid + "/fd/" + fd)
			if err != nil || !strings.HasPrefix(link, "socket:[") {
				continue
			}
			ino, err := strconv.ParseInt(strings.TrimSuffix(link[8:], "]"), 10, 64)
			if err != nil {
				continue
			}
			key := strconv.FormatInt(ino, 10)
			if _, taken := owner[key]; listen.Has(key) && !taken {
				owner[key] = pid
			}
		}
	}
	out := newObj() // port -> entry
	for _, ino := range listen.Keys() {
		port := listen.Val(ino).(int64)
		pid := owner[ino]
		cwd, cmd := "", ""
		if pid != "" {
			if target, err := os.Readlink(proc + "/" + pid + "/cwd"); err == nil {
				cwd = target
			}
			if data, err := os.ReadFile(proc + "/" + pid + "/cmdline"); err == nil {
				cmd = wPyStrip(wPyDecodeReplace(bytes.ReplaceAll(data, []byte{0}, []byte{' '})))
				if r := []rune(cmd); len(r) > 160 {
					cmd = string(r[:160])
				}
			}
		}
		inws := work != "" && cwd != "" && (cwd == work || strings.HasPrefix(cwd, work+"/"))
		key := strconv.FormatInt(port, 10)
		if prev, ok := out.Get(key); ok && prev.(portEntry).obj.Val("in_workspace") == true {
			continue
		}
		var pidVal any
		if pid != "" {
			n, _ := strconv.ParseInt(pid, 10, 64)
			pidVal = n
		}
		out.Set(key, portEntry{newObj("port", port, "pid", pidVal, "command", cmd, "in_workspace", inws), port})
	}
	var ports []portEntry
	for _, k := range out.Keys() {
		ports = append(ports, out.Val(k).(portEntry))
	}
	if len(ports) > 300 {
		ports = ports[:300]
	}
	probed := make([]bool, len(ports))
	var wg sync.WaitGroup
	slots := make(chan struct{}, 32)
	for i, e := range ports {
		wg.Add(1)
		slots <- struct{}{}
		go func() {
			defer func() { <-slots; wg.Done() }()
			probed[i] = probeHTTP(e.port)
		}()
	}
	wg.Wait()
	for i, e := range ports {
		e.obj.Set("http", probed[i])
	}
	rank := func(e portEntry, i int) [3]int64 {
		r := [3]int64{0, 0, e.port}
		if e.obj.Val("in_workspace") != true {
			r[0] = 1
		}
		if !probed[i] {
			r[1] = 1
		}
		return r
	}
	order := make([]int, len(ports))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool {
		ra, rb := rank(ports[order[a]], order[a]), rank(ports[order[b]], order[b])
		for k := range ra {
			if ra[k] != rb[k] {
				return ra[k] < rb[k]
			}
		}
		return false
	})
	list := []any{}
	for _, i := range order {
		if len(list) == 80 {
			break
		}
		list = append(list, ports[i].obj)
	}
	fmt.Fprintln(stdout, pyDumps(newObj("ports", list)))
	return 0
}

// probeHTTP reports whether the port answers a HEAD request with HTTP.
func probeHTTP(port int64) bool {
	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.FormatInt(port, 10)), 300*time.Millisecond)
	if err != nil {
		return false
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(600 * time.Millisecond))
	if _, err := conn.Write([]byte("HEAD / HTTP/1.0\r\nHost: localhost\r\n\r\n")); err != nil {
		return false
	}
	buf := make([]byte, 5)
	n, err := conn.Read(buf)
	return err == nil && string(buf[:n]) == "HTTP/"
}
