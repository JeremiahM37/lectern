package sessions

import (
	"strings"
	"testing"
)

func TestHandoffRequiresCurrentFinalMarker(t *testing.T) {
	path := "/tmp/lectern-handoff-1-current.md"
	body := strings.Repeat("a partial write ", 20)
	for _, raw := range []string{body, body + "\n<!-- lectern:complete /tmp/lectern-handoff-1-old.md -->", body + "\n" + handoffMarker(path) + "\nmore work", handoffMarker(path)} {
		if _, ok := completedHandoff([]byte(raw), path); ok {
			t.Fatalf("accepted incomplete/stale wrap: %s", raw)
		}
	}
	got, ok := completedHandoff([]byte(body+"\n"+handoffMarker(path)+"\n"), path)
	if !ok || got != strings.TrimSpace(body) {
		t.Fatalf("completed wrap: %q %v", got, ok)
	}
	prompt := HandoffPrompt(path)
	if !strings.Contains(prompt, path+".partial") || !strings.Contains(prompt, handoffMarker(path)) || !strings.Contains(prompt, "atomically rename") {
		t.Fatal(prompt)
	}
}
