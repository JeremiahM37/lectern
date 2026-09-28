package main

import (
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/config"
)

func TestListenHostDefaultsToLoopbackWithoutSignIn(t *testing.T) {
	down := func() bool { return false }
	up := func() bool { return true }
	cases := []struct {
		name     string
		cfg      config.Config
		hostSet  bool
		insecure bool
		ts       func() bool
		want     string
		refused  bool
	}{
		{name: "nothing configured", cfg: config.Config{Auth: "auto"}, ts: down, want: "127.0.0.1"},
		{name: "mock mode with no tailscale", cfg: config.Config{Auth: "auto", Mock: true}, ts: down, want: "127.0.0.1"},
		{name: "token configured", cfg: config.Config{Auth: "auto", AuthToken: "t"}, ts: down, want: "0.0.0.0"},
		{name: "tailscale running", cfg: config.Config{Auth: "auto"}, ts: up, want: "0.0.0.0"},
		{name: "explicit none stays local", cfg: config.Config{Auth: "none"}, ts: up, want: "127.0.0.1"},
		{name: "explicit network host, no sign-in", cfg: config.Config{Auth: "auto", Host: "0.0.0.0"}, hostSet: true, ts: down, refused: true},
		{name: "explicit none on the network", cfg: config.Config{Auth: "none", Host: "0.0.0.0", AuthToken: "t"}, hostSet: true, ts: up, refused: true},
		{name: "explicit network host with a token", cfg: config.Config{Auth: "auto", Host: "0.0.0.0", AuthToken: "t"}, hostSet: true, ts: down, want: "0.0.0.0"},
		{name: "explicit network host with tailscale", cfg: config.Config{Auth: "auto", Host: "0.0.0.0"}, hostSet: true, ts: up, want: "0.0.0.0"},
		{name: "explicit loopback", cfg: config.Config{Auth: "auto", Host: "127.0.0.1"}, hostSet: true, ts: down, want: "127.0.0.1"},
		{name: "insecure-listen accepted", cfg: config.Config{Auth: "auto", Host: "0.0.0.0"}, hostSet: true, insecure: true, ts: down, want: "0.0.0.0"},
	}
	for _, c := range cases {
		got, err := listenHost(&c.cfg, c.hostSet, c.insecure, c.ts)
		if c.refused {
			if err == nil || !strings.Contains(err.Error(), "--insecure-listen") || !strings.Contains(err.Error(), "LECTERN_AUTH_TOKEN") {
				t.Errorf("%s: want a refusal naming the fixes, got host=%q err=%v", c.name, got, err)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("%s: host=%q err=%v, want %q", c.name, got, err, c.want)
		}
	}
}

func TestServeArgs(t *testing.T) {
	if insecure, help, err := serveArgs([]string{"--insecure-listen"}); !insecure || help || err != nil {
		t.Fatalf("--insecure-listen: %v %v %v", insecure, help, err)
	}
	if _, help, err := serveArgs([]string{"--help"}); !help || err != nil {
		t.Fatalf("--help: %v %v", help, err)
	}
	if _, _, err := serveArgs([]string{"--bogus"}); err == nil {
		t.Fatal("unknown flag accepted")
	}
}
