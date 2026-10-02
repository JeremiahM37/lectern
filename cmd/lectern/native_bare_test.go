package main

import (
	"bytes"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

// fakeConn is a terminal that records what is typed into it.
type fakeConn struct {
	mu      sync.Mutex
	typed   bytes.Buffer
	out     chan []byte
	resized [2]int
}

func newFakeConn() *fakeConn { return &fakeConn{out: make(chan []byte, 16)} }

func (f *fakeConn) Read() ([]byte, error) {
	b, ok := <-f.out
	if !ok {
		return nil, io.EOF
	}
	return b, nil
}
func (f *fakeConn) Write(b []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.typed.Write(b)
	return nil
}
func (f *fakeConn) Resize(c, r int) error { f.resized = [2]int{c, r}; return nil }
func (f *fakeConn) Close()                {}
func (f *fakeConn) got() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.typed.String()
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}
func (s *syncBuffer) String() string { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

func testBareClient(cols, rows int) (*bareClient, *fakeConn, *syncBuffer) {
	out := &syncBuffer{}
	c := &bareClient{controls: nativeControls{Kind: "session", ID: "4"}, out: out,
		cache: map[*barePane][]string{}, realModes: map[int]bool{},
		dirty: make(chan struct{}, 1), done: make(chan string, 1), dir: '|', cols: cols, rows: rows}
	conn := newFakeConn()
	p := newBarePane(c, conn, true, "agent", cols, rows-1)
	c.panes = []*barePane{p}
	c.layout()
	return c, conn, out
}

func TestBareInputTokensKeepSequencesWhole(t *testing.T) {
	tok, rest, ok := nextInputToken([]byte("\x1b[<0;3;4Mab"))
	if !ok || string(tok) != "\x1b[<0;3;4M" || string(rest) != "ab" {
		t.Fatalf("mouse report split: %q %q %v", tok, rest, ok)
	}
	if _, _, ok := nextInputToken([]byte("\x1b[<0;3")); ok {
		t.Fatal("a partial report was taken as whole")
	}
	tok, rest, _ = nextInputToken([]byte("\x1ddls"))
	if string(tok) != "\x1d" || string(rest) != "dls" {
		t.Fatalf("Ctrl+] not taken alone: %q %q", tok, rest)
	}
}

// Every encoding a terminal uses for the two control keys is recognised,
// so a program that turned on kitty keys or modifyOtherKeys cannot hide them.
func TestBareKeyNamesCoverKeyboardProtocols(t *testing.T) {
	for _, seq := range []string{"\x1d", "\x1b[93;5u", "\x1b[27;5;93~"} {
		if keyOf([]byte(seq)) != "C-]" {
			t.Errorf("%q is not Ctrl+]", seq)
		}
	}
	for _, seq := range []string{"\x1c", "\x1b[92;5u", "\x1b[27;5;92~"} {
		if keyOf([]byte(seq)) != `C-\` {
			t.Errorf("%q is not Ctrl+\\", seq)
		}
	}
	if keyOf([]byte("\x1b[100u")) != "d" || keyOf([]byte("\x1b[97;2u")) != "A" {
		t.Error("kitty letters are not read")
	}
}

// Ctrl+\ never reaches the program: in a terminal it is SIGQUIT, which
// ended the agent and left its launch command on screen.
func TestBareCtrlBackslashNeverReachesTheAgent(t *testing.T) {
	c, conn, _ := testBareClient(80, 24)
	c.paused = true // the popup it opens takes the screen; nothing to show here
	for _, seq := range []string{"\x1c", "\x1b[92;5u", "\x1b[27;5;92~"} {
		c.input([]byte("a" + seq + "b"))
	}
	if strings.ContainsAny(conn.got(), "\x1c") || strings.Contains(conn.got(), "92") || conn.got() != "ababab" {
		t.Fatalf("typed into the agent: %q", conn.got())
	}
}

func TestBarePrefixKeys(t *testing.T) {
	c, conn, _ := testBareClient(80, 24)
	c.input([]byte("hi\x1d\x1d"))
	if conn.got() != "hi\x1d" {
		t.Fatalf("Ctrl+] Ctrl+] should type one Ctrl+]: %q", conn.got())
	}
	// A paste is text: a Ctrl+] inside it is not the prefix.
	c.input([]byte("\x1b[200~x\x1dd\x1b[201~"))
	if !strings.HasSuffix(conn.got(), "\x1b[200~x\x1dd\x1b[201~") {
		t.Fatalf("paste changed: %q", conn.got())
	}
	c.input([]byte("\x1d?"))
	if c.overlay == nil || !strings.Contains(c.overlay.items[len(c.overlay.items)-2].label, "Leave") {
		t.Fatalf("Ctrl+] ? did not list the keys: %+v", c.overlay)
	}
	c.input([]byte("\x1b"))
	if c.overlay != nil {
		t.Fatal("Esc did not close the list")
	}
	c.input([]byte("\x1dd"))
	select {
	case reason := <-c.done:
		if reason != "leave" {
			t.Fatalf("Ctrl+] d finished with %q", reason)
		}
	case <-time.After(time.Second):
		t.Fatal("Ctrl+] d did not leave")
	}
}

func plainBar(c *bareClient) string { return ansi.Strip(c.barText()) }

func TestBareBarNamesTheWayOutAtEveryWidth(t *testing.T) {
	for _, cols := range []int{40, 59, 80, 120} {
		c, _, _ := testBareClient(cols, 24)
		bar := plainBar(c)
		if !strings.Contains(bar, "Ctrl+] d leave") || ansi.StringWidth(bar) != cols {
			t.Errorf("%d columns: %q", cols, bar)
		}
		if cols >= 80 && !strings.Contains(bar, `Ctrl+\ send file`) {
			t.Errorf("%d columns lost the send-file key: %q", cols, bar)
		}
		c.prefix = true
		if bar := plainBar(c); !strings.Contains(bar, "Ctrl+] then  m actions · d leave") {
			t.Errorf("%d columns, after Ctrl+]: %q", cols, bar)
		}
		c.prefix = false
		c.needsYou = "⏸ Needs you: Bash: " + strings.Repeat("x", 200)
		if bar := plainBar(c); !strings.HasPrefix(strings.TrimSpace(bar), "⏸ Needs you · Ctrl+] y allow") {
			t.Errorf("%d columns cut the answer: %q", cols, bar)
		}
	}
}

func TestBareRenderDrawsThePaneAndTheBar(t *testing.T) {
	c, conn, out := testBareClient(60, 10)
	p := c.panes[0]
	p.start()
	conn.out <- []byte("hello from the agent\r\nsecond line")
	deadline := time.Now().Add(2 * time.Second)
	for {
		c.render()
		if strings.Contains(out.String(), "second line") || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	frame := out.String()
	for _, want := range []string{"hello from the agent", "second line", "Ctrl+] d", "\x1b[?1002h", "\x1b[?1006h"} {
		if !strings.Contains(frame, want) {
			t.Fatalf("frame lacks %q: %q", want, frame)
		}
	}
	close(conn.out)
}

func TestBareSplitLayoutSharesTheScreen(t *testing.T) {
	c, _, _ := testBareClient(81, 25)
	c.panes = append(c.panes, newBarePane(c, newFakeConn(), false, "shell", 1, 1))
	c.dir = '|'
	c.layout()
	a, b := c.panes[0], c.panes[1]
	if a.w+b.w+1 != 81 || a.h != 24 || b.x != a.w+1 {
		t.Fatalf("side by side: %+v %+v", [4]int{a.x, a.y, a.w, a.h}, [4]int{b.x, b.y, b.w, b.h})
	}
	c.dir = '-'
	c.layout()
	if a.h+b.h+1 != 24 || b.y != a.h+1 || a.w != 81 {
		t.Fatalf("stacked: %+v %+v", [4]int{a.x, a.y, a.w, a.h}, [4]int{b.x, b.y, b.w, b.h})
	}
}

// A program that asked for the mouse gets clicks in its own coordinates;
// otherwise a drag selects and copies with OSC 52.
func TestBareMouseGoesToTheProgramOrSelects(t *testing.T) {
	c, conn, out := testBareClient(80, 24)
	p := c.panes[0]
	p.start()
	conn.out <- []byte("copy me please\r\n")
	time.Sleep(100 * time.Millisecond)
	c.input([]byte("\x1b[<0;1;1M\x1b[<32;7;1M\x1b[<0;7;1m"))
	if !strings.Contains(out.String(), "\x1b]52;c;") || !strings.Contains(c.message, "Copied 7 characters") {
		t.Fatalf("drag did not copy: %q %q", out.String(), c.message)
	}
	conn.out <- []byte("\x1b[?1000h\x1b[?1006h")
	time.Sleep(100 * time.Millisecond)
	c.input([]byte("\x1b[<0;5;3M"))
	if !strings.Contains(conn.got(), "\x1b[<0;5;3M") {
		t.Fatalf("the program did not get its click: %q", conn.got())
	}
	close(conn.out)
}

// The agent's clipboard request reaches this terminal; a request to read
// the clipboard does not.
func TestBareClipboardWriteOnly(t *testing.T) {
	c, conn, out := testBareClient(80, 24)
	c.panes[0].start()
	conn.out <- []byte("\x1b]52;c;aGVsbG8=\a\x1b]52;c;?\a")
	time.Sleep(100 * time.Millisecond)
	if !strings.Contains(out.String(), "\x1b]52;c;aGVsbG8=\a") || strings.Contains(out.String(), "52;c;?") {
		t.Fatalf("clipboard: %q", out.String())
	}
	close(conn.out)
}
