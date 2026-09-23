package auth

import "testing"

func TestResolveModeExplicit(t *testing.T) {
	// An explicit setting always wins, even one that would otherwise probe.
	for _, m := range []Mode{ModeNone, ModeToken, ModeTailscale} {
		got := ResolveMode(string(m), "0.0.0.0", true, func() bool { t.Fatal("must not probe when explicit"); return false })
		if got != m {
			t.Errorf("ResolveMode(%q): got %q want %q", m, got, m)
		}
	}
}

func TestResolveModeAutoLoopback(t *testing.T) {
	for _, host := range []string{"", "127.0.0.1", "::1", "localhost"} {
		got := ResolveMode("", host, true, func() bool { t.Fatal("loopback must not probe tailscaled"); return false })
		if got != ModeNone {
			t.Errorf("host %q: got %q want none", host, got)
		}
	}
}

func TestResolveModeAutoTailscaleUp(t *testing.T) {
	got := ResolveMode("auto", "0.0.0.0", false, func() bool { return true })
	if got != ModeTailscale {
		t.Errorf("got %q want tailscale", got)
	}
}

func TestResolveModeAutoFallsBackToToken(t *testing.T) {
	got := ResolveMode("auto", "0.0.0.0", true, func() bool { return false })
	if got != ModeToken {
		t.Errorf("got %q want token", got)
	}
}

func TestResolveModeAutoFallsBackToNone(t *testing.T) {
	got := ResolveMode("auto", "0.0.0.0", false, func() bool { return false })
	if got != ModeNone {
		t.Errorf("got %q want none", got)
	}
}
