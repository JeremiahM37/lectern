package tui

import "github.com/JeremiahM37/lectern/v2/internal/scribe/tui/theme"

// keyHelpLines is the key reference behind the help screen (? on an empty
// input, or the palette). It lists what keys.go and input.go bind; a key
// added there belongs here too. Each line fits the help box at 80 columns
// (keyHelpMaxWidth), so nothing wraps and the descriptions stay aligned.
func keyHelpLines() []string {
	lines := []string{
		"Keys",
		"  Enter               send; queues while Scribe works",
		"  Shift+Enter, Ctrl+J newline",
		"  Up / Down           history (on the first or last line)",
		"  Ctrl+R              search history",
		"  Alt+S / Alt+P       stash draft / pop it back",
		"  Right               accept a completion",
		"  Tab                 accept a completion, or next tab",
		"  @path               mention a file",
		"  !command            run a shell command",
		"  ?                   this help (on an empty input)",
		"",
		"Panels",
		"  Ctrl+K              command palette",
		"  Ctrl+L              model picker",
		"  Ctrl+P              sessions",
		"  Ctrl+G              git menu",
		"  Ctrl+S              skills",
		"  /theme: Tab transparent, M / D opacity, B border style",
		"  Ctrl+O              expand or collapse tool output",
		"  Alt+R               error log",
		"",
		"Modes",
		"  Ctrl+D or Shift+Tab cycle the mode, in this order:",
	}
	lines = append(lines, modeHelpLines()...)
	return append(lines,
		"",
		"Control",
		"  Esc                 close the top window, else stop",
		"  Ctrl+X              stop the running turn",
		"  Ctrl+C              clear the input; twice to quit",
		"  Ctrl+Z              undo the last turn",
		"  PgUp / PgDn         scroll the transcript; wheel too",
		"  Left / Right        fold or unfold a group (grouped lists)",
		"",
		"Checkpoints",
		"  Ctrl+B / Ctrl+F     step back or forward in checkpoints",
		"  Ctrl+F past the last returns to live; /checkpoint live too",
		"  Alt+C               checkpoint rail",
		"                      Up / Down move, Enter restores,",
		"                      Esc leaves the rail",
		"  Click a marker      browse to that checkpoint",
		"",
		"Tabs",
		"  Ctrl+T new tab      Ctrl+W close      Alt+1..5 switch",
		"  Ctrl+[ / Ctrl+]     previous or next tab",
		"",
		"Commands",
	)
}

// modeHelpLines gives one line per mode in cycle order: its name and what it
// does. The descriptions are the ones the status bar shows (modeStyle).
func modeHelpLines() []string {
	var out []string
	for _, id := range modeOrder {
		mi := modeStyle(id, theme.Theme{})
		out = append(out, "    "+padRight(mi.label, 11)+mi.desc)
	}
	return out
}

// keyHelpMaxWidth is the longest help line that fits the help box at 80 columns.
const keyHelpMaxWidth = 60
