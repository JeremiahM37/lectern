package executor

import (
	"strings"
	"testing"
)

func TestBridgeCommandFollowsTheTargetsLecternBinary(t *testing.T) {
	p := NewPct("101")
	if got := bridgeCommand(p, 8080); !strings.HasPrefix(got, "exec python3 -u -c ") || !strings.HasSuffix(got, " 8080") {
		t.Fatalf("no lectern binary: %s", got)
	}
	SetTargetEnv(p, TargetEnv{Lectern: "/opt/lectern bin/lectern"})
	if got := bridgeCommand(p, 8080); got != "exec '/opt/lectern bin/lectern' helper bridge 8080" {
		t.Fatalf("with lectern: %s", got)
	}
	if got := bridgeCommand(nil, 1); !strings.HasPrefix(got, "exec python3 ") {
		t.Fatalf("no executor: %s", got)
	}
}
