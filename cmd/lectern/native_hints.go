package main

// Hints and feedback for terminal links in a native attachment
// (docs/terminal-client.md). tmux reports no pointer movement, so there is
// no hover: instead Ctrl+] e labels every path and link on screen, the way a
// terminal's hints mode does, and typing a label opens it. A popup laid
// exactly over the pane re-draws the pane's text with the links underlined
// and their labels on top; nothing is sent to the agent. The same popup,
// shown for a moment, highlights a link a click just opened.

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/filelinks"
	"github.com/JeremiahM37/lectern/v2/internal/shellq"
	"golang.org/x/term"
)

// linkDelayEnv makes an action wait a moment first, so the hints popup it
// came from has closed before its highlight opens.
const linkDelayEnv = "LECTERN_LINK_DELAY_MS"

// hintAlphabet is the home row first, as hints modes usually order it.
const hintAlphabet = "asdfghjklqwertyuiopzxcvbnm"

// hintLabels gives n labels, all the same length so none is a prefix of
// another: one letter each while they last, then two.
func hintLabels(n int) []string {
	letters := []rune(hintAlphabet)
	labels := make([]string, 0, n)
	if n <= len(letters) {
		for i := 0; i < n; i++ {
			labels = append(labels, string(letters[i]))
		}
		return labels
	}
	for _, a := range letters {
		for _, b := range letters {
			if len(labels) == n {
				return labels
			}
			labels = append(labels, string(a)+string(b))
		}
	}
	return labels
}

// failure is the status line for an action that did not work.
func failure(action string, err error) string {
	verb := map[string]string{"open": "open", "web": "open", "download": "download", "copy": "copy", "send": "send", "view": "show"}[action]
	if verb == "" {
		verb = "do that"
	}
	return "Couldn't " + verb + ": " + err.Error()
}

// ---- drawing the pane again, with some cells styled

type cellStyle int

const (
	styleDim cellStyle = iota
	stylePlain
	styleLink
	styleLabel
	styleFlash
)

var styleSGR = map[cellStyle]string{
	styleDim:   "\x1b[0;2m",
	stylePlain: "\x1b[0m",
	styleLink:  "\x1b[0;1;4m",
	styleLabel: "\x1b[0;1;30;43m",
	styleFlash: "\x1b[0;1;7m",
}

// drawPane writes rows to out, each character styled by style(row, index),
// and labels written over the characters they name.
func drawPane(out io.Writer, rows []filelinks.Row, style func(row, col int) cellStyle, labels map[[2]int]string) {
	var b strings.Builder
	b.WriteString("\x1b[?25l\x1b[H\x1b[2J")
	for y, row := range rows {
		fmt.Fprintf(&b, "\x1b[%d;1H", y+1)
		text := []rune(row.Text)
		current := cellStyle(-1)
		for x := 0; x < len(text); x++ {
			if label, ok := labels[[2]int{y, x}]; ok && label != "" {
				b.WriteString(styleSGR[styleLabel])
				current = styleLabel
				for _, r := range label {
					b.WriteRune(r)
				}
				x += len([]rune(label)) - 1
				continue
			}
			if s := style(y, x); s != current {
				b.WriteString(styleSGR[s])
				current = s
			}
			b.WriteRune(text[x])
		}
	}
	b.WriteString("\x1b[0m")
	io.WriteString(out, b.String())
}

// ---- the highlight after a click or a menu choice

// flash shows, for a moment, which text a link was: every row of it.
func (e *linkEnv) flash(link filelinks.Link) {
	if e.client == "" || e.pane == "" || len(link.Spans) == 0 {
		return
	}
	file, err := e.saveLink(link)
	if err != nil {
		return
	}
	size, err := e.tmux("display-message", "-p", "-t", e.pane, "#{pane_width} #{pane_height}")
	if err != nil {
		return
	}
	w, h, ok := strings.Cut(strings.TrimSpace(size), " ")
	if !ok {
		return
	}
	cmd := exec.Command("tmux", "-S", e.socket, "display-popup", "-c", e.client, "-t", e.pane, "-B", "-x", "P", "-y", "P", "-w", w, "-h", h, "-E",
		e.selfCommand("flash", file))
	if cmd.Start() == nil {
		go cmd.Wait()
		time.Sleep(50 * time.Millisecond)
	}
}

func (e *linkEnv) showFlash(link filelinks.Link) {
	rows, _, err := e.paneRows(e.pane)
	if err != nil {
		return
	}
	lit := map[[2]int]bool{}
	for _, span := range link.Spans {
		for x := span.Start; x < span.End; x++ {
			lit[[2]int{span.Row, x}] = true
		}
	}
	drawPane(os.Stdout, rows, func(row, col int) cellStyle {
		if lit[[2]int{row, col}] {
			return styleFlash
		}
		return stylePlain
	}, nil)
	// The popup holds the keyboard while it shows: pass whatever is typed
	// meanwhile on to the pane, so nothing typed is lost.
	if state, err := term.MakeRaw(int(os.Stdin.Fd())); err == nil {
		defer term.Restore(int(os.Stdin.Fd()), state)
	}
	typed := make(chan []byte, 16)
	go func() {
		buf := make([]byte, 256)
		for {
			n, err := os.Stdin.Read(buf)
			if err != nil || n == 0 {
				return
			}
			typed <- append([]byte(nil), buf[:n]...)
		}
	}()
	done := time.After(flashFor)
	for {
		select {
		case data := <-typed:
			args := []string{"send-keys", "-t", e.pane, "-H"}
			for _, b := range data {
				args = append(args, strconv.FormatInt(int64(b)|0x100, 16)[1:])
			}
			_, _ = e.tmux(args...)
		case <-done:
			return
		}
	}
}

// flashFor is how long a highlight shows.
const flashFor = 400 * time.Millisecond

// selfCommand is a shell command running this binary's link entry point for
// this attachment, with the link script's environment.
func (e *linkEnv) selfCommand(args ...string) string {
	words := []string{shellq.Quote(e.dir + "/link.sh")}
	for _, arg := range args {
		words = append(words, shellq.Quote(arg))
	}
	return strings.Join(words, " ")
}

// ---- hints mode

// openHints lays the hints popup over the pane.
func (e *linkEnv) openHints(width, height string) int {
	if _, err := e.tmux("display-popup", "-c", e.client, "-t", e.pane, "-B", "-x", "P", "-y", "P", "-w", width, "-h", height, "-E",
		e.selfCommand("hints", e.pane, e.client)); err != nil {
		e.say("Couldn't show links: " + err.Error())
		return 1
	}
	return 0
}

type hint struct {
	label string
	link  filelinks.Link
}

// paneHints is every usable link on the visible pane, labelled top to
// bottom, left to right.
func (e *linkEnv) paneHints(rows []filelinks.Row, width int) []hint {
	workdir := e.workdir()
	seen := map[[2]int]bool{}
	var found []filelinks.Link
	for y := range rows {
		for _, link := range filelinks.LinksOnRow(rows, y, workdir, width) {
			first := link.Spans[0]
			key := [2]int{first.Row, first.Start}
			if seen[key] {
				continue
			}
			seen[key] = true
			found = append(found, link)
		}
	}
	// Bare names need the workspace to have them: ask for all at once.
	usable := make([]bool, len(found))
	var wg sync.WaitGroup
	limit := make(chan struct{}, 8)
	for i, link := range found {
		wg.Add(1)
		go func(i int, link filelinks.Link) {
			defer wg.Done()
			limit <- struct{}{}
			usable[i] = e.usable(link, workdir)
			<-limit
		}(i, link)
	}
	wg.Wait()
	var links []filelinks.Link
	for i, link := range found {
		if usable[i] {
			links = append(links, link)
		}
	}
	sort.SliceStable(links, func(i, j int) bool {
		a, b := links[i].Spans[0], links[j].Spans[0]
		return a.Row < b.Row || a.Row == b.Row && a.Start < b.Start
	})
	labels := hintLabels(len(links))
	hints := make([]hint, len(links))
	for i, link := range links {
		hints[i] = hint{label: labels[i], link: link}
	}
	return hints
}

// hints runs inside the popup: draw, read a label, act.
func (e *linkEnv) hints(in *os.File, out io.Writer) int {
	rows, width, err := e.paneRows(e.pane)
	if err != nil {
		return 1
	}
	return e.hintsFromRows(in, out, rows, width)
}

func (e *linkEnv) hintsFromRows(in *os.File, out io.Writer, rows []filelinks.Row, width int) int {
	hints := e.paneHints(rows, width)
	if len(hints) == 0 {
		e.say("No paths or links on screen")
		return 0
	}
	if state, err := term.MakeRaw(int(in.Fd())); err == nil {
		defer term.Restore(int(in.Fd()), state)
	}
	draw := func(typed string, footer string) {
		inLink := map[[2]int]bool{}
		labels := map[[2]int]string{}
		for _, h := range hints {
			if !strings.HasPrefix(h.label, typed) {
				continue
			}
			for _, span := range h.link.Spans {
				for x := span.Start; x < span.End; x++ {
					inLink[[2]int{span.Row, x}] = true
				}
			}
			if rest := h.label[len(typed):]; rest != "" {
				first := h.link.Spans[0]
				labels[[2]int{first.Row, first.Start}] = rest
			}
		}
		drawPane(out, rows, func(row, col int) cellStyle {
			if inLink[[2]int{row, col}] {
				return styleLink
			}
			return styleDim
		}, labels)
		if footer != "" {
			fmt.Fprintf(out, "\x1b[%d;1H\x1b[0;7m%s\x1b[K\x1b[0m", len(rows), footer)
		}
	}
	typed, actions := "", false
	draw("", "")
	buf := make([]byte, 16)
	for {
		n, err := in.Read(buf)
		if err != nil || n == 0 {
			return 0
		}
		for _, c := range buf[:n] {
			if c == 0x1b || c == 0x03 {
				return 0
			}
			if c >= 'A' && c <= 'Z' {
				actions = true
				c += 'a' - 'A'
			}
			if c < 'a' || c > 'z' {
				continue
			}
			typed += string(c)
			var matched *hint
			prefix := false
			for i := range hints {
				if hints[i].label == typed {
					matched = &hints[i]
				} else if strings.HasPrefix(hints[i].label, typed) {
					prefix = true
				}
			}
			switch {
			case matched != nil && !actions:
				return e.hintAct("open", matched.link)
			case matched != nil:
				return e.hintMenu(in, out, matched.link, draw, typed)
			case prefix:
				draw(typed, "")
			default:
				typed, actions = "", false
				draw("", "")
			}
		}
	}
}

// hintMenu offers the right-click actions for one hinted link.
func (e *linkEnv) hintMenu(in *os.File, out io.Writer, link filelinks.Link, draw func(string, string), typed string) int {
	keys := map[byte]string{'o': "open", 'c': "copy"}
	footer := " o open · c copy link · Esc cancel"
	if link.Kind == "file" {
		keys = map[byte]string{'o': "open", 'd': "download", 'c': "copy", 's': "send", 'w': "web"}
		footer = " o open · d download · c copy path · s send to the agent · w web viewer · Esc cancel"
	}
	draw(typed, footer)
	buf := make([]byte, 8)
	for {
		n, err := in.Read(buf)
		if err != nil || n == 0 {
			return 0
		}
		for _, c := range buf[:n] {
			if c == 0x1b || c == 0x03 {
				return 0
			}
			if action, ok := keys[c]; ok {
				return e.hintAct(action, link)
			}
		}
	}
}

// hintAct runs the action on its own once this popup has closed.
func (e *linkEnv) hintAct(action string, link filelinks.Link) int {
	if e.directAction != nil {
		if err := e.directAction(action, link); err != nil {
			e.say(failure(action, err))
			return 1
		}
		return 0
	}

	file, err := e.saveLink(link)
	if err != nil {
		return 1
	}
	os.Setenv(linkDelayEnv, strconv.Itoa(150))
	if err := e.spawn("act", action, file); err != nil {
		return 1
	}
	return 0
}
