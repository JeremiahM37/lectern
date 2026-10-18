package tui

import "strings"

// gitFailurePrefixes are the flash lines a git operation reports when it fails
// (see modal_git.go and commands_menus.go). Each one leaves the user with a
// repository in a state they must look at, so the failure is an alert. These
// are matched by text because the git producers return plain flashMsg values
// without the alert flag.
var gitFailurePrefixes = []string{
	"Commit failed: ",
	"Stash failed: ",
	"Stash pop failed: ",
	"Pull failed: ",
	"Push failed: ",
	"Force push failed: ",
	"Reset failed: ",
}

// flashErrAlertTitle is the title of the alert for a failed command.
const flashErrAlertTitle = "Command failed"

// failAlert reports a failure the user must act on: the flash line where the
// user is looking, and a dismissable alert too (spec 01 §7.3 and §7.7). Use it
// for a real failure (a save that did not happen, a stop that did not take,
// a write that failed). Usage hints, invalid values and transient states stay
// on setFlash alone.
func (m *Model) failAlert(text string) {
	m.setFlash("err", text)
	m.raiseFlashAlert(flashErrAlertTitle, text)
}

// flashAlertTitle decides whether a flash line is a failure the user must act
// on, and so also opens a dismissable alert (spec 01 §7.3 and §7.7). It returns
// the alert title and true when it should.
//
// Flash-only: info and ok lines, usage hints ("Usage: ..."), and invalid
// choices from cmdHint. A usage hint or an invalid choice is a mistake in how a
// command was typed; the flash line is enough for it, and an alert would make a
// typo look like a crash.
func flashAlertTitle(msg flashMsg) (string, bool) {
	if msg.kind != "err" || strings.TrimSpace(msg.text) == "" {
		return "", false
	}
	if strings.HasPrefix(msg.text, "Usage:") {
		return "", false
	}
	// A producer that marks the failure (cmdErr: a command the backend
	// reported) is trusted; the text table covers the git failures that are
	// not marked.
	if msg.alert || hasAnyPrefix(msg.text, gitFailurePrefixes) {
		return flashErrAlertTitle, true
	}
	return "", false
}

// raiseFlashAlert opens the dismissable alert for a failed flash line. A
// repeat of an alert already on the stack is not added again.
func (m *Model) raiseFlashAlert(title, text string) {
	body := alertBody(strings.TrimSpace(text))
	if m.errorAlertShowing(body) {
		return
	}
	m.showAlert(title, body)
}

// alertBody turns a failed flash line into the body of its alert. A flash line
// joins the lines of command output with " · " (outputLine), which reads as
// loose fragments in a popup. Here each fragment is a sentence, and a command
// such as "git push <name>" gets a line of its own. The flash line itself keeps
// its separators, since it has to fit on one row.
func alertBody(text string) string {
	if !strings.Contains(text, " · ") {
		return text
	}
	var b strings.Builder
	prevCommand := false
	for _, part := range strings.Split(text, " · ") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		command := isCommandLine(part)
		switch {
		case b.Len() == 0:
		case command:
			b.WriteString("\n  ")
		case prevCommand:
			b.WriteString("\n")
		default:
			b.WriteString(" ")
		}
		b.WriteString(part)
		prevCommand = command
	}
	return b.String()
}

// isCommandLine says whether a fragment of command output is a command to run,
// which is shown on its own line rather than inside a sentence.
func isCommandLine(s string) bool {
	return strings.HasPrefix(s, "git ")
}

func hasAnyPrefix(s string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}
