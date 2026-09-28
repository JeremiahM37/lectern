package ptyhost

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/cancelreader"
	"golang.org/x/term"
)

// TerminalControls supplies the same controls for local and remote attachments.
// Actions run with the terminal restored; the underlying agent keeps running.
type TerminalControls struct {
	Action  func(action string, insert func(string) error) error
	Status  func() string
	History func(text string) error
	Links   func(text string, width int, insert func(string) error) error
}

// AttachProcess gives any attachment command a client-side screen and a
// reserved footer. It uses the same PTY and emulator as the built-in host,
// including Windows ConPTY, without depending on a second terminal multiplexer.
func AttachProcess(argv []string, in, out *os.File, controls TerminalControls) error {
	cols, rows, err := term.GetSize(int(out.Fd()))
	if err != nil {
		return err
	}
	if rows < 3 {
		return fmt.Errorf("terminal needs at least three rows")
	}
	proc, err := StartProcess(argv, cols, rows-1)
	if err != nil {
		return err
	}
	defer proc.Close()
	old, err := term.MakeRaw(int(in.Fd()))
	if err != nil {
		return err
	}
	defer term.Restore(int(in.Fd()), old)
	restoreVT := enableVT(out)
	defer restoreVT()
	reset := "\x1b[?2004l\x1b[?1000l\x1b[?1002l\x1b[?1003l\x1b[?1006l\x1b[?1l\x1b[=0u\x1b[>4;0m\x1b[0m"
	defer io.WriteString(out, reset+"\x1b[?1049l\x1b[?25h\r\n")
	io.WriteString(out, "\x1b[?1049h")
	screen := &Session{host: &Host{history: DefaultHistory}, cols: cols, rows: rows - 1, modes: map[ansi.Mode]bool{}}
	screen.emu = screen.newEmulator(cols, rows-1)
	var writeMu sync.Mutex
	write := func(b []byte) error { writeMu.Lock(); defer writeMu.Unlock(); _, err := proc.Write(b); return err }
	// Answer terminal queries even while the controls menu covers the agent.
	go func() {
		b := make([]byte, 4096)
		for {
			n, e := screen.emu.Read(b)
			if n > 0 {
				_ = write(b[:n])
			}
			if e != nil {
				return
			}
		}
	}()
	dirty := true
	outputDone := make(chan struct{})
	go func() {
		defer close(outputDone)
		b := make([]byte, 32768)
		for {
			n, e := proc.Read(b)
			if n > 0 {
				screen.mu.Lock()
				_, _ = screen.emu.Write(b[:n])
				dirty = true
				screen.mu.Unlock()
			}
			if e != nil {
				return
			}
		}
	}()
	defer func() { _ = proc.Close(); <-outputDone; _ = screen.emu.Close() }()
	stopResize := watchResize(out, func(w, h int) {
		if w < 1 || h < 3 {
			return
		}
		screen.mu.Lock()
		screen.cols, screen.rows = w, h-1
		screen.emu.Resize(w, h-1)
		dirty = true
		screen.mu.Unlock()
		_ = proc.Resize(w, h-1)
	})
	defer stopResize()
	status := ""
	quit := make(chan struct{})
	defer close(quit)
	if controls.Status != nil {
		go func() {
			for {
				s := controls.Status()
				screen.mu.Lock()
				status = s
				dirty = true
				screen.mu.Unlock()
				select {
				case <-quit:
					return
				case <-time.After(5 * time.Second):
				}
			}
		}()
	}
	type inputEvent struct {
		data []byte
		err  error
	}
	var input <-chan inputEvent
	var stopInput func()
	startInput := func() error {
		reader, e := cancelreader.NewReader(in)
		if e != nil {
			return e
		}
		events := make(chan inputEvent)
		done := make(chan struct{})
		stopped := make(chan struct{})
		go func() {
			defer close(stopped)
			b := make([]byte, 4096)
			for {
				n, e := reader.Read(b)
				event := inputEvent{append([]byte(nil), b[:n]...), e}
				select {
				case events <- event:
				case <-done:
					return
				}
				if e != nil {
					return
				}
			}
		}()
		input = events
		stopInput = func() { close(done); reader.Cancel(); <-stopped; _ = reader.Close() }
		return nil
	}
	if err := startInput(); err != nil {
		return err
	}
	defer func() { stopInput() }()
	ticker := time.NewTicker(time.Second / 30)
	defer ticker.Stop()
	prefix := false
	for {
		select {
		case <-outputDone:
			return nil
		case <-ticker.C:
			screen.mu.Lock()
			if dirty {
				frame := string(screen.snapshot())
				// A snapshot describes current keyboard flags, not another stack push.
				if len(screen.kitty) > 0 {
					f := screen.kitty[len(screen.kitty)-1]
					frame = strings.ReplaceAll(frame, fmt.Sprintf("\x1b[>%du", f), fmt.Sprintf("\x1b[=%du", f))
				}
				hint := "Ctrl+] menu · Ctrl+] d leave · Ctrl+\\ send file"
				if prefix {
					hint = "m actions · e links · [ scroll · u file · d leave · Esc cancel"
				}
				if status != "" {
					hint = "Needs you · Ctrl+] m · " + hint
				}
				hint = ansi.Truncate(hint, screen.cols, "")
				pos := screen.emu.CursorPosition()
				fmt.Fprintf(out, "%s%s\x1b[%d;1H\x1b[0;7m%s\x1b[K\x1b[0m\x1b[%d;%dH", reset, frame, screen.rows+1, hint, pos.Y+1, pos.X+1)
				dirty = false
			}
			screen.mu.Unlock()
		case event := <-input:
			if event.err != nil {
				return nil
			}
			var pending []byte
			action := ""
			detach := false
			for _, b := range event.data {
				if prefix {
					prefix = false
					switch b {
					case 'd':
						detach = true
					case 'u':
						action = "upload"
					case 'e':
						action = "links"
					case '[':
						action = "history"
					case 'm', '?', ' ':
						action = "menu"
					case 0x1b:
					case detachPrefix:
						pending = append(pending, b)
					default:
						pending = append(pending, detachPrefix, b)
					}
				} else if b == detachPrefix {
					prefix = true
				} else if b == 0x1c {
					action = "upload"
				} else {
					pending = append(pending, b)
				}
				if action != "" || detach {
					break
				}
			}
			if len(pending) > 0 {
				if err := write(pending); err != nil {
					return err
				}
			}
			if detach {
				return nil
			}
			screen.mu.Lock()
			dirty = true
			screen.mu.Unlock()
			if action != "" && (controls.Action != nil || action == "links" && controls.Links != nil || action == "history" && controls.History != nil) {
				stopInput()
				io.WriteString(out, reset+"\x1b[?1049l\x1b[?25h")
				_ = term.Restore(int(in.Fd()), old)
				var actionErr error
				insert := func(s string) error { return write([]byte(s)) }
				if action == "history" && controls.History != nil {
					actionErr = controls.History(screen.capture(DefaultHistory))
				} else if action == "links" && controls.Links != nil {
					screen.mu.Lock()
					width := screen.cols
					screen.mu.Unlock()
					actionErr = controls.Links(screen.capture(0), width, insert)
				} else if controls.Action != nil {
					actionErr = controls.Action(action, insert)
				}
				_, err = term.MakeRaw(int(in.Fd()))
				if err != nil {
					stopInput = func() {}
					return err
				}
				io.WriteString(out, "\x1b[?1049h")
				if err := startInput(); err != nil {
					stopInput = func() {}
					return err
				}
				if actionErr != nil {
					screen.mu.Lock()
					status = ""
					_, _ = screen.emu.Write([]byte("\r\nLectern controls: " + actionErr.Error() + "\r\n"))
					screen.mu.Unlock()
				}
			}
		}
	}
}
