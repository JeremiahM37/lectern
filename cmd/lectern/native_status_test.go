package main

import (
	"strings"
	"testing"
)

func TestAttachStatusSaysNeedsYouOnlyForThisSession(t *testing.T) {
	data := []byte(`[{"session_id":4,"tool_name":"Bash","input":{"command":"rm -rf build"}},{"session_id":9,"tool_name":"Edit","input":{"file_path":"/x"}}]`)
	if got := attachStatusLine(data, "9"); got != "⏸ Needs you: Edit: /x · Ctrl+] m answers · " {
		t.Fatalf("line: %q", got)
	}
	if got := attachStatusLine(data, "5"); got != "" {
		t.Fatalf("another session's approval shown: %q", got)
	}
}

// tmux expands formats in a status job's output. A command the agent wrote
// must not become a tmux job or style: every # is doubled, controls dropped.
func TestAttachStatusCannotInjectTmuxFormats(t *testing.T) {
	data := []byte(`[{"session_id":1,"tool_name":"Bash","input":{"command":"echo #(touch /tmp/pwned) #[fg=red]\u001b[2J"}}]`)
	got := attachStatusLine(data, "1")
	if strings.Contains(strings.ReplaceAll(got, "##", ""), "#") || strings.ContainsRune(got, 0x1b) {
		t.Fatalf("unescaped format or control in %q", got)
	}
}
