package tui

// keyHelpLines is the key reference behind the help screen (? on an empty
// input, or the palette). It lists what keys.go and input.go bind; a key
// added there belongs here too. Each line fits the help box at 80 columns
// (keyHelpMaxWidth), so nothing wraps and the descriptions stay aligned.
func keyHelpLines() []string {
	return []string{
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
		"  Ctrl+O              expand or collapse tool output",
		"  Alt+R               error log",
		"",
		"Modes",
		"  Ctrl+D or Shift+Tab cycle the mode:",
		"                      default, auto, plan, architect,",
		"                      socratic, challenge",
		"",
		"Control",
		"  Esc                 close the top window, else stop",
		"  Ctrl+X              stop the running turn",
		"  Ctrl+C              clear the input; twice to quit",
		"  Ctrl+Z              undo the last turn",
		"  PgUp / PgDn         scroll the transcript; wheel too",
		"",
		"Checkpoints",
		"  Ctrl+B / Ctrl+F     step back or forward in checkpoints",
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
	}
}

// keyHelpMaxWidth is the longest help line that fits the help box at 80 columns.
const keyHelpMaxWidth = 60
