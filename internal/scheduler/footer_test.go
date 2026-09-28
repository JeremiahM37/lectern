package scheduler

import (
	"strings"
	"testing"
)

func TestAgentTaskFooterNamesTheTargetsKit(t *testing.T) {
	if AgentTaskFooterFor("") != AgentTaskFooter {
		t.Error("a target without lectern keeps the lec.py footer")
	}
	got := AgentTaskFooterFor("/opt/lectern")
	if strings.Contains(got, "lec.py") || strings.Count(got, `/opt/lectern helper lec add-task "short title" "detailed prompt"`) != 2 {
		t.Errorf("footer does not name the Go kit:\n%s", got)
	}
}
