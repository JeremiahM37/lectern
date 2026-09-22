package main

import (
	"os/exec"
	"reflect"
	"strings"
	"testing"
)

func TestAttachmentInWorkspacePreservesArguments(t *testing.T) {
	argv := []string{"printf", "%s\\n", "path with spaces", ";", "$(touch must-not-run)", "it's quoted"}
	if got := attachmentInWorkspace(argv, ""); !reflect.DeepEqual(got, argv) {
		t.Fatalf("outside workspace: %q", got)
	}
	got := attachmentInWorkspace(argv, "/tmp/tmux")
	if got[0] != "tmux" || got[1] != "display-popup" {
		t.Fatalf("missing popup: %q", got)
	}
	out, err := exec.Command("sh", "-c", got[len(got)-1]).Output()
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != strings.Join(argv[2:], "\n")+"\n" {
		t.Fatalf("changed inner arguments: %q", out)
	}
}
