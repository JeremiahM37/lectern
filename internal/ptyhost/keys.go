package ptyhost

import "strings"

// keyBytes turns a tmux key name into what a terminal sends for it. As with
// tmux's send-keys, a word that is not a key name is typed as it is.
func keyBytes(name string, appCursor bool) ([]byte, bool) {
	if name == "" {
		return nil, true
	}
	cursor := func(final string) []byte {
		if appCursor {
			return []byte("\x1bO" + final)
		}
		return []byte("\x1b[" + final)
	}
	switch name {
	case "Enter":
		return []byte("\r"), true
	case "Escape":
		return []byte("\x1b"), true
	case "Tab":
		return []byte("\t"), true
	case "BTab":
		return []byte("\x1b[Z"), true
	case "BSpace":
		return []byte("\x7f"), true
	case "Space":
		return []byte(" "), true
	case "Up":
		return cursor("A"), true
	case "Down":
		return cursor("B"), true
	case "Right":
		return cursor("C"), true
	case "Left":
		return cursor("D"), true
	case "Home":
		return cursor("H"), true
	case "End":
		return cursor("F"), true
	case "PageUp", "PPage", "PgUp":
		return []byte("\x1b[5~"), true
	case "PageDown", "NPage", "PgDn":
		return []byte("\x1b[6~"), true
	case "DC", "Delete":
		return []byte("\x1b[3~"), true
	case "IC", "Insert":
		return []byte("\x1b[2~"), true
	}
	if seq, ok := functionKeys[name]; ok {
		return []byte(seq), true
	}
	if rest, ok := strings.CutPrefix(name, "M-"); ok && rest != "" {
		inner, _ := keyBytes(rest, appCursor)
		return append([]byte("\x1b"), inner...), true
	}
	if rest, ok := strings.CutPrefix(name, "C-"); ok && len(rest) == 1 {
		c := rest[0]
		switch {
		case c >= 'a' && c <= 'z':
			return []byte{c - 'a' + 1}, true
		case c >= 'A' && c <= 'Z':
			return []byte{c - 'A' + 1}, true
		case c == '@' || c == ' ':
			return []byte{0}, true
		case c >= '[' && c <= '_':
			return []byte{c - '@'}, true
		case c == '?':
			return []byte{0x7f}, true
		}
	}
	return []byte(name), true
}

var functionKeys = map[string]string{
	"F1": "\x1bOP", "F2": "\x1bOQ", "F3": "\x1bOR", "F4": "\x1bOS",
	"F5": "\x1b[15~", "F6": "\x1b[17~", "F7": "\x1b[18~", "F8": "\x1b[19~",
	"F9": "\x1b[20~", "F10": "\x1b[21~", "F11": "\x1b[23~", "F12": "\x1b[24~",
}
