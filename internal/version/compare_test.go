package version

import "testing"

func TestOlder(t *testing.T) {
	cases := []struct {
		a, b         Info
		older, known bool
	}{
		{Info{Version: "2.4.0-dev+54311ce5d6a9"}, Info{Version: "2.4.1+f95011613274"}, true, true},
		{Info{Version: "2.4.1"}, Info{Version: "2.4.0"}, false, true},
		{Info{Version: "2.4.1-dev"}, Info{Version: "2.4.1"}, true, true},
		{Info{Version: "2.4.1"}, Info{Version: "2.4.1-dev"}, false, true},
		{Info{Version: "2.10.0"}, Info{Version: "2.9.9"}, false, true},
		{Info{Version: "2.4.1+a"}, Info{Version: "2.4.1+b"}, false, false},
		{Info{Version: "2.4.1", CommitTime: "2026-09-25T10:00:00Z"}, Info{Version: "2.4.1", CommitTime: "2026-09-27T10:00:00Z"}, true, true},
		{Info{Version: "2.4.1", CommitTime: "2026-09-27T10:00:00Z"}, Info{Version: "2.4.1", CommitTime: "2026-09-27T10:00:00Z"}, false, true},
		{Info{Version: "dev"}, Info{Version: "2.4.1"}, false, false},
	}
	for _, c := range cases {
		older, known := Older(c.a, c.b)
		if older != c.older || known != c.known {
			t.Errorf("Older(%q, %q) = %v,%v; want %v,%v", c.a.Version, c.b.Version, older, known, c.older, c.known)
		}
	}
}
