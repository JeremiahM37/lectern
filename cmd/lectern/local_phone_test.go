package main

import (
	"bytes"
	"net"
	"net/http"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// TestPhoneOnTheSameWiFi: `lectern phone` makes the same runtime reachable on
// the LAN address, and a phone gets in only by redeeming the one-time code.
func TestPhoneOnTheSameWiFi(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("local runtime test uses POSIX process locks")
	}
	if !hasLANAddress() {
		t.Skip("no local network address here (the isolated runner has none)")
	}
	bin := filepath.Join(t.TempDir(), "lectern")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	env := append(localTestEnv(t.TempDir()), "HOME="+t.TempDir())
	t.Cleanup(func() { _, _ = runLocalCLI(bin, env, "phone", "--off"); _, _ = runLocalCLI(bin, env, "local", "stop") })
	out, err := runLocalCLI(bin, env, "phone", "--no-qr")
	if err != nil || !bytes.Contains(out, []byte("not encrypted")) || !bytes.Contains(out, []byte("Tailscale")) {
		t.Fatalf("lectern phone: %v %s", err, out)
	}
	m := regexp.MustCompile(`(http://[0-9.]+:\d+)/pair#code=([A-Za-z0-9%-]+)`).FindSubmatch(out)
	if m == nil {
		t.Fatalf("no pairing link: %s", out)
	}
	base, code := string(m[1]), strings.ReplaceAll(string(m[2]), "%2D", "-")
	if res, err := http.Get(base + "/api/sessions"); err != nil || res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unpaired LAN request: %v %v", err, res)
	}
	exchange := func() *http.Response {
		req, _ := http.NewRequest("POST", base+"/api/pair/exchange", strings.NewReader(`{"code":"`+code+`","name":"test phone"}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", base)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res
	}
	res := exchange()
	var device *http.Cookie
	for _, c := range res.Cookies() {
		if c.Name == "lectern_device" {
			device = c
		}
	}
	if res.StatusCode != 200 || device == nil || device.Secure {
		t.Fatalf("pairing over the LAN: %d cookies=%v (a Secure cookie would be dropped on plain HTTP)", res.StatusCode, res.Cookies())
	}
	req, _ := http.NewRequest("GET", base+"/api/sessions", nil)
	req.AddCookie(device)
	if res, err := http.DefaultClient.Do(req); err != nil || res.StatusCode != 200 {
		t.Fatalf("paired phone: %v %v", err, res)
	}
	if res := exchange(); res.StatusCode == 200 {
		t.Fatal("a pairing code worked twice")
	}
	if out, err := runLocalCLI(bin, env, "phone", "--off"); err != nil {
		t.Fatalf("phone --off: %v %s", err, out)
	}
	if _, err := http.Get(base + "/"); err == nil {
		t.Fatal("still reachable on the LAN after --off")
	}
}

func hasLANAddress() bool {
	addrs, _ := net.InterfaceAddrs()
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && n.IP.To4() != nil && n.IP.IsPrivate() && !n.IP.IsLoopback() {
			return true
		}
	}
	return false
}
