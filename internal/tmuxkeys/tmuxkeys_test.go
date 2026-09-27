package tmuxkeys

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The suffix reaches tmux as one more command in the same invocation, with
// the if-shell condition and its commands each as a single argument.
func TestSuffixIsOneMoreCommandForTheSameTmux(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "tmux")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nfor a do printf '<%s>' \"$a\"; done\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", "-c", "tmux new-session -d -s lec-s1 -- bash"+Suffix())
	cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"))
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	want := "<new-session><-d><-s><lec-s1><--><bash><;><if-shell><-F><#{?#{extended-keys-format},#{==:#{extended-keys},off},0}>" +
		"<set-option -sq extended-keys on ; set-option -sq extended-keys-format csi-u>"
	if got := strings.TrimSpace(string(out)); got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}
