package exec

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A plain rm or a redirect into the home directory used to look like a write
// inside the project ("$HOME/x" joined to the working dir), so it ran with no
// approval while rm -rf of the same path asked.
func TestOutsideWritesSeesHomeTargetsAndPlainRm(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	work := t.TempDir()
	for _, cmd := range []string{
		"rm $HOME/.qa/file",
		`rm "${HOME}/.qa/file"`,
		"echo hi > $HOME/.qa/w.txt",
		"touch ~/.qa/new",
		"cd . && unlink /srv/elsewhere/f",
	} {
		outs := outsideWrites(work, work, cmd)
		if len(outs) == 0 {
			t.Errorf("%q: expected an outside target", cmd)
			continue
		}
		if strings.Contains(cmd, "HOME") || strings.Contains(cmd, "~") {
			if !strings.HasPrefix(outs[0], filepath.Clean(home)) {
				t.Errorf("%q: target %q not expanded to home", cmd, outs[0])
			}
		}
	}
	for _, cmd := range []string{"rm build/out.o", "rm -rf $HOME/.qa", "echo x > /tmp/a"} {
		if outs := outsideWrites(work, work, cmd); len(outs) != 0 {
			t.Errorf("%q: unexpected outside targets %v", cmd, outs)
		}
	}
}

func TestRmDescriptionQuotedHomeTarget(t *testing.T) {
	desc, ok, severe := classifySeverity(`rm -rf "$HOME/.cache/x"`, t.TempDir())
	if !ok || !severe || !strings.Contains(desc, "home") {
		t.Fatalf("got %q ok=%v severe=%v", desc, ok, severe)
	}
}

func TestUserDeclinedClosesTheMatter(t *testing.T) {
	msg := userDeclined("delete files", "")
	for _, bad := range []string{"/tmp", "narrower"} {
		if strings.Contains(msg, bad) {
			t.Fatalf("decline text suggests a workaround (%q): %s", bad, msg)
		}
	}
}
