package ptyhost

import (
	"bytes"
	"io"
	"os"
	"sync"

	"golang.org/x/term"
)

// DetachKeys end an interactive attachment and leave the session running:
// Ctrl-] then d. Closing the terminal does the same.
const detachPrefix = 0x1d

// AttachTerminal shows a session in this process's terminal until the session
// ends, the host goes away, or the user detaches.
func AttachTerminal(cl *Client, name string, in, out *os.File) error {
	fd := int(in.Fd())
	cols, rows := 0, 0
	if w, h, err := term.GetSize(int(out.Fd())); err == nil {
		cols, rows = w, h
	}
	stream, err := cl.Attach(name, cols, rows)
	if err != nil {
		return err
	}
	defer stream.Close()
	if term.IsTerminal(fd) {
		if old, err := term.MakeRaw(fd); err == nil {
			defer term.Restore(fd, old)
		}
	}
	restoreOut := enableVT(out)
	defer restoreOut()
	// Leave the program's modes behind when detaching, and put the cursor
	// back, so the terminal is usable again.
	defer io.WriteString(out, "\x1b[?1049l\x1b[?2004l\x1b[?1000l\x1b[?1002l\x1b[?1003l\x1b[?1006l\x1b[?25h\x1b[0m\r\n")

	done := make(chan struct{})
	var once sync.Once
	finish := func() { once.Do(func() { close(done) }) }
	go func() {
		defer finish()
		for {
			b, err := stream.Read()
			if err != nil {
				return
			}
			if _, err := out.Write(b); err != nil {
				return
			}
		}
	}()
	stopResize := watchResize(out, func(w, h int) { _ = stream.Resize(w, h) })
	defer stopResize()
	go func() {
		defer finish()
		buf := make([]byte, 4096)
		prefix := false
		for {
			n, err := in.Read(buf)
			if n > 0 {
				chunk := buf[:n]
				if prefix {
					prefix = false
					if chunk[0] == 'd' {
						return
					}
					if chunk[0] != detachPrefix {
						chunk = append([]byte{detachPrefix}, chunk...)
					}
				}
				if i := bytes.IndexByte(chunk, detachPrefix); i == len(chunk)-1 {
					prefix = true
					chunk = chunk[:i]
				}
				if len(chunk) > 0 && stream.Write(append([]byte(nil), chunk...)) != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()
	<-done
	return nil
}
