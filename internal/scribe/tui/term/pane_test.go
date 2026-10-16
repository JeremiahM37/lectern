package term

import (
	"runtime"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// startPTY starts a pane for a test and closes it when the test ends.
func startPTY(t *testing.T, w, h int, argv ...string) *Pane {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("pty tests run on unix")
	}
	p, err := New(argv, t.TempDir(), w, h)
	if err != nil {
		t.Fatalf("New(%v): %v", argv, err)
	}
	t.Cleanup(func() { _ = p.Close() })
	return p
}

// waitFor polls cond until it holds or the deadline passes.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestColourOutput(t *testing.T) {
	p := startPTY(t, 20, 4, "sh", "-c", `printf '\033[31mred\033[0m ok\n'; sleep 5`)
	waitFor(t, "output", func() bool { return strings.Contains(p.Text(), "red ok") })

	raw := p.View()
	lines := strings.Split(raw, "\n")
	if len(lines) != 4 {
		t.Fatalf("View has %d lines, want 4", len(lines))
	}
	for i, l := range lines {
		if w := ansi.StringWidth(l); w != 20 {
			t.Fatalf("line %d is %d cells, want 20: %q", i, w, l)
		}
	}
	if !strings.Contains(raw, "\x1b[31m") {
		t.Fatalf("red foreground not drawn as SGR: %q", raw)
	}
	if got := ansi.Strip(lines[0]); !strings.HasPrefix(got, "red ok") {
		t.Fatalf("first row = %q", got)
	}
}

func TestCatEchoesWrittenKeys(t *testing.T) {
	p := startPTY(t, 40, 6, "cat")
	if err := p.Write([]byte("hello pane\n")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "echo", func() bool { return strings.Contains(p.Text(), "hello pane") })
}

func TestSendKeyTypesIntoProgram(t *testing.T) {
	p := startPTY(t, 40, 6, "sh", "-c", "read x; echo got=$x; sleep 5")
	for _, r := range "abc" {
		_ = p.SendKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	_ = p.SendKey(tea.KeyMsg{Type: tea.KeyEnter})
	waitFor(t, "shell echo", func() bool { return strings.Contains(p.Text(), "got=abc") })
}

func TestResizeReachesProgram(t *testing.T) {
	p := startPTY(t, 80, 24, "sh", "-c", "read x; stty size; sleep 5")
	if err := p.Resize(30, 6); err != nil {
		t.Fatal(err)
	}
	if w, h := p.Size(); w != 30 || h != 6 {
		t.Fatalf("Size = %dx%d, want 30x6", w, h)
	}
	if err := p.Write([]byte("\n")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "stty size", func() bool { return strings.Contains(p.Text(), "6 30") })
	if got := len(strings.Split(p.View(), "\n")); got != 6 {
		t.Fatalf("View has %d rows after resize, want 6", got)
	}
}

func TestExitCode(t *testing.T) {
	p := startPTY(t, 40, 6, "sh", "-c", "echo bye; exit 3")
	select {
	case <-p.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("program did not exit")
	}
	exited, code := p.Exited()
	if !exited || code != 3 {
		t.Fatalf("Exited = %v, %d; want true, 3", exited, code)
	}
	if !strings.Contains(p.Text(), "bye") {
		t.Fatalf("final output lost: %q", p.Text())
	}
}

func TestScrollback(t *testing.T) {
	p := startPTY(t, 20, 5, "sh", "-c", "seq 1 100; sleep 5")
	waitFor(t, "last line", func() bool { return strings.Contains(p.Text(), "100") })
	live := p.View()
	p.Wheel(true, 10)
	scrolled := p.View()
	if scrolled == live {
		t.Fatal("wheel up did not move the view into history")
	}
	if strings.Contains(ansi.Strip(scrolled), "100") {
		t.Fatalf("scrolled view still shows the live last line:\n%s", ansi.Strip(scrolled))
	}
	p.ScrollToBottom()
	if p.View() != live {
		t.Fatal("ScrollToBottom did not restore the live screen")
	}
}

func TestCloseIsIdempotentAndWritesFail(t *testing.T) {
	p := startPTY(t, 20, 4, "sh", "-c", "sleep 5")
	if err := p.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := p.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if err := p.Write([]byte("x")); err == nil {
		t.Fatal("Write after Close succeeded")
	}
}

func TestNewRejectsEmptyCommand(t *testing.T) {
	if _, err := New(nil, "", 10, 10); err == nil {
		t.Fatal("empty command accepted")
	}
}
