//go:build windows

package main

import (
	"os"
	"unsafe"

	"github.com/muesli/cancelreader"
	"golang.org/x/sys/windows"
)

// newInputReader reads this terminal's keys in a way that can be stopped
// while another program (the controls popup, a pager) has the terminal.
//
// cancelreader empties the console's input buffer whenever it starts, so
// that events ReadFile cannot turn into bytes (key releases, focus and
// resize events) do not leave a read it cannot cancel. The attach client
// starts a new reader each time the popup closes, and a key pressed in that
// moment — the next Ctrl+\, or the first letter of a reply — used to vanish.
// Key presses are kept and put back; only the events that cause the trouble
// are dropped.
func newInputReader(f *os.File) (cancelreader.CancelReader, error) {
	h := windows.Handle(f.Fd())
	var mode uint32
	if windows.GetConsoleMode(h, &mode) != nil {
		return cancelreader.NewReader(f)
	}
	keys := pendingKeyPresses(h)
	r, err := cancelreader.NewReader(f)
	if len(keys) > 0 {
		var written uint32
		_, _, _ = procWriteConsoleInputW.Call(uintptr(h), uintptr(unsafe.Pointer(&keys[0])), uintptr(len(keys)), uintptr(unsafe.Pointer(&written)))
	}
	return r, err
}

// consoleInputRecord is INPUT_RECORD with its KEY_EVENT_RECORD member; the
// union's largest member is 16 bytes, so every record is 20.
type consoleInputRecord struct {
	EventType       uint16
	_               uint16
	KeyDown         int32
	RepeatCount     uint16
	VirtualKeyCode  uint16
	VirtualScanCode uint16
	Char            uint16
	ControlKeyState uint32
}

var (
	kernel32               = windows.NewLazySystemDLL("kernel32.dll")
	procReadConsoleInputW  = kernel32.NewProc("ReadConsoleInputW")
	procWriteConsoleInputW = kernel32.NewProc("WriteConsoleInputW")
)

// pendingKeyPresses takes everything waiting in the console's input buffer
// and returns the key presses among it, in order.
func pendingKeyPresses(h windows.Handle) []consoleInputRecord {
	var n uint32
	if windows.GetNumberOfConsoleInputEvents(h, &n) != nil || n == 0 {
		return nil
	}
	records := make([]consoleInputRecord, n)
	var read uint32
	if ok, _, _ := procReadConsoleInputW.Call(uintptr(h), uintptr(unsafe.Pointer(&records[0])), uintptr(n), uintptr(unsafe.Pointer(&read))); ok == 0 {
		return nil
	}
	keys := records[:0]
	for _, rec := range records[:read] {
		if rec.EventType == windows.KEY_EVENT && rec.KeyDown != 0 {
			keys = append(keys, rec)
		}
	}
	return keys
}
