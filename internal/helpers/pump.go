package helpers

import (
	"fmt"
	"io"
	"os"
	"unicode/utf8"
)

// pump is internal/drivers' pumpScript, the relay that owns a streaming
// run's steer fifo: it reopens the fifo after every writer leaves and copies
// each non-empty line to stdout (the agent's stdin) until the end sentinel.
func init() { Register("pump", pump) }

// pumpEnd is the drivers' endSentinel.
const pumpEnd = "\x1eLECTERN_END\x1e"

func pump(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	if len(args) < 1 {
		fmt.Fprintln(stderr, "usage: pump FIFO")
		return 1
	}
	for {
		f, err := os.Open(args[0])
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		code, done := pumpOnce(f, stdout, stderr)
		f.Close()
		if done {
			return code
		}
	}
}

// pumpOnce relays lines until the writer side closes. Line endings are read
// as Python's text mode reads them: "\n", "\r\n" and a lone "\r" all end a
// line.
func pumpOnce(f io.Reader, stdout, stderr io.Writer) (int, bool) {
	var line []byte
	skipLF := false
	emit := func() (int, bool) {
		s := line
		line = line[:0]
		if !utf8.Valid(s) {
			fmt.Fprintln(stderr, "pump: input is not valid UTF-8")
			return 1, true
		}
		switch string(s) {
		case pumpEnd:
			return 0, true
		case "":
			return 0, false
		}
		if _, err := stdout.Write(append(s, '\n')); err != nil {
			return 1, true
		}
		return 0, false
	}
	buf := make([]byte, 8192)
	for {
		n, err := f.Read(buf)
		for _, c := range buf[:n] {
			switch {
			case c == '\n' && skipLF:
				skipLF = false
				continue
			case c == '\n' || c == '\r':
				skipLF = c == '\r'
				if code, done := emit(); done {
					return code, true
				}
				continue
			}
			skipLF = false
			line = append(line, c)
		}
		if err != nil {
			if len(line) > 0 {
				if code, done := emit(); done {
					return code, true
				}
			}
			if err == io.EOF {
				return 0, false
			}
			fmt.Fprintln(stderr, err)
			return 1, true
		}
	}
}
