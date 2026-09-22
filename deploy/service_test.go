package deploy

import (
	"os"
	"strings"
	"testing"
)

func TestHostedUnitKeepsAgentProcessesAcrossControlPlaneRestart(t *testing.T) {
	raw, err := os.ReadFile("lectern.service")
	if err != nil {
		t.Fatal(err)
	}
	unit := string(raw)
	if !strings.Contains(unit, "KillMode=process\n") {
		t.Fatal("hosted unit must preserve agent descendants across a control-plane restart")
	}
	if !strings.Contains(unit, "ExecStart=/usr/local/bin/lectern\n") {
		t.Fatal("hosted unit lost the Lectern executable")
	}
}
