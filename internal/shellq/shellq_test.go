package shellq

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestQuoteOfHomeStringDisablesExpansion is a regression guard for the real
// bug HomePath exists to fix: naively quoting a Go string that already
// contains literal "$HOME" text disables the shell's own expansion of it,
// because Quote's safeWord charset excludes "$" and so falls back to single
// quoting. Run against a real shell, not asserted about string shape alone,
// since the whole point is what bash actually does with the result.
func TestQuoteOfHomeStringDisablesExpansion(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not installed")
	}
	broken := Quote("$HOME/shellq-regression-marker")
	if !strings.HasPrefix(broken, "'") {
		t.Fatalf("expected Quote to single-quote a $HOME-containing string (proving the bug still exists to guard against), got %q", broken)
	}
	out, err := exec.Command("bash", "-c", "echo "+broken).CombinedOutput()
	if err != nil {
		t.Fatalf("bash -c: %v (%s)", err, out)
	}
	got := strings.TrimSpace(string(out))
	if got != "$HOME/shellq-regression-marker" {
		t.Fatalf("expected the literal unexpanded string (demonstrating why HomePath is needed), got %q", got)
	}
}

// TestHomePathExpandsUnderRealShell is the fix's own proof: HomePath's
// output, run through a real bash, must resolve to the shell's actual $HOME,
// not a literal "$HOME" string.
func TestHomePathExpandsUnderRealShell(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not installed")
	}
	word := HomePath("/shellq-regression-marker/sub dir/file.txt")
	cmd := exec.Command("bash", "-c", "printf '%s' "+word)
	cmd.Env = append(os.Environ(), "HOME=/tmp/shellq-home-test")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bash -c: %v (%s)", err, out)
	}
	got := string(out)
	want := "/tmp/shellq-home-test/shellq-regression-marker/sub dir/file.txt"
	if got != want {
		t.Fatalf("HomePath did not expand correctly under a real shell: got %q, want %q", got, want)
	}
}

// TestHomePathIsOneShellWord proves a path containing a space still survives
// as a single argument end to end (a real `cat >` failure mode if the two
// concatenated fragments were ever not adjacent).
func TestHomePathIsOneShellWord(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not installed")
	}
	word := HomePath("/a b/c")
	cmd := exec.Command("bash", "-c", "set -- "+word+"; printf '%d:%s' \"$#\" \"$1\"")
	cmd.Env = append(os.Environ(), "HOME=/tmp/shellq-home-test2")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bash -c: %v (%s)", err, out)
	}
	if got, want := string(out), "1:/tmp/shellq-home-test2/a b/c"; got != want {
		t.Fatalf("got %q, want %q (word split into more than one argument)", got, want)
	}
}
