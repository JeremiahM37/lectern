package term

import (
	tea "github.com/charmbracelet/bubbletea"
)

// Translate returns the bytes a terminal sends for key press km. appCursor
// selects the SS3 (ESC O) forms of the cursor and Home/End keys, which a
// program asks for with DECCKM. Keys with no terminal meaning return nil.
func Translate(km tea.KeyMsg, appCursor bool) []byte {
	var out []byte
	switch km.Type {
	case tea.KeyRunes:
		out = []byte(string(km.Runes))
	case tea.KeySpace:
		out = []byte(" ")
	case tea.KeyEnter:
		out = []byte("\r")
	case tea.KeyBackspace:
		out = []byte{0x7f}
	case tea.KeyTab:
		out = []byte("\t")
	case tea.KeyShiftTab:
		out = []byte("\x1b[Z")
	case tea.KeyEsc:
		out = []byte{0x1b}
	case tea.KeyUp:
		out = cursorKey('A', appCursor)
	case tea.KeyDown:
		out = cursorKey('B', appCursor)
	case tea.KeyRight:
		out = cursorKey('C', appCursor)
	case tea.KeyLeft:
		out = cursorKey('D', appCursor)
	case tea.KeyHome:
		out = cursorKey('H', appCursor)
	case tea.KeyEnd:
		out = cursorKey('F', appCursor)
	case tea.KeyDelete:
		out = []byte("\x1b[3~")
	case tea.KeyInsert:
		out = []byte("\x1b[2~")
	case tea.KeyPgUp:
		out = []byte("\x1b[5~")
	case tea.KeyPgDown:
		out = []byte("\x1b[6~")
	case tea.KeyF1:
		out = []byte("\x1bOP")
	case tea.KeyF2:
		out = []byte("\x1bOQ")
	case tea.KeyF3:
		out = []byte("\x1bOR")
	case tea.KeyF4:
		out = []byte("\x1bOS")
	case tea.KeyF5:
		out = []byte("\x1b[15~")
	case tea.KeyF6:
		out = []byte("\x1b[17~")
	case tea.KeyF7:
		out = []byte("\x1b[18~")
	case tea.KeyF8:
		out = []byte("\x1b[19~")
	case tea.KeyF9:
		out = []byte("\x1b[20~")
	case tea.KeyF10:
		out = []byte("\x1b[21~")
	case tea.KeyF11:
		out = []byte("\x1b[23~")
	case tea.KeyF12:
		out = []byte("\x1b[24~")
	default:
		// Ctrl+letters and the other C0 controls: ctrl+a is 0x01 and so on.
		if t := int(km.Type); t >= 0 && t <= 31 {
			out = []byte{byte(t)}
		}
	}
	if km.Alt && len(out) > 0 {
		out = append([]byte{0x1b}, out...)
	}
	return out
}

func cursorKey(final byte, app bool) []byte {
	if app {
		return []byte{0x1b, 'O', final}
	}
	return []byte{0x1b, '[', final}
}
