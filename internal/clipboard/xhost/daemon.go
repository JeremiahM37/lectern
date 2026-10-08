// Package xhost gives a machine a real clipboard that headless sessions can
// reach: a private Xvfb plus a selection owner Lectern fills from the device
// the person is using. Any program that reads the X clipboard natively (Codex
// via arboard, xclip, a toolkit) then finds the person's screenshot there.
package xhost

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DefaultTTL is how long mirrored content stays before it is cleared.
const DefaultTTL = 10 * time.Minute

// MaxItem bounds one mirrored item.
const MaxItem = 20 << 20

// Dir is the default state directory: private to the user.
func Dir() string {
	if x := os.Getenv("XDG_RUNTIME_DIR"); x != "" {
		if st, err := os.Stat(x); err == nil && st.IsDir() {
			return filepath.Join(x, "lectern-clipboard")
		}
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".lectern", "clipboard")
}

func prepare(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return os.Chmod(dir, 0o700)
}

// Env is what a session needs to reach the clipboard.
type Env struct{ Display, Xauthority string }

func readEnv(dir string) (Env, bool) {
	b, err := os.ReadFile(filepath.Join(dir, "env"))
	if err != nil {
		return Env{}, false
	}
	var e Env
	for _, l := range strings.Split(string(b), "\n") {
		k, v, ok := strings.Cut(l, "=")
		if !ok {
			continue
		}
		switch k {
		case "DISPLAY":
			e.Display = v
		case "XAUTHORITY":
			e.Xauthority = v
		}
	}
	return e, e.Display != ""
}

// Ping reports the daemon's environment if it answers.
func Ping(dir string) (Env, bool) {
	c, err := net.DialTimeout("unix", filepath.Join(dir, "ctl.sock"), time.Second)
	if err != nil {
		return Env{}, false
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(2 * time.Second))
	fmt.Fprint(c, "PING\n")
	line, err := bufio.NewReader(c).ReadString('\n')
	if err != nil || !strings.HasPrefix(line, "PONG") {
		return Env{}, false
	}
	return readEnv(dir)
}

// Ensure makes sure the daemon is running and returns the environment for it.
// self is the lectern binary that can run `clipboard daemon`.
func Ensure(dir, self string) (Env, error) {
	if dir == "" {
		dir = Dir()
	}
	if err := prepare(dir); err != nil {
		return Env{}, err
	}
	if e, ok := Ping(dir); ok {
		return e, nil
	}
	if _, err := exec.LookPath("Xvfb"); err != nil {
		return Env{}, errors.New("Xvfb is not installed")
	}
	logf, err := os.OpenFile(filepath.Join(dir, "daemon.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return Env{}, err
	}
	defer logf.Close()
	cmd := exec.Command(self, "clipboard", "daemon", "--dir", dir)
	cmd.Stdout, cmd.Stderr = logf, logf
	setDetached(cmd)
	if err := cmd.Start(); err != nil {
		return Env{}, err
	}
	go cmd.Wait()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if e, ok := Ping(dir); ok {
			return e, nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return Env{}, errors.New("the clipboard daemon did not start (see " + filepath.Join(dir, "daemon.log") + ")")
}

// Set puts an item on the headless clipboard (data empty clears).
func Set(dir, mime string, data []byte) error {
	if dir == "" {
		dir = Dir()
	}
	if len(data) > MaxItem {
		return errors.New("too large")
	}
	c, err := net.DialTimeout("unix", filepath.Join(dir, "ctl.sock"), 2*time.Second)
	if err != nil {
		return err
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(15 * time.Second))
	if len(data) == 0 {
		fmt.Fprint(c, "CLEAR\n")
	} else {
		fmt.Fprintf(c, "SET %s %d\n", mime, len(data))
		if _, err := c.Write(data); err != nil {
			return err
		}
	}
	line, err := bufio.NewReader(c).ReadString('\n')
	if err != nil || !strings.HasPrefix(line, "OK") {
		return fmt.Errorf("clipboard daemon: %q %v", strings.TrimSpace(line), err)
	}
	return nil
}

// Daemon runs until killed: Xvfb, the owner and the control socket.
func Daemon(dir string, ttl time.Duration, logf func(string, ...any)) error {
	if err := prepare(dir); err != nil {
		return err
	}
	if _, ok := Ping(dir); ok {
		return errors.New("already running")
	}
	sock := filepath.Join(dir, "ctl.sock")
	os.Remove(sock)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		return err
	}
	os.Chmod(sock, 0o600)
	defer ln.Close()
	defer os.Remove(sock)

	var (
		mu    sync.Mutex
		owner *Owner
		timer *time.Timer
	)
	setItem := func(mime string, data []byte) error {
		mu.Lock()
		defer mu.Unlock()
		if owner == nil {
			return errors.New("no display")
		}
		owner.Set(mime, data)
		if timer != nil {
			timer.Stop()
		}
		if len(data) > 0 && ttl > 0 {
			timer = time.AfterFunc(ttl, func() { mu.Lock(); defer mu.Unlock(); owner.Set("", nil) })
		}
		return nil
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go serve(c, setItem, dir)
		}
	}()
	// Supervise Xvfb: when it dies, start another and re-publish the env.
	for {
		disp, cmd, xauth, err := startXvfb(dir)
		if err != nil {
			logf("xvfb: %v", err)
			time.Sleep(3 * time.Second)
			continue
		}
		os.Setenv("XAUTHORITY", xauth)
		o, err := NewOwner(disp)
		if err != nil {
			logf("owner: %v", err)
			cmd.Process.Kill()
			cmd.Wait()
			time.Sleep(2 * time.Second)
			continue
		}
		mu.Lock()
		owner = o
		mu.Unlock()
		envText := "DISPLAY=" + disp + "\nXAUTHORITY=" + xauth + "\n"
		os.WriteFile(filepath.Join(dir, "env"), []byte(envText), 0o600)
		logf("serving %s", disp)
		o.Run() // returns when the X connection closes
		mu.Lock()
		owner = nil
		mu.Unlock()
		cmd.Process.Kill()
		cmd.Wait()
		logf("display %s ended; restarting", disp)
		time.Sleep(time.Second)
	}
}

func serve(c net.Conn, set func(string, []byte) error, dir string) {
	defer c.Close()
	c.SetDeadline(time.Now().Add(20 * time.Second))
	r := bufio.NewReader(c)
	line, err := r.ReadString('\n')
	if err != nil {
		return
	}
	f := strings.Fields(line)
	if len(f) == 0 {
		return
	}
	switch f[0] {
	case "PING":
		fmt.Fprint(c, "PONG\n")
	case "CLEAR":
		set("", nil)
		fmt.Fprint(c, "OK\n")
	case "SET":
		if len(f) != 3 {
			fmt.Fprint(c, "ERR usage\n")
			return
		}
		n, err := strconv.Atoi(f[2])
		if err != nil || n <= 0 || n > MaxItem {
			fmt.Fprint(c, "ERR size\n")
			return
		}
		buf := make([]byte, n)
		if _, err := io.ReadFull(r, buf); err != nil {
			return
		}
		if err := set(f[1], buf); err != nil {
			fmt.Fprintf(c, "ERR %v\n", err)
			return
		}
		fmt.Fprint(c, "OK\n")
	default:
		fmt.Fprint(c, "ERR unknown\n")
	}
}

func startXvfb(dir string) (display string, cmd *exec.Cmd, xauth string, err error) {
	host, _ := os.Hostname()
	xauth = filepath.Join(dir, "Xauthority")
	for n := 90; n < 190; n++ {
		if _, e := os.Stat(fmt.Sprintf("/tmp/.X11-unix/X%d", n)); e == nil {
			continue
		}
		if _, e := os.Stat(fmt.Sprintf("/tmp/.X%d-lock", n)); e == nil {
			continue
		}
		if err = writeXauthority(xauth, host, strconv.Itoa(n)); err != nil {
			return
		}
		cmd = exec.Command("Xvfb", ":"+strconv.Itoa(n), "-nolisten", "tcp", "-auth", xauth, "-screen", "0", "16x16x8", "-noreset")
		cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
		if err = cmd.Start(); err != nil {
			return
		}
		exited := make(chan struct{})
		go func() { cmd.Wait(); close(exited) }()
		for i := 0; i < 60; i++ {
			select {
			case <-exited:
				goto next
			case <-time.After(100 * time.Millisecond):
			}
			if _, e := os.Stat(fmt.Sprintf("/tmp/.X11-unix/X%d", n)); e == nil {
				return ":" + strconv.Itoa(n), cmd, xauth, nil
			}
		}
		cmd.Process.Kill()
	next:
	}
	return "", nil, "", errors.New("no free display number")
}
